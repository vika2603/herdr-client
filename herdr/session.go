package herdr

import (
	"context"
	"errors"
	"sync"
	"time"
)

// ResyncEvent reports that the mirror was rebuilt from a fresh snapshot after
// the event stream ended, which happens when the server restarts on live
// handoff. The events of the gap are not recoverable, so state a caller
// derived from earlier events has to be discarded.
type ResyncEvent struct {
	// Cause is the error that ended the previous stream. It is never nil: a
	// stream that ended without one is reported as ErrStreamClosed.
	Cause error
}

// EventName returns "session.resync", a name no server event carries.
func (ResyncEvent) EventName() string { return "session.resync" }

// Session mirrors the server state the session snapshot reports and keeps it
// current from an events.subscribe stream.
//
// The accessors are safe for concurrent use. Next applies each event to the
// mirror before returning it, so a reader that consults the accessors after
// Next sees the state that event produced; events that have not been read yet
// are not applied. Accessors and Snapshot return independent values, including
// nested optional values, maps and slices. Events returned by Next do not alias the
// mirror either; callers may modify their returned data independently.
type Session struct {
	client *Client
	subs   []Subscription
	cfg    sessionConfig
	done   chan struct{}

	// nextMu serialises Next, which owns the queue of events that are read
	// but not yet delivered.
	nextMu  sync.Mutex
	pending []*RawEvent
	resync  *ResyncEvent
	cause   error

	streamMu sync.Mutex
	stream   *EventStream
	closed   bool

	state *sessionState
}

// sessionConfig holds what the tests replace to avoid waiting.
type sessionConfig struct {
	backoff func(attempt int) time.Duration
}

const (
	reconnectBaseDelay = 100 * time.Millisecond
	reconnectMaxDelay  = 5 * time.Second
)

func defaultSessionConfig() sessionConfig {
	return sessionConfig{backoff: defaultBackoff}
}

// defaultBackoff doubles the delay per attempt up to reconnectMaxDelay.
func defaultBackoff(attempt int) time.Duration {
	delay := reconnectBaseDelay
	for i := 1; i < attempt && delay < reconnectMaxDelay; i++ {
		delay *= 2
	}
	return min(delay, reconnectMaxDelay)
}

// MirrorSubscriptions returns the subscriptions a Session uses when it is
// opened without any: every lifecycle subscription that needs no parameters.
// The three pane-scoped subscriptions are absent because each of them
// requires a pane id.
func MirrorSubscriptions() []Subscription {
	return []Subscription{
		WorkspaceCreatedSubscription{},
		WorkspaceUpdatedSubscription{},
		WorkspaceMetadataUpdatedSubscription{},
		WorkspaceRenamedSubscription{},
		WorkspaceMovedSubscription{},
		WorkspaceReorderedSubscription{},
		WorkspaceClosedSubscription{},
		WorkspaceFocusedSubscription{},
		WorktreeCreatedSubscription{},
		WorktreeOpenedSubscription{},
		WorktreeRemovedSubscription{},
		TabCreatedSubscription{},
		TabClosedSubscription{},
		TabRenamedSubscription{},
		TabMovedSubscription{},
		TabFocusedSubscription{},
		PaneCreatedSubscription{},
		PaneClosedSubscription{},
		PaneUpdatedSubscription{},
		PaneFocusedSubscription{},
		PaneMovedSubscription{},
		PaneExitedSubscription{},
		PaneAgentDetectedSubscription{},
		LayoutUpdatedSubscription{},
	}
}

// OpenSession subscribes, takes a session snapshot and returns the mirror the
// two produce. Without a subscription it mirrors everything
// MirrorSubscriptions lists.
//
// ctx bounds the bootstrap only. Once the Session exists it lives until Close;
// each Next takes its own context.
func OpenSession(ctx context.Context, c *Client, subs ...Subscription) (*Session, error) {
	return openSession(ctx, c, defaultSessionConfig(), subs)
}

func openSession(ctx context.Context, c *Client, cfg sessionConfig, subs []Subscription) (*Session, error) {
	if len(subs) == 0 {
		subs = MirrorSubscriptions()
	}
	boot, err := bootstrapSession(ctx, c, subs)
	if err != nil {
		return nil, err
	}
	return &Session{
		client:  c,
		subs:    subs,
		cfg:     cfg,
		done:    make(chan struct{}),
		pending: boot.buffered,
		stream:  boot.stream,
		state:   &sessionState{cache: boot.cache},
	}, nil
}

// Next returns the next event, having applied it to the mirror.
//
// When the stream ends because the server went away, Next reconnects, rebuilds
// the mirror from a fresh snapshot and reports the gap as a *ResyncEvent
// before the events of the new stream. Reconnection waits between attempts and
// gives up when ctx is done or the Session is closed, both of which leave the
// Session reconnectable on a later Next. Use errors.Is to identify a closed
// session (ErrStreamClosed) or a canceled read/backoff (ctx.Err()).
//
// Payload decoding failures carry OpDecode and do not reach the mirror. An
// unknown event remains inspectable as *UnknownEventError through errors.As.
func (s *Session) Next(ctx context.Context) (Event, error) {
	s.nextMu.Lock()
	defer s.nextMu.Unlock()

	for {
		if s.resync != nil {
			event := s.resync
			s.resync = nil
			return event, nil
		}
		if len(s.pending) > 0 {
			raw := s.pending[0]
			s.pending = s.pending[1:]
			return s.deliver(raw)
		}
		stream := s.currentStream()
		if stream == nil {
			if s.isClosed() {
				return nil, ErrStreamClosed
			}
			if err := s.reconnect(ctx); err != nil {
				return nil, err
			}
			continue
		}
		raw, err := stream.stream.Next(ctx)
		switch {
		case err == nil:
			return s.deliver(raw)
		case !errors.Is(err, ErrStreamClosed):
			return nil, err
		case s.isClosed():
			return nil, ErrStreamClosed
		default:
			// The server is gone. Drop the connection and reconnect on the
			// next turn of the loop.
			s.cause = err
			s.closeStream()
		}
	}
}

// Close ends the stream. A blocked Next matches ErrStreamClosed through
// errors.Is; a connection close failure retains its OpClose context.
func (s *Session) Close() error {
	s.streamMu.Lock()
	if s.closed {
		s.streamMu.Unlock()
		return nil
	}
	s.closed = true
	close(s.done)
	stream := s.stream
	s.stream = nil
	s.streamMu.Unlock()

	if stream == nil {
		return nil
	}
	return stream.Close()
}

// deliver decodes one pushed line, applies it to the mirror and returns it.
func (s *Session) deliver(raw *RawEvent) (Event, error) {
	event, err := DecodeEvent(raw.Event, raw.Data)
	if err != nil {
		return nil, opError(MethodEventsSubscribe, OpDecode, err)
	}
	s.state.apply(event)
	return event, nil
}

// reconnect bootstraps a new stream and mirror, and queues the ResyncEvent
// that reports the gap.
func (s *Session) reconnect(ctx context.Context) error {
	cause := s.cause
	if cause == nil {
		cause = ErrStreamClosed
	}
	for attempt := 1; ; attempt++ {
		if err := s.wait(ctx, s.cfg.backoff(attempt)); err != nil {
			return err
		}
		boot, err := bootstrapSession(ctx, s.client, s.subs)
		if err != nil {
			if !isRetryable(err) {
				return err
			}
			continue
		}
		if !s.install(boot) {
			_ = boot.stream.Close()
			return ErrStreamClosed
		}
		s.pending = boot.buffered
		s.resync = &ResyncEvent{Cause: cause}
		s.cause = nil
		return nil
	}
}

// wait blocks for d, or until ctx is done or the Session is closed.
func (s *Session) wait(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.done:
		return ErrStreamClosed
	case <-timer.C:
		return nil
	}
}

// install publishes a fresh stream and mirror. It reports false when the
// Session was closed while the bootstrap was running.
func (s *Session) install(boot *bootstrapResult) bool {
	s.streamMu.Lock()
	if s.closed {
		s.streamMu.Unlock()
		return false
	}
	s.stream = boot.stream
	s.streamMu.Unlock()

	s.state.replace(boot.cache)
	return true
}

func (s *Session) currentStream() *EventStream {
	s.streamMu.Lock()
	defer s.streamMu.Unlock()
	return s.stream
}

func (s *Session) isClosed() bool {
	s.streamMu.Lock()
	defer s.streamMu.Unlock()
	return s.closed
}

func (s *Session) closeStream() {
	s.streamMu.Lock()
	stream := s.stream
	s.stream = nil
	s.streamMu.Unlock()
	if stream != nil {
		_ = stream.Close()
	}
}

// isRetryable reports whether a failed bootstrap is worth another attempt. An
// error response means the request itself is unacceptable to the server, so
// repeating it cannot succeed; everything else is treated as the server not
// being there yet.
func isRetryable(err error) bool {
	var apiErr *Error
	if errors.As(err, &apiErr) {
		return false
	}
	var unexpected *UnexpectedResultError
	return !errors.As(err, &unexpected)
}
