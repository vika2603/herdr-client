package herdr

import (
	"maps"
	"slices"
)

// cloneEach detaches references in an already owned slice, such as the fresh
// projection returned by orderedCollection.values.
func cloneEach[T any](values []T, clone func(T) T) []T {
	for i, value := range values {
		values[i] = clone(value)
	}
	return values
}

// clonePtr is used only for scalar values and structs without references.
func clonePtr[T any](value *T) *T {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneWorkspace(value WorkspaceInfo) WorkspaceInfo {
	value.Tokens = maps.Clone(value.Tokens)
	value.Worktree = clonePtr(value.Worktree)
	return value
}

func cloneTab(value TabInfo) TabInfo { return value }

func clonePane(value PaneInfo) PaneInfo {
	value.Agent = clonePtr(value.Agent)
	value.AgentSession = clonePtr(value.AgentSession)
	value.Cwd = clonePtr(value.Cwd)
	value.DisplayAgent = clonePtr(value.DisplayAgent)
	value.ForegroundCwd = clonePtr(value.ForegroundCwd)
	value.Label = clonePtr(value.Label)
	value.Scroll = clonePtr(value.Scroll)
	value.StateLabels = maps.Clone(value.StateLabels)
	value.TerminalTitle = clonePtr(value.TerminalTitle)
	value.TerminalTitleStripped = clonePtr(value.TerminalTitleStripped)
	value.Title = clonePtr(value.Title)
	value.Tokens = maps.Clone(value.Tokens)
	return value
}

func cloneAgent(value AgentInfo) AgentInfo {
	value.Agent = clonePtr(value.Agent)
	value.AgentSession = clonePtr(value.AgentSession)
	value.Cwd = clonePtr(value.Cwd)
	value.DisplayAgent = clonePtr(value.DisplayAgent)
	value.ForegroundCwd = clonePtr(value.ForegroundCwd)
	value.InteractiveReady = clonePtr(value.InteractiveReady)
	value.LaunchPending = clonePtr(value.LaunchPending)
	value.Name = clonePtr(value.Name)
	value.ScreenDetectionSkipped = clonePtr(value.ScreenDetectionSkipped)
	value.StateChangeSeq = clonePtr(value.StateChangeSeq)
	value.StateLabels = maps.Clone(value.StateLabels)
	value.TerminalTitle = clonePtr(value.TerminalTitle)
	value.TerminalTitleStripped = clonePtr(value.TerminalTitleStripped)
	value.Title = clonePtr(value.Title)
	value.Tokens = maps.Clone(value.Tokens)
	return value
}

func cloneLayout(value PaneLayoutSnapshot) PaneLayoutSnapshot {
	value.Panes = slices.Clone(value.Panes)
	value.Splits = slices.Clone(value.Splits)
	return value
}
