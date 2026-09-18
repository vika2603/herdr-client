package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// checkout writes files into a herdr-shaped tree and returns its root. Keys
// are paths under src/.
func checkout(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		path := filepath.Join(root, "src", filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func read(t *testing.T, root string) []source {
	t.Helper()
	files, err := rustFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// Every dispatch shape herdr uses, each answering one method, plus an inline
// test module of the kind Rust keeps beside the code it tests.
const dispatchSource = `
#[derive(Deserialize)]
#[serde(tag = "method", content = "params")]
pub enum Method {
    #[serde(rename = "ping")]
    Ping(PingParams),
    #[serde(rename = "pane.link.resolve")]
    PaneLinkResolve(PaneLinkParams),
    #[serde(rename = "workspace.create")]
    WorkspaceCreate(WorkspaceCreateParams),
    #[serde(rename = "tab.close")]
    TabClose(TabTarget),
    // A comment between the rename and the variant must not hide it.
    #[serde(rename = "client_shell.surface.set")]
    ClientShellSurfaceSet(SurfaceParams),
    #[serde(rename = "worktree.create")]
    WorktreeCreate(WorktreeCreateParams),
}

fn dispatch(&mut self, request: Request) -> String {
    if matches!(&request.method, Method::Ping(_)) {
        return encode_success(request.id, ResponseResult::Pong { protocol: 22 });
    }
    match request.method {
        Method::PaneLinkResolve(params) => {
            return self.handle_pane_link_resolve(request.id, params);
        }
        Method::WorkspaceCreate(params) => return self.handle_workspace_create(request.id, params),
        Method::TabClose(_) => {
            return encode_success(request.id, ResponseResult::Ok {});
        }
        Method::WorktreeCreate(params) => return self.start_worktree_create(request.id, params),
        _ => encode_error(request.id, "invalid_request", "unknown"),
    }
}

fn endpoint(&mut self, request: Request) -> String {
    if let Method::ClientShellSurfaceSet(params) = &request.method {
        return encode_success(request.id, ResponseResult::ClientShellSurfaceSet { ok: true });
    }
    String::new()
}

fn handle_pane_link_resolve(&mut self, id: String, params: PaneLinkParams) -> String {
    match self.read_link(&params) {
        Ok(regions) => encode_success(id, ResponseResult::PaneLinkResolved { regions }),
        Err(error) => error,
    }
}

// The result is built two calls away from the dispatch.
fn handle_workspace_create(&mut self, id: String, params: WorkspaceCreateParams) -> String {
    match self.create(params) {
        Ok(index) => encode_success(id, self.workspace_created_result(index)),
        Err(err) => encode_error(id, "workspace_create_failed", err.to_string()),
    }
}

fn workspace_created_result(&self, index: usize) -> ResponseResult {
    ResponseResult::WorkspaceCreated { workspace: self.info(index) }
}

// The dispatch starts the work; a completion callback answers, and nothing
// connects the two but an operation id.
fn start_worktree_create(&mut self, id: String, params: WorktreeCreateParams) -> String {
    self.pending.insert(self.next_operation_id(), id);
    String::new()
}

fn handle_worktree_add_finished(&mut self, op: u64) {
    let id = self.pending.remove(&op).unwrap();
    send(encode_success(id, ResponseResult::WorktreeCreated { worktree }));
}

#[cfg(test)]
mod tests {
    use super::*;

    // A test that names one method and one result is the pairing evidence.
    #[test]
    fn pane_link_resolve_round_trips() {
        let request = Method::PaneLinkResolve(PaneLinkParams::default());
        let result = ResponseResult::PaneLinkResolved { regions: vec![] };
        assert_eq!(json(&result)["type"], "pane_link_resolved");
    }

    // This one sets up with a method and asserts on something else, which
    // reads the same way and is why a pairing never decides an entry.
    #[test]
    fn closing_a_tab_leaves_the_workspace() {
        let request = Method::TabClose(TabTarget::default());
        let result = ResponseResult::WorkspaceCreated { workspace };
    }
}
`

func TestMethodNamesReadsTheEnumInOrder(t *testing.T) {
	files := read(t, checkout(t, map[string]string{"api/schema.rs": dispatchSource}))
	variants, err := methodNames(files)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"Ping", "PaneLinkResolve", "WorkspaceCreate", "TabClose", "ClientShellSurfaceSet", "WorktreeCreate"}
	if len(variants) != len(want) {
		t.Fatalf("read %d variants, want %d: %+v", len(variants), len(want), variants)
	}
	for i, name := range want {
		if variants[i].variant != name {
			t.Errorf("variant %d is %q, want %q", i, variants[i].variant, name)
		}
	}
	if variants[1].method != "pane.link.resolve" {
		t.Errorf("PaneLinkResolve serialises as %q", variants[1].method)
	}
}

func TestResultsForReadsEachDispatchShape(t *testing.T) {
	files := read(t, checkout(t, map[string]string{"api/schema.rs": dispatchSource}))
	index := functions(files)
	declared := map[string]bool{
		"pong": true, "pane_link_resolved": true, "workspace_created": true,
		"ok": true, "client_shell_surface_set": true, "worktree_created": true,
	}

	for _, tc := range []struct{ variant, want string }{
		{"Ping", "pong"}, // behind a matches! guard
		{"PaneLinkResolve", "pane_link_resolved"},             // one call from the arm
		{"WorkspaceCreate", "workspace_created"},              // two calls from the arm
		{"TabClose", "ok"},                                    // encoded in the arm itself
		{"ClientShellSurfaceSet", "client_shell_surface_set"}, // bound by an if let
	} {
		got := resultsFor(tc.variant, files, index).results(declared)
		if len(got) != 1 || got[0] != tc.want {
			t.Errorf("%s resolved to %v, want [%s]", tc.variant, got, tc.want)
		}
	}

	// A deferred handler answers from a callback the dispatch never calls, so
	// the scan has to say it found nothing rather than reach for the result
	// that happens to be nearby.
	deferred := resultsFor("WorktreeCreate", files, index)
	if len(deferred.results(declared)) != 0 {
		t.Errorf("WorktreeCreate resolved to %v, want nothing", deferred.results(declared))
	}
	if deferred.why != reasonNoResult {
		t.Errorf("the reason given is %q, want the deferred-handler one", deferred.why)
	}
}

// A method named only by an inline test must not read as dispatch.
func TestDispatchIgnoresTestModules(t *testing.T) {
	files := read(t, checkout(t, map[string]string{"api/schema.rs": `
pub enum Method {
    #[serde(rename = "tab.close")]
    TabClose(TabTarget),
}

#[cfg(test)]
mod tests {
    #[test]
    fn closing_a_tab_answers_ok() {
        let request = Method::TabClose(TabTarget::default());
        assert_eq!(result, ResponseResult::Ok {});
    }
}
`}))
	got := resultsFor("TabClose", files, functions(files))
	if len(got.findings) != 0 {
		t.Errorf("a test module was read as dispatch: %+v", got.findings)
	}
	if got.why != reasonNoDispatch {
		t.Errorf("the reason given is %q, want the no-dispatch one", got.why)
	}
}

// A helper shared by two methods encodes both results, which the caller has
// to see rather than have picked for it.
func TestResultsForReportsEveryReachableResult(t *testing.T) {
	files := read(t, checkout(t, map[string]string{"api/schema.rs": `
pub enum Method {
    #[serde(rename = "plugin.enable")]
    PluginEnable(PluginTarget),
}

fn dispatch(&mut self, request: Request) -> String {
    match request.method {
        Method::PluginEnable(params) => return self.set_plugin_enabled(request.id, params, true),
    }
}

fn set_plugin_enabled(&mut self, id: String, params: PluginTarget, on: bool) -> String {
    if on {
        return encode_success(id, ResponseResult::PluginEnabled { plugin });
    }
    encode_success(id, ResponseResult::PluginDisabled { plugin })
}
`}))
	got := resultsFor("PluginEnable", files, functions(files)).results(map[string]bool{
		"plugin_enabled": true, "plugin_disabled": true,
	})
	sort.Strings(got)
	if strings.Join(got, ",") != "plugin_disabled,plugin_enabled" {
		t.Errorf("resolved to %v, want both plugin_enabled and plugin_disabled", got)
	}
}

// Blanking the test modules has to leave every byte offset alone, or the
// line numbers the report cites point at the wrong code.
func TestProductionKeepsLineNumbers(t *testing.T) {
	files := read(t, checkout(t, map[string]string{"api/schema.rs": dispatchSource}))
	file := files[0]
	if len(file.production()) != len(file.text) || len(file.tests()) != len(file.text) {
		t.Fatal("blanking changed the file length")
	}
	marker := "fn handle_worktree_add_finished"
	if lineAt(file.production(), strings.Index(file.production(), marker)) != lineAt(file.text, strings.Index(file.text, marker)) {
		t.Error("a line number taken from the production view does not match the file")
	}
	if strings.Contains(file.production(), "round_trips") {
		t.Error("the test module survived in the production view")
	}
	if !strings.Contains(file.tests(), "round_trips") || strings.Contains(file.tests(), "fn dispatch") {
		t.Error("the test view does not hold the tests alone")
	}
}

// Braces inside strings, comments and character literals must not shift the
// brace count that delimits a body.
func TestBalancedIgnoresNonCode(t *testing.T) {
	src := `fn f() {
    let brace = '{';
    let text = "} not the end {";
    let raw = r#"} still not it"#;
    /* } nested /* } */ comment */
    // } line comment
    if x { let _ = 1; }
    let closing = '}';
}
tail`
	open := strings.IndexByte(src, '{')
	body, end, ok := blockAt(src, open)
	if !ok {
		t.Fatal("the body was not closed")
	}
	if !strings.Contains(body, "let closing") {
		t.Errorf("the body stopped early: %q", body)
	}
	if strings.TrimSpace(src[end:]) != "tail" {
		t.Errorf("the body ended at %q, want the whole function", src[end:])
	}
}

func TestSnakeCase(t *testing.T) {
	for variant, want := range map[string]string{
		"Ok":                    "ok",
		"PaneLinkResolved":      "pane_link_resolved",
		"ClientShellSurfaceSet": "client_shell_surface_set",
	} {
		if got := snakeCase(variant); got != want {
			t.Errorf("snakeCase(%q) = %q, want %q", variant, got, want)
		}
	}
}
