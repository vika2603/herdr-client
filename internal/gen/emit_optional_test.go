package gen

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestOptionalUnionFieldsPreserveJSONStates(t *testing.T) {
	leaf := &Type{Name: "ChoiceLeaf", Kind: KindStruct, File: fileTypes, UnionName: "Choice", TagField: "kind", TagValue: "leaf", Fields: []*Field{
		{Name: "Text", JSON: "text", Type: "string", ValueType: "string", Required: true},
	}}
	choice := &Type{Name: "Choice", Kind: KindUnion, File: fileTypes, Discriminator: "kind", Variants: []*Type{leaf}}
	envelope := &Type{Name: "Envelope", Kind: KindStruct, File: fileTypes, Fields: []*Field{
		{Name: "Required", JSON: "required", Type: "bool", ValueType: "bool", Required: true},
		{Name: "Count", JSON: "count", Type: "Optional[uint64]", ValueType: "uint64", Optional: true},
		{Name: "Choice", JSON: "choice", Type: "Optional[Choice]", ValueType: "Choice", Optional: true, Union: "Choice"},
		{Name: "Choices", JSON: "choices", Type: "Optional[[]Choice]", ValueType: "[]Choice", Optional: true, Union: "Choice", UnionSlice: true},
	}}
	pkg := &Package{Name: "fixture", Types: []*Type{choice, leaf, envelope}}
	clones, err := newCloneEmitter(pkg)
	if err != nil {
		t.Fatal(err)
	}

	files := map[string]string{
		"go.mod":       "module fixture\n\ngo 1.25\n",
		"types_gen.go": emitTypesFile(pkg, clones),
		"optional.go": `package fixture
import "encoding/json"
type Optional[T any] struct { value T; state uint8 }
func Some[T any](v T) Optional[T] { return Optional[T]{value: v, state: 2} }
func Null[T any]() Optional[T] { return Optional[T]{state: 1} }
func (o Optional[T]) Get() (T, bool) { return o.value, o.state == 2 }
func (o Optional[T]) IsSet() bool { return o.state != 0 }
func (o Optional[T]) IsNull() bool { return o.state == 1 }
func (o Optional[T]) IsZero() bool { return o.state == 0 }
func (o Optional[T]) MarshalJSON() ([]byte, error) { if o.state != 2 { return []byte("null"), nil }; return json.Marshal(o.value) }
func (o *Optional[T]) UnmarshalJSON(data []byte) error { if string(data) == "null" { *o = Null[T](); return nil }; var v T; if err := json.Unmarshal(data, &v); err != nil { return err }; *o = Some(v); return nil }
`,
		"optional_test.go": `package fixture
import ("encoding/json"; "testing")
func TestStates(t *testing.T) {
    encoded, err := json.Marshal(Envelope{Count: Some(uint64(0))})
    if err != nil || string(encoded) != "{\"required\":false,\"count\":0}" { t.Fatalf("encoded = %s, %v", encoded, err) }
    var absent Envelope
    if err := json.Unmarshal([]byte("{\"required\":true}"), &absent); err != nil || absent.Choice.IsSet() || absent.Choices.IsSet() { t.Fatalf("absent = %#v, %v", absent, err) }
    var null Envelope
    if err := json.Unmarshal([]byte("{\"required\":true,\"choice\":null,\"choices\":null}"), &null); err != nil || !null.Choice.IsNull() || !null.Choices.IsNull() { t.Fatalf("null = %#v, %v", null, err) }
    var value Envelope
    if err := json.Unmarshal([]byte("{\"required\":true,\"choice\":{\"kind\":\"leaf\",\"text\":\"one\"},\"choices\":[]}"), &value); err != nil { t.Fatal(err) }
    one, ok := value.Choice.Get(); if !ok || one.(ChoiceLeaf).Text != "one" { t.Fatalf("choice = %#v, %t", one, ok) }
    many, ok := value.Choices.Get(); if !ok || many == nil || len(many) != 0 { t.Fatalf("choices = %#v, %t", many, ok) }
}
`,
	}
	dir := t.TempDir()
	for name, contents := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "test", ".")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOTOOLCHAIN=local", "GOWORK=off")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generated fixture: %v\n%s", err, output)
	}
}
