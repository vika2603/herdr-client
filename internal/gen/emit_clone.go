package gen

import (
	"fmt"
	"go/ast"
	"go/parser"
)

// newCloneEmitter validates every generated struct and union. Copy generation
// follows field types and the IR, without selecting a named root or type list.
func newCloneEmitter(pkg *Package) (*cloneEmitter, error) {
	e := &cloneEmitter{types: make(map[string]*Type), seen: make(map[string]bool), manual: make(map[string]bool), unions: make(map[string]bool)}
	for _, name := range pkg.ManualTypes {
		e.manual[name] = true
	}
	all := append(append([]*Type(nil), pkg.Types...), pkg.AuxiliaryTypes...)
	for _, typ := range all {
		e.types[typ.Name] = typ
	}
	for _, typ := range all {
		if typ.Kind != KindEnum {
			if err := e.visit(typ); err != nil {
				return nil, err
			}
		}
	}
	return e, nil
}

type cloneEmitter struct {
	types   map[string]*Type
	seen    map[string]bool
	manual  map[string]bool
	unions  map[string]bool
	nextVar int
}

func (e *cloneEmitter) visit(typ *Type) error {
	if e.seen[typ.Name] {
		return nil
	}
	e.seen[typ.Name] = true
	if typ.Kind == KindUnion {
		for _, variant := range typ.Variants {
			if err := e.visit(variant); err != nil {
				return err
			}
		}
		return nil
	}
	for _, field := range typ.Fields {
		if field.Name == "Clone" {
			return fmt.Errorf("generate clone: %s.%s conflicts with generated Clone method", typ.Name, field.Name)
		}
		expr, err := parser.ParseExpr(field.Type)
		if err != nil {
			return fmt.Errorf("generate clone: %s.%s has invalid Go type %q: %w", typ.Name, field.Name, field.Type, err)
		}
		if err := e.visitExpr(expr, typ.Name, field.Name); err != nil {
			return err
		}
	}
	return nil
}

func (e *cloneEmitter) visitExpr(expr ast.Expr, owner, field string) error {
	switch expr := expr.(type) {
	case *ast.Ident:
		if isCloneScalar(expr.Name) {
			return nil
		}
		if e.manual[expr.Name] {
			return nil
		}
		typ, ok := e.types[expr.Name]
		if !ok {
			return fmt.Errorf("generate clone: %s.%s uses unsupported named type %q", owner, field, expr.Name)
		}
		switch typ.Kind {
		case KindEnum:
			return nil
		case KindStruct:
			return e.visit(typ)
		case KindUnion:
			e.unions[typ.Name] = true
			return e.visit(typ)
		default:
			return fmt.Errorf("generate clone: %s.%s uses unsupported %s type %q", owner, field, kindName(typ.Kind), typ.Name)
		}
	case *ast.StarExpr:
		return e.visitExpr(expr.X, owner, field)
	case *ast.ArrayType:
		if expr.Len != nil {
			return fmt.Errorf("generate clone: %s.%s uses unsupported array type", owner, field)
		}
		return e.visitExpr(expr.Elt, owner, field)
	case *ast.MapType:
		key, ok := expr.Key.(*ast.Ident)
		if !ok || key.Name != "string" {
			return fmt.Errorf("generate clone: %s.%s uses unsupported map key type", owner, field)
		}
		return e.visitExpr(expr.Value, owner, field)
	case *ast.SelectorExpr:
		if isRawMessage(expr) {
			return nil
		}
		return fmt.Errorf("generate clone: %s.%s uses unsupported qualified type", owner, field)
	default:
		return fmt.Errorf("generate clone: %s.%s uses unsupported Go type %q", owner, field, fieldType(expr))
	}
}

func (e *cloneEmitter) emitMethod(c *code, typ *Type) {
	e.nextVar = 0
	c.printf("// Clone returns a deep copy of v.\n")
	c.printf("func (v %s) Clone() %s {\n", typ.Name, typ.Name)
	c.line("\tout := v")
	for _, field := range typ.Fields {
		expr, err := parser.ParseExpr(field.Type)
		if err != nil {
			panic(err) // visit parsed every field before emission
		}
		if !e.needsClone(expr) {
			continue
		}
		e.emitAssign(c, expr, "out."+field.Name, "v."+field.Name, 1)
	}
	c.line("\treturn out")
	c.line("}")
}

func (e *cloneEmitter) needsClone(expr ast.Expr) bool {
	switch expr := expr.(type) {
	case *ast.Ident:
		typ := e.types[expr.Name]
		return e.manual[expr.Name] || (typ != nil && typ.Kind != KindEnum)
	case *ast.StarExpr, *ast.ArrayType, *ast.MapType:
		return true
	case *ast.SelectorExpr:
		return isRawMessage(expr)
	default:
		return false
	}
}

func (e *cloneEmitter) emitAssign(c *code, expr ast.Expr, dst, src string, depth int) {
	indent := tabs(depth)
	switch expr := expr.(type) {
	case *ast.Ident:
		typ := e.types[expr.Name]
		switch {
		case typ != nil && typ.Kind == KindUnion:
			c.printf("%s%s = clone%s(%s)\n", indent, dst, typ.Name, src)
		case e.manual[expr.Name] || (typ != nil && typ.Kind == KindStruct):
			c.printf("%s%s = %s.Clone()\n", indent, dst, src)
		default:
			c.printf("%s%s = %s\n", indent, dst, src)
		}
	case *ast.StarExpr:
		c.printf("%sif %s != nil {\n", indent, src)
		c.printf("%s\t%s = new(%s)\n", indent, dst, fieldType(expr.X))
		e.emitAssign(c, expr.X, "(*"+dst+")", "(*"+src+")", depth+1)
		c.printf("%s}\n", indent)
	case *ast.ArrayType:
		c.printf("%sif %s != nil {\n", indent, src)
		c.printf("%s\t%s = make(%s, len(%s))\n", indent, dst, fieldType(expr), src)
		if !e.needsClone(expr.Elt) {
			c.printf("%s\tcopy(%s, %s)\n", indent, dst, src)
			c.printf("%s}\n", indent)
			return
		}
		index := e.variable("i")
		c.printf("%s\tfor %s := range %s {\n", indent, index, src)
		e.emitAssign(c, expr.Elt, dst+"["+index+"]", src+"["+index+"]", depth+2)
		c.printf("%s\t}\n", indent)
		c.printf("%s}\n", indent)
	case *ast.MapType:
		c.printf("%sif %s != nil {\n", indent, src)
		c.printf("%s\t%s = make(%s, len(%s))\n", indent, dst, fieldType(expr), src)
		key := e.variable("key")
		value := e.variable("value")
		c.printf("%s\tfor %s, %s := range %s {\n", indent, key, value, src)
		if !e.needsClone(expr.Value) {
			c.printf("%s\t\t%s[%s] = %s\n", indent, dst, key, value)
			c.printf("%s\t}\n", indent)
			c.printf("%s}\n", indent)
			return
		}
		cloned := e.variable("cloned")
		c.printf("%s\t\tvar %s %s\n", indent, cloned, fieldType(expr.Value))
		e.emitAssign(c, expr.Value, cloned, value, depth+2)
		c.printf("%s\t\t%s[%s] = %s\n", indent, dst, key, cloned)
		c.printf("%s\t}\n", indent)
		c.printf("%s}\n", indent)
	case *ast.SelectorExpr:
		if isRawMessage(expr) {
			c.printf("%sif %s != nil {\n", indent, src)
			c.printf("%s\t%s = make(json.RawMessage, len(%s))\n", indent, dst, src)
			c.printf("%s\tcopy(%s, %s)\n", indent, dst, src)
			c.printf("%s}\n", indent)
		}
	}
}

func (e *cloneEmitter) variable(prefix string) string {
	e.nextVar++
	return fmt.Sprintf("%s%d", prefix, e.nextVar)
}

func isCloneScalar(name string) bool {
	switch name {
	case "bool", "string", "byte", "rune",
		"int", "int8", "int16", "int32", "int64",
		"uint", "uint8", "uint16", "uint32", "uint64", "uintptr",
		"float32", "float64", "complex64", "complex128":
		return true
	default:
		return false
	}
}

func isRawMessage(expr *ast.SelectorExpr) bool {
	pkg, ok := expr.X.(*ast.Ident)
	return ok && pkg.Name == "json" && expr.Sel.Name == "RawMessage"
}

func kindName(kind TypeKind) string {
	switch kind {
	case KindEnum:
		return "enum"
	case KindStruct:
		return "struct"
	case KindUnion:
		return "union"
	default:
		return fmt.Sprintf("kind(%d)", kind)
	}
}

func fieldType(expr ast.Expr) string {
	switch expr := expr.(type) {
	case *ast.Ident:
		return expr.Name
	case *ast.SelectorExpr:
		return fieldType(expr.X) + "." + expr.Sel.Name
	case *ast.StarExpr:
		return "*" + fieldType(expr.X)
	case *ast.ArrayType:
		return "[]" + fieldType(expr.Elt)
	case *ast.MapType:
		return "map[" + fieldType(expr.Key) + "]" + fieldType(expr.Value)
	default:
		return fmt.Sprintf("%T", expr)
	}
}

func tabs(n int) string {
	indent := ""
	for range n {
		indent += "\t"
	}
	return indent
}

// emitUnion writes a schema-derived type switch beside the union interface.
// Both value and pointer variants retain their dynamic representation.
func (e *cloneEmitter) emitUnion(c *code, typ *Type) {
	if !e.unions[typ.Name] {
		return
	}
	c.blank()
	c.printf("func clone%s(value %s) %s {\n", typ.Name, typ.Name, typ.Name)
	c.line("\tswitch v := value.(type) {")
	c.line("\tcase nil:")
	c.line("\t\treturn nil")
	for _, variant := range typ.Variants {
		c.printf("\tcase %s:\n\t\treturn v.Clone()\n", variant.Name)
		c.printf("\tcase *%s:\n", variant.Name)
		c.line("\t\tif v == nil { return v }")
		c.line("\t\tcloned := v.Clone()")
		c.line("\t\treturn &cloned")
	}
	c.line("\tdefault:")
	c.printf("\t\tpanic(fmt.Sprintf(\"herdr: cannot clone unsupported %s implementation %%T\", value))\n", typ.Name)
	c.line("\t}")
	c.line("}")
}
