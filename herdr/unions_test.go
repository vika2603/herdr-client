package herdr

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// roundTrip encodes value, decodes the result into a fresh value of the same
// type and returns the encoded form.
func roundTrip[T any](t *testing.T, value T) (T, string) {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded T
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("Unmarshal %s: %v", encoded, err)
	}
	return decoded, string(encoded)
}

func TestLayoutNodeRoundTrip(t *testing.T) {
	params := LayoutApplyParams{
		Root: LayoutNodeSplit{
			Direction: SplitDirectionRight,
			Ratio:     0.5,
			First:     LayoutNodePane{Label: Some("editor"), Command: Some([]string{"nvim"})},
			Second: LayoutNodeSplit{
				Direction: SplitDirectionDown,
				Ratio:     0.25,
				First:     LayoutNodePane{},
				Second:    LayoutNodePane{},
			},
		},
	}
	decoded, encoded := roundTrip(t, params)
	for _, want := range []string{`"type":"split"`, `"type":"pane"`, `"direction":"right"`} {
		if !strings.Contains(encoded, want) {
			t.Errorf("encoded form %s does not contain %s", encoded, want)
		}
	}
	root, ok := decoded.Root.(LayoutNodeSplit)
	if !ok {
		t.Fatalf("root is %T, want LayoutNodeSplit", decoded.Root)
	}
	pane, ok := root.First.(LayoutNodePane)
	if !ok {
		t.Fatalf("first child is %T, want LayoutNodePane", root.First)
	}
	if pane.Label.ValueOrZero() != "editor" {
		t.Errorf("label = %v, want editor", pane.Label)
	}
	nested, ok := root.Second.(LayoutNodeSplit)
	if !ok {
		t.Fatalf("second child is %T, want LayoutNodeSplit", root.Second)
	}
	if nested.Ratio != 0.25 {
		t.Errorf("nested ratio = %v, want 0.25", nested.Ratio)
	}
}

func TestSubscriptionListRoundTrip(t *testing.T) {
	params := EventsSubscribeParams{
		Subscriptions: []Subscription{
			PaneCreatedSubscription{},
			PaneOutputMatchedSubscription{
				PaneID: "w1:p1",
				Source: ReadSourceRecent,
				Match:  OutputMatchRegex{Value: "^done"},
			},
		},
	}
	decoded, encoded := roundTrip(t, params)
	for _, want := range []string{`"type":"pane.created"`, `"type":"pane.output_matched"`, `"type":"regex"`} {
		if !strings.Contains(encoded, want) {
			t.Errorf("encoded form %s does not contain %s", encoded, want)
		}
	}
	if len(decoded.Subscriptions) != 2 {
		t.Fatalf("decoded %d subscriptions, want 2", len(decoded.Subscriptions))
	}
	if _, ok := decoded.Subscriptions[0].(PaneCreatedSubscription); !ok {
		t.Errorf("first subscription is %T", decoded.Subscriptions[0])
	}
	output, ok := decoded.Subscriptions[1].(PaneOutputMatchedSubscription)
	if !ok {
		t.Fatalf("second subscription is %T", decoded.Subscriptions[1])
	}
	match, ok := output.Match.(OutputMatchRegex)
	if !ok {
		t.Fatalf("match is %T, want OutputMatchRegex", output.Match)
	}
	if match.Value != "^done" {
		t.Errorf("match value = %q", match.Value)
	}
}

func TestEventMatchAndDestinationRoundTrip(t *testing.T) {
	wait, encoded := roundTrip(t, EventsWaitParams{MatchEvent: EventMatchPaneClosed{PaneID: "w1:p1"}})
	if !strings.Contains(encoded, `"event":"pane_closed"`) {
		t.Errorf("encoded form %s has no event discriminator", encoded)
	}
	closed, ok := wait.MatchEvent.(EventMatchPaneClosed)
	if !ok {
		t.Fatalf("match is %T, want EventMatchPaneClosed", wait.MatchEvent)
	}
	if closed.PaneID != "w1:p1" {
		t.Errorf("pane id = %q", closed.PaneID)
	}

	move, encoded := roundTrip(t, PaneMoveParams{
		PaneID:      "w1:p1",
		Destination: PaneMoveDestinationNewTab{Label: Some("tests")},
	})
	if !strings.Contains(encoded, `"type":"new_tab"`) {
		t.Errorf("encoded form %s has no type discriminator", encoded)
	}
	if _, ok := move.Destination.(PaneMoveDestinationNewTab); !ok {
		t.Errorf("destination is %T", move.Destination)
	}
}

func TestOptionalUnionFieldStaysAbsent(t *testing.T) {
	decoded, encoded := roundTrip(t, AgentViewSetParams{Source: "plugin:demo"})
	if strings.Contains(encoded, "filter") {
		t.Errorf("encoded form %s should not carry an empty filter", encoded)
	}
	if decoded.Filter.IsSet() {
		t.Errorf("filter = %+v, want absent", decoded.Filter)
	}

	filtered, encoded := roundTrip(t, AgentViewSetParams{
		Source: "plugin:demo",
		Filter: Some[AgentViewFilter](AgentViewFilterNot{
			Filter: AgentViewFilterEq{
				Field: AgentViewFieldOf(AgentViewBuiltinFieldStatus),
				Value: AgentViewText(string(AgentStatusWorking)),
			},
		}),
		Sort: Some([]AgentViewSort{{Field: AgentViewSortFieldToken("weight"), Order: Some(AgentViewSortOrderDesc)}}),
	})
	if !strings.Contains(encoded, `"op":"not"`) || !strings.Contains(encoded, `"op":"eq"`) {
		t.Errorf("encoded form %s is missing an op discriminator", encoded)
	}
	filter, ok := filtered.Filter.Get()
	if !ok {
		t.Fatal("filter is absent")
	}
	not, ok := filter.(AgentViewFilterNot)
	if !ok {
		t.Fatalf("filter is %T, want AgentViewFilterNot", filter)
	}
	eq, ok := not.Filter.(AgentViewFilterEq)
	if !ok {
		t.Fatalf("inner filter is %T, want AgentViewFilterEq", not.Filter)
	}
	if eq.Field.Builtin != AgentViewBuiltinFieldStatus {
		t.Errorf("field = %+v", eq.Field)
	}
	if eq.Value.Text == nil || *eq.Value.Text != "working" {
		t.Errorf("value = %+v", eq.Value)
	}
	sort := filtered.Sort.ValueOrZero()
	if len(sort) != 1 || sort[0].Field.Token != "weight" {
		t.Errorf("sort = %+v", filtered.Sort)
	}
}

func TestUnknownUnionVariantIsRejected(t *testing.T) {
	var params LayoutApplyParams
	err := json.Unmarshal([]byte(`{"root":{"type":"grid"}}`), &params)
	if err == nil || !strings.Contains(err.Error(), "unknown LayoutNode") {
		t.Fatalf("error = %v, want an unknown LayoutNode error", err)
	}
}

// A value and a pointer both satisfy a union interface and encode alike, but
// only the value form is what decoding produces, so a type switch written
// against what the caller built also matches what a response carries.
func TestUnionValueAndPointerImplementTheInterface(t *testing.T) {
	var nodes []LayoutNode
	nodes = append(nodes, LayoutNodePane{}, &LayoutNodePane{})
	if len(nodes) != 2 {
		t.Fatal("both forms must satisfy LayoutNode")
	}
	var decoded LayoutApplyParams
	if err := json.Unmarshal([]byte(`{"root":{"type":"pane"}}`), &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if _, ok := decoded.Root.(LayoutNodePane); !ok {
		t.Errorf("decoded root is %T, want the value form LayoutNodePane", decoded.Root)
	}
	encodedValue, err := json.Marshal(nodes[0])
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	encodedPointer, err := json.Marshal(nodes[1])
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !reflect.DeepEqual(encodedValue, encodedPointer) {
		t.Errorf("value encodes as %s, pointer as %s", encodedValue, encodedPointer)
	}
}
