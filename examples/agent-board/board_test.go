package main

import (
	"strings"
	"testing"

	"github.com/vika2603/herdr-client/herdr"
)

// session is the frame the layout cases vary: two workspaces, three tabs and
// one agent per tab, with a pane for two of the three agents.
func session() board {
	return board{
		Workspaces: []herdr.WorkspaceInfo{
			{WorkspaceID: "ws-1", Number: 1, Label: "main", TabCount: 2, PaneCount: 3, AgentStatus: herdr.AgentStatusWorking, Focused: true},
			{WorkspaceID: "ws-2", Number: 2, Label: "review", TabCount: 1, PaneCount: 1, AgentStatus: herdr.AgentStatusIdle},
		},
		Tabs: []herdr.TabInfo{
			{TabID: "tab-1", WorkspaceID: "ws-1", Number: 1, Label: "server", AgentStatus: herdr.AgentStatusWorking, Focused: true},
			{TabID: "tab-2", WorkspaceID: "ws-1", Number: 2, Label: "client", AgentStatus: herdr.AgentStatusBlocked},
			{TabID: "tab-3", WorkspaceID: "ws-2", Number: 1, Label: "diff", AgentStatus: herdr.AgentStatusIdle},
		},
		Agents: []herdr.AgentInfo{
			{PaneID: "pane-1", TabID: "tab-1", WorkspaceID: "ws-1", DisplayAgent: herdr.Some("claude"), AgentStatus: herdr.AgentStatusWorking, Focused: true},
			{PaneID: "pane-2", TabID: "tab-2", WorkspaceID: "ws-1", Agent: herdr.Some("codex"), Name: herdr.Some("reviewer"), AgentStatus: herdr.AgentStatusBlocked},
			{PaneID: "pane-3", TabID: "tab-3", WorkspaceID: "ws-2", AgentStatus: herdr.AgentStatusIdle},
		},
		Panes: map[string]herdr.PaneInfo{
			"pane-1": {PaneID: "pane-1", Label: herdr.Some("api")},
			"pane-2": {PaneID: "pane-2"},
		},
	}
}

func TestRender(t *testing.T) {
	withNote := session()
	withNote.Note = "resynced from a new snapshot after herdr: stream closed"

	tests := []struct {
		name  string
		frame board
		want  string
	}{
		{
			name:  "a session the mirror holds nothing of",
			frame: board{},
			want: `agent board  workspaces 0  agents 0  events 0

the session has no workspaces
`,
		},
		{
			name:  "workspaces, their tabs and the agents in them",
			frame: session(),
			want: `agent board  workspaces 2  agents 3  events 0

> ws 1 main  tabs 2  panes 3  [working]
  > tab 1 server  [working]
    > claude       working   api
    tab 2 client  [blocked]
      reviewer     blocked   pane-2

  ws 2 review  tabs 1  panes 1  [idle]
    tab 1 diff  [idle]
      agent        idle      pane-3
`,
		},
		{
			name:  "a note above the board",
			frame: withNote,
			want: `agent board  workspaces 2  agents 3  events 0
resynced from a new snapshot after herdr: stream closed

> ws 1 main  tabs 2  panes 3  [working]
  > tab 1 server  [working]
    > claude       working   api
    tab 2 client  [blocked]
      reviewer     blocked   pane-2

  ws 2 review  tabs 1  panes 1  [idle]
    tab 1 diff  [idle]
      agent        idle      pane-3
`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := render(test.frame); got != test.want {
				t.Errorf("render() =\n%s\nwant\n%s", got, test.want)
			}
		})
	}
}

// The event count describes the events the frame on screen was built from, so
// a resync, which replaces the mirror with a fresh snapshot, starts it again.
func TestAfterCountsEventsUntilAResync(t *testing.T) {
	focused := &herdr.PaneFocusedEvent{PaneID: "pane-1", WorkspaceID: "ws-1"}

	frame := board{}
	for range 3 {
		frame = frame.after(focused)
	}
	if frame.Events != 3 || frame.Note != "" {
		t.Fatalf("after three events: events = %d, note = %q", frame.Events, frame.Note)
	}

	frame = frame.after(&herdr.ResyncEvent{Cause: herdr.ErrStreamClosed})
	if frame.Events != 0 {
		t.Errorf("events after a resync = %d, want the count to start again", frame.Events)
	}
	if !strings.Contains(frame.Note, herdr.ErrStreamClosed.Error()) {
		t.Errorf("note after a resync = %q, want the cause in it", frame.Note)
	}

	frame = frame.after(focused)
	if frame.Events != 1 || frame.Note != "" {
		t.Errorf("after the resync: events = %d, note = %q, want the note gone", frame.Events, frame.Note)
	}
}

func TestPaneLabelFallsBackToThePaneID(t *testing.T) {
	frame := session()
	if got := frame.paneLabel("pane-1"); got != "api" {
		t.Errorf("paneLabel(pane-1) = %q, want the pane label", got)
	}
	if got := frame.paneLabel("pane-2"); got != "pane-2" {
		t.Errorf("paneLabel(pane-2) = %q, want the id of a pane without a label", got)
	}
	if got := frame.paneLabel("pane-3"); got != "pane-3" {
		t.Errorf("paneLabel(pane-3) = %q, want the id of a pane the mirror does not hold", got)
	}
}

func TestDrawClearsTheScreenBeforeTheFrame(t *testing.T) {
	var out strings.Builder
	draw(&out, board{})
	if !strings.HasPrefix(out.String(), clearScreen) {
		t.Errorf("draw() wrote %q, want the clear sequence first", out.String())
	}
	if !strings.HasSuffix(out.String(), render(board{})) {
		t.Error("draw() did not write the rendered frame")
	}
}
