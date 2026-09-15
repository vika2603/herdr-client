# Herdr Client

Go client for [Herdr](https://herdr.dev), generated against herdr 0.9.0,
protocol 22: the full socket API, a live mirror of the session, and the
pieces a Herdr plugin written in Go needs.

The wire types, the result and event decoders, and a typed wrapper for every
one of the 102 API methods are generated from the schema the herdr binary
prints, so the client tracks the server rather than a hand-written guess of
it. The transport, the session mirror, the graphics frame stream, the plugin
process environment and the manifest parser are hand-written.

When herdr moves, `just herdr-check` reports the drift and `just gen`
regenerates from the new schema, so a protocol bump arrives as a diff to
review rather than as a runtime surprise. See
[Upgrading to a new herdr](#upgrading-to-a-new-herdr).

## Install

```bash
go get github.com/vika2603/herdr-client
```

This is a v0, so the API can still change between minor versions. Pin the
version you build against.

The runtime packages a plugin imports are:

```go
import (
	"github.com/vika2603/herdr-client/herdr"  // the API client and the session mirror
	"github.com/vika2603/herdr-client/plugin" // the environment Herdr injects, and dispatch
)
```

## Connect and call

`NewFromEnv` resolves the socket the way the
herdr CLI does: `HERDR_SOCKET_PATH` first, then the session named by
`HERDR_SESSION`, then the default session.

```go
client, err := herdr.NewFromEnv()
if err != nil {
	return err
}

pong, err := client.Ping(ctx)
if err != nil {
	return err
}
fmt.Printf("herdr %s, protocol %d\n", pong.Version, pong.Protocol)

panes, err := client.PaneList(ctx, herdr.PaneListParams{})
if err != nil {
	return err
}
for _, pane := range panes.Panes {
	fmt.Printf("%s %s\n", pane.PaneID, pane.AgentStatus)
}
```

Each method takes its own params type and returns its own result type. A
method whose params carry nothing takes none, as `Ping` does above.

Optional fields that are not strings are pointers, so that leaving one unset
is distinguishable from sending its zero value. `herdr.Ptr` supplies the
address inline, and `herdr.Value` reads one back:

```go
created, err := client.PaneSplit(ctx, herdr.PaneSplitParams{
	Direction: herdr.SplitDirectionRight,
	Cwd:       herdr.Ptr("/repo"),
})
if err != nil {
	return err
}

_, err = client.PaneSendInput(ctx, herdr.PaneSendInputParams{
	PaneID: created.Pane.PaneID,
	Text:   "go test ./...",
	Keys:   []string{"enter"},
})
```

The server reads one request per connection and closes it afterwards, so every
call dials a fresh connection. A `Client` is safe for concurrent use, performs
no I/O until its first call, and cancels an in-flight call when its context is
done.

## Errors

The server answers a failed request with a code and a message, which arrive as
`*herdr.Error`:

```go
_, err := client.PaneGet(ctx, herdr.PaneTarget{PaneID: "w9:p9"})
if herdr.IsCode(err, herdr.ErrCodePaneNotFound) {
	// the pane is gone
}

var apiErr *herdr.Error
if errors.As(err, &apiErr) {
	log.Println(apiErr.Code, apiErr.Message)
}
```

The codes herdr reports are available as `ErrCode*` constants, and comparing
`Code` against a plain string keeps working for codes a newer server adds.

## Events

`events.subscribe` is the one method that keeps its connection open. It
returns a `*Stream` whose `NextEvent` yields decoded events:

```go
stream, err := client.EventsSubscribe(ctx, herdr.EventsSubscribeParams{
	Subscriptions: []herdr.Subscription{
		herdr.PaneAgentStatusChangedSubscription{PaneID: "w1:p1"},
	},
})
if err != nil {
	return err
}
defer stream.Close()

for {
	event, err := stream.NextEvent(ctx)
	if err != nil {
		return err
	}
	if changed, ok := event.(*herdr.PaneAgentStatusChangedEvent); ok {
		fmt.Println(changed.PaneID, changed.AgentStatus)
	}
}
```

A subscription starts when the server accepts it and does not replay earlier
events. To build a complete picture, open the subscription first, buffer what
arrives, then call `SessionSnapshot` and apply the buffer on top.

## Mirroring a session

A plugin that reacts to what happens across the whole session wants the state,
not just the events. `OpenSession` subscribes first, takes a snapshot, applies
whatever arrived in between, and then keeps the cache current as it hands each
event to the caller:

```go
session, err := herdr.OpenSession(ctx, client)
if err != nil {
	return err
}
defer session.Close()

for {
	event, err := session.Next(ctx)
	if err != nil {
		return err
	}
	if _, ok := event.(*herdr.PaneAgentStatusChangedEvent); ok {
		for _, agent := range session.Agents() {
			fmt.Println(agent.PaneID, agent.AgentStatus)
		}
	}
}
```

The cache is updated before `Next` returns, so reading it afterwards shows the
state that event produced. `Snapshot` reads the whole mirror under one lock,
for a caller that wants one consistent frame rather than a field at a time.
`LayoutPanes` walks a layout tree to its pane leaves, which is how a plugin
learns the ids `layout.apply` assigned. A server restart, which happens on live handoff,
is handled by reconnecting and taking a fresh snapshot; the gap is reported as
one resync event so a caller can drop anything it derived from the old state.

## Writing a plugin

A Herdr plugin is a directory with a `herdr-plugin.toml` manifest and commands
Herdr launches. Herdr injects the invocation context into the environment,
which `plugin.Load` reads:

```go
env, err := plugin.Load()
if err != nil {
	log.Fatal(err)
}

client := env.Client()
log.Println(env.PluginID, env.Kind(), env.StateDir)
```

`Kind` reports which manifest entrypoint started the process: a startup hook,
an action, an event hook or a pane command. One binary usually serves several
of them, so register a handler per entrypoint and let the registry pick:

```go
func main() {
	ctx, stop := plugin.ShutdownContext(context.Background())
	code := newPlugin().Run(ctx)
	stop()
	os.Exit(code)
}

func newPlugin() *plugin.Plugin {
	p := plugin.New()
	p.Startup(onStartup)
	p.Action("show", onShow)
	p.Pane("board", onBoard)
	plugin.OnEvent(p, onStatusChanged)
	return p
}

func onStatusChanged(ctx context.Context, env *plugin.Env, e *herdr.PaneAgentStatusChangedEvent) error {
	return env.AppendStateJSONL("log.jsonl", record{Pane: e.PaneID, Status: string(e.AgentStatus)})
}
```

`OnEvent` takes the event name from the handler's own payload type, so no
event string is written twice. `Run` returns the exit code Herdr records in
its plugin command log: 0 for a handler that returned nil, 1 for one that
returned an error, and 2 when no handler ran at all. `plugin.Run(ctx,
plugin.Handlers{…})` still dispatches by kind for a plugin that wants the
switch itself.

`ShutdownContext` matters for a pane entrypoint, which runs until the user
closes the pane: closing it delivers SIGHUP and then SIGTERM, and the context
ends on either.

`Env.Invocation` reads the invocation context with its optional fields
flattened to values. Durable state belongs under `env.StateDir` and
user-editable configuration under `env.ConfigDir`; `ReadState`, `WriteState`,
their JSON forms and `AppendStateJSONL` address a file by name inside the
state directory and write through a temporary file and a rename, so a crash
mid-write cannot truncate what was there. `ReadConfig` and `ReadConfigJSON`
do the same for the configuration directory, which has no write counterpart
because that directory belongs to the user.

### Testing a plugin

`plugin/plugintest` builds the environment Herdr injects, so a handler test
sets no environment variables and needs no server:

```go
func TestShow(t *testing.T) {
	env := plugintest.Env(plugintest.Action("show"), plugintest.StateDir(t.TempDir()))
	if err := newPlugin().Dispatch(context.Background(), env); err != nil {
		t.Fatal(err)
	}
}

func TestManifest(t *testing.T) {
	plugintest.CheckManifest(t, "herdr-plugin.toml", newPlugin())
}
```

`plugintest.NewServer` adds a socket the test controls, for a handler whose
calls have to be checked as a sequence:

```go
server := plugintest.NewServer(t).
	Reply(herdr.MethodPaneSplit, herdr.PaneInfoResponse{Pane: herdr.PaneInfo{PaneID: "w1:p2"}}).
	Reply(herdr.MethodPaneSendInput, herdr.OKResponse{})

if err := newPlugin().Dispatch(ctx, server.Env(plugintest.Action("run"))); err != nil {
	t.Fatal(err)
}
// server.Methods() and server.Calls() report what the handler asked for.
```

The protocol server is also available as `herdrtest.NewServer(t)` for programs
that use the client without being plugins. `plugintest.Server` adapts that
server to a plugin `Env`; both exercise the real client's encoding, local IPC,
response decoding and event handling. Neither needs a Herdr binary, an agent
process or a terminal UI. Socket tests skip on Windows because this test
server does not implement a named-pipe listener.

`Reply` captures a fixed result and answers every call with it. `Fail` scripts
an API error. `Handle` computes a result for each call, so it can inspect
parameters, return different results, or hold a snapshot response until a test
releases it:

```go
server.Handle(herdr.MethodSessionSnapshot,
    func(ctx context.Context, call herdrtest.Call) (herdr.Result, error) {
        select {
        case <-releaseSnapshot:
            return herdr.SessionSnapshotResponse{Snapshot: snapshot}, nil
        case <-ctx.Done():
            return nil, ctx.Err()
        }
    })
```

Handlers can run concurrently. Synchronize shared state, and observe their
context when blocking: it ends when the client disconnects or the server
closes. A `*herdr.Error` preserves its API code; other errors return
`internal_error`. A handler returning neither a result nor an error fails the
test as a scripting mistake. An unscripted method returns `invalid_request`
naming the method; assert on the client error or recorded calls rather than
expecting it to fail the test automatically.

### Testing subscriptions and session mirrors

`AllowSubscriptions` acknowledges `events.subscribe`. `WaitSubscription`
returns a handle for each accepted stream, including reconnects. The following
uses the same `OpenSession` and decoding path a plugin uses:

```go
server := herdrtest.NewServer(t).
    AllowSubscriptions().
    Reply(herdr.MethodSessionSnapshot, herdr.SessionSnapshotResponse{})

session, err := herdr.OpenSession(ctx, server.Client())
if err != nil {
    t.Fatal(err)
}
defer session.Close()

subscription, err := server.WaitSubscription(ctx, 0)
if err != nil {
    t.Fatal(err)
}
err = subscription.Send(ctx, herdr.WorkspaceCreatedEvent{
    Workspace: herdr.WorkspaceInfo{WorkspaceID: "w1", Label: "project"},
})
if err != nil {
    t.Fatal(err)
}
if _, err := session.Next(ctx); err != nil {
    t.Fatal(err)
}
// session.Workspaces() now includes w1.
```

`WaitCall(ctx, index)` waits for a request, and `WaitSubscription(ctx, index)`
waits for a subscription acknowledgement to be written. Indices are zero-based;
waits observe records without consuming them. Multiple waiters can observe the
same request or subscription. Use a bounded context and channels to arrange
request/response timing, not fixed sleeps.

`Send` writes a typed event in its wire format. `SendRaw` can inject an unknown
event name or a mismatched payload. The server does not filter sent events by
the subscriptions requested, emulate Herdr's business rules, or maintain a
session snapshot automatically: the test owns those responses. A successful
send means the bytes were written, not that the plugin processed them. Wait on
observable plugin output or another explicit application signal before
asserting on a frame. `Session` itself applies an event when `Next` returns it.

Close a subscription to simulate a disconnect. Set up the new snapshot before
closing it, then drive the client's next read and wait for subscription index 1
to observe the reconnect. `Subscription.Done()` reports a closed connection.
`Server.Close()` cancels handlers, closes active and partially read connections,
waits for server goroutines, and removes its socket directory; test cleanup does
this automatically. A handler must not call `Server.Close()` itself, because
close waits for handlers to return. Caller-owned goroutines still belong to the
test: cancel and join them before cleanup completes.

The `agent-board` example tests its full registered pane entrypoint this way,
with output written to a test recorder. Those tests cover bootstrap, live
updates, resync, cancellation and errors; they do not establish terminal
rendering quality or real-server compatibility. Graphics streaming and a real
Herdr harness are outside this test server's API.

### Checking dispatch and manifests

`Dispatch` runs the same selection `Run` does and returns the handler's error
instead of an exit code. `CheckManifest` reports every disagreement between
the manifest and the code: an id declared with no handler, a handler with no
manifest entry, a handler for an event Herdr never fires a hook for, and
every rule and warning herdr itself produces when it loads the manifest.

`plugin/manifest` is that parser on its own, for tooling that has no registry:

```go
m, warnings, err := manifest.Parse("herdr-plugin.toml")
```

Warnings are returned separately from errors, matching herdr: an event hook
naming an event that herdr never fires for hooks is a warning, a duplicate
action id is an error. `manifest.HookEventNames` lists the events that are
eligible.

`examples/` holds worked plugins built on all of this:

| Example                 | Shows                                                                        |
| ----------------------- | ---------------------------------------------------------------------------- |
| `examples/agent-status`  | A startup hook, an event hook and an action in one binary, over plugin state |
| `examples/agent-board`   | A pane entrypoint on the session mirror, redrawn until the pane closes       |
| `examples/worktree-bootstrap` | A link handler that opens a worktree, lays out its panes and starts an agent |

## Layout

| Path                                | Contents                                                                 |
| ----------------------------------- | ------------------------------------------------------------------------ |
| `herdr`                             | Transport, plus the generated types, results, events and method wrappers |
| `plugin`                            | The environment Herdr injects into plugin commands, and the registry     |
| `plugin/manifest`                   | `herdr-plugin.toml` parsing and validation                               |
| `herdrtest`                         | Protocol test server, dynamic responses, subscriptions and synchronization |
| `plugin/plugintest`                 | Plugin environment and manifest checks, plus protocol server adaptation |
| `examples`                          | Worked plugins                                                           |
| `cmd/herdr-api-gen`, `internal/gen` | The generator that produces `*_gen.go`                                   |
| `internal/e2e`                      | The suite that proves the result types against a real server             |
| `internal/cmd/herdrcheck`           | The drift report `just herdr-check` runs                                 |
| `schema`                            | The schema snapshot and the method-to-result table                       |
| `docs/design.md`                    | Protocol facts, generation rules and the development plan                |

## Upgrading to a new herdr

The `Track herdr` workflow does the mechanical part on a schedule: it
downloads the newest herdr release from `herdrdev/herdr`, rewrites the
snapshot from that binary, regenerates, runs the e2e suite against a server it
starts from it, and opens a pull request carrying the drift report and the
review items the schema cannot settle. Nothing about it depends on the herdr a
maintainer has installed.

GitHub does not run workflows for events its own token produced, so the CI run
on that pull request is created but never executed and its red mark reports
nothing. The workflow therefore runs everything `just check` covers and reports
the results in the body. A commit pushed to the branch by hand starts CI on
it.

A release that leaves `schema/herdr-api.schema.json` byte for byte what it was
cannot change the generated code, so the recorded version is the whole of the
diff; the workflow merges that pull request itself once every check has passed.
Any change to the schema waits for a reviewer. Tagging a release of this module
is not automated.

The commands below are the same upgrade run locally, against the installed
binary.

```bash
just herdr-check     # report what moved before changing anything
just schema-update   # rewrite schema/herdr-api.schema.json from the installed herdr
just gen             # regenerate *_gen.go
just check           # build, test, lint, cross-platform type-check, and verify the generated code is current
just e2e             # confirm the result types against a server the suite starts
```

`just herdr-check` compares the installed binary's schema and the running
server against the snapshot: the protocol number, methods and types that
differ, and methods the server accepts without declaring them. It exits
non-zero on anything `schema/known-gaps.json` does not already account for.

`schema/method-results.json` records which result type each method returns,
which the schema itself does not state. Add an entry for any new method; the
generator refuses to run while one is missing. `schema/README.md` has the
details.

Generated code ignores fields it does not know, and an unrecognised result or
event type surfaces as an error rather than a panic, so a newer server does
not break a client outright. Compare the `Protocol` in a `Ping` response with
`herdr.SchemaProtocol` to detect one.

Behaviour the schema does not describe, such as how socket paths resolve or
what a plugin manifest may contain, was read out of the herdr sources. The
upgrade checklist in `docs/design.md` lists each of those facts with the file
it came from, because nothing regenerates them.
