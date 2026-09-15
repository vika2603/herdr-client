package herdr

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"
)

// mirrorServer answers the two methods a Session bootstrap uses: it keeps an
// events.subscribe connection open and pushes what a test hands it, and
// answers session.snapshot on a fresh connection, the way the real server
// does. It builds on fakeServer, so the wire framing is the transport's.
type mirrorServer struct {
	t    *testing.T
	fake *fakeServer

	accepted chan *mirrorStream

	mu             sync.Mutex
	snapshot       SessionSnapshot
	snapshots      int
	beforeSnapshot func(*mirrorServer)
	streams        []*mirrorStream
}

// mirrorStream is one accepted subscription connection.
type mirrorStream struct {
	lines    chan pushedLine
	drop     chan struct{}
	dropOnce sync.Once
}

// pushedLine is one line to write, with an optional signal for a test that
// needs the write to have happened before it continues.
type pushedLine struct {
	text    string
	written chan struct{}
}

func newMirrorServer(t *testing.T, snapshot SessionSnapshot) *mirrorServer {
	t.Helper()
	server := &mirrorServer{t: t, snapshot: snapshot, accepted: make(chan *mirrorStream, 8)}
	server.fake = newFakeServer(t, server.handle)
	t.Cleanup(server.dropAll)
	return server
}

func (m *mirrorServer) handle(session *fakeSession) {
	switch method := session.request().Method; method {
	case MethodEventsSubscribe:
		m.serveStream(session)
	case MethodSessionSnapshot:
		m.serveSnapshot(session)
	default:
		session.fail(ErrCodeInvalidRequest, "unexpected method "+method)
	}
}

func (m *mirrorServer) serveStream(session *fakeSession) {
	stream := &mirrorStream{lines: make(chan pushedLine, 16), drop: make(chan struct{})}
	m.mu.Lock()
	m.streams = append(m.streams, stream)
	m.mu.Unlock()

	session.success(json.RawMessage(subscriptionStarted))
	m.accepted <- stream

	for {
		select {
		case line := <-stream.lines:
			session.writeLine(line.text)
			if line.written != nil {
				close(line.written)
			}
		case <-stream.drop:
			_ = session.conn.Close()
			return
		}
	}
}

func (m *mirrorServer) serveSnapshot(session *fakeSession) {
	m.mu.Lock()
	m.snapshots++
	snapshot := m.snapshot
	hook := m.beforeSnapshot
	m.mu.Unlock()

	if hook != nil {
		hook(m)
	}
	session.success(SessionSnapshotResponse{Snapshot: snapshot})
}

// setSnapshot replaces what the next session.snapshot call answers.
func (m *mirrorServer) setSnapshot(snapshot SessionSnapshot) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.snapshot = snapshot
}

// onSnapshot installs a hook that runs before the snapshot response is
// written, which is how a test makes an event arrive before the snapshot.
func (m *mirrorServer) onSnapshot(hook func(*mirrorServer)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.beforeSnapshot = hook
}

func (m *mirrorServer) snapshotCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.snapshots
}

// lastStream returns the subscription connection accepted most recently.
func (m *mirrorServer) lastStream() *mirrorStream {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.streams) == 0 {
		return nil
	}
	return m.streams[len(m.streams)-1]
}

// acceptStream waits for the next subscription connection.
func (m *mirrorServer) acceptStream() *mirrorStream {
	m.t.Helper()
	select {
	case stream := <-m.accepted:
		return stream
	case <-time.After(2 * time.Second):
		m.t.Fatal("no subscription connection was accepted")
		return nil
	}
}

func (m *mirrorServer) dropAll() {
	m.mu.Lock()
	streams := append([]*mirrorStream(nil), m.streams...)
	m.mu.Unlock()
	for _, stream := range streams {
		stream.dropConn()
	}
}

// push queues one line for the subscription connection.
func (s *mirrorStream) push(line string) {
	s.lines <- pushedLine{text: line}
}

// pushSync queues one line and returns once it has been written.
func (s *mirrorStream) pushSync(line string) {
	written := make(chan struct{})
	s.lines <- pushedLine{text: line, written: written}
	<-written
}

// dropConn closes the subscription connection without notice, which is what a
// restarting server does to its subscribers.
func (s *mirrorStream) dropConn() {
	s.dropOnce.Do(func() { close(s.drop) })
}

// openTestSession opens a Session that never waits before a reconnect.
func openTestSession(t *testing.T, server *mirrorServer, subs ...Subscription) *Session {
	t.Helper()
	return openTestSessionWith(t, server, sessionConfig{backoff: func(int) time.Duration { return 0 }}, subs...)
}

func openTestSessionWith(t *testing.T, server *mirrorServer, cfg sessionConfig, subs ...Subscription) *Session {
	t.Helper()
	session, err := openSession(context.Background(), New(server.fake.path), cfg, subs)
	if err != nil {
		t.Fatalf("openSession: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

// nextEvent reads one event and fails when none arrives.
func nextEvent(t *testing.T, session *Session) Event {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	event, err := session.Next(ctx)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	return event
}

// expectNoEvent fails when another event is waiting.
func expectNoEvent(t *testing.T, session *Session) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	event, err := session.Next(ctx)
	if err == nil {
		t.Fatalf("unexpected event %s", event.EventName())
	}
	if ctx.Err() == nil {
		t.Fatalf("Next error = %v, want the context deadline", err)
	}
}

// applyEvent pushes one event, reads it back and returns it, so the mirror has
// seen it when applyEvent returns.
func applyEvent(t *testing.T, session *Session, stream *mirrorStream, event string, data any) Event {
	t.Helper()
	stream.push(eventLine(t, event, data))
	return nextEvent(t, session)
}

// eventLine encodes one pushed line. The generated payload types write their
// own "type" field, so data is the payload as the server sends it.
func eventLine(t *testing.T, event string, data any) string {
	t.Helper()
	encoded, err := json.Marshal(map[string]any{"event": event, "data": data})
	if err != nil {
		t.Fatalf("encode %s event: %v", event, err)
	}
	return string(encoded)
}

// testSnapshot is a two-workspace session: w1 holds tabs t1 and t2 with one
// pane each, w2 holds one tab with one pane, and w1:p1 runs an agent.
func testSnapshot() SessionSnapshot {
	first := testPane("w1", "w1:t1", "w1:p1")
	first.Focused = true
	second := testPane("w1", "w1:t2", "w1:p2")
	third := testPane("w2", "w2:t1", "w2:p1")

	firstWorkspace := testWorkspace("w1", "one")
	firstWorkspace.Focused = true
	firstTab := testTab("w1", "w1:t1")
	firstTab.Focused = true

	return SessionSnapshot{
		Version:            "0.9.0",
		Protocol:           SchemaProtocol,
		FocusedWorkspaceID: Some("w1"),
		FocusedTabID:       Some("w1:t1"),
		FocusedPaneID:      Some("w1:p1"),
		Workspaces:         []WorkspaceInfo{firstWorkspace, testWorkspace("w2", "two")},
		Tabs:               []TabInfo{firstTab, testTab("w1", "w1:t2"), testTab("w2", "w2:t1")},
		Panes:              []PaneInfo{first, second, third},
		Agents:             []AgentInfo{testAgent(first, "codex")},
		Layouts: []PaneLayoutSnapshot{
			testLayout("w1", "w1:t1", "w1:p1"),
			testLayout("w1", "w1:t2", "w1:p2"),
			testLayout("w2", "w2:t1", "w2:p1"),
		},
	}
}

func testWorkspace(workspaceID, label string) WorkspaceInfo {
	return WorkspaceInfo{
		ActiveTabID: workspaceID + ":t1",
		AgentStatus: AgentStatusIdle,
		Label:       label,
		Number:      1,
		PaneCount:   1,
		TabCount:    1,
		WorkspaceID: workspaceID,
	}
}

func testTab(workspaceID, tabID string) TabInfo {
	return TabInfo{
		AgentStatus: AgentStatusIdle,
		Label:       tabID,
		Number:      1,
		PaneCount:   1,
		TabID:       tabID,
		WorkspaceID: workspaceID,
	}
}

func testPane(workspaceID, tabID, paneID string) PaneInfo {
	return PaneInfo{
		AgentStatus: AgentStatusIdle,
		Cwd:         Some("/tmp"),
		PaneID:      paneID,
		Revision:    1,
		TabID:       tabID,
		TerminalID:  "term_" + paneID,
		Tokens:      Some(map[string]string{"pane": paneID}),
		WorkspaceID: workspaceID,
	}
}

func testAgent(pane PaneInfo, agent string) AgentInfo {
	info := AgentInfo{
		AgentStatus: AgentStatusWorking,
		Name:        Some(agent + " in " + pane.PaneID),
		PaneID:      pane.PaneID,
		Revision:    pane.Revision,
		TabID:       pane.TabID,
		TerminalID:  pane.TerminalID,
		WorkspaceID: pane.WorkspaceID,
	}
	info.Agent = Some(agent)
	return info
}

func testLayout(workspaceID, tabID string, paneIDs ...string) PaneLayoutSnapshot {
	area := PaneLayoutRect{Height: 40, Width: 80}
	layout := PaneLayoutSnapshot{
		Area:        area,
		TabID:       tabID,
		WorkspaceID: workspaceID,
	}
	if len(paneIDs) > 0 {
		layout.FocusedPaneID = paneIDs[0]
	}
	for _, paneID := range paneIDs {
		layout.Panes = append(layout.Panes, PaneLayoutPane{
			Focused: paneID == layout.FocusedPaneID,
			PaneID:  paneID,
			Rect:    area,
		})
	}
	return layout
}

// paneIDs names the panes a layout covers, for comparing layouts in a test
// failure message.
func paneIDs(layout PaneLayoutSnapshot) string {
	ids := make([]string, 0, len(layout.Panes))
	for _, pane := range layout.Panes {
		ids = append(ids, pane.PaneID)
	}
	return fmt.Sprint(ids)
}
