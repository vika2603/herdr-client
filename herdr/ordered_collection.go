package herdr

import "slices"

// orderedCollection holds entries by id in the order they were reported.
type orderedCollection[T any] struct {
	ids     []string
	entries map[string]T
}

func (o *orderedCollection[T]) get(id string) (T, bool) {
	value, ok := o.entries[id]
	return value, ok
}

func (o *orderedCollection[T]) values() []T {
	values := make([]T, 0, len(o.ids))
	for _, id := range o.ids {
		values = append(values, o.entries[id])
	}
	return values
}

func (o *orderedCollection[T]) set(id string, value T) {
	if o.entries == nil {
		o.entries = make(map[string]T)
	}
	if _, exists := o.entries[id]; !exists {
		o.ids = append(o.ids, id)
	}
	o.entries[id] = value
}

func (o *orderedCollection[T]) delete(id string) {
	if _, exists := o.entries[id]; !exists {
		return
	}
	delete(o.entries, id)
	o.ids = slices.DeleteFunc(o.ids, func(candidate string) bool { return candidate == id })
}

// deleteWhere removes the matching entries and returns the position the first
// of them held, or -1 when nothing matched.
func (o *orderedCollection[T]) deleteWhere(match func(T) bool) int {
	first := -1
	kept := make([]string, 0, len(o.ids))
	for i, id := range o.ids {
		if match(o.entries[id]) {
			if first < 0 {
				first = i
			}
			delete(o.entries, id)
			continue
		}
		kept = append(kept, id)
	}
	o.ids = kept
	return first
}

// insertAt adds values at the given position, appending when the position is
// outside the collection. A value whose id is already held keeps its place.
func (o *orderedCollection[T]) insertAt(index int, values []T, key func(T) string, clone func(T) T) {
	if o.entries == nil {
		o.entries = make(map[string]T)
	}
	fresh := make([]string, 0, len(values))
	for _, value := range values {
		id := key(value)
		if _, exists := o.entries[id]; !exists {
			fresh = append(fresh, id)
		}
		o.entries[id] = clone(value)
	}
	if index < 0 || index > len(o.ids) {
		index = len(o.ids)
	}
	o.ids = slices.Insert(o.ids, index, fresh...)
}

// reset replaces every entry with the given values, in their order.
func (o *orderedCollection[T]) reset(values []T, key func(T) string, clone func(T) T) {
	o.ids = make([]string, 0, len(values))
	o.entries = make(map[string]T, len(values))
	for _, value := range values {
		o.set(key(value), clone(value))
	}
}

func (o *orderedCollection[T]) update(rewrite func(id string, value T) T) {
	for _, id := range o.ids {
		o.entries[id] = rewrite(id, o.entries[id])
	}
}
