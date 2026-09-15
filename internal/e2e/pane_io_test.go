//go:build e2e

package e2e

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/vika2603/herdr-client/herdr"
)

const (
	markerSendText  = "E2E-SEND-TEXT"
	markerSendInput = "E2E-SEND-INPUT"
	markerShell     = "E2E-SHELL-READY"
)

// stagePaneIO drives the terminal of the workspace root pane: it writes two
// commands, waits for the second to appear and then reads, so the read and
// the copy mode methods work on known content.
func stagePaneIO(t *testing.T, h *harness, st *state) {
	waitPaneShell(t, h, st.paneID)

	sent, err := h.client.PaneSendText(h.ctx(t), herdr.PaneSendTextParams{
		PaneID: st.paneID,
		Text:   "echo " + markerSendText + "\n",
	})
	h.cover(t, herdr.MethodPaneSendText, sent, err)

	keys, err := h.client.PaneSendKeys(h.ctx(t), herdr.PaneSendKeysParams{
		PaneID: st.paneID,
		Keys:   []string{"Enter"},
	})
	h.cover(t, herdr.MethodPaneSendKeys, keys, err)

	input, err := h.client.PaneSendInput(h.ctx(t), herdr.PaneSendInputParams{
		PaneID: st.paneID,
		Text:   herdr.Some("echo " + markerSendInput + "\n"),
	})
	h.cover(t, herdr.MethodPaneSendInput, input, err)

	matched, err := h.client.PaneWaitForOutput(h.ctx(t), herdr.PaneWaitForOutputParams{
		PaneID:    st.paneID,
		Source:    herdr.ReadSourceRecent,
		Match:     herdr.OutputMatchSubstring{Value: markerSendInput},
		TimeoutMs: herdr.Some(uint64(15000)),
	})
	if h.cover(t, herdr.MethodPaneWaitForOutput, matched, err) {
		if line, ok := matched.MatchedLine.Get(); !ok || !strings.Contains(line, markerSendInput) {
			t.Errorf("pane.wait_for_output matched %q, expected a line holding %s", line, markerSendInput)
		}
	}

	read, err := h.client.PaneRead(h.ctx(t), herdr.PaneReadParams{
		PaneID: st.paneID,
		Source: herdr.ReadSourceVisible,
		Format: herdr.Some(herdr.ReadFormatText),
	})
	if h.cover(t, herdr.MethodPaneRead, read, err) {
		for _, marker := range []string{markerSendText, markerSendInput} {
			if !strings.Contains(read.Read.Text, marker) {
				t.Errorf("pane.read did not return %s:\n%s", marker, read.Read.Text)
			}
		}
	}

	selection, err := h.client.PaneSelectionRead(h.ctx(t), herdr.PaneSelectionReadParams{
		PaneID: st.paneID,
		Anchor: herdr.PaneTextPoint{Row: 0, Col: 0},
		Cursor: herdr.PaneTextPoint{Row: 0, Col: 8},
	})
	if h.cover(t, herdr.MethodPaneSelectionRead, selection, err) && selection.PaneID != st.paneID {
		t.Errorf("pane.selection.read returned %s, asked for %s", selection.PaneID, st.paneID)
	}

	// pane.copy_search validates the revision it is given, so the revision
	// pane.copy_motion reports is used right away.
	motion, err := h.client.PaneCopyMotion(h.ctx(t), herdr.PaneCopyMotionParams{
		PaneID: st.paneID,
		Cursor: herdr.PaneTextPoint{Row: 0, Col: 0},
		Motion: herdr.PaneCopyMotionLineEnd,
	})
	if !h.cover(t, herdr.MethodPaneCopyMotion, motion, err) {
		return
	}
	search, err := h.client.PaneCopySearch(h.ctx(t), herdr.PaneCopySearchParams{
		PaneID:          st.paneID,
		ContentRevision: motion.ContentRevision,
		Cursor:          herdr.PaneTextPoint{Row: 0, Col: 0},
		Direction:       herdr.PaneCopySearchDirectionForward,
		Query:           markerSendInput,
	})
	if h.cover(t, herdr.MethodPaneCopySearch, search, err) && search.Total == 0 {
		t.Errorf("pane.copy_search found no %s although pane.read returned it", markerSendInput)
	}

	// A one pixel RGBA layer is enough to reach the graphics store; drawing
	// it needs an attached client, which is why pane.graphics.info stays out
	// of reach.
	graphics, err := h.client.PaneGraphicsSet(h.ctx(t), herdr.PaneGraphicsSetParams{
		PaneID:      st.paneID,
		Format:      herdr.PaneGraphicsFormatRgba,
		ImageWidth:  1,
		ImageHeight: 1,
		DataBase64:  herdr.Some(base64.StdEncoding.EncodeToString([]byte{0, 0, 0, 0})),
		LayerID:     herdr.Some("e2e"),
	})
	h.cover(t, herdr.MethodPaneGraphicsSet, graphics, err)

	cleared, err := h.client.PaneGraphicsClear(h.ctx(t), herdr.PaneGraphicsClearParams{
		PaneID:  st.paneID,
		LayerID: herdr.Some("e2e"),
	})
	h.cover(t, herdr.MethodPaneGraphicsClear, cleared, err)

	stageEditScrollback(t, h, st)
}

// stageEditScrollback opens the scrollback of the root pane in a pane of its
// own and closes that pane again, so the later stages see the layout the pane
// stage left behind.
func stageEditScrollback(t *testing.T, h *harness, st *state) {
	before := panesOf(t, h, st.workspaceID)

	edit, err := h.client.PaneEditScrollback(h.ctx(t), herdr.PaneTarget{PaneID: st.paneID})
	if !h.cover(t, herdr.MethodPaneEditScrollback, edit, err) {
		return
	}

	for paneID := range panesOf(t, h, st.workspaceID) {
		if before[paneID] {
			continue
		}
		// The editor pane closes itself when its editor exits, which can
		// happen before this call arrives. The stage needs the layout
		// restored, not this particular close to succeed.
		_, err := h.client.PaneClose(h.ctx(t), herdr.PaneTarget{PaneID: paneID})
		if err != nil && !herdr.IsCode(err, herdr.ErrCodePaneNotFound) {
			t.Errorf("close the editor pane %s: %v", paneID, err)
		}
	}
}

// waitPaneShell blocks until the shell of the pane runs what is written to
// it. A pane answers pane.get as soon as it exists, but text written before
// its shell reads from the terminal is lost, so the marker is repeated until
// it comes back.
func waitPaneShell(t *testing.T, h *harness, paneID string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		if _, err := h.client.PaneSendText(h.ctx(t), herdr.PaneSendTextParams{
			PaneID: paneID,
			Text:   "echo " + markerShell + "\n",
		}); err != nil {
			t.Fatalf("pane.send_text: %v", err)
		}
		_, err := h.client.PaneWaitForOutput(h.ctx(t), herdr.PaneWaitForOutputParams{
			PaneID:    paneID,
			Source:    herdr.ReadSourceRecent,
			Match:     herdr.OutputMatchSubstring{Value: markerShell},
			TimeoutMs: herdr.Some(uint64(1000)),
		})
		if err == nil {
			return
		}
		if !herdr.IsCode(err, herdr.ErrCodeTimeout) {
			t.Fatalf("pane.wait_for_output: %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("the shell of pane %s did not run a command within 15s\nprocess: %s\nvisible: %s",
				paneID, describePaneProcess(t, h, paneID), describePaneText(t, h, paneID))
		}
	}
}

func describePaneProcess(t *testing.T, h *harness, paneID string) string {
	t.Helper()
	info, err := h.client.PaneProcessInfo(h.ctx(t), herdr.PaneProcessInfoParams{PaneID: herdr.Some(paneID)})
	if err != nil {
		return "pane.process_info: " + err.Error()
	}
	return marshal(info.ProcessInfo)
}

func describePaneText(t *testing.T, h *harness, paneID string) string {
	t.Helper()
	read, err := h.client.PaneRead(h.ctx(t), herdr.PaneReadParams{
		PaneID: paneID,
		Source: herdr.ReadSourceVisible,
		Format: herdr.Some(herdr.ReadFormatText),
	})
	if err != nil {
		return "pane.read: " + err.Error()
	}
	return marshal(read.Read.Text)
}

func panesOf(t *testing.T, h *harness, workspaceID string) map[string]bool {
	t.Helper()
	list, err := h.client.PaneList(h.ctx(t), herdr.PaneListParams{WorkspaceID: herdr.Some(workspaceID)})
	if err != nil {
		t.Fatalf("pane.list: %v", err)
	}
	panes := make(map[string]bool, len(list.Panes))
	for _, pane := range list.Panes {
		panes[pane.PaneID] = true
	}
	return panes
}
