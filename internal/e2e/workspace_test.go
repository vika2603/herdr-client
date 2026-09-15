//go:build e2e

package e2e

import (
	"testing"

	"github.com/vika2603/herdr-client/herdr"
)

// stageWorkspace creates the workspace the later stages work in and calls
// every workspace method on it. The second workspace exists because move and
// move_block need something to reorder, and because workspace.close must not
// be the last workspace of the session.
func stageWorkspace(t *testing.T, h *harness, st *state) {
	created, err := h.client.WorkspaceCreate(h.ctx(t), herdr.WorkspaceCreateParams{
		Label: herdr.Some("e2e-main"),
		Cwd:   herdr.Some(h.root),
		Focus: herdr.Some(true),
	})
	if !h.cover(t, herdr.MethodWorkspaceCreate, created, err) {
		t.Fatal("no workspace to continue with")
	}
	st.workspaceID = created.Workspace.WorkspaceID
	st.tabID = created.Tab.TabID
	st.paneID = created.RootPane.PaneID
	if st.workspaceID == "" || st.tabID == "" || st.paneID == "" {
		t.Fatalf("workspace.create returned an incomplete workspace: %+v", created)
	}

	second, err := h.client.WorkspaceCreate(h.ctx(t), herdr.WorkspaceCreateParams{
		Label: herdr.Some("e2e-second"),
		Cwd:   herdr.Some(h.root),
		Focus: herdr.Some(false),
	})
	if err != nil {
		t.Fatalf("second workspace: %v", err)
	}
	secondID := second.Workspace.WorkspaceID

	list, err := h.client.WorkspaceList(h.ctx(t))
	if h.cover(t, herdr.MethodWorkspaceList, list, err) {
		seen := make(map[string]bool, len(list.Workspaces))
		for _, workspace := range list.Workspaces {
			seen[workspace.WorkspaceID] = true
		}
		if !seen[st.workspaceID] || !seen[secondID] {
			t.Errorf("workspace.list is missing %s or %s: %+v", st.workspaceID, secondID, list.Workspaces)
		}
	}

	info, err := h.client.WorkspaceGet(h.ctx(t), herdr.WorkspaceTarget{WorkspaceID: st.workspaceID})
	if h.cover(t, herdr.MethodWorkspaceGet, info, err) && info.Workspace.WorkspaceID != st.workspaceID {
		t.Errorf("workspace.get returned %s, asked for %s", info.Workspace.WorkspaceID, st.workspaceID)
	}

	focused, err := h.client.WorkspaceFocus(h.ctx(t), herdr.WorkspaceTarget{WorkspaceID: st.workspaceID})
	if h.cover(t, herdr.MethodWorkspaceFocus, focused, err) && !focused.Workspace.Focused {
		t.Errorf("workspace.focus left %s unfocused", st.workspaceID)
	}

	renamed, err := h.client.WorkspaceRename(h.ctx(t), herdr.WorkspaceRenameParams{
		WorkspaceID: st.workspaceID,
		Label:       "e2e-renamed",
	})
	if h.cover(t, herdr.MethodWorkspaceRename, renamed, err) && renamed.Workspace.Label != "e2e-renamed" {
		t.Errorf("workspace.rename reported label %q", renamed.Workspace.Label)
	}

	moved, err := h.client.WorkspaceMove(h.ctx(t), herdr.WorkspaceMoveParams{
		WorkspaceID: secondID,
		InsertIndex: 0,
	})
	if h.cover(t, herdr.MethodWorkspaceMove, moved, err) && len(moved.Workspaces) < 2 {
		t.Errorf("workspace.move returned %d workspaces", len(moved.Workspaces))
	}

	block, err := h.client.WorkspaceMoveBlock(h.ctx(t), herdr.WorkspaceMoveBlockParams{
		WorkspaceIds: []string{secondID},
	})
	if h.cover(t, herdr.MethodWorkspaceMoveBlock, block, err) && len(block.Workspaces) < 2 {
		t.Errorf("workspace.move_block returned %d workspaces", len(block.Workspaces))
	}

	metadata, err := h.client.WorkspaceReportMetadata(h.ctx(t), herdr.WorkspaceReportMetadataParams{
		WorkspaceID: st.workspaceID,
		Source:      "herdr-client-e2e",
		Tokens:      map[string]*string{"e2e": herdr.Ptr("1")},
	})
	h.cover(t, herdr.MethodWorkspaceReportMetadata, metadata, err)

	closed, err := h.client.WorkspaceClose(h.ctx(t), herdr.WorkspaceCloseParams{WorkspaceID: secondID})
	h.cover(t, herdr.MethodWorkspaceClose, closed, err)
}
