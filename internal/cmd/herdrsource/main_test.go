package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const tableText = `{
  "_comment": "Result type per request method.",
  "herdr_version": "0.9.0",
  "methods": {
    "ping": {
      "result": "pong"
    },
    "pane.link.activate": {
      "result": "pane_link_activated"
    },
    "tab.close": {
      "result": "ok"
    }
  }
}
`

// An inserted entry has to land next to the method it follows in the enum,
// and leave a file the table still parses with its comment intact.
func TestInsertEntryKeepsTheOrderAndTheShape(t *testing.T) {
	for _, tc := range []struct {
		name, after, before string
	}{
		{name: "in the middle", after: "pane.link.activate", before: "tab.close"},
		{name: "at the end", after: "tab.close"},
		{name: "at the head", before: "ping"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := insertEntry(tableText, tc.after, "pane.link.resolve", entry{Result: "pane_link_resolved"})
			if err != nil {
				t.Fatal(err)
			}
			var doc struct {
				Comment string           `json:"_comment"`
				Methods map[string]entry `json:"methods"`
			}
			if err := json.Unmarshal([]byte(got), &doc); err != nil {
				t.Fatalf("the table no longer parses: %v\n%s", err, got)
			}
			if doc.Comment == "" {
				t.Error("the comment was dropped")
			}
			if doc.Methods["pane.link.resolve"].Result != "pane_link_resolved" {
				t.Errorf("the entry is %+v", doc.Methods["pane.link.resolve"])
			}
			if len(doc.Methods) != 4 {
				t.Errorf("the table holds %d methods, want 4", len(doc.Methods))
			}
			at := strings.Index(got, `"pane.link.resolve"`)
			if tc.after != "" && at < strings.Index(got, `"`+tc.after+`"`) {
				t.Errorf("the entry landed before %q", tc.after)
			}
			if tc.before != "" && at > strings.Index(got, `"`+tc.before+`"`) {
				t.Errorf("the entry landed after %q", tc.before)
			}
		})
	}
}

// files writes the three inputs a run takes and returns their paths.
func files(t *testing.T, schema, methods string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	write := func(name, content string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	return write("schema.json", schema), write("method-results.json", methods)
}

// tableWith renders the table the way the project keeps it, which is the
// format insertEntry anchors on.
func tableWith(entries ...[2]string) string {
	var b strings.Builder
	b.WriteString("{\n  \"_comment\": \"Result type per request method.\",\n  \"methods\": {\n")
	for i, e := range entries {
		comma := ","
		if i == len(entries)-1 {
			comma = ""
		}
		fmt.Fprintf(&b, "    %q: {\n      \"result\": %q\n    }%s\n", e[0], e[1], comma)
	}
	b.WriteString("  }\n}\n")
	return b.String()
}

func schemaWith(methods, results []string) string {
	var m, r []string
	for _, name := range methods {
		m = append(m, `{"properties": {"method": {"const": "`+name+`"}}}`)
	}
	for _, name := range results {
		r = append(r, `{"properties": {"type": {"const": "`+name+`"}}}`)
	}
	return `{"schemas": {
      "request": {"oneOf": [` + strings.Join(m, ",") + `]},
      "success_response": {"$defs": {"ResponseResult": {"oneOf": [` + strings.Join(r, ",") + `]}}}
    }}`
}

// The schema declaring a method the table lacks is what stops the generator,
// and settling it from the sources is the whole job.
func TestRunNarrowsAMethodItCanRead(t *testing.T) {
	root := checkout(t, map[string]string{"api/schema.rs": dispatchSource})
	schema, methods := files(t,
		schemaWith([]string{"ping", "tab.close", "pane.link.resolve"}, []string{"pong", "ok", "pane_link_resolved"}),
		tableWith([2]string{"ping", "pong"}, [2]string{"tab.close", "ok"}))

	res, err := run(options{src: root, schema: schema, methods: methods, apply: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.narrowed) != 1 || res.narrowed[0].method != "pane.link.resolve" {
		t.Fatalf("narrowed %+v, want pane.link.resolve", res.narrowed)
	}
	if got := res.narrowed[0].written.Result; got != "pane_link_resolved" {
		t.Errorf("wrote %q, want pane_link_resolved", got)
	}
	if !strings.Contains(res.narrowed[0].evidence[0], "api/schema.rs:") {
		t.Errorf("the evidence is %q, want a file and line", res.narrowed[0].evidence[0])
	}

	var doc struct {
		Methods map[string]entry `json:"methods"`
	}
	written, err := os.ReadFile(methods)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(written, &doc); err != nil {
		t.Fatalf("the table no longer parses: %v", err)
	}
	if doc.Methods["pane.link.resolve"].Result != "pane_link_resolved" {
		t.Errorf("the table holds %+v", doc.Methods["pane.link.resolve"])
	}
}

// A method the sources do not settle still gets an entry, because a missing
// one stops the generator from producing anything at all.
func TestRunWidensAMethodItCannotRead(t *testing.T) {
	root := checkout(t, map[string]string{"api/schema.rs": dispatchSource})
	schema, methods := files(t,
		schemaWith([]string{"tab.close", "worktree.create"}, []string{"ok", "worktree_created"}),
		tableWith([2]string{"tab.close", "ok"}))

	res, err := run(options{src: root, schema: schema, methods: methods, apply: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.widened) != 1 || res.widened[0].method != "worktree.create" {
		t.Fatalf("widened %+v, want worktree.create", res.widened)
	}
	if got := res.widened[0].written.String(); got != "*" {
		t.Errorf("wrote %q, want the any-result entry", got)
	}
	if res.widened[0].why != reasonNoResult {
		t.Errorf("the reason given is %q", res.widened[0].why)
	}
	if !res.needsAPerson() {
		t.Error("a widened entry was not reported as needing a person")
	}
	if !strings.Contains(res.markdown(), "`Result` interface") {
		t.Error("the report does not say what the widened entry costs")
	}
}

// An entry the sources contradict is drift worth catching, and it must not
// be written over.
func TestRunReportsAnEntryTheHandlersContradict(t *testing.T) {
	root := checkout(t, map[string]string{"api/schema.rs": dispatchSource})
	schema, methods := files(t,
		schemaWith([]string{"tab.close"}, []string{"ok", "tab_closed"}),
		tableWith([2]string{"tab.close", "tab_closed"}))

	res, err := run(options{src: root, schema: schema, methods: methods, apply: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.contradicts) != 1 || res.contradicts[0].method != "tab.close" {
		t.Fatalf("reported %+v, want tab.close", res.contradicts)
	}
	if res.contradicts[0].table != "tab_closed" || res.contradicts[0].handlers[0] != "ok" {
		t.Errorf("reported %+v, want the table's tab_closed against the source's ok", res.contradicts[0])
	}
	written, err := os.ReadFile(methods)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(written), "tab_closed") {
		t.Error("the contradicted entry was overwritten")
	}
}

func TestRunRejectsSchemaMethodsMissingFromSource(t *testing.T) {
	root := checkout(t, map[string]string{"api/schema.rs": dispatchSource})
	schema, methods := files(t,
		schemaWith([]string{"ping", "pane.new_method"}, []string{"pong", "ok"}),
		tableWith([2]string{"ping", "pong"}))

	_, err := run(options{src: root, schema: schema, methods: methods, apply: true})
	if err == nil || !strings.Contains(err.Error(), "schema methods absent from the source Method enum: pane.new_method") {
		t.Fatalf("run error = %v, want the missing schema method", err)
	}
	written, err := os.ReadFile(methods)
	if err != nil {
		t.Fatal(err)
	}
	if string(written) != tableWith([2]string{"ping", "pong"}) {
		t.Error("failed validation changed the table")
	}
}

func TestRunRejectsTableMethodsAbsentFromSchema(t *testing.T) {
	root := checkout(t, map[string]string{"api/schema.rs": dispatchSource})
	schema, methods := files(t,
		schemaWith([]string{"ping"}, []string{"pong", "ok"}),
		tableWith([2]string{"ping", "pong"}, [2]string{"old.method", "ok"}))

	_, err := run(options{src: root, schema: schema, methods: methods, apply: true})
	if err == nil || !strings.Contains(err.Error(), "method table entries absent from the schema: old.method") {
		t.Fatalf("run error = %v, want the obsolete table entry", err)
	}
}

func TestRunReportsPartiallyCoveredResults(t *testing.T) {
	root := checkout(t, map[string]string{"api/schema.rs": `
pub enum Method {
    #[serde(rename = "tab.close")]
    TabClose(TabTarget),
}
fn dispatch(request: Request) -> String {
    match request.method {
        Method::TabClose(_) => {
            if request.is_empty() {
                return encode_success(request.id, ResponseResult::Ok {});
            }
            encode_success(request.id, ResponseResult::TabClosed {})
        }
    }
}
`})
	schema, methods := files(t,
		schemaWith([]string{"tab.close"}, []string{"ok", "tab_closed"}),
		tableWith([2]string{"tab.close", "ok"}))

	res, err := run(options{src: root, schema: schema, methods: methods})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.partial) != 1 || res.partial[0].method != "tab.close" || res.confirmed != 0 {
		t.Fatalf("partial = %+v, confirmed = %d; want the unproven table entry reported", res.partial, res.confirmed)
	}
	if !strings.Contains(res.markdown(), "`tab_closed`") {
		t.Error("the report omitted the additional result candidate")
	}
	if res.needsAPerson() {
		t.Error("a shared handler is not by itself proof the typed entry is wrong")
	}
}

// A test pairing is the weaker reading and never decides an entry, but a
// disagreement with the handlers is worth surfacing.
func TestRunSurfacesATestThatDisagrees(t *testing.T) {
	root := checkout(t, map[string]string{"api/schema.rs": dispatchSource})
	schema, methods := files(t,
		schemaWith([]string{"tab.close"}, []string{"ok", "workspace_created"}),
		tableWith([2]string{"tab.close", "ok"}))

	res, err := run(options{src: root, schema: schema, methods: methods})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.disputed) != 1 || res.disputed[0].method != "tab.close" {
		t.Fatalf("reported %+v, want tab.close disputed", res.disputed)
	}
	if res.disputed[0].tests != "workspace_created" || res.disputed[0].handlers[0] != "ok" {
		t.Errorf("reported %+v", res.disputed[0])
	}
	if res.confirmed != 1 {
		t.Errorf("the entry was not confirmed against the handlers despite the dispute")
	}
	if res.needsAPerson() {
		t.Error("a weak test pairing should not block a release")
	}
}

func TestBlockingFindingsStillRequireReview(t *testing.T) {
	for _, res := range []*result{
		{widened: []added{{method: "new.method"}}},
		{contradicts: []conflict{{method: "old.method"}}},
	} {
		if !res.needsAPerson() {
			t.Errorf("%+v did not request review", res)
		}
	}
}
