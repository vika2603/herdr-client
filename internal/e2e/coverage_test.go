//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

var (
	schemaPath  = filepath.Join("..", "..", "schema", "herdr-api.schema.json")
	methodsPath = filepath.Join("..", "..", "schema", "method-results.json")
)

// outOfReach lists the methods the suite cannot call, with the reason. Every
// entry must name a method the schema still declares; the coverage check
// fails when one disappears, so the list cannot drift away from the schema.
var outOfReach = map[string]string{
	"server.live_handoff":          "hands the running server to a replacement process for an in-place update, which would tear down the server the suite owns",
	"client_shell.surface.set":     "only a client shell endpoint may call it; a socket client is rejected with connection_local_only",
	"command.invoke":               "needs a command_id issued by a client shell projection; without one the server answers command_not_found",
	"product_announcement.dismiss": "needs an announcement the server holds as current; a fresh server answers stale_announcement",
	"release_notes.dismiss":        "needs release notes the server holds as current; a fresh server answers stale_release_notes",
	"popup.close":                  "needs an open popup, which only an attached client can open; the server answers popup_not_open",
	"agent.start":                  "spawns one of the supported agent CLIs in a pane and waits for it to be detected; the suite starts no real agent",
	"agent.prompt":                 "needs the agent process in the pane foreground; a shell pane is rejected with agent_not_ready",
	"agent.send_keys":              "needs an agent started through agent.start; a pane-reported agent is rejected with agent_not_ready",
}

// expectation is one entry of schema/method-results.json.
type expectation struct {
	Result  string   `json:"result"`
	Results []string `json:"results"`
	Stream  string   `json:"stream"`
}

// allows reports whether typ is a result type the table permits for the
// method. "*" in Results stands for any variant.
func (e expectation) allows(typ string) bool {
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

func (e expectation) String() string {
	switch {
	case e.Result != "":
		return e.Result
	case e.Stream != "":
		return "stream:" + e.Stream
	default:
		return strings.Join(e.Results, "|")
	}
}

// schemaMethods returns every method name the schema declares.
func schemaMethods() ([]string, error) {
	raw, err := os.ReadFile(schemaPath)
	if err != nil {
		return nil, err
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
		} `json:"schemas"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("%s: %w", schemaPath, err)
	}
	methods := make([]string, 0, len(doc.Schemas.Request.OneOf))
	for _, variant := range doc.Schemas.Request.OneOf {
		name := variant.Properties.Method.Const
		if name == "" {
			return nil, fmt.Errorf("%s: request variant without a method constant", schemaPath)
		}
		methods = append(methods, name)
	}
	if len(methods) == 0 {
		return nil, fmt.Errorf("%s: no request methods", schemaPath)
	}
	sort.Strings(methods)
	return methods, nil
}

// methodResults returns the result type table the wrappers were generated
// from.
func methodResults() (map[string]expectation, error) {
	raw, err := os.ReadFile(methodsPath)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Methods map[string]expectation `json:"methods"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("%s: %w", methodsPath, err)
	}
	if len(doc.Methods) == 0 {
		return nil, fmt.Errorf("%s: no methods", methodsPath)
	}
	return doc.Methods, nil
}

// call records one exercised method: the result type the server answered and
// the request id it answered under.
type call struct {
	requestID  string
	resultType string
}

// disagreement is a method whose response does not match
// schema/method-results.json.
type disagreement struct {
	method    string
	requestID string
	want      string
	got       string
	response  string
}

func (d disagreement) String() string {
	return fmt.Sprintf("%s (request %s): expected %s, got %s: %s",
		d.method, d.requestID, d.want, d.got, d.response)
}

// recorder collects what the suite exercised. Calls from the trigger
// goroutines make it concurrent.
type recorder struct {
	mu            sync.Mutex
	calls         map[string]call
	disagreements []disagreement
}

func newRecorder() *recorder {
	return &recorder{calls: make(map[string]call)}
}

func (r *recorder) record(method string, c call) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls[method] = c
}

func (r *recorder) reportDisagreement(d disagreement) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.disagreements = append(r.disagreements, d)
}

func (r *recorder) covered() map[string]call {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]call, len(r.calls))
	for method, c := range r.calls {
		out[method] = c
	}
	return out
}

func (r *recorder) findings() []disagreement {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]disagreement(nil), r.disagreements...)
}

// problems returns every inconsistency between the schema, the out of reach
// list and what the suite exercised.
func problems(methods []string, covered map[string]call) []string {
	declared := make(map[string]bool, len(methods))
	for _, method := range methods {
		declared[method] = true
	}
	var out []string
	for _, method := range methods {
		_, isCovered := covered[method]
		_, isOutOfReach := outOfReach[method]
		switch {
		case isCovered && isOutOfReach:
			out = append(out, method+": exercised although listed as out of reach")
		case !isCovered && !isOutOfReach:
			out = append(out, method+": neither exercised nor listed as out of reach")
		}
	}
	for method := range outOfReach {
		if !declared[method] {
			out = append(out, method+": listed as out of reach but no longer declared by the schema")
		}
	}
	for method := range covered {
		if !declared[method] {
			out = append(out, method+": exercised but no longer declared by the schema")
		}
	}
	sort.Strings(out)
	return out
}

// report renders the coverage of the methods the schema declares, the reason
// each remaining method is out of reach, and every disagreement with
// schema/method-results.json.
func report(methods []string, covered map[string]call, findings []disagreement, serverVersion string, protocol uint32) string {
	var out strings.Builder
	fmt.Fprintf(&out, "\ne2e coverage: %d of %d methods exercised against herdr %s (protocol %d)\n\n",
		len(covered), len(methods), serverVersion, protocol)

	fmt.Fprintf(&out, "exercised (%d):\n", len(covered))
	for _, method := range methods {
		if c, ok := covered[method]; ok {
			fmt.Fprintf(&out, "  %-30s %-26s %s\n", method, c.resultType, c.requestID)
		}
	}

	var unreached []string
	for _, method := range methods {
		if _, ok := covered[method]; !ok {
			unreached = append(unreached, method)
		}
	}
	fmt.Fprintf(&out, "\nout of reach (%d):\n", len(unreached))
	for _, method := range unreached {
		reason, ok := outOfReach[method]
		if !ok {
			reason = "NO REASON RECORDED"
		}
		fmt.Fprintf(&out, "  %-30s %s\n", method, reason)
	}

	fmt.Fprintf(&out, "\ndisagreements with schema/method-results.json (%d):\n", len(findings))
	for _, finding := range findings {
		fmt.Fprintf(&out, "  %s\n", finding)
	}
	out.WriteString("\n")
	return out.String()
}
