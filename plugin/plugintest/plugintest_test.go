package plugintest_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/vika2603/herdr-client/herdr"
	"github.com/vika2603/herdr-client/plugin"
	"github.com/vika2603/herdr-client/plugin/plugintest"
)

func TestEnvKinds(t *testing.T) {
	tests := []struct {
		name string
		opt  plugintest.Option
		want plugin.EntryKind
	}{
		{name: "startup", opt: plugintest.Startup(), want: plugin.KindStartup},
		{name: "action", opt: plugintest.Action("show"), want: plugin.KindAction},
		{name: "pane", opt: plugintest.PaneCommand("board"), want: plugin.KindPane},
		{
			name: "event",
			opt:  plugintest.EventHook(&herdr.PaneCreatedEvent{Pane: herdr.PaneInfo{PaneID: "pane-1"}}),
			want: plugin.KindEvent,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := plugintest.Env(tt.opt).Kind(); got != tt.want {
				t.Errorf("Kind() = %q, want %q", got, tt.want)
			}
		})
	}

	if got := plugintest.Env().Kind(); got != plugin.KindUnknown {
		t.Errorf("Kind() without options = %q, want %q", got, plugin.KindUnknown)
	}
}

func TestEnvIDs(t *testing.T) {
	env := plugintest.Env(plugintest.Action("show"), plugintest.PaneCommand("board"))
	if env.ActionID != "show" || env.EntrypointID != "board" {
		t.Errorf("ActionID = %q, EntrypointID = %q", env.ActionID, env.EntrypointID)
	}
}

func TestEnvInvocationContext(t *testing.T) {
	env := plugintest.Env(
		plugintest.Action("show"),
		plugintest.Workspace("ws-1"),
		plugintest.Tab("tab-1"),
		plugintest.Pane("pane-1"),
		plugintest.FocusedPane("pane-2"),
		plugintest.SelectedText("go test ./..."),
		plugintest.ClickedURL("https://example.test/issues/7"),
		plugintest.LinkHandler("issue"),
	)

	got := env.Invocation()
	want := plugin.Invocation{
		WorkspaceID:   "ws-1",
		TabID:         "tab-1",
		FocusedPaneID: "pane-2",
		SelectedText:  "go test ./...",
		ClickedURL:    "https://example.test/issues/7",
		LinkHandlerID: "issue",
	}
	if got != want {
		t.Errorf("Invocation() = %+v, want %+v", got, want)
	}

	// Herdr sets the shared variables as well as the context.
	if env.WorkspaceID != "ws-1" || env.TabID != "tab-1" || env.PaneID != "pane-1" {
		t.Errorf("ids = %q, %q, %q", env.WorkspaceID, env.TabID, env.PaneID)
	}
	if env.ClickedURL != "https://example.test/issues/7" || env.LinkHandlerID != "issue" {
		t.Errorf("link = %q, %q", env.ClickedURL, env.LinkHandlerID)
	}
}

func TestEnvWithoutContextOptions(t *testing.T) {
	env := plugintest.Env(plugintest.Startup())

	if len(env.ContextJSON) != 0 {
		t.Errorf("ContextJSON = %q, want none", env.ContextJSON)
	}
	if _, err := env.Context(); err == nil {
		t.Error("Context() error = nil, want ErrNoContext")
	}
	if got := env.Invocation(); got != (plugin.Invocation{}) {
		t.Errorf("Invocation() = %+v, want the zero value", got)
	}
}

func TestContextOptionAndOverrides(t *testing.T) {
	env := plugintest.Env(
		plugintest.Context(herdr.PluginInvocationContext{
			WorkspaceID:    herdr.Some("ws-1"),
			WorkspaceLabel: herdr.Some("herdr"),
		}),
		plugintest.Workspace("ws-2"),
	)

	got := env.Invocation()
	if got.WorkspaceID != "ws-2" || got.WorkspaceLabel != "herdr" {
		t.Errorf("Invocation() = %+v, want the later option to win", got)
	}
}

func TestEventHookEnvelope(t *testing.T) {
	payload := &herdr.PaneAgentStatusChangedEvent{
		PaneID:      "pane-1",
		WorkspaceID: "ws-1",
		AgentStatus: herdr.AgentStatusWorking,
	}
	env := plugintest.Env(plugintest.EventHook(payload))

	if env.Event != "pane.agent_status_changed" {
		t.Errorf("Event = %q", env.Event)
	}
	envelope, err := env.EventEnvelope()
	if err != nil {
		t.Fatalf("EventEnvelope() error = %v", err)
	}
	if envelope.Event != herdr.EventKindPaneAgentStatusChanged {
		t.Errorf("envelope event = %q", envelope.Event)
	}
	decoded, ok := envelope.Data.(*herdr.PaneAgentStatusChangedEvent)
	if !ok {
		t.Fatalf("payload = %T, want *herdr.PaneAgentStatusChangedEvent", envelope.Data)
	}
	if !reflect.DeepEqual(decoded, payload) {
		t.Errorf("payload = %+v, want %+v", decoded, payload)
	}

	// The envelope is the one Herdr writes into HERDR_PLUGIN_EVENT_JSON.
	var raw struct {
		Event string          `json:"event"`
		Data  json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(env.EventJSON, &raw); err != nil {
		t.Fatal(err)
	}
	if raw.Event != "pane_agent_status_changed" {
		t.Errorf("event field = %q, want the underscore spelling", raw.Event)
	}
	if !strings.Contains(string(raw.Data), `"type":"pane_agent_status_changed"`) {
		t.Errorf("data = %s, want the discriminator herdr sends", raw.Data)
	}
}

// The two payloads only a dedicated subscription carries have no
// discriminator, so no hook envelope can express them.
func TestEventHookRejectsASubscriptionOnlyPayload(t *testing.T) {
	for _, payload := range []herdr.Event{
		&herdr.PaneOutputMatchedEvent{PaneID: "pane-1"},
		&herdr.PaneScrollChangedEvent{PaneID: "pane-1"},
	} {
		t.Run(payload.EventName(), func(t *testing.T) {
			defer func() {
				recovered, _ := recover().(string)
				if !strings.Contains(recovered, "fires no hook") {
					t.Errorf("recover() = %q, want it to say herdr fires no hook", recovered)
				}
			}()
			plugintest.Env(plugintest.EventHook(payload))
			t.Error("EventHook did not panic")
		})
	}
}

func TestEnvDirectories(t *testing.T) {
	state := t.TempDir()
	env := plugintest.Env(plugintest.StateDir(state), plugintest.ConfigDir("/config/example"))

	if got, want := env.StatePath("log.jsonl"), filepath.Join(state, "log.jsonl"); got != want {
		t.Errorf("StatePath() = %q, want %q", got, want)
	}
	if got, want := env.ConfigPath("config.toml"), filepath.Join("/config", "example", "config.toml"); got != want {
		t.Errorf("ConfigPath() = %q, want %q", got, want)
	}
}

// A handler is tested by calling it with a built environment, which is what
// the package is for.
func TestEnvDrivesAHandler(t *testing.T) {
	handler := func(_ context.Context, env *plugin.Env) error {
		return env.WriteStateJSON("last.json", env.Invocation().WorkspaceID)
	}

	env := plugintest.Env(plugintest.Action("show"), plugintest.Workspace("ws-1"), plugintest.StateDir(t.TempDir()))
	if err := handler(context.Background(), env); err != nil {
		t.Fatalf("handler error = %v", err)
	}
	var got string
	if err := env.ReadStateJSON("last.json", &got); err != nil {
		t.Fatalf("ReadStateJSON() error = %v", err)
	}
	if got != "ws-1" {
		t.Errorf("state = %q, want %q", got, "ws-1")
	}
}
