package herdr

// Ptr returns a pointer to v.
//
// Optional request fields that are not strings are pointers, so that leaving
// one unset is distinguishable from sending its zero value. Ptr supplies the
// address inline:
//
//	client.PaneSplit(ctx, herdr.PaneSplitParams{
//		Direction: herdr.SplitDirectionRight,
//		Cwd:       herdr.Ptr("/repo"),
//		Focus:     herdr.Ptr(false),
//	})
func Ptr[T any](v T) *T { return &v }

// Value returns the value p points at, or the zero value of T when p is nil.
// Optional response fields are pointers for the same reason request fields
// are.
func Value[T any](p *T) T {
	if p == nil {
		var zero T
		return zero
	}
	return *p
}

// clonePtr detaches scalar pointers for handwritten protocol adapters.
func clonePtr[T any](value *T) *T {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
