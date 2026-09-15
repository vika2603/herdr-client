package gen

import (
	"strings"
	"testing"
)

func mustParseNode(t *testing.T, source string) *Node {
	t.Helper()
	node, err := parseNode("/test", []byte(source))
	if err != nil {
		t.Fatalf("parseNode: %v", err)
	}
	return node
}

// testBuilder knows the definition kinds the field tests reference.
func testBuilder() *builder {
	return &builder{
		kinds: map[string]defKind{
			"AgentStatus": defEnum,
			"PaneInfo":    defStruct,
			"LayoutNode":  defUnion,
			"PopupSize":   defManual,
		},
	}
}

func TestFieldOptionalAndNullableMapping(t *testing.T) {
	owner := mustParseNode(t, `{
		"type": "object",
		"properties": {
			"required_string":   {"type": "string"},
			"required_null":     {"type": ["string", "null"]},
			"required_bool":     {"type": "boolean"},
			"required_enum":     {"$ref": "#/schemas/request/$defs/AgentStatus"},
			"optional_string":   {"type": "string"},
			"optional_enum":     {"$ref": "#/schemas/request/$defs/AgentStatus"},
			"optional_bool":     {"type": "boolean"},
			"optional_int":      {"type": "integer", "format": "uint64"},
			"optional_struct":   {"$ref": "#/schemas/request/$defs/PaneInfo"},
			"optional_manual":   {"anyOf": [{"$ref": "#/schemas/request/$defs/PopupSize"}, {"type": "null"}]},
			"optional_null":     {"type": ["string", "null"]},
			"optional_array":    {"type": "array", "items": {"type": "string"}},
			"optional_nullable_array": {"type": ["array", "null"], "items": {"type": "string"}},
			"optional_map":      {"type": "object", "additionalProperties": {"type": "string"}},
			"optional_null_map": {"type": "object", "additionalProperties": {"type": ["string", "null"]}},
			"raw":               true,
			"union":             {"$ref": "#/schemas/request/$defs/LayoutNode"},
			"union_list":        {"type": "array", "items": {"$ref": "#/schemas/request/$defs/LayoutNode"}}
		},
		"required": ["required_string", "required_null", "required_bool", "required_enum", "raw", "union"]
	}`)

	cases := []struct {
		property  string
		goType    string
		valueType string
		required  bool
		nullable  bool
		union     string
		slice     bool
	}{
		{property: "required_string", goType: "string", valueType: "string", required: true},
		{property: "required_null", goType: "*string", valueType: "*string", required: true, nullable: true},
		{property: "required_bool", goType: "bool", valueType: "bool", required: true},
		{property: "required_enum", goType: "AgentStatus", valueType: "AgentStatus", required: true},
		{property: "optional_string", goType: "Optional[string]", valueType: "string"},
		{property: "optional_enum", goType: "Optional[AgentStatus]", valueType: "AgentStatus"},
		{property: "optional_bool", goType: "Optional[bool]", valueType: "bool"},
		{property: "optional_int", goType: "Optional[uint64]", valueType: "uint64"},
		{property: "optional_struct", goType: "Optional[PaneInfo]", valueType: "PaneInfo"},
		{property: "optional_manual", goType: "Optional[PopupSize]", valueType: "PopupSize", nullable: true},
		{property: "optional_null", goType: "Optional[string]", valueType: "string", nullable: true},
		{property: "optional_array", goType: "Optional[[]string]", valueType: "[]string"},
		{property: "optional_nullable_array", goType: "Optional[[]string]", valueType: "[]string", nullable: true},
		{property: "optional_map", goType: "Optional[map[string]string]", valueType: "map[string]string"},
		{property: "optional_null_map", goType: "Optional[map[string]*string]", valueType: "map[string]*string"},
		{property: "raw", goType: "json.RawMessage", valueType: "json.RawMessage", required: true},
		{property: "union", goType: "LayoutNode", valueType: "LayoutNode", required: true, union: "LayoutNode"},
		{property: "union_list", goType: "Optional[[]LayoutNode]", valueType: "[]LayoutNode", union: "LayoutNode", slice: true},
	}
	b := testBuilder()
	for _, c := range cases {
		field, err := b.field(owner, c.property)
		if err != nil {
			t.Errorf("%s: %v", c.property, err)
			continue
		}
		if field.Type != c.goType {
			t.Errorf("%s: type = %q, want %q", c.property, field.Type, c.goType)
		}
		if field.ValueType != c.valueType || field.Required != c.required || field.Optional == c.required || field.Nullable != c.nullable {
			t.Errorf("%s: metadata = value %q required %t optional %t nullable %t", c.property, field.ValueType, field.Required, field.Optional, field.Nullable)
		}
		if field.OmitEmpty {
			t.Errorf("%s: schema field unexpectedly uses omitempty", c.property)
		}
		if field.Union != c.union || field.UnionSlice != c.slice {
			t.Errorf("%s: union = %q/%t, want %q/%t", c.property, field.Union, field.UnionSlice, c.union, c.slice)
		}
	}
}

func TestFieldNameAndTag(t *testing.T) {
	owner := mustParseNode(t, `{"type":"object","properties":{"pane_id":{"type":"string"}},"required":["pane_id"]}`)
	field, err := testBuilder().field(owner, "pane_id")
	if err != nil {
		t.Fatalf("field: %v", err)
	}
	if field.Name != "PaneID" || field.JSON != "pane_id" {
		t.Fatalf("field = %+v", field)
	}
	if got, want := field.tag(), "`json:\"pane_id\"`"; got != want {
		t.Errorf("tag = %s, want %s", got, want)
	}
}

func TestOptionalFieldTag(t *testing.T) {
	owner := mustParseNode(t, `{"type":"object","properties":{"focus":{"type":"boolean"}}}`)
	field, err := testBuilder().field(owner, "focus")
	if err != nil {
		t.Fatalf("field: %v", err)
	}
	if got, want := field.tag(), "`json:\"focus,omitzero\"`"; got != want {
		t.Errorf("tag = %s, want %s", got, want)
	}
}

func TestOptionalStructCycleUsesPointerValue(t *testing.T) {
	node := &Type{Name: "Node", Kind: KindStruct, Fields: []*Field{{
		Name: "Next", Type: "Optional[Node]", ValueType: "Node", Optional: true,
	}}}
	if err := breakOptionalStructCycles([]*Type{node}); err != nil {
		t.Fatal(err)
	}
	if got, want := node.Fields[0].Type, "Optional[*Node]"; got != want {
		t.Fatalf("recursive field type = %q, want %q", got, want)
	}

	required := &Type{Name: "Required", Kind: KindStruct, Fields: []*Field{{
		Name: "Next", Type: "Required", ValueType: "Required", Required: true,
	}}}
	if err := breakOptionalStructCycles([]*Type{required}); err == nil {
		t.Fatal("required value cycle was accepted")
	}
}

func TestIntegerFormats(t *testing.T) {
	cases := map[string]string{
		"":       "int64",
		"uint":   "uint64",
		"uint16": "uint16",
		"uint32": "uint32",
		"uint64": "uint64",
		"int32":  "int32",
	}
	for format, want := range cases {
		got, err := integerType(format)
		if err != nil {
			t.Errorf("integerType(%q): %v", format, err)
			continue
		}
		if got != want {
			t.Errorf("integerType(%q) = %q, want %q", format, got, want)
		}
	}
	if _, err := integerType("i128"); err == nil {
		t.Error("integerType accepted an unknown format")
	}
}

func TestDiscriminator(t *testing.T) {
	variants := func(t *testing.T, sources ...string) []*Node {
		t.Helper()
		var out []*Node
		for _, source := range sources {
			out = append(out, mustParseNode(t, source))
		}
		return out
	}

	found, err := discriminator(variants(t,
		`{"type":"object","properties":{"type":{"const":"pane","type":"string"},"pane_id":{"type":"string"}}}`,
		`{"type":"object","properties":{"type":{"const":"split","type":"string"},"ratio":{"type":"number"}}}`,
	))
	if err != nil || found != "type" {
		t.Fatalf("discriminator = %q, %v; want \"type\"", found, err)
	}

	found, err = discriminator(variants(t,
		`{"type":"object","properties":{"event":{"const":"pane_closed","type":"string"}}}`,
		`{"type":"object","properties":{"event":{"const":"pane_created","type":"string"}}}`,
	))
	if err != nil || found != "event" {
		t.Fatalf("discriminator = %q, %v; want \"event\"", found, err)
	}

	if _, err := discriminator(variants(t,
		`{"type":"object","properties":{"type":{"const":"a","type":"string"}}}`,
		`{"type":"object","properties":{"kind":{"const":"b","type":"string"}}}`,
	)); err == nil {
		t.Error("discriminator accepted variants without a shared const property")
	}

	if _, err := discriminator(variants(t,
		`{"type":"object","properties":{"type":{"const":"a","type":"string"},"kind":{"const":"x","type":"string"}}}`,
		`{"type":"object","properties":{"type":{"const":"b","type":"string"},"kind":{"const":"y","type":"string"}}}`,
	)); err == nil {
		t.Error("discriminator accepted an ambiguous union")
	}
}

func TestUnsupportedKeywordIsRejected(t *testing.T) {
	_, err := ParseSchema([]byte(`{
		"protocol": 1,
		"schema_version": 1,
		"schemas": {"request": {"type": "object", "allOf": [{"type": "object"}]}}
	}`))
	if err == nil || !strings.Contains(err.Error(), "allOf") {
		t.Fatalf("error = %v, want it to name allOf", err)
	}
}

func TestValidationKeywordsAreAccepted(t *testing.T) {
	doc, err := ParseSchema([]byte(`{
		"protocol": 22,
		"schema_version": 1,
		"schemas": {"request": {"type": "object", "properties": {
			"n": {"type": "integer", "format": "uint16", "minimum": 0, "maximum": 65535}
		}}}
	}`))
	if err != nil {
		t.Fatalf("ParseSchema: %v", err)
	}
	if doc.Protocol != 22 {
		t.Errorf("protocol = %d, want 22", doc.Protocol)
	}
}

func TestDefinitionsMustAgreeAcrossSections(t *testing.T) {
	source := `{
		"protocol": 1,
		"schema_version": 1,
		"schemas": {
			"event": {"type": "object", "$defs": {"PaneInfo": {"type": "object", "properties": {"pane_id": {"type": "string"}}}}},
			"success_response": {"type": "object", "$defs": {"PaneInfo": {"type": "object", "properties": {"pane_id": {"type": "integer"}}}}}
		}
	}`
	doc, err := ParseSchema([]byte(source))
	if err != nil {
		t.Fatalf("ParseSchema: %v", err)
	}
	b := &builder{doc: doc, defs: map[string]*Node{}, kinds: map[string]defKind{}}
	if err := b.collectDefs(); err == nil || !strings.Contains(err.Error(), "PaneInfo") {
		t.Fatalf("error = %v, want it to name PaneInfo", err)
	}
}

func TestDefinitionsMayRepeatWithTheSameShape(t *testing.T) {
	source := `{
		"protocol": 1,
		"schema_version": 1,
		"schemas": {
			"event": {"type": "object", "$defs": {"PaneInfo": {"type": "object", "properties": {"pane": {"$ref": "#/schemas/event/$defs/PaneInfo"}}}}},
			"success_response": {"type": "object", "$defs": {"PaneInfo": {"type": "object", "properties": {"pane": {"$ref": "#/schemas/success_response/$defs/PaneInfo"}}}}}
		}
	}`
	doc, err := ParseSchema([]byte(source))
	if err != nil {
		t.Fatalf("ParseSchema: %v", err)
	}
	b := &builder{doc: doc, defs: map[string]*Node{}, kinds: map[string]defKind{}}
	if err := b.collectDefs(); err != nil {
		t.Fatalf("collectDefs: %v", err)
	}
	if len(b.defs) != 1 {
		t.Fatalf("collected %d definitions, want 1", len(b.defs))
	}
}
