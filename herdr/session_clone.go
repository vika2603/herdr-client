package herdr

// cloneEach detaches references in an already owned slice, such as the fresh
// projection returned by orderedCollection.values.
func cloneEach[T any](values []T, clone func(T) T) []T {
	for i, value := range values {
		values[i] = clone(value)
	}
	return values
}
