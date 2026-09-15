package herdr

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"testing"
	"time"
)

const pongResult = `{"type":"pong","version":"0.9.0","protocol":22}`

func TestCallSendsOneRequestLineAndDecodesResult(t *testing.T) {
	server := newFakeServer(t, func(s *fakeSession) {
		s.success(json.RawMessage(pongResult))
	})
	client := New(server.path)

	var pong struct {
		Type     string `json:"type"`
		Version  string `json:"version"`
		Protocol uint32 `json:"protocol"`
	}
	if err := client.Call(context.Background(), "ping", nil, &pong); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if pong.Type != "pong" || pong.Version != "0.9.0" || pong.Protocol != 22 {
		t.Fatalf("unexpected result: %+v", pong)
	}

	req := server.request(0)
	if req.Method != "ping" {
		t.Errorf("method = %q, want ping", req.Method)
	}
	if req.ID != "herdr-go-1" {
		t.Errorf("id = %q, want herdr-go-1", req.ID)
	}
	if string(req.Params) != "{}" {
		t.Errorf("params = %s, want {}", req.Params)
	}
	if got := bytes.Count(req.raw, []byte("\n")); got != 1 {
		t.Errorf("request contains %d newlines, want 1", got)
	}
}

// unencodable is a params value json.Marshal refuses, which is how the tests
// below reach the encoding failure without a live server.
type unencodable struct{}

func (unencodable) MarshalJSON() ([]byte, error) { return nil, errors.New("unencodable") }

func TestRequestIsEncodedBeforeTheConnectionIsDialed(t *testing.T) {
	cases := []struct {
		name string
		call func(*Client) error
	}{
		{"CallRaw", func(c *Client) error {
			_, err := c.CallRaw(context.Background(), "pane.get", unencodable{})
			return err
		}},
		{"OpenStream", func(c *Client) error {
			_, err := c.OpenStream(context.Background(), "events.subscribe", unencodable{})
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := newFakeServer(t, func(s *fakeSession) {
				s.success(json.RawMessage(pongResult))
			})
			client := New(server.path)

			if err := tc.call(client); err == nil {
				t.Fatal("expected the encoding failure to be reported")
			}
			// The answered call proves the accept loop has run: it counts
			// accepts in order, so a connection dialed for the failed call
			// would already be counted.
			if err := client.Call(context.Background(), "ping", nil, nil); err != nil {
				t.Fatalf("Call: %v", err)
			}
			if got := server.acceptCount(); got != 1 {
				t.Errorf("server accepted %d connections, want 1", got)
			}
		})
	}
}

func TestCallSendsSuppliedParams(t *testing.T) {
	server := newFakeServer(t, func(s *fakeSession) {
		s.success(json.RawMessage(`{"type":"ok"}`))
	})
	client := New(server.path)

	params := struct {
		PaneID string `json:"pane_id"`
	}{PaneID: "pane_1"}
	if err := client.Call(context.Background(), "pane.get", params, nil); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if got := string(server.request(0).Params); got != `{"pane_id":"pane_1"}` {
		t.Errorf("params = %s", got)
	}
}

func TestCallRawReturnsResultObject(t *testing.T) {
	server := newFakeServer(t, func(s *fakeSession) {
		s.success(json.RawMessage(pongResult))
	})

	raw, err := New(server.path).CallRaw(context.Background(), "ping", nil)
	if err != nil {
		t.Fatalf("CallRaw: %v", err)
	}
	if string(raw) != pongResult {
		t.Errorf("raw result = %s, want %s", raw, pongResult)
	}
}

func TestCallErrorResponse(t *testing.T) {
	server := newFakeServer(t, func(s *fakeSession) {
		s.fail(ErrCodePaneNotFound, "pane pane_9 not found")
	})

	err := New(server.path).Call(context.Background(), "pane.get", nil, nil)
	var apiErr *Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %v, want *Error", err)
	}
	if apiErr.Method != "pane.get" || apiErr.Code != ErrCodePaneNotFound || apiErr.Message != "pane pane_9 not found" {
		t.Errorf("unexpected error: %+v", apiErr)
	}
	if !IsCode(err, ErrCodePaneNotFound) {
		t.Errorf("IsCode(%v, %q) = false", err, ErrCodePaneNotFound)
	}
	if IsCode(err, ErrCodeNotFound) {
		t.Errorf("IsCode matched the wrong code")
	}
}

func TestCallUsesOneConnectionPerCallAndCountsIDs(t *testing.T) {
	server := newFakeServer(t, func(s *fakeSession) {
		s.success(json.RawMessage(`{"type":"ok"}`))
	})
	client := New(server.path)

	for i := 0; i < 3; i++ {
		if err := client.Call(context.Background(), "ping", nil, nil); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if got := server.connectionCount(); got != 3 {
		t.Errorf("connections = %d, want 3", got)
	}
	for i, want := range []string{"herdr-go-1", "herdr-go-2", "herdr-go-3"} {
		if got := server.request(i).ID; got != want {
			t.Errorf("request %d id = %q, want %q", i, got, want)
		}
	}
}

func TestCallWithRequestIDs(t *testing.T) {
	server := newFakeServer(t, func(s *fakeSession) {
		s.success(json.RawMessage(`{"type":"ok"}`))
	})
	client := New(server.path, WithRequestIDs(func() string { return "fixed-id" }))

	if err := client.Call(context.Background(), "ping", nil, nil); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if got := server.request(0).ID; got != "fixed-id" {
		t.Errorf("id = %q, want fixed-id", got)
	}
}

func TestCallContextCanceled(t *testing.T) {
	received := make(chan struct{})
	release := make(chan struct{})
	server := newFakeServer(t, func(_ *fakeSession) {
		close(received)
		<-release
	})
	t.Cleanup(func() { close(release) })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-received
		cancel()
	}()

	_, err := New(server.path).CallRaw(ctx, "ping", nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestCallContextDeadline(t *testing.T) {
	release := make(chan struct{})
	server := newFakeServer(t, func(_ *fakeSession) { <-release })
	t.Cleanup(func() { close(release) })

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := New(server.path).CallRaw(ctx, "ping", nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want context.DeadlineExceeded", err)
	}
}

func TestCallDialTimeout(t *testing.T) {
	release := make(chan struct{})
	server := newFakeServer(t, func(_ *fakeSession) { <-release })
	t.Cleanup(func() { close(release) })

	_, err := New(server.path, WithDialTimeout(time.Nanosecond)).CallRaw(context.Background(), "ping", nil)
	if err == nil {
		t.Fatal("expected a dial timeout")
	}
	var netErr net.Error
	if !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Fatalf("error = %v, want a timeout", err)
	}
	if server.connectionCount() != 0 {
		t.Errorf("server saw a request despite the dial timeout")
	}
}

func TestCallDialTimeoutLeavesNormalCallsWorking(t *testing.T) {
	server := newFakeServer(t, func(s *fakeSession) {
		s.success(json.RawMessage(`{"type":"ok"}`))
	})

	client := New(server.path, WithDialTimeout(5*time.Second))
	if err := client.Call(context.Background(), "ping", nil, nil); err != nil {
		t.Fatalf("Call: %v", err)
	}
}

func TestCallDialFailure(t *testing.T) {
	err := New("/nonexistent/herdr.sock").Call(context.Background(), "ping", nil, nil)
	if err == nil {
		t.Fatal("expected a dial error")
	}
	if IsCode(err, ErrCodeInternalError) {
		t.Errorf("dial failure reported as a server error: %v", err)
	}
}

func TestCallMalformedResponseLine(t *testing.T) {
	server := newFakeServer(t, func(s *fakeSession) {
		s.writeLine("not json")
	})

	err := New(server.path).Call(context.Background(), "ping", nil, nil)
	if err == nil {
		t.Fatal("expected a decode error")
	}
	var apiErr *Error
	if errors.As(err, &apiErr) {
		t.Fatalf("malformed line reported as a server error: %v", err)
	}
}

func TestCallServerClosesWithoutResponse(t *testing.T) {
	server := newFakeServer(t, func(_ *fakeSession) {})

	_, err := New(server.path).CallRaw(context.Background(), "ping", nil)
	if err == nil {
		t.Fatal("expected an error when the server answers nothing")
	}
}

func TestCallResponseWithoutResultOrError(t *testing.T) {
	server := newFakeServer(t, func(s *fakeSession) {
		s.writeJSON(map[string]any{"id": s.request().ID})
	})

	_, err := New(server.path).CallRaw(context.Background(), "ping", nil)
	if err == nil {
		t.Fatal("expected an error for a response without result and error")
	}
}

func TestCallErrorResponseWithEmptyID(t *testing.T) {
	// herdr answers a request it cannot parse with an empty id.
	server := newFakeServer(t, func(s *fakeSession) {
		s.writeJSON(map[string]any{
			"id":    "",
			"error": map[string]string{"code": ErrCodeInvalidRequest, "message": "invalid request"},
		})
	})

	err := New(server.path).Call(context.Background(), "ping", nil, nil)
	if !IsCode(err, ErrCodeInvalidRequest) {
		t.Fatalf("error = %v, want code %s", err, ErrCodeInvalidRequest)
	}
}

func TestFakeServerAnswersMalformedRequest(t *testing.T) {
	server := newFakeServer(t, func(_ *fakeSession) {
		t.Error("handler ran for an unparsable request")
	})

	conn, err := net.Dial("unix", server.path)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.Write([]byte("{not json\n")); err != nil {
		t.Fatalf("write: %v", err)
	}

	line, err := readLine(bufio.NewReader(conn))
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	var response struct {
		ID    string `json:"id"`
		Error *struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(line, &response); err != nil {
		t.Fatalf("decode response %s: %v", line, err)
	}
	if response.ID != "" || response.Error == nil || response.Error.Code != ErrCodeInvalidRequest {
		t.Fatalf("unexpected response: %s", line)
	}
}

func TestCallDecodeIntoWrongType(t *testing.T) {
	server := newFakeServer(t, func(s *fakeSession) {
		s.success(json.RawMessage(pongResult))
	})

	var wrong []string
	err := New(server.path).Call(context.Background(), "ping", nil, &wrong)
	if err == nil {
		t.Fatal("expected a decode error")
	}
}
