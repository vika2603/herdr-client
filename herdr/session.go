package herdr

import (
	"context"
	"errors"
	"maps"
	"slices"
	"sync"
	"time"
)

// ResyncEvent reports that the mirror was rebuilt from a fresh snapshot after
// the event stream ended, which happens when the server restarts on live
// handoff. The events of the gap are not recoverable, so state a caller
// derived from earlier events has to be discarded.
type ResyncEvent struct {
	// Cause is the error that ended the previous stream. It is never nil: a
	// stream that ended without one is reported as ErrStreamClosed.
	Cause error
}

// EventName returns "session.resync", a name no server event carries.
func (ResyncEvent) EventName() string { return "session.resync" }

// Session mirrors the server state the session snapshot reports and keeps it
// current from an events.subscribe stream.
//
// The accessors are safe for concurrent use. Next applies each event to the
// mirror before returning it, so a reader that consults the accessors after
// Next sees the state that event produced; events that have not been read yet
// are not applied. Values the accessors return are copies, but the pointers
// inside them are shared with the mirror and must not be written through.
type Session struct {
	client *Client
	subs   []Subscription
	cfg    sessionConfig
	done   chan struct{}

	// nextMu serialises Next, which owns the queue of events that are read
	// but not yet delivered.
	nextMu  sync.Mutex
	pending []*RawEvent
	resync  *ResyncEvent
	cause   error

	streamMu sync.Mutex
	stream   *EventStream
	closed   bool

	mu    sync.RWMutex
	cache *sessionCache
}

// sessionConfig holds what the tests replace to avoid waiting.
type sessionConfig struct {
	backoff func(attempt int) time.Duration
}

const (
	reconnectBaseDelay = 100 * time.Millisecond
	reconnectMaxDelay  = 5 * time.Second
)

func defaultSessionConfig() sessionConfig {
	return sessionConfig{backoff: defaultBackoff}
}

// defaultBackoff doubles the delay per attempt up to reconnectMaxDelay.
func defaultBackoff(attempt int) time.Duration {
	delay := reconnectBaseDelay
	for i := 1; i < attempt && delay < reconnectMaxDelay; i++ {
		delay *= 2
	}
	return min(delay, reconnectMaxDelay)
}

// MirrorSubscriptions returns the subscriptions a Session uses when it is
// opened without any: every lifecycle subscription that needs no parameters.
// The three pane-scoped subscriptions are absent because each of them
// requires a pane id.
func MirrorSubscriptions() []Subscription {
	return []Subscription{
		WorkspaceCreatedSubscription{},
		WorkspaceUpdatedSubscription{},
		WorkspaceMetadataUpdatedSubscription{},
		WorkspaceRenamedSubscription{},
		WorkspaceMovedSubscription{},
		WorkspaceReorderedSubscription{},
		WorkspaceClosedSubscription{},
		WorkspaceFocusedSubscription{},
		WorktreeCreatedSubscription{},
		WorktreeOpenedSubscription{},
		WorktreeRemovedSubscription{},
		TabCreatedSubscription{},
		TabClosedSubscription{},
		TabRenamedSubscription{},
		TabMovedSubscription{},
		TabFocusedSubscription{},
		PaneCreatedSubscription{},
		PaneClosedSubscription{},
		PaneUpdatedSubscription{},
		PaneFocusedSubscription{},
		PaneMovedSubscription{},
		PaneExitedSubscription{},
		PaneAgentDetectedSubscription{},
		LayoutUpdatedSubscription{},
	}
}

// OpenSession subscribes, takes a session snapshot and returns the mirror the
// two produce. Without a subscription it mirrors everything
// MirrorSubscriptions lists.
//
// ctx bounds the bootstrap only. Once the Session exists it lives until Close;
// each Next takes its own context.
func OpenSession(ctx context.Context, c *Client, subs ...Subscription) (*Session, error) {
	return openSession(ctx, c, defaultSessionConfig(), subs)
}

func openSession(ctx context.Context, c *Client, cfg sessionConfig, subs []Subscription) (*Session, error) {
	if len(subs) == 0 {
		subs = MirrorSubscriptions()
	}
	boot, err := bootstrapSession(ctx, c, subs)
	if err != nil {
		return nil, err
	}
	return &Session{
		client:  c,
		subs:    subs,
		cfg:     cfg,
		done:    make(chan struct{}),
		pending: boot.buffered,
		stream:  boot.stream,
		cache:   boot.cache,
	}, nil
}

// Workspaces returns the mirrored workspaces in the order the server reports
// them.
func (s *Session) Workspaces() []WorkspaceInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneEach(s.cache.workspaces.values(), cloneWorkspace)
}

// Tabs returns the mirrored tabs in the order the server reports them.
func (s *Session) Tabs() []TabInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cache.tabs.values()
}

// Pane returns the mirrored pane with the given id.
func (s *Session) Pane(paneID string) (PaneInfo, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	pane, ok := s.cache.panes.get(paneID)
	if !ok {
		return PaneInfo{}, false
	}
	return clonePane(pane), true
}

// Agents returns the mirrored agents in the order the server reports them.
func (s *Session) Agents() []AgentInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneEach(s.cache.agents.values(), cloneAgent)
}

// Layout returns the mirrored layout of the given tab.
func (s *Session) Layout(tabID string) (PaneLayoutSnapshot, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	layout, ok := s.cache.layouts.get(tabID)
	if !ok {
		return PaneLayoutSnapshot{}, false
	}
	return cloneLayout(layout), true
}

// Snapshot returns the whole mirror as one SessionSnapshot, read under a
// single lock so that every part of it describes the same moment. The
// accessors above each take the lock separately, so a caller that needs one
// consistent frame, or that needs the panes and layouts the accessors do not
// list, should read it here.
//
// Version and Protocol are those of the snapshot the mirror was built from; a
// resync replaces them, since it may reach an upgraded server. The focused
// ids are read back from the Focused flag the mirror maintains, and are unset
// when nothing holds focus.
func (s *Session) Snapshot() SessionSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()

	workspaces := cloneEach(s.cache.workspaces.values(), cloneWorkspace)
	tabs := s.cache.tabs.values()
	panes := cloneEach(s.cache.panes.values(), clonePane)
	return SessionSnapshot{
		Version:            s.cache.version,
		Protocol:           s.cache.protocol,
		Workspaces:         workspaces,
		Tabs:               tabs,
		Panes:              panes,
		Agents:             cloneEach(s.cache.agents.values(), cloneAgent),
		Layouts:            cloneEach(s.cache.layouts.values(), cloneLayout),
		FocusedWorkspaceID: focusedID(workspaces, func(w WorkspaceInfo) (string, bool) { return w.WorkspaceID, w.Focused }),
		FocusedTabID:       focusedID(tabs, func(t TabInfo) (string, bool) { return t.TabID, t.Focused }),
		FocusedPaneID:      focusedID(panes, func(p PaneInfo) (string, bool) { return p.PaneID, p.Focused }),
	}
}

// focusedID returns the id of the one focused value, which the server keeps
// exclusive across the session.
func focusedID[T any](values []T, read func(T) (string, bool)) *string {
	for _, value := range values {
		if id, focused := read(value); focused {
			return &id
		}
	}
	return nil
}

// Next returns the next event, having applied it to the mirror.
//
// When the stream ends because the server went away, Next reconnects, rebuilds
// the mirror from a fresh snapshot and reports the gap as a *ResyncEvent
// before the events of the new stream. Reconnection waits between attempts and
// gives up when ctx is done or the Session is closed, both of which leave the
// Session reconnectable on a later Next. Use errors.Is to identify a closed
// session (ErrStreamClosed) or a canceled read/backoff (ctx.Err()).
//
// Payload decoding failures carry OpDecode and do not reach the mirror. An
// unknown event remains inspectable as *UnknownEventError through errors.As.
func (s *Session) Next(ctx context.Context) (Event, error) {
	s.nextMu.Lock()
	defer s.nextMu.Unlock()

	for {
		if s.resync != nil {
			event := s.resync
			s.resync = nil
			return event, nil
		}
		if len(s.pending) > 0 {
			raw := s.pending[0]
			s.pending = s.pending[1:]
			return s.deliver(raw)
		}
		stream := s.currentStream()
		if stream == nil {
			if s.isClosed() {
				return nil, ErrStreamClosed
			}
			if err := s.reconnect(ctx); err != nil {
				return nil, err
			}
			continue
		}
		raw, err := stream.stream.Next(ctx)
		switch {
		case err == nil:
			return s.deliver(raw)
		case !errors.Is(err, ErrStreamClosed):
			return nil, err
		case s.isClosed():
			return nil, ErrStreamClosed
		default:
			// The server is gone. Drop the connection and reconnect on the
			// next turn of the loop.
			s.cause = err
			s.closeStream()
		}
	}
}

// Close ends the stream. A blocked Next matches ErrStreamClosed through
// errors.Is; a connection close failure retains its OpClose context.
func (s *Session) Close() error {
	s.streamMu.Lock()
	if s.closed {
		s.streamMu.Unlock()
		return nil
	}
	s.closed = true
	close(s.done)
	stream := s.stream
	s.stream = nil
	s.streamMu.Unlock()

	if stream == nil {
		return nil
	}
	return stream.Close()
}

// deliver decodes one pushed line, applies it to the mirror and returns it.
func (s *Session) deliver(raw *RawEvent) (Event, error) {
	event, err := DecodeEvent(raw.Event, raw.Data)
	if err != nil {
		return nil, opError(MethodEventsSubscribe, OpDecode, err)
	}
	s.mu.Lock()
	s.cache.apply(event)
	s.mu.Unlock()
	return event, nil
}

// reconnect bootstraps a new stream and mirror, and queues the ResyncEvent
// that reports the gap.
func (s *Session) reconnect(ctx context.Context) error {
	cause := s.cause
	if cause == nil {
		cause = ErrStreamClosed
	}
	for attempt := 1; ; attempt++ {
		if err := s.wait(ctx, s.cfg.backoff(attempt)); err != nil {
			return err
		}
		boot, err := bootstrapSession(ctx, s.client, s.subs)
		if err != nil {
			if !isRetryable(err) {
				return err
			}
			continue
		}
		if !s.install(boot) {
			_ = boot.stream.Close()
			return ErrStreamClosed
		}
		s.pending = boot.buffered
		s.resync = &ResyncEvent{Cause: cause}
		s.cause = nil
		return nil
	}
}

// wait blocks for d, or until ctx is done or the Session is closed.
func (s *Session) wait(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.done:
		return ErrStreamClosed
	case <-timer.C:
		return nil
	}
}

// install publishes a fresh stream and mirror. It reports false when the
// Session was closed while the bootstrap was running.
func (s *Session) install(boot *bootstrapResult) bool {
	s.streamMu.Lock()
	if s.closed {
		s.streamMu.Unlock()
		return false
	}
	s.stream = boot.stream
	s.streamMu.Unlock()

	s.mu.Lock()
	s.cache = boot.cache
	s.mu.Unlock()
	return true
}

func (s *Session) currentStream() *EventStream {
	s.streamMu.Lock()
	defer s.streamMu.Unlock()
	return s.stream
}

func (s *Session) isClosed() bool {
	s.streamMu.Lock()
	defer s.streamMu.Unlock()
	return s.closed
}

func (s *Session) closeStream() {
	s.streamMu.Lock()
	stream := s.stream
	s.stream = nil
	s.streamMu.Unlock()
	if stream != nil {
		_ = stream.Close()
	}
}

// isRetryable reports whether a failed bootstrap is worth another attempt. An
// error response means the request itself is unacceptable to the server, so
// repeating it cannot succeed; everything else is treated as the server not
// being there yet.
func isRetryable(err error) bool {
	var apiErr *Error
	if errors.As(err, &apiErr) {
		return false
	}
	var unexpected *UnexpectedResultError
	return !errors.As(err, &unexpected)
}

// bootstrapResult is one completed bootstrap: the live stream, the mirror
// built from the snapshot, and the events that arrived before the snapshot
// answered.
type bootstrapResult struct {
	stream   *EventStream
	cache    *sessionCache
	buffered []*RawEvent
}

// bootstrapSession runs the documented order: subscribe first, buffer what the
// stream pushes, then take the snapshot. Buffering before the snapshot is what
// keeps an event that fires during the call from being lost; the buffered
// events are applied after the snapshot is installed, and applying one the
// snapshot already reflects assigns the same state again.
func bootstrapSession(ctx context.Context, c *Client, subs []Subscription) (*bootstrapResult, error) {
	stream, err := c.Subscribe(ctx, subs...)
	if err != nil {
		return nil, err
	}
	collector := collectEvents(stream)
	snapshot, err := c.SessionSnapshot(ctx)
	buffered, streamErr := collector.stop()
	if err == nil {
		err = streamErr
	}
	if err != nil {
		_ = stream.Close()
		return nil, err
	}
	return &bootstrapResult{
		stream:   stream,
		cache:    newSessionCache(snapshot.Snapshot),
		buffered: buffered,
	}, nil
}

// eventCollector drains a stream into a buffer until it is stopped.
type eventCollector struct {
	cancel context.CancelFunc
	done   chan struct{}
	events []*RawEvent
	err    error
}

func collectEvents(stream *EventStream) *eventCollector {
	ctx, cancel := context.WithCancel(context.Background())
	collector := &eventCollector{cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(collector.done)
		for {
			event, err := stream.stream.Next(ctx)
			if err != nil {
				if !errors.Is(err, context.Canceled) {
					collector.err = err
				}
				return
			}
			collector.events = append(collector.events, event)
		}
	}()
	return collector
}

// stop ends the collection and returns what was buffered. A line the reader
// had already taken from the connection but not yet handed over stays on the
// stream and is read by the first Next.
func (c *eventCollector) stop() ([]*RawEvent, error) {
	c.cancel()
	<-c.done
	return c.events, c.err
}

// sessionCache is the mirrored state. Every collection keeps the order the
// server reported.
type sessionCache struct {
	workspaces orderedCollection[WorkspaceInfo]
	tabs       orderedCollection[TabInfo]
	panes      orderedCollection[PaneInfo]
	agents     orderedCollection[AgentInfo]
	layouts    orderedCollection[PaneLayoutSnapshot]
	// version and protocol come from the snapshot the mirror was built from.
	// No event carries them, so they change only when a resync rebuilds the
	// cache against a server that may have been upgraded.
	version  string
	protocol uint32
}

func workspaceKey(w WorkspaceInfo) string   { return w.WorkspaceID }
func tabKey(t TabInfo) string               { return t.TabID }
func paneKey(p PaneInfo) string             { return p.PaneID }
func agentKey(a AgentInfo) string           { return a.PaneID }
func layoutKey(l PaneLayoutSnapshot) string { return l.TabID }

func newSessionCache(snapshot SessionSnapshot) *sessionCache {
	cache := &sessionCache{version: snapshot.Version, protocol: snapshot.Protocol}
	cache.workspaces.reset(snapshot.Workspaces, workspaceKey)
	cache.tabs.reset(snapshot.Tabs, tabKey)
	cache.panes.reset(snapshot.Panes, paneKey)
	cache.agents.reset(snapshot.Agents, agentKey)
	cache.layouts.reset(snapshot.Layouts, layoutKey)
	return cache
}

// apply folds one event into the mirror. Applying the same event twice leaves
// the same state, which is what lets a bootstrap replay events the snapshot
// already reflects.
func (c *sessionCache) apply(event Event) {
	switch e := event.(type) {
	case *WorkspaceCreatedEvent:
		c.workspaces.set(e.Workspace.WorkspaceID, e.Workspace)
	case *WorkspaceUpdatedEvent:
		c.workspaces.set(e.Workspace.WorkspaceID, e.Workspace)
	case *WorkspaceMetadataUpdatedEvent:
		c.workspaces.set(e.Workspace.WorkspaceID, e.Workspace)
	case *WorkspaceRenamedEvent:
		if workspace, ok := c.workspaces.get(e.WorkspaceID); ok {
			workspace.Label = e.Label
			c.workspaces.set(e.WorkspaceID, workspace)
		}
	case *WorkspaceMovedEvent:
		c.workspaces.reset(e.Workspaces, workspaceKey)
	case *WorkspaceReorderedEvent:
		c.workspaces.reset(e.Workspaces, workspaceKey)
	case *WorkspaceFocusedEvent:
		c.focusWorkspace(e.WorkspaceID)
	case *WorkspaceClosedEvent:
		c.closeWorkspace(e.WorkspaceID)
	case *WorktreeCreatedEvent:
		c.workspaces.set(e.Workspace.WorkspaceID, e.Workspace)
	case *WorktreeOpenedEvent:
		c.workspaces.set(e.Workspace.WorkspaceID, e.Workspace)
	case *WorktreeRemovedEvent:
		if e.Workspace != nil {
			c.workspaces.set(e.Workspace.WorkspaceID, *e.Workspace)
		}
	case *TabCreatedEvent:
		c.tabs.set(e.Tab.TabID, e.Tab)
	case *TabRenamedEvent:
		if tab, ok := c.tabs.get(e.TabID); ok {
			tab.Label = e.Label
			c.tabs.set(e.TabID, tab)
		}
	case *TabMovedEvent:
		c.reorderTabs(e.WorkspaceID, e.Tabs)
	case *TabFocusedEvent:
		c.focusTab(e.TabID)
	case *TabClosedEvent:
		c.closeTab(e.TabID)
	case *PaneCreatedEvent:
		c.setPane(e.Pane)
	case *PaneUpdatedEvent:
		c.setPane(e.Pane)
	case *PaneMovedEvent:
		c.movePane(e)
	case *PaneFocusedEvent:
		c.focusPane(e.PaneID)
	case *PaneClosedEvent:
		c.closePane(e.PaneID)
	case *PaneOutputChangedEvent:
		if pane, ok := c.panes.get(e.PaneID); ok {
			pane.Revision = e.Revision
			c.setPane(pane)
		}
	case *PaneScrollChangedEvent:
		if pane, ok := c.panes.get(e.PaneID); ok {
			scroll := e.Scroll
			pane.Scroll = &scroll
			c.setPane(pane)
		}
	case *PaneAgentDetectedEvent:
		c.applyAgentDetected(e)
	case *PaneAgentStatusChangedEvent:
		c.applyAgentStatus(e)
	case *LayoutUpdatedEvent:
		c.layouts.set(e.Layout.TabID, e.Layout)
	}
	// pane.exited leaves the pane in place: the process ended, and a pane
	// that goes away with it is reported by pane.closed. pane.output_matched
	// and an unknown event carry nothing the mirror holds.
}

// setPane installs a pane and refreshes the agent entry that pane carries,
// which repeats the fields AgentInfo shares with PaneInfo.
func (c *sessionCache) setPane(pane PaneInfo) {
	c.panes.set(pane.PaneID, pane)
	if agent, ok := c.agents.get(pane.PaneID); ok {
		c.agents.set(pane.PaneID, mergeAgent(agent, pane))
	}
}

func (c *sessionCache) closePane(paneID string) {
	c.panes.delete(paneID)
	c.agents.delete(paneID)
}

// movePane follows a pane to its destination. The pane id changes with the
// move, so the entry is rekeyed rather than updated.
func (c *sessionCache) movePane(e *PaneMovedEvent) {
	if e.CreatedWorkspace != nil {
		c.workspaces.set(e.CreatedWorkspace.WorkspaceID, *e.CreatedWorkspace)
	}
	if e.CreatedTab != nil {
		c.tabs.set(e.CreatedTab.TabID, *e.CreatedTab)
	}
	agent, hadAgent := c.agents.get(e.PreviousPaneID)
	c.closePane(e.PreviousPaneID)
	c.panes.set(e.Pane.PaneID, e.Pane)
	if hadAgent {
		c.agents.set(e.Pane.PaneID, mergeAgent(agent, e.Pane))
	}
	// The pane already carries its destination, so the cascade below cannot
	// remove it with the container it left.
	if e.ClosedTabID != nil {
		c.closeTab(*e.ClosedTabID)
	}
	if e.ClosedWorkspaceID != nil {
		c.closeWorkspace(*e.ClosedWorkspaceID)
	}
}

// closeTab drops a tab with the panes, agents and layout it held. The server
// does not repeat those removals as separate events.
func (c *sessionCache) closeTab(tabID string) {
	c.tabs.delete(tabID)
	c.layouts.delete(tabID)
	c.panes.deleteWhere(func(pane PaneInfo) bool { return pane.TabID == tabID })
	c.agents.deleteWhere(func(agent AgentInfo) bool { return agent.TabID == tabID })
}

// closeWorkspace drops a workspace with everything it held.
func (c *sessionCache) closeWorkspace(workspaceID string) {
	c.workspaces.delete(workspaceID)
	c.tabs.deleteWhere(func(tab TabInfo) bool { return tab.WorkspaceID == workspaceID })
	c.panes.deleteWhere(func(pane PaneInfo) bool { return pane.WorkspaceID == workspaceID })
	c.agents.deleteWhere(func(agent AgentInfo) bool { return agent.WorkspaceID == workspaceID })
	c.layouts.deleteWhere(func(layout PaneLayoutSnapshot) bool { return layout.WorkspaceID == workspaceID })
}

// reorderTabs replaces the tabs of one workspace with the ordered list the
// event carries, keeping the tabs of the other workspaces where they were.
func (c *sessionCache) reorderTabs(workspaceID string, tabs []TabInfo) {
	at := c.tabs.deleteWhere(func(tab TabInfo) bool { return tab.WorkspaceID == workspaceID })
	c.tabs.insertAt(at, tabs, tabKey)
}

// applyAgentDetected is the authority on which agent a pane runs: a detection
// names it, and the released form ends it, because herdr fires that one when
// the detected agent hands the pane back to the shell (herdr 0.9.0,
// src/events.rs, AppEvent::HookAgentReleased, emitted as PaneAgentDetected in
// src/api/app_api.rs).
func (c *sessionCache) applyAgentDetected(e *PaneAgentDetectedEvent) {
	pane, hasPane := c.panes.get(e.PaneID)
	released := e.Released != nil && *e.Released
	if hasPane {
		if released {
			pane.Agent = nil
			pane.DisplayAgent = nil
		} else {
			pane.Agent = e.Agent
		}
		if e.FinalStatus != nil {
			pane.AgentStatus = *e.FinalStatus
		}
		c.panes.set(e.PaneID, pane)
	}
	if released {
		c.agents.delete(e.PaneID)
		return
	}
	if !hasPane {
		// An agent is only ever detected in a pane the mirror already holds;
		// without one there is nothing to build an AgentInfo from.
		return
	}
	agent, _ := c.agents.get(e.PaneID)
	c.agents.set(e.PaneID, mergeAgent(agent, pane))
}

func (c *sessionCache) applyAgentStatus(e *PaneAgentStatusChangedEvent) {
	pane, hasPane := c.panes.get(e.PaneID)
	if hasPane {
		pane.AgentStatus = e.AgentStatus
		if e.Agent != nil {
			pane.Agent = e.Agent
		}
		if e.DisplayAgent != nil {
			pane.DisplayAgent = e.DisplayAgent
		}
		if e.Title != nil {
			pane.Title = e.Title
		}
		if e.StateLabels != nil {
			pane.StateLabels = e.StateLabels
		}
		c.panes.set(e.PaneID, pane)
	}
	agent, hasAgent := c.agents.get(e.PaneID)
	if !hasAgent {
		return
	}
	if hasPane {
		c.agents.set(e.PaneID, mergeAgent(agent, pane))
		return
	}
	agent.AgentStatus = e.AgentStatus
	c.agents.set(e.PaneID, agent)
}

// The focused flag is exclusive across the whole session, and herdr emits the
// whole workspace, tab and pane chain when the focus moves, so a focus event
// clears the flag on every other entry of its kind. Both hold for herdr 0.9.0.
func (c *sessionCache) focusWorkspace(workspaceID string) {
	c.workspaces.update(func(id string, workspace WorkspaceInfo) WorkspaceInfo {
		workspace.Focused = id == workspaceID
		return workspace
	})
}

func (c *sessionCache) focusTab(tabID string) {
	c.tabs.update(func(id string, tab TabInfo) TabInfo {
		tab.Focused = id == tabID
		return tab
	})
}

func (c *sessionCache) focusPane(paneID string) {
	c.panes.update(func(id string, pane PaneInfo) PaneInfo {
		pane.Focused = id == paneID
		return pane
	})
	c.agents.update(func(id string, agent AgentInfo) AgentInfo {
		agent.Focused = id == paneID
		return agent
	})

	// A cached layout carries the focused pane of its own tab, and herdr
	// emits no layout.updated when focus moves, which was measured against a
	// running server: focusing a pane produces pane.focused alone, while
	// layout.export already reports the new focused_pane_id. Without this the
	// layout would keep naming whichever pane held focus when the layout was
	// last reported. The field is per tab, so the other tabs keep theirs.
	pane, ok := c.panes.get(paneID)
	if !ok {
		return
	}
	layout, ok := c.layouts.get(pane.TabID)
	if !ok {
		return
	}
	layout.FocusedPaneID = paneID
	c.layouts.set(pane.TabID, layout)
}

// mergeAgent refreshes the fields an AgentInfo shares with the pane it runs
// in. The remaining fields are only ever reported by the snapshot.
func mergeAgent(agent AgentInfo, pane PaneInfo) AgentInfo {
	agent.Agent = pane.Agent
	agent.AgentSession = pane.AgentSession
	agent.AgentStatus = pane.AgentStatus
	agent.Cwd = pane.Cwd
	agent.DisplayAgent = pane.DisplayAgent
	agent.Focused = pane.Focused
	agent.ForegroundCwd = pane.ForegroundCwd
	agent.PaneID = pane.PaneID
	agent.Revision = pane.Revision
	agent.StateLabels = pane.StateLabels
	agent.TabID = pane.TabID
	agent.TerminalID = pane.TerminalID
	agent.TerminalTitle = pane.TerminalTitle
	agent.TerminalTitleStripped = pane.TerminalTitleStripped
	agent.Title = pane.Title
	agent.Tokens = pane.Tokens
	agent.WorkspaceID = pane.WorkspaceID
	return agent
}

// cloneEach copies the maps and slices a value owns, so that a caller cannot
// reach into the mirror through them.
func cloneEach[T any](values []T, clone func(T) T) []T {
	for i, value := range values {
		values[i] = clone(value)
	}
	return values
}

func cloneWorkspace(workspace WorkspaceInfo) WorkspaceInfo {
	workspace.Tokens = maps.Clone(workspace.Tokens)
	return workspace
}

func clonePane(pane PaneInfo) PaneInfo {
	pane.StateLabels = maps.Clone(pane.StateLabels)
	pane.Tokens = maps.Clone(pane.Tokens)
	return pane
}

func cloneAgent(agent AgentInfo) AgentInfo {
	agent.StateLabels = maps.Clone(agent.StateLabels)
	agent.Tokens = maps.Clone(agent.Tokens)
	return agent
}

func cloneLayout(layout PaneLayoutSnapshot) PaneLayoutSnapshot {
	layout.Panes = slices.Clone(layout.Panes)
	layout.Splits = slices.Clone(layout.Splits)
	return layout
}

// orderedCollection holds entries by id in the order they were reported.
type orderedCollection[T any] struct {
	ids     []string
	entries map[string]T
}

func (o *orderedCollection[T]) get(id string) (T, bool) {
	value, ok := o.entries[id]
	return value, ok
}

func (o *orderedCollection[T]) values() []T {
	values := make([]T, 0, len(o.ids))
	for _, id := range o.ids {
		values = append(values, o.entries[id])
	}
	return values
}

func (o *orderedCollection[T]) set(id string, value T) {
	if o.entries == nil {
		o.entries = make(map[string]T)
	}
	if _, exists := o.entries[id]; !exists {
		o.ids = append(o.ids, id)
	}
	o.entries[id] = value
}

func (o *orderedCollection[T]) delete(id string) {
	if _, exists := o.entries[id]; !exists {
		return
	}
	delete(o.entries, id)
	o.ids = slices.DeleteFunc(o.ids, func(candidate string) bool { return candidate == id })
}

// deleteWhere removes the matching entries and returns the position the first
// of them held, or -1 when nothing matched.
func (o *orderedCollection[T]) deleteWhere(match func(T) bool) int {
	first := -1
	kept := make([]string, 0, len(o.ids))
	for i, id := range o.ids {
		if match(o.entries[id]) {
			if first < 0 {
				first = i
			}
			delete(o.entries, id)
			continue
		}
		kept = append(kept, id)
	}
	o.ids = kept
	return first
}

// insertAt adds values at the given position, appending when the position is
// outside the collection. A value whose id is already held keeps its place.
func (o *orderedCollection[T]) insertAt(index int, values []T, key func(T) string) {
	if o.entries == nil {
		o.entries = make(map[string]T)
	}
	fresh := make([]string, 0, len(values))
	for _, value := range values {
		id := key(value)
		if _, exists := o.entries[id]; !exists {
			fresh = append(fresh, id)
		}
		o.entries[id] = value
	}
	if index < 0 || index > len(o.ids) {
		index = len(o.ids)
	}
	o.ids = slices.Insert(o.ids, index, fresh...)
}

// reset replaces every entry with the given values, in their order.
func (o *orderedCollection[T]) reset(values []T, key func(T) string) {
	o.ids = make([]string, 0, len(values))
	o.entries = make(map[string]T, len(values))
	for _, value := range values {
		o.set(key(value), value)
	}
}

func (o *orderedCollection[T]) update(rewrite func(id string, value T) T) {
	for _, id := range o.ids {
		o.entries[id] = rewrite(id, o.entries[id])
	}
}
