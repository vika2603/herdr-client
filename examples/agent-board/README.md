# Agent Board

An example Herdr plugin whose entrypoint is a pane: Herdr runs the command
inside a pane it owns, and the command runs until the user closes that pane.
The board mirrors the session and redraws itself on every change.

```go
p := newPluginWithOutput(os.Stdout)

ctx, stop := plugin.ShutdownContext(context.Background())
code := p.Run(ctx)
```

| Entrypoint | Manifest section | What it does |
| --- | --- | --- |
| Pane `board` | `[[panes]]` | Renders the workspaces, tabs and agents of the session and redraws them on every event |
| Action `open` | `[[actions]]` | Opens that pane through `plugin.pane.open` |

[agent-status](../agent-status) shows the short-lived entrypoints: a startup
hook, an event hook and an action, one process per invocation. This example
shows the long-lived one and the session mirror it reads.

## The session mirror

`herdr.OpenSession` subscribes first and takes the `session.snapshot` second,
because a subscription never replays what came before it. It then applies each
event to the mirror before `Session.Next` returns that event, so reading
`Workspaces`, `Tabs`, `Pane` and `Agents` after `Next` shows the state the
event produced. The board redraws its whole frame from those accessors, which
is why no single event needs a handler of its own.

`Session.Pane` is read once per agent because `PaneInfo.Label` has no
`AgentInfo` counterpart; an agent whose pane the mirror no longer holds is
named by its pane id instead.

The mirror also owns the reconnect. When the server restarts, which is what a
live handoff does, the stream ends, the mirror bootstraps again and reports the
gap as one `*herdr.ResyncEvent`. Anything a caller derived from the events
before it has to be discarded. The board counts the events its frame was built
from, so a resync starts that count again and prints the cause above the frame;
everything else it shows comes from the accessors and is already current.

An event this build cannot decode arrives as `*herdr.UnknownEventError` and
never reaches the mirror, so the board skips it and keeps running rather than
ending on an event a newer server added.

## Closing the pane

Closing a pane delivers SIGHUP and then SIGTERM to the process in it.
`plugin.ShutdownContext` cancels the context on those signals, `Session.Next`
then reports `context.Canceled`, and the handler returns nil: the loop ends
between two frames instead of being killed inside one.

A failed write to the pane is dropped for the same reason. The terminal can be
gone before the signal arrives, and reporting that as a handler error would
enter a normal close in Herdr's plugin command log as a failure.

## Its own title

Herdr displays a plugin pane under the command line that runs in it until the
plugin names itself. `Client.PaneReportMetadata` reports a title for the pane
in `HERDR_PANE_ID`, with the plugin id as the reporting source: a later report
from this plugin replaces the title, and no other reporter can. A popup plugin
pane receives no pane id, so the board runs untitled there.

## Install

The manifest declares `linux` and `macos`. On Windows, add a second entry per
command that builds and runs `herdr-agent-board.exe`.

Linking registers the plugin for your user account and enables it in every
Herdr session, so run it yourself rather than letting a tool do it. `herdr
plugin link` does not run the `[[build]]` command, so build the binary first:

```bash
cd path/to/herdr-client/examples/agent-board
go build -o herdr-agent-board .
herdr plugin link .
herdr plugin list
```

To remove the plugin again:

```bash
herdr plugin unlink example.agent-board
```

## Use

Open the board through the action, or through the pane entrypoint directly:

```bash
herdr plugin action invoke open --plugin example.agent-board
herdr plugin pane open --plugin example.agent-board --entrypoint board
```

Both reach the same command. The action exists to show that a plugin can open
its own pane: `plugin.pane.open` needs nothing but the plugin id and the
entrypoint, because the manifest declares the placement.

Then split a pane, focus another workspace or let an agent change status, and
the board redraws. Close the pane to end it.

## Tests

The rendering is a pure function of a frame struct, so `TestRender` pins the
layout with golden strings. The pane constructor accepts an `io.Writer`;
`newPlugin()` still uses stdout, while tests capture frames without opening a
terminal or changing what the board displays.

`dispatch_test.go` runs the registered entrypoints with `plugintest.NewServer`
and a real `herdr.Client` and `Session`. It checks the initial subscription and
snapshot, event-driven updates, continued operation after an unknown event,
disconnect and fresh-snapshot resync, cancellation, API errors and connection
cleanup. The action test checks the actual `plugin.pane.open` parameters.
Tests wait for subscription acknowledgements and complete output frames rather
than sleeping to guess when the board has updated.

Run these tests from the repository root:

```bash
go test -race ./examples/agent-board
```

No Herdr binary, agent process or terminal UI is needed. Socket tests skip on
Windows because the test server has no named-pipe listener. These tests verify
plugin behavior and rendered text, not terminal display quality or compatibility
with a real Herdr server. `TestManifest` continues to check agreement between
`herdr-plugin.toml` and the registry with `plugintest.CheckManifest`.

`Plugin.Run` returns the exit code Herdr records in its plugin command log;
[agent-status](../agent-status/README.md#exit-codes) documents the three.
