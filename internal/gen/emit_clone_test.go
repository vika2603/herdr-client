package gen

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCloneMethodsAreAdjacentAndCopyNestedValues(t *testing.T) {
	pkg := &Package{Name: "fixture", Types: []*Type{
		{Name: "Flag", Kind: KindEnum},
		{Name: "Child", Kind: KindStruct, Fields: []*Field{
			{Name: "Text", Type: "*string"}, {Name: "Next", Type: "*Child"},
		}},
		{Name: "Envelope", Kind: KindStruct, Fields: []*Field{
			{Name: "Child", Type: "*Child"}, {Name: "Rows", Type: "[]Child"},
			{Name: "Lists", Type: "*map[string][]string"},
			{Name: "Raw", Type: "map[string]json.RawMessage"},
			{Name: "Empty", Type: "*[]string"}, {Name: "Flags", Type: "[]Flag"},
			{Name: "Maybe", Type: "Optional[[]Child]"}, {Name: "Label", Type: "Optional[string]"},
		}},
	}}
	for _, typ := range pkg.Types {
		typ.File = fileTypes
	}
	clones, err := newCloneEmitter(pkg)
	if err != nil {
		t.Fatal(err)
	}
	source := emitTypesFile(pkg, clones)
	file, err := parser.ParseFile(token.NewFileSet(), "types_gen.go", source, 0)
	if err != nil {
		t.Fatal(err)
	}
	for i, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "Clone" {
			continue
		}
		previous, ok := file.Decls[i-1].(*ast.GenDecl)
		if !ok || previous.Tok != token.TYPE || len(previous.Specs) != 1 {
			t.Fatal("Clone is not immediately after its type declaration")
		}
		if previous.Specs[0].(*ast.TypeSpec).Name.Name != fn.Recv.List[0].Type.(*ast.Ident).Name {
			t.Fatal("Clone follows a different type")
		}
	}

	dir := t.TempDir()
	files := map[string]string{
		"go.mod":       "module fixture\n\ngo 1.25\n",
		"types_gen.go": source,
		"optional.go": `package fixture
type Optional[T any] struct { value T; state uint8 }
func Some[T any](v T) Optional[T] { return Optional[T]{value: v, state: 2} }
func Null[T any]() Optional[T] { return Optional[T]{state: 1} }
func (o Optional[T]) Get() (T, bool) { return o.value, o.state == 2 }
`,
		"clone_test.go": `package fixture
import ("encoding/json"; "reflect"; "testing")
func TestClone(t *testing.T) {
    text := "source"
    lists := map[string][]string{"value": {"source"}, "empty": {}}
    var nilSlice []string
    source := Envelope{
        Child: &Child{Text: &text, Next: &Child{}}, Rows: []Child{{Text: &text}},
        Lists: &lists, Empty: &nilSlice, Flags: []Flag{"source"},
		Raw: map[string]json.RawMessage{"value": {1,2}, "empty": {}, "nil": nil},
		Maybe: Some([]Child{{Text: &text}}), Label: Some("source"),
    }
    cloned := source.Clone()
    if !reflect.DeepEqual(source, cloned) { t.Fatal("Clone changed values") }
    if cloned.Empty == source.Empty || *cloned.Empty != nil { t.Fatal("pointer to nil slice not detached") }
    *cloned.Child.Text = "clone"
    cloned.Child.Next.Text = &text
    *cloned.Rows[0].Text = "clone"
    (*cloned.Lists)["value"][0] = "clone"
    (*cloned.Lists)["empty"] = append((*cloned.Lists)["empty"], "clone")
    cloned.Raw["value"][0] = 9
	cloned.Flags[0] = "clone"
	maybe, ok := cloned.Maybe.Get()
	if !ok { t.Fatal("present optional became absent") }
	*maybe[0].Text = "clone"
	if label, ok := cloned.Label.Get(); !ok || label != "source" { t.Fatal("scalar optional changed") }
    if text != "source" || source.Child.Next.Text != nil ||
        lists["value"][0] != "source" || len(lists["empty"]) != 0 ||
		source.Raw["value"][0] != 1 || source.Flags[0] != "source" ||
		func() bool { value, _ := source.Maybe.Get(); return *value[0].Text != "source" }() {
        t.Fatal("nested values still alias source")
    }
	zero := Envelope{}
	if !reflect.DeepEqual(zero, zero.Clone()) { t.Fatal("Clone changed nil values") }
	null := Envelope{Maybe: Null[[]Child]()}
	if !reflect.DeepEqual(null, null.Clone()) { t.Fatal("Clone changed null optional") }
}
`,
	}
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

func TestCloneRejectsUnsupportedFields(t *testing.T) {
	for _, fieldType := range []string{"ManualValue", "map[int]string", "[2]string", "other.Type", "func()"} {
		t.Run(fieldType, func(t *testing.T) {
			pkg := &Package{Types: []*Type{
				{Name: "Envelope", Kind: KindStruct, Fields: []*Field{{Name: "Value", Type: fieldType}}},
			}}
			if _, err := newCloneEmitter(pkg); err == nil || !strings.Contains(err.Error(), "Envelope.Value") {
				t.Fatalf("error = %v, want owning type and field", err)
			}
		})
	}
	pkg := &Package{Types: []*Type{{Name: "Record", Kind: KindStruct, Fields: []*Field{{Name: "Clone", Type: "string"}}}}}
	if _, err := newCloneEmitter(pkg); err == nil {
		t.Fatal("accepted a Clone field collision")
	}
}
