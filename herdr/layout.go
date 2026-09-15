package herdr

// LayoutPanes returns the pane leaves of a layout tree, left to right, which
// is the order layout.apply reads its splits in.
//
// Pane ids are assigned by the server, so the only way to learn the id of a
// pane an applied layout created is to walk the tree the response carries.
// Labelling the panes in the request and matching the label here identifies
// one without depending on its position:
//
//	applied, err := client.LayoutApply(ctx, params)
//	for _, pane := range herdr.LayoutPanes(applied.Layout.Root) {
//		if pane.Label.ValueOrZero() == "agent" {
//			…
//		}
//	}
//
// Decoding produces the value form of each variant, but a pointer satisfies
// LayoutNode too, so a tree built by hand with &LayoutNodePane{} is walked
// the same way rather than coming back empty. A nil root contributes no
// panes; a variant this package cannot decode never reaches here, because
// decoding the layout fails first.
func LayoutPanes(root LayoutNode) []LayoutNodePane {
	return appendLayoutPanes(nil, root)
}

func appendLayoutPanes(panes []LayoutNodePane, node LayoutNode) []LayoutNodePane {
	switch node := node.(type) {
	case LayoutNodePane:
		return append(panes, node)
	case *LayoutNodePane:
		return append(panes, *node)
	case LayoutNodeSplit:
		return appendLayoutPanes(appendLayoutPanes(panes, node.First), node.Second)
	case *LayoutNodeSplit:
		return appendLayoutPanes(appendLayoutPanes(panes, node.First), node.Second)
	default:
		return panes
	}
}
