package herdr

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

// Client sends requests to a Herdr server over local IPC or an injected dialer.
//
// The server reads exactly one request per connection and closes the
// connection after writing the response, so every Call dials a fresh
// connection. Methods that keep the connection open, such as
// events.subscribe, go through OpenStream.
//
// A Client is safe for concurrent use.
type Client struct {
	socketPath  string
	dialTimeout time.Duration
	dialer      DialFunc
	nextID      func() string
	sequence    atomic.Uint64
}

// Option configures a Client.
type Option func(*Client)

// WithDialTimeout bounds connection establishment, including an injected
// dialer. It does not bound the request exchange or a returned stream's lifetime.
// A non-positive duration adds no deadline beyond the caller's context.
func WithDialTimeout(d time.Duration) Option {
	return func(c *Client) { c.dialTimeout = d }
}

// WithRequestIDs replaces the request id generator.
func WithRequestIDs(next func() string) Option {
	return func(c *Client) { c.nextID = next }
}

// New returns a Client that dials socketPath. It performs no I/O. WithDialer
// passes socketPath unchanged to the injected dialer as its address.
func New(socketPath string, opts ...Option) *Client {
	c := &Client{socketPath: socketPath}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// NewFromEnv resolves the socket path the way the herdr CLI does
// (HERDR_SOCKET_PATH, then HERDR_SESSION, then the default session) and
// returns a Client for it.
func NewFromEnv(opts ...Option) (*Client, error) {
	path, err := ResolveSocketPath("")
	if err != nil {
		return nil, err
	}
	return New(path, opts...), nil
}

// SocketPath returns the configured address, also passed to an injected dialer.
func (c *Client) SocketPath() string { return c.socketPath }

// Call sends one request and decodes the response's result object into
// result, which may be nil. A server error response is returned as *Error.
//
// Cancelling ctx or reaching its deadline closes the connection and the call
// wraps ctx.Err() in an OpError.
func (c *Client) Call(ctx context.Context, method string, params, result any) error {
	raw, err := c.CallRaw(ctx, method, params)
	if err != nil {
		return err
	}
	if result == nil {
		return nil
	}
	if err := json.Unmarshal(raw, result); err != nil {
		return opError(method, OpDecode, err)
	}
	return nil
}

// CallRaw sends one request and returns the raw result object.
func (c *Client) CallRaw(ctx context.Context, method string, params any) (json.RawMessage, error) {
	opened, err := c.open(ctx, method, params)
	if err != nil {
		return nil, err
	}
	defer func() { _ = opened.conn.Close() }()
	return opened.result, nil
}

// OpenStream sends one request, reads its acknowledging response, and keeps
// the connection open so that the lines the server pushes afterwards can be
// read from the returned Stream.
//
// ctx bounds the opening request only. Once the Stream exists it lives until
// Close; each Next takes its own context.
func (c *Client) OpenStream(ctx context.Context, method string, params any) (*Stream, error) {
	opened, err := c.open(ctx, method, params)
	if err != nil {
		return nil, err
	}
	return newStream(method, opened.conn, opened.reader, opened.result), nil
}

// openedConnection transfers a successful exchange and its buffered reader to
// the caller. The caller closes it or gives it to a stream; failed exchanges
// never transfer ownership.
type openedConnection struct {
	conn   io.ReadWriteCloser
	reader *bufio.Reader
	result json.RawMessage
}

// open is the shared first exchange for ordinary requests and both stream
// protocols. The request is ready before dialing, and the opening context is
// detached before ownership transfers. Any bytes read beyond the response
// remain in reader for the stream to consume.
func (c *Client) open(ctx context.Context, method string, params any) (*openedConnection, error) {
	request, err := requestLine(c.requestID(), method, params)
	if err != nil {
		return nil, requestError(ctx, method, OpEncode, err)
	}
	conn, err := c.dial(ctx)
	if err != nil {
		return nil, opError(method, OpDial, err)
	}
	transferred := false
	defer func() {
		if !transferred {
			_ = conn.Close()
		}
	}()
	stopWatch := watchContext(ctx, conn)
	defer stopWatch()
	reader := bufio.NewReader(conn)
	if err := writeAll(conn, request); err != nil {
		return nil, requestError(ctx, method, OpWrite, err)
	}
	line, err := readLine(reader)
	if err != nil {
		return nil, requestError(ctx, method, OpRead, err)
	}
	result, err := decodeResponseLine(method, line)
	// Stopping also joins a cancellation already in progress. It must not be
	// possible for this watcher to close a successfully returned stream later.
	stopWatch()
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, opError(method, OpRead, ctxErr)
	}
	if err != nil {
		return nil, err
	}
	transferred = true
	return &openedConnection{conn: conn, reader: reader, result: result}, nil
}

// requestID returns the id of the next request.
func (c *Client) requestID() string {
	if c.nextID != nil {
		return c.nextID()
	}
	return fmt.Sprintf("herdr-go-%d", c.sequence.Add(1))
}

type wireRequest struct {
	ID     string `json:"id"`
	Method string `json:"method"`
	Params any    `json:"params"`
}

type wireResponse struct {
	ID     string          `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *wireError      `json:"error"`
}

type wireError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// emptyParams stands in for a nil params value; the server requires the field
// to be an object.
var emptyParams = json.RawMessage(`{}`)

// requestLine encodes one request as the line to write to the socket.
//
// Every caller encodes the line before it dials. The server reads the request
// once as soon as it accepts the connection and, finding nothing there yet,
// waits out a poll interval before looking again, so a request encoded on an
// open connection answers a poll interval later than one already in hand.
func requestLine(id, method string, params any) ([]byte, error) {
	if params == nil {
		params = emptyParams
	}
	line, err := json.Marshal(wireRequest{ID: id, Method: method, Params: params})
	if err != nil {
		return nil, err
	}
	return append(line, '\n'), nil
}

func decodeResponseLine(method string, line []byte) (json.RawMessage, error) {
	var response wireResponse
	if err := json.Unmarshal(line, &response); err != nil {
		return nil, opError(method, OpDecode, err)
	}
	if response.Error != nil {
		return nil, &Error{Method: method, Code: response.Error.Code, Message: response.Error.Message}
	}
	if len(response.Result) == 0 {
		return nil, opError(method, OpDecode, fmt.Errorf("response carries neither result nor error"))
	}
	return response.Result, nil
}

// readLine reads one newline-terminated line without its line ending. Blank
// lines are skipped. A final line that the server did not terminate is
// returned rather than dropped.
func readLine(r *bufio.Reader) ([]byte, error) {
	for {
		line, err := r.ReadBytes('\n')
		line = bytes.TrimRight(line, "\r\n")
		if len(bytes.TrimSpace(line)) > 0 {
			return line, nil
		}
		if err != nil {
			return nil, err
		}
	}
}

// requestError reports a failed exchange, preferring the context error when
// the connection was closed because ctx was done.
func requestError(ctx context.Context, method string, op Op, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		err = ctxErr
	}
	return opError(method, op, err)
}

// decodeResult adds method context at the client boundary. DecodeResult itself
// remains usable for standalone JSON decoding without inventing a method.
func decodeResult(method string, raw json.RawMessage) (Result, error) {
	result, err := DecodeResult(raw)
	return result, opError(method, OpDecode, err)
}

// watchContext closes conn once ctx is done, which is the only way to unblock
// a read on a connection that has no deadline support on every platform. The
// returned function stops the watch and must run before conn outlives the
// call.
func watchContext(ctx context.Context, conn io.Closer) func() {
	if ctx.Done() == nil {
		return func() {}
	}
	finished := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		defer close(finished)
		_ = conn.Close()
	})
	return sync.OnceFunc(func() {
		if !stop() {
			<-finished
		}
	})
}

// writeAll writes each complete protocol segment or reports a short write.
// A dialer may return any io.ReadWriteCloser, so a short write must not be
// mistaken for a successfully transmitted request or frame.
func writeAll(w io.Writer, parts ...[]byte) error {
	for _, part := range parts {
		if len(part) == 0 {
			continue
		}
		n, err := w.Write(part)
		if err != nil {
			return err
		}
		if n != len(part) {
			return io.ErrShortWrite
		}
	}
	return nil
}
