package herdr

import (
	"bytes"
	"encoding/json"
)

// Optional represents an absent field, an explicit JSON null, or a value.
// Its zero value is absent. Use json:",omitzero" on optional struct fields;
// omission belongs to the enclosing object, not MarshalJSON itself.
// Null should only be used where the protocol permits it.
type Optional[T any] struct {
	value T
	state optionalState
}

type optionalState uint8

const (
	optionalAbsent optionalState = iota
	optionalNull
	optionalValue
)

// Some returns a present value, including false, zero and empty collections.
// If v itself encodes as JSON null (for example a nil slice), its JSON is
// indistinguishable from Null; decoding that JSON produces the null state.
func Some[T any](v T) Optional[T] { return Optional[T]{value: v, state: optionalValue} }

// Null returns an explicitly null field, distinct from the absent zero value.
func Null[T any]() Optional[T] { return Optional[T]{state: optionalNull} }

// Get returns the value and true only in the present-value state.
func (o Optional[T]) Get() (T, bool) { return o.value, o.state == optionalValue }

// ValueOrZero returns the value or T's zero value when absent or null.
func (o Optional[T]) ValueOrZero() T { return o.value }

// IsSet reports whether the field is present, including explicit null.
func (o Optional[T]) IsSet() bool { return o.state != optionalAbsent }

// IsNull reports whether the field is explicitly null.
func (o Optional[T]) IsNull() bool { return o.state == optionalNull }

// IsZero lets encoding/json omit absent fields tagged with omitzero.
func (o Optional[T]) IsZero() bool { return o.state == optionalAbsent }

// MarshalJSON writes the wrapped value. Absent values encode as null when
// marshaled outside a struct field with omitzero, since JSON has no absent value.
func (o Optional[T]) MarshalJSON() ([]byte, error) {
	if o.state != optionalValue {
		return []byte("null"), nil
	}
	return json.Marshal(o.value)
}

// UnmarshalJSON decodes a present field atomically. Missing object fields do
// not invoke this method, so decoding into reused structs follows the usual
// encoding/json merge semantics. Use a fresh struct for independent messages.
func (o *Optional[T]) UnmarshalJSON(data []byte) error {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		*o = Null[T]()
		return nil
	}
	var value T
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*o = Some(value)
	return nil
}
