package gen

import "testing"

func TestPascal(t *testing.T) {
	cases := []struct{ in, want string }{
		{"id", "ID"},
		{"pane_id", "PaneID"},
		{"url", "URL"},
		{"clicked_url", "ClickedURL"},
		{"strip_ansi", "StripANSI"},
		{"ttl_ms", "TTLMs"},
		{"shell_pid", "ShellPID"},
		{"tty", "TTY"},
		{"cwd", "Cwd"},
		{"data_base64", "DataBase64"},
		{"argv0", "Argv0"},
		{"cell_width_px", "CellWidthPx"},
		{"foreground_process_group_id", "ForegroundProcessGroupID"},
		{"recent_unwrapped", "RecentUnwrapped"},
		{"top-left", "TopLeft"},
		{"antigravity_cli", "AntigravityCLI"},
		{"pane.input.set", "PaneInputSet"},
		{"pane.output_matched", "PaneOutputMatched"},
		{"ok", "OK"},
		{"pong", "Pong"},
	}
	for _, c := range cases {
		if got := pascal(c.in); got != c.want {
			t.Errorf("pascal(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestEnumConstName(t *testing.T) {
	cases := []struct {
		typeName, value, want string
	}{
		{"AgentStatus", "idle", "AgentStatusIdle"},
		{"ReadSource", "recent_unwrapped", "ReadSourceRecentUnwrapped"},
		{"ReadFormat", "ansi", "ReadFormatANSI"},
		{"ToastHerdrPosition", "top-left", "ToastHerdrPositionTopLeft"},
		{"IntegrationTarget", "antigravity_cli", "IntegrationTargetAntigravityCLI"},
		{"EventKind", "pane_agent_status_changed", "EventKindPaneAgentStatusChanged"},
	}
	for _, c := range cases {
		if got := enumConstName(c.typeName, c.value); got != c.want {
			t.Errorf("enumConstName(%q, %q) = %q, want %q", c.typeName, c.value, got, c.want)
		}
	}
}

func TestDotName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"pane_created", "pane.created"},
		{"workspace_metadata_updated", "workspace.metadata_updated"},
		{"pane_agent_status_changed", "pane.agent_status_changed"},
		{"layout_updated", "layout.updated"},
	}
	for _, c := range cases {
		if got := dotName(c.in); got != c.want {
			t.Errorf("dotName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
