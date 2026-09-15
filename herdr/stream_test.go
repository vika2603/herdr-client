package herdr

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

const subscriptionStarted = `{"type":"subscription_started"}`

// newStreamServer starts a fake server that acknowledges a streaming request
// and then pushes every line sent on the returned channel.
func newStreamServer(t *testing.T, ack string) (*fakeServer, chan string) {
	t.Helper()
	lines := make(chan string, 8)
	server := newFakeServer(t, func(s *fakeSession) {
		s.success(json.RawMessage(ack))
		s.watchClientWrites()
		for line := range lines {
			s.writeLine(line)
		}
	})
	t.Cleanup(func() { close(lines) })
	return server, lines
}

func openTestStream(t *testing.T, server *fakeServer) *Stream {
	t.Helper()
	params := map[string]any{"subscriptions": []any{map[string]string{"type": "pane.created"}}}
	stream, err := New(server.path).OpenStream(context.Background(), "events.subscribe", params)
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	return stream
}

func TestOpenStreamKeepsAckAndReadsEvents(t *testing.T) {
	server, lines := newStreamServer(t, subscriptionStarted)
	stream := openTestStream(t, server)

	if got := string(stream.Ack()); got != subscriptionStarted {
		t.Errorf("Ack() = %s, want %s", got, subscriptionStarted)
	}
	if got := server.request(0).Method; got != "events.subscribe" {
		t.Errorf("method = %q", got)
	}

	lines <- `{"event":"pane_created","data":{"type":"pane_created","pane_id":"pane_1"}}`
	lines <- `{"event":"pane.output_matched","data":{"pane_id":"pane_2"}}`

	ctx := context.Background()
	first, err := stream.Next(ctx)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if first.Event != "pane_created" {
		t.Errorf("event = %q, want pane_created", first.Event)
	}
	if string(first.Data) != `{"type":"pane_created","pane_id":"pane_1"}` {
		t.Errorf("data = %s", first.Data)
	}

	second, err := stream.Next(ctx)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if second.Event != "pane.output_matched" {
		t.Errorf("event = %q, want pane.output_matched", second.Event)
	}
}

func TestOpenStreamErrorResponse(t *testing.T) {
	server := newFakeServer(t, func(s *fakeSession) {
		s.fail(ErrCodeStreamConflict, "another stream is active")
	})

	stream, err := New(server.path).OpenStream(context.Background(), "events.subscribe", nil)
	if stream != nil {
		t.Errorf("stream = %v, want nil", stream)
	}
	var apiErr *Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %v, want *Error", err)
	}
	if apiErr.Method != "events.subscribe" || apiErr.Code != ErrCodeStreamConflict {
		t.Errorf("unexpected error: %+v", apiErr)
	}
}

func TestOpenStreamKeepsEventsWrittenWithTheAck(t *testing.T) {
	// The server may put the acknowledgement and the first events into one
	// write; the reader buffered by OpenStream must keep them.
	release := make(chan struct{})
	server := newFakeServer(t, func(s *fakeSession) {
		s.writeString(`{"id":"` + s.request().ID + `","result":` + subscriptionStarted + "}\n" +
			`{"event":"pane_created","data":{"type":"pane_created"}}` + "\n")
		<-release
	})
	t.Cleanup(func() { close(release) })
	stream := openTestStream(t, server)

	event, err := stream.Next(context.Background())
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if event.Event != "pane_created" {
		t.Errorf("event = %q, want pane_created", event.Event)
	}
}

func TestStreamCloseUnblocksNext(t *testing.T) {
	server, _ := newStreamServer(t, subscriptionStarted)
	stream := openTestStream(t, server)

	type result struct {
		event *RawEvent
		err   error
	}
	done := make(chan result, 1)
	go func() {
		event, err := stream.Next(context.Background())
		done <- result{event, err}
	}()

	// Give Next time to block on the connection before closing it.
	time.Sleep(20 * time.Millisecond)
	if err := stream.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	select {
	case got := <-done:
		if !errors.Is(got.err, ErrStreamClosed) {
			t.Fatalf("Next error = %v, want ErrStreamClosed", got.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not unblock Next")
	}
}

func TestStreamNextAfterClose(t *testing.T) {
	server, _ := newStreamServer(t, subscriptionStarted)
	stream := openTestStream(t, server)

	if err := stream.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := stream.Next(context.Background()); !errors.Is(err, ErrStreamClosed) {
		t.Fatalf("Next error = %v, want ErrStreamClosed", err)
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestStreamNextAfterServerClosedConnection(t *testing.T) {
	server := newFakeServer(t, func(s *fakeSession) {
		s.success(json.RawMessage(subscriptionStarted))
	})
	stream := openTestStream(t, server)

	if _, err := stream.Next(context.Background()); !errors.Is(err, ErrStreamClosed) {
		t.Fatalf("Next error = %v, want ErrStreamClosed", err)
	}
}

func TestStreamNextContextDeadlineKeepsStreamUsable(t *testing.T) {
	server, lines := newStreamServer(t, subscriptionStarted)
	stream := openTestStream(t, server)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := stream.Next(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Next error = %v, want context.DeadlineExceeded", err)
	}

	lines <- `{"event":"pane_created","data":{"type":"pane_created"}}`
	event, err := stream.Next(context.Background())
	if err != nil {
		t.Fatalf("Next after deadline: %v", err)
	}
	if event.Event != "pane_created" {
		t.Errorf("event = %q, want pane_created", event.Event)
	}
}

func TestStreamNeverWritesToTheConnection(t *testing.T) {
	server, lines := newStreamServer(t, subscriptionStarted)
	stream := openTestStream(t, server)

	lines <- `{"event":"pane_created","data":{"type":"pane_created"}}`
	if _, err := stream.Next(context.Background()); err != nil {
		t.Fatalf("Next: %v", err)
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if writes := server.clientWrites(); len(writes) != 0 {
		t.Errorf("client wrote %q on the stream connection", writes)
	}
}

func TestStreamInvalidEventLine(t *testing.T) {
	server, lines := newStreamServer(t, subscriptionStarted)
	stream := openTestStream(t, server)

	lines <- "not json"
	_, err := stream.Next(context.Background())
	if err == nil {
		t.Fatal("expected a decode error")
	}
	if errors.Is(err, ErrStreamClosed) {
		t.Fatalf("decode error reported as a closed stream: %v", err)
	}

	lines <- `{"event":"pane_created","data":{"type":"pane_created"}}`
	if _, err := stream.Next(context.Background()); err != nil {
		t.Fatalf("Next after a bad line: %v", err)
	}
}
