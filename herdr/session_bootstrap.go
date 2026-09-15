package herdr

import (
	"context"
	"errors"
)

// bootstrapResult is one completed bootstrap: the live stream, the mirror
// built from the snapshot, and the events that arrived before the snapshot
// answered.
type bootstrapResult struct {
	stream   *EventStream
	cache    *sessionCache
	buffered []*RawEvent
}

// bootstrapSession runs the documented order: subscribe first, buffer what the
// stream pushes, then take the snapshot. Buffering before the snapshot is what
// keeps an event that fires during the call from being lost; the buffered
// events are applied after the snapshot is installed, and applying one the
// snapshot already reflects assigns the same state again.
func bootstrapSession(ctx context.Context, c *Client, subs []Subscription) (*bootstrapResult, error) {
	stream, err := c.Subscribe(ctx, subs...)
	if err != nil {
		return nil, err
	}
	collector := collectEvents(stream)
	snapshot, err := c.SessionSnapshot(ctx)
	buffered, streamErr := collector.stop()
	if err == nil {
		err = streamErr
	}
	if err != nil {
		_ = stream.Close()
		return nil, err
	}
	return &bootstrapResult{
		stream:   stream,
		cache:    newSessionCache(snapshot.Snapshot),
		buffered: buffered,
	}, nil
}

// eventCollector drains a stream into a buffer until it is stopped.
type eventCollector struct {
	cancel context.CancelFunc
	done   chan struct{}
	events []*RawEvent
	err    error
}

func collectEvents(stream *EventStream) *eventCollector {
	ctx, cancel := context.WithCancel(context.Background())
	collector := &eventCollector{cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(collector.done)
		for {
			event, err := stream.stream.Next(ctx)
			if err != nil {
				if !errors.Is(err, context.Canceled) {
					collector.err = err
				}
				return
			}
			collector.events = append(collector.events, event)
		}
	}()
	return collector
}

// stop ends the collection and returns what was buffered. A line the reader
// had already taken from the connection but not yet handed over stays on the
// stream and is read by the first Next.
func (c *eventCollector) stop() ([]*RawEvent, error) {
	c.cancel()
	<-c.done
	return c.events, c.err
}
