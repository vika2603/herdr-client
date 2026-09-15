package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/vika2603/herdr-client/herdr"
	"github.com/vika2603/herdr-client/plugin/manifest"
	"github.com/vika2603/herdr-client/plugin/plugintest"
)

const issueURL = "https://github.com/vika2603/herdr-client/issues/7"

func TestNewPlan(t *testing.T) {
	custom := config{AgentKind: "codex", Prompt: "Fix " + urlPlaceholder + " now."}

	tests := []struct {
		name       string
		url        string
		cfg        config
		wantBranch string
		wantKind   string
		wantPrompt string
	}{{
		name:       "issue",
		url:        issueURL,
		cfg:        defaultConfig(),
		wantBranch: "issue-7",
		wantKind:   "claude",
		wantPrompt: "Read " + issueURL + " and propose a plan before you change anything.",
	}, {
		name:       "pull request",
		url:        "https://github.com/vika2603/herdr-client/pull/42",
		cfg:        custom,
		wantBranch: "pr-42",
		wantKind:   "codex",
		wantPrompt: "Fix https://github.com/vika2603/herdr-client/pull/42 now.",
	}, {
		// The branch comes from the number alone, while the prompt and the
		// workspace token keep the URL the user actually clicked.
		name:       "issue link with a fragment",
		url:        issueURL + "#issuecomment-1",
		cfg:        custom,
		wantBranch: "issue-7",
		wantKind:   "codex",
		wantPrompt: "Fix " + issueURL + "#issuecomment-1 now.",
	}}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			p, err := newPlan(test.url, test.cfg)
			if err != nil {
				t.Fatalf("newPlan() error = %v", err)
			}
			if got := p.Worktree.Branch.ValueOrZero(); got != test.wantBranch {
				t.Errorf("branch = %q, want %q", got, test.wantBranch)
			}
			if !p.Worktree.Focus.ValueOrZero() {
				t.Error("worktree.create is asked not to focus the new workspace")
			}
			if p.Agent.Kind != test.wantKind {
				t.Errorf("agent kind = %q, want %q", p.Agent.Kind, test.wantKind)
			}
			if p.Agent.Name != test.wantBranch {
				t.Errorf("agent name = %q, want the branch %q", p.Agent.Name, test.wantBranch)
			}
			if p.Prompt.Text != test.wantPrompt {
				t.Errorf("prompt = %q, want %q", p.Prompt.Text, test.wantPrompt)
			}
			if got := herdr.Value(p.Metadata.Tokens["issue"]); got != test.url {
				t.Errorf("issue token = %q, want %q", got, test.url)
			}
			// Comparing against the constant would only restate the
			// assignment. Herdr attributes reported tokens to their source,
			// so what matters is that it is this plugin's own id.
			if p.Metadata.Source != manifestPluginID(t) {
				t.Errorf("metadata source = %q, want the plugin id %q", p.Metadata.Source, manifestPluginID(t))
			}
		})
	}
}

func TestNewPlanRejectsWhatIsNotAnIssue(t *testing.T) {
	for _, url := range []string{"", "https://github.com/vika2603/herdr-client", "not a url"} {
		if _, err := newPlan(url, defaultConfig()); err == nil {
			t.Errorf("newPlan(%q) error = nil, want one", url)
		}
	}
}

func TestPlanLayoutUsesTheCheckout(t *testing.T) {
	p, err := newPlan(issueURL, defaultConfig())
	if err != nil {
		t.Fatalf("newPlan() error = %v", err)
	}
	created := &herdr.WorktreeCreatedResponse{
		Tab:      herdr.TabInfo{TabID: "tab-1"},
		Worktree: herdr.WorktreeInfo{Path: "/repo/.worktrees/issue-7"},
	}

	params := p.layout(created)
	if got := params.TabID.ValueOrZero(); got != "tab-1" {
		t.Errorf("tab_id = %q, want the tab worktree.create opened", got)
	}
	split, ok := params.Root.(herdr.LayoutNodeSplit)
	if !ok {
		t.Fatalf("root = %T, want a split", params.Root)
	}
	for name, child := range map[string]herdr.LayoutNode{"first": split.First, "second": split.Second} {
		pane, ok := child.(herdr.LayoutNodePane)
		if !ok {
			t.Fatalf("%s child = %T, want a pane", name, child)
		}
		if got := pane.Cwd.ValueOrZero(); got != created.Worktree.Path {
			t.Errorf("%s pane cwd = %q, want the checkout %q", name, got, created.Worktree.Path)
		}
	}
	agent, ok := split.First.(herdr.LayoutNodePane)
	if !ok {
		t.Fatalf("first child = %T, want a pane", split.First)
	}
	if got := agent.Label.ValueOrZero(); got != "issue-7" {
		t.Errorf("agent pane label = %q, want the agent name", got)
	}
}

// agentPaneID reads a layout as layout.apply answers it, where every pane
// carries the id the server assigned and the labels are the ones the request
// asked for. The agent pane is found by its label, so a layout.apply that
// reorders the tree still starts the agent in the right pane.
func TestAgentPaneID(t *testing.T) {
	applied := herdr.LayoutNodeSplit{
		Direction: herdr.SplitDirectionRight,
		Ratio:     0.6,
		First: herdr.LayoutNodeSplit{
			Direction: herdr.SplitDirectionDown,
			Ratio:     0.5,
			First:     herdr.LayoutNodePane{Label: herdr.Some("shell"), PaneID: herdr.Some("pane-1")},
			Second:    herdr.LayoutNodePane{Label: herdr.Some("issue-7"), PaneID: herdr.Some("pane-2")},
		},
		Second: herdr.LayoutNodePane{Label: herdr.Some("notes"), PaneID: herdr.Some("pane-3")},
	}
	if got := agentPaneID(applied, "issue-7"); got != "pane-2" {
		t.Errorf("agentPaneID() = %q, want the pane labelled for the agent", got)
	}
	if got := agentPaneID(applied, "absent"); got != "" {
		t.Errorf("agentPaneID() = %q for a label no pane carries, want the empty string", got)
	}
}

func TestLoadConfig(t *testing.T) {
	// Run by hand outside Herdr there is no configuration directory at all.
	if cfg, err := loadConfig(plugintest.Env()); err != nil || cfg != defaultConfig() {
		t.Errorf("loadConfig() = %+v, %v, want the defaults", cfg, err)
	}

	dir := t.TempDir()
	env := plugintest.Env(plugintest.ConfigDir(dir))
	if cfg, err := loadConfig(env); err != nil || cfg != defaultConfig() {
		t.Errorf("loadConfig() without a file = %+v, %v, want the defaults", cfg, err)
	}

	// A file that sets one field leaves the other at its default.
	write(t, dir, `{"agent_kind":"codex"}`)
	cfg, err := loadConfig(env)
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if cfg.AgentKind != "codex" || cfg.Prompt != defaultConfig().Prompt {
		t.Errorf("loadConfig() = %+v", cfg)
	}

	write(t, dir, "not json")
	if _, err := loadConfig(env); err == nil {
		t.Error("loadConfig() error = nil for a file that does not decode, want one")
	}
}

func write(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, configName), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// manifestPluginID reads the id herdr-plugin.toml declares, so a source
// constant that drifts from the manifest is a test failure rather than a
// mis-attributed token at run time.
func manifestPluginID(t *testing.T) string {
	t.Helper()
	parsed, _, err := manifest.Parse(manifestPath)
	if err != nil {
		t.Fatalf("%s: %v", manifestPath, err)
	}
	return parsed.ID
}
