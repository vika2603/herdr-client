package herdr_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/vika2603/herdr-client/herdr"
	"github.com/vika2603/herdr-client/herdrtest"
)

func BenchmarkSessionSnapshot(b *testing.B) {
	for _, paneCount := range []int{1, 15, 100} {
		b.Run(fmt.Sprintf("panes=%d", paneCount), func(b *testing.B) {
			snapshot := benchmarkSnapshot(paneCount)
			server := herdrtest.NewServer(b).
				AllowSubscriptions().
				Reply(herdr.MethodSessionSnapshot, herdr.SessionSnapshotResponse{Snapshot: snapshot})
			ctx, cancel := context.WithTimeout(b.Context(), 2*time.Second)
			session, err := herdr.OpenSession(ctx, server.Client())
			cancel()
			if err != nil {
				b.Fatalf("OpenSession: %v", err)
			}
			b.Cleanup(func() {
				if err := session.Close(); err != nil {
					b.Errorf("Close: %v", err)
				}
			})

			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				_ = session.Snapshot()
			}
		})
	}
}

func benchmarkSnapshot(paneCount int) herdr.SessionSnapshot {
	workspaceID := "w1"
	tabID := "w1:t1"
	focusedPaneID := "w1:p1"
	snapshot := herdr.SessionSnapshot{
		Version:  "benchmark",
		Protocol: 1,
		Workspaces: []herdr.WorkspaceInfo{{
			WorkspaceID: workspaceID,
			ActiveTabID: tabID,
			AgentStatus: herdr.AgentStatusWorking,
			Focused:     true,
			Label:       "benchmark workspace",
			Number:      1,
			PaneCount:   uint64(paneCount),
			TabCount:    1,
			Tokens:      herdr.Some(map[string]string{"scope": "benchmark"}),
			Worktree: herdr.Some(herdr.WorkspaceWorktreeInfo{
				CheckoutPath:     "/tmp/benchmark",
				IsLinkedWorktree: true,
				RepoKey:          "benchmark/repository",
				RepoName:         "repository",
				RepoRoot:         "/tmp/repository",
			}),
		}},
		Tabs: []herdr.TabInfo{{
			WorkspaceID: workspaceID,
			TabID:       tabID,
			AgentStatus: herdr.AgentStatusWorking,
			Focused:     true,
			Label:       "benchmark tab",
			Number:      1,
			PaneCount:   uint64(paneCount),
		}},
		FocusedWorkspaceID: herdr.Some(workspaceID),
		FocusedTabID:       herdr.Some(tabID),
		FocusedPaneID:      herdr.Some(focusedPaneID),
	}

	layout := herdr.PaneLayoutSnapshot{
		Area:          herdr.PaneLayoutRect{Height: 60, Width: 180},
		FocusedPaneID: focusedPaneID,
		Panes:         make([]herdr.PaneLayoutPane, 0, paneCount),
		Splits:        make([]herdr.PaneLayoutSplit, 0, paneCount),
		TabID:         tabID,
		WorkspaceID:   workspaceID,
	}
	for i := range paneCount {
		paneID := fmt.Sprintf("w1:p%d", i+1)
		agent := fmt.Sprintf("agent-%d", i+1)
		title := fmt.Sprintf("benchmark pane %d", i+1)
		cwd := fmt.Sprintf("/tmp/benchmark/%d", i+1)
		focused := i == 0
		pane := herdr.PaneInfo{
			Agent:                 herdr.Some(agent),
			AgentSession:          herdr.Some(herdr.AgentSessionInfo{Agent: agent, Kind: herdr.AgentSessionRefKindID, Source: "benchmark", Value: fmt.Sprintf("session-%d", i+1)}),
			AgentStatus:           herdr.AgentStatusWorking,
			Cwd:                   herdr.Some(cwd),
			DisplayAgent:          herdr.Some(agent),
			Focused:               focused,
			ForegroundCwd:         herdr.Some(cwd),
			Label:                 herdr.Some(title),
			PaneID:                paneID,
			Revision:              uint64(i + 1),
			Scroll:                herdr.Some(herdr.PaneScrollInfo{MaxOffsetFromBottom: 1000, OffsetFromBottom: uint64(i), ViewportRows: 60}),
			StateLabels:           herdr.Some(map[string]string{"phase": "working", "owner": agent}),
			TabID:                 tabID,
			TerminalID:            fmt.Sprintf("terminal-%d", i+1),
			TerminalTitle:         herdr.Some(title),
			TerminalTitleStripped: herdr.Some(title),
			Title:                 herdr.Some(title),
			Tokens:                herdr.Some(map[string]string{"pane": paneID, "agent": agent}),
			WorkspaceID:           workspaceID,
		}
		snapshot.Panes = append(snapshot.Panes, pane)
		snapshot.Agents = append(snapshot.Agents, herdr.AgentInfo{
			Agent:                  pane.Agent,
			AgentSession:           pane.AgentSession,
			AgentStatus:            pane.AgentStatus,
			Cwd:                    pane.Cwd,
			DisplayAgent:           pane.DisplayAgent,
			Focused:                pane.Focused,
			ForegroundCwd:          pane.ForegroundCwd,
			InteractiveReady:       herdr.Some(true),
			LaunchPending:          herdr.Some(false),
			Name:                   herdr.Some(agent),
			PaneID:                 pane.PaneID,
			Revision:               pane.Revision,
			ScreenDetectionSkipped: herdr.Some(false),
			StateChangeSeq:         herdr.Some(uint64(i + 1)),
			StateLabels:            herdr.Some(map[string]string{"phase": "working", "owner": agent}),
			TabID:                  pane.TabID,
			TerminalID:             pane.TerminalID,
			TerminalTitle:          pane.TerminalTitle,
			TerminalTitleStripped:  pane.TerminalTitleStripped,
			Title:                  pane.Title,
			Tokens:                 herdr.Some(map[string]string{"pane": paneID, "agent": agent}),
			WorkspaceID:            pane.WorkspaceID,
		})
		layout.Panes = append(layout.Panes, herdr.PaneLayoutPane{
			Focused: focused,
			PaneID:  paneID,
			Rect:    herdr.PaneLayoutRect{Height: 60, Width: 180, X: uint16(i)},
		})
		if i > 0 {
			layout.Splits = append(layout.Splits, herdr.PaneLayoutSplit{
				Direction: herdr.SplitDirectionRight,
				ID:        fmt.Sprintf("split-%d", i),
				Ratio:     0.5,
				Rect:      herdr.PaneLayoutRect{Height: 60, Width: 180, X: uint16(i)},
			})
		}
	}
	snapshot.Layouts = []herdr.PaneLayoutSnapshot{layout}
	return snapshot
}
