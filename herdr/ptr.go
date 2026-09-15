package herdr

// Ptr returns a pointer to v. Use it for nullable map values or genuinely
// pointer-shaped data; optional protocol object fields use Some instead.
func Ptr[T any](v T) *T { return &v }

// Value returns the value p points at, or T's zero value when p is nil.
// For Optional values use Get or ValueOrZero.
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
