package herdrtest

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"time"

	"github.com/vika2603/herdr-client/herdr"
)

// Subscription is an acknowledged events.subscribe connection. It belongs to
// its Server and ends on peer disconnect, Close, or server cleanup.
type Subscription struct {
	conn   net.Conn
	ctx    context.Context
	cancel context.CancelFunc
	gate   chan struct{}
}

// Done closes when the subscription disconnects. It can be used to wait for
// client cancellation without polling or sleeps.
func (s *Subscription) Done() <-chan struct{} { return s.ctx.Done() }

// Close drops this connection. A live Session will reconnect when Next runs;
// WaitSubscription observes that new connection at the next index. Closing an
// already closed subscription succeeds, including concurrent close calls.
func (s *Subscription) Close() error {
	err := s.conn.Close()
	s.cancel()
	if errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

// Send writes a typed event through the real JSON wire format. Completion
// means the write finished, not that the client or plugin applied the event.
// Concurrent sends serialize; no order is promised between concurrent callers.
func (s *Subscription) Send(ctx context.Context, event herdr.Event) error {
	if event == nil {
		return errors.New("herdrtest: nil event")
	}
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if string(payload) == "null" {
		return errors.New("herdrtest: null event")
	}
	name := event.EventName()
	switch name {
	case "pane.output_matched", "pane.agent_status_changed", "pane.scroll_changed":
		// Dedicated subscription payloads have no lifecycle discriminator.
		// Agent status also has a lifecycle variant, so its generated marshaler
		// includes a type field that must not leak into the dotted envelope.
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(payload, &fields); err != nil {
			return err
		}
		delete(fields, "type")
		payload, err = json.Marshal(fields)
		if err != nil {
			return err
		}
	default:
		name = strings.ReplaceAll(name, ".", "_")
	}
	decoded, err := herdr.DecodeEvent(name, payload)
	if err != nil {
		return err
	}
	if decoded.EventName() != event.EventName() {
		return errors.New("herdrtest: event name does not match its payload")
	}
	return s.SendRaw(ctx, herdr.RawEvent{Event: name, Data: payload})
}

// SendRaw writes an event envelope, allowing tests to send unknown event names
// or mismatched payloads. The JSON must be valid. A canceled write that has
// started closes the subscription: a partial frame cannot safely be resumed.
// Cancellation while waiting for another writer leaves the connection intact.
func (s *Subscription) SendRaw(ctx context.Context, event herdr.RawEvent) error {
	encoded, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.ctx.Done():
		return net.ErrClosed
	case <-s.gate:
	}
	defer func() { s.gate <- struct{}{} }()
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.ctx.Err() != nil {
		return net.ErrClosed
	}
	finished := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		_ = s.conn.SetWriteDeadline(time.Now())
		close(finished)
	})
	err = writeAll(s.conn, append(encoded, '\n'))
	if !stop() {
		<-finished
	}
	if ctx.Err() != nil {
		_ = s.Close()
		return ctx.Err()
	}
	if err != nil {
		_ = s.Close()
	}
	return err
}

func writeAll(w io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := w.Write(data)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}
