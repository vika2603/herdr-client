# Design

This document describes how `github.com/vika2603/herdr-client` is built and
why, and what is still missing. Every statement about the protocol is backed
by `schema/herdr-api.schema.json` (herdr 0.9.0, protocol 22), by the herdr
v0.9.0 sources, or by a test in this repository.

## What the module is

A Go client for Herdr. It carries the whole socket API, a cache that mirrors a
live session, and the pieces a Herdr plugin written in Go needs. It began as a
wrapper over the API, which is where the original name herdr-api came from,
and was renamed once it grew past that.

Most of the surface is generated from the schema the herdr binary prints, so
the client follows the server rather than a hand-written guess of it. Only
what the schema cannot express is hand-written: the transport, the session
mirror, the plugin process environment, the manifest parser, and four unions
that carry no discriminator.

## Verified protocol facts

- Transport is newline-delimited JSON over a local socket. On Unix it is a Unix
  domain socket. On Windows it is a named pipe: `HERDR_SOCKET_PATH` holds a
  file path and the pipe name is `\\.\pipe\` followed by that path.
- The server reads exactly one request line per connection, writes one
  response line, and closes the connection (`src/api/server.rs`,
  `handle_connection`). Ordinary calls therefore dial a fresh connection each
  time; connections cannot be reused or pipelined.
- The server looks for the request line as soon as it accepts the connection
  and, finding nothing there, waits out a poll interval before looking again.
  Measured against herdr 0.9.0, a request that arrives more than roughly
  0.2 ms after the connection opens is answered about 100 ms later, while one
  that is already waiting is answered in under a millisecond. This is why
  every request here is encoded before the connection is dialed.
- `events.subscribe` keeps the connection open after answering
  `{"type":"subscription_started"}` and pushes one event per line. Writing
  anything further on that connection makes the server close it.
  `pane.graphics.stream` also keeps its connection open but is absent from the
  schema and is out of scope for this iteration.
- Pushed events use two envelopes, distinguished by the `event` field:
  - Lifecycle events: `{"event":"pane_created","data":{"type":"pane_created",...}}`.
    `event` is an `EventKind` (underscore form) and `data` is discriminated by
    `type`. This is the `event` section of the schema.
  - The three dedicated subscriptions:
    `{"event":"pane.output_matched","data":{...}}`. `event` is the dotted name
    and `data` has no discriminator. This is the `subscription_event` section.
- Error responses are `{"id":"...","error":{"code":"...","message":"..."}}`.
  When the request itself cannot be parsed, `id` is the empty string. Observed
  codes are listed under "Error codes".
- For plugin event hooks, `HERDR_PLUGIN_EVENT` is the dotted event name
  (`EventKind::dot_name`) and `HERDR_PLUGIN_EVENT_JSON` is the full
  `EventEnvelope`. `HERDR_PLUGIN_CONTEXT_JSON` is a `PluginInvocationContext`.
- The schema does not record which `ResponseResult` variant a method returns.
  That relation is maintained in `schema/method-results.json`, derived from the
  v0.9.0 handler sources.

## Layout

| Path | Contents | Written by |
| --- | --- | --- |
| `herdr/client.go` `stream.go` `dial.go` `dial_*.go` `socketpath.go` `errors.go` `ptr.go` | Transport, error codes, pointer helpers | hand |
| `herdr/subscribe.go` `session.go` | Typed event stream, live session mirror | hand |
| `herdr/graphics.go` | The `pane.graphics.stream` frame stream | hand |
| `herdr/layout.go` | Walking an applied layout to its panes | hand |
| `herdr/unions_manual.go` | The four unions without a discriminator | hand |
| `herdr/*_gen.go` | Types, results, events, and a wrapper per method | generated |
| `cmd/herdr-api-gen` `internal/gen` | The generator | hand |
| `internal/cmd/herdrcheck` | Drift detection against the installed herdr | hand |
| `plugin` `plugin/manifest` | Plugin environment, registry, manifest | hand |
| `herdrtest` | Protocol test server, dynamic responses and event streams | hand |
| `internal/testsocket` | Shared test listener and connection lifecycle | hand |
| `plugin/plugintest` | Plugin environment, manifest checks and protocol server adaptation | hand |
| `examples/agent-status` | A worked plugin serving three entrypoint kinds | hand |
| `examples/agent-board` | A pane entrypoint on the session mirror | hand |
| `examples/worktree-bootstrap` | A link handler that opens a ready workspace | hand |
| `internal/e2e` | The suite that exercises the API against a real server | hand |
| `schema` | The snapshot, the method result table, the accepted gaps | recorded |

## Transport

```go
func New(socketPath string, opts ...Option) *Client          // no I/O
func NewFromEnv(opts ...Option) (*Client, error)
func ResolveSocketPath(session string) (string, error)
type DialFunc func(context.Context, string) (io.ReadWriteCloser, error)
func WithDialer(dialer DialFunc) Option
func WithDialTimeout(d time.Duration) Option
func WithRequestIDs(next func() string) Option

func (c *Client) SocketPath() string
func (c *Client) Call(ctx context.Context, method string, params, result any) error
func (c *Client) CallRaw(ctx context.Context, method string, params any) (json.RawMessage, error)
func (c *Client) OpenStream(ctx context.Context, method string, params any) (*Stream, error)
```

`Call` and `CallRaw` dial a connection, write one request line, read one
response line and close, because that is all the server allows. A `nil`
`params` is sent as `{}`. An error response becomes `*Error` carrying the
method. Cancelling the context closes the connection, which is the only way to
unblock a read on every platform, and the call wraps `ctx.Err()` in an `OpError`.

The request line is encoded before the dial, here as in `OpenStream` and
`PaneGraphicsStream`, so that it is ready to write the moment the connection
opens. The server's first read decides whether the call is answered
immediately or a poll interval later, which is enough time to encode a request
in but not enough to encode one after. An encoding failure therefore surfaces
without a connection having been made.

Connection establishment is injectable through `WithDialer`. A `DialFunc`
receives the configured address unchanged and returns an exclusively owned
`io.ReadWriteCloser`; nil restores the platform dialer. The function must
honor its context and support concurrent invocations. `WithDialTimeout` adds
a deadline only around dialing, for both built-in and custom dialers. The
dial context is not a connection lifetime: a successful connection must survive
its cancellation. A nil connection without an error is rejected, and a
connection returned with an error or after cancellation is closed. Successful
dials are wrapped in a private once-closer, so cancellation, failed handshakes
and stream cleanup never invoke the underlying Close more than once.

Ordinary calls, subscriptions and graphics streams use one `open` exchange:
encode before dial, watch the request context, write the complete request,
read and decode the response, then transfer the connection and its existing
buffered reader. Failed exchanges close the connection. The watcher is stopped
and any cancellation callback already running is joined before ownership
transfers, so a completed opening context cannot later close a returned stream.
Both request and frame writes reject a short write with `io.ErrShortWrite`.
A failed frame write closes the graphics stream because a partial header or
body cannot be followed safely by another frame. This changes no wire framing
and introduces no connection pool or retries.

`OpenStream` keeps the connection and hands back a `*Stream` whose `Next`
decodes each pushed line into a `RawEvent`; `Ack` holds the result of the
response that opened it. Nothing is ever written to that connection again,
because the server closes a streaming connection as soon as the client sends
anything else. `Close` unblocks a waiting `Next`, which then reports
`ErrStreamClosed` through `errors.Is`.

`ResolveSocketPath` follows herdr: an explicit session name wins, then
`HERDR_SOCKET_PATH`, then `HERDR_SESSION`, then the default session. The
config directory is `$XDG_CONFIG_HOME/herdr` or the platform default. A herdr
built with debug assertions uses `herdr-dev` instead, so a resolved path only
reaches a release build; `HERDR_SOCKET_PATH` is the way to a debug one.

Optional request fields that are not strings are pointers, so that leaving one
unset differs from sending its zero value. `Ptr` and `Value` cover the 134
such fields without a named variable per field.

## Generated code

Generator inputs: `schema/herdr-api.schema.json` and
`schema/method-results.json`. All output files live in `herdr/` beside the
handwritten transport and start with
`// Code generated by herdr-api-gen. DO NOT EDIT.`:

| File | Content |
| --- | --- |
| `schema_gen.go` | `const SchemaProtocol uint32 = 22` |
| `types_gen.go` | Every `$defs` entry: params, info structs, enums, discriminated unions |
| `results_gen.go` | `Result` interface, the 64 result variants, `DecodeResult` |
| `events_gen.go` | `EventKind`, `Event` interface, event structs, `DecodeEvent`, `EventEnvelope` |
| `methods_gen.go` | `Method*` constants and the `(*Client)` wrappers |

`herdr/herdr.go` carries
`//go:generate go run ../cmd/herdr-api-gen -schema ../schema/herdr-api.schema.json -methods ../schema/method-results.json -out .`,
whose relative paths are read from the package directory `go generate` runs
in.

### Naming

- `$defs` names are used verbatim (`PaneInfo`, `PluginInvocationContext`).
  A name that appears in several schema sections must be identical after
  normalising `$ref` values to bare definition names; otherwise the generator
  fails.
- Fields: snake_case to PascalCase with Go initialisms upper-cased: `id→ID`,
  `url→URL`, `json→JSON`, `api→API`, `ttl→TTL`, `pid→PID`, `tty→TTY`,
  `ansi→ANSI`, `ui→UI`, `cli→CLI`, `os→OS`; everything else is title-cased
  per word (`cwd→Cwd`, `ms→Ms`, `px→Px`, `data_base64→DataBase64`,
  `argv0→Argv0`).
- Enums: `type AgentStatus string` with constants such as
  `AgentStatusIdle AgentStatus = "idle"`. `-`, `_` and `.` in values are word
  boundaries (`recent_unwrapped→RecentUnwrapped`, `top-left→TopLeft`,
  `antigravity_cli→AntigravityCLI`).
- `ResponseResult` variants: `<Pascal(type)>Response`, for example
  `pane_info→PaneInfoResponse`, `ok→OKResponse`, `pong→PongResponse`. The
  suffix is `Response` rather than `Result` because ten variants carry a
  payload the schema already calls `<Pascal(type)>Result`, such as
  `pane_read→PaneReadResponse` holding a `PaneReadResult`.
- `EventData` variants: `<Pascal(type)>Event`, for example
  `pane_created→PaneCreatedEvent`. Definitions in the `subscription_event`
  section with the same name and shape (`PaneAgentStatusChangedEvent`) merge
  into one type.
- `Subscription` variants: `<Pascal(type)>Subscription` with dots as word
  boundaries, for example
  `pane.agent_status_changed→PaneAgentStatusChangedSubscription`.
- Variants of other discriminated unions: `<UnionName><Pascal(tag)>`, for
  example `LayoutNodePane`, `LayoutNodeSplit`, `PaneMoveDestinationNewTab`,
  `OutputMatchRegex`, `AgentViewFilterAll`, `EventMatchPaneClosed`. The
  discriminator is the property that carries a `const` in every variant
  (usually `type`; `event` for `EventMatch`).
- Methods: `pane.graphics.set→MethodPaneGraphicsSet = "pane.graphics.set"`
  with wrapper `PaneGraphicsSet`.

### Type mapping

| Schema | Go |
| --- | --- |
| `string` | `string` |
| `integer` with `format` `uint64`/`uint32`/`uint16`/`int32` | matching Go type |
| `integer` with `format` `uint` | `uint64` |
| `integer` without format | `int64` |
| `number` | `float64` |
| `boolean` | `bool` |
| `array` | `[]T` |
| `object` with `additionalProperties` | `map[string]T`; `map[string]*T` when values are nullable |
| `true` (any value, e.g. `agent_explain.explain`) | `json.RawMessage` |
| `oneOf`/`anyOf` with a discriminator | sealed interface plus variant structs (below) |
| union without a discriminator | handwritten type, see "Handwritten complements" |

Optional and nullable fields:

| Case | Go field |
| --- | --- |
| required, not nullable | `T` |
| required, nullable | `*T` |
| optional `string` or enum, not nullable | `T` with `omitempty` |
| optional `bool`/integer/float/struct, not nullable | `*T` with `omitempty` (`strip_ansi` defaults to true; a value type could not express an explicit false) |
| optional, nullable | `*T` with `omitempty` (serde treats null and absent identically for `Option` fields, so an explicit null is never needed) |
| optional array/map | `[]T` / `map[string]T` with `omitempty` |

Discriminated unions are generated as:

```go
type LayoutNode interface{ isLayoutNode() }
type LayoutNodePane struct{ ... }                       // without the discriminator field
func (LayoutNodePane) isLayoutNode() {}
func (v LayoutNodePane) MarshalJSON() ([]byte, error)   // injects "type":"pane"
func decodeLayoutNode(data []byte) (LayoutNode, error)  // reads the discriminator, decodes the variant
```

The marker and `MarshalJSON` take value receivers and the decoder returns the
value form, so one spelling covers both directions. The cost is that two
decoded union values cannot be compared with `==` when the variant holds a
slice or map, which `LayoutNodePane` and three `AgentViewFilter` variants do:
the comparison panics at run time, where the pointer form would have compared
addresses. Distinguish variants with a type switch, and do not use a union
value as a map key. The decoder returned a
pointer at first, which meant a caller wrote `LayoutNodePane{…}` to build a
layout and `*LayoutNodePane` to read one back. Both forms satisfy the
interface, so the wrong one compiles and matches nothing; writing
`examples/worktree-bootstrap`, which touches both sides, is what made the cost
visible.

Structs with union-typed fields (including slices and pointers of them) get an
`UnmarshalJSON` that first decodes into an auxiliary struct holding the union
fields as `json.RawMessage`, then calls the matching `decodeX` per field.

### Results and events

```go
type Result interface{ ResultType() string }          // the "type" constant; variants are <Pascal(type)>Response
func DecodeResult(raw json.RawMessage) (Result, error) // by "type"; unknown types yield *UnknownResultError

type EventKind string                                  // underscore form, constants like EventKindPaneCreated
func (k EventKind) DotName() string                    // first underscore becomes a dot, matching herdr EventKind::dot_name
type Event interface{ EventName() string }             // dotted name, e.g. "pane.created", "pane.output_matched"
type EventEnvelope struct {
    Event EventKind `json:"event"`
    Data  Event     `json:"data"`
}
func DecodeEvent(event string, data json.RawMessage) (Event, error)
func (s *Stream) NextEvent(ctx context.Context) (Event, error)   // Next followed by DecodeEvent
```

`DecodeEvent`: when `event` contains a dot, decode directly by the three
`subscription_event` names; otherwise decode the `EventData` variant selected
by `data.type`. Unknown names yield `*UnknownEventError` carrying the raw data.

### Method wrappers

The shape follows `method-results.json`:

```go
// {"result":"pane_info"}
func (c *Client) PaneGet(ctx context.Context, params PaneTarget) (*PaneInfoResponse, error)
// params of type EmptyParams or PingParams are omitted from the signature
func (c *Client) Ping(ctx context.Context) (*PongResponse, error)
// {"results":[...]} or "*"
func (c *Client) PluginPaneOpen(ctx context.Context, params PluginPaneOpenParams) (Result, error)
// {"stream":"subscription_started"}
func (c *Client) EventsSubscribe(ctx context.Context, params EventsSubscribeParams) (*Stream, error)
```

Single-result wrappers are implemented uniformly as `CallRaw` →
`DecodeResult` → type assertion; a failed assertion returns
`*UnexpectedResultError{Method, Want, Got}`.

### Handwritten complements (`unions_manual.go`)

Unions without a discriminator are handwritten types implementing
`MarshalJSON`/`UnmarshalJSON`; the generator skips these definitions via its
configuration and references the handwritten names: `PopupSize` (integer or a
`"80%"` string), `AgentViewValue` (string | bool | uint64 | `{"context":...}`),
`AgentViewField` and `AgentViewSortField` (built-in enum | `{"token":...}`).

### Generator (`cmd/herdr-api-gen`, `internal/gen`)

- Implements only the JSON Schema subset the schema uses: `type` (string or
  array), `properties`, `required`, `$ref`, `oneOf`/`anyOf`, `enum`, `const`,
  `items`, `additionalProperties`, `format`, `description`, `default`. Any
  other keyword is an error, never silently ignored.
- Output is deterministically ordered and formatted with `go/format`.
  `description` values become godoc.
- Validation: `method-results.json` must cover every method in the schema and
  must not reference unknown methods or result types.
- Tests: unit tests for the naming and mapping rules; a golden test on the real
  schema; round-trip decoding tests in the `herdr` package using responses
  captured from a live server under `herdr/testdata/`.
- No third-party dependencies.

## Graphics streaming

`pane.graphics.stream` keeps its connection open and sends binary frames, so
`graphics.go` implements it by hand on the transport's own helpers:

```go
func (c *Client) PaneGraphicsStream(ctx context.Context, params PaneGraphicsStreamParams) (*GraphicsStream, error)
func (s *GraphicsStream) SendFrame(ctx context.Context, frame GraphicsFrame) error
func (s *GraphicsStream) SendFileFrame(ctx context.Context, frame GraphicsFileFrame) (*PaneGraphicsFrameAckResponse, error)
func (s *GraphicsStream) Wait(ctx context.Context) error
func (s *GraphicsStream) Close() error
```

An inline frame is one JSON header line followed by exactly `data_length`
raw bytes and draws no reply, which is why `SendFrame` returns only an error.
A file frame names an immutable file the terminal reads itself, and the
server answers it with `pane_graphics_frame_ack` once the terminal has
accepted it. That variant is the only one in `method-results.json` that no
method returns, which is consistent with it belonging here. Closing the
connection clears the layer, and the server ends the stream on an error
response or on a frame that stalls.

`OpenStream` hands the connection to a reader goroutine that never writes,
whereas a graphics stream keeps writing frames. Their first exchange shares
`open`, including connection injection, context ownership, and the buffered
reader. After its `ok` acknowledgement is validated, `GraphicsStream` takes
over the connection and retains its own framing and acknowledgement reader.

What the fake server proves is the framing: two frames in sequence parse only
if the first body was consumed whole. Against a live server only the entry
point was checked, read-only, by opening a stream for a pane id that cannot
exist and getting `pane_not_found`, which shows the method is accepted and
reaches its handler. Frame acceptance, acknowledgements, timeouts and the
error codes are backed by the herdr sources rather than by execution, because
exercising them means drawing into a real pane.

## Session mirror

```go
func (c *Client) Subscribe(ctx context.Context, subs ...Subscription) (*EventStream, error)
func (s *EventStream) Next(ctx context.Context) (Event, error)

func MirrorSubscriptions() []Subscription
func OpenSession(ctx context.Context, c *Client, subs ...Subscription) (*Session, error)
func (s *Session) Workspaces() []WorkspaceInfo
func (s *Session) Tabs() []TabInfo
func (s *Session) Pane(paneID string) (PaneInfo, bool)
func (s *Session) Agents() []AgentInfo
func (s *Session) Layout(tabID string) (PaneLayoutSnapshot, bool)
func (s *Session) Snapshot() SessionSnapshot
func (s *Session) Next(ctx context.Context) (Event, error)
func (s *Session) Close() error
```

A subscription starts when the server accepts it and never replays what came
before, so a client that wants complete state has to subscribe first and take
the snapshot second. `OpenSession` does exactly that: it subscribes, buffers
what arrives, calls `session.snapshot`, installs it, applies the buffer in
order and then keeps streaming. Applying an event twice has to be safe for
that to work, which it is because every handler assigns state rather than
adjusting it.

The cache advances only as `Next` delivers, so reading an accessor after
`Next` shows the state that event produced. Accessors copy what they return
and the mirror is safe for concurrent readers.

Each accessor takes the lock on its own, which is enough while the mirror is
only advanced by `Next`, but leaves no way to read one consistent frame under
one lock, and no way to list panes or layouts at all. `Snapshot` closes both:
it returns the same `SessionSnapshot` the bootstrap consumed, with the focused
ids read back from the `Focused` flags and the version and protocol carried
from the snapshot the mirror was built on. Writing `examples/agent-board` is
what showed the gap; the board needs a whole frame, not a field at a time.

A server restart, which is what live handoff does, ends the stream.`Session`
reconnects, bootstraps again, and reports the gap as one `*ResyncEvent` so a
caller can drop anything it derived from the older state. Backoff is bounded
by the context. A response that the server refuses, rather than a connection
that failed, is not retried.

Three behaviours were settled by experiment rather than by reading the schema.
Focus is exclusive across the session and herdr emits the whole chain, so
focusing a workspace also produces `tab.focused` and `pane.focused`; the
mirror can treat focus as a single flag without lagging. A focus change emits
no `layout.updated`, though, while `layout.export` already reports the new
`focused_pane_id`, and that field belongs to one tab rather than the session:
a second tab keeps naming its own pane. So `pane.focused` also moves the
focused pane of the cached layout of that pane's tab, or `Snapshot` would
hand back a layout naming whoever held focus when the layout was last
reported. And `released` on
`pane.agent_detected` means the agent handed the pane back to the shell, which
`src/events.rs` calls `AppEvent::HookAgentReleased`, so the mirror drops the
agent and keeps its final status.

## Package `plugin`

```go
func Load() (*Env, error)
func LoadFrom(lookup func(string) (string, bool)) (*Env, error)
func (e *Env) Kind() EntryKind
func (e *Env) Client(opts ...herdr.Option) *herdr.Client
func (e *Env) Context() (*herdr.PluginInvocationContext, error)
func (e *Env) EventEnvelope() (*herdr.EventEnvelope, error)

type Handlers struct {
    Startup func(context.Context, *Env) error
    Action  func(context.Context, *Env, string) error
    Event   func(context.Context, *Env, *herdr.EventEnvelope) error
    Pane    func(context.Context, *Env, string) error
}
func Run(ctx context.Context, h Handlers) int
```

This is the base the rest of the package rests on. A plugin normally
registers a handler per entrypoint instead of writing the switch by hand; see
"The plugin authoring layer", which also covers reading the invocation
context, owning state and configuration, and testing without Herdr.

`Env` is the environment Herdr injects into a plugin command. `Kind` reports
which manifest entrypoint started the process: a startup hook sets
`HERDR_PLUGIN_EVENT` to the literal `startup`, an event hook sets it to a
dotted event name, an action sets `HERDR_PLUGIN_ACTION_ID`, and a pane command
sets `HERDR_PLUGIN_ENTRYPOINT_ID`.

`ShutdownContext` cancels a context when Herdr asks the process to stop, which
a pane entrypoint needs because it runs until the user closes the pane and
nothing else tells it to finish. Closing a pane delivers SIGHUP and then
SIGTERM to the process in it. That was measured against a running server, by
opening a pane on a trapping script through `layout.apply` and closing it with
`pane.close`, not inferred: herdr has no explicit kill on that path, and the
plan had wrongly guessed SIGINT. `Run` installs nothing itself, so a plugin
opts in by passing the context.

`Run` dispatches by kind and returns a process exit code: `ExitOK` when the
handler returned nil, `ExitHandlerError` when it returned an error, and
`ExitRuntimeError` when no handler ran at all. Herdr records the exit status
in its plugin command log, so those two failures are worth telling apart. A
missing handler for the invoked kind is a configuration error rather than a
silent success. Errors are written to stderr, which Herdr captures in the same
log.

## Package `plugin/manifest`

`Parse`, `Decode` and `Validate` mirror what herdr does with
`herdr-plugin.toml`, down to the error codes, the 120-character id limit and
the rule that trims surrounding whitespace before judging a value. Warnings
come back separately from errors, as they do in herdr: an unknown event name
is a warning, a duplicate action id is an error.

The event names a hook may reference are the 22 in `PLUGIN_HOOK_EVENT_KINDS`,
not all 26 `EventKind` values. Herdr excludes `workspace.metadata_updated`,
`pane.updated`, `pane.output_changed` and `layout.updated` and never fires a
hook for them, so naming one is a warning here too.

`github.com/BurntSushi/toml` is the module's only third-party dependency, and
only this package uses it.

## Errors

`errors.go` names the 35 codes seen so far: those read out of `encode_error`
callers in the herdr sources, and 13 more that `internal/e2e` met while
exercising the methods that report them. `IsCode` matches through wrapping,
and comparing `Code` against a plain string keeps working for a code a newer
server adds.

Client boundaries add `OpError{Method, Op, Err}` to local failures. Operations
are `validate`, `encode`, `dial`, `write`, `read`, `decode` and `close`.
`Unwrap` preserves cause inspection; a decoded server error envelope remains
`*Error` without an operation wrapper. Cancellation prioritizes `ctx.Err()` and
is inspected through `errors.Is`, including when closing the connection wakes
a read concurrently with the context signal.

The result decoder itself stays context-free. Generated methods call the
handwritten `decodeResult(method, raw)` boundary and wrap unexpected result
types with `OpDecode`. The generated `Stream.NextEvent` and the mirror's
`deliver` path add the subscription method to payload-decoding failures.
These are template call-site changes, not new schema facts or metadata.
`DecodeResult` and `DecodeEvent` remain useful independently of a connection.

A stream read error retains `ErrStreamClosed` and its original I/O cause;
explicit close failures carry `OpClose`. Graphics acknowledgement failures
retain their original API/decode/read classification when reported by a send
or `Wait`. Validation fails before transmission, while a partial frame write
still closes the stream as before. The model does not change connection
ownership, reconnect policy, or whether canceling an event read keeps it open.

Session-local lifecycle outcomes remain sentinel/context errors when no wire
operation is in progress. Its existing `errors.As` and `errors.Is` decisions
continue to see wrapped API, unexpected-result and stream-closed causes.
Operation metadata must not be interpreted as proof of server execution or as
a retry-safety classification.

## Testing

Unit tests cover the transport against a fake server that behaves exactly as
herdr does, the generator's rules and its output, the mirror's bootstrap
ordering and reconnection, the plugin environment and dispatch, and manifest
validation with a fixture per rule. Decoding is also checked against responses
captured from a real server, sanitised, under `herdr/testdata`.

`internal/e2e` runs behind the `e2e` build tag against a server it starts
itself: `herdr --session <name> server` under a temporary `XDG_CONFIG_HOME`,
stopped in cleanup. It never touches the caller's session. Its purpose is to
prove `schema/method-results.json`, which the schema does not state and which
was derived by reading herdr's handlers: every reachable method is called
through its generated wrapper and its result type asserted, so a wrong mapping
fails as a decode or assertion error. It currently exercises 92 of the 102
methods with no disagreements, and the coverage list is checked against the
schema so a method can neither disappear nor go unexplained unnoticed.

The eight plugin methods are among them. Linking a fixture manifest written
inside the harness's own temporary directory reaches the registry and the
plugin pane methods, because the registry follows `XDG_CONFIG_HOME`; the
suite asserts that the caller's real registry is unchanged across the run,
comparing contents rather than modification time, because a live server
rewrites that file with identical contents.

`just check` runs build, tests, lint and the generated-code check, then two
type-checks that nothing else covers: the e2e suite, which its build tag
keeps out of `test` and `.golangci.yml` excludes from lint, and every package
for the other platforms, because `go build` skips test files and a break
confined to a platform-specific one would otherwise reach CI. `just e2e` runs
the suite above. `just herdr-check` reports drift from the snapshot.

## Known gaps

`pane.graphics.stream` is the one method the server accepts that the schema
does not declare, so no wrapper is generated for it. Comparing the method list
the server reports in an `invalid_request` error against the schema snapshot
of herdr 0.9.0 shows that single difference; every other method the server
accepts is generated. The method is absent from the schema because its framing
is not newline-delimited JSON: after the server acknowledges the request, the
client sends one JSON header followed by exactly `data_length` raw bytes per
frame, which no generated wrapper can express. It is written by hand instead;
see "Graphics streaming" for what that covers and what it does not.
`schema/known-gaps.json` records the difference so `just herdr-check` does not
report it as drift.

Rerun that comparison after a schema refresh: a method that appears in the
error list but not in the snapshot is a method this module cannot reach.

## Where the server is narrower than the schema

The schema states what a request may contain, not what the server accepts, so
two methods take arguments the schema permits and herdr 0.9.0 refuses.
`internal/e2e` found both.

`events.wait` accepts every `EventMatch` variant in the schema, but 0.9.0
matches only pane agent status; any other variant returns
`unsupported_event_wait_match`. Wait on other events with `events.subscribe`
instead.

`worktree.create` without a `path` puts the checkout under the calling user's
home, at `~/.herdr/worktrees/<repo>/<branch>`, not relative to `cwd`. Pass an
explicit `path` when the location matters, which is what the e2e suite does so
that it stays inside its temporary directory.

## Following a new herdr release

Three kinds of change arrive with a release, and they are found in different
ways. Run `just herdr-check` first: it compares the installed binary's schema
and the running server against the snapshot, and exits non-zero on any
difference that `schema/known-gaps.json` does not already account for.

**Changes the schema describes** are handled by regenerating. `just
schema-update && just gen && just check` rewrites the snapshot and the
generated code. The generator fails when a method in the schema has no entry
in `schema/method-results.json` and when that file names a method or result
type that no longer exists, so an added or removed method cannot pass
silently. A changed field type becomes a compile error in whatever uses it. A
new enum value simply appears; decoding an unknown one still works because the
enums are strings.

**Which result type a method returns** is not in the schema, so a change there
would leave `method-results.json` quietly wrong. `internal/e2e` is the guard:
it calls each reachable method through its generated wrapper and asserts on the
decoded result type, so a changed mapping fails as a decode or assertion error.
Run `just e2e` after regenerating.

**Behaviour the schema does not describe** is the part with no automatic
guard. Each item below was read out of the herdr sources at v0.9.0 and has to
be re-read when the version this module targets changes. The file is the place
to look, not a guarantee it still exists.

| Fact | Where it came from |
| --- | --- |
| One request per connection; only `events.subscribe` keeps the connection open | `src/api/server.rs`, `handle_connection` and `stream_subscriptions` |
| Socket path resolution and the session name rules | `src/session.rs`, `api_socket_path_for` and `validate_name` |
| The config directory chain, including `herdr-dev` for debug builds | `src/config/io.rs`, `config_dir`, `platform_config_dir` and `app_dir_name` |
| The environment injected into plugin commands | `src/app/api/plugins/runtime.rs` and `src/app/api/plugins/panes.rs` |
| Manifest validation rules, limits and error codes | `src/app/api/plugins/manifest.rs` |
| The 22 events a manifest hook may name, narrower than the 26 `EventKind` values | `src/api/schema/events.rs`, `PLUGIN_HOOK_EVENT_KINDS` |
| Popup size parsing, integer or percentage | `src/popup_size.rs` |
| `plugin.pane.open` with `split` placement takes `target_pane_id` alone; a `workspace_id` or `direction` beside it is `invalid_params` | measured against a running server, `internal/e2e/plugin_test.go` |
| `released` on `pane.agent_detected` meaning the agent handed the pane back | `src/events.rs`, `AppEvent::HookAgentReleased` |
| The set of error codes | `encode_error` callers across `src/app/api/` |

The last column is why `schema/README.md` records the version a snapshot was
taken from: an upgrade means re-reading those files at the new tag, not
guessing from behaviour.

## The plugin authoring layer

Writing `examples/agent-status`, the first real plugin on this module, showed
what the library still left to the author. Each piece below closes a gap that
example had hand-rolled, which is why the rewrite onto the layer cut its
`main.go` from 201 lines to 112 and its manifest test from 56 to 13.

### Registering by id instead of switching on one

```go
p := plugin.New()
p.Startup(onStartup)
p.Action("show", onShow)
p.Pane("board", onBoard)
plugin.OnEvent(p, onStatusChanged)   // func(context.Context, *plugin.Env, *herdr.PaneAgentStatusChangedEvent) error
os.Exit(p.Run(ctx))
```

`OnEvent` is a free function rather than a method because it takes a type
parameter: the generated event types implement `EventName`, so the name comes
from the handler's own argument and the author never writes the string.
Registering a payload herdr never delivers to a hook is accepted here, because
the registry alone cannot tell one from a payload the manifest simply has not
declared yet; `plugintest.CheckManifest` reports it, reading the hook set from
`manifest.HookEventNames`.

Registration mistakes panic rather than surfacing at dispatch: a nil handler,
an empty id, or a second registration for an id already taken. Registration is
a program's static description of itself, and a silent overwrite would make
`CheckManifest` agree with a manifest the binary does not serve. `Handlers`
and the original `Run` still work, on the same dispatch.

### Checking the manifest against the code

```go
func TestManifest(t *testing.T) {
    plugintest.CheckManifest(t, "herdr-plugin.toml", newPlugin())
}
```

Herdr validates the manifest, but nothing tied its ids to the handlers a
binary serves, so a renamed action failed at invocation time rather than in a
test. The registry knows every id, so one call reports an id declared with no
handler, a handler with no manifest entry, an `[[events]] on` value herdr
never fires a hook for, and a handler registered for such an event, which no
manifest entry could reach either. That last check reads the hook set from
`manifest.HookEventNames`, the exported form of `PLUGIN_HOOK_EVENT_KINDS`.

### Testing a handler without Herdr

```go
env := plugintest.Env(plugintest.Action("show"), plugintest.Workspace("w1"))
```

`plugin/plugintest` builds an `Env` directly, with options for the entrypoint
kind, the invocation context, the event payload and the two directories, so a
handler test sets no environment variables. It is a separate package so that
importing it cannot pull test-only code into a plugin binary.

`herdrtest.NewServer` answers the real client over a local socket. It records
requests, serves fixed results with `Reply`, API errors with `Fail`, and dynamic
results with `Handle`. The latter lets a test inspect parameters or coordinate
a snapshot response with an event producer. Handlers run concurrently and get
a context canceled by client disconnect or server shutdown.

`AllowSubscriptions` acknowledges valid, nonempty `events.subscribe` requests.
`WaitSubscription` returns the acknowledged streams in order, including
reconnects; each handle sends typed events with `Send`, sends explicitly raw
envelopes with `SendRaw`, and can be closed to force a resync. The test supplies
snapshot responses and events: the server does not emulate Herdr's state,
filter events, or promise replay. Lifecycle events use underscore envelope
names; the three pane-scoped event envelopes retain their dotted names.

`WaitCall` and `WaitSubscription` use indexed, non-consuming records and wake
all interested waiters. Contexts bound waits; channels in handlers can arrange
precise response timing without sleeps. A written acknowledgement or event does
not prove client consumption. Plugin tests wait for observable output to prove
an event was processed.

`plugin/plugintest.Server` adds only plugin environment adaptation to this
protocol server. Its existing `Reply(...).Fail(...).Env(...)` calls remain
available, along with the protocol server's subscriptions and waits. The shared
`internal/testsocket` owns the listener, accepted sockets, cancellation and
joining of server goroutines. The older raw fake server and its mirror helper
also use that lifecycle owner; their special wire-corruption and internal
Session tests stay in the client package to avoid a client/test-package import
cycle.

Closing the server cancels handlers and closes connections before joining the
server goroutines. Handlers must observe cancellation and must not close the
server themselves. Cleanup releases test-owned handler gates before joining
those handlers. A canceled in-progress event write closes its subscription,
since a partially written frame cannot safely be resumed; cancellation while
waiting for another writer does not disturb that connection.

Unscripted methods return a diagnostic API error naming the method. They do not
automatically fail the test, so tests can verify how a caller handles a rejected
request. Invalid scripted results fail the test and return an error to the
caller. Graphics streaming and a public real-server harness are not included.
Unix socket tests skip on Windows, where the Herdr client uses named pipes and
the standard library supplies no matching listener. Cross-platform type checks
are not Windows runtime evidence.

```go
err := p.Dispatch(ctx, plugintest.Env(plugintest.Action("show")))
```

`Dispatch` runs the same selection `Run` does and returns the handler's error
instead of an exit code, so a test covers the registration and the envelope
decode rather than only the handler function it calls directly.

### Reading the context without pointer checks

`Env.Invocation` returns the invocation context with its optional fields
flattened to values: a field Herdr did not send reads as the empty string.
It reports no error, because an entrypoint invoked without a context and a
context that fails to decode leave a plugin reading one field with nothing
different to do; `Env.Context` keeps the pointer form for when the difference
matters. `Worktree` stays a pointer, having no useful empty value.

### Owning state without the file plumbing

`StatePath`, `ConfigPath`, `ReadState`, `WriteState`, their JSON forms and
`AppendStateJSONL` share one write path: a temporary file in the same
directory, then a rename, so a crash mid-write cannot truncate what was there.
A name that would escape the directory is rejected. That atomicity is the
reason this belongs in the library rather than in each plugin.

`ReadConfig` and `ReadConfigJSON` read the configuration directory on the
same terms, an absent file included, because a plugin the user has never
configured is the normal case and every configurable plugin would otherwise
write that branch itself. There is no write counterpart: the configuration
directory belongs to the user, and rewriting it would discard their comments
and formatting.

`LayoutPanes` walks an applied layout to its pane leaves. Pane ids are
assigned by `layout.apply`, so a plugin that arranges panes and then acts on
one of them can only learn its id from the response, and every such plugin
was writing the same recursion. Labelling the panes in the request and
matching the label in the answer identifies a pane without depending on its
position.

### What is deliberately not included

No wrapper for multi-step flows such as "split a pane, run a command, wait
for output". Those are two or three generated calls and the useful shape
differs per plugin; a wrapper would guess wrong and hide the calls that
matter. No logging helper either: Herdr already captures stdout and stderr
into its command log, so the standard library is enough.

## Not built yet

**The last 10 methods.** `agent.start`, `agent.prompt` and `agent.send_keys`
need a real agent process in the pane; a machine with a supported agent CLI
could cover them and one without would skip. `client_shell.surface.set`,
`command.invoke`, `popup.close` and `pane.graphics.info` need an attached
client or the client shell endpoint. `product_announcement.dismiss` and
`release_notes.dismiss` need state a fresh server does not have; the API
offers no way to create it, since neither has a matching read method.
`server.live_handoff` would take down the suite's own server.

`integration.install` and `integration.uninstall` used to be on this list,
because integrations are written under the user's own home rather than under
`XDG_CONFIG_HOME`. Redirecting `HOME` as well brought them in reach, and the
default worktree location, `~/.herdr/worktrees`, moved inside the harness
root with them.

**A stable API.** v0.1.0 is the first tagged release, cut once the package
move to `herdr/` had settled the import path. It is a v0, so the API may
still change between minor versions; a v1 would be a promise the surface is
finished, which several of this release's own additions argue against.
