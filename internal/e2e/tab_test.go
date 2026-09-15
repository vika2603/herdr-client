//go:build e2e

package e2e

import (
	"testing"

	"github.com/vika2603/herdr-client/herdr"
)

// stageTab works on a tab of its own so closing it leaves the workspace the
// pane stages need intact.
func stageTab(t *testing.T, h *harness, st *state) {
	created, err := h.client.TabCreate(h.ctx(t), herdr.TabCreateParams{
		WorkspaceID: herdr.Some(st.workspaceID),
		Label:       herdr.Some("e2e-tab"),
		Cwd:         herdr.Some(h.root),
		Focus:       herdr.Some(false),
	})
	if !h.cover(t, herdr.MethodTabCreate, created, err) {
		t.Fatal("no tab to continue with")
	}
	tabID := created.Tab.TabID
	if tabID == "" || created.RootPane.PaneID == "" {
		t.Fatalf("tab.create returned an incomplete tab: %+v", created)
	}

	list, err := h.client.TabList(h.ctx(t), herdr.TabListParams{WorkspaceID: herdr.Some(st.workspaceID)})
	if h.cover(t, herdr.MethodTabList, list, err) {
		seen := false
		for _, tab := range list.Tabs {
			seen = seen || tab.TabID == tabID
		}
		if !seen {
			t.Errorf("tab.list is missing %s: %+v", tabID, list.Tabs)
		}
	}

	info, err := h.client.TabGet(h.ctx(t), herdr.TabTarget{TabID: tabID})
	if h.cover(t, herdr.MethodTabGet, info, err) && info.Tab.TabID != tabID {
		t.Errorf("tab.get returned %s, asked for %s", info.Tab.TabID, tabID)
	}

	focused, err := h.client.TabFocus(h.ctx(t), herdr.TabTarget{TabID: tabID})
	if h.cover(t, herdr.MethodTabFocus, focused, err) && !focused.Tab.Focused {
		t.Errorf("tab.focus left %s unfocused", tabID)
	}

	renamed, err := h.client.TabRename(h.ctx(t), herdr.TabRenameParams{TabID: tabID, Label: "e2e-renamed"})
	if h.cover(t, herdr.MethodTabRename, renamed, err) && renamed.Tab.Label != "e2e-renamed" {
		t.Errorf("tab.rename reported label %q", renamed.Tab.Label)
	}

	moved, err := h.client.TabMove(h.ctx(t), herdr.TabMoveParams{TabID: tabID, InsertIndex: 0})
	if h.cover(t, herdr.MethodTabMove, moved, err) && len(moved.Tabs) < 2 {
		t.Errorf("tab.move returned %d tabs", len(moved.Tabs))
	}

	closed, err := h.client.TabClose(h.ctx(t), herdr.TabTarget{TabID: tabID})
	h.cover(t, herdr.MethodTabClose, closed, err)

	if _, err := h.client.TabFocus(h.ctx(t), herdr.TabTarget{TabID: st.tabID}); err != nil {
		t.Fatalf("refocus %s: %v", st.tabID, err)
	}
}
