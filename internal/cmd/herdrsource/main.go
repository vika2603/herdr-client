// Command herdrsource fills in the one thing an upgrade needs that the API
// schema does not carry: which result type each method returns.
//
// The schema names every method and every result type but never states that
// one answers the other, and the generator refuses to run while a method in
// the schema has no entry in schema/method-results.json. A release that adds
// a method therefore stops the regeneration until someone reads the handler.
// This command reads it instead, out of a checkout of herdr at that release,
// and re-checks the entries that already exist against the same sources.
//
// Every method ends up with an entry either way. One the sources settle gets
// the result type they name; one they do not gets an entry accepting any
// result, which generates a wrapper returning the Result interface rather
// than a named response type. Generation is never blocked, and a wrong
// narrowing is never guessed.
//
// Without -apply it only reports. A non-zero exit means something is left for
// a person: an entry that could not be narrowed, one the sources contradict,
// or one herdr's own tests disagree with.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
)

func main() {
	var (
		src       = flag.String("src", "", "path to a herdr checkout at the release being upgraded to")
		schema    = flag.String("schema", "schema/herdr-api.schema.json", "path to the schema snapshot")
		methods   = flag.String("methods", "schema/method-results.json", "path to the method result table")
		apply     = flag.Bool("apply", false, "write the entries that are missing into the table")
		reportOut = flag.String("report", "", "write the Markdown report here instead of stdout")
	)
	flag.Parse()

	if *src == "" {
		fmt.Fprintln(os.Stderr, "herdrsource: -src is required")
		os.Exit(2)
	}

	res, err := run(options{src: *src, schema: *schema, methods: *methods, apply: *apply})
	if err != nil {
		fmt.Fprintln(os.Stderr, "herdrsource:", err)
		os.Exit(2)
	}

	report := res.markdown()
	if *reportOut == "" {
		fmt.Print(report)
	} else if err := os.WriteFile(*reportOut, []byte(report), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "herdrsource:", err)
		os.Exit(2)
	}
	if res.needsAPerson() {
		os.Exit(1)
	}
}

type options struct {
	src, schema, methods string
	apply                bool
}

// entry is one method's row in the result table, in the encoding
// method-results.json uses.
type entry struct {
	Result  string   `json:"result,omitempty"`
	Results []string `json:"results,omitempty"`
	Stream  string   `json:"stream,omitempty"`
}

// anyResult is the entry a method gets when the sources do not settle it. It
// generates a wrapper returning the Result interface, which is correct for
// every method and precise for none.
var anyResult = entry{Results: []string{"*"}}

// allows reports whether the entry permits typ, with "*" standing for any
// result type.
func (e entry) allows(typ string) bool {
	switch {
	case e.Result != "":
		return typ == e.Result
	case e.Stream != "":
		return typ == e.Stream
	default:
		for _, want := range e.Results {
			if want == "*" || want == typ {
				return true
			}
		}
		return false
	}
}

func (e entry) String() string {
	switch {
	case e.Result != "":
		return e.Result
	case e.Stream != "":
		return "stream:" + e.Stream
	default:
		return strings.Join(e.Results, "|")
	}
}

// added is a method written into the table by this run.
type added struct {
	method   string
	written  entry
	evidence []string
	why      reason // why it got the any-result entry, empty when narrowed
	choices  []string
}

// conflict is an entry two readings of the sources disagree about.
type conflict struct {
	method   string
	table    string
	handlers []string
	tests    string
	at       []string
}

type result struct {
	narrowed    []added
	widened     []added
	contradicts []conflict // the handlers contradict the table
	disputed    []conflict // herdr's own tests contradict the handlers
	confirmed   int
	unread      []string
	applied     bool
}

func run(opts options) (*result, error) {
	files, err := rustFiles(opts.src)
	if err != nil {
		return nil, err
	}
	variants, err := methodNames(files)
	if err != nil {
		return nil, err
	}
	schemaMethods, resultTypes, err := readSchema(opts.schema)
	if err != nil {
		return nil, err
	}
	table, text, err := readTable(opts.methods)
	if err != nil {
		return nil, err
	}

	index := functions(files)
	pairs := testPairs(files)
	res := &result{}

	// The enum order is the order method-results.json follows, so walking it
	// gives each new entry a place next to its neighbours.
	var previous string
	for _, v := range variants {
		if !schemaMethods[v.method] {
			continue
		}
		found := resultsFor(v.variant, files, index)
		types := found.results(resultTypes)
		where := found.evidence(resultTypes)

		existing, inTable := table[v.method]
		switch {
		case !inTable:
			record := added{method: v.method, evidence: where}
			if len(types) == 1 {
				record.written = entry{Result: types[0]}
			} else {
				record.written = anyResult
				record.why = found.why
				record.choices = types
				if len(types) > 1 {
					record.why = "its dispatch can encode more than one result"
				}
			}
			if text, err = insertEntry(text, previous, v.method, record.written); err != nil {
				return nil, err
			}
			table[v.method] = record.written
			if record.written.Result != "" {
				res.narrowed = append(res.narrowed, record)
			} else {
				res.widened = append(res.widened, record)
			}
		case len(types) == 0:
			res.unread = append(res.unread, v.method)
		default:
			// A helper shared by two methods encodes both their results, so
			// the entry agrees as long as what it names is among them. An
			// entry the sources no longer encode at all is real drift.
			agrees := false
			for _, typ := range types {
				if existing.allows(typ) {
					agrees = true
				}
			}
			if agrees {
				res.confirmed++
			} else {
				res.contradicts = append(res.contradicts, conflict{
					method: v.method, table: existing.String(), handlers: types, at: where,
				})
			}
		}

		// The tests are a weaker reading than the handlers and never decide
		// an entry, but a disagreement is worth a look: one of the two is
		// reading the wrong thing.
		if p, ok := pairs[v.variant]; ok && len(types) > 0 && !contains(types, p.result) {
			res.disputed = append(res.disputed, conflict{
				method: v.method, handlers: types, tests: p.result,
				at: append(where, p.at.String()),
			})
		}
		previous = v.method
	}

	if opts.apply {
		res.applied = true
		if len(res.narrowed) > 0 || len(res.widened) > 0 {
			if err := os.WriteFile(opts.methods, []byte(text), 0o644); err != nil {
				return nil, err
			}
		}
	}
	return res, nil
}

func contains(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

// needsAPerson reports whether the run left something to settle by hand.
func (r *result) needsAPerson() bool {
	return len(r.widened) > 0 || len(r.contradicts) > 0 || len(r.disputed) > 0
}

func (r *result) markdown() string {
	var b strings.Builder
	verb := "were read out of"
	if !r.applied {
		verb = "read out of"
	}

	if len(r.narrowed) > 0 {
		fmt.Fprintf(&b, "%d method(s) the table lacked %s the handlers:\n\n", len(r.narrowed), verb)
		b.WriteString("| Method | Result | Read from |\n| --- | --- | --- |\n")
		for _, a := range r.narrowed {
			fmt.Fprintf(&b, "| `%s` | `%s` | `%s` |\n", a.method, a.written.Result, strings.Join(a.evidence, "`, `"))
		}
		b.WriteString("\n")
	}

	if len(r.widened) > 0 {
		fmt.Fprintf(&b, "%d method(s) the table lacked could not be narrowed, so each accepts any result and its wrapper returns the `Result` interface:\n\n", len(r.widened))
		for _, a := range r.widened {
			fmt.Fprintf(&b, "- `%s`: %s.", a.method, a.why)
			if len(a.choices) > 0 {
				fmt.Fprintf(&b, " The candidates are `%s`, read from `%s`.", strings.Join(a.choices, "`, `"), strings.Join(a.evidence, "`, `"))
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	if len(r.contradicts) > 0 {
		fmt.Fprintf(&b, "%d entry/entries the handlers contradict:\n\n", len(r.contradicts))
		b.WriteString("| Method | Table says | Handlers encode | Read from |\n| --- | --- | --- | --- |\n")
		for _, c := range r.contradicts {
			fmt.Fprintf(&b, "| `%s` | `%s` | `%s` | `%s` |\n",
				c.method, c.table, strings.Join(c.handlers, "`, `"), strings.Join(c.at, "`, `"))
		}
		b.WriteString("\n")
	}

	if len(r.disputed) > 0 {
		fmt.Fprintf(&b, "%d method(s) where herdr's own tests name a result the handlers do not. A test that sets up with one method and asserts on another reads this way too, so this is a prompt to look, not a finding:\n\n", len(r.disputed))
		b.WriteString("| Method | Handlers encode | A test names | Read from |\n| --- | --- | --- | --- |\n")
		for _, c := range r.disputed {
			fmt.Fprintf(&b, "| `%s` | `%s` | `%s` | `%s` |\n",
				c.method, strings.Join(c.handlers, "`, `"), c.tests, strings.Join(c.at, "`, `"))
		}
		b.WriteString("\n")
	}

	fmt.Fprintf(&b, "%d entry/entries agree with the handlers.", r.confirmed)
	if len(r.unread) > 0 {
		fmt.Fprintf(&b, " %d encode their result where this scan does not reach and stand on `internal/e2e` instead: `%s`.",
			len(r.unread), strings.Join(r.unread, "`, `"))
	}
	b.WriteString("\n")
	return b.String()
}

// readSchema returns the methods and the result types the schema declares.
func readSchema(path string) (map[string]bool, map[string]bool, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	var doc struct {
		Schemas struct {
			Request struct {
				OneOf []struct {
					Properties struct {
						Method struct {
							Const string `json:"const"`
						} `json:"method"`
					} `json:"properties"`
				} `json:"oneOf"`
			} `json:"request"`
			SuccessResponse struct {
				Defs struct {
					ResponseResult struct {
						OneOf []struct {
							Properties struct {
								Type struct {
									Const string `json:"const"`
								} `json:"type"`
							} `json:"properties"`
						} `json:"oneOf"`
					} `json:"ResponseResult"`
				} `json:"$defs"`
			} `json:"success_response"`
		} `json:"schemas"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, nil, fmt.Errorf("%s: %w", path, err)
	}
	methods := make(map[string]bool)
	for _, v := range doc.Schemas.Request.OneOf {
		if v.Properties.Method.Const != "" {
			methods[v.Properties.Method.Const] = true
		}
	}
	types := make(map[string]bool)
	for _, v := range doc.Schemas.SuccessResponse.Defs.ResponseResult.OneOf {
		if v.Properties.Type.Const != "" {
			types[v.Properties.Type.Const] = true
		}
	}
	if len(methods) == 0 || len(types) == 0 {
		return nil, nil, fmt.Errorf("%s: no methods or no result types", path)
	}
	return methods, types, nil
}

// readTable returns the result table and the file's text, which entries are
// inserted into so that the hand-kept order and formatting survive.
func readTable(path string) (map[string]entry, string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, "", err
	}
	var doc struct {
		Methods map[string]entry `json:"methods"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, "", fmt.Errorf("%s: %w", path, err)
	}
	if len(doc.Methods) == 0 {
		return nil, "", fmt.Errorf("%s: no methods", path)
	}
	return doc.Methods, string(raw), nil
}

// insertEntry writes one method into the table text after the entry for
// after, or at the head when after is empty. Rewriting the file from a
// decoded map would sort the methods and drop the comment, so the insertion
// is textual.
func insertEntry(text, after, method string, e entry) (string, error) {
	encoded, err := json.MarshalIndent(e, "    ", "  ")
	if err != nil {
		return "", err
	}
	block := fmt.Sprintf("    %q: %s,\n", method, encoded)

	if after == "" {
		anchor := "\"methods\": {\n"
		at := strings.Index(text, anchor)
		if at < 0 {
			return "", fmt.Errorf("the table has no \"methods\" object")
		}
		return text[:at+len(anchor)] + block + text[at+len(anchor):], nil
	}

	anchor := fmt.Sprintf("    %q: {", after)
	at := strings.Index(text, anchor)
	if at < 0 {
		return "", fmt.Errorf("the table has no entry for %q to insert %q after", after, method)
	}
	_, end, ok := blockAt(text, at+len(anchor)-1)
	if !ok {
		return "", fmt.Errorf("the entry for %q is not closed", after)
	}
	// The entry being inserted after is the last one when no comma follows
	// it, which makes it the one that now needs the comma.
	rest := text[end:]
	if trimmed := strings.TrimLeft(rest, " \t\r\n"); strings.HasPrefix(trimmed, ",") {
		cut := end + strings.Index(rest, ",") + 1
		if nl := strings.IndexByte(text[cut:], '\n'); nl >= 0 {
			cut += nl + 1
		}
		return text[:cut] + block + text[cut:], nil
	}
	block = strings.TrimSuffix(block, ",\n") + "\n"
	return text[:end] + ",\n" + block + text[end:], nil
}
