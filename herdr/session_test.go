package herdr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestOpenSessionSubscribesBeforeSnapshot(t *testing.T) {
	server := newMirrorServer(t, testSnapshot())
	session := openTestSession(t, server)
	server.acceptStream()

	if got := server.fake.request(0).Method; got != MethodEventsSubscribe {
		t.Errorf("first request = %q, want %q", got, MethodEventsSubscribe)
	}
	if got := server.fake.request(1).Method; got != MethodSessionSnapshot {
		t.Errorf("second request = %q, want %q", got, MethodSessionSnapshot)
	}
	if got := server.snapshotCount(); got != 1 {
		t.Errorf("snapshot calls = %d, want 1", got)
	}

	params := string(server.fake.request(0).Params)
	for _, subscription := range MirrorSubscriptions() {
		encoded, err := json.Marshal(subscription)
		if err != nil {
			t.Fatalf("encode subscription: %v", err)
		}
		if !strings.Contains(params, string(encoded)) {
			t.Errorf("subscribe params %s do not contain %s", params, encoded)
		}
	}
	if _, ok := session.Pane("w1:p1"); !ok {
		t.Error("pane w1:p1 is missing from the mirror")
	}
}

func TestOpenSessionInstallsSnapshot(t *testing.T) {
	server := newMirrorServer(t, testSnapshot())
	session := openTestSession(t, server, PaneCreatedSubscription{})
	server.acceptStream()

	workspaces := session.Workspaces()
	if len(workspaces) != 2 || workspaces[0].WorkspaceID != "w1" || workspaces[1].WorkspaceID != "w2" {
		t.Fatalf("workspaces = %+v", workspaces)
	}
	tabs := session.Tabs()
	if len(tabs) != 3 || tabs[0].TabID != "w1:t1" || tabs[2].TabID != "w2:t1" {
		t.Fatalf("tabs = %+v", tabs)
	}
	pane, ok := session.Pane("w1:p2")
	if !ok || pane.TabID != "w1:t2" {
		t.Fatalf("pane w1:p2 = %+v, ok = %v", pane, ok)
	}
	if _, ok := session.Pane("nope"); ok {
		t.Error("Pane reported an unknown pane")
	}
	agents := session.Agents()
	if len(agents) != 1 || agents[0].PaneID != "w1:p1" {
		t.Fatalf("agents = %+v", agents)
	}
	layout, ok := session.Layout("w1:t1")
	if !ok || paneIDs(layout) != "[w1:p1]" {
		t.Fatalf("layout w1:t1 = %s, ok = %v", paneIDs(layout), ok)
	}
	if _, ok := session.Layout("nope"); ok {
		t.Error("Layout reported an unknown tab")
	}
}

func TestSessionAccessorsReturnCopies(t *testing.T) {
	server := newMirrorServer(t, testSnapshot())
	session := openTestSession(t, server, PaneCreatedSubscription{})
	server.acceptStream()

	workspaces := session.Workspaces()
	workspaces[0].Label = "rewritten"
	if got := session.Workspaces()[0].Label; got != "one" {
		t.Errorf("workspace label = %q, want one", got)
	}

	pane, _ := session.Pane("w1:p1")
	pane.Tokens.ValueOrZero()["pane"] = "rewritten"
	again, _ := session.Pane("w1:p1")
	if got := again.Tokens.ValueOrZero()["pane"]; got != "w1:p1" {
		t.Errorf("pane token = %q, want w1:p1", got)
	}

	layout, _ := session.Layout("w1:t1")
	layout.Panes[0].PaneID = "rewritten"
	if got, _ := session.Layout("w1:t1"); paneIDs(got) != "[w1:p1]" {
		t.Errorf("layout = %s, want [w1:p1]", paneIDs(got))
	}
}

// An event that fires between the subscription and the snapshot must reach the
// mirror exactly once, whether or not the snapshot already reflects it.
func TestOpenSessionAppliesEventsBufferedBeforeSnapshot(t *testing.T) {
	snapshot := testSnapshot()
	// The server took the snapshot after p1 reached revision 9, so the pane
	// event below is already reflected in it.
	snapshot.Panes[0].Revision = 9

	server := newMirrorServer(t, snapshot)
	reflected := testPane("w1", "w1:t1", "w1:p1")
	reflected.Revision = 9
	reflected.Focused = true
	added := testPane("w1", "w1:t2", "w1:p3")
	server.onSnapshot(func(m *mirrorServer) {
		stream := m.lastStream()
		stream.pushSync(eventLine(t, string(EventKindPaneUpdated), PaneUpdatedEvent{Pane: reflected}))
		stream.pushSync(eventLine(t, string(EventKindPaneCreated), PaneCreatedEvent{Pane: added}))
	})

	session := openTestSession(t, server)
	server.acceptStream()

	if _, ok := session.Pane("w1:p3"); ok {
		t.Error("a buffered event reached the mirror before it was read")
	}

	first := nextEvent(t, session)
	if _, ok := first.(*PaneUpdatedEvent); !ok {
		t.Fatalf("first event is %T, want *PaneUpdatedEvent", first)
	}
	pane, ok := session.Pane("w1:p1")
	if !ok || pane.Revision != 9 {
		t.Fatalf("pane w1:p1 = %+v, ok = %v", pane, ok)
	}

	second := nextEvent(t, session)
	if _, ok := second.(*PaneCreatedEvent); !ok {
		t.Fatalf("second event is %T, want *PaneCreatedEvent", second)
	}
	if _, ok := session.Pane("w1:p3"); !ok {
		t.Error("pane w1:p3 is missing from the mirror")
	}
	expectNoEvent(t, session)

	if got := len(session.Agents()); got != 1 {
		t.Errorf("agents = %d, want 1", got)
	}
	if got := len(session.Tabs()); got != 3 {
		t.Errorf("tabs = %d, want 3", got)
	}
}

func TestSessionAppliesWorkspaceEvents(t *testing.T) {
	server := newMirrorServer(t, testSnapshot())
	session := openTestSession(t, server)
	stream := server.acceptStream()

	third := testWorkspace("w3", "three")
	applyEvent(t, session, stream, string(EventKindWorkspaceCreated), WorkspaceCreatedEvent{Workspace: third})
	if got := workspaceIDs(session); got != "[w1 w2 w3]" {
		t.Fatalf("workspaces = %s, want [w1 w2 w3]", got)
	}

	applyEvent(t, session, stream, string(EventKindWorkspaceRenamed), WorkspaceRenamedEvent{WorkspaceID: "w1", Label: "renamed"})
	if got := session.Workspaces()[0].Label; got != "renamed" {
		t.Errorf("label = %q, want renamed", got)
	}

	updated := testWorkspace("w2", "two")
	updated.PaneCount = 5
	applyEvent(t, session, stream, string(EventKindWorkspaceUpdated), WorkspaceUpdatedEvent{Workspace: updated})
	if got := session.Workspaces()[1].PaneCount; got != 5 {
		t.Errorf("pane count = %d, want 5", got)
	}

	applyEvent(t, session, stream, string(EventKindWorkspaceFocused), WorkspaceFocusedEvent{WorkspaceID: "w2"})
	workspaces := session.Workspaces()
	if workspaces[0].Focused || !workspaces[1].Focused {
		t.Errorf("focused flags = %v, %v, want false, true", workspaces[0].Focused, workspaces[1].Focused)
	}

	applyEvent(t, session, stream, string(EventKindWorkspaceReordered), WorkspaceReorderedEvent{
		WorkspaceIds: []string{"w3", "w2", "w1"},
		Workspaces:   []WorkspaceInfo{third, updated, session.Workspaces()[0]},
	})
	if got := workspaceIDs(session); got != "[w3 w2 w1]" {
		t.Errorf("workspaces = %s, want [w3 w2 w1]", got)
	}

	applyEvent(t, session, stream, string(EventKindWorkspaceClosed), WorkspaceClosedEvent{WorkspaceID: "w1"})
	if got := workspaceIDs(session); got != "[w3 w2]" {
		t.Errorf("workspaces = %s, want [w3 w2]", got)
	}
	if got := tabIDs(session); got != "[w2:t1]" {
		t.Errorf("tabs = %s, want [w2:t1], the tabs of w1 were kept", got)
	}
	if _, ok := session.Pane("w1:p1"); ok {
		t.Error("a pane of the closed workspace was kept")
	}
	if got := len(session.Agents()); got != 0 {
		t.Errorf("agents = %d, want 0", got)
	}
	if _, ok := session.Layout("w1:t1"); ok {
		t.Error("a layout of the closed workspace was kept")
	}
}

func TestSessionAppliesTabEvents(t *testing.T) {
	server := newMirrorServer(t, testSnapshot())
	session := openTestSession(t, server)
	stream := server.acceptStream()

	applyEvent(t, session, stream, string(EventKindTabCreated), TabCreatedEvent{Tab: testTab("w2", "w2:t2")})
	if got := tabIDs(session); got != "[w1:t1 w1:t2 w2:t1 w2:t2]" {
		t.Fatalf("tabs = %s", got)
	}

	applyEvent(t, session, stream, string(EventKindTabRenamed), TabRenamedEvent{TabID: "w1:t1", WorkspaceID: "w1", Label: "first"})
	if got := session.Tabs()[0].Label; got != "first" {
		t.Errorf("label = %q, want first", got)
	}

	applyEvent(t, session, stream, string(EventKindTabFocused), TabFocusedEvent{TabID: "w1:t2", WorkspaceID: "w1"})
	tabs := session.Tabs()
	if tabs[0].Focused || !tabs[1].Focused {
		t.Errorf("focused flags = %v, %v, want false, true", tabs[0].Focused, tabs[1].Focused)
	}

	applyEvent(t, session, stream, string(EventKindTabMoved), TabMovedEvent{
		TabID:       "w1:t2",
		WorkspaceID: "w1",
		InsertIndex: 0,
		Tabs:        []TabInfo{tabs[1], tabs[0]},
	})
	if got := tabIDs(session); got != "[w1:t2 w1:t1 w2:t1 w2:t2]" {
		t.Errorf("tabs = %s, want [w1:t2 w1:t1 w2:t1 w2:t2]", got)
	}

	applyEvent(t, session, stream, string(EventKindTabClosed), TabClosedEvent{TabID: "w1:t1", WorkspaceID: "w1"})
	if got := tabIDs(session); got != "[w1:t2 w2:t1 w2:t2]" {
		t.Errorf("tabs = %s, want [w1:t2 w2:t1 w2:t2]", got)
	}
	if _, ok := session.Pane("w1:p1"); ok {
		t.Error("a pane of the closed tab was kept")
	}
	if got := len(session.Agents()); got != 0 {
		t.Errorf("agents = %d, want 0", got)
	}
	if _, ok := session.Layout("w1:t1"); ok {
		t.Error("the layout of the closed tab was kept")
	}
	if _, ok := session.Layout("w1:t2"); !ok {
		t.Error("the layout of another tab was dropped")
	}
}

func TestSessionAppliesPaneEvents(t *testing.T) {
	server := newMirrorServer(t, testSnapshot())
	session := openTestSession(t, server)
	stream := server.acceptStream()

	applyEvent(t, session, stream, string(EventKindPaneCreated), PaneCreatedEvent{Pane: testPane("w2", "w2:t1", "w2:p2")})
	if _, ok := session.Pane("w2:p2"); !ok {
		t.Fatal("pane w2:p2 is missing from the mirror")
	}

	updated := testPane("w1", "w1:t1", "w1:p1")
	updated.Revision = 7
	updated.Title = Some("busy")
	applyEvent(t, session, stream, string(EventKindPaneUpdated), PaneUpdatedEvent{Pane: updated})
	pane, _ := session.Pane("w1:p1")
	if pane.Revision != 7 || pane.Title.ValueOrZero() != "busy" {
		t.Errorf("pane = %+v", pane)
	}
	if got := session.Agents()[0]; got.Revision != 7 || got.Title.ValueOrZero() != "busy" {
		t.Errorf("agent = %+v, want the fields of its pane", got)
	}

	applyEvent(t, session, stream, string(EventKindPaneOutputChanged), PaneOutputChangedEvent{
		PaneID:      "w1:p1",
		WorkspaceID: "w1",
		Revision:    11,
	})
	pane, _ = session.Pane("w1:p1")
	if pane.Revision != 11 || session.Agents()[0].Revision != 11 {
		t.Errorf("revision = %d, agent revision = %d, want 11", pane.Revision, session.Agents()[0].Revision)
	}

	applyEvent(t, session, stream, string(EventKindPaneFocused), PaneFocusedEvent{PaneID: "w1:p2", WorkspaceID: "w1"})
	first, _ := session.Pane("w1:p1")
	second, _ := session.Pane("w1:p2")
	if first.Focused || !second.Focused || session.Agents()[0].Focused {
		t.Errorf("focus = %v, %v, agent %v, want false, true, false", first.Focused, second.Focused, session.Agents()[0].Focused)
	}

	applyEvent(t, session, stream, "pane.scroll_changed", PaneScrollChangedEvent{
		PaneID:      "w1:p1",
		WorkspaceID: "w1",
		Scroll:      PaneScrollInfo{MaxOffsetFromBottom: 90, OffsetFromBottom: 12, ViewportRows: 40},
	})
	pane, _ = session.Pane("w1:p1")
	if scroll, ok := pane.Scroll.Get(); !ok || scroll.OffsetFromBottom != 12 {
		t.Errorf("scroll = %+v", pane.Scroll)
	}

	applyEvent(t, session, stream, string(EventKindPaneExited), PaneExitedEvent{PaneID: "w1:p1", WorkspaceID: "w1"})
	if _, ok := session.Pane("w1:p1"); !ok {
		t.Error("pane.exited removed the pane, which only pane.closed does")
	}

	applyEvent(t, session, stream, string(EventKindPaneClosed), PaneClosedEvent{PaneID: "w1:p1", WorkspaceID: "w1"})
	if _, ok := session.Pane("w1:p1"); ok {
		t.Error("the closed pane was kept")
	}
	if got := len(session.Agents()); got != 0 {
		t.Errorf("agents = %d, want 0", got)
	}
}

func TestSessionAppliesPaneMovedWithNewPaneID(t *testing.T) {
	server := newMirrorServer(t, testSnapshot())
	session := openTestSession(t, server)
	stream := server.acceptStream()

	// w1:p1 is the only pane of w1:t1 and runs the agent, so moving it to a
	// tab of its own closes the tab it came from.
	moved := testPane("w1", "w1:t3", "w1:p7")
	createdTab := testTab("w1", "w1:t3")
	applyEvent(t, session, stream, string(EventKindPaneMoved), PaneMovedEvent{
		Pane:                moved,
		PreviousPaneID:      "w1:p1",
		PreviousTabID:       "w1:t1",
		PreviousWorkspaceID: "w1",
		CreatedTab:          Some(createdTab),
		ClosedTabID:         Some("w1:t1"),
	})

	if _, ok := session.Pane("w1:p1"); ok {
		t.Error("the pane is still mirrored under the id it had before the move")
	}
	pane, ok := session.Pane("w1:p7")
	if !ok || pane.TabID != "w1:t3" {
		t.Fatalf("pane w1:p7 = %+v, ok = %v", pane, ok)
	}
	agents := session.Agents()
	if len(agents) != 1 || agents[0].PaneID != "w1:p7" || agents[0].TabID != "w1:t3" {
		t.Fatalf("agents = %+v, want the agent to follow its pane", agents)
	}
	if got := tabIDs(session); got != "[w1:t2 w2:t1 w1:t3]" {
		t.Errorf("tabs = %s, want [w1:t2 w2:t1 w1:t3]", got)
	}
	if _, ok := session.Layout("w1:t1"); ok {
		t.Error("the layout of the closed tab was kept")
	}
}

func TestSessionAppliesAgentEvents(t *testing.T) {
	server := newMirrorServer(t, testSnapshot())
	session := openTestSession(t, server)
	stream := server.acceptStream()

	applyEvent(t, session, stream, string(EventKindPaneAgentDetected), PaneAgentDetectedEvent{
		PaneID:      "w1:p2",
		WorkspaceID: "w1",
		Agent:       Some("claude"),
	})
	agents := session.Agents()
	if len(agents) != 2 || agents[1].PaneID != "w1:p2" {
		t.Fatalf("agents = %+v, want an entry for w1:p2", agents)
	}
	if agents[1].Agent.ValueOrZero() != "claude" {
		t.Errorf("agent = %+v, want claude", agents[1].Agent)
	}
	pane, _ := session.Pane("w1:p2")
	if pane.Agent.ValueOrZero() != "claude" {
		t.Errorf("pane agent = %+v, want claude", pane.Agent)
	}

	applyEvent(t, session, stream, "pane.agent_status_changed", PaneAgentStatusChangedEvent{
		PaneID:      "w1:p2",
		WorkspaceID: "w1",
		AgentStatus: AgentStatusBlocked,
		Title:       Some("waiting"),
	})
	pane, _ = session.Pane("w1:p2")
	if pane.AgentStatus != AgentStatusBlocked || pane.Title.ValueOrZero() != "waiting" {
		t.Errorf("pane = %+v", pane)
	}
	if got := session.Agents()[1]; got.AgentStatus != AgentStatusBlocked {
		t.Errorf("agent status = %q, want blocked", got.AgentStatus)
	}

	applyEvent(t, session, stream, string(EventKindPaneAgentStatusChanged), PaneAgentStatusChangedEvent{
		PaneID:      "w1:p1",
		WorkspaceID: "w1",
		AgentStatus: AgentStatusDone,
	})
	if got := session.Agents()[0].AgentStatus; got != AgentStatusDone {
		t.Errorf("agent status = %q, want done", got)
	}

	applyEvent(t, session, stream, string(EventKindPaneAgentDetected), PaneAgentDetectedEvent{
		PaneID:      "w1:p2",
		WorkspaceID: "w1",
		Released:    Some(true),
		FinalStatus: Some(AgentStatusDone),
	})
	agents = session.Agents()
	if len(agents) != 1 || agents[0].PaneID != "w1:p1" {
		t.Fatalf("agents = %+v, want only the entry for w1:p1", agents)
	}
	pane, _ = session.Pane("w1:p2")
	if pane.Agent.IsSet() || pane.AgentStatus != AgentStatusDone {
		t.Errorf("pane = %+v, want no agent and the final status", pane)
	}
}

func TestSessionAppliesLayoutEvents(t *testing.T) {
	server := newMirrorServer(t, testSnapshot())
	session := openTestSession(t, server)
	stream := server.acceptStream()

	applyEvent(t, session, stream, string(EventKindLayoutUpdated), LayoutUpdatedEvent{
		Layout: testLayout("w1", "w1:t1", "w1:p1", "w1:p9"),
	})
	layout, ok := session.Layout("w1:t1")
	if !ok || paneIDs(layout) != "[w1:p1 w1:p9]" {
		t.Fatalf("layout w1:t1 = %s, ok = %v", paneIDs(layout), ok)
	}

	applyEvent(t, session, stream, string(EventKindLayoutUpdated), LayoutUpdatedEvent{
		Layout: testLayout("w2", "w2:t9", "w2:p9"),
	})
	if _, ok := session.Layout("w2:t9"); !ok {
		t.Error("the layout of a tab the mirror did not know was dropped")
	}
}

func TestSessionReportsUndecodableEvent(t *testing.T) {
	server := newMirrorServer(t, testSnapshot())
	session := openTestSession(t, server)
	stream := server.acceptStream()

	stream.push(`{"event":"cosmic_ray","data":{"type":"cosmic_ray"}}`)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := session.Next(ctx)
	var unknown *UnknownEventError
	if !errors.As(err, &unknown) {
		t.Fatalf("Next error = %v, want *UnknownEventError", err)
	}

	applyEvent(t, session, stream, string(EventKindWorkspaceRenamed), WorkspaceRenamedEvent{WorkspaceID: "w1", Label: "still here"})
	if got := session.Workspaces()[0].Label; got != "still here" {
		t.Errorf("label = %q, want still here", got)
	}
}

func workspaceIDs(session *Session) string {
	ids := make([]string, 0, 4)
	for _, workspace := range session.Workspaces() {
		ids = append(ids, workspace.WorkspaceID)
	}
	return fmt.Sprint(ids)
}

func tabIDs(session *Session) string {
	ids := make([]string, 0, 4)
	for _, tab := range session.Tabs() {
		ids = append(ids, tab.TabID)
	}
	return fmt.Sprint(ids)
}

func TestSessionReconnectsAfterStreamDrops(t *testing.T) {
	server := newMirrorServer(t, testSnapshot())
	session := openTestSession(t, server)
	stream := server.acceptStream()

	// The restarted server reports a session that no longer holds w2, and
	// pushes an event on the new stream before answering the snapshot.
	restarted := testSnapshot()
	restarted.Workspaces = restarted.Workspaces[:1]
	restarted.Tabs = restarted.Tabs[:2]
	restarted.Panes = restarted.Panes[:2]
	restarted.Layouts = restarted.Layouts[:2]
	server.setSnapshot(restarted)
	server.onSnapshot(func(m *mirrorServer) {
		m.lastStream().pushSync(eventLine(t, string(EventKindWorkspaceCreated), WorkspaceCreatedEvent{
			Workspace: testWorkspace("w3", "three"),
		}))
	})
	stream.dropConn()

	event := nextEvent(t, session)
	resync, ok := event.(*ResyncEvent)
	if !ok {
		t.Fatalf("event is %T, want *ResyncEvent", event)
	}
	if event.EventName() != "session.resync" {
		t.Errorf("event name = %q, want session.resync", event.EventName())
	}
	if !errors.Is(resync.Cause, ErrStreamClosed) {
		t.Errorf("cause = %v, want ErrStreamClosed", resync.Cause)
	}
	if got := server.snapshotCount(); got != 2 {
		t.Errorf("snapshot calls = %d, want 2", got)
	}
	if got := workspaceIDs(session); got != "[w1]" {
		t.Errorf("workspaces = %s, want [w1] from the new snapshot", got)
	}
	if _, ok := session.Pane("w2:p1"); ok {
		t.Error("the rebuilt mirror kept a pane the new snapshot does not report")
	}

	// The event that arrived during the second bootstrap follows the resync.
	if _, ok := nextEvent(t, session).(*WorkspaceCreatedEvent); !ok {
		t.Fatal("the event buffered during the reconnect was not delivered after the resync")
	}
	if got := workspaceIDs(session); got != "[w1 w3]" {
		t.Errorf("workspaces = %s, want [w1 w3]", got)
	}

	// The new stream stays live.
	second := server.acceptStream()
	applyEvent(t, session, second, string(EventKindWorkspaceRenamed), WorkspaceRenamedEvent{WorkspaceID: "w1", Label: "after handoff"})
	if got := session.Workspaces()[0].Label; got != "after handoff" {
		t.Errorf("label = %q, want after handoff", got)
	}
}

func TestSessionReconnectBacksOffAndStopsWithTheContext(t *testing.T) {
	var mu sync.Mutex
	attempts := 0
	cfg := sessionConfig{backoff: func(int) time.Duration {
		mu.Lock()
		attempts++
		mu.Unlock()
		return 5 * time.Millisecond
	}}

	server := newMirrorServer(t, testSnapshot())
	session := openTestSessionWith(t, server, cfg)
	stream := server.acceptStream()

	// The server is gone for good, so every attempt fails to dial.
	_ = server.fake.listener.Close()
	stream.dropConn()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := session.Next(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Next error = %v, want the context deadline", err)
	}
	mu.Lock()
	tried := attempts
	mu.Unlock()
	if tried < 2 {
		t.Errorf("backoff was consulted %d times, want more than once", tried)
	}

	if err := session.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := session.Next(context.Background()); !errors.Is(err, ErrStreamClosed) {
		t.Fatalf("Next after Close = %v, want ErrStreamClosed", err)
	}
}

func TestSessionCloseUnblocksNext(t *testing.T) {
	server := newMirrorServer(t, testSnapshot())
	session := openTestSession(t, server)
	server.acceptStream()

	errs := make(chan error, 1)
	go func() {
		_, err := session.Next(context.Background())
		errs <- err
	}()

	if err := session.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case err := <-errs:
		if !errors.Is(err, ErrStreamClosed) {
			t.Fatalf("Next error = %v, want ErrStreamClosed", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not unblock Next")
	}
}

func TestSessionServesConcurrentReaders(t *testing.T) {
	server := newMirrorServer(t, testSnapshot())
	session := openTestSession(t, server)
	stream := server.acceptStream()

	stop := make(chan struct{})
	var readers sync.WaitGroup
	for range 4 {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				for _, workspace := range session.Workspaces() {
					_ = workspace.Label
				}
				for _, tab := range session.Tabs() {
					_, _ = session.Layout(tab.TabID)
				}
				for _, agent := range session.Agents() {
					_, _ = session.Pane(agent.PaneID)
				}
			}
		}()
	}

	for i := range 20 {
		pane := testPane("w1", "w1:t1", fmt.Sprintf("w1:p%d", 100+i))
		applyEvent(t, session, stream, string(EventKindPaneCreated), PaneCreatedEvent{Pane: pane})
	}

	// Two more callers of Next must not corrupt the queue; both find nothing.
	var callers sync.WaitGroup
	for range 2 {
		callers.Add(1)
		go func() {
			defer callers.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			_, _ = session.Next(ctx)
		}()
	}
	callers.Wait()

	close(stop)
	readers.Wait()

	if _, ok := session.Pane("w1:p119"); !ok {
		t.Error("the last pane event did not reach the mirror")
	}
}

func TestLiveOpenSession(t *testing.T) {
	client := newLiveClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	session, err := OpenSession(ctx, client)
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	defer func() { _ = session.Close() }()

	workspaces := session.Workspaces()
	if len(workspaces) == 0 {
		t.Fatal("the live session reports no workspace")
	}
	for _, workspace := range workspaces {
		if _, ok := session.Layout(workspace.ActiveTabID); !ok {
			t.Errorf("no layout for the active tab %s of workspace %s", workspace.ActiveTabID, workspace.WorkspaceID)
		}
	}
	for _, tab := range session.Tabs() {
		if tab.WorkspaceID == "" {
			t.Errorf("tab %s reports no workspace", tab.TabID)
		}
	}
	for _, agent := range session.Agents() {
		if _, ok := session.Pane(agent.PaneID); !ok {
			t.Errorf("agent in pane %s has no pane in the mirror", agent.PaneID)
		}
	}
	if paneID := os.Getenv("HERDR_PANE_ID"); paneID != "" {
		if _, ok := session.Pane(paneID); !ok {
			t.Errorf("pane %s is missing from the mirror", paneID)
		}
	}
}

// The collector stops as soon as the snapshot answers, so a buffered event may
// be taken from the connection or still be waiting on it. Repeating the
// bootstrap covers both.
func TestOpenSessionKeepsEveryBufferedEvent(t *testing.T) {
	for range 20 {
		server := newMirrorServer(t, testSnapshot())
		server.onSnapshot(func(m *mirrorServer) {
			stream := m.lastStream()
			for i := range 3 {
				pane := testPane("w1", "w1:t1", fmt.Sprintf("w1:p%d", 20+i))
				stream.pushSync(eventLine(t, string(EventKindPaneCreated), PaneCreatedEvent{Pane: pane}))
			}
		})

		session := openTestSession(t, server)
		server.acceptStream()
		for i := range 3 {
			event := nextEvent(t, session)
			created, ok := event.(*PaneCreatedEvent)
			if !ok {
				t.Fatalf("event %d is %T, want *PaneCreatedEvent", i, event)
			}
			if want := fmt.Sprintf("w1:p%d", 20+i); created.Pane.PaneID != want {
				t.Fatalf("event %d is for pane %s, want %s", i, created.Pane.PaneID, want)
			}
		}
		if err := session.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}
}

func TestSessionSnapshotReturnsTheWholeMirror(t *testing.T) {
	server := newMirrorServer(t, testSnapshot())
	session := openTestSession(t, server, PaneCreatedSubscription{})
	server.acceptStream()

	got := session.Snapshot()

	if got.Version != "0.9.0" || got.Protocol != SchemaProtocol {
		t.Errorf("Version = %q, Protocol = %d", got.Version, got.Protocol)
	}
	if len(got.Workspaces) != 2 || len(got.Tabs) != 3 || len(got.Panes) != 3 ||
		len(got.Agents) != 1 || len(got.Layouts) != 3 {
		t.Fatalf("counts: workspaces %d tabs %d panes %d agents %d layouts %d",
			len(got.Workspaces), len(got.Tabs), len(got.Panes), len(got.Agents), len(got.Layouts))
	}
	if got.FocusedWorkspaceID.ValueOrZero() != "w1" || got.FocusedTabID.ValueOrZero() != "w1:t1" || got.FocusedPaneID.ValueOrZero() != "w1:p1" {
		t.Errorf("focused: workspace %q tab %q pane %q",
			got.FocusedWorkspaceID.ValueOrZero(), got.FocusedTabID.ValueOrZero(), got.FocusedPaneID.ValueOrZero())
	}

	got.Workspaces[0].Label = "rewritten"
	got.Panes[0].Tokens.ValueOrZero()["pane"] = "rewritten"
	again := session.Snapshot()
	if again.Workspaces[0].Label != "one" || again.Panes[0].Tokens.ValueOrZero()["pane"] != "w1:p1" {
		t.Error("Snapshot shares state with the mirror")
	}
}

// Focus is exclusive, so a snapshot taken after it moves reports the new
// holder, and reports none once the focused pane is gone.
func TestSessionSnapshotTracksFocus(t *testing.T) {
	server := newMirrorServer(t, testSnapshot())
	session := openTestSession(t, server)
	stream := server.acceptStream()

	applyEvent(t, session, stream, string(EventKindPaneFocused), PaneFocusedEvent{PaneID: "w1:p2", WorkspaceID: "w1"})
	if got := session.Snapshot().FocusedPaneID.ValueOrZero(); got != "w1:p2" {
		t.Errorf("FocusedPaneID = %q, want w1:p2", got)
	}

	applyEvent(t, session, stream, string(EventKindPaneClosed), PaneClosedEvent{PaneID: "w1:p2", WorkspaceID: "w1"})
	if got := session.Snapshot().FocusedPaneID; got.IsSet() {
		t.Errorf("FocusedPaneID = %q, want none", got.ValueOrZero())
	}
}

// herdr emits no layout.updated when focus moves, so the mirror has to carry
// the new focused pane into the layout of that pane's tab itself. Measured
// against a running server: pane.focus produces pane.focused alone, while
// layout.export already reports the new focused_pane_id.
func TestSessionKeepsLayoutFocusCurrent(t *testing.T) {
	// The shared fixture gives every tab one pane, so focus cannot move
	// inside a layout. This one puts two panes in w1:t1.
	snapshot := testSnapshot()
	third := testPane("w1", "w1:t1", "w1:p3")
	snapshot.Panes = append(snapshot.Panes, third)
	snapshot.Layouts[0] = testLayout("w1", "w1:t1", "w1:p1", "w1:p3")

	server := newMirrorServer(t, snapshot)
	session := openTestSession(t, server)
	stream := server.acceptStream()

	if before, _ := session.Layout("w1:t1"); before.FocusedPaneID != "w1:p1" {
		t.Fatalf("layout w1:t1 starts focused on %q, want w1:p1", before.FocusedPaneID)
	}

	applyEvent(t, session, stream, string(EventKindPaneFocused), PaneFocusedEvent{PaneID: "w1:p3", WorkspaceID: "w1"})

	if moved, _ := session.Layout("w1:t1"); moved.FocusedPaneID != "w1:p3" {
		t.Errorf("layout w1:t1 focused on %q, want w1:p3", moved.FocusedPaneID)
	}
	// The field is per tab, so another tab keeps the pane it had.
	if untouched, _ := session.Layout("w1:t2"); untouched.FocusedPaneID != "w1:p2" {
		t.Errorf("layout w1:t2 focused on %q, want it left at w1:p2", untouched.FocusedPaneID)
	}
	if got := session.Snapshot().FocusedPaneID.ValueOrZero(); got != "w1:p3" {
		t.Errorf("snapshot FocusedPaneID = %q, want w1:p3", got)
	}
}
