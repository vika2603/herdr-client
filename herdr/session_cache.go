package herdr

import "maps"

// sessionCache is the mirrored state. Every collection keeps the order the
// server reported. It has no I/O or locking; sessionState synchronizes access.
// Snapshot and event inputs are borrowed: all retained references are copied.
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
	cache.workspaces.reset(snapshot.Workspaces, workspaceKey, cloneWorkspace)
	cache.tabs.reset(snapshot.Tabs, tabKey, cloneTab)
	cache.panes.reset(snapshot.Panes, paneKey, clonePane)
	cache.agents.reset(snapshot.Agents, agentKey, cloneAgent)
	cache.layouts.reset(snapshot.Layouts, layoutKey, cloneLayout)
	return cache
}

// apply folds one event into the mirror. Applying the same event twice leaves
// the same state, which is what lets a bootstrap replay events the snapshot
// already reflects.
func (c *sessionCache) apply(event Event) {
	switch e := event.(type) {
	case *WorkspaceCreatedEvent:
		c.workspaces.set(e.Workspace.WorkspaceID, cloneWorkspace(e.Workspace))
	case *WorkspaceUpdatedEvent:
		c.workspaces.set(e.Workspace.WorkspaceID, cloneWorkspace(e.Workspace))
	case *WorkspaceMetadataUpdatedEvent:
		c.workspaces.set(e.Workspace.WorkspaceID, cloneWorkspace(e.Workspace))
	case *WorkspaceRenamedEvent:
		if workspace, ok := c.workspaces.get(e.WorkspaceID); ok {
			workspace.Label = e.Label
			c.workspaces.set(e.WorkspaceID, workspace)
		}
	case *WorkspaceMovedEvent:
		c.workspaces.reset(e.Workspaces, workspaceKey, cloneWorkspace)
	case *WorkspaceReorderedEvent:
		c.workspaces.reset(e.Workspaces, workspaceKey, cloneWorkspace)
	case *WorkspaceFocusedEvent:
		c.focusWorkspace(e.WorkspaceID)
	case *WorkspaceClosedEvent:
		c.closeWorkspace(e.WorkspaceID)
	case *WorktreeCreatedEvent:
		c.workspaces.set(e.Workspace.WorkspaceID, cloneWorkspace(e.Workspace))
	case *WorktreeOpenedEvent:
		c.workspaces.set(e.Workspace.WorkspaceID, cloneWorkspace(e.Workspace))
	case *WorktreeRemovedEvent:
		if e.Workspace != nil {
			c.workspaces.set(e.Workspace.WorkspaceID, cloneWorkspace(*e.Workspace))
		}
	case *TabCreatedEvent:
		c.tabs.set(e.Tab.TabID, cloneTab(e.Tab))
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
		c.layouts.set(e.Layout.TabID, cloneLayout(e.Layout))
	}
	// pane.exited leaves the pane in place: the process ended, and a pane
	// that goes away with it is reported by pane.closed. pane.output_matched
	// and an unknown event carry nothing the mirror holds.
}

// setPane installs a pane and refreshes the agent entry that pane carries,
// which repeats the fields AgentInfo shares with PaneInfo.
func (c *sessionCache) setPane(pane PaneInfo) {
	pane = clonePane(pane)
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
		c.workspaces.set(e.CreatedWorkspace.WorkspaceID, cloneWorkspace(*e.CreatedWorkspace))
	}
	if e.CreatedTab != nil {
		c.tabs.set(e.CreatedTab.TabID, cloneTab(*e.CreatedTab))
	}
	agent, hadAgent := c.agents.get(e.PreviousPaneID)
	c.closePane(e.PreviousPaneID)
	pane := clonePane(e.Pane)
	c.panes.set(pane.PaneID, pane)
	if hadAgent {
		c.agents.set(pane.PaneID, mergeAgent(agent, pane))
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
	c.tabs.insertAt(at, tabs, tabKey, cloneTab)
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
			pane.Agent = clonePtr(e.Agent)
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
			pane.Agent = clonePtr(e.Agent)
		}
		if e.DisplayAgent != nil {
			pane.DisplayAgent = clonePtr(e.DisplayAgent)
		}
		if e.Title != nil {
			pane.Title = clonePtr(e.Title)
		}
		if e.StateLabels != nil {
			pane.StateLabels = maps.Clone(e.StateLabels)
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
