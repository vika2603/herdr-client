//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vika2603/herdr-client/herdr"
)

const (
	// sessionName is the named session the suite creates under its own
	// XDG_CONFIG_HOME. It never resolves to the caller's default session.
	// It is short because it is part of the socket path.
	sessionName = "e2e"

	// defaultSession names the session whose socket sits directly in the
	// config directory. Resolving it is how the harness finds the config
	// directory of whoever runs the suite.
	defaultSession = "default"

	// pluginRegistryFile is the file herdr records linked plugins in,
	// relative to the config directory.
	pluginRegistryFile = "plugins.json"

	startTimeout = 30 * time.Second
	stopTimeout  = 15 * time.Second
	callTimeout  = 20 * time.Second

	// sunPathMax is the shortest sun_path capacity among the supported
	// systems (macOS: 104 bytes including the terminator).
	sunPathMax = 103
)

// harness owns the server the suite talks to: a temporary XDG_CONFIG_HOME,
// one `herdr --session <name> server` process, and the clients that reach it.
type harness struct {
	root       string
	configHome string
	socketPath string
	repo       string

	binary  string
	git     string
	cmd     *exec.Cmd
	logPath string
	waited  chan error

	client  *herdr.Client
	trigger *herdr.Client

	rec      *recorder
	registry registryState

	mu        sync.Mutex
	seq       int
	lastReqID string

	version  string
	protocol uint32
}

// registryState is a plugin registry as it stood at one moment: whether the
// file existed and what it held.
type registryState struct {
	path    string
	exists  bool
	content []byte
}

// tempBase is the directory the temporary root is created in. On Unix it is
// /tmp rather than the per-user temporary directory, which is long enough on
// macOS to push the server sockets past sun_path.
func tempBase() string {
	if runtime.GOOS == "windows" {
		return ""
	}
	return "/tmp"
}

// callerRegistry reads the plugin registry of the config directory in
// effect. It has to run before XDG_CONFIG_HOME is redirected at the temporary
// root. ResolveSocketPath answers <config>/herdr.sock for the default
// session, so its directory is the config directory, found by the same rules
// herdr follows.
func callerRegistry() (registryState, error) {
	socket, err := herdr.ResolveSocketPath(defaultSession)
	if err != nil {
		return registryState{}, err
	}
	return readRegistry(filepath.Join(filepath.Dir(socket), pluginRegistryFile))
}

func readRegistry(path string) (registryState, error) {
	content, err := os.ReadFile(path)
	switch {
	case err == nil:
		return registryState{path: path, exists: true, content: content}, nil
	case errors.Is(err, fs.ErrNotExist):
		return registryState{path: path}, nil
	default:
		return registryState{path: path}, err
	}
}

// newHarness prepares the temporary directories, resolves the socket path
// through the transport's own resolver and starts the server.
func newHarness() (*harness, error) {
	registry, err := callerRegistry()
	if err != nil {
		return nil, fmt.Errorf("locate the plugin registry of the caller: %w", err)
	}

	binary, err := exec.LookPath("herdr")
	if err != nil {
		return nil, fmt.Errorf("herdr binary not found in PATH: %w", err)
	}
	gitBinary, err := exec.LookPath("git")
	if err != nil {
		return nil, fmt.Errorf("git binary not found in PATH: %w", err)
	}

	// The short base and prefix keep the sockets the server creates inside
	// sun_path: on macOS the per-user temporary directory alone is 49 bytes.
	root, err := os.MkdirTemp(tempBase(), "he2e")
	if err != nil {
		return nil, err
	}
	// The server reports resolved paths, so the suite compares against
	// resolved ones. On macOS the temporary directory sits behind a symlink.
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	h := &harness{
		root: root,
		// The temporary root doubles as XDG_CONFIG_HOME, which keeps the
		// socket path short; herdr only creates the herdr subdirectory
		// there.
		configHome: root,
		repo:       filepath.Join(root, "repo"),
		binary:     binary,
		git:        gitBinary,
		logPath:    filepath.Join(root, "server-stdout.log"),
		rec:        newRecorder(),
		registry:   registry,
	}
	for _, dir := range []string{filepath.Join(root, "state"), filepath.Join(root, "data"), filepath.Join(root, "cache"), filepath.Join(root, "tmp"), h.home()} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return h, err
		}
	}

	// The suite must never inherit a socket path or a session name from the
	// caller, and ResolveSocketPath reads the same variables.
	for _, name := range []string{"HERDR_SOCKET_PATH", "HERDR_SESSION"} {
		if err := os.Unsetenv(name); err != nil {
			return h, err
		}
	}
	if err := os.Setenv("XDG_CONFIG_HOME", h.configHome); err != nil {
		return h, err
	}
	socketPath, err := herdr.ResolveSocketPath(sessionName)
	if err != nil {
		return h, err
	}
	h.socketPath = socketPath
	// The server also creates herdr-client.sock beside the API socket, which
	// is the longest name it binds there.
	longest := filepath.Join(filepath.Dir(socketPath), "herdr-client.sock")
	if runtime.GOOS != "windows" && len(longest) > sunPathMax {
		return h, fmt.Errorf("the server would bind %s, %d bytes, over the %d the system allows",
			longest, len(longest), sunPathMax)
	}

	h.client = herdr.New(socketPath, herdr.WithRequestIDs(h.nextRequestID), herdr.WithDialTimeout(5*time.Second))
	var triggerSeq int
	h.trigger = herdr.New(socketPath, herdr.WithRequestIDs(func() string {
		triggerSeq++
		return fmt.Sprintf("e2e-trigger-%d", triggerSeq)
	}), herdr.WithDialTimeout(5*time.Second))

	if err := h.writeConfig(); err != nil {
		return h, err
	}
	if err := h.initRepo(); err != nil {
		return h, err
	}
	if err := h.startServer(); err != nil {
		return h, err
	}
	return h, nil
}

// nextRequestID hands the transport the ids the report refers to.
func (h *harness) nextRequestID() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.seq++
	h.lastReqID = fmt.Sprintf("e2e-%d", h.seq)
	return h.lastReqID
}

// requestID returns the id of the most recent call made through h.client.
func (h *harness) requestID() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.lastReqID
}

// home is the temporary HOME the server runs under.
func (h *harness) home() string { return filepath.Join(h.root, "home") }

// serverEnv builds the child environment: the temporary XDG directories, a
// temporary HOME and TMPDIR, and no HERDR_ variable from the caller.
func (h *harness) serverEnv() []string {
	overridden := map[string]bool{
		"HOME":            true,
		"XDG_CONFIG_HOME": true,
		"XDG_STATE_HOME":  true,
		"XDG_DATA_HOME":   true,
		"XDG_CACHE_HOME":  true,
		"TMPDIR":          true,
		"EDITOR":          true,
		"VISUAL":          true,
	}
	env := make([]string, 0, len(os.Environ())+7)
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if overridden[name] || strings.HasPrefix(name, "HERDR_") {
			continue
		}
		env = append(env, entry)
	}
	return append(env,
		// Agent integrations are written under the user's own home, not
		// under XDG_CONFIG_HOME, so redirecting HOME is what keeps
		// integration.install off the caller's machine. It also moves the
		// default worktree location, ~/.herdr/worktrees, inside the root.
		"HOME="+h.home(),
		"XDG_CONFIG_HOME="+h.configHome,
		// The agent detection manifests are cached in the state directory.
		// Pointing it at the temporary root keeps the suite off the caller's
		// cache and makes the manifest methods answer from the bundled set.
		"XDG_STATE_HOME="+filepath.Join(h.root, "state"),
		"XDG_DATA_HOME="+filepath.Join(h.root, "data"),
		"XDG_CACHE_HOME="+filepath.Join(h.root, "cache"),
		"TMPDIR="+filepath.Join(h.root, "tmp"),
		// pane.edit_scrollback opens ${EDITOR} on a scrollback file in a new
		// pane and deletes the file when the editor exits.
		"EDITOR=true",
		"VISUAL=true",
	)
}

// writeConfig gives the server a configuration of its own. The panes run a
// non-login /bin/sh so that starting one does not depend on the interactive
// shell configuration of whoever runs the suite: a slow or blocking shell
// startup shows up as text written to a pane getting lost.
func (h *harness) writeConfig() error {
	dir := filepath.Join(h.configHome, "herdr")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	config := "[terminal]\nshell_mode = \"non_login\"\n"
	if runtime.GOOS != "windows" {
		config += "default_shell = \"/bin/sh\"\n"
	}
	return os.WriteFile(filepath.Join(dir, "config.toml"), []byte(config), 0o600)
}

func (h *harness) startServer() error {
	logFile, err := os.Create(h.logPath)
	if err != nil {
		return err
	}
	defer func() { _ = logFile.Close() }()

	cmd := exec.Command(h.binary, "--session", sessionName, "server")
	cmd.Dir = h.root
	cmd.Env = h.serverEnv()
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		return err
	}
	h.cmd = cmd
	h.waited = make(chan error, 1)
	go func() { h.waited <- cmd.Wait() }()

	return h.waitReady()
}

// waitReady polls ping until the server answers or the process gives up.
func (h *harness) waitReady() error {
	deadline := time.Now().Add(startTimeout)
	var lastErr error
	for time.Now().Before(deadline) {
		select {
		case err := <-h.waited:
			h.waited = nil
			return fmt.Errorf("server exited before it was ready: %w; output: %s", err, h.serverLog())
		default:
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		pong, err := h.client.Ping(ctx)
		cancel()
		if err == nil {
			h.version = pong.Version
			h.protocol = pong.Protocol
			return nil
		}
		lastErr = err
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("server did not answer ping within %s: %w; output: %s", startTimeout, lastErr, h.serverLog())
}

// stop asks the server to stop, waits for the process and kills it when it
// does not exit. It is safe to call after the suite already stopped it.
func (h *harness) stop() {
	if h.cmd == nil || h.waited == nil {
		return
	}
	select {
	case <-h.waited:
		h.waited = nil
		return
	default:
	}

	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	_, _ = h.client.ServerStop(ctx)
	cancel()

	select {
	case <-h.waited:
	case <-time.After(stopTimeout):
		_ = h.cmd.Process.Kill()
		<-h.waited
	}
	h.waited = nil
}

func (h *harness) cleanup() {
	h.stop()
	if h.root != "" {
		_ = os.RemoveAll(h.root)
	}
}

func (h *harness) serverLog() string {
	data, err := os.ReadFile(h.logPath)
	if err != nil {
		return "no server output: " + err.Error()
	}
	return strings.TrimSpace(string(data))
}

// initRepo creates the git repository the worktree methods operate on. The
// git configuration files of the caller are ignored so the repository is
// reproducible.
func (h *harness) initRepo() error {
	if err := os.MkdirAll(h.repo, 0o700); err != nil {
		return err
	}
	readme := filepath.Join(h.repo, "README.md")
	if err := os.WriteFile(readme, []byte("herdr-client end-to-end fixture\n"), 0o600); err != nil {
		return err
	}
	commands := [][]string{
		{"init", "--initial-branch=main"},
		{"add", "README.md"},
		{"commit", "--message=initial"},
	}
	for _, args := range commands {
		if err := h.runGit(args...); err != nil {
			return err
		}
	}
	return nil
}

// addWorktree checks out branch at path so worktree.open has an existing
// checkout to adopt.
func (h *harness) addWorktree(path, branch string) error {
	return h.runGit("worktree", "add", "-b", branch, path)
}

func (h *harness) runGit(args ...string) error {
	full := append([]string{
		"-c", "user.name=herdr-client e2e",
		"-c", "user.email=e2e@example.invalid",
		"-c", "commit.gpgsign=false",
	}, args...)
	cmd := exec.Command(h.git, full...)
	cmd.Dir = h.repo
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_CONFIG_SYSTEM="+os.DevNull,
		"GIT_TERMINAL_PROMPT=0",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// ctx returns a context for one call.
func (h *harness) ctx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	t.Cleanup(cancel)
	return ctx
}

// cover asserts that a wrapper answered with the result type
// schema/method-results.json documents. The method counts as exercised as
// soon as the server answered with a result type, so a type the table does
// not allow is recorded both as coverage and as a disagreement; only a
// failed call leaves the method uncovered.
func (h *harness) cover(t *testing.T, method string, result herdr.Result, err error) bool {
	t.Helper()
	requestID := h.requestID()
	want := h.expect(t, method)

	if err != nil {
		var unexpected *herdr.UnexpectedResultError
		var unknown *herdr.UnknownResultError
		switch {
		case errors.As(err, &unexpected):
			h.disagree(method, requestID, want, unexpected.Got,
				"the wrapper rejected the result type before the response was decoded")
		case errors.As(err, &unknown):
			h.disagree(method, requestID, want, unknown.Type, string(unknown.Data))
		default:
			t.Errorf("%s (request %s): %v", method, requestID, err)
			return false
		}
		t.Errorf("%s (request %s): %v", method, requestID, err)
		return false
	}
	if result == nil {
		t.Errorf("%s (request %s): no error and no result", method, requestID)
		return false
	}

	got := result.ResultType()
	h.rec.record(method, call{requestID: requestID, resultType: got})
	if !want.allows(got) {
		h.disagree(method, requestID, want, got, marshal(result))
		t.Errorf("%s (request %s): result type %q, table says %s", method, requestID, got, want)
		return false
	}
	return true
}

// disagree records a response that contradicts schema/method-results.json.
func (h *harness) disagree(method, requestID string, want expectation, got, response string) {
	h.rec.record(method, call{requestID: requestID, resultType: got})
	h.rec.reportDisagreement(disagreement{
		method: method, requestID: requestID,
		want: want.String(), got: got, response: response,
	})
}

// coverStream asserts the acknowledging result of a method that keeps its
// connection open.
func (h *harness) coverStream(t *testing.T, method string, stream *herdr.Stream, err error) bool {
	t.Helper()
	requestID := h.requestID()
	if err != nil {
		t.Errorf("%s (request %s): %v", method, requestID, err)
		return false
	}
	ack, err := herdr.DecodeResult(stream.Ack())
	if err != nil {
		var unknown *herdr.UnknownResultError
		if errors.As(err, &unknown) {
			h.disagree(method, requestID, h.expect(t, method), unknown.Type, string(unknown.Data))
		}
		t.Errorf("%s (request %s): decode acknowledgement %s: %v", method, requestID, stream.Ack(), err)
		return false
	}
	return h.cover(t, method, ack, nil)
}

func (h *harness) expect(t *testing.T, method string) expectation {
	t.Helper()
	want, ok := table[method]
	if !ok {
		t.Fatalf("%s is not listed in %s", method, methodsPath)
	}
	return want
}

// assertCallerRegistryUnchanged fails when the plugin registry outside the
// harness differs from what it held before the server started. The suite
// links a fixture plugin, and only the registry under its own
// XDG_CONFIG_HOME may record it. Existence and content are compared rather
// than the modification time: a running herdr rewrites that file with
// identical content, which moves the timestamp although nothing changed.
func (h *harness) assertCallerRegistryUnchanged(t *testing.T) {
	t.Helper()
	now, err := readRegistry(h.registry.path)
	if err != nil {
		t.Fatalf("read the plugin registry of the caller %s: %v", h.registry.path, err)
	}
	switch {
	case now.exists != h.registry.exists:
		t.Fatalf("the suite changed whether the plugin registry of the caller %s exists: %t before the run, %t now",
			h.registry.path, h.registry.exists, now.exists)
	case !bytes.Equal(now.content, h.registry.content):
		t.Fatalf("the suite changed the plugin registry of the caller %s: %d bytes before the run, %d now",
			h.registry.path, len(h.registry.content), len(now.content))
	}
}

func marshal(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprintf("%+v", value)
	}
	return string(data)
}
