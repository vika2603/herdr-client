//go:build e2e

package e2e

import (
	"slices"
	"testing"

	"github.com/vika2603/herdr-client/herdr"
)

// stageTeardown withdraws what the agent stage reported, moves and closes the
// pane the pane stage split off, and stops the server. It runs last: after
// server.stop the socket is gone.
func stageTeardown(t *testing.T, h *harness, st *state) {
	authority, err := h.client.PaneClearAgentAuthority(h.ctx(t), herdr.PaneClearAgentAuthorityParams{
		PaneID: st.paneID,
		Source: herdr.Some(reportSource),
	})
	h.cover(t, herdr.MethodPaneClearAgentAuthority, authority, err)

	released, err := h.client.PaneReleaseAgent(h.ctx(t), herdr.PaneReleaseAgentParams{
		PaneID: st.paneID,
		Source: reportSource,
		Agent:  reportedAgent,
	})
	h.cover(t, herdr.MethodPaneReleaseAgent, released, err)

	moved, err := h.client.PaneMove(h.ctx(t), herdr.PaneMoveParams{
		PaneID:      st.splitPaneID,
		Destination: herdr.PaneMoveDestinationNewTab{Label: herdr.Some("e2e-moved")},
		Focus:       herdr.Some(false),
	})
	if h.cover(t, herdr.MethodPaneMove, moved, err) {
		if !moved.MoveResult.Changed {
			t.Errorf("pane.move changed nothing: %+v", moved.MoveResult)
		}
		if _, ok := moved.MoveResult.CreatedTab.Get(); !ok {
			t.Errorf("pane.move to a new tab created none: %+v", moved.MoveResult)
		}
	}

	closed, err := h.client.PaneClose(h.ctx(t), herdr.PaneTarget{PaneID: st.splitPaneID})
	h.cover(t, herdr.MethodPaneClose, closed, err)

	stopped, err := h.client.ServerStop(h.ctx(t))
	h.cover(t, herdr.MethodServerStop, stopped, err)
}

// stageCoverage checks the report the suite is about to print: the schema, the
// result table and the out of reach list have to agree with what ran.
func stageCoverage(t *testing.T, h *harness, _ *state) {
	// The suite linked a plugin, so the last thing it checks is that the
	// registry it must not reach came through the run untouched.
	h.assertCallerRegistryUnchanged(t)

	for _, problem := range problems(methods, h.rec.covered()) {
		t.Errorf("%s", problem)
	}
	for _, method := range methods {
		if _, ok := table[method]; !ok {
			t.Errorf("%s: declared by the schema but missing from %s", method, methodsPath)
		}
	}
	for method := range table {
		if !slices.Contains(methods, method) {
			t.Errorf("%s: listed in %s but no longer declared by the schema", method, methodsPath)
		}
	}
}
