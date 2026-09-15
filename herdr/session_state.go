package herdr

import "sync"

// sessionState owns synchronization and the current cache independently of
// connection/reconnect state. The cache is never exposed outside this owner.
// Each successful bootstrap supplies a fully owned replacement cache.
type sessionState struct {
	mu    sync.RWMutex
	cache *sessionCache
}

func (s *sessionState) replace(cache *sessionCache) {
	s.mu.Lock()
	s.cache = cache
	s.mu.Unlock()
}

func (s *sessionState) apply(event Event) {
	s.mu.Lock()
	s.cache.apply(event)
	s.mu.Unlock()
}

// Workspaces returns the mirrored workspaces in the order the server reports
// them.
func (s *Session) Workspaces() []WorkspaceInfo {
	s.state.mu.RLock()
	defer s.state.mu.RUnlock()
	return cloneEach(s.state.cache.workspaces.values(), cloneWorkspace)
}

// Tabs returns the mirrored tabs in the order the server reports them.
func (s *Session) Tabs() []TabInfo {
	s.state.mu.RLock()
	defer s.state.mu.RUnlock()
	return cloneEach(s.state.cache.tabs.values(), cloneTab)
}

// Pane returns the mirrored pane with the given id.
func (s *Session) Pane(paneID string) (PaneInfo, bool) {
	s.state.mu.RLock()
	defer s.state.mu.RUnlock()
	pane, ok := s.state.cache.panes.get(paneID)
	if !ok {
		return PaneInfo{}, false
	}
	return clonePane(pane), true
}

// Agents returns the mirrored agents in the order the server reports them.
func (s *Session) Agents() []AgentInfo {
	s.state.mu.RLock()
	defer s.state.mu.RUnlock()
	return cloneEach(s.state.cache.agents.values(), cloneAgent)
}

// Layout returns the mirrored layout of the given tab.
func (s *Session) Layout(tabID string) (PaneLayoutSnapshot, bool) {
	s.state.mu.RLock()
	defer s.state.mu.RUnlock()
	layout, ok := s.state.cache.layouts.get(tabID)
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
	s.state.mu.RLock()
	defer s.state.mu.RUnlock()

	workspaces := cloneEach(s.state.cache.workspaces.values(), cloneWorkspace)
	tabs := cloneEach(s.state.cache.tabs.values(), cloneTab)
	panes := cloneEach(s.state.cache.panes.values(), clonePane)
	return SessionSnapshot{
		Version:            s.state.cache.version,
		Protocol:           s.state.cache.protocol,
		Workspaces:         workspaces,
		Tabs:               tabs,
		Panes:              panes,
		Agents:             cloneEach(s.state.cache.agents.values(), cloneAgent),
		Layouts:            cloneEach(s.state.cache.layouts.values(), cloneLayout),
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
