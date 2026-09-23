package herdr

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

// replyWith starts a fake server that answers every request with result.
func replyWith(t *testing.T, result string) *fakeServer {
	t.Helper()
	return newFakeServer(t, func(s *fakeSession) {
		s.success(json.RawMessage(result))
	})
}

func TestWrapperWithoutParams(t *testing.T) {
	server := replyWith(t, `{"type":"pong","version":"0.9.0","protocol":22}`)
	pong, err := New(server.path).Ping(context.Background())
	if err != nil {
		t.Fatalf("Ping: %v", err)
	}
	if pong.Version != "0.9.0" || pong.Protocol != 22 {
		t.Errorf("pong = %+v", pong)
	}
	request := server.request(0)
	if request.Method != MethodPing {
		t.Errorf("method = %q, want %q", request.Method, MethodPing)
	}
	if string(request.Params) != "{}" {
		t.Errorf("params = %s, want {}", request.Params)
	}
}

func TestWrapperSendsParams(t *testing.T) {
	server := replyWith(t, `{"type":"pane_info","pane":{"pane_id":"w1:p1","terminal_id":"t","workspace_id":"w1","tab_id":"w1:t1","focused":true,"revision":3}}`)
	info, err := New(server.path).PaneGet(context.Background(), PaneTarget{PaneID: "w1:p1"})
	if err != nil {
		t.Fatalf("PaneGet: %v", err)
	}
	if info.Pane.PaneID != "w1:p1" || !info.Pane.Focused {
		t.Errorf("pane = %+v", info.Pane)
	}
	request := server.request(0)
	if request.Method != MethodPaneGet {
		t.Errorf("method = %q, want %q", request.Method, MethodPaneGet)
	}
	if string(request.Params) != `{"pane_id":"w1:p1"}` {
		t.Errorf("params = %s", request.Params)
	}
}

func TestPaneLinkResolveWrapper(t *testing.T) {
	server := replyWith(t, `{"type":"pane_link_resolved","regions":[{"row":2,"start_col":3,"end_col":8}]}`)
	params := PaneLinkActivateParams{PaneID: "w1:p1", ViewportRow: 2, Col: 3}
	resolved, err := New(server.path).PaneLinkResolve(context.Background(), params)
	if err != nil {
		t.Fatalf("PaneLinkResolve: %v", err)
	}
	if len(resolved.Regions) != 1 || resolved.Regions[0] != (PaneLinkRegion{Row: 2, StartCol: 3, EndCol: 8}) {
		t.Errorf("regions = %+v", resolved.Regions)
	}
	cloned := resolved.Clone()
	cloned.Regions[0].StartCol = 4
	if resolved.Regions[0].StartCol != 3 {
		t.Error("cloned regions share storage with the original response")
	}
	request := server.request(0)
	if request.Method != MethodPaneLinkResolve {
		t.Errorf("method = %q, want %q", request.Method, MethodPaneLinkResolve)
	}
	if string(request.Params) != `{"col":3,"pane_id":"w1:p1","viewport_row":2}` {
		t.Errorf("params = %s", request.Params)
	}
}

func TestPaneLinkResolveRejectsUnexpectedResult(t *testing.T) {
	server := replyWith(t, `{"type":"pane_link_activated","handled":false}`)
	_, err := New(server.path).PaneLinkResolve(context.Background(), PaneLinkActivateParams{PaneID: "w1:p1"})
	var unexpected *UnexpectedResultError
	if !errors.As(err, &unexpected) {
		t.Fatalf("error = %v (%T), want *UnexpectedResultError", err, err)
	}
	if unexpected.Method != MethodPaneLinkResolve || unexpected.Want != "pane_link_resolved" || unexpected.Got != "pane_link_activated" {
		t.Errorf("error = %+v", unexpected)
	}
}

func TestWrapperReportsUnexpectedResult(t *testing.T) {
	server := replyWith(t, `{"type":"ok"}`)
	_, err := New(server.path).PaneGet(context.Background(), PaneTarget{PaneID: "w1:p1"})
	var unexpected *UnexpectedResultError
	if !errors.As(err, &unexpected) {
		t.Fatalf("error = %v (%T), want *UnexpectedResultError", err, err)
	}
	if unexpected.Method != MethodPaneGet || unexpected.Want != "pane_info" || unexpected.Got != "ok" {
		t.Errorf("error = %+v", unexpected)
	}
}

func TestWrapperReturnsServerError(t *testing.T) {
	server := newFakeServer(t, func(s *fakeSession) {
		s.fail(ErrCodePaneNotFound, "no such pane")
	})
	_, err := New(server.path).PaneGet(context.Background(), PaneTarget{PaneID: "w1:p9"})
	if !IsCode(err, ErrCodePaneNotFound) {
		t.Fatalf("error = %v, want %s", err, ErrCodePaneNotFound)
	}
}

func TestMultiResultWrapper(t *testing.T) {
	server := replyWith(t, `{"type":"ok"}`)
	result, err := New(server.path).PluginPaneOpen(context.Background(), PluginPaneOpenParams{
		PluginID:   "demo",
		Entrypoint: "main",
	})
	if err != nil {
		t.Fatalf("PluginPaneOpen: %v", err)
	}
	if _, ok := result.(*OKResponse); !ok {
		t.Errorf("result is %T, want *OKResponse", result)
	}
	if result.ResultType() != "ok" {
		t.Errorf("result type = %q, want ok", result.ResultType())
	}
}

func TestStreamWrapper(t *testing.T) {
	server, lines := newStreamServer(t, subscriptionStarted)
	stream, err := New(server.path).EventsSubscribe(context.Background(), EventsSubscribeParams{
		Subscriptions: []Subscription{PaneCreatedSubscription{}},
	})
	if err != nil {
		t.Fatalf("EventsSubscribe: %v", err)
	}
	defer func() { _ = stream.Close() }()

	if request := server.request(0); request.Method != MethodEventsSubscribe {
		t.Errorf("method = %q, want %q", request.Method, MethodEventsSubscribe)
	} else if string(request.Params) != `{"subscriptions":[{"type":"pane.created"}]}` {
		t.Errorf("params = %s", request.Params)
	}

	lines <- `{"event":"pane_created","data":{"type":"pane_created","pane":{"pane_id":"w1:p1","terminal_id":"t","workspace_id":"w1","tab_id":"w1:t1","focused":false,"revision":1}}}`
	event, err := stream.NextEvent(context.Background())
	if err != nil {
		t.Fatalf("NextEvent: %v", err)
	}
	created, ok := event.(*PaneCreatedEvent)
	if !ok {
		t.Fatalf("event is %T, want *PaneCreatedEvent", event)
	}
	if created.Pane.PaneID != "w1:p1" {
		t.Errorf("pane = %+v", created.Pane)
	}
}
