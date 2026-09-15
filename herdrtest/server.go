// Package herdrtest tests programs through the real Herdr client's local IPC
// protocol, without a Herdr binary, agent process, or terminal UI.
//
// The server scripts protocol responses rather than emulating Herdr's business
// rules. It supports ordinary requests and events.subscribe, not graphics
// streaming. Tests using NewServer skip on Windows: the client uses named
// pipes there, and this package currently supplies a Unix socket listener.
package herdrtest

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"
	"testing"

	"github.com/vika2603/herdr-client/herdr"
	"github.com/vika2603/herdr-client/internal/testsocket"
)

// ErrClosed is returned by waits interrupted by server shutdown.
var ErrClosed = errors.New("herdrtest: server closed")

// Call records a request. Params is an independent copy of its JSON object.
type Call struct {
	Method string
	Params json.RawMessage
}

// Handler supplies a response for each request to a method. Handlers may run
// concurrently and must synchronize shared state. The context ends when the
// peer disconnects or the server closes; a blocking handler must observe it.
// A *herdr.Error becomes an API error with its code and message; another error
// becomes internal_error. A nil result and nil error is a scripting error.
// Handlers must not call Server.Close, which waits for them to return.
type Handler func(context.Context, Call) (herdr.Result, error)

// Server records calls and answers them with registered handlers. A method
// without a handler returns invalid_request naming the unscripted method;
// it does not silently succeed or wait forever. Inspect the client error or
// Calls to assert on such a request; it does not automatically fail the test.
type Server struct {
	t       testing.TB
	socket  *testsocket.Server
	mu      sync.Mutex
	routes  map[string]route
	calls   []Call
	subs    []*Subscription
	changed chan struct{}
}

type route struct {
	handler   Handler
	subscribe bool
}

// NewServer starts an empty server and registers cleanup with t. It does not
// change process environment variables or touch any real Herdr session.
func NewServer(t testing.TB) *Server {
	t.Helper()
	s := &Server{t: t, routes: make(map[string]route), changed: make(chan struct{})}
	s.socket = testsocket.New(t, s.serveConn)
	return s
}

// SocketPath is the local IPC address for herdr.New or a plugin environment.
func (s *Server) SocketPath() string { return s.socket.Path }

// Client returns a real client configured for this server.
func (s *Server) Client(opts ...herdr.Option) *herdr.Client {
	return herdr.New(s.SocketPath(), opts...)
}

// Close closes all connections, cancels handlers and waits for server
// goroutines to exit. It is safe to call more than once or concurrently.
func (s *Server) Close() { s.socket.Close() }

// Handle replaces the response to method. Registration and calls may overlap;
// a call uses the handler registered when the request was recorded.
func (s *Server) Handle(method string, handler Handler) *Server {
	if handler == nil {
		panic("herdrtest: nil handler")
	}
	s.mu.Lock()
	s.routes[method] = route{handler: handler}
	s.mu.Unlock()
	return s
}

// Reply answers every call to method with a fixed result, captured at
// registration. Registering again replaces the previous response.
func (s *Server) Reply(method string, result herdr.Result) *Server {
	s.t.Helper()
	encoded, err := encodeResult(result)
	if err != nil {
		s.t.Fatalf("herdrtest: reply to %s: %v", method, err)
	}
	return s.Handle(method, func(context.Context, Call) (herdr.Result, error) {
		return encodedResult(encoded), nil
	})
}

// Fail answers every call to method with a server API error.
func (s *Server) Fail(method, code, message string) *Server {
	return s.Handle(method, func(context.Context, Call) (herdr.Result, error) {
		return nil, &herdr.Error{Code: code, Message: message}
	})
}

// AllowSubscriptions acknowledges events.subscribe and leaves its connection
// open. WaitSubscription returns each acknowledged connection in order.
// Requests must contain valid, nonempty subscriptions. Send deliberately does
// not filter events, so tests can also exercise unexpected event handling.
// Handle, Reply or Fail for events.subscribe replaces this behavior.
func (s *Server) AllowSubscriptions() *Server {
	s.mu.Lock()
	s.routes[herdr.MethodEventsSubscribe] = route{subscribe: true}
	s.mu.Unlock()
	return s
}

// Calls returns independent copies of all requests in arrival order.
func (s *Server) Calls() []Call {
	s.mu.Lock()
	defer s.mu.Unlock()
	calls := make([]Call, len(s.calls))
	for i, call := range s.calls {
		calls[i] = cloneCall(call)
	}
	return calls
}

// Methods returns the method names in arrival order.
func (s *Server) Methods() []string {
	calls := s.Calls()
	methods := make([]string, len(calls))
	for i, call := range calls {
		methods[i] = call.Method
	}
	return methods
}

// WaitCall waits for the zero-based request index to arrive. Arrival does not
// mean its handler or the client has finished. Multiple waiters can observe
// the same call; waits do not consume records. ctx bounds only this wait.
func (s *Server) WaitCall(ctx context.Context, index int) (Call, error) {
	if index < 0 {
		return Call{}, fmt.Errorf("herdrtest: negative call index %d", index)
	}
	for {
		if err := ctx.Err(); err != nil {
			return Call{}, err
		}
		s.mu.Lock()
		if index < len(s.calls) {
			call := cloneCall(s.calls[index])
			s.mu.Unlock()
			return call, nil
		}
		changed := s.changed
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return Call{}, ctx.Err()
		case <-s.socket.Done():
			return Call{}, ErrClosed
		case <-changed:
		}
	}
}

// WaitSubscription waits for the zero-based acknowledged subscription index.
// Reconnects create new entries; closed subscriptions retain their index.
// Acknowledgement means its response was written, not that the client consumed it.
func (s *Server) WaitSubscription(ctx context.Context, index int) (*Subscription, error) {
	if index < 0 {
		return nil, fmt.Errorf("herdrtest: negative subscription index %d", index)
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		s.mu.Lock()
		if index < len(s.subs) {
			sub := s.subs[index]
			s.mu.Unlock()
			return sub, nil
		}
		changed := s.changed
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-s.socket.Done():
			return nil, ErrClosed
		case <-changed:
		}
	}
}

func (s *Server) notify() {
	close(s.changed)
	s.changed = make(chan struct{})
}

func cloneCall(call Call) Call {
	call.Params = bytes.Clone(call.Params)
	return call
}

func (s *Server) serveConn(parent context.Context, conn net.Conn) {
	reader := bufio.NewReader(conn)
	line, err := reader.ReadBytes('\n')
	if err != nil {
		return
	}
	var request struct {
		ID     string          `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(line, &request); err != nil {
		_ = writeResponse(conn, "", nil, &herdr.Error{Code: herdr.ErrCodeInvalidRequest, Message: "herdrtest: malformed request"})
		return
	}
	ctx, cancel := context.WithCancel(parent)
	peerDone := make(chan struct{})
	go func() {
		defer close(peerDone)
		// EOF or any extra input ends this one-request connection.
		_, _ = reader.ReadByte()
		cancel()
		_ = conn.Close()
	}()
	defer func() {
		cancel()
		_ = conn.Close()
		<-peerDone
	}()
	call := Call{Method: request.Method, Params: request.Params}
	s.mu.Lock()
	s.calls = append(s.calls, cloneCall(call))
	r := s.routes[call.Method]
	s.notify()
	s.mu.Unlock()
	if r.subscribe {
		s.serveSubscription(ctx, cancel, conn, request.ID, call)
		return
	}
	if r.handler == nil {
		_ = writeResponse(conn, request.ID, nil, &herdr.Error{
			Code: herdr.ErrCodeInvalidRequest, Message: "herdrtest: no reply is scripted for " + call.Method,
		})
		return
	}
	result, handlerErr := r.handler(ctx, call)
	// A handler may finish its cleanup after shutdown has begun. Its result
	// no longer belongs to a live request and must not race connection closure.
	if ctx.Err() != nil {
		return
	}
	if handlerErr != nil {
		_ = writeResponse(conn, request.ID, nil, handlerErr)
		return
	}
	encoded, err := encodeResult(result)
	if err != nil {
		s.t.Errorf("herdrtest: response to %s: %v", call.Method, err)
		_ = writeResponse(conn, request.ID, nil, err)
		return
	}
	_ = writeResponse(conn, request.ID, encoded, nil)
}

func (s *Server) serveSubscription(ctx context.Context, cancel context.CancelFunc, conn net.Conn, id string, call Call) {
	var params herdr.EventsSubscribeParams
	if err := json.Unmarshal(call.Params, &params); err != nil || len(params.Subscriptions) == 0 {
		_ = writeResponse(conn, id, nil, &herdr.Error{Code: herdr.ErrCodeInvalidParams, Message: "herdrtest: expected nonempty subscriptions"})
		return
	}
	ack, _ := json.Marshal(herdr.SubscriptionStartedResponse{})
	if err := writeResponse(conn, id, ack, nil); err != nil {
		return
	}
	sub := &Subscription{conn: conn, ctx: ctx, cancel: cancel, gate: make(chan struct{}, 1)}
	sub.gate <- struct{}{}
	s.mu.Lock()
	s.subs = append(s.subs, sub)
	s.notify()
	s.mu.Unlock()
	<-ctx.Done()
}

func encodeResult(result herdr.Result) (json.RawMessage, error) {
	if result == nil {
		return nil, errors.New("nil result without an API error")
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	if bytes.Equal(encoded, []byte("null")) {
		return nil, errors.New("null result without an API error")
	}
	return encoded, nil
}

type encodedResult json.RawMessage

func (encodedResult) ResultType() string             { return "" }
func (r encodedResult) MarshalJSON() ([]byte, error) { return r, nil }

func writeResponse(conn net.Conn, id string, result json.RawMessage, responseErr error) error {
	response := map[string]any{"id": id}
	if responseErr != nil {
		var apiErr *herdr.Error
		if !errors.As(responseErr, &apiErr) {
			apiErr = &herdr.Error{Code: herdr.ErrCodeInternalError, Message: responseErr.Error()}
		}
		response["error"] = map[string]string{"code": apiErr.Code, "message": apiErr.Message}
	} else {
		response["result"] = result
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		return err
	}
	return writeAll(conn, append(encoded, '\n'))
}
