package herdr

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

type transportDialer struct {
	t     *testing.T
	serve func(net.Conn) error

	mu        sync.Mutex
	addresses []string
	servers   map[net.Conn]struct{}
	closed    []<-chan struct{}
	stopping  bool
	wg        sync.WaitGroup
}

func newTransportDialer(t *testing.T, serve func(net.Conn) error) *transportDialer {
	t.Helper()
	d := &transportDialer{t: t, serve: serve, servers: make(map[net.Conn]struct{})}
	t.Cleanup(d.cleanup)
	return d
}

func (d *transportDialer) dial(_ context.Context, address string) (io.ReadWriteCloser, error) {
	client, server := net.Pipe()
	observed := newCloseObservedConn(client)

	d.mu.Lock()
	if d.stopping {
		d.mu.Unlock()
		_ = client.Close()
		_ = server.Close()
		return nil, errors.New("test dialer is stopping")
	}
	d.addresses = append(d.addresses, address)
	d.servers[server] = struct{}{}
	d.closed = append(d.closed, observed.closed)
	d.wg.Add(1)
	d.mu.Unlock()

	go func() {
		defer d.wg.Done()
		defer func() { _ = server.Close() }()
		defer func() {
			d.mu.Lock()
			delete(d.servers, server)
			d.mu.Unlock()
		}()
		if err := d.serve(server); err != nil {
			d.t.Errorf("serve injected connection: %v", err)
		}
	}()
	return observed, nil
}

func (d *transportDialer) snapshot() ([]string, []<-chan struct{}) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.addresses...), append([]<-chan struct{}(nil), d.closed...)
}

func (d *transportDialer) cleanup() {
	d.mu.Lock()
	d.stopping = true
	for server := range d.servers {
		_ = server.Close()
	}
	d.mu.Unlock()

	done := make(chan struct{})
	go func() {
		d.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		d.t.Error("injected connection handler did not stop")
	}
}

type closeObservedConn struct {
	io.ReadWriteCloser
	closed chan struct{}
	once   sync.Once
}

func newCloseObservedConn(conn io.ReadWriteCloser) *closeObservedConn {
	return &closeObservedConn{ReadWriteCloser: conn, closed: make(chan struct{})}
}

func (c *closeObservedConn) Close() error {
	var err error
	c.once.Do(func() {
		close(c.closed)
		err = c.ReadWriteCloser.Close()
	})
	return err
}

func readTransportRequest(conn io.Reader) (wireRequest, *bufio.Reader, error) {
	reader := bufio.NewReader(conn)
	line, err := reader.ReadBytes('\n')
	if err != nil {
		return wireRequest{}, reader, err
	}
	var request wireRequest
	if err := json.Unmarshal(line, &request); err != nil {
		return wireRequest{}, reader, err
	}
	return request, reader, nil
}

func writeTransportResult(conn io.Writer, id, result string) error {
	_, err := fmt.Fprintf(conn, `{"id":%q,"result":%s}`+"\n", id, result)
	return err
}

func waitTransportClosed(t *testing.T, closed <-chan struct{}) {
	t.Helper()
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("client did not close the injected connection")
	}
}

func waitTransportSignal(t *testing.T, signal <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

func TestWithDialerGeneratedCallsUseAddressAndFreshConnections(t *testing.T) {
	dialer := newTransportDialer(t, func(conn net.Conn) error {
		request, _, err := readTransportRequest(conn)
		if err != nil {
			return err
		}
		if request.Method != MethodPing {
			return fmt.Errorf("method = %q, want %q", request.Method, MethodPing)
		}
		return writeTransportResult(conn, request.ID, pongResult)
	})
	client := New("test://local-session", WithDialer(dialer.dial))
	if addresses, _ := dialer.snapshot(); len(addresses) != 0 {
		t.Fatalf("New performed %d dial calls, want 0", len(addresses))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	const calls = 8
	start := make(chan struct{})
	errs := make(chan error, calls)
	for range calls {
		go func() {
			<-start
			pong, err := client.Ping(ctx)
			if err == nil && (pong.Version != "0.9.0" || pong.Protocol != 22) {
				err = fmt.Errorf("unexpected pong: %+v", pong)
			}
			errs <- err
		}()
	}
	close(start)
	for range calls {
		if err := <-errs; err != nil {
			t.Errorf("Ping: %v", err)
		}
	}

	addresses, closed := dialer.snapshot()
	if len(addresses) != calls {
		t.Fatalf("dial calls = %d, want %d", len(addresses), calls)
	}
	for i, address := range addresses {
		if address != "test://local-session" {
			t.Errorf("dial %d address = %q, want %q", i, address, "test://local-session")
		}
	}
	for _, done := range closed {
		waitTransportClosed(t, done)
	}
}

func TestWithDialerRejectsNilConnection(t *testing.T) {
	_, err := New("unused", WithDialer(func(context.Context, string) (io.ReadWriteCloser, error) {
		return nil, nil
	})).CallRaw(context.Background(), MethodPing, nil)
	if err == nil {
		t.Fatal("nil connection was accepted")
	}
}

func TestWithDialTimeoutAppliesOnlyWhileDialing(t *testing.T) {
	t.Run("cancels dial", func(t *testing.T) {
		dial := func(ctx context.Context, _ string) (io.ReadWriteCloser, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, err := New("unused", WithDialer(dial), WithDialTimeout(20*time.Millisecond)).CallRaw(ctx, MethodPing, nil)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("error = %v, want context.DeadlineExceeded", err)
		}
	})

	t.Run("does not bound handshake", func(t *testing.T) {
		serverDone := make(chan error, 1)
		dial := func(ctx context.Context, _ string) (io.ReadWriteCloser, error) {
			if _, ok := ctx.Deadline(); !ok {
				return nil, errors.New("dial context has no deadline")
			}
			client, server := net.Pipe()
			go func() {
				defer func() { _ = server.Close() }()
				request, _, err := readTransportRequest(server)
				if err == nil {
					<-ctx.Done()
					err = writeTransportResult(server, request.ID, pongResult)
				}
				serverDone <- err
			}()
			return client, nil
		}

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		pong, err := New("unused", WithDialer(dial), WithDialTimeout(20*time.Millisecond)).Ping(ctx)
		if err != nil {
			t.Fatalf("Ping: %v", err)
		}
		if pong.Version != "0.9.0" {
			t.Errorf("version = %q, want 0.9.0", pong.Version)
		}
		select {
		case err := <-serverDone:
			if err != nil {
				t.Fatalf("serve connection: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("injected connection handler did not stop")
		}
	})
}

type transportOpenCase struct {
	name string
	open func(context.Context, *Client) (io.Closer, error)
}

func transportOpenCases() []transportOpenCase {
	return []transportOpenCase{
		{
			name: "ordinary call",
			open: func(ctx context.Context, client *Client) (io.Closer, error) {
				_, err := client.CallRaw(ctx, MethodPing, nil)
				return nil, err
			},
		},
		{
			name: "event stream",
			open: func(ctx context.Context, client *Client) (io.Closer, error) {
				stream, err := client.OpenStream(ctx, MethodEventsSubscribe, nil)
				if err != nil {
					return nil, err
				}
				return stream, nil
			},
		},
		{
			name: "graphics stream",
			open: func(ctx context.Context, client *Client) (io.Closer, error) {
				stream, err := client.PaneGraphicsStream(ctx, PaneGraphicsStreamParams{PaneID: "w1:p1"})
				if err != nil {
					return nil, err
				}
				return stream, nil
			},
		},
	}
}

func TestInjectedConnectionsCloseAfterHandshakeFailure(t *testing.T) {
	failures := []struct {
		name  string
		serve func(net.Conn) error
	}{
		{
			name: "read",
			serve: func(conn net.Conn) error {
				_, _, err := readTransportRequest(conn)
				return err
			},
		},
		{
			name: "decode",
			serve: func(conn net.Conn) error {
				if _, _, err := readTransportRequest(conn); err != nil {
					return err
				}
				_, err := io.WriteString(conn, "not json\n")
				return err
			},
		},
	}

	for _, entry := range transportOpenCases() {
		for _, failure := range failures {
			t.Run(entry.name+"/"+failure.name, func(t *testing.T) {
				dialer := newTransportDialer(t, failure.serve)
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				opened, err := entry.open(ctx, New("unused", WithDialer(dialer.dial)))
				if opened != nil {
					_ = opened.Close()
					t.Fatalf("opened = %T, want nil", opened)
				}
				if err == nil {
					t.Fatal("handshake failure was not reported")
				}
				_, closed := dialer.snapshot()
				if len(closed) != 1 {
					t.Fatalf("dial calls = %d, want 1", len(closed))
				}
				waitTransportClosed(t, closed[0])
			})
		}
	}
}

type shortWriteConn struct {
	closed chan struct{}
	once   sync.Once
}

func (c *shortWriteConn) Read([]byte) (int, error) { return 0, errors.New("unexpected read") }

func (c *shortWriteConn) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	return len(p) - 1, nil
}

func (c *shortWriteConn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return nil
}

func TestHandshakeRejectsShortWrites(t *testing.T) {
	for _, entry := range transportOpenCases() {
		t.Run(entry.name, func(t *testing.T) {
			conn := &shortWriteConn{closed: make(chan struct{})}
			client := New("unused", WithDialer(func(context.Context, string) (io.ReadWriteCloser, error) {
				return conn, nil
			}))
			opened, err := entry.open(testContext(t), client)
			if opened != nil {
				_ = opened.Close()
				t.Fatalf("opened = %T, want nil", opened)
			}
			if !errors.Is(err, io.ErrShortWrite) {
				t.Fatalf("error = %v, want io.ErrShortWrite", err)
			}
			waitTransportClosed(t, conn.closed)
		})
	}
}

type countedCloseConn struct {
	io.ReadWriteCloser
	mu     sync.Mutex
	closes int
}

func (c *countedCloseConn) Close() error {
	c.mu.Lock()
	c.closes++
	c.mu.Unlock()
	return c.ReadWriteCloser.Close()
}

func (c *countedCloseConn) closeCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closes
}

func newCountedPipe(t *testing.T, serve func(net.Conn)) (*countedCloseConn, DialFunc) {
	t.Helper()
	client, server := net.Pipe()
	conn := &countedCloseConn{ReadWriteCloser: client}
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() { _ = server.Close() }()
		serve(server)
	}()
	t.Cleanup(func() {
		_ = client.Close()
		_ = server.Close()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("injected connection handler did not stop")
		}
	})
	return conn, func(context.Context, string) (io.ReadWriteCloser, error) { return conn, nil }
}

func TestClientClosesInjectedConnectionOnce(t *testing.T) {
	t.Run("ordinary completion", func(t *testing.T) {
		conn, dial := newCountedPipe(t, func(server net.Conn) {
			request, _, err := readTransportRequest(server)
			if err == nil {
				_ = writeTransportResult(server, request.ID, pongResult)
			}
		})
		if _, err := New("unused", WithDialer(dial)).Ping(testContext(t)); err != nil {
			t.Fatalf("Ping: %v", err)
		}
		if got := conn.closeCount(); got != 1 {
			t.Errorf("underlying Close calls = %d, want 1", got)
		}
	})

	t.Run("failed handshake", func(t *testing.T) {
		conn, dial := newCountedPipe(t, func(server net.Conn) {
			if _, _, err := readTransportRequest(server); err == nil {
				_, _ = io.WriteString(server, "not json\n")
			}
		})
		if _, err := New("unused", WithDialer(dial)).CallRaw(testContext(t), MethodPing, nil); err == nil {
			t.Fatal("handshake failure was not reported")
		}
		if got := conn.closeCount(); got != 1 {
			t.Errorf("underlying Close calls = %d, want 1", got)
		}
	})

	t.Run("canceled handshake", func(t *testing.T) {
		ctx, cancel := context.WithCancel(testContext(t))
		defer cancel()
		conn, dial := newCountedPipe(t, func(server net.Conn) {
			if _, _, err := readTransportRequest(server); err == nil {
				cancel()
				_, _ = io.Copy(io.Discard, server)
			}
		})
		_, err := New("unused", WithDialer(dial)).CallRaw(ctx, MethodPing, nil)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context.Canceled", err)
		}
		if got := conn.closeCount(); got != 1 {
			t.Errorf("underlying Close calls = %d, want 1", got)
		}
	})

	t.Run("dial returns connection and error", func(t *testing.T) {
		client, peer := net.Pipe()
		t.Cleanup(func() { _ = peer.Close() })
		conn := &countedCloseConn{ReadWriteCloser: client}
		wantErr := errors.New("dial failed")
		dial := func(context.Context, string) (io.ReadWriteCloser, error) { return conn, wantErr }
		if _, err := New("unused", WithDialer(dial)).CallRaw(testContext(t), MethodPing, nil); !errors.Is(err, wantErr) {
			t.Fatalf("error = %v, want %v", err, wantErr)
		}
		if got := conn.closeCount(); got != 1 {
			t.Errorf("underlying Close calls = %d, want 1", got)
		}
	})

	t.Run("late success after cancellation", func(t *testing.T) {
		client, peer := net.Pipe()
		t.Cleanup(func() { _ = peer.Close() })
		conn := &countedCloseConn{ReadWriteCloser: client}
		ctx, cancel := context.WithCancel(context.Background())
		dial := func(context.Context, string) (io.ReadWriteCloser, error) {
			cancel()
			return conn, nil
		}
		_, err := New("unused", WithDialer(dial)).CallRaw(ctx, MethodPing, nil)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context.Canceled", err)
		}
		if got := conn.closeCount(); got != 1 {
			t.Errorf("underlying Close calls = %d, want 1", got)
		}
	})

	for _, tc := range []struct {
		name string
		open func(context.Context, *Client) (io.Closer, error)
	}{
		{
			name: "event stream",
			open: func(ctx context.Context, client *Client) (io.Closer, error) {
				return client.OpenStream(ctx, MethodEventsSubscribe, nil)
			},
		},
		{
			name: "graphics stream",
			open: func(ctx context.Context, client *Client) (io.Closer, error) {
				return client.PaneGraphicsStream(ctx, PaneGraphicsStreamParams{PaneID: "w1:p1"})
			},
		},
	} {
		t.Run(tc.name+" close", func(t *testing.T) {
			conn, dial := newCountedPipe(t, func(server net.Conn) {
				request, _, err := readTransportRequest(server)
				if err == nil {
					_ = writeTransportResult(server, request.ID, `{"type":"ok"}`)
					_, _ = io.Copy(io.Discard, server)
				}
			})
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			stream, err := tc.open(ctx, New("unused", WithDialer(dial)))
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			if err := stream.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			if err := stream.Close(); err != nil {
				t.Fatalf("second Close: %v", err)
			}
			if got := conn.closeCount(); got != 1 {
				t.Errorf("underlying Close calls = %d, want 1", got)
			}
		})
	}
}

type nthShortWriteConn struct {
	io.ReadWriteCloser
	shortAt int

	mu     sync.Mutex
	writes int
}

func (c *nthShortWriteConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	c.writes++
	writeNumber := c.writes
	c.mu.Unlock()
	if writeNumber == c.shortAt {
		if len(p) <= 1 {
			return 0, nil
		}
		return c.ReadWriteCloser.Write(p[:len(p)-1])
	}
	return c.ReadWriteCloser.Write(p)
}

func (c *nthShortWriteConn) writeCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.writes
}

func TestGraphicsStreamShortFrameWriteClosesConnection(t *testing.T) {
	for _, tc := range []struct {
		name    string
		shortAt int
	}{
		{name: "header", shortAt: 2},
		{name: "body", shortAt: 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clientSide, serverSide := net.Pipe()
			short := &nthShortWriteConn{ReadWriteCloser: clientSide, shortAt: tc.shortAt}
			conn := &countedCloseConn{ReadWriteCloser: short}
			serverDone := make(chan struct{})
			go func() {
				defer close(serverDone)
				defer func() { _ = serverSide.Close() }()
				request, reader, err := readTransportRequest(serverSide)
				if err != nil {
					return
				}
				if err := writeTransportResult(serverSide, request.ID, `{"type":"ok"}`); err != nil {
					return
				}
				_, _ = io.Copy(io.Discard, reader)
			}()
			t.Cleanup(func() {
				_ = clientSide.Close()
				_ = serverSide.Close()
				waitTransportSignal(t, serverDone, "graphics server shutdown")
			})
			dial := func(context.Context, string) (io.ReadWriteCloser, error) { return conn, nil }
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			stream, err := New("unused", WithDialer(dial)).PaneGraphicsStream(
				ctx, PaneGraphicsStreamParams{PaneID: "w1:p1"})
			if err != nil {
				t.Fatalf("PaneGraphicsStream: %v", err)
			}

			err = stream.SendFrame(ctx, GraphicsFrame{
				Format: PaneGraphicsFormatRgba, ImageWidth: 1, ImageHeight: 1, Data: []byte{0, 0, 0, 0},
			})
			if !errors.Is(err, io.ErrShortWrite) {
				t.Fatalf("SendFrame error = %v, want io.ErrShortWrite", err)
			}
			writes := short.writeCount()
			if err := stream.SendFrame(ctx, GraphicsFrame{
				Format: PaneGraphicsFormatRgba, ImageWidth: 1, ImageHeight: 1, Data: []byte{0, 0, 0, 0},
			}); !errors.Is(err, ErrStreamClosed) {
				t.Fatalf("second SendFrame error = %v, want ErrStreamClosed", err)
			}
			if got := short.writeCount(); got != writes {
				t.Errorf("writes after closed stream = %d, want %d", got, writes)
			}
			if got := conn.closeCount(); got != 1 {
				t.Errorf("underlying Close calls = %d, want 1", got)
			}
		})
	}
}

func TestSuccessfulStreamOwnsConnectionAfterOpeningContextEnds(t *testing.T) {
	dialer := newTransportDialer(t, func(conn net.Conn) error {
		request, _, err := readTransportRequest(conn)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(conn,
			`{"id":%q,"result":%s}`+"\n"+`{"event":"pane_created","data":{"type":"pane_created","pane_id":"w1:p1"}}`+"\n",
			request.ID, subscriptionStarted)
		if err != nil {
			return err
		}
		_, err = io.Copy(io.Discard, conn)
		return err
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	stream, err := New("unused", WithDialer(dialer.dial)).OpenStream(ctx, MethodEventsSubscribe, nil)
	if err != nil {
		cancel()
		t.Fatalf("OpenStream: %v", err)
	}
	cancel()

	eventCtx, stop := context.WithTimeout(context.Background(), 2*time.Second)
	defer stop()
	event, err := stream.Next(eventCtx)
	if err != nil {
		_ = stream.Close()
		t.Fatalf("Next after opening context cancellation: %v", err)
	}
	if event.Event != "pane_created" {
		t.Errorf("event = %q, want pane_created", event.Event)
	}

	_, closed := dialer.snapshot()
	select {
	case <-closed[0]:
		t.Fatal("opening context cancellation closed the stream")
	default:
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	waitTransportClosed(t, closed[0])
}

func TestSuccessfulGraphicsStreamPreservesBufferedAckAfterOpeningContextEnds(t *testing.T) {
	dialer := newTransportDialer(t, func(conn net.Conn) error {
		request, reader, err := readTransportRequest(conn)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(conn,
			`{"id":%q,"result":{"type":"ok"}}`+"\n"+
				`{"id":%q,"result":{"type":"pane_graphics_frame_ack","sequence":7,"revision":8}}`+"\n",
			request.ID, request.ID+":file:7")
		if err != nil {
			return err
		}
		_, err = reader.ReadBytes('\n')
		return err
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	stream, err := New("unused", WithDialer(dialer.dial)).PaneGraphicsStream(ctx, PaneGraphicsStreamParams{PaneID: "w1:p1"})
	if err != nil {
		cancel()
		t.Fatalf("PaneGraphicsStream: %v", err)
	}
	cancel()

	sendCtx, stop := context.WithTimeout(context.Background(), 2*time.Second)
	defer stop()
	ack, err := stream.SendFileFrame(sendCtx, GraphicsFileFrame{
		Format: PaneGraphicsFormatRgba, Path: "/tmp/frame.raw", Sequence: 7, Revision: 8,
	})
	if err != nil {
		_ = stream.Close()
		t.Fatalf("SendFileFrame after opening context cancellation: %v", err)
	}
	if ack.Sequence != 7 || ack.Revision != 8 {
		t.Errorf("ack = %+v, want sequence 7 revision 8", ack)
	}

	_, closed := dialer.snapshot()
	select {
	case <-closed[0]:
		t.Fatal("opening context cancellation closed the graphics stream")
	default:
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	waitTransportClosed(t, closed[0])
}

func TestEncodingFailureDoesNotDialInjectedTransport(t *testing.T) {
	for _, tc := range []struct {
		name string
		call func(*Client) error
	}{
		{
			name: "ordinary call",
			call: func(client *Client) error {
				_, err := client.CallRaw(context.Background(), MethodPing, unencodable{})
				return err
			},
		},
		{
			name: "event stream",
			call: func(client *Client) error {
				_, err := client.OpenStream(context.Background(), MethodEventsSubscribe, unencodable{})
				return err
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			client := New("unused", WithDialer(func(context.Context, string) (io.ReadWriteCloser, error) {
				called = true
				return nil, errors.New("unexpected dial")
			}))
			if err := tc.call(client); err == nil {
				t.Fatal("encoding failure was not reported")
			}
			if called {
				t.Fatal("dialer called before request encoding succeeded")
			}
		})
	}
}
