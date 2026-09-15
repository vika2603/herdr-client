package herdr

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// PopupSize is one dimension of a popup pane: a count of outer terminal cells
// or a percentage of the terminal area. The schema describes it as an integer
// or a string such as "80%", which has no discriminator, so the type is
// written by hand.
type PopupSize struct {
	// Cells is the size in outer terminal cells, border included. It is only
	// meaningful when Percent is zero.
	Cells uint16
	// Percent is the size as a percentage of the terminal area, 1 to 100.
	// Zero means the size is a cell count.
	Percent uint8
}

// Clone returns an independent copy of s.
func (s PopupSize) Clone() PopupSize {
	return s
}

// IsPercent reports whether the size is a percentage of the terminal area.
func (s PopupSize) IsPercent() bool { return s.Percent != 0 }

// String returns the wire spelling of the size.
func (s PopupSize) String() string {
	if s.IsPercent() {
		return strconv.FormatUint(uint64(s.Percent), 10) + "%"
	}
	return strconv.FormatUint(uint64(s.Cells), 10)
}

// MarshalJSON writes a percentage as a string and a cell count as a number.
func (s PopupSize) MarshalJSON() ([]byte, error) {
	if s.IsPercent() {
		if s.Percent > 100 {
			return nil, fmt.Errorf("herdr: popup size %d%% is out of range; a percentage is 1 to 100", s.Percent)
		}
		return json.Marshal(s.String())
	}
	return json.Marshal(s.Cells)
}

// UnmarshalJSON decodes a cell count or a percentage string.
func (s *PopupSize) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return fmt.Errorf("herdr: popup size is empty")
	}
	if trimmed[0] == '"' {
		var text string
		if err := json.Unmarshal(data, &text); err != nil {
			return err
		}
		digits, ok := strings.CutSuffix(text, "%")
		if !ok {
			return fmt.Errorf("herdr: popup size %q must be a percentage like \"80%%\"; use a number for cells", text)
		}
		percent, err := strconv.ParseUint(digits, 10, 8)
		if err != nil || percent < 1 || percent > 100 {
			return fmt.Errorf("herdr: popup size %q must be a percentage between 1%% and 100%%", text)
		}
		*s = PopupSize{Percent: uint8(percent)}
		return nil
	}
	var cells uint64
	if err := json.Unmarshal(data, &cells); err != nil {
		return fmt.Errorf("herdr: popup size must be a number or a percentage like \"80%%\": %w", err)
	}
	if cells > math.MaxUint16 {
		return fmt.Errorf("herdr: popup size %d is out of range; a cell count is 0 to %d", cells, math.MaxUint16)
	}
	*s = PopupSize{Cells: uint16(cells)}
	return nil
}

// AgentViewValue is the value an agent view filter compares against. It is a
// string, a boolean, an unsigned integer, or a placeholder the server resolves
// against the current session. Exactly one field is set.
type AgentViewValue struct {
	Text    *string
	Bool    *bool
	Uint    *uint64
	Context *AgentViewContext
}

// Clone returns an independent copy of v.
func (v AgentViewValue) Clone() AgentViewValue {
	v.Text = clonePtr(v.Text)
	v.Bool = clonePtr(v.Bool)
	v.Uint = clonePtr(v.Uint)
	v.Context = clonePtr(v.Context)
	return v
}

// AgentViewText returns a string value.
func AgentViewText(text string) AgentViewValue { return AgentViewValue{Text: &text} }

// AgentViewBool returns a boolean value.
func AgentViewBool(value bool) AgentViewValue { return AgentViewValue{Bool: &value} }

// AgentViewUint returns an unsigned integer value.
func AgentViewUint(value uint64) AgentViewValue { return AgentViewValue{Uint: &value} }

// AgentViewContextValue returns a value the server resolves against the
// current session.
func AgentViewContextValue(context AgentViewContext) AgentViewValue {
	return AgentViewValue{Context: &context}
}

// MarshalJSON writes the one field that is set.
func (v AgentViewValue) MarshalJSON() ([]byte, error) {
	var set int
	for _, isSet := range []bool{v.Text != nil, v.Bool != nil, v.Uint != nil, v.Context != nil} {
		if isSet {
			set++
		}
	}
	if set != 1 {
		return nil, fmt.Errorf("herdr: an agent view value must have exactly one field set, got %d", set)
	}
	switch {
	case v.Text != nil:
		return json.Marshal(*v.Text)
	case v.Bool != nil:
		return json.Marshal(*v.Bool)
	case v.Uint != nil:
		return json.Marshal(*v.Uint)
	default:
		return json.Marshal(struct {
			Context AgentViewContext `json:"context"`
		}{Context: *v.Context})
	}
}

// UnmarshalJSON decodes the value from the JSON type it was written as.
func (v *AgentViewValue) UnmarshalJSON(data []byte) error {
	*v = AgentViewValue{}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return fmt.Errorf("herdr: agent view value is empty")
	}
	switch trimmed[0] {
	case '"':
		var text string
		if err := json.Unmarshal(data, &text); err != nil {
			return err
		}
		v.Text = &text
	case 't', 'f':
		var value bool
		if err := json.Unmarshal(data, &value); err != nil {
			return err
		}
		v.Bool = &value
	case '{':
		var wrapper struct {
			Context *AgentViewContext `json:"context"`
		}
		if err := json.Unmarshal(data, &wrapper); err != nil {
			return err
		}
		if wrapper.Context == nil {
			return fmt.Errorf("herdr: agent view value object has no context field")
		}
		v.Context = wrapper.Context
	default:
		var value uint64
		if err := json.Unmarshal(data, &value); err != nil {
			return fmt.Errorf("herdr: unsupported agent view value %s", trimmed)
		}
		v.Uint = &value
	}
	return nil
}

// AgentViewField selects what an agent view filter compares: a built-in field
// or a metadata token reported for the pane.
type AgentViewField struct {
	// Builtin is the field name; it is ignored when Token is set.
	Builtin AgentViewBuiltinField
	// Token is the name of a metadata token.
	Token string
}

// Clone returns an independent copy of f.
func (f AgentViewField) Clone() AgentViewField {
	return f
}

// AgentViewFieldOf returns a filter field for a built-in field.
func AgentViewFieldOf(field AgentViewBuiltinField) AgentViewField {
	return AgentViewField{Builtin: field}
}

// AgentViewFieldToken returns a filter field for a metadata token.
func AgentViewFieldToken(token string) AgentViewField {
	return AgentViewField{Token: token}
}

// MarshalJSON writes a built-in field as a string and a token as an object.
func (f AgentViewField) MarshalJSON() ([]byte, error) {
	if f.Token != "" {
		return marshalToken(f.Token)
	}
	if f.Builtin == "" {
		return nil, fmt.Errorf("herdr: an agent view field must name a built-in field or a token")
	}
	return json.Marshal(string(f.Builtin))
}

// UnmarshalJSON decodes a built-in field name or a token object.
func (f *AgentViewField) UnmarshalJSON(data []byte) error {
	*f = AgentViewField{}
	builtin, token, err := unmarshalFieldOrToken(data, "agent view field")
	if err != nil {
		return err
	}
	f.Builtin, f.Token = AgentViewBuiltinField(builtin), token
	return nil
}

// AgentViewSortField selects what an agent view sorts by: a built-in sort
// field or a metadata token reported for the pane.
type AgentViewSortField struct {
	// Builtin is the sort field; it is ignored when Token is set.
	Builtin AgentViewBuiltinSortField
	// Token is the name of a metadata token.
	Token string
}

// Clone returns an independent copy of f.
func (f AgentViewSortField) Clone() AgentViewSortField {
	return f
}

// AgentViewSortFieldOf returns a sort field for a built-in sort field.
func AgentViewSortFieldOf(field AgentViewBuiltinSortField) AgentViewSortField {
	return AgentViewSortField{Builtin: field}
}

// AgentViewSortFieldToken returns a sort field for a metadata token.
func AgentViewSortFieldToken(token string) AgentViewSortField {
	return AgentViewSortField{Token: token}
}

// MarshalJSON writes a built-in sort field as a string and a token as an
// object.
func (f AgentViewSortField) MarshalJSON() ([]byte, error) {
	if f.Token != "" {
		return marshalToken(f.Token)
	}
	if f.Builtin == "" {
		return nil, fmt.Errorf("herdr: an agent view sort field must name a built-in field or a token")
	}
	return json.Marshal(string(f.Builtin))
}

// UnmarshalJSON decodes a built-in sort field name or a token object.
func (f *AgentViewSortField) UnmarshalJSON(data []byte) error {
	*f = AgentViewSortField{}
	builtin, token, err := unmarshalFieldOrToken(data, "agent view sort field")
	if err != nil {
		return err
	}
	f.Builtin, f.Token = AgentViewBuiltinSortField(builtin), token
	return nil
}

func marshalToken(token string) ([]byte, error) {
	return json.Marshal(struct {
		Token string `json:"token"`
	}{Token: token})
}

// unmarshalFieldOrToken decodes the shape the two agent view field unions
// share: a bare string naming a built-in field, or {"token": "..."}.
func unmarshalFieldOrToken(data []byte, what string) (builtin, token string, err error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return "", "", fmt.Errorf("herdr: %s is empty", what)
	}
	switch trimmed[0] {
	case '"':
		if err := json.Unmarshal(data, &builtin); err != nil {
			return "", "", err
		}
		return builtin, "", nil
	case '{':
		var wrapper struct {
			Token *string `json:"token"`
		}
		if err := json.Unmarshal(data, &wrapper); err != nil {
			return "", "", err
		}
		if wrapper.Token == nil {
			return "", "", fmt.Errorf("herdr: %s object has no token field", what)
		}
		return "", *wrapper.Token, nil
	default:
		return "", "", fmt.Errorf("herdr: %s must be a string or a token object, got %s", what, trimmed)
	}
}
