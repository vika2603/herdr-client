package herdr

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPopupSizeMarshal(t *testing.T) {
	cases := []struct {
		size PopupSize
		want string
	}{
		{size: PopupSize{Cells: 20}, want: "20"},
		{size: PopupSize{}, want: "0"},
		{size: PopupSize{Cells: 65535}, want: "65535"},
		{size: PopupSize{Percent: 80}, want: `"80%"`},
		{size: PopupSize{Percent: 100}, want: `"100%"`},
	}
	for _, c := range cases {
		encoded, err := json.Marshal(c.size)
		if err != nil {
			t.Errorf("Marshal(%+v): %v", c.size, err)
			continue
		}
		if string(encoded) != c.want {
			t.Errorf("Marshal(%+v) = %s, want %s", c.size, encoded, c.want)
		}
		var decoded PopupSize
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			t.Errorf("Unmarshal(%s): %v", encoded, err)
			continue
		}
		if decoded != c.size {
			t.Errorf("round trip of %+v gave %+v", c.size, decoded)
		}
	}
}

func TestPopupSizeUnmarshalRejectsBadValues(t *testing.T) {
	cases := []string{`"80"`, `"0%"`, `"101%"`, `"eighty%"`, `65536`, `-1`, `true`, `{}`, ``}
	for _, source := range cases {
		var size PopupSize
		if err := json.Unmarshal([]byte(source), &size); err == nil {
			t.Errorf("Unmarshal(%q) was accepted as %+v", source, size)
		}
	}
}

func TestPopupSizeStringAndIsPercent(t *testing.T) {
	if got := (PopupSize{Cells: 20}).String(); got != "20" {
		t.Errorf("String() = %q, want 20", got)
	}
	if got := (PopupSize{Percent: 80}).String(); got != "80%" {
		t.Errorf("String() = %q, want 80%%", got)
	}
	if (PopupSize{Cells: 20}).IsPercent() {
		t.Error("a cell count reports itself as a percentage")
	}
	if !(PopupSize{Percent: 1}).IsPercent() {
		t.Error("a percentage does not report itself as one")
	}
}

func TestAgentViewValueRoundTrip(t *testing.T) {
	cases := []struct {
		value AgentViewValue
		want  string
	}{
		{value: AgentViewText("working"), want: `"working"`},
		{value: AgentViewBool(true), want: `true`},
		{value: AgentViewUint(42), want: `42`},
		{value: AgentViewContextValue(AgentViewContextCurrentWorkspaceID), want: `{"context":"current_workspace_id"}`},
	}
	for _, c := range cases {
		encoded, err := json.Marshal(c.value)
		if err != nil {
			t.Errorf("Marshal(%+v): %v", c.value, err)
			continue
		}
		if string(encoded) != c.want {
			t.Errorf("Marshal(%+v) = %s, want %s", c.value, encoded, c.want)
		}
		var decoded AgentViewValue
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			t.Errorf("Unmarshal(%s): %v", encoded, err)
			continue
		}
		reEncoded, err := json.Marshal(decoded)
		if err != nil {
			t.Errorf("Marshal after Unmarshal: %v", err)
			continue
		}
		if string(reEncoded) != c.want {
			t.Errorf("round trip of %s gave %s", c.want, reEncoded)
		}
	}
}

func TestAgentViewValueRejectsAmbiguousAndEmpty(t *testing.T) {
	text := "a"
	value := true
	if _, err := json.Marshal(AgentViewValue{Text: &text, Bool: &value}); err == nil {
		t.Error("a value with two fields set was accepted")
	}
	if _, err := json.Marshal(AgentViewValue{}); err == nil {
		t.Error("an empty value was accepted")
	}
	var decoded AgentViewValue
	if err := json.Unmarshal([]byte(`{"other":1}`), &decoded); err == nil ||
		!strings.Contains(err.Error(), "context") {
		t.Errorf("error = %v, want it to name the missing context field", err)
	}
	if err := json.Unmarshal([]byte(`[]`), &decoded); err == nil {
		t.Error("a list was accepted as an agent view value")
	}
}

func TestAgentViewFieldRoundTrip(t *testing.T) {
	cases := []struct {
		field AgentViewField
		want  string
	}{
		{field: AgentViewFieldOf(AgentViewBuiltinFieldStatus), want: `"status"`},
		{field: AgentViewFieldOf(AgentViewBuiltinFieldStateChangeSeq), want: `"state_change_seq"`},
		{field: AgentViewFieldToken("weight"), want: `{"token":"weight"}`},
	}
	for _, c := range cases {
		encoded, err := json.Marshal(c.field)
		if err != nil {
			t.Errorf("Marshal(%+v): %v", c.field, err)
			continue
		}
		if string(encoded) != c.want {
			t.Errorf("Marshal(%+v) = %s, want %s", c.field, encoded, c.want)
		}
		var decoded AgentViewField
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			t.Errorf("Unmarshal(%s): %v", encoded, err)
			continue
		}
		if decoded != c.field {
			t.Errorf("round trip of %+v gave %+v", c.field, decoded)
		}
	}
	if _, err := json.Marshal(AgentViewField{}); err == nil {
		t.Error("an empty field was accepted")
	}
	var decoded AgentViewField
	if err := json.Unmarshal([]byte(`{"name":"weight"}`), &decoded); err == nil ||
		!strings.Contains(err.Error(), "token") {
		t.Errorf("error = %v, want it to name the missing token field", err)
	}
	if err := json.Unmarshal([]byte(`7`), &decoded); err == nil {
		t.Error("a number was accepted as an agent view field")
	}
}

func TestAgentViewSortFieldRoundTrip(t *testing.T) {
	cases := []struct {
		field AgentViewSortField
		want  string
	}{
		{field: AgentViewSortFieldOf(AgentViewBuiltinSortFieldAttention), want: `"attention"`},
		{field: AgentViewSortFieldToken("weight"), want: `{"token":"weight"}`},
	}
	for _, c := range cases {
		encoded, err := json.Marshal(c.field)
		if err != nil {
			t.Errorf("Marshal(%+v): %v", c.field, err)
			continue
		}
		if string(encoded) != c.want {
			t.Errorf("Marshal(%+v) = %s, want %s", c.field, encoded, c.want)
		}
		var decoded AgentViewSortField
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			t.Errorf("Unmarshal(%s): %v", encoded, err)
			continue
		}
		if decoded != c.field {
			t.Errorf("round trip of %+v gave %+v", c.field, decoded)
		}
	}
	if _, err := json.Marshal(AgentViewSortField{}); err == nil {
		t.Error("an empty sort field was accepted")
	}
}

// TestPopupSizeInParams covers the manual types where the generator
// references them.
func TestPopupSizeInParams(t *testing.T) {
	params := PluginPaneOpenParams{
		PluginID:   "demo",
		Entrypoint: "main",
		Width:      Some(PopupSize{Percent: 80}),
		Height:     Some(PopupSize{Cells: 20}),
	}
	encoded, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(encoded), `"width":"80%"`) || !strings.Contains(string(encoded), `"height":20`) {
		t.Fatalf("encoded form = %s", encoded)
	}
	var decoded PluginPaneOpenParams
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if decoded.Width.ValueOrZero() != (PopupSize{Percent: 80}) {
		t.Errorf("width = %+v", decoded.Width)
	}
	if decoded.Height.ValueOrZero() != (PopupSize{Cells: 20}) {
		t.Errorf("height = %+v", decoded.Height)
	}
}
