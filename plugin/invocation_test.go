package plugin

import (
	"reflect"
	"testing"

	"github.com/vika2603/herdr-client/herdr"
)

func TestEnvInvocation(t *testing.T) {
	full := `{"workspace_id":"ws-1","workspace_label":"herdr","workspace_cwd":"/repo",` +
		`"tab_id":"tab-1","tab_label":"main","focused_pane_id":"pane-1","focused_pane_agent":"claude",` +
		`"focused_pane_cwd":"/repo/sub","focused_pane_status":"working","selected_text":"go test",` +
		`"clicked_url":"https://example.test/1","link_handler_id":"issue","invocation_source":"palette",` +
		`"correlation_id":"req-7","worktree":{"checkout_path":"/wt","is_linked_worktree":true,` +
		`"repo_key":"k","repo_name":"herdr-client","repo_root":"/repo"}}`

	tests := []struct {
		name string
		env  Env
		want Invocation
	}{
		{name: "no context variable", env: Env{}},
		{name: "empty context", env: Env{ContextJSON: []byte(`{}`)}},
		{name: "context that does not decode", env: Env{ContextJSON: []byte(`{"workspace_id":7}`)}},
		{
			name: "only the fields Herdr sent",
			env:  Env{ContextJSON: []byte(`{"workspace_id":"ws-1","selected_text":"go test"}`)},
			want: Invocation{WorkspaceID: "ws-1", SelectedText: "go test"},
		},
		{
			name: "every field",
			env:  Env{ContextJSON: []byte(full)},
			want: Invocation{
				WorkspaceID:       "ws-1",
				WorkspaceLabel:    "herdr",
				WorkspaceCwd:      "/repo",
				TabID:             "tab-1",
				TabLabel:          "main",
				FocusedPaneID:     "pane-1",
				FocusedPaneAgent:  "claude",
				FocusedPaneCwd:    "/repo/sub",
				FocusedPaneStatus: herdr.AgentStatusWorking,
				SelectedText:      "go test",
				ClickedURL:        "https://example.test/1",
				LinkHandlerID:     "issue",
				InvocationSource:  "palette",
				CorrelationID:     "req-7",
				Worktree: herdr.Some(herdr.WorkspaceWorktreeInfo{
					CheckoutPath:     "/wt",
					IsLinkedWorktree: true,
					RepoKey:          "k",
					RepoName:         "herdr-client",
					RepoRoot:         "/repo",
				}),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.env.Invocation(); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Invocation() = %+v, want %+v", got, tt.want)
			}
		})
	}
}
