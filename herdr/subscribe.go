package herdr

import (
	"context"
	"errors"
)

// EventStream is an events.subscribe connection that yields decoded events.
//
// It is a typed view of the underlying Stream: every line the server pushes
// is decoded with DecodeEvent, so callers never handle a RawEvent.
type EventStream struct {
	stream *Stream
}

// Subscribe starts an events.subscribe stream for the given subscriptions.
// At least one subscription is required; a stream without one would never
// push an event.
func (c *Client) Subscribe(ctx context.Context, subs ...Subscription) (*EventStream, error) {
	if len(subs) == 0 {
		return nil, opError(MethodEventsSubscribe, OpValidate, errors.New("no subscriptions"))
	}
	stream, err := c.EventsSubscribe(ctx, EventsSubscribeParams{Subscriptions: subs})
	if err != nil {
		return nil, err
	}
	if err := checkSubscriptionAck(stream); err != nil {
		_ = stream.Close()
		return nil, err
	}
	return &EventStream{stream: stream}, nil
}

// Next blocks until the server pushes the next event, the stream is closed,
// or ctx is done. The errors of (*Stream).Next are returned unchanged: a
// read errors match ErrStreamClosed or ctx.Err() through errors.Is, and a
// canceled read leaves the stream usable. Payload decoding errors carry
// OpDecode; an unknown event remains available as *UnknownEventError through
// errors.As.
func (s *EventStream) Next(ctx context.Context) (Event, error) {
	return s.stream.NextEvent(ctx)
}

// Close closes the connection. A blocked Next matches ErrStreamClosed through
// errors.Is; a connection close failure carries OpClose.
func (s *EventStream) Close() error { return s.stream.Close() }

// checkSubscriptionAck rejects an opening response that is not the
// subscription_started result the method is documented to return.
func checkSubscriptionAck(stream *Stream) error {
	result, err := decodeResult(MethodEventsSubscribe, stream.Ack())
	if err != nil {
		return err
	}
	if _, ok := result.(*SubscriptionStartedResponse); !ok {
		return opError(MethodEventsSubscribe, OpDecode, &UnexpectedResultError{
			Method: MethodEventsSubscribe,
			Want:   "subscription_started",
			Got:    result.ResultType(),
		})
	}
	return nil
}
