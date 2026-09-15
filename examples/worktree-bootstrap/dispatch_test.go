package main

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/vika2603/herdr-client/herdr"
	"github.com/vika2603/herdr-client/plugin/plugintest"
)

// The two tests below stop before the first request: each gives the handler
// something it rejects while deciding, which is what lets them cover the
// wiring from the environment Herdr injects to the URL the handler works on.
// TestBootstrapCallsInOrder covers the calls themselves, against a socket
// plugintest controls.

func TestDispatchThroughTheLinkHandler(t *testing.T) {
	env := plugintest.Env(
		plugintest.Action(actionBootstrap),
		plugintest.LinkHandler("issue"),
		plugintest.ClickedURL("https://example.com/vika2603/herdr-client/issues/7"),
		plugintest.Workspace("ws-1"),
	)

	err := newPlugin().Dispatch(context.Background(), env)
	if err == nil {
		t.Fatal("Dispatch() error = nil, want the clicked URL rejected")
	}
	if !strings.Contains(err.Error(), "example.com") {
		t.Errorf("Dispatch() error = %v, want it to name the clicked URL", err)
	}
}

func TestDispatchOnASelection(t *testing.T) {
	env := plugintest.Env(
		plugintest.Action(actionBootstrap),
		plugintest.SelectedText("release/2.0"),
		plugintest.Workspace("ws-1"),
	)

	err := newPlugin().Dispatch(context.Background(), env)
	if err == nil {
		t.Fatal("Dispatch() error = nil, want the selection rejected")
	}
	if !strings.Contains(err.Error(), "release/2.0") {
		t.Errorf("Dispatch() error = %v, want it to name the selection", err)
	}
}

// A link handler and a selection can both be present, since Herdr sets the
// invocation context either way. The link handler is what invoked the action,
// so its URL wins.
func TestClickedURLPrefersTheLinkHandler(t *testing.T) {
	clicked := plugintest.Env(
		plugintest.LinkHandler("issue"),
		plugintest.ClickedURL(issueURL),
		plugintest.SelectedText("release/2.0"),
	)
	if got := clickedURL(clicked); got != issueURL {
		t.Errorf("clickedURL() = %q, want the clicked URL %q", got, issueURL)
	}

	selected := plugintest.Env(plugintest.SelectedText(issueURL))
	if got := clickedURL(selected); got != issueURL {
		t.Errorf("clickedURL() = %q, want the selection %q", got, issueURL)
	}
}

// Herdr offers the action on a selection in any pane, including one outside a
// workspace, and worktree.create needs the workspace to find the repository.
func TestBootstrapNeedsAWorkspace(t *testing.T) {
	env := plugintest.Env(plugintest.Action(actionBootstrap), plugintest.SelectedText(issueURL))

	err := newPlugin().Dispatch(context.Background(), env)
	if err == nil || !strings.Contains(err.Error(), "workspace") {
		t.Errorf("Dispatch() error = %v, want it to report the missing workspace", err)
	}
}

// An action id the binary does not serve is a dispatch error rather than a
// silent success, which is what ties herdr-plugin.toml to the registry.
func TestDispatchRejectsAnUnregisteredAction(t *testing.T) {
	err := newPlugin().Dispatch(context.Background(), plugintest.Env(plugintest.Action("open")))
	if err == nil || !strings.Contains(err.Error(), "open") {
		t.Errorf("Dispatch() error = %v, want no handler for %q", err, "open")
	}
}

// The whole sequence, against a socket the test controls: every call has to
// take its ids from the response before it, which is what a test stopping at
// the first request cannot check.
func TestBootstrapCallsInOrder(t *testing.T) {
	const issue = "https://github.com/vika2603/herdr-client/issues/7"
	agentPane := herdr.PaneInfo{PaneID: "w9:p1", WorkspaceID: "w9", TabID: "w9:t1"}

	server := plugintest.NewServer(t).
		Reply(herdr.MethodWorktreeCreate, herdr.WorktreeCreatedResponse{
			Workspace: herdr.WorkspaceInfo{WorkspaceID: "w9"},
			Tab:       herdr.TabInfo{TabID: "w9:t1", WorkspaceID: "w9"},
			Worktree:  herdr.WorktreeInfo{Path: "/checkouts/issue-7", Branch: herdr.Some("issue-7")},
		}).
		Reply(herdr.MethodWorkspaceReportMetadata, herdr.OKResponse{}).
		Reply(herdr.MethodLayoutApply, herdr.LayoutApplyResponse{
			Layout: herdr.LayoutDescription{
				TabID:       "w9:t1",
				WorkspaceID: "w9",
				Root: herdr.LayoutNodeSplit{
					Direction: herdr.SplitDirectionRight,
					Ratio:     0.6,
					First:     herdr.LayoutNodePane{Label: herdr.Some("shell"), PaneID: herdr.Some("w9:p2")},
					Second:    herdr.LayoutNodePane{Label: herdr.Some("issue-7"), PaneID: herdr.Some(agentPane.PaneID)},
				},
			},
		}).
		Reply(herdr.MethodAgentStart, herdr.AgentStartedResponse{
			Agent: herdr.AgentInfo{PaneID: agentPane.PaneID, TabID: agentPane.TabID, WorkspaceID: "w9"},
		}).
		Reply(herdr.MethodAgentPrompt, herdr.AgentPromptedResponse{})

	env := server.Env(
		plugintest.Action(actionBootstrap),
		plugintest.LinkHandler("issue"),
		plugintest.ClickedURL(issue),
		plugintest.Workspace("ws-1"),
	)
	if err := newPlugin().Dispatch(context.Background(), env); err != nil {
		t.Fatalf("Dispatch() = %v", err)
	}

	want := []string{
		herdr.MethodWorktreeCreate,
		herdr.MethodWorkspaceReportMetadata,
		herdr.MethodLayoutApply,
		herdr.MethodAgentStart,
		herdr.MethodAgentPrompt,
	}
	calls := server.Calls()
	if got := server.Methods(); !slices.Equal(got, want) {
		t.Fatalf("called %v, want %v", got, want)
	}

	var worktree herdr.WorktreeCreateParams
	decode(t, calls[0].Params, &worktree)
	if worktree.Branch.ValueOrZero() != "issue-7" || worktree.WorkspaceID.ValueOrZero() != "ws-1" {
		t.Errorf("worktree.create asked for %+v, want branch issue-7 in the invoking workspace", worktree)
	}

	var metadata herdr.WorkspaceReportMetadataParams
	decode(t, calls[1].Params, &metadata)
	if metadata.WorkspaceID != "w9" || herdr.Value(metadata.Tokens["issue"]) != issue {
		t.Errorf("workspace.report_metadata sent %+v, want the issue on the new workspace", metadata)
	}

	var layout herdr.LayoutApplyParams
	decode(t, calls[2].Params, &layout)
	if layout.TabID.ValueOrZero() != "w9:t1" {
		t.Errorf("layout.apply targeted %q, want the tab the worktree opened", layout.TabID.ValueOrZero())
	}

	// The agent starts in the pane carrying the label the layout asked for,
	// which is the second leaf here rather than the first.
	var start herdr.AgentStartParams
	decode(t, calls[3].Params, &start)
	if start.PaneID != agentPane.PaneID || start.Name != "issue-7" {
		t.Errorf("agent.start sent %+v, want the labelled pane", start)
	}

	var prompt herdr.AgentPromptParams
	decode(t, calls[4].Params, &prompt)
	if prompt.Target != agentPane.PaneID || !strings.Contains(prompt.Text, issue) {
		t.Errorf("agent.prompt sent %+v, want the issue URL to the agent pane", prompt)
	}
}

func decode(t *testing.T, params json.RawMessage, into any) {
	t.Helper()
	if err := json.Unmarshal(params, into); err != nil {
		t.Fatalf("decode params %s: %v", params, err)
	}
}
