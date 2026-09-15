package herdrtest

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vika2603/herdr-client/herdr"
)

const testTimeout = 5 * time.Second

func TestServerFixedAndDynamicResponses(t *testing.T) {
	server := NewServer(t)
	capabilities := &herdr.ServerCapabilities{LiveHandoff: true}
	server.Reply(herdr.MethodPing, herdr.PongResponse{
		Capabilities: capabilities,
		Protocol:     22,
		Version:      "fixed",
	})
	// Reply captures the result when it is registered, including pointed-to
	// data that the test might reuse and mutate later.
	capabilities.LiveHandoff = false

	ctx, cancel := context.WithTimeout(t.Context(), testTimeout)
	defer cancel()
	pong, err := server.Client().Ping(ctx)
	if err != nil {
		t.Fatalf("Ping: %v", err)
	}
	if pong.Version != "fixed" || pong.Protocol != 22 {
		t.Errorf("pong = %+v", pong)
	}
	if pong.Capabilities == nil || !pong.Capabilities.LiveHandoff {
		t.Errorf("capabilities = %+v, want captured live_handoff=true", pong.Capabilities)
	}

	type params struct {
		Value string `json:"value"`
	}
	var mu sync.Mutex
	var values []string
	server.Handle("test.echo", func(_ context.Context, call Call) (herdr.Result, error) {
		var request params
		if err := json.Unmarshal(call.Params, &request); err != nil {
			return nil, err
		}
		mu.Lock()
		values = append(values, request.Value)
		sequence := len(values)
		mu.Unlock()
		return herdr.PongResponse{Version: request.Value, Protocol: uint32(sequence)}, nil
	})

	client := server.Client()
	for index, value := range []string{"first", "second"} {
		var result herdr.PongResponse
		if err := client.Call(ctx, "test.echo", params{Value: value}, &result); err != nil {
			t.Fatalf("Call %d: %v", index, err)
		}
		if result.Version != value || result.Protocol != uint32(index+1) {
			t.Errorf("result %d = %+v", index, result)
		}
	}
	mu.Lock()
	gotValues := append([]string(nil), values...)
	mu.Unlock()
	if !reflect.DeepEqual(gotValues, []string{"first", "second"}) {
		t.Errorf("handler params = %v", gotValues)
	}
	if got := server.Methods(); !reflect.DeepEqual(got, []string{herdr.MethodPing, "test.echo", "test.echo"}) {
		t.Errorf("methods = %v", got)
	}
}

func TestServerRegistrationReplacement(t *testing.T) {
	server := NewServer(t)
	server.Reply(herdr.MethodPing, herdr.PongResponse{Version: "old", Protocol: 1})
	server.Reply(herdr.MethodPing, herdr.PongResponse{Version: "new", Protocol: 2})

	ctx, cancel := context.WithTimeout(t.Context(), testTimeout)
	defer cancel()
	pong, err := server.Client().Ping(ctx)
	if err != nil {
		t.Fatalf("Ping: %v", err)
	}
	if pong.Version != "new" || pong.Protocol != 2 {
		t.Errorf("pong = %+v, want replacement response", pong)
	}

	server.AllowSubscriptions()
	server.Fail(herdr.MethodEventsSubscribe, herdr.ErrCodeStreamConflict, "replacement")
	_, err = server.Client().Subscribe(ctx, herdr.PaneCreatedSubscription{})
	if !herdr.IsCode(err, herdr.ErrCodeStreamConflict) {
		t.Fatalf("Subscribe error = %v, want %s", err, herdr.ErrCodeStreamConflict)
	}
	server.AllowSubscriptions()
	stream, err := server.Client().Subscribe(ctx, herdr.PaneCreatedSubscription{})
	if err != nil {
		t.Fatalf("Subscribe after replacement: %v", err)
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("close stream: %v", err)
	}
}

func TestServerCallsAreIndependentCopies(t *testing.T) {
	server := NewServer(t)
	server.Handle("test.copy", func(_ context.Context, call Call) (herdr.Result, error) {
		for i := range call.Params {
			call.Params[i] = 'x'
		}
		return herdr.OKResponse{}, nil
	})

	ctx, cancel := context.WithTimeout(t.Context(), testTimeout)
	defer cancel()
	if err := server.Client().Call(ctx, "test.copy", struct {
		Name string `json:"name"`
	}{Name: "original"}, nil); err != nil {
		t.Fatalf("Call: %v", err)
	}

	want := `{"name":"original"}`
	calls := server.Calls()
	if len(calls) != 1 || string(calls[0].Params) != want {
		t.Fatalf("calls = %+v, want params %s", calls, want)
	}
	calls[0].Params[0] = 'x'
	if got := string(server.Calls()[0].Params); got != want {
		t.Errorf("Calls mutation changed recorded params to %q", got)
	}
	waited, err := server.WaitCall(ctx, 0)
	if err != nil {
		t.Fatalf("WaitCall: %v", err)
	}
	waited.Params[0] = 'x'
	if got := string(server.Calls()[0].Params); got != want {
		t.Errorf("WaitCall mutation changed recorded params to %q", got)
	}
}

func TestServerReportsAPIHandlerAndUnexpectedRequestErrors(t *testing.T) {
	t.Run("API error", func(t *testing.T) {
		server := NewServer(t)
		server.Fail("pane.inspect", herdr.ErrCodePaneNotFound, "pane w1:p9 not found")
		ctx, cancel := context.WithTimeout(t.Context(), testTimeout)
		defer cancel()

		_, err := server.Client().CallRaw(ctx, "pane.inspect", nil)
		var apiErr *herdr.Error
		if !errors.As(err, &apiErr) {
			t.Fatalf("error = %v, want *herdr.Error", err)
		}
		if apiErr.Method != "pane.inspect" || apiErr.Code != herdr.ErrCodePaneNotFound || apiErr.Message != "pane w1:p9 not found" {
			t.Errorf("API error = %+v", apiErr)
		}
	})

	t.Run("handler error", func(t *testing.T) {
		server := NewServer(t)
		server.Handle("test.failure", func(context.Context, Call) (herdr.Result, error) {
			return nil, errors.New("callback failed")
		})
		ctx, cancel := context.WithTimeout(t.Context(), testTimeout)
		defer cancel()

		_, err := server.Client().CallRaw(ctx, "test.failure", nil)
		var apiErr *herdr.Error
		if !errors.As(err, &apiErr) {
			t.Fatalf("error = %v, want *herdr.Error", err)
		}
		if apiErr.Code != herdr.ErrCodeInternalError || apiErr.Message != "callback failed" {
			t.Errorf("handler error = %+v", apiErr)
		}
	})

	t.Run("unexpected request", func(t *testing.T) {
		server := NewServer(t)
		ctx, cancel := context.WithTimeout(t.Context(), testTimeout)
		defer cancel()

		_, err := server.Client().CallRaw(ctx, "not.scripted", nil)
		var apiErr *herdr.Error
		if !errors.As(err, &apiErr) {
			t.Fatalf("error = %v, want *herdr.Error", err)
		}
		if apiErr.Code != herdr.ErrCodeInvalidRequest || !strings.Contains(apiErr.Message, "not.scripted") {
			t.Errorf("unexpected request error = %+v", apiErr)
		}
	})
}

func TestWaitCallIsIndexedBroadcastAndBounded(t *testing.T) {
	server := NewServer(t)
	server.Reply(herdr.MethodPing, herdr.PongResponse{Version: "test", Protocol: 1})
	ctx, cancel := context.WithTimeout(t.Context(), testTimeout)
	defer cancel()

	type waitResult struct {
		call Call
		err  error
	}
	wait := func(index int) <-chan waitResult {
		result := make(chan waitResult, 1)
		go func() {
			call, err := server.WaitCall(ctx, index)
			result <- waitResult{call: call, err: err}
		}()
		return result
	}
	firstWaiter := wait(0)
	secondWaiter := wait(0)
	if _, err := server.Client().Ping(ctx); err != nil {
		t.Fatalf("first Ping: %v", err)
	}
	for index, waiter := range []<-chan waitResult{firstWaiter, secondWaiter} {
		result := receive(ctx, t, waiter, "WaitCall waiter")
		if result.err != nil || result.call.Method != herdr.MethodPing {
			t.Errorf("waiter %d = %+v", index, result)
		}
	}

	secondCall := wait(1)
	if _, err := server.Client().Ping(ctx); err != nil {
		t.Fatalf("second Ping: %v", err)
	}
	if result := receive(ctx, t, secondCall, "second WaitCall"); result.err != nil || result.call.Method != herdr.MethodPing {
		t.Errorf("second WaitCall = %+v", result)
	}

	canceled, cancelWait := context.WithCancel(ctx)
	cancelWait()
	if _, err := server.WaitCall(canceled, 2); !errors.Is(err, context.Canceled) {
		t.Errorf("WaitCall canceled error = %v", err)
	}
	if _, err := server.WaitCall(ctx, -1); err == nil {
		t.Error("WaitCall accepted a negative index")
	}

	server.Close()
	if _, err := server.WaitCall(ctx, 2); !errors.Is(err, ErrClosed) {
		t.Errorf("WaitCall after Close = %v, want ErrClosed", err)
	}
}

func TestPeerCloseCancelsDynamicHandler(t *testing.T) {
	server := NewServer(t)
	entered := make(chan struct{})
	handlerDone := make(chan error, 1)
	server.Handle("test.block", func(ctx context.Context, _ Call) (herdr.Result, error) {
		close(entered)
		<-ctx.Done()
		handlerDone <- ctx.Err()
		return nil, ctx.Err()
	})

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	callDone := make(chan error, 1)
	go func() {
		_, err := server.Client().CallRaw(ctx, "test.block", nil)
		callDone <- err
	}()
	waitCtx, waitCancel := context.WithTimeout(t.Context(), testTimeout)
	defer waitCancel()
	receive(waitCtx, t, entered, "handler entry")
	cancel()
	if err := receive(waitCtx, t, callDone, "client cancellation"); !errors.Is(err, context.Canceled) {
		t.Errorf("CallRaw error = %v, want context.Canceled", err)
	}
	if err := receive(waitCtx, t, handlerDone, "handler cancellation"); !errors.Is(err, context.Canceled) {
		t.Errorf("handler context error = %v, want context.Canceled", err)
	}
}

func TestServerCloseCancelsAndJoinsCallbacks(t *testing.T) {
	server := NewServer(t)
	entered := make(chan struct{})
	canceled := make(chan struct{})
	release := make(chan struct{})
	exited := make(chan struct{})
	var releaseOnce sync.Once
	releaseHandler := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseHandler)
	server.Handle("test.block", func(ctx context.Context, _ Call) (herdr.Result, error) {
		close(entered)
		<-ctx.Done()
		close(canceled)
		<-release
		close(exited)
		return herdr.OKResponse{}, nil
	})

	callDone := make(chan error, 1)
	go func() {
		_, err := server.Client().CallRaw(t.Context(), "test.block", nil)
		callDone <- err
	}()
	ctx, cancel := context.WithTimeout(t.Context(), testTimeout)
	defer cancel()
	receive(ctx, t, entered, "handler entry")

	closeDone := make(chan struct{})
	go func() {
		server.Close()
		close(closeDone)
	}()
	receive(ctx, t, canceled, "handler cancellation")
	if err := receive(ctx, t, callDone, "client connection close"); err == nil {
		t.Error("CallRaw succeeded while its callback remained blocked during shutdown")
	}
	select {
	case <-closeDone:
		t.Fatal("Close returned before the callback exited")
	default:
	}
	releaseHandler()
	receive(ctx, t, exited, "handler exit")
	receive(ctx, t, closeDone, "server Close")
}

func TestEncodeResultRejectsMissingAndInvalidResults(t *testing.T) {
	tests := []struct {
		name   string
		result herdr.Result
		want   string
	}{
		{name: "nil", want: "nil result"},
		{name: "null", result: nullResult{}, want: "null result"},
		{name: "marshal error", result: invalidResult{}, want: "cannot encode result"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := encodeResult(test.result)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Errorf("encodeResult error = %v, want containing %q", err, test.want)
			}
		})
	}
}

type nullResult struct{}

func (nullResult) ResultType() string           { return "null" }
func (nullResult) MarshalJSON() ([]byte, error) { return []byte("null"), nil }

type invalidResult struct{}

func (invalidResult) ResultType() string           { return "invalid" }
func (invalidResult) MarshalJSON() ([]byte, error) { return nil, errors.New("cannot encode result") }

func receive[T any](ctx context.Context, t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-ctx.Done():
		var zero T
		t.Fatalf("waiting for %s: %v", what, ctx.Err())
		return zero
	}
}
