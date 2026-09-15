package herdr

import "testing"

func TestLayoutPanesWalksLeftToRight(t *testing.T) {
	root := LayoutNodeSplit{
		Direction: SplitDirectionRight,
		Ratio:     0.5,
		First: LayoutNodeSplit{
			Direction: SplitDirectionDown,
			Ratio:     0.5,
			First:     LayoutNodePane{Label: Some("agent"), PaneID: Some("w1:p1")},
			Second:    LayoutNodePane{Label: Some("logs"), PaneID: Some("w1:p2")},
		},
		Second: LayoutNodePane{Label: Some("shell"), PaneID: Some("w1:p3")},
	}

	var ids []string
	for _, pane := range LayoutPanes(root) {
		ids = append(ids, pane.PaneID.ValueOrZero())
	}
	want := []string{"w1:p1", "w1:p2", "w1:p3"}
	if len(ids) != len(want) {
		t.Fatalf("panes = %v, want %v", ids, want)
	}
	for i, id := range want {
		if ids[i] != id {
			t.Errorf("pane %d = %q, want %q", i, ids[i], id)
		}
	}
}

// A pointer satisfies LayoutNode, so a tree built by hand with pointers must
// walk the same as the value form decoding produces.
func TestLayoutPanesWalksThePointerForm(t *testing.T) {
	root := &LayoutNodeSplit{
		Direction: SplitDirectionRight,
		Ratio:     0.5,
		First:     &LayoutNodePane{PaneID: Some("w1:p1")},
		Second:    LayoutNodePane{PaneID: Some("w1:p2")},
	}

	panes := LayoutPanes(root)
	if len(panes) != 2 || panes[0].PaneID.ValueOrZero() != "w1:p1" || panes[1].PaneID.ValueOrZero() != "w1:p2" {
		t.Errorf("panes = %+v, want both leaves in order", panes)
	}
}

func TestLayoutPanesOnALeafAndOnNothing(t *testing.T) {
	if got := LayoutPanes(LayoutNodePane{PaneID: Some("w1:p1")}); len(got) != 1 {
		t.Errorf("a single pane yielded %d panes, want 1", len(got))
	}
	if got := LayoutPanes(nil); got != nil {
		t.Errorf("LayoutPanes(nil) = %v, want nil", got)
	}
}
