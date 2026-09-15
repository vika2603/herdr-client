// Command herdr-worktree-bootstrap is a worked example of a Herdr plugin that
// turns one gesture into a ready workspace: a git worktree for the branch an
// issue names, a two-pane arrangement in it, a coding agent in one of the
// panes and the agent's first prompt.
//
// The gesture is either a click on an issue or pull request link in a pane,
// which the [[link_handlers]] entry routes to the action, or the action
// invoked on a selected URL. See README.md.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/vika2603/herdr-client/herdr"
	"github.com/vika2603/herdr-client/plugin"
)

// actionBootstrap is the action id herdr-plugin.toml declares, both as an
// [[actions]] entry and as the action its [[link_handlers]] entry names.
const actionBootstrap = "bootstrap"

// linkPattern is the [[link_handlers]] pattern. It lives here as well so the
// URLs Herdr offers the handler for and the URLs this binary can parse cannot
// drift apart; TestLinkPatternMatchesTheManifest checks the two against each
// other. Herdr matches it with the Rust regex crate, so the pattern stays
// within the syntax both engines read the same way.
const linkPattern = `^https://github\.com/[^/]+/[^/]+/(issues|pull)/([0-9]+)\b`

var issueLink = regexp.MustCompile(linkPattern)

// metadataSource identifies this plugin as the authority for the workspace
// tokens it reports, which is what lets Herdr attribute and expire them.
const metadataSource = "example.worktree-bootstrap"

// configName is the configuration file inside HERDR_PLUGIN_CONFIG_DIR.
const configName = "config.json"

// urlPlaceholder is replaced with the issue URL in the configured prompt.
const urlPlaceholder = "{{url}}"

// config is the user-editable part of the bootstrap. Both fields have working
// defaults, so a plugin the user has not configured still bootstraps.
type config struct {
	// AgentKind is a Herdr agent kind, such as "claude" or "codex".
	AgentKind string `json:"agent_kind"`
	// Prompt is the first prompt the agent receives.
	Prompt string `json:"prompt"`
}

func defaultConfig() config {
	return config{
		AgentKind: "claude",
		Prompt:    "Read " + urlPlaceholder + " and propose a plan before you change anything.",
	}
}

func main() {
	os.Exit(newPlugin().Run(context.Background()))
}

// newPlugin registers one handler per entrypoint of herdr-plugin.toml.
// TestManifest checks the two against each other. A link handler needs no
// registration of its own: it invokes the action it names.
func newPlugin() *plugin.Plugin {
	p := plugin.New()
	p.Action(actionBootstrap, onBootstrap)
	return p
}

// onBootstrap runs the whole sequence for the URL the gesture carried.
func onBootstrap(ctx context.Context, env *plugin.Env) error {
	cfg, err := loadConfig(env)
	if err != nil {
		return err
	}
	p, err := newPlan(clickedURL(env), cfg)
	if err != nil {
		return err
	}
	workspace := env.Invocation().WorkspaceID
	if workspace == "" {
		return errors.New("bootstrap needs the workspace whose repository the worktree comes from")
	}
	return bootstrap(ctx, env.Client(), workspace, p)
}

// clickedURL returns the URL the action was invoked for: the one the link
// handler reports, and otherwise the terminal selection, which is how the
// action reaches the same work when no link was clicked.
func clickedURL(env *plugin.Env) string {
	if env.LinkHandlerID != "" {
		return env.ClickedURL
	}
	return env.Invocation().SelectedText
}

// plan is the request sequence one gesture turns into, decided before any
// call is made, so that everything that can be wrong about a bootstrap is
// wrong before the first request. Each request is left missing the one id it
// cannot know yet, which bootstrap fills in: the workspace the action was
// invoked from for the first call, and the response of the previous call for
// the rest. The layout is built by a method instead, because its panes need
// the checkout path only worktree.create can report.
type plan struct {
	Worktree herdr.WorktreeCreateParams
	Metadata herdr.WorkspaceReportMetadataParams
	Agent    herdr.AgentStartParams
	Prompt   herdr.AgentPromptParams
}

// newPlan derives the branch from the issue or pull request the URL names and
// takes the agent and its first prompt from the configuration.
func newPlan(url string, cfg config) (plan, error) {
	match := issueLink.FindStringSubmatch(url)
	if match == nil {
		return plan{}, fmt.Errorf("%q is not a GitHub issue or pull request URL", url)
	}
	branch := "issue-" + match[2]
	if match[1] == "pull" {
		branch = "pr-" + match[2]
	}
	return plan{
		Worktree: herdr.WorktreeCreateParams{
			Branch: herdr.Some(branch),
			Focus:  herdr.Some(true),
		},
		Metadata: herdr.WorkspaceReportMetadataParams{
			Source: metadataSource,
			Tokens: map[string]*string{"issue": herdr.Ptr(url)},
		},
		// The agent name is what "herdr agent" targets by hand later, so it
		// names the branch rather than the plugin.
		Agent:  herdr.AgentStartParams{Kind: cfg.AgentKind, Name: branch},
		Prompt: herdr.AgentPromptParams{Text: strings.ReplaceAll(cfg.Prompt, urlPlaceholder, url)},
	}, nil
}

// layout is the arrangement to apply to the tab the worktree opened: the
// agent pane beside a shell in the same checkout. Herdr does not give a new
// pane the working directory of whatever created it, so both panes name the
// checkout explicitly.
func (p plan) layout(created *herdr.WorktreeCreatedResponse) herdr.LayoutApplyParams {
	checkout := herdr.Some(created.Worktree.Path)
	return herdr.LayoutApplyParams{
		// The worktree opens as a single pane in a tab of its own, so the
		// arrangement replaces that tab rather than adding a second one.
		TabID: herdr.Some(created.Tab.TabID),
		Focus: herdr.Some(true),
		Root: herdr.LayoutNodeSplit{
			Direction: herdr.SplitDirectionRight,
			Ratio:     0.6,
			First:     herdr.LayoutNodePane{Label: herdr.Some(p.Agent.Name), Cwd: checkout},
			Second:    herdr.LayoutNodePane{Label: herdr.Some("shell"), Cwd: checkout},
		},
	}
}

// bootstrap makes the calls the plan decided on, taking every id from the
// response that carries it.
func bootstrap(ctx context.Context, client *herdr.Client, workspaceID string, p plan) error {
	// The workspace names the repository the worktree branches from; without
	// it the server resolves a repository from its own working directory.
	p.Worktree.WorkspaceID = herdr.Some(workspaceID)
	created, err := client.WorktreeCreate(ctx, p.Worktree)
	if err != nil {
		return err
	}

	// Reported before the panes exist, so a workspace whose agent failed to
	// start still says which issue it was opened for.
	p.Metadata.WorkspaceID = created.Workspace.WorkspaceID
	if _, err := client.WorkspaceReportMetadata(ctx, p.Metadata); err != nil {
		return err
	}

	applied, err := client.LayoutApply(ctx, p.layout(created))
	if err != nil {
		return err
	}
	p.Agent.PaneID = agentPaneID(applied.Layout.Root, p.Agent.Name)
	if p.Agent.PaneID == "" {
		return errors.New("layout.apply returned no pane to start the agent in")
	}
	// agent.start answers only once Herdr has detected the agent in the pane
	// and considers it ready for input, which the response reports as
	// interactive_ready, so the prompt needs no wait of its own.
	started, err := client.AgentStart(ctx, p.Agent)
	if err != nil {
		return err
	}

	// agent.prompt targets a unique agent name or a pane that hosts an agent.
	// The pane is the one id that cannot collide with another session's.
	p.Prompt.Target = started.Agent.PaneID
	_, err = client.AgentPrompt(ctx, p.Prompt)
	return err
}

// agentPaneID returns the id of the pane plan.layout labelled for the agent.
// Pane ids are assigned by layout.apply, so they can only be read out of its
// answer, and the label rather than the position is what identifies the pane.
func agentPaneID(root herdr.LayoutNode, label string) string {
	for _, pane := range herdr.LayoutPanes(root) {
		if pane.Label.ValueOrZero() == label {
			return pane.PaneID.ValueOrZero()
		}
	}
	return ""
}

// loadConfig reads the configuration file over the defaults. ReadConfigJSON
// leaves them in place when the file is absent, which is the normal case, and
// when it sets only some of the fields.
func loadConfig(env *plugin.Env) (config, error) {
	cfg := defaultConfig()
	err := env.ReadConfigJSON(configName, &cfg)
	if errors.Is(err, plugin.ErrNoConfigDir) {
		// The command was run by hand rather than by Herdr.
		return cfg, nil
	}
	return cfg, err
}
