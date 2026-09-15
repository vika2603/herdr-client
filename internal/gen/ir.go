package gen

import (
	"fmt"
	"sort"
)

// TypeKind distinguishes the three shapes of generated named types.
type TypeKind int

// Kinds of generated named types.
const (
	KindEnum TypeKind = iota
	KindStruct
	KindUnion
)

// Output files the generated types are written to.
const (
	fileTypes   = "types"
	fileResults = "results"
	fileEvents  = "events"
)

// EnumValue is one constant of a string enum.
type EnumValue struct {
	Name  string
	Value string
}

// Field is one struct field.
type Field struct {
	Name string
	JSON string
	Type string
	// ValueType is the schema value type before an optional object property is
	// wrapped in Optional. It equals Type for required and synthetic fields.
	ValueType string
	Doc       string
	Required  bool
	Nullable  bool
	Optional  bool
	OmitEmpty bool
	// Union names the sealed interface the field decodes through, empty when
	// the field is decoded by encoding/json alone.
	Union string
	// UnionSlice reports that the field is a slice of Union values.
	UnionSlice bool
}

// Type is one generated named type.
type Type struct {
	Name string
	Kind TypeKind
	Doc  string
	File string

	// Enum
	Values []EnumValue

	// Struct
	Fields []*Field
	// TagField and TagValue describe the discriminator that MarshalJSON adds
	// back to the encoded object.
	TagField string
	TagValue string
	// UnionName is the sealed interface the struct is a variant of.
	UnionName string
	// ResultTag is the "type" value of a ResponseResult variant.
	ResultTag string
	// EventName is the dotted name of an event payload.
	EventName string

	// Union
	Discriminator string
	Variants      []*Type
}

// NeedsUnmarshal reports whether the struct has fields that have to be decoded
// through a union decoder.
func (t *Type) NeedsUnmarshal() bool {
	for _, f := range t.Fields {
		if f.Union != "" {
			return true
		}
	}
	return false
}

// MethodKind is the call shape of a generated Client wrapper.
type MethodKind int

// Call shapes of the generated Client wrappers.
const (
	MethodSingle MethodKind = iota
	MethodMulti
	MethodStream
)

// Method is one generated Client wrapper.
type Method struct {
	Name    string
	Const   string
	Wrapper string
	Params  string
	Kind    MethodKind
	// ResultType and ResultTag describe the single expected result.
	ResultType string
	ResultTag  string
	// ResultTypes lists the documented result types of a multi-result method.
	ResultTypes []string
}

// EventRef pairs the name of a dedicated subscription with the Go type of
// its payload.
type EventRef struct {
	Name string
	Type string
}

// Package is everything the emitters need to write package herdr.
type Package struct {
	Name      string
	Protocol  uint32
	Types     []*Type
	EventKind []EnumValue
	// Subscriptions are the events that arrive under a dotted name and carry
	// no discriminator of their own.
	Subscriptions []EventRef
	Methods       []*Method
	// ManualTypes are implemented in unions_manual.go and supply their own Clone.
	ManualTypes []string
	// AuxiliaryTypes describe symbols owned by dedicated emitters rather than
	// the ordinary type loop, so generic field operations can still inspect them.
	AuxiliaryTypes []*Type
}

// TypesIn returns the types of one output file, sorted by name.
func (p *Package) TypesIn(file string) []*Type {
	var out []*Type
	for _, t := range p.Types {
		if t.File == file {
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// manualDefs are the definitions without a discriminator that are written by
// hand in unions_manual.go.
var manualDefs = map[string]bool{
	"AgentViewField":     true,
	"AgentViewSortField": true,
	"AgentViewValue":     true,
	"PopupSize":          true,
}

// specialDefs are definitions the dedicated emitters replace.
var specialDefs = map[string]bool{
	"EventData":             true,
	"EventEnvelope":         true,
	"EventKind":             true,
	"ResponseResult":        true,
	"SubscriptionEventData": true,
	"SubscriptionEventKind": true,
}

// variantNamers overrides the default <Union><Tag> variant naming.
var variantNamers = map[string]func(tag string) string{
	"Subscription": func(tag string) string { return pascal(tag) + "Subscription" },
}

type defKind int

const (
	defEnum defKind = iota
	defStruct
	defUnion
	defManual
	defSpecial
)

type builder struct {
	doc   *Document
	defs  map[string]*Node
	kinds map[string]defKind
	types map[string]*Type
	order []*Type
}

// Build turns a parsed schema and method table into the generator IR.
func Build(doc *Document, table *MethodTable, pkgName string) (*Package, error) {
	b := &builder{
		doc:   doc,
		defs:  map[string]*Node{},
		kinds: map[string]defKind{},
		types: map[string]*Type{},
	}
	if err := b.collectDefs(); err != nil {
		return nil, err
	}
	b.classify()

	pkg := &Package{Name: pkgName, Protocol: doc.Protocol}
	if err := b.buildEvents(pkg); err != nil {
		return nil, err
	}
	if err := b.buildResults(); err != nil {
		return nil, err
	}
	if err := b.buildRemaining(); err != nil {
		return nil, err
	}
	if err := breakOptionalStructCycles(b.order); err != nil {
		return nil, err
	}
	methods, err := b.buildMethods(table)
	if err != nil {
		return nil, err
	}
	pkg.Methods = methods
	pkg.Types = b.order
	for _, name := range sortedKeys(manualDefs) {
		if _, exists := b.defs[name]; exists {
			pkg.ManualTypes = append(pkg.ManualTypes, name)
		}
	}
	pkg.AuxiliaryTypes = []*Type{
		{Name: "EventKind", Kind: KindEnum},
		{Name: "Event", Kind: KindUnion, Variants: pkg.TypesIn(fileEvents)},
		{Name: "Result", Kind: KindUnion, Variants: pkg.TypesIn(fileResults)},
		{Name: "EventEnvelope", Kind: KindStruct, Doc: "EventEnvelope is one event line pushed on a subscription connection.", Fields: []*Field{
			{Name: "Event", Type: "EventKind", ValueType: "EventKind", JSON: "event", Required: true},
			{Name: "Data", Type: "Event", ValueType: "Event", JSON: "data", Required: true},
		}},
	}
	sort.Slice(pkg.Types, func(i, j int) bool { return pkg.Types[i].Name < pkg.Types[j].Name })
	return pkg, nil
}

// breakOptionalStructCycles retains value semantics for ordinary optional
// structs, but uses a pointer inside Optional when a direct struct reference
// would otherwise give a generated Go type infinite size.
func breakOptionalStructCycles(types []*Type) error {
	structs := make(map[string]*Type)
	for _, typ := range types {
		if typ.Kind == KindStruct {
			structs[typ.Name] = typ
		}
	}

	edges := func() map[string][]string {
		out := make(map[string][]string)
		for _, typ := range structs {
			for _, field := range typ.Fields {
				if _, ok := structs[field.ValueType]; ok {
					out[typ.Name] = append(out[typ.Name], field.ValueType)
				}
			}
		}
		return out
	}
	reaches := func(graph map[string][]string, from, want string) bool {
		seen := make(map[string]bool)
		var visit func(string) bool
		visit = func(name string) bool {
			if name == want {
				return true
			}
			if seen[name] {
				return false
			}
			seen[name] = true
			for _, next := range graph[name] {
				if visit(next) {
					return true
				}
			}
			return false
		}
		return visit(from)
	}

	for {
		graph := edges()
		changed := false
		for _, typ := range types {
			for _, field := range typ.Fields {
				if !field.Optional {
					continue
				}
				target, ok := structs[field.ValueType]
				if !ok || !reaches(graph, target.Name, typ.Name) {
					continue
				}
				field.ValueType = "*" + field.ValueType
				field.Type = "Optional[" + field.ValueType + "]"
				changed = true
				break
			}
			if changed {
				break
			}
		}
		if !changed {
			graph = edges()
			for owner, targets := range graph {
				for _, target := range targets {
					if reaches(graph, target, owner) {
						return fmt.Errorf("generated structs %s and %s form a required value cycle", owner, target)
					}
				}
			}
			return nil
		}
	}
}

// collectDefs merges the $defs of every section. A name used in more than one
// section must describe the same shape once $ref values are reduced to bare
// definition names.
func (b *builder) collectDefs() error {
	seen := map[string]string{}
	for _, section := range b.doc.SectionNames() {
		node := b.doc.Sections[section]
		for _, name := range sortedKeys(node.Defs) {
			def := node.Defs[name]
			shape := canonical(def)
			if prev, ok := seen[name]; ok {
				if prev != shape {
					return fmt.Errorf("definition %q differs between schema sections", name)
				}
				continue
			}
			seen[name] = shape
			b.defs[name] = def
		}
	}
	return nil
}

func (b *builder) classify() {
	for name, def := range b.defs {
		switch {
		case manualDefs[name]:
			b.kinds[name] = defManual
		case specialDefs[name]:
			b.kinds[name] = defSpecial
		case len(def.Enum) > 0:
			b.kinds[name] = defEnum
		case len(def.OneOf) > 0:
			b.kinds[name] = defUnion
		default:
			b.kinds[name] = defStruct
		}
	}
}

func (b *builder) register(t *Type) error {
	if _, ok := b.types[t.Name]; ok {
		return fmt.Errorf("duplicate type name %q", t.Name)
	}
	b.types[t.Name] = t
	b.order = append(b.order, t)
	return nil
}

// subscriptionEvents pairs the dotted names of the dedicated subscriptions
// with the definitions carrying their payload.
func (b *builder) subscriptionEvents() ([][2]string, error) {
	kind, ok := b.defs["SubscriptionEventKind"]
	if !ok {
		return nil, fmt.Errorf("schema: missing SubscriptionEventKind")
	}
	data, ok := b.defs["SubscriptionEventData"]
	if !ok {
		return nil, fmt.Errorf("schema: missing SubscriptionEventData")
	}
	if len(kind.Enum) != len(data.AnyOf) {
		return nil, fmt.Errorf("schema: SubscriptionEventKind has %d values but SubscriptionEventData has %d variants",
			len(kind.Enum), len(data.AnyOf))
	}
	out := make([][2]string, 0, len(kind.Enum))
	for i, name := range kind.Enum {
		ref := data.AnyOf[i].Ref
		if ref == "" {
			return nil, fmt.Errorf("schema: SubscriptionEventData variant %d is not a $ref", i)
		}
		out = append(out, [2]string{name, ref})
	}
	return out, nil
}

func (b *builder) buildEvents(pkg *Package) error {
	kindDef, ok := b.defs["EventKind"]
	if !ok {
		return fmt.Errorf("schema: missing EventKind")
	}
	for _, value := range kindDef.Enum {
		pkg.EventKind = append(pkg.EventKind, EnumValue{Name: enumConstName("EventKind", value), Value: value})
	}
	known := map[string]bool{}
	for _, value := range kindDef.Enum {
		known[value] = true
	}

	dataDef, ok := b.defs["EventData"]
	if !ok {
		return fmt.Errorf("schema: missing EventData")
	}
	disc, err := discriminator(dataDef.OneOf)
	if err != nil {
		return fmt.Errorf("EventData: %w", err)
	}
	for _, variant := range dataDef.OneOf {
		tag := *variant.Properties[disc].Const
		if !known[tag] {
			return fmt.Errorf("EventData: %q is not an EventKind value", tag)
		}
		t, err := b.buildStruct(pascal(tag)+"Event", variant, disc)
		if err != nil {
			return err
		}
		t.File = fileEvents
		t.TagField = disc
		t.TagValue = tag
		t.EventName = dotName(tag)
		t.Doc = variant.Description
		if err := b.register(t); err != nil {
			return err
		}
	}

	pairs, err := b.subscriptionEvents()
	if err != nil {
		return err
	}
	for _, pair := range pairs {
		name, ref := pair[0], pair[1]
		pkg.Subscriptions = append(pkg.Subscriptions, EventRef{Name: name, Type: ref})
		def, ok := b.defs[ref]
		if !ok {
			return fmt.Errorf("schema: missing definition %q", ref)
		}
		t, err := b.buildStruct(ref, def, "")
		if err != nil {
			return err
		}
		t.File = fileEvents
		t.EventName = name
		t.Doc = def.Description
		if existing, ok := b.types[ref]; ok {
			// A subscription payload that is also an EventData variant must
			// describe the same fields; the two decode into one type.
			if existing.EventName != name {
				return fmt.Errorf("%s: event name %q does not match subscription name %q", ref, existing.EventName, name)
			}
			if !sameFields(existing.Fields, t.Fields) {
				return fmt.Errorf("%s: subscription payload differs from the EventData variant", ref)
			}
			continue
		}
		if err := b.register(t); err != nil {
			return err
		}
	}
	return nil
}

func (b *builder) buildResults() error {
	def, ok := b.defs["ResponseResult"]
	if !ok {
		return fmt.Errorf("schema: missing ResponseResult")
	}
	disc, err := discriminator(def.OneOf)
	if err != nil {
		return fmt.Errorf("ResponseResult: %w", err)
	}
	for _, variant := range def.OneOf {
		tag := *variant.Properties[disc].Const
		// The Response suffix keeps the variant apart from the payload type
		// it carries, for example PaneReadResponse and PaneReadResult.
		t, err := b.buildStruct(pascal(tag)+"Response", variant, disc)
		if err != nil {
			return err
		}
		t.File = fileResults
		t.TagField = disc
		t.TagValue = tag
		t.ResultTag = tag
		t.Doc = variant.Description
		if err := b.register(t); err != nil {
			return err
		}
	}
	return nil
}

func (b *builder) buildRemaining() error {
	handled := map[string]bool{}
	pairs, err := b.subscriptionEvents()
	if err != nil {
		return err
	}
	for _, pair := range pairs {
		handled[pair[1]] = true
	}
	for _, name := range sortedKeys(b.defs) {
		if handled[name] {
			continue
		}
		def := b.defs[name]
		switch b.kinds[name] {
		case defManual, defSpecial:
			continue
		case defEnum:
			t := &Type{Name: name, Kind: KindEnum, File: fileTypes, Doc: def.Description}
			for _, value := range def.Enum {
				t.Values = append(t.Values, EnumValue{Name: enumConstName(name, value), Value: value})
			}
			if err := b.register(t); err != nil {
				return err
			}
		case defUnion:
			if err := b.buildUnion(name, def); err != nil {
				return err
			}
		default:
			t, err := b.buildStruct(name, def, "")
			if err != nil {
				return err
			}
			t.File = fileTypes
			t.Doc = def.Description
			if err := b.register(t); err != nil {
				return err
			}
		}
	}
	return nil
}

func (b *builder) buildUnion(name string, def *Node) error {
	disc, err := discriminator(def.OneOf)
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	union := &Type{
		Name:          name,
		Kind:          KindUnion,
		File:          fileTypes,
		Doc:           def.Description,
		Discriminator: disc,
	}
	if err := b.register(union); err != nil {
		return err
	}
	namer := variantNamers[name]
	for _, variant := range def.OneOf {
		tag := *variant.Properties[disc].Const
		variantName := name + pascal(tag)
		if namer != nil {
			variantName = namer(tag)
		}
		t, err := b.buildStruct(variantName, variant, disc)
		if err != nil {
			return err
		}
		t.File = fileTypes
		t.TagField = disc
		t.TagValue = tag
		t.UnionName = name
		t.Doc = variant.Description
		if err := b.register(t); err != nil {
			return err
		}
		union.Variants = append(union.Variants, t)
	}
	return nil
}

func (b *builder) buildStruct(name string, node *Node, skip string) (*Type, error) {
	if base, err := node.BaseType(); err != nil {
		return nil, err
	} else if base != "" && base != "object" {
		return nil, fmt.Errorf("%s: expected an object, got %q", node.Path, base)
	}
	t := &Type{Name: name, Kind: KindStruct, File: fileTypes}
	for _, prop := range node.PropertyNames() {
		if prop == skip {
			continue
		}
		field, err := b.field(node, prop)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		t.Fields = append(t.Fields, field)
	}
	return t, nil
}

func (b *builder) field(owner *Node, name string) (*Field, error) {
	node := owner.Properties[name]
	res, err := b.resolve(node)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	required := owner.IsRequired(name)
	f := &Field{
		Name:      pascal(name),
		JSON:      name,
		Doc:       node.Description,
		Required:  required,
		Nullable:  res.Nullable,
		Optional:  !required,
		ValueType: res.Go,
	}
	switch res.Cat {
	case catUnion:
		f.Type = res.Go
		f.Union = res.Union
	case catSlice:
		f.Type = res.Go
		f.Union = res.ElemUnion
		f.UnionSlice = res.ElemUnion != ""
	case catMap, catRaw:
		f.Type = res.Go
	default:
		if required && res.Nullable {
			f.Type = "*" + res.Go
			f.ValueType = f.Type
		} else {
			f.Type = res.Go
		}
	}
	if f.Optional {
		f.Type = "Optional[" + f.ValueType + "]"
	}
	return f, nil
}

type category int

const (
	catString category = iota
	catEnum
	catBool
	catInt
	catFloat
	catStruct
	catSlice
	catMap
	catRaw
	catUnion
)

type resolved struct {
	Go        string
	Cat       category
	Nullable  bool
	Union     string
	ElemUnion string
}

func (b *builder) resolve(n *Node) (resolved, error) {
	if n.Always != nil {
		if !*n.Always {
			return resolved{}, fmt.Errorf("%s: schema rejects every value", n.Path)
		}
		return resolved{Go: "json.RawMessage", Cat: catRaw}, nil
	}
	if n.Ref != "" {
		return b.resolveRef(n)
	}
	if len(n.AnyOf) > 0 {
		if len(n.AnyOf) != 2 || !isNull(n.AnyOf[1]) {
			return resolved{}, fmt.Errorf("%s: unsupported anyOf, only [schema, null] is supported outside $defs", n.Path)
		}
		res, err := b.resolve(n.AnyOf[0])
		if err != nil {
			return resolved{}, err
		}
		res.Nullable = true
		return res, nil
	}
	if len(n.OneOf) > 0 {
		return resolved{}, fmt.Errorf("%s: inline oneOf is not supported, define it in $defs", n.Path)
	}
	base, err := n.BaseType()
	if err != nil {
		return resolved{}, err
	}
	nullable := n.Nullable()
	switch base {
	case "string":
		if len(n.Enum) > 0 {
			return resolved{}, fmt.Errorf("%s: inline enum is not supported, define it in $defs", n.Path)
		}
		return resolved{Go: "string", Cat: catString, Nullable: nullable}, nil
	case "boolean":
		return resolved{Go: "bool", Cat: catBool, Nullable: nullable}, nil
	case "integer":
		goType, err := integerType(n.Format)
		if err != nil {
			return resolved{}, fmt.Errorf("%s: %w", n.Path, err)
		}
		return resolved{Go: goType, Cat: catInt, Nullable: nullable}, nil
	case "number":
		switch n.Format {
		case "", "float", "double":
			return resolved{Go: "float64", Cat: catFloat, Nullable: nullable}, nil
		default:
			return resolved{}, fmt.Errorf("%s: unsupported number format %q", n.Path, n.Format)
		}
	case "array":
		if n.Items == nil {
			return resolved{}, fmt.Errorf("%s: array without items", n.Path)
		}
		elem, err := b.resolve(n.Items)
		if err != nil {
			return resolved{}, err
		}
		return resolved{
			Go:        "[]" + elemType(elem),
			Cat:       catSlice,
			Nullable:  nullable,
			ElemUnion: elem.Union,
		}, nil
	case "object":
		if n.AddProps != nil {
			value, err := b.resolve(n.AddProps)
			if err != nil {
				return resolved{}, err
			}
			if value.Union != "" {
				return resolved{}, fmt.Errorf("%s: map values may not be a union", n.Path)
			}
			return resolved{Go: "map[string]" + elemType(value), Cat: catMap, Nullable: nullable}, nil
		}
		return resolved{}, fmt.Errorf("%s: inline object is not supported, define it in $defs", n.Path)
	default:
		return resolved{}, fmt.Errorf("%s: unsupported type %q", n.Path, base)
	}
}

func (b *builder) resolveRef(n *Node) (resolved, error) {
	kind, ok := b.kinds[n.Ref]
	if !ok {
		return resolved{}, fmt.Errorf("%s: unknown $ref target %q", n.Path, n.Ref)
	}
	switch kind {
	case defEnum:
		return resolved{Go: n.Ref, Cat: catEnum}, nil
	case defStruct, defManual:
		return resolved{Go: n.Ref, Cat: catStruct}, nil
	case defUnion:
		return resolved{Go: n.Ref, Cat: catUnion, Union: n.Ref}, nil
	default:
		if n.Ref == "EventEnvelope" {
			return resolved{Go: "EventEnvelope", Cat: catStruct}, nil
		}
		return resolved{}, fmt.Errorf("%s: %q cannot be referenced as a field type", n.Path, n.Ref)
	}
}

// elemType is the Go type of a slice element or map value.
func elemType(res resolved) string {
	if res.Nullable && res.Cat != catSlice && res.Cat != catMap && res.Cat != catRaw && res.Cat != catUnion {
		return "*" + res.Go
	}
	return res.Go
}

func integerType(format string) (string, error) {
	switch format {
	case "":
		return "int64", nil
	case "uint":
		return "uint64", nil
	case "uint8", "uint16", "uint32", "uint64", "int8", "int16", "int32", "int64":
		return format, nil
	default:
		return "", fmt.Errorf("unsupported integer format %q", format)
	}
}

func isNull(n *Node) bool {
	return len(n.Types) == 1 && n.Types[0] == "null"
}

// discriminator returns the single property that carries a const in every
// variant of a union.
func discriminator(variants []*Node) (string, error) {
	if len(variants) == 0 {
		return "", fmt.Errorf("union without variants")
	}
	counts := map[string]int{}
	for _, variant := range variants {
		for name, prop := range variant.Properties {
			if prop.Const != nil {
				counts[name]++
			}
		}
	}
	var found []string
	for name, count := range counts {
		if count == len(variants) {
			found = append(found, name)
		}
	}
	sort.Strings(found)
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		return "", fmt.Errorf("no property carries a const in every variant")
	default:
		return "", fmt.Errorf("several discriminator candidates: %v", found)
	}
}

func sameFields(a, b []*Field) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Name != b[i].Name || a[i].JSON != b[i].JSON ||
			a[i].Type != b[i].Type || a[i].ValueType != b[i].ValueType ||
			a[i].Required != b[i].Required || a[i].Nullable != b[i].Nullable ||
			a[i].Optional != b[i].Optional || a[i].OmitEmpty != b[i].OmitEmpty ||
			a[i].Union != b[i].Union || a[i].UnionSlice != b[i].UnionSlice {
			return false
		}
	}
	return true
}

func (b *builder) buildMethods(table *MethodTable) ([]*Method, error) {
	request, ok := b.doc.Sections["request"]
	if !ok {
		return nil, fmt.Errorf("schema: missing request section")
	}
	if len(request.OneOf) == 0 {
		return nil, fmt.Errorf("schema: request section has no methods")
	}
	resultTags := map[string]bool{}
	resultTypes := map[string]string{}
	for _, t := range b.order {
		if t.ResultTag != "" {
			resultTags[t.ResultTag] = true
			resultTypes[t.ResultTag] = t.Name
		}
	}
	var names []string
	params := map[string]string{}
	for _, variant := range request.OneOf {
		methodProp, ok := variant.Properties["method"]
		if !ok || methodProp.Const == nil {
			return nil, fmt.Errorf("%s: request variant without a method const", variant.Path)
		}
		paramsProp, ok := variant.Properties["params"]
		if !ok || paramsProp.Ref == "" {
			return nil, fmt.Errorf("%s: request variant without a params $ref", variant.Path)
		}
		names = append(names, *methodProp.Const)
		params[*methodProp.Const] = paramsProp.Ref
	}
	if err := table.validate(names, resultTags); err != nil {
		return nil, err
	}
	sort.Strings(names)
	methods := make([]*Method, 0, len(names))
	for _, name := range names {
		entry := table.Methods[name]
		wrapper := pascal(name)
		m := &Method{
			Name:    name,
			Const:   "Method" + wrapper,
			Wrapper: wrapper,
		}
		if p := params[name]; p != "EmptyParams" && p != "PingParams" {
			m.Params = p
		}
		switch {
		case entry.Stream != "":
			m.Kind = MethodStream
			m.ResultTag = entry.Stream
		case entry.Result != "":
			m.Kind = MethodSingle
			m.ResultTag = entry.Result
			m.ResultType = resultTypes[entry.Result]
		default:
			m.Kind = MethodMulti
			for _, tag := range entry.Results {
				if tag == "*" {
					m.ResultTypes = nil
					break
				}
				m.ResultTypes = append(m.ResultTypes, resultTypes[tag])
			}
		}
		methods = append(methods, m)
	}
	return methods, nil
}
