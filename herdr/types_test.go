package herdr

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// readResult returns the result object of a response captured from a running
// Herdr server. The captured files keep the shape of the real responses;
// paths, titles and session ids are replaced with placeholders.
func readResult(t *testing.T, name string) json.RawMessage {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name+".json"))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	var response struct {
		ID     string          `json:"id"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		t.Fatalf("decode %s: %v", name, err)
	}
	if len(response.Result) == 0 {
		t.Fatalf("%s has no result object", name)
	}
	return response.Result
}

func TestDecodeCapturedResults(t *testing.T) {
	cases := []struct {
		file  string
		check func(t *testing.T, result Result)
	}{
		{
			file: "ping",
			check: func(t *testing.T, result Result) {
				pong, ok := result.(*PongResponse)
				if !ok {
					t.Fatalf("result is %T, want *PongResponse", result)
				}
				if pong.Version != "0.9.0" {
					t.Errorf("version = %q, want 0.9.0", pong.Version)
				}
				if pong.Protocol != SchemaProtocol {
					t.Errorf("protocol = %d, want %d", pong.Protocol, SchemaProtocol)
				}
				capabilities, ok := pong.Capabilities.Get()
				if !ok || !capabilities.LiveHandoff {
					t.Errorf("capabilities = %+v, want live_handoff", pong.Capabilities)
				}
				if !capabilities.HealthCheck.ValueOrZero() {
					t.Error("health_check was not decoded")
				}
			},
		},
		{
			file: "workspace_list",
			check: func(t *testing.T, result Result) {
				list, ok := result.(*WorkspaceListResponse)
				if !ok {
					t.Fatalf("result is %T, want *WorkspaceListResponse", result)
				}
				if len(list.Workspaces) != 5 {
					t.Fatalf("decoded %d workspaces, want 5", len(list.Workspaces))
				}
				first := list.Workspaces[0]
				if first.WorkspaceID != "w6W" || first.Number != 1 || first.PaneCount != 3 {
					t.Errorf("first workspace = %+v", first)
				}
				if first.AgentStatus != AgentStatusWorking {
					t.Errorf("agent status = %q, want %q", first.AgentStatus, AgentStatusWorking)
				}
				var worktrees int
				for _, workspace := range list.Workspaces {
					if worktree, ok := workspace.Worktree.Get(); ok {
						worktrees++
						if worktree.RepoName != "herdr-client" {
							t.Errorf("worktree repo = %q", worktree.RepoName)
						}
					}
				}
				if worktrees != 3 {
					t.Errorf("decoded %d worktrees, want 3", worktrees)
				}
			},
		},
		{
			file: "pane_list",
			check: func(t *testing.T, result Result) {
				list, ok := result.(*PaneListResponse)
				if !ok {
					t.Fatalf("result is %T, want *PaneListResponse", result)
				}
				if len(list.Panes) == 0 {
					t.Fatal("no panes decoded")
				}
				pane := list.Panes[0]
				if pane.PaneID != "w6W:p5" || pane.WorkspaceID != "w6W" || pane.TabID != "w6W:t4" {
					t.Errorf("first pane = %+v", pane)
				}
				if pane.Agent.ValueOrZero() != "codex" {
					t.Errorf("agent = %v, want codex", pane.Agent)
				}
				agentSession, ok := pane.AgentSession.Get()
				if !ok || agentSession.Kind != AgentSessionRefKindID {
					t.Errorf("agent session = %+v", pane.AgentSession)
				}
				scroll, ok := pane.Scroll.Get()
				if !ok || scroll.ViewportRows == 0 {
					t.Errorf("scroll = %+v", pane.Scroll)
				}
			},
		},
		{
			file: "agent_list",
			check: func(t *testing.T, result Result) {
				list, ok := result.(*AgentListResponse)
				if !ok {
					t.Fatalf("result is %T, want *AgentListResponse", result)
				}
				if len(list.Agents) == 0 {
					t.Fatal("no agents decoded")
				}
				agent := list.Agents[0]
				if agent.TerminalID == "" || agent.PaneID == "" {
					t.Errorf("first agent = %+v", agent)
				}
				if !agent.StateChangeSeq.IsSet() {
					t.Error("state_change_seq was not decoded")
				}
			},
		},
		{
			file: "plugin_list",
			check: func(t *testing.T, result Result) {
				list, ok := result.(*PluginListResponse)
				if !ok {
					t.Fatalf("result is %T, want *PluginListResponse", result)
				}
				if len(list.Plugins) == 0 {
					t.Fatal("no plugins decoded")
				}
				plugin := list.Plugins[0]
				if plugin.PluginID == "" || plugin.Name == "" {
					t.Errorf("first plugin = %+v", plugin)
				}
				source, ok := plugin.Source.Get()
				if !ok || source.Kind.ValueOrZero() != PluginSourceKindGithub {
					t.Errorf("source = %+v, want a github source", plugin.Source)
				}
				build := plugin.Build.ValueOrZero()
				if len(build) == 0 || len(build[0].Command) == 0 {
					t.Errorf("build = %+v", plugin.Build)
				}
				if len(plugin.Platforms.ValueOrZero()) != 3 {
					t.Errorf("platforms = %v", plugin.Platforms)
				}
			},
		},
		{
			file: "plugin_action_list",
			check: func(t *testing.T, result Result) {
				list, ok := result.(*PluginActionListResponse)
				if !ok {
					t.Fatalf("result is %T, want *PluginActionListResponse", result)
				}
				if len(list.Actions) != 1 {
					t.Fatalf("decoded %d actions, want 1", len(list.Actions))
				}
				action := list.Actions[0]
				if action.ActionID != "open" || action.PluginID != "example-tools" {
					t.Errorf("action = %+v", action)
				}
				contexts := action.Contexts.ValueOrZero()
				if len(contexts) != 1 || contexts[0] != PluginActionContextGlobal {
					t.Errorf("contexts = %v", action.Contexts)
				}
			},
		},
		{
			file: "session_snapshot",
			check: func(t *testing.T, result Result) {
				snapshot, ok := result.(*SessionSnapshotResponse)
				if !ok {
					t.Fatalf("result is %T, want *SessionSnapshotResponse", result)
				}
				session := snapshot.Snapshot
				if session.Protocol != SchemaProtocol {
					t.Errorf("protocol = %d, want %d", session.Protocol, SchemaProtocol)
				}
				if len(session.Workspaces) == 0 || len(session.Tabs) == 0 || len(session.Panes) == 0 {
					t.Fatalf("snapshot is missing entries: %d workspaces, %d tabs, %d panes",
						len(session.Workspaces), len(session.Tabs), len(session.Panes))
				}
				if session.FocusedWorkspaceID.ValueOrZero() == "" {
					t.Error("focused_workspace_id was not decoded")
				}
				if len(session.Layouts) == 0 {
					t.Fatal("no layouts decoded")
				}
				layout := session.Layouts[0]
				if layout.TabID == "" || len(layout.Panes) == 0 {
					t.Errorf("layout = %+v", layout)
				}
			},
		},
		{
			file: "server_agent_manifests",
			check: func(t *testing.T, result Result) {
				status, ok := result.(*AgentManifestStatusResponse)
				if !ok {
					t.Fatalf("result is %T, want *AgentManifestStatusResponse", result)
				}
				if len(status.Manifests) == 0 {
					t.Fatal("no manifests decoded")
				}
				if status.LastCheckUnix.ValueOrZero() == 0 {
					t.Error("last_check_unix was not decoded")
				}
				manifest := status.Manifests[0]
				if manifest.Agent == "" || manifest.Source == "" {
					t.Errorf("first manifest = %+v", manifest)
				}
			},
		},
	}

	for _, c := range cases {
		t.Run(c.file, func(t *testing.T) {
			raw := readResult(t, c.file)
			result, err := DecodeResult(raw)
			if err != nil {
				t.Fatalf("DecodeResult: %v", err)
			}
			c.check(t, result)

			// Re-encoding and decoding again must reproduce the same value:
			// a field with a wrong or missing json tag would not survive.
			encoded, err := json.Marshal(result)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			again, err := DecodeResult(encoded)
			if err != nil {
				t.Fatalf("DecodeResult after Marshal: %v", err)
			}
			if !reflect.DeepEqual(result, again) {
				t.Errorf("round trip changed the result\nfirst:  %+v\nsecond: %+v", result, again)
			}
			if again.ResultType() != result.ResultType() {
				t.Errorf("result type = %q, want %q", again.ResultType(), result.ResultType())
			}
		})
	}
}

func TestDecodeResultRejectsUnknownType(t *testing.T) {
	_, err := DecodeResult(json.RawMessage(`{"type":"something_new","value":1}`))
	var unknown *UnknownResultError
	if !errors.As(err, &unknown) {
		t.Fatalf("error = %v (%T), want *UnknownResultError", err, err)
	}
	if unknown.Type != "something_new" {
		t.Errorf("type = %q, want something_new", unknown.Type)
	}
	if string(unknown.Data) != `{"type":"something_new","value":1}` {
		t.Errorf("data = %s", unknown.Data)
	}
}
