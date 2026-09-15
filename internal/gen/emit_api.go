package gen

import (
	"fmt"
	"go/format"
	"sort"
	"strings"
)

// Files are the generated file names, in a stable order.
var Files = []string{
	"schema_gen.go",
	"types_gen.go",
	"results_gen.go",
	"events_gen.go",
	"methods_gen.go",
}

// Generate turns the schema snapshot and the method result table into the
// generated files of package pkgName, keyed by file name.
func Generate(schemaData, methodsData []byte, pkgName string) (map[string][]byte, error) {
	doc, err := ParseSchema(schemaData)
	if err != nil {
		return nil, err
	}
	table, err := ParseMethodTable(methodsData)
	if err != nil {
		return nil, err
	}
	pkg, err := Build(doc, table, pkgName)
	if err != nil {
		return nil, err
	}
	sources := map[string]string{
		"schema_gen.go":  emitSchemaFile(pkg),
		"types_gen.go":   emitTypesFile(pkg),
		"results_gen.go": emitResultsFile(pkg),
		"events_gen.go":  emitEventsFile(pkg),
		"methods_gen.go": emitMethodsFile(pkg),
	}
	out := make(map[string][]byte, len(sources))
	for name, src := range sources {
		formatted, err := format.Source([]byte(src))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		out[name] = formatted
	}
	return out, nil
}

func emitSchemaFile(pkg *Package) string {
	c := newFile(pkg.Name)
	c.blank()
	c.doc("SchemaProtocol is the protocol version of the API schema this code was generated from. Compare it with the protocol reported by Ping to detect a server that is newer than the generated code.")
	c.printf("const SchemaProtocol uint32 = %d\n", pkg.Protocol)
	return c.String()
}

func emitTypesFile(pkg *Package) string {
	types := pkg.TypesIn(fileTypes)
	needJSON, needFmt := false, false
	for _, t := range types {
		if t.Kind == KindUnion {
			needJSON, needFmt = true, true
		}
		if t.TagField != "" || t.NeedsUnmarshal() {
			needJSON = true
		}
		for _, f := range t.Fields {
			if strings.Contains(f.Type, "json.RawMessage") {
				needJSON = true
			}
		}
	}
	var imports []string
	if needJSON {
		imports = append(imports, "encoding/json")
	}
	if needFmt {
		imports = append(imports, "fmt")
	}
	c := newFile(pkg.Name, imports...)
	for _, t := range types {
		c.blank()
		emitType(c, t)
	}
	return c.String()
}

func emitResultsFile(pkg *Package) string {
	results := pkg.TypesIn(fileResults)
	c := newFile(pkg.Name, "encoding/json", "fmt")

	c.blank()
	c.doc("Result is the decoded result object of a successful response. Every variant is named after its \"type\" value with a Response suffix, for example PaneInfoResponse for \"pane_info\".")
	c.line("type Result interface {")
	c.doc("\tResultType returns the value of the result's \"type\" field.")
	c.line("\tResultType() string")
	c.line("}")

	c.blank()
	c.doc("DecodeResult decodes a result object into the type named by its \"type\" field. An unknown type is reported as *UnknownResultError.")
	c.line("func DecodeResult(raw json.RawMessage) (Result, error) {")
	c.line("\tvar head struct {")
	c.line("\t\tType string `json:\"type\"`")
	c.line("\t}")
	c.line("\tif err := json.Unmarshal(raw, &head); err != nil {")
	c.line("\t\treturn nil, err")
	c.line("\t}")
	c.line("\tswitch head.Type {")
	for _, t := range results {
		c.printf("\tcase %q:\n", t.ResultTag)
		c.printf("\t\tvar value %s\n", t.Name)
		c.line("\t\tif err := json.Unmarshal(raw, &value); err != nil {")
		c.line("\t\t\treturn nil, err")
		c.line("\t\t}")
		c.line("\t\treturn &value, nil")
	}
	c.line("\t}")
	c.line("\treturn nil, &UnknownResultError{Type: head.Type, Data: append(json.RawMessage(nil), raw...)}")
	c.line("}")

	c.blank()
	c.doc("UnknownResultError is returned for a result type that is not in the schema the code was generated from. Data holds the undecoded result object.")
	c.line("type UnknownResultError struct {")
	c.line("\tType string")
	c.line("\tData json.RawMessage")
	c.line("}")
	c.blank()
	c.line("func (e *UnknownResultError) Error() string {")
	c.line("\treturn fmt.Sprintf(\"herdr: unknown result type %q\", e.Type)")
	c.line("}")

	c.blank()
	c.doc("UnexpectedResultError is returned when a method answers with a result type other than the one it is documented to return.")
	c.line("type UnexpectedResultError struct {")
	c.line("\tMethod string")
	c.line("\tWant   string")
	c.line("\tGot    string")
	c.line("}")
	c.blank()
	c.line("func (e *UnexpectedResultError) Error() string {")
	c.line("\treturn fmt.Sprintf(\"herdr: %s: expected result type %q, got %q\", e.Method, e.Want, e.Got)")
	c.line("}")

	for _, t := range results {
		c.blank()
		emitType(c, t)
	}
	return c.String()
}

func emitEventsFile(pkg *Package) string {
	events := pkg.TypesIn(fileEvents)
	c := newFile(pkg.Name, "context", "encoding/json", "fmt", "strings")

	c.blank()
	c.doc("EventKind is the \"event\" field of a lifecycle event envelope.")
	c.line("type EventKind string")
	c.blank()
	c.line("// EventKind values.")
	c.line("const (")
	for _, v := range pkg.EventKind {
		c.printf("\t%s EventKind = %q\n", v.Name, v.Value)
	}
	c.line(")")

	c.blank()
	c.doc("DotName returns the dotted spelling of the event name, matching EventKind::dot_name in herdr: the first underscore becomes a dot.")
	c.line("func (k EventKind) DotName() string {")
	c.line("\treturn strings.Replace(string(k), \"_\", \".\", 1)")
	c.line("}")

	c.blank()
	c.doc("Event is a decoded event payload.")
	c.line("type Event interface {")
	c.doc("\tEventName returns the dotted name of the event, for example \"pane.created\".")
	c.line("\tEventName() string")
	c.line("}")

	c.blank()
	c.doc("EventEnvelope is one event line pushed on a subscription connection.")
	c.line("type EventEnvelope struct {")
	c.line("\tEvent EventKind `json:\"event\"`")
	c.line("\tData  Event     `json:\"data\"`")
	c.line("}")

	c.blank()
	c.doc("UnmarshalJSON decodes the payload according to the event name.")
	c.line("func (e *EventEnvelope) UnmarshalJSON(data []byte) error {")
	c.line("\tvar aux struct {")
	c.line("\t\tEvent EventKind       `json:\"event\"`")
	c.line("\t\tData  json.RawMessage `json:\"data\"`")
	c.line("\t}")
	c.line("\tif err := json.Unmarshal(data, &aux); err != nil {")
	c.line("\t\treturn err")
	c.line("\t}")
	c.line("\te.Event = aux.Event")
	c.line("\te.Data = nil")
	c.line("\tif len(aux.Data) == 0 || string(aux.Data) == \"null\" {")
	c.line("\t\treturn nil")
	c.line("\t}")
	c.line("\tdecoded, err := DecodeEvent(string(aux.Event), aux.Data)")
	c.line("\tif err != nil {")
	c.line("\t\treturn err")
	c.line("\t}")
	c.line("\te.Data = decoded")
	c.line("\treturn nil")
	c.line("}")

	c.blank()
	c.doc("DecodeEvent decodes an event payload. A dotted event name selects one of the dedicated subscription payloads; any other name selects the variant named by the payload's \"type\" field. An unknown name is reported as *UnknownEventError.")
	c.line("func DecodeEvent(event string, data json.RawMessage) (Event, error) {")
	c.line("\tif strings.Contains(event, \".\") {")
	c.line("\t\tswitch event {")
	for _, ref := range pkg.Subscriptions {
		c.printf("\t\tcase %q:\n", ref.Name)
		c.printf("\t\t\tvar value %s\n", ref.Type)
		c.line("\t\t\tif err := json.Unmarshal(data, &value); err != nil {")
		c.line("\t\t\t\treturn nil, err")
		c.line("\t\t\t}")
		c.line("\t\t\treturn &value, nil")
	}
	c.line("\t\t}")
	c.line("\t\treturn nil, &UnknownEventError{Event: event, Data: append(json.RawMessage(nil), data...)}")
	c.line("\t}")
	c.line("\tvar head struct {")
	c.line("\t\tType string `json:\"type\"`")
	c.line("\t}")
	c.line("\tif err := json.Unmarshal(data, &head); err != nil {")
	c.line("\t\treturn nil, err")
	c.line("\t}")
	c.line("\tswitch head.Type {")
	for _, t := range events {
		if t.TagValue == "" {
			continue
		}
		c.printf("\tcase %q:\n", t.TagValue)
		c.printf("\t\tvar value %s\n", t.Name)
		c.line("\t\tif err := json.Unmarshal(data, &value); err != nil {")
		c.line("\t\t\treturn nil, err")
		c.line("\t\t}")
		c.line("\t\treturn &value, nil")
	}
	c.line("\t}")
	c.line("\treturn nil, &UnknownEventError{Event: event, Data: append(json.RawMessage(nil), data...)}")
	c.line("}")

	c.blank()
	c.doc("UnknownEventError is returned for an event that is not in the schema the code was generated from. Data holds the undecoded payload.")
	c.line("type UnknownEventError struct {")
	c.line("\tEvent string")
	c.line("\tData  json.RawMessage")
	c.line("}")
	c.blank()
	c.line("func (e *UnknownEventError) Error() string {")
	c.line("\treturn fmt.Sprintf(\"herdr: unknown event %q\", e.Event)")
	c.line("}")

	c.blank()
	c.doc("NextEvent reads the next line the server pushes and decodes its payload. The errors of Next are returned unchanged; payload decoding failures carry OpDecode.")
	c.line("func (s *Stream) NextEvent(ctx context.Context) (Event, error) {")
	c.line("\traw, err := s.Next(ctx)")
	c.line("\tif err != nil {")
	c.line("\t\treturn nil, err")
	c.line("\t}")
	c.line("\tevent, err := DecodeEvent(raw.Event, raw.Data)")
	c.line("\treturn event, opError(s.method, OpDecode, err)")
	c.line("}")

	for _, t := range events {
		c.blank()
		emitType(c, t)
	}
	return c.String()
}

func emitMethodsFile(pkg *Package) string {
	methods := append([]*Method(nil), pkg.Methods...)
	sort.Slice(methods, func(i, j int) bool { return methods[i].Wrapper < methods[j].Wrapper })

	c := newFile(pkg.Name, "context")
	c.blank()
	c.line("// Method names of the Herdr API.")
	c.line("const (")
	for _, m := range methods {
		c.printf("\t%s = %q\n", m.Const, m.Name)
	}
	c.line(")")

	for _, m := range methods {
		c.blank()
		emitMethod(c, m)
	}
	return c.String()
}

func emitMethod(c *code, m *Method) {
	params := ""
	argument := "nil"
	if m.Params != "" {
		params = fmt.Sprintf(", params %s", m.Params)
		argument = "params"
	}
	switch m.Kind {
	case MethodStream:
		c.doc(fmt.Sprintf("%s calls %q and keeps the connection open. Read the pushed events with (*Stream).NextEvent.", m.Wrapper, m.Name))
		c.printf("func (c *Client) %s(ctx context.Context%s) (*Stream, error) {\n", m.Wrapper, params)
		c.printf("\treturn c.OpenStream(ctx, %s, %s)\n", m.Const, argument)
		c.line("}")
	case MethodMulti:
		doc := fmt.Sprintf("%s calls %q. The method answers with more than one result type, so the result is returned as a Result.", m.Wrapper, m.Name)
		if len(m.ResultTypes) > 0 {
			doc = fmt.Sprintf("%s calls %q. The result is one of %s.", m.Wrapper, m.Name, joinTypes(m.ResultTypes))
		}
		c.doc(doc)
		c.printf("func (c *Client) %s(ctx context.Context%s) (Result, error) {\n", m.Wrapper, params)
		c.printf("\traw, err := c.CallRaw(ctx, %s, %s)\n", m.Const, argument)
		c.line("\tif err != nil {")
		c.line("\t\treturn nil, err")
		c.line("\t}")
		c.printf("\treturn decodeResult(%s, raw)\n", m.Const)
		c.line("}")
	default:
		c.doc(fmt.Sprintf("%s calls %q.", m.Wrapper, m.Name))
		c.printf("func (c *Client) %s(ctx context.Context%s) (*%s, error) {\n", m.Wrapper, params, m.ResultType)
		c.printf("\traw, err := c.CallRaw(ctx, %s, %s)\n", m.Const, argument)
		c.line("\tif err != nil {")
		c.line("\t\treturn nil, err")
		c.line("\t}")
		c.printf("\tresult, err := decodeResult(%s, raw)\n", m.Const)
		c.line("\tif err != nil {")
		c.line("\t\treturn nil, err")
		c.line("\t}")
		c.printf("\ttyped, ok := result.(*%s)\n", m.ResultType)
		c.line("\tif !ok {")
		c.printf("\t\treturn nil, opError(%s, OpDecode, &UnexpectedResultError{Method: %s, Want: %q, Got: result.ResultType()})\n", m.Const, m.Const, m.ResultTag)
		c.line("\t}")
		c.line("\treturn typed, nil")
		c.line("}")
	}
}

func joinTypes(names []string) string {
	pointers := make([]string, 0, len(names))
	for _, name := range names {
		pointers = append(pointers, "*"+name)
	}
	switch len(pointers) {
	case 1:
		return pointers[0]
	case 2:
		return pointers[0] + " or " + pointers[1]
	default:
		return strings.Join(pointers[:len(pointers)-1], ", ") + " or " + pointers[len(pointers)-1]
	}
}
