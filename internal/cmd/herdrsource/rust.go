package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Reading of herdr's Rust sources for the one relation the API schema does
// not carry: which ResponseResult a method's handler encodes on success. The
// schema names every method and every result but never states that one
// answers the other, and the generator refuses to run without it.
//
// This is not a Rust parser. It matches the shapes herdr's dispatch actually
// uses and reports each answer with the file and line it was read from, so a
// mapping it proposes can be checked against the source. Where a shape is not
// one it knows, it says so rather than guessing: an unread method costs a
// wildcard entry, a wrong one costs a broken wrapper.

// location is where a piece of evidence was read.
type location struct {
	file string
	line int
}

func (l location) String() string { return fmt.Sprintf("%s:%d", l.file, l.line) }

// finding is one ResponseResult variant a method's dispatch can encode.
type finding struct {
	variant string // Rust variant name, e.g. PaneLinkResolved
	at      location
	through string // the function it was read in, empty when the dispatch held it
}

// source is one Rust file read into memory.
type source struct {
	path string // path relative to the checkout root
	text string
}

// production is the file with its test modules blanked out, and tests is the
// inverse. Rust keeps unit tests inline behind #[cfg(test)], and a test names
// methods it merely sets up with, which would read as dispatch. Blanking
// rather than cutting keeps every byte offset, so a line number taken from
// either view still points into the original file.
func (s source) production() string { return s.split(false) }
func (s source) tests() string      { return s.split(true) }

func (s source) split(keepTests bool) string {
	out := []byte(s.text)
	blank := func(from, to int) {
		for i := from; i < to; i++ {
			if out[i] != '\n' {
				out[i] = ' '
			}
		}
	}
	if keepTests {
		blank(0, len(out))
	}
	for at := 0; ; {
		found := strings.Index(s.text[at:], "#[cfg(test)]")
		if found < 0 {
			break
		}
		found += at
		open := strings.IndexByte(s.text[found:], '{')
		if open < 0 {
			break
		}
		_, end, ok := blockAt(s.text, found+open)
		if !ok {
			break
		}
		if keepTests {
			copy(out[found:end], s.text[found:end])
		} else {
			blank(found, end)
		}
		at = end
	}
	return string(out)
}

// rustFiles reads every .rs file under root/src.
func rustFiles(root string) ([]source, error) {
	var files []source
	srcDir := filepath.Join(root, "src")
	err := filepath.WalkDir(srcDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".rs") {
			return nil
		}
		text, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files = append(files, source{path: filepath.ToSlash(rel), text: string(text)})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", srcDir, err)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("%s holds no .rs files", srcDir)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].path < files[j].path })
	return files, nil
}

var (
	methodEnumRe    = regexp.MustCompile(`(?m)^pub enum Method\s*\{`)
	methodVariantRe = regexp.MustCompile(`#\[serde\(rename\s*=\s*"([^"]+)"\)\]\s*(?://[^\n]*\n\s*)*([A-Z][A-Za-z0-9]*)\s*\(`)
	fnRe            = regexp.MustCompile(`\bfn\s+([a-z_][A-Za-z0-9_]*)\s*[(<]`)
	callRe          = regexp.MustCompile(`\b([a-z_][A-Za-z0-9_]*)\s*\(`)
	responseRe      = regexp.MustCompile(`\bResponseResult::([A-Z][A-Za-z0-9]*)`)
	methodVariantAt = regexp.MustCompile(`\bMethod::([A-Z][A-Za-z0-9]*)`)
)

// methodVariant pairs a Method variant with the method name it serialises as.
type methodVariant struct {
	variant string
	method  string
}

// methodNames lists the Method variants in declaration order. The enum
// carries an explicit serde rename per variant, which is what the schema's
// method constants are generated from.
func methodNames(files []source) ([]methodVariant, error) {
	for _, file := range files {
		loc := methodEnumRe.FindStringIndex(file.text)
		if loc == nil {
			continue
		}
		body, _, ok := blockAt(file.text, loc[1]-1)
		if !ok {
			return nil, fmt.Errorf("%s: enum Method is not closed", file.path)
		}
		var variants []methodVariant
		for _, m := range methodVariantRe.FindAllStringSubmatch(body, -1) {
			variants = append(variants, methodVariant{variant: m[2], method: m[1]})
		}
		if len(variants) == 0 {
			return nil, fmt.Errorf("%s: enum Method holds no renamed variants", file.path)
		}
		return variants, nil
	}
	return nil, fmt.Errorf("no file declares `pub enum Method`")
}

// function is one Rust function body with the position it starts at.
type function struct {
	name string
	body string
	at   location
}

// functions indexes every function body by name. A name declared in more than
// one file keeps all of them: dispatch reaches handlers by name and the scan
// has no type information to tell two apart.
func functions(files []source) map[string][]function {
	index := make(map[string][]function)
	for _, file := range files {
		text := file.production()
		for _, m := range fnRe.FindAllStringSubmatchIndex(text, -1) {
			name := text[m[2]:m[3]]
			open := strings.IndexByte(text[m[1]-1:], '{')
			if open < 0 {
				continue
			}
			body, _, ok := blockAt(text, m[1]-1+open)
			if !ok {
				continue
			}
			index[name] = append(index[name], function{
				name: name,
				body: body,
				at:   location{file: file.path, line: lineAt(text, m[0])},
			})
		}
	}
	return index
}

// maxDepth is how far past the dispatch the scan follows calls. Handlers
// reach their result through at most a couple of helpers, and stopping at the
// first layer that encodes one keeps a helper's unrelated responses out.
const maxDepth = 4

// reason says why a method was not read, so a gap is a stated one rather
// than a silent miss.
type reason string

const (
	reasonNoDispatch reason = "its dispatch is not a shape this scan reads"
	reasonNoResult   reason = "no ResponseResult is reachable from its dispatch, which is what a deferred handler looks like: the dispatch starts the work and a completion callback answers"
)

// scan is what the sources said about one method.
type scan struct {
	findings []finding
	why      reason // set when findings is empty
}

// results returns the distinct result types, snake_case, in the order found.
func (s scan) results(declared map[string]bool) []string {
	var out []string
	seen := map[string]bool{}
	for _, f := range s.findings {
		typ := snakeCase(f.variant)
		// A variant the schema does not declare as a result type is something
		// else the handler builds, not an answer to the call.
		if !declared[typ] || seen[typ] {
			continue
		}
		seen[typ] = true
		out = append(out, typ)
	}
	return out
}

// evidence returns where each result was read.
func (s scan) evidence(declared map[string]bool) []string {
	var out []string
	seen := map[string]bool{}
	for _, f := range s.findings {
		typ := snakeCase(f.variant)
		if !declared[typ] || seen[typ] {
			continue
		}
		seen[typ] = true
		where := f.at.String()
		if f.through != "" {
			where += " (" + f.through + ")"
		}
		out = append(out, where)
	}
	return out
}

// resultsFor reports the ResponseResult variants the dispatch of one Method
// variant can encode. It starts where the dispatch names the variant and
// follows the calls it makes, layer by layer, stopping at the first layer
// that encodes a result.
func resultsFor(variant string, files []source, index map[string][]function) scan {
	points := dispatchPoints(variant, files)
	if len(points) == 0 {
		return scan{why: reasonNoDispatch}
	}

	var found []finding
	seen := make(map[string]bool)
	add := func(f finding) {
		key := f.variant + "@" + f.at.String()
		if !seen[key] {
			seen[key] = true
			found = append(found, f)
		}
	}

	for _, entry := range points {
		frontier := []function{entry}
		visited := map[string]bool{}
		for depth := 0; depth < maxDepth && len(frontier) > 0; depth++ {
			var hits []finding
			var next []function
			for _, fn := range frontier {
				for _, r := range responseRe.FindAllStringSubmatch(fn.body, -1) {
					through := ""
					if depth > 0 {
						through = fn.name
					}
					hits = append(hits, finding{variant: r[1], at: fn.at, through: through})
				}
				for _, call := range calledFunctions(fn.body) {
					if visited[call] {
						continue
					}
					visited[call] = true
					for _, target := range index[call] {
						target.name = call
						next = append(next, target)
					}
				}
			}
			if len(hits) > 0 {
				for _, hit := range hits {
					add(hit)
				}
				break
			}
			frontier = next
		}
	}
	if len(found) == 0 {
		return scan{why: reasonNoResult}
	}
	sort.Slice(found, func(i, j int) bool {
		if found[i].variant != found[j].variant {
			return found[i].variant < found[j].variant
		}
		return found[i].at.String() < found[j].at.String()
	})
	return scan{findings: found}
}

// dispatchPoints returns the bodies that decide what one Method variant
// answers with: the match arms naming it, the blocks guarded by a `matches!`
// on it, and the `if let` that binds it, which is how an endpoint answers a
// method before the shared dispatch sees it.
func dispatchPoints(variant string, files []source) []function {
	var points []function
	nameRe := regexp.MustCompile(`\bMethod::` + regexp.QuoteMeta(variant) + `\s*\(`)
	for _, file := range files {
		text := file.production()
		for _, m := range nameRe.FindAllStringIndex(text, -1) {
			at := location{file: file.path, line: lineAt(text, m[0])}
			if body, ok := armBody(text, m[1]-1); ok {
				points = append(points, function{body: body, at: at})
				continue
			}
			if body, ok := guardedBlock(text, m[0]); ok {
				points = append(points, function{body: body, at: at})
				continue
			}
			if body, ok := boundBlock(text, m[1]-1); ok {
				points = append(points, function{body: body, at: at})
			}
		}
	}
	return points
}

// calledFunctions lists the functions a body calls, excluding the ones that
// carry a response rather than decide it.
func calledFunctions(body string) []string {
	skip := map[string]bool{
		"encode_success": true, "encode_error": true, "error_response_json": true,
		"to_string": true, "unwrap_or_else": true, "matches": true, "format": true,
		"if": true, "match": true, "return": true, "while": true, "for": true,
	}
	var names []string
	seen := make(map[string]bool)
	for _, m := range callRe.FindAllStringSubmatch(body, -1) {
		name := m[1]
		if skip[name] || seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	return names
}

// armBody returns the body of the match arm whose pattern starts at open,
// which is the `(` of `Method::Variant(`. An arm is either a block or an
// expression ending at the comma that closes it.
func armBody(src string, open int) (string, bool) {
	_, end, ok := groupAt(src, open)
	if !ok {
		return "", false
	}
	rest := src[end:]
	arrow := strings.Index(rest, "=>")
	if arrow < 0 || strings.ContainsAny(strings.TrimSpace(rest[:arrow]), ";{}") {
		return "", false
	}
	start := end + arrow + 2
	for start < len(src) && (src[start] == ' ' || src[start] == '\n' || src[start] == '\t' || src[start] == '\r') {
		start++
	}
	if start >= len(src) {
		return "", false
	}
	if src[start] == '{' {
		body, _, ok := blockAt(src, start)
		return body, ok
	}
	return untilArmComma(src, start)
}

// guardedBlock returns the block that follows the `matches!` call containing
// index, if the variant is named inside one.
func guardedBlock(src string, index int) (string, bool) {
	start := strings.LastIndex(src[:index], "matches!")
	if start < 0 || strings.Contains(src[start:index], "\n\n") {
		return "", false
	}
	open := strings.IndexByte(src[start:], '(')
	if open < 0 {
		return "", false
	}
	_, end, ok := groupAt(src, start+open)
	if !ok || end > len(src) {
		return "", false
	}
	rest := src[end:]
	brace := strings.IndexByte(rest, '{')
	if brace < 0 || strings.ContainsAny(strings.TrimSpace(rest[:brace]), ";=") {
		return "", false
	}
	body, _, ok := blockAt(src, end+brace)
	return body, ok
}

// boundBlock returns the block of an `if let Method::Variant(..) = ..` test.
// open is the `(` of the pattern.
func boundBlock(src string, open int) (string, bool) {
	_, end, ok := groupAt(src, open)
	if !ok {
		return "", false
	}
	rest := src[end:]
	eq := strings.IndexByte(rest, '=')
	if eq < 0 || strings.HasPrefix(rest[eq:], "=>") || strings.ContainsAny(strings.TrimSpace(rest[:eq]), ";{}") {
		return "", false
	}
	brace := strings.IndexByte(rest[eq:], '{')
	if brace < 0 || strings.ContainsAny(strings.TrimSpace(rest[eq+1:eq+brace]), ";}") {
		return "", false
	}
	body, _, ok := blockAt(src, end+eq+brace)
	return body, ok
}

// untilArmComma returns the expression from start up to the comma that ends
// the arm, skipping commas nested in groups and non-code spans.
func untilArmComma(src string, start int) (string, bool) {
	depth := 0
	for i := start; i < len(src); {
		if next, skipped := skipNonCode(src, i); skipped {
			i = next
			continue
		}
		switch src[i] {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			if depth == 0 {
				return src[start:i], true
			}
			depth--
		case ',':
			if depth == 0 {
				return src[start:i], true
			}
		}
		i++
	}
	return "", false
}

// blockAt returns the contents of the brace-delimited block that opens at
// src[open], which must be '{'.
func blockAt(src string, open int) (string, int, bool) {
	return balanced(src, open, '{', '}')
}

// groupAt returns the contents of the parenthesised group that opens at
// src[open], which must be '('.
func groupAt(src string, open int) (string, int, bool) {
	return balanced(src, open, '(', ')')
}

// balanced returns the contents between src[open] and its matching close, and
// the index just past the close. Comments, strings and character literals are
// skipped so that a delimiter inside one does not shift the count.
func balanced(src string, open int, oc, cc byte) (string, int, bool) {
	if open < 0 || open >= len(src) || src[open] != oc {
		return "", 0, false
	}
	depth := 0
	for i := open; i < len(src); {
		if next, skipped := skipNonCode(src, i); skipped {
			i = next
			continue
		}
		switch src[i] {
		case oc:
			depth++
		case cc:
			depth--
			if depth == 0 {
				return src[open+1 : i], i + 1, true
			}
		}
		i++
	}
	return "", 0, false
}

// skipNonCode reports the index just past the comment, string or character
// literal starting at i, if one starts there.
func skipNonCode(src string, i int) (int, bool) {
	switch {
	case strings.HasPrefix(src[i:], "//"):
		if end := strings.IndexByte(src[i:], '\n'); end >= 0 {
			return i + end + 1, true
		}
		return len(src), true
	case strings.HasPrefix(src[i:], "/*"):
		// Rust block comments nest.
		depth, j := 0, i
		for j < len(src) {
			switch {
			case strings.HasPrefix(src[j:], "/*"):
				depth++
				j += 2
			case strings.HasPrefix(src[j:], "*/"):
				depth--
				j += 2
				if depth == 0 {
					return j, true
				}
			default:
				j++
			}
		}
		return len(src), true
	case src[i] == 'r' && (strings.HasPrefix(src[i:], `r"`) || strings.HasPrefix(src[i:], "r#")):
		return skipRawString(src, i)
	case src[i] == '"':
		return skipString(src, i), true
	case src[i] == '\'':
		return skipChar(src, i)
	}
	return i, false
}

func skipString(src string, i int) int {
	for j := i + 1; j < len(src); j++ {
		switch src[j] {
		case '\\':
			j++
		case '"':
			return j + 1
		}
	}
	return len(src)
}

// skipRawString handles r"...", r#"..."# and further hash counts.
func skipRawString(src string, i int) (int, bool) {
	j := i + 1
	hashes := 0
	for j < len(src) && src[j] == '#' {
		hashes++
		j++
	}
	if j >= len(src) || src[j] != '"' {
		return i, false
	}
	closer := `"` + strings.Repeat("#", hashes)
	if end := strings.Index(src[j+1:], closer); end >= 0 {
		return j + 1 + end + len(closer), true
	}
	return len(src), true
}

// skipChar distinguishes a character literal from a lifetime, which shares
// the quote and has no closing one.
func skipChar(src string, i int) (int, bool) {
	rest := src[i+1:]
	if strings.HasPrefix(rest, `\`) {
		if end := strings.IndexByte(rest, '\''); end >= 0 && end <= 5 {
			return i + 1 + end + 1, true
		}
		return i, false
	}
	for width := 1; width <= 4 && width < len(rest); width++ {
		if rest[width] == '\'' {
			return i + 1 + width + 1, true
		}
	}
	return i, false
}

// lineAt is the 1-based line number of the byte at index.
func lineAt(src string, index int) int {
	return strings.Count(src[:index], "\n") + 1
}

// snakeCase converts a Rust variant name to the serde snake_case rendering
// ResponseResult declares, which is what the schema records as the result
// type.
func snakeCase(variant string) string {
	var b strings.Builder
	for i, r := range variant {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				b.WriteByte('_')
			}
			b.WriteRune(r + ('a' - 'A'))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
