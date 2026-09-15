//go:build e2e

package e2e

import (
	"testing"

	"github.com/vika2603/herdr-client/herdr"
)

// stageLayout works on the tab the pane stage split in two: its exported
// description must be one split of two panes, which also proves the
// LayoutNode union decodes. layout.apply builds a tab of its own so the
// fixture layout survives.
func stageLayout(t *testing.T, h *harness, st *state) {
	exported, err := h.client.LayoutExport(h.ctx(t), herdr.LayoutExportParams{TabID: herdr.Some(st.tabID)})
	if h.cover(t, herdr.MethodLayoutExport, exported, err) {
		assertSplitOfPanes(t, "layout.export", exported.Layout.Root)
	}

	ratio, err := h.client.LayoutSetSplitRatio(h.ctx(t), herdr.LayoutSetSplitRatioParams{
		TabID: herdr.Some(st.tabID),
		Path:  []bool{},
		Ratio: 0.4,
	})
	if h.cover(t, herdr.MethodLayoutSetSplitRatio, ratio, err) {
		if split, ok := ratio.Layout.Root.(herdr.LayoutNodeSplit); ok && split.Ratio != 0.4 {
			t.Errorf("layout.set_split_ratio reports ratio %v, asked for 0.4", split.Ratio)
		}
	}

	applied, err := h.client.LayoutApply(h.ctx(t), herdr.LayoutApplyParams{
		WorkspaceID: herdr.Some(st.workspaceID),
		TabLabel:    herdr.Some("e2e-applied"),
		Focus:       herdr.Some(false),
		Root: herdr.LayoutNodeSplit{
			Direction: herdr.SplitDirectionDown,
			Ratio:     0.5,
			First:     herdr.LayoutNodePane{Label: herdr.Some("e2e-first"), Cwd: herdr.Some(h.root)},
			Second:    herdr.LayoutNodePane{Label: herdr.Some("e2e-second"), Cwd: herdr.Some(h.root)},
		},
	})
	if !h.cover(t, herdr.MethodLayoutApply, applied, err) {
		return
	}
	assertSplitOfPanes(t, "layout.apply", applied.Layout.Root)
	if applied.Layout.TabID == st.tabID {
		t.Fatalf("layout.apply replaced the fixture tab %s", st.tabID)
	}
	if _, err := h.client.TabClose(h.ctx(t), herdr.TabTarget{TabID: applied.Layout.TabID}); err != nil {
		t.Errorf("close the applied tab %s: %v", applied.Layout.TabID, err)
	}
	if _, err := h.client.TabFocus(h.ctx(t), herdr.TabTarget{TabID: st.tabID}); err != nil {
		t.Fatalf("refocus %s: %v", st.tabID, err)
	}
}

func assertSplitOfPanes(t *testing.T, method string, root herdr.LayoutNode) {
	t.Helper()
	split, ok := root.(herdr.LayoutNodeSplit)
	if !ok {
		t.Errorf("%s returned root %T, expected a split", method, root)
		return
	}
	for name, child := range map[string]herdr.LayoutNode{"first": split.First, "second": split.Second} {
		if _, ok := child.(herdr.LayoutNodePane); !ok {
			t.Errorf("%s returned %s child %T, expected a pane", method, name, child)
		}
	}
}
