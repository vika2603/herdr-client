package plugin

import "github.com/vika2603/herdr-client/herdr"

// Invocation is the invocation context Herdr passed, with its optional scalar
// fields flattened to values: a field Herdr did not send reads as its zero
// value.
//
// A plugin reading its own invocation context almost never needs to tell an
// absent field from an empty one; the optional form stays available through
// Env.Context.
type Invocation struct {
	// WorkspaceID, WorkspaceLabel and WorkspaceCwd describe the workspace the
	// entrypoint was invoked from.
	WorkspaceID    string
	WorkspaceLabel string
	WorkspaceCwd   string
	// TabID and TabLabel describe its tab.
	TabID    string
	TabLabel string
	// FocusedPane* describe the focused pane and the agent in it, if any.
	FocusedPaneID     string
	FocusedPaneAgent  string
	FocusedPaneCwd    string
	FocusedPaneStatus herdr.AgentStatus
	// SelectedText is the terminal selection an action in the "selection"
	// context was invoked on.
	SelectedText string
	// ClickedURL and LinkHandlerID are set when a link handler invoked the
	// action.
	ClickedURL    string
	LinkHandlerID string
	// InvocationSource records how the entrypoint was reached and
	// CorrelationID ties the run to the API request that caused it.
	InvocationSource string
	CorrelationID    string
	// Worktree is the git worktree behind the workspace when it has one.
	Worktree herdr.Optional[herdr.WorkspaceWorktreeInfo]
}

// Invocation decodes the invocation context, reporting every field Herdr did
// not pass as empty.
//
// It reports no error: an entrypoint invoked without a context and a context
// that does not decode both yield the zero Invocation, because a plugin
// reading a single field can do nothing else with either. Use Context when
// the difference matters.
func (e *Env) Invocation() Invocation {
	context, err := e.Context()
	if err != nil {
		return Invocation{}
	}
	return Invocation{
		WorkspaceID:       context.WorkspaceID.ValueOrZero(),
		WorkspaceLabel:    context.WorkspaceLabel.ValueOrZero(),
		WorkspaceCwd:      context.WorkspaceCwd.ValueOrZero(),
		TabID:             context.TabID.ValueOrZero(),
		TabLabel:          context.TabLabel.ValueOrZero(),
		FocusedPaneID:     context.FocusedPaneID.ValueOrZero(),
		FocusedPaneAgent:  context.FocusedPaneAgent.ValueOrZero(),
		FocusedPaneCwd:    context.FocusedPaneCwd.ValueOrZero(),
		FocusedPaneStatus: context.FocusedPaneStatus.ValueOrZero(),
		SelectedText:      context.SelectedText.ValueOrZero(),
		ClickedURL:        context.ClickedURL.ValueOrZero(),
		LinkHandlerID:     context.LinkHandlerID.ValueOrZero(),
		InvocationSource:  context.InvocationSource.ValueOrZero(),
		CorrelationID:     context.CorrelationID.ValueOrZero(),
		Worktree:          context.Worktree,
	}
}
