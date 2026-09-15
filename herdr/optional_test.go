package herdr

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestOptionalJSONStates(t *testing.T) {
	type fields struct {
		Focus Optional[bool]              `json:"focus,omitzero"`
		Text  Optional[string]            `json:"text,omitzero"`
		List  Optional[[]string]          `json:"list,omitzero"`
		Map   Optional[map[string]string] `json:"map,omitzero"`
	}
	for _, test := range []struct {
		value fields
		json  string
	}{
		{fields{}, `{}`},
		{fields{Focus: Null[bool]()}, `{"focus":null}`},
		{fields{Focus: Some(false), Text: Some("")}, `{"focus":false,"text":""}`},
		{fields{List: Some([]string{}), Map: Some(map[string]string{})}, `{"list":[],"map":{}}`},
	} {
		encoded, err := json.Marshal(test.value)
		if err != nil || string(encoded) != test.json {
			t.Fatalf("Marshal = %s, %v; want %s", encoded, err, test.json)
		}
		var decoded fields
		if err := json.Unmarshal(encoded, &decoded); err != nil || !reflect.DeepEqual(decoded, test.value) {
			t.Fatalf("round trip = %#v, %v; want %#v", decoded, err, test.value)
		}
	}
	var missing Optional[bool]
	if missing.IsSet() || missing.IsNull() || !missing.IsZero() {
		t.Fatal("zero value is not absent")
	}
	if value, ok := Some(false).Get(); !ok || value {
		t.Fatal("Some(false) lost its presence")
	}
	if value, ok := Null[bool]().Get(); ok || value || !Null[bool]().IsSet() || !Null[bool]().IsNull() {
		t.Fatal("null state is not distinct from a present value")
	}
}

func TestOptionalDecodeIsAtomicAndResetsNull(t *testing.T) {
	value := Some(true)
	if err := json.Unmarshal([]byte(`"invalid"`), &value); err == nil || !value.ValueOrZero() {
		t.Fatal("invalid input replaced the prior value")
	}
	if err := json.Unmarshal([]byte(`null`), &value); err != nil || !value.IsNull() || value.ValueOrZero() {
		t.Fatal("null did not clear the prior value")
	}
	var missing Optional[string]
	if encoded, err := json.Marshal(missing); err != nil || string(encoded) != "null" {
		t.Fatalf("standalone absent = %s, %v", encoded, err)
	}
}
