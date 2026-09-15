package herdr

import (
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestSessionAccessorsReturnFullyDetachedValues(t *testing.T) {
	want := ownershipSnapshot()
	server := newMirrorServer(t, want)
	session := openTestSession(t, server)
	server.acceptStream()

	workspaces := session.Workspaces()
	workspaces[0].Tokens["owner"] = "caller"
	workspaces[0].Worktree.CheckoutPath = "/caller"
	workspaces[0].Label = "caller"

	tabs := session.Tabs()
	tabs[0].Label = "caller"

	pane, ok := session.Pane("w1:p1")
	if !ok {
		t.Fatal("Pane(w1:p1) is missing")
	}
	mutateOwnedPane(&pane)

	agents := session.Agents()
	mutateOwnedAgent(&agents[0])

	layout, ok := session.Layout("w1:t1")
	if !ok {
		t.Fatal("Layout(w1:t1) is missing")
	}
	mutateOwnedLayout(&layout)

	if got := session.Snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("mutating accessor results changed the mirror\ngot:  %#v\nwant: %#v", got, want)
	}

	snapshot := session.Snapshot()
	mutateOwnedSnapshot(&snapshot)
	if got := session.Snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("mutating Snapshot changed the mirror\ngot:  %#v\nwant: %#v", got, want)
	}
}

func TestSessionSnapshotRemainsStableAcrossLaterEvents(t *testing.T) {
	wantBefore := ownershipSnapshot()
	server := newMirrorServer(t, wantBefore)
	session := openTestSession(t, server)
	stream := server.acceptStream()

	before := session.Snapshot()
	updatedPane := richPane("w1", "w1:t1", "w1:p1")
	updatedPane.Focused = true
	*updatedPane.Title = "later title"
	updatedPane.Tokens["phase"] = "later"
	applyEvent(t, session, stream, string(EventKindPaneUpdated), PaneUpdatedEvent{Pane: updatedPane})

	updatedLayout := richLayout("w1", "w1:t1", "w1:p1")
	updatedLayout.Panes[0].PaneID = "later-pane"
	applyEvent(t, session, stream, string(EventKindLayoutUpdated), LayoutUpdatedEvent{Layout: updatedLayout})

	if !reflect.DeepEqual(before, wantBefore) {
		t.Fatalf("an earlier Snapshot changed after later events\ngot:  %#v\nwant: %#v", before, wantBefore)
	}
	after := session.Snapshot()
	if *after.Panes[0].Title != "later title" || after.Panes[0].Tokens["phase"] != "later" {
		t.Errorf("later pane update is absent: %#v", after.Panes[0])
	}
	if after.Layouts[0].Panes[0].PaneID != "later-pane" {
		t.Errorf("later layout update is absent: %#v", after.Layouts[0])
	}
}

func TestSessionNextEventDoesNotAliasMirror(t *testing.T) {
	t.Run("workspace update", func(t *testing.T) {
		session, stream := openOwnershipEventSession(t)
		workspace := richWorkspace("w1", "updated workspace")
		event := applyEvent(t, session, stream, string(EventKindWorkspaceUpdated), WorkspaceUpdatedEvent{Workspace: workspace}).(*WorkspaceUpdatedEvent)
		event.Workspace.Label = "caller"
		event.Workspace.Tokens["owner"] = "caller"
		event.Workspace.Worktree.CheckoutPath = "/caller"

		got := session.Workspaces()[0]
		if !reflect.DeepEqual(got, workspace) {
			t.Fatalf("workspace aliases returned event\ngot:  %#v\nwant: %#v", got, workspace)
		}
	})

	t.Run("workspace reorder", func(t *testing.T) {
		session, stream := openOwnershipEventSession(t)
		ordered := []WorkspaceInfo{richWorkspace("w2", "second first"), richWorkspace("w1", "first second")}
		event := applyEvent(t, session, stream, string(EventKindWorkspaceReordered), WorkspaceReorderedEvent{
			WorkspaceIds: []string{"w2", "w1"},
			Workspaces:   ordered,
		}).(*WorkspaceReorderedEvent)
		event.Workspaces[0].Label = "caller"
		event.Workspaces[0].Tokens["owner"] = "caller"
		event.Workspaces[0].Worktree.RepoName = "caller"
		event.Workspaces[0], event.Workspaces[1] = event.Workspaces[1], event.Workspaces[0]

		got := session.Workspaces()
		if !reflect.DeepEqual(got, ordered) {
			t.Fatalf("workspace order aliases returned event\ngot:  %#v\nwant: %#v", got, ordered)
		}
	})

	t.Run("pane update", func(t *testing.T) {
		session, stream := openOwnershipEventSession(t)
		pane := richPane("w1", "w1:t1", "w1:p1")
		*pane.Title = "updated pane"
		event := applyEvent(t, session, stream, string(EventKindPaneUpdated), PaneUpdatedEvent{Pane: pane}).(*PaneUpdatedEvent)
		mutateOwnedPane(&event.Pane)

		got, ok := session.Pane(pane.PaneID)
		if !ok || !reflect.DeepEqual(got, pane) {
			t.Fatalf("pane aliases returned event\ngot:  %#v, ok: %v\nwant: %#v", got, ok, pane)
		}
	})

	t.Run("pane move", func(t *testing.T) {
		session, stream := openOwnershipEventSession(t)
		pane := richPane("w3", "w3:t1", "w3:p1")
		workspace := richWorkspace("w3", "created workspace")
		tab := testTab("w3", "w3:t1")
		event := applyEvent(t, session, stream, string(EventKindPaneMoved), PaneMovedEvent{
			CreatedTab:          &tab,
			CreatedWorkspace:    &workspace,
			Pane:                pane,
			PreviousPaneID:      "w1:p1",
			PreviousTabID:       "w1:t1",
			PreviousWorkspaceID: "w1",
		}).(*PaneMovedEvent)
		mutateOwnedPane(&event.Pane)
		event.CreatedWorkspace.Label = "caller"
		event.CreatedWorkspace.Tokens["owner"] = "caller"
		event.CreatedWorkspace.Worktree.CheckoutPath = "/caller"
		event.CreatedTab.Label = "caller"

		gotPane, ok := session.Pane("w3:p1")
		if !ok || !reflect.DeepEqual(gotPane, pane) {
			t.Fatalf("moved pane aliases returned event\ngot:  %#v, ok: %v\nwant: %#v", gotPane, ok, pane)
		}
		gotWorkspace := findWorkspace(t, session.Workspaces(), "w3")
		if !reflect.DeepEqual(gotWorkspace, workspace) {
			t.Fatalf("created workspace aliases returned event\ngot:  %#v\nwant: %#v", gotWorkspace, workspace)
		}
		if got := findTab(t, session.Tabs(), "w3:t1"); !reflect.DeepEqual(got, tab) {
			t.Fatalf("created tab aliases returned event\ngot:  %#v\nwant: %#v", got, tab)
		}
		agents := session.Agents()
		if len(agents) != 1 || agents[0].PaneID != "w3:p1" || *agents[0].AgentSession != *pane.AgentSession {
			t.Fatalf("agent did not move with its pane: %#v", agents)
		}
	})

	t.Run("agent detection", func(t *testing.T) {
		session, stream := openOwnershipEventSession(t)
		agent := "new-agent"
		event := applyEvent(t, session, stream, string(EventKindPaneAgentDetected), PaneAgentDetectedEvent{
			Agent:       &agent,
			PaneID:      "w1:p1",
			WorkspaceID: "w1",
		}).(*PaneAgentDetectedEvent)
		*event.Agent = "caller"

		pane, _ := session.Pane("w1:p1")
		if Value(pane.Agent) != agent {
			t.Errorf("pane agent = %q, want %q", Value(pane.Agent), agent)
		}
		agents := session.Agents()
		if len(agents) != 1 || Value(agents[0].Agent) != agent {
			t.Errorf("agents = %#v, want detected agent %q", agents, agent)
		}
	})

	t.Run("agent status", func(t *testing.T) {
		session, stream := openOwnershipEventSession(t)
		agent, display, title := "codex-updated", "Codex Updated", "updated title"
		labels := map[string]string{"phase": "updated"}
		event := applyEvent(t, session, stream, string(EventKindPaneAgentStatusChanged), PaneAgentStatusChangedEvent{
			Agent:        &agent,
			AgentStatus:  AgentStatusBlocked,
			DisplayAgent: &display,
			PaneID:       "w1:p1",
			StateLabels:  labels,
			Title:        &title,
			WorkspaceID:  "w1",
		}).(*PaneAgentStatusChangedEvent)
		*event.Agent = "caller"
		*event.DisplayAgent = "caller"
		*event.Title = "caller"
		event.StateLabels["phase"] = "caller"

		pane, _ := session.Pane("w1:p1")
		if Value(pane.Agent) != agent || Value(pane.DisplayAgent) != display || Value(pane.Title) != title || pane.StateLabels["phase"] != "updated" {
			t.Errorf("pane aliases returned agent event: %#v", pane)
		}
		agents := session.Agents()
		if len(agents) != 1 || Value(agents[0].Agent) != agent || agents[0].StateLabels["phase"] != "updated" {
			t.Errorf("agent aliases returned agent event: %#v", agents)
		}
	})

	t.Run("layout update", func(t *testing.T) {
		session, stream := openOwnershipEventSession(t)
		layout := richLayout("w1", "w1:t1", "w1:p1")
		layout.Splits = append(layout.Splits, PaneLayoutSplit{ID: "split-2", Ratio: 0.75})
		event := applyEvent(t, session, stream, string(EventKindLayoutUpdated), LayoutUpdatedEvent{Layout: layout}).(*LayoutUpdatedEvent)
		mutateOwnedLayout(&event.Layout)

		got, ok := session.Layout("w1:t1")
		if !ok || !reflect.DeepEqual(got, layout) {
			t.Fatalf("layout aliases returned event\ngot:  %#v, ok: %v\nwant: %#v", got, ok, layout)
		}
	})
}

func TestSessionCacheOwnsSnapshotAndEventInputs(t *testing.T) {
	source := ownershipSnapshot()
	want := ownershipSnapshot()
	cache := newSessionCache(source)
	mutateOwnedSnapshot(&source)

	if got := cacheSnapshot(cache); !reflect.DeepEqual(got, want) {
		t.Fatalf("cache aliases its snapshot input\ngot:  %#v\nwant: %#v", got, want)
	}

	updated := richWorkspace("w1", "event workspace")
	wantUpdated := richWorkspace("w1", "event workspace")
	event := &WorkspaceUpdatedEvent{Workspace: updated}
	cache.apply(event)
	event.Workspace.Label = "caller"
	event.Workspace.Tokens["owner"] = "caller"
	event.Workspace.Worktree.CheckoutPath = "/caller"

	got, ok := cache.workspaces.get("w1")
	if !ok || !reflect.DeepEqual(got, wantUpdated) {
		t.Fatalf("cache aliases its event input\ngot:  %#v, ok: %v\nwant: %#v", got, ok, wantUpdated)
	}
}

func TestSessionCopiesPreserveNilAndEmptyCollections(t *testing.T) {
	snapshot := ownershipSnapshot()
	snapshot.Workspaces[0].Tokens = nil
	snapshot.Workspaces[1].Tokens = map[string]string{}
	snapshot.Panes[0].StateLabels = nil
	snapshot.Panes[0].Tokens = map[string]string{}
	snapshot.Panes[1].StateLabels = map[string]string{}
	snapshot.Panes[1].Tokens = nil
	snapshot.Agents[0].StateLabels = nil
	snapshot.Agents[0].Tokens = map[string]string{}
	snapshot.Layouts[0].Panes = nil
	snapshot.Layouts[0].Splits = []PaneLayoutSplit{}
	snapshot.Layouts[1].Panes = []PaneLayoutPane{}
	snapshot.Layouts[1].Splits = nil

	session := &Session{state: &sessionState{cache: newSessionCache(snapshot)}}
	got := session.Snapshot()
	if got.Workspaces[0].Tokens != nil || got.Workspaces[1].Tokens == nil {
		t.Errorf("workspace maps lost nil/empty distinction: %#v", got.Workspaces)
	}
	if got.Panes[0].StateLabels != nil || got.Panes[0].Tokens == nil || got.Panes[1].StateLabels == nil || got.Panes[1].Tokens != nil {
		t.Errorf("pane maps lost nil/empty distinction: %#v", got.Panes)
	}
	if got.Agents[0].StateLabels != nil || got.Agents[0].Tokens == nil {
		t.Errorf("agent maps lost nil/empty distinction: %#v", got.Agents[0])
	}
	if got.Layouts[0].Panes != nil || got.Layouts[0].Splits == nil || got.Layouts[1].Panes == nil || got.Layouts[1].Splits != nil {
		t.Errorf("layout slices lost nil/empty distinction: %#v", got.Layouts)
	}
}

func TestSessionReturnedValuesCanBeMutatedWhileEventsAreApplied(t *testing.T) {
	server := newMirrorServer(t, ownershipSnapshot())
	session := openTestSession(t, server)
	stream := server.acceptStream()

	start := make(chan struct{})
	stop := make(chan struct{})
	var stopOnce sync.Once
	var ready sync.WaitGroup
	var readers sync.WaitGroup
	for range 4 {
		ready.Add(1)
		readers.Add(1)
		go func() {
			defer readers.Done()
			ready.Done()
			<-start
			for {
				select {
				case <-stop:
					return
				default:
				}
				workspaces := session.Workspaces()
				workspaces[0].Worktree.CheckoutPath = "/caller"
				pane, _ := session.Pane("w1:p1")
				mutateOwnedPane(&pane)
				agents := session.Agents()
				mutateOwnedAgent(&agents[0])
				layout, _ := session.Layout("w1:t1")
				mutateOwnedLayout(&layout)
				snapshot := session.Snapshot()
				mutateOwnedSnapshot(&snapshot)
			}
		}()
	}
	ready.Wait()
	readersDone := make(chan struct{})
	go func() {
		readers.Wait()
		close(readersDone)
	}()
	stopReaders := func() {
		stopOnce.Do(func() { close(stop) })
		select {
		case <-readersDone:
		case <-time.After(2 * time.Second):
			t.Error("session copy readers did not stop")
		}
	}
	t.Cleanup(stopReaders)
	close(start)

	for i := range 40 {
		pane := richPane("w1", "w1:t1", "w1:p1")
		pane.Revision = uint64(i + 2)
		*pane.Title = "server"
		applyEvent(t, session, stream, string(EventKindPaneUpdated), PaneUpdatedEvent{Pane: pane})
		layout := richLayout("w1", "w1:t1", "w1:p1")
		layout.Panes[0].Rect.X = uint16(i)
		applyEvent(t, session, stream, string(EventKindLayoutUpdated), LayoutUpdatedEvent{Layout: layout})
	}
	stopReaders()

	pane, _ := session.Pane("w1:p1")
	if pane.Revision != 41 || Value(pane.Title) != "server" {
		t.Errorf("final pane = %#v, want the last server update", pane)
	}
}

func openOwnershipEventSession(t *testing.T) (*Session, *mirrorStream) {
	t.Helper()
	server := newMirrorServer(t, ownershipSnapshot())
	session := openTestSession(t, server)
	return session, server.acceptStream()
}

func ownershipSnapshot() SessionSnapshot {
	snapshot := testSnapshot()
	snapshot.Workspaces[0] = richWorkspace("w1", "one")
	snapshot.Workspaces[0].Focused = true
	snapshot.Workspaces[1] = richWorkspace("w2", "two")

	for i := range snapshot.Panes {
		snapshot.Panes[i] = richPane(snapshot.Panes[i].WorkspaceID, snapshot.Panes[i].TabID, snapshot.Panes[i].PaneID)
	}
	snapshot.Panes[0].Focused = true
	snapshot.Agents[0] = richAgent(snapshot.Panes[0], "codex")
	for i := range snapshot.Layouts {
		paneIDs := make([]string, len(snapshot.Layouts[i].Panes))
		for j := range snapshot.Layouts[i].Panes {
			paneIDs[j] = snapshot.Layouts[i].Panes[j].PaneID
		}
		snapshot.Layouts[i] = richLayout(snapshot.Layouts[i].WorkspaceID, snapshot.Layouts[i].TabID, paneIDs...)
	}
	return snapshot
}

func richWorkspace(workspaceID, label string) WorkspaceInfo {
	workspace := testWorkspace(workspaceID, label)
	workspace.Tokens = map[string]string{"owner": "server", "workspace": workspaceID}
	workspace.Worktree = &WorkspaceWorktreeInfo{
		CheckoutPath:     "/checkout/" + workspaceID,
		IsLinkedWorktree: true,
		RepoKey:          "repo/" + workspaceID,
		RepoName:         "repo-" + workspaceID,
		RepoRoot:         "/repo/" + workspaceID,
	}
	return workspace
}

func richPane(workspaceID, tabID, paneID string) PaneInfo {
	pane := testPane(workspaceID, tabID, paneID)
	agent := "codex"
	cwd := "/cwd/" + paneID
	displayAgent := "Codex"
	label := "label " + paneID
	title := "title " + paneID
	pane.Agent = &agent
	pane.AgentSession = &AgentSessionInfo{Agent: agent, Kind: AgentSessionRefKindID, Source: "test", Value: "session-" + paneID}
	pane.Cwd = &cwd
	pane.DisplayAgent = &displayAgent
	pane.ForegroundCwd = stringPtr(cwd + "/foreground")
	pane.Label = &label
	pane.Scroll = &PaneScrollInfo{MaxOffsetFromBottom: 100, OffsetFromBottom: 3, ViewportRows: 40}
	pane.StateLabels = map[string]string{"phase": "server"}
	pane.TerminalTitle = &title
	pane.TerminalTitleStripped = stringPtr(title + " stripped")
	pane.Title = stringPtr(title + " agent")
	pane.Tokens = map[string]string{"owner": "server", "pane": paneID}
	return pane
}

func richAgent(pane PaneInfo, agentName string) AgentInfo {
	agent := testAgent(pane, agentName)
	ready, pending, skipped := true, false, false
	seq := uint64(42)
	agent.Agent = cloneTestPtr(pane.Agent)
	agent.AgentSession = cloneTestPtr(pane.AgentSession)
	agent.Cwd = cloneTestPtr(pane.Cwd)
	agent.DisplayAgent = cloneTestPtr(pane.DisplayAgent)
	agent.ForegroundCwd = cloneTestPtr(pane.ForegroundCwd)
	agent.InteractiveReady = &ready
	agent.LaunchPending = &pending
	agent.ScreenDetectionSkipped = &skipped
	agent.StateChangeSeq = &seq
	agent.StateLabels = map[string]string{"phase": "server"}
	agent.TerminalTitle = cloneTestPtr(pane.TerminalTitle)
	agent.TerminalTitleStripped = cloneTestPtr(pane.TerminalTitleStripped)
	agent.Title = cloneTestPtr(pane.Title)
	agent.Tokens = map[string]string{"owner": "server", "pane": pane.PaneID}
	return agent
}

func richLayout(workspaceID, tabID string, paneIDs ...string) PaneLayoutSnapshot {
	layout := testLayout(workspaceID, tabID, paneIDs...)
	layout.Splits = []PaneLayoutSplit{{
		Direction: SplitDirectionRight,
		ID:        "split-1",
		Ratio:     0.5,
		Rect:      PaneLayoutRect{Height: 40, Width: 80},
	}}
	return layout
}

func mutateOwnedSnapshot(snapshot *SessionSnapshot) {
	if snapshot.FocusedWorkspaceID != nil {
		*snapshot.FocusedWorkspaceID = "caller"
	}
	if snapshot.FocusedTabID != nil {
		*snapshot.FocusedTabID = "caller"
	}
	if snapshot.FocusedPaneID != nil {
		*snapshot.FocusedPaneID = "caller"
	}
	if len(snapshot.Workspaces) > 0 {
		snapshot.Workspaces[0].Label = "caller"
		snapshot.Workspaces[0].Tokens["owner"] = "caller"
		snapshot.Workspaces[0].Worktree.CheckoutPath = "/caller"
	}
	if len(snapshot.Tabs) > 0 {
		snapshot.Tabs[0].Label = "caller"
	}
	if len(snapshot.Panes) > 0 {
		mutateOwnedPane(&snapshot.Panes[0])
	}
	if len(snapshot.Agents) > 0 {
		mutateOwnedAgent(&snapshot.Agents[0])
	}
	if len(snapshot.Layouts) > 0 {
		mutateOwnedLayout(&snapshot.Layouts[0])
	}
}

func mutateOwnedPane(pane *PaneInfo) {
	*pane.Agent = "caller"
	pane.AgentSession.Value = "caller"
	*pane.Cwd = "/caller"
	*pane.DisplayAgent = "caller"
	*pane.ForegroundCwd = "/caller"
	*pane.Label = "caller"
	pane.Scroll.OffsetFromBottom = 99
	pane.StateLabels["phase"] = "caller"
	*pane.TerminalTitle = "caller"
	*pane.TerminalTitleStripped = "caller"
	*pane.Title = "caller"
	pane.Tokens["owner"] = "caller"
}

func mutateOwnedAgent(agent *AgentInfo) {
	*agent.Agent = "caller"
	agent.AgentSession.Value = "caller"
	*agent.Cwd = "/caller"
	*agent.DisplayAgent = "caller"
	*agent.ForegroundCwd = "/caller"
	*agent.InteractiveReady = false
	*agent.LaunchPending = true
	*agent.Name = "caller"
	*agent.ScreenDetectionSkipped = true
	*agent.StateChangeSeq = 99
	agent.StateLabels["phase"] = "caller"
	*agent.TerminalTitle = "caller"
	*agent.TerminalTitleStripped = "caller"
	*agent.Title = "caller"
	agent.Tokens["owner"] = "caller"
}

func mutateOwnedLayout(layout *PaneLayoutSnapshot) {
	if len(layout.Panes) > 0 {
		layout.Panes[0].PaneID = "caller"
		layout.Panes[0].Rect.X = 99
	}
	if len(layout.Splits) > 0 {
		layout.Splits[0].ID = "caller"
		layout.Splits[0].Ratio = 0.99
	}
}

func cacheSnapshot(cache *sessionCache) SessionSnapshot {
	workspaces := cloneEach(cache.workspaces.values(), WorkspaceInfo.Clone)
	tabs := cloneEach(cache.tabs.values(), TabInfo.Clone)
	panes := cloneEach(cache.panes.values(), PaneInfo.Clone)
	return SessionSnapshot{
		Version:            cache.version,
		Protocol:           cache.protocol,
		Workspaces:         workspaces,
		Tabs:               tabs,
		Panes:              panes,
		Agents:             cloneEach(cache.agents.values(), AgentInfo.Clone),
		Layouts:            cloneEach(cache.layouts.values(), PaneLayoutSnapshot.Clone),
		FocusedWorkspaceID: focusedID(workspaces, func(workspace WorkspaceInfo) (string, bool) { return workspace.WorkspaceID, workspace.Focused }),
		FocusedTabID:       focusedID(tabs, func(tab TabInfo) (string, bool) { return tab.TabID, tab.Focused }),
		FocusedPaneID:      focusedID(panes, func(pane PaneInfo) (string, bool) { return pane.PaneID, pane.Focused }),
	}
}

func findWorkspace(t *testing.T, workspaces []WorkspaceInfo, workspaceID string) WorkspaceInfo {
	t.Helper()
	for _, workspace := range workspaces {
		if workspace.WorkspaceID == workspaceID {
			return workspace
		}
	}
	t.Fatalf("workspace %s is missing from %#v", workspaceID, workspaces)
	return WorkspaceInfo{}
}

func findTab(t *testing.T, tabs []TabInfo, tabID string) TabInfo {
	t.Helper()
	for _, tab := range tabs {
		if tab.TabID == tabID {
			return tab
		}
	}
	t.Fatalf("tab %s is missing from %#v", tabID, tabs)
	return TabInfo{}
}

func cloneTestPtr[T any](value *T) *T {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
