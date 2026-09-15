package herdr

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

func requireOpError(t *testing.T, err error, method string, op Op) {
	t.Helper()
	if err == nil {
		t.Fatal("error = nil, want *OpError")
	}
	var opErr *OpError
	if !errors.As(err, &opErr) {
		t.Fatalf("error = %v (%T), want *OpError", err, err)
	}
	if opErr.Method != method || opErr.Op != op {
		t.Errorf("operation = {%q %q}, want {%q %q}", opErr.Method, opErr.Op, method, op)
	}
	if opErr.Err == nil {
		t.Error("OpError.Err = nil")
	}
}

type operationUnencodable struct{ cause error }

func (v operationUnencodable) MarshalJSON() ([]byte, error) { return nil, v.cause }

type operationConn struct {
	readErr  error
	writeErr error
	closeErr error
}

func (c *operationConn) Read([]byte) (int, error) { return 0, c.readErr }

func (c *operationConn) Write(p []byte) (int, error) {
	if c.writeErr != nil {
		return 0, c.writeErr
	}
	return len(p), nil
}

func (c *operationConn) Close() error { return c.closeErr }

func TestOperationValuesAndUnwrap(t *testing.T) {
	values := map[Op]string{
		OpValidate: "validate",
		OpEncode:   "encode",
		OpDial:     "dial",
		OpWrite:    "write",
		OpRead:     "read",
		OpDecode:   "decode",
		OpClose:    "close",
	}
	for op, want := range values {
		if got := string(op); got != want {
			t.Errorf("string(%q) = %q, want %q", op, got, want)
		}
	}

	cause := errors.New("underlying failure")
	err := &OpError{Method: MethodPing, Op: OpRead, Err: cause}
	if !errors.Is(err, cause) {
		t.Errorf("errors.Is(%v, cause) = false", err)
	}
	if err.Error() == "" {
		t.Error("Error() returned an empty string")
	}
}

func TestClientOperationErrorsIdentifyExchangePhase(t *testing.T) {
	encodeCause := errors.New("cannot marshal params")
	dialCause := &net.OpError{Op: "dial", Net: "unix", Err: errors.New("refused")}
	writeCause := errors.New("request write failed")
	readCause := errors.New("response read failed")

	tests := []struct {
		name   string
		op     Op
		cause  error
		client *Client
		params any
	}{
		{
			name:  "encode",
			op:    OpEncode,
			cause: encodeCause,
			client: New("unused", WithDialer(func(context.Context, string) (io.ReadWriteCloser, error) {
				t.Fatal("dialer called after request encoding failed")
				return nil, nil
			})),
			params: operationUnencodable{cause: encodeCause},
		},
		{
			name:  "dial",
			op:    OpDial,
			cause: dialCause,
			client: New("unused", WithDialer(func(context.Context, string) (io.ReadWriteCloser, error) {
				return nil, dialCause
			})),
		},
		{
			name:  "write",
			op:    OpWrite,
			cause: writeCause,
			client: New("unused", WithDialer(func(context.Context, string) (io.ReadWriteCloser, error) {
				return &operationConn{readErr: io.EOF, writeErr: writeCause}, nil
			})),
		},
		{
			name:  "read",
			op:    OpRead,
			cause: readCause,
			client: New("unused", WithDialer(func(context.Context, string) (io.ReadWriteCloser, error) {
				return &operationConn{readErr: readCause}, nil
			})),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.client.CallRaw(context.Background(), MethodPing, tc.params)
			requireOpError(t, err, MethodPing, tc.op)
			if !errors.Is(err, tc.cause) {
				t.Errorf("errors.Is(%v, cause) = false", err)
			}
			if tc.op == OpDial {
				var netErr *net.OpError
				if !errors.As(err, &netErr) {
					t.Fatalf("error = %v, want *net.OpError cause", err)
				}
			}
		})
	}

	t.Run("decode response envelope", func(t *testing.T) {
		server := newFakeServer(t, func(s *fakeSession) { s.writeLine("not json") })
		_, err := New(server.path).CallRaw(context.Background(), MethodPing, nil)
		requireOpError(t, err, MethodPing, OpDecode)
		var syntaxErr *json.SyntaxError
		if !errors.As(err, &syntaxErr) {
			t.Fatalf("error = %v, want *json.SyntaxError", err)
		}
	})

	t.Run("decode call result", func(t *testing.T) {
		server := replyWith(t, pongResult)
		var result struct {
			Protocol string `json:"protocol"`
		}
		err := New(server.path).Call(context.Background(), MethodPing, nil, &result)
		requireOpError(t, err, MethodPing, OpDecode)
		var typeErr *json.UnmarshalTypeError
		if !errors.As(err, &typeErr) {
			t.Fatalf("error = %v, want *json.UnmarshalTypeError", err)
		}
	})
}

func TestClientOperationErrorsPreserveContextCause(t *testing.T) {
	t.Run("dial", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := New("unused").CallRaw(ctx, MethodPing, nil)
		requireOpError(t, err, MethodPing, OpDial)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context.Canceled", err)
		}
	})

	t.Run("dial deadline", func(t *testing.T) {
		dial := func(ctx context.Context, _ string) (io.ReadWriteCloser, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		_, err := New("unused", WithDialer(dial), WithDialTimeout(5*time.Millisecond)).CallRaw(
			context.Background(), MethodPing, nil)
		requireOpError(t, err, MethodPing, OpDial)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("error = %v, want context.DeadlineExceeded", err)
		}
	})

	t.Run("read", func(t *testing.T) {
		requestRead := make(chan struct{})
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		clientSide, serverSide := net.Pipe()
		t.Cleanup(func() {
			_ = clientSide.Close()
			_ = serverSide.Close()
		})
		go func() {
			if _, _, err := readTransportRequest(serverSide); err == nil {
				close(requestRead)
				_, _ = io.Copy(io.Discard, serverSide)
			}
		}()
		go func() {
			<-requestRead
			cancel()
		}()
		_, err := New("unused", WithDialer(func(context.Context, string) (io.ReadWriteCloser, error) {
			return clientSide, nil
		})).CallRaw(ctx, MethodPing, nil)
		requireOpError(t, err, MethodPing, OpRead)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context.Canceled", err)
		}
	})
}

func TestServerErrorsAreNotOperationErrors(t *testing.T) {
	server := newFakeServer(t, func(s *fakeSession) {
		s.fail(ErrCodePaneNotFound, "missing")
	})
	_, err := New(server.path).PaneGet(context.Background(), PaneTarget{PaneID: "w1:p9"})
	var apiErr *Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %v, want *Error", err)
	}
	var opErr *OpError
	if errors.As(err, &opErr) {
		t.Fatalf("server error was wrapped as operation error: %+v", opErr)
	}
}

func TestGeneratedMethodsAddDecodeOperationContext(t *testing.T) {
	tests := []struct {
		name   string
		method string
		result string
		call   func(*Client) error
		check  func(*testing.T, error)
	}{
		{
			name:   "standard unknown result",
			method: MethodPing,
			result: `{"type":"future_pong"}`,
			call: func(client *Client) error {
				_, err := client.Ping(context.Background())
				return err
			},
			check: func(t *testing.T, err error) {
				var target *UnknownResultError
				if !errors.As(err, &target) {
					t.Fatalf("error = %v, want *UnknownResultError", err)
				}
			},
		},
		{
			name:   "standard malformed result",
			method: MethodPing,
			result: `{"type":"pong","version":"0.9.0","protocol":"wrong"}`,
			call: func(client *Client) error {
				_, err := client.Ping(context.Background())
				return err
			},
			check: func(t *testing.T, err error) {
				var target *json.UnmarshalTypeError
				if !errors.As(err, &target) {
					t.Fatalf("error = %v, want *json.UnmarshalTypeError", err)
				}
			},
		},
		{
			name:   "standard unexpected result",
			method: MethodPing,
			result: `{"type":"ok"}`,
			call: func(client *Client) error {
				_, err := client.Ping(context.Background())
				return err
			},
			check: func(t *testing.T, err error) {
				var target *UnexpectedResultError
				if !errors.As(err, &target) {
					t.Fatalf("error = %v, want *UnexpectedResultError", err)
				}
			},
		},
		{
			name:   "multi result",
			method: MethodPluginPaneOpen,
			result: `{"type":"future_plugin_pane"}`,
			call: func(client *Client) error {
				_, err := client.PluginPaneOpen(context.Background(), PluginPaneOpenParams{
					PluginID: "example", Entrypoint: "main",
				})
				return err
			},
			check: func(t *testing.T, err error) {
				var target *UnknownResultError
				if !errors.As(err, &target) {
					t.Fatalf("error = %v, want *UnknownResultError", err)
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := replyWith(t, tc.result)
			err := tc.call(New(server.path))
			requireOpError(t, err, tc.method, OpDecode)
			tc.check(t, err)
		})
	}
}

func TestStandaloneDecodersKeepTypedErrorsUnwrapped(t *testing.T) {
	tests := []struct {
		name string
		run  func() error
	}{
		{
			name: "result",
			run: func() error {
				_, err := DecodeResult(json.RawMessage(`{"type":"future"}`))
				return err
			},
		},
		{
			name: "event",
			run: func() error {
				_, err := DecodeEvent("future.event", json.RawMessage(`{}`))
				return err
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.run()
			var opErr *OpError
			if errors.As(err, &opErr) {
				t.Fatalf("standalone decoder returned operation context: %+v", opErr)
			}
		})
	}
}

func TestStreamOperationErrors(t *testing.T) {
	t.Run("envelope syntax", func(t *testing.T) {
		server, lines := newStreamServer(t, subscriptionStarted)
		stream := openTestStream(t, server)
		lines <- "not json"
		_, err := stream.Next(context.Background())
		requireOpError(t, err, MethodEventsSubscribe, OpDecode)
		var target *json.SyntaxError
		if !errors.As(err, &target) {
			t.Fatalf("error = %v, want *json.SyntaxError", err)
		}
		lines <- `{"event":"pane_created","data":{"type":"pane_created"}}`
		if _, err := stream.Next(context.Background()); err != nil {
			t.Fatalf("Next after decode error: %v", err)
		}
	})

	t.Run("envelope type", func(t *testing.T) {
		server, lines := newStreamServer(t, subscriptionStarted)
		stream := openTestStream(t, server)
		lines <- `{"event":42,"data":{}}`
		_, err := stream.Next(context.Background())
		requireOpError(t, err, MethodEventsSubscribe, OpDecode)
		var target *json.UnmarshalTypeError
		if !errors.As(err, &target) {
			t.Fatalf("error = %v, want *json.UnmarshalTypeError", err)
		}
	})

	t.Run("unknown event", func(t *testing.T) {
		server, lines := newStreamServer(t, subscriptionStarted)
		stream := openTestStream(t, server)
		lines <- `{"event":"future.event","data":{"value":1}}`
		_, err := stream.NextEvent(context.Background())
		requireOpError(t, err, MethodEventsSubscribe, OpDecode)
		var target *UnknownEventError
		if !errors.As(err, &target) {
			t.Fatalf("error = %v, want *UnknownEventError", err)
		}
	})

	t.Run("event payload", func(t *testing.T) {
		server, lines := newStreamServer(t, subscriptionStarted)
		stream := openTestStream(t, server)
		lines <- `{"event":"pane_closed","data":{"type":"pane_closed","pane_id":42}}`
		_, err := stream.NextEvent(context.Background())
		requireOpError(t, err, MethodEventsSubscribe, OpDecode)
		var target *json.UnmarshalTypeError
		if !errors.As(err, &target) {
			t.Fatalf("error = %v, want *json.UnmarshalTypeError", err)
		}
	})

	t.Run("server EOF", func(t *testing.T) {
		server := newFakeServer(t, func(s *fakeSession) {
			s.success(json.RawMessage(subscriptionStarted))
		})
		stream := openTestStream(t, server)
		_, err := stream.Next(context.Background())
		requireOpError(t, err, MethodEventsSubscribe, OpRead)
		if !errors.Is(err, ErrStreamClosed) || !errors.Is(err, io.EOF) {
			t.Fatalf("error = %v, want ErrStreamClosed and io.EOF", err)
		}
	})

	t.Run("next context", func(t *testing.T) {
		server, lines := newStreamServer(t, subscriptionStarted)
		stream := openTestStream(t, server)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := stream.Next(ctx)
		requireOpError(t, err, MethodEventsSubscribe, OpRead)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context.Canceled", err)
		}
		lines <- `{"event":"pane_created","data":{"type":"pane_created"}}`
		if _, err := stream.Next(context.Background()); err != nil {
			t.Fatalf("Next after cancellation: %v", err)
		}
	})
}

func TestStreamCloseOperationError(t *testing.T) {
	cause := errors.New("close failed")
	clientSide, serverSide := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() { _ = serverSide.Close() }()
		request, _, err := readTransportRequest(serverSide)
		if err == nil {
			_ = writeTransportResult(serverSide, request.ID, subscriptionStarted)
			_, _ = io.Copy(io.Discard, serverSide)
		}
	}()
	t.Cleanup(func() {
		_ = clientSide.Close()
		_ = serverSide.Close()
		waitTransportSignal(t, done, "stream server shutdown")
	})
	conn := &closeErrorConn{ReadWriteCloser: clientSide, err: cause}
	stream, err := New("unused", WithDialer(func(context.Context, string) (io.ReadWriteCloser, error) {
		return conn, nil
	})).OpenStream(testContext(t), MethodEventsSubscribe, nil)
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}
	err = stream.Close()
	requireOpError(t, err, MethodEventsSubscribe, OpClose)
	if !errors.Is(err, cause) {
		t.Fatalf("error = %v, want close cause", err)
	}
}

type closeErrorConn struct {
	io.ReadWriteCloser
	err error
}

func (c *closeErrorConn) Close() error {
	_ = c.ReadWriteCloser.Close()
	return c.err
}

func TestSessionEventDecodeHasSubscriptionContext(t *testing.T) {
	server := newMirrorServer(t, testSnapshot())
	session := openTestSession(t, server)
	stream := server.acceptStream()
	stream.push(`{"event":"future.event","data":{}}`)
	_, err := session.Next(testContext(t))
	requireOpError(t, err, MethodEventsSubscribe, OpDecode)
	var target *UnknownEventError
	if !errors.As(err, &target) {
		t.Fatalf("error = %v, want *UnknownEventError", err)
	}
}

func TestGraphicsValidationOperationErrors(t *testing.T) {
	server := newGraphicsServer(t, graphicsServerConfig{})
	stream := openGraphicsStream(t, server, PaneGraphicsStreamParams{PaneID: "w1:p1"})
	tests := []struct {
		name string
		run  func() error
	}{
		{
			name: "empty inline data",
			run: func() error {
				return stream.SendFrame(context.Background(), GraphicsFrame{})
			},
		},
		{
			name: "empty file path",
			run: func() error {
				_, err := stream.SendFileFrame(context.Background(), GraphicsFileFrame{Format: PaneGraphicsFormatRgba})
				return err
			},
		},
		{
			name: "unsupported file format",
			run: func() error {
				_, err := stream.SendFileFrame(context.Background(), GraphicsFileFrame{
					Format: PaneGraphicsFormatPng, Path: "/tmp/frame.png",
				})
				return err
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			requireOpError(t, tc.run(), MethodPaneGraphicsStream, OpValidate)
		})
	}
}

func TestGraphicsOperationErrors(t *testing.T) {
	t.Run("write frame", func(t *testing.T) {
		clientSide, serverSide := net.Pipe()
		short := &nthShortWriteConn{ReadWriteCloser: clientSide, shortAt: 2}
		done := make(chan struct{})
		go func() {
			defer close(done)
			defer func() { _ = serverSide.Close() }()
			request, reader, err := readTransportRequest(serverSide)
			if err == nil {
				_ = writeTransportResult(serverSide, request.ID, `{"type":"ok"}`)
				_, _ = io.Copy(io.Discard, reader)
			}
		}()
		t.Cleanup(func() {
			_ = clientSide.Close()
			_ = serverSide.Close()
			waitTransportSignal(t, done, "graphics server shutdown")
		})
		stream, err := New("unused", WithDialer(func(context.Context, string) (io.ReadWriteCloser, error) {
			return short, nil
		})).PaneGraphicsStream(testContext(t), PaneGraphicsStreamParams{PaneID: "w1:p1"})
		if err != nil {
			t.Fatalf("PaneGraphicsStream: %v", err)
		}
		err = stream.SendFrame(testContext(t), GraphicsFrame{
			Format: PaneGraphicsFormatRgba, ImageWidth: 1, ImageHeight: 1, Data: []byte{0, 0, 0, 0},
		})
		requireOpError(t, err, MethodPaneGraphicsStream, OpWrite)
		if !errors.Is(err, io.ErrShortWrite) {
			t.Fatalf("error = %v, want io.ErrShortWrite", err)
		}
	})

	t.Run("decode frame acknowledgement", func(t *testing.T) {
		server := newFakeServer(t, func(s *fakeSession) {
			s.success(map[string]string{"type": "ok"})
			s.writeLine("not json")
		})
		stream, err := New(server.path).PaneGraphicsStream(testContext(t), PaneGraphicsStreamParams{PaneID: "w1:p1"})
		if err != nil {
			t.Fatalf("PaneGraphicsStream: %v", err)
		}
		defer func() { _ = stream.Close() }()
		err = stream.Wait(testContext(t))
		requireOpError(t, err, MethodPaneGraphicsStream, OpDecode)
		var target *json.SyntaxError
		if !errors.As(err, &target) {
			t.Fatalf("error = %v, want *json.SyntaxError", err)
		}
	})

	t.Run("server frame error", func(t *testing.T) {
		server := newFakeServer(t, func(s *fakeSession) {
			s.success(map[string]string{"type": "ok"})
			s.fail(ErrCodeStreamClosed, "frame rejected")
		})
		stream, err := New(server.path).PaneGraphicsStream(testContext(t), PaneGraphicsStreamParams{PaneID: "w1:p1"})
		if err != nil {
			t.Fatalf("PaneGraphicsStream: %v", err)
		}
		defer func() { _ = stream.Close() }()
		err = stream.Wait(testContext(t))
		var apiErr *Error
		if !errors.As(err, &apiErr) {
			t.Fatalf("error = %v, want *Error", err)
		}
		var opErr *OpError
		if errors.As(err, &opErr) {
			t.Fatalf("server frame error was wrapped as operation error: %+v", opErr)
		}
	})

	t.Run("read EOF", func(t *testing.T) {
		server := newFakeServer(t, func(s *fakeSession) {
			s.success(map[string]string{"type": "ok"})
		})
		stream, err := New(server.path).PaneGraphicsStream(testContext(t), PaneGraphicsStreamParams{PaneID: "w1:p1"})
		if err != nil {
			t.Fatalf("PaneGraphicsStream: %v", err)
		}
		defer func() { _ = stream.Close() }()
		err = stream.Wait(testContext(t))
		requireOpError(t, err, MethodPaneGraphicsStream, OpRead)
		if !errors.Is(err, ErrStreamClosed) || !errors.Is(err, io.EOF) {
			t.Fatalf("error = %v, want ErrStreamClosed and io.EOF", err)
		}
	})

	t.Run("wait context", func(t *testing.T) {
		server := newGraphicsServer(t, graphicsServerConfig{})
		stream := openGraphicsStream(t, server, PaneGraphicsStreamParams{PaneID: "w1:p1"})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err := stream.Wait(ctx)
		requireOpError(t, err, MethodPaneGraphicsStream, OpRead)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context.Canceled", err)
		}
	})
}

func openGraphicsWithoutFrameAck(t *testing.T) (*GraphicsStream, <-chan struct{}) {
	t.Helper()
	frameRead := make(chan struct{})
	server := newFakeServer(t, func(s *fakeSession) {
		s.success(map[string]string{"type": "ok"})
		if _, err := s.reader.ReadBytes('\n'); err == nil {
			close(frameRead)
			_, _ = io.Copy(io.Discard, s.reader)
		}
	})
	stream, err := New(server.path).PaneGraphicsStream(testContext(t), PaneGraphicsStreamParams{PaneID: "w1:p1"})
	if err != nil {
		t.Fatalf("PaneGraphicsStream: %v", err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	return stream, frameRead
}

func TestGraphicsFileFrameAckWaitOperationErrors(t *testing.T) {
	frame := GraphicsFileFrame{Format: PaneGraphicsFormatRgba, Path: "/tmp/frame.raw"}
	t.Run("context canceled", func(t *testing.T) {
		stream, frameRead := openGraphicsWithoutFrameAck(t)
		ctx, cancel := context.WithCancel(testContext(t))
		defer cancel()
		result := make(chan error, 1)
		finished := make(chan struct{})
		go func() {
			defer close(finished)
			_, err := stream.SendFileFrame(ctx, frame)
			result <- err
		}()
		t.Cleanup(func() {
			cancel()
			_ = stream.Close()
			waitTransportSignal(t, finished, "SendFileFrame shutdown")
		})
		waitTransportSignal(t, frameRead, "file frame")
		cancel()
		select {
		case err := <-result:
			requireOpError(t, err, MethodPaneGraphicsStream, OpRead)
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("error = %v, want context.Canceled", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("SendFileFrame did not stop after cancellation")
		}
	})

	t.Run("stream closed", func(t *testing.T) {
		stream, frameRead := openGraphicsWithoutFrameAck(t)
		ctx := testContext(t)
		result := make(chan error, 1)
		finished := make(chan struct{})
		go func() {
			defer close(finished)
			_, err := stream.SendFileFrame(ctx, frame)
			result <- err
		}()
		t.Cleanup(func() {
			_ = stream.Close()
			waitTransportSignal(t, finished, "SendFileFrame shutdown")
		})
		waitTransportSignal(t, frameRead, "file frame")
		if err := stream.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		select {
		case err := <-result:
			requireOpError(t, err, MethodPaneGraphicsStream, OpRead)
			if !errors.Is(err, ErrStreamClosed) {
				t.Fatalf("error = %v, want ErrStreamClosed", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("SendFileFrame did not stop after Close")
		}
	})
}

func TestGraphicsCloseOperationError(t *testing.T) {
	cause := errors.New("graphics close failed")
	clientSide, serverSide := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() { _ = serverSide.Close() }()
		request, _, err := readTransportRequest(serverSide)
		if err == nil {
			_ = writeTransportResult(serverSide, request.ID, `{"type":"ok"}`)
			_, _ = io.Copy(io.Discard, serverSide)
		}
	}()
	t.Cleanup(func() {
		_ = clientSide.Close()
		_ = serverSide.Close()
		waitTransportSignal(t, done, "graphics server shutdown")
	})
	conn := &closeErrorConn{ReadWriteCloser: clientSide, err: cause}
	stream, err := New("unused", WithDialer(func(context.Context, string) (io.ReadWriteCloser, error) {
		return conn, nil
	})).PaneGraphicsStream(testContext(t), PaneGraphicsStreamParams{PaneID: "w1:p1"})
	if err != nil {
		t.Fatalf("PaneGraphicsStream: %v", err)
	}
	err = stream.Close()
	requireOpError(t, err, MethodPaneGraphicsStream, OpClose)
	if !errors.Is(err, cause) {
		t.Fatalf("error = %v, want close cause", err)
	}
}
