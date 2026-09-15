package herdrtest

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"reflect"
	"sync"
	"testing"

	"github.com/vika2603/herdr-client/herdr"
)

func TestSubscriptionSendsTypedLifecycleAndScopedEventsInOrder(t *testing.T) {
	server := NewServer(t).AllowSubscriptions()
	ctx, cancel := context.WithTimeout(t.Context(), testTimeout)
	defer cancel()
	stream, err := server.Client().Subscribe(ctx,
		herdr.PaneCreatedSubscription{},
		herdr.PaneOutputMatchedSubscription{
			PaneID: "w1:p1",
			Match:  herdr.OutputMatchSubstring{Value: "ready"},
			Source: herdr.ReadSourceRecent,
		},
		herdr.PaneAgentStatusChangedSubscription{PaneID: "w1:p1"},
		herdr.PaneScrollChangedSubscription{PaneID: "w1:p1"},
	)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer func() { _ = stream.Close() }()
	sub, err := server.WaitSubscription(ctx, 0)
	if err != nil {
		t.Fatalf("WaitSubscription: %v", err)
	}

	call, err := server.WaitCall(ctx, 0)
	if err != nil {
		t.Fatalf("WaitCall: %v", err)
	}
	if call.Method != herdr.MethodEventsSubscribe {
		t.Fatalf("method = %q", call.Method)
	}
	var params herdr.EventsSubscribeParams
	if err := json.Unmarshal(call.Params, &params); err != nil {
		t.Fatalf("decode subscriptions: %v", err)
	}
	if len(params.Subscriptions) != 4 {
		t.Fatalf("subscriptions = %#v", params.Subscriptions)
	}

	events := []herdr.Event{
		&herdr.PaneCreatedEvent{Pane: testPane("w1:p1")},
		&herdr.PaneOutputMatchedEvent{
			MatchedLine: "ready",
			PaneID:      "w1:p1",
			Read: herdr.PaneReadResult{
				Format:      herdr.ReadFormatText,
				PaneID:      "w1:p1",
				Revision:    7,
				Source:      herdr.ReadSourceRecent,
				TabID:       "w1:t1",
				Text:        "ready\n",
				WorkspaceID: "w1",
			},
		},
		&herdr.PaneAgentStatusChangedEvent{
			AgentStatus: herdr.AgentStatusWorking,
			PaneID:      "w1:p1",
			StateLabels: herdr.Some(map[string]string{"phase": "testing"}),
			WorkspaceID: "w1",
		},
		&herdr.PaneScrollChangedEvent{
			PaneID:      "w1:p1",
			Scroll:      herdr.PaneScrollInfo{MaxOffsetFromBottom: 20, OffsetFromBottom: 3, ViewportRows: 40},
			WorkspaceID: "w1",
		},
	}
	for _, event := range events {
		if err := sub.Send(ctx, event); err != nil {
			t.Fatalf("Send %s: %v", event.EventName(), err)
		}
	}
	for index, want := range events {
		got, err := stream.Next(ctx)
		if err != nil {
			t.Fatalf("Next %d: %v", index, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("event %d = %#v, want %#v", index, got, want)
		}
	}
}

func TestSubscriptionSendRawReportsUnknownPayloadAndPreservesStream(t *testing.T) {
	server := NewServer(t).AllowSubscriptions()
	ctx, cancel := context.WithTimeout(t.Context(), testTimeout)
	defer cancel()
	stream, err := server.Client().Subscribe(ctx, herdr.PaneCreatedSubscription{})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer func() { _ = stream.Close() }()
	sub, err := server.WaitSubscription(ctx, 0)
	if err != nil {
		t.Fatalf("WaitSubscription: %v", err)
	}

	data := json.RawMessage(`{"answer":42}`)
	if err := sub.SendRaw(ctx, herdr.RawEvent{Event: "plugin.future_event", Data: data}); err != nil {
		t.Fatalf("SendRaw: %v", err)
	}
	_, err = stream.Next(ctx)
	var unknown *herdr.UnknownEventError
	if !errors.As(err, &unknown) {
		t.Fatalf("Next error = %v, want *herdr.UnknownEventError", err)
	}
	if unknown.Event != "plugin.future_event" || string(unknown.Data) != string(data) {
		t.Errorf("unknown event = %+v", unknown)
	}

	if err := sub.SendRaw(ctx, herdr.RawEvent{Event: "invalid", Data: json.RawMessage(`{`)}); err == nil {
		t.Error("SendRaw accepted invalid JSON data")
	}
	canceled, cancelSend := context.WithCancel(ctx)
	cancelSend()
	if err := sub.SendRaw(canceled, herdr.RawEvent{Event: "ignored", Data: json.RawMessage(`{}`)}); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled SendRaw = %v", err)
	}
	if err := sub.Send(ctx, nil); err == nil {
		t.Error("Send accepted a nil event")
	}
	var typedNil *herdr.PaneCreatedEvent
	if err := sub.Send(ctx, typedNil); err == nil {
		t.Error("Send accepted a typed nil event")
	}
	if err := sub.Send(ctx, herdr.ResyncEvent{Cause: herdr.ErrStreamClosed}); err == nil {
		t.Error("Send accepted a local-only ResyncEvent")
	}

	want := herdr.PaneCreatedEvent{Pane: testPane("w1:p2")}
	if err := sub.Send(ctx, want); err != nil {
		t.Fatalf("Send after rejected writes: %v", err)
	}
	got, err := stream.Next(ctx)
	if err != nil {
		t.Fatalf("Next after rejected writes: %v", err)
	}
	created, ok := got.(*herdr.PaneCreatedEvent)
	if !ok || created.Pane.PaneID != "w1:p2" {
		t.Errorf("event = %#v, want pane.created for w1:p2", got)
	}
}

func TestWaitSubscriptionIsIndexedBroadcastAndBounded(t *testing.T) {
	server := NewServer(t).AllowSubscriptions()
	ctx, cancel := context.WithTimeout(t.Context(), testTimeout)
	defer cancel()
	type waitResult struct {
		sub *Subscription
		err error
	}
	wait := func(index int) <-chan waitResult {
		result := make(chan waitResult, 1)
		go func() {
			sub, err := server.WaitSubscription(ctx, index)
			result <- waitResult{sub: sub, err: err}
		}()
		return result
	}
	firstWaiter := wait(0)
	secondWaiter := wait(0)
	firstStream, err := server.Client().Subscribe(ctx, herdr.PaneCreatedSubscription{})
	if err != nil {
		t.Fatalf("first Subscribe: %v", err)
	}
	defer func() { _ = firstStream.Close() }()
	firstA := receive(ctx, t, firstWaiter, "first subscription waiter")
	firstB := receive(ctx, t, secondWaiter, "second subscription waiter")
	if firstA.err != nil || firstB.err != nil || firstA.sub == nil || firstA.sub != firstB.sub {
		t.Fatalf("subscription waiters = %+v, %+v", firstA, firstB)
	}

	secondWaiterIndexed := wait(1)
	secondStream, err := server.Client().Subscribe(ctx, herdr.PaneScrollChangedSubscription{PaneID: "w1:p1"})
	if err != nil {
		t.Fatalf("second Subscribe: %v", err)
	}
	defer func() { _ = secondStream.Close() }()
	second := receive(ctx, t, secondWaiterIndexed, "indexed subscription waiter")
	if second.err != nil || second.sub == nil || second.sub == firstA.sub {
		t.Fatalf("second subscription = %+v", second)
	}
	if got := server.Methods(); !reflect.DeepEqual(got, []string{herdr.MethodEventsSubscribe, herdr.MethodEventsSubscribe}) {
		t.Errorf("methods = %v", got)
	}

	canceled, cancelWait := context.WithCancel(ctx)
	cancelWait()
	if _, err := server.WaitSubscription(canceled, 2); !errors.Is(err, context.Canceled) {
		t.Errorf("WaitSubscription canceled error = %v", err)
	}
	if err := second.sub.Send(ctx, herdr.PaneScrollChangedEvent{
		PaneID:      "w1:p1",
		WorkspaceID: "w1",
	}); err != nil {
		t.Fatalf("Send after canceled waiter: %v", err)
	}
	if event, err := secondStream.Next(ctx); err != nil || event.EventName() != "pane.scroll_changed" {
		t.Fatalf("Next after canceled waiter = %T, %v", event, err)
	}
	if _, err := server.WaitSubscription(ctx, -1); err == nil {
		t.Error("WaitSubscription accepted a negative index")
	}

	server.Close()
	if _, err := server.WaitSubscription(ctx, 2); !errors.Is(err, ErrClosed) {
		t.Errorf("WaitSubscription after Close = %v, want ErrClosed", err)
	}
	select {
	case <-firstA.sub.Done():
	case <-ctx.Done():
		t.Fatalf("waiting for first subscription cleanup: %v", ctx.Err())
	}
	select {
	case <-second.sub.Done():
	case <-ctx.Done():
		t.Fatalf("waiting for second subscription cleanup: %v", ctx.Err())
	}
}

func TestSubscriptionWireNamesAndPayloads(t *testing.T) {
	server := NewServer(t).AllowSubscriptions()
	ctx, cancel := context.WithTimeout(t.Context(), testTimeout)
	defer cancel()
	stream, err := server.Client().EventsSubscribe(ctx, herdr.EventsSubscribeParams{Subscriptions: []herdr.Subscription{
		herdr.PaneCreatedSubscription{},
		herdr.PaneAgentStatusChangedSubscription{PaneID: "w1:p1"},
	}})
	if err != nil {
		t.Fatalf("EventsSubscribe: %v", err)
	}
	defer func() { _ = stream.Close() }()
	sub, err := server.WaitSubscription(ctx, 0)
	if err != nil {
		t.Fatalf("WaitSubscription: %v", err)
	}

	if err := sub.Send(ctx, herdr.PaneCreatedEvent{Pane: testPane("w1:p1")}); err != nil {
		t.Fatalf("Send pane.created: %v", err)
	}
	lifecycle, err := stream.Next(ctx)
	if err != nil {
		t.Fatalf("Next pane.created: %v", err)
	}
	if lifecycle.Event != "pane_created" {
		t.Errorf("lifecycle envelope name = %q, want pane_created", lifecycle.Event)
	}
	var lifecyclePayload map[string]json.RawMessage
	if err := json.Unmarshal(lifecycle.Data, &lifecyclePayload); err != nil {
		t.Fatalf("decode lifecycle payload: %v", err)
	}
	if got := string(lifecyclePayload["type"]); got != `"pane_created"` {
		t.Errorf("lifecycle payload type = %s", got)
	}

	if err := sub.Send(ctx, herdr.PaneAgentStatusChangedEvent{
		AgentStatus: herdr.AgentStatusWorking,
		PaneID:      "w1:p1",
		WorkspaceID: "w1",
	}); err != nil {
		t.Fatalf("Send pane.agent_status_changed: %v", err)
	}
	scoped, err := stream.Next(ctx)
	if err != nil {
		t.Fatalf("Next pane.agent_status_changed: %v", err)
	}
	if scoped.Event != "pane.agent_status_changed" {
		t.Errorf("scoped envelope name = %q", scoped.Event)
	}
	var scopedPayload map[string]json.RawMessage
	if err := json.Unmarshal(scoped.Data, &scopedPayload); err != nil {
		t.Fatalf("decode scoped payload: %v", err)
	}
	if _, ok := scopedPayload["type"]; ok {
		t.Errorf("scoped payload unexpectedly contains a type discriminator: %s", scoped.Data)
	}
}

func TestBlockedSendCancellationClosesSubscription(t *testing.T) {
	serverConn, peerConn := net.Pipe()
	t.Cleanup(func() { _ = peerConn.Close() })
	writeStarted := make(chan struct{})
	conn := &writeSignalConn{Conn: serverConn, started: writeStarted}
	subCtx, cancelSub := context.WithCancel(t.Context())
	sub := &Subscription{conn: conn, ctx: subCtx, cancel: cancelSub, gate: make(chan struct{}, 1)}
	sub.gate <- struct{}{}
	t.Cleanup(func() { _ = sub.Close() })

	writeCtx, cancelWrite := context.WithCancel(t.Context())
	defer cancelWrite()
	writeDone := make(chan error, 1)
	go func() {
		writeDone <- sub.SendRaw(writeCtx, herdr.RawEvent{Event: "test.event", Data: json.RawMessage(`{"value":1}`)})
	}()
	waitCtx, waitCancel := context.WithTimeout(t.Context(), testTimeout)
	defer waitCancel()
	receive(waitCtx, t, writeStarted, "blocked write entry")
	cancelWrite()
	if err := receive(waitCtx, t, writeDone, "blocked SendRaw cancellation"); !errors.Is(err, context.Canceled) {
		t.Errorf("SendRaw error = %v, want context.Canceled", err)
	}
	select {
	case <-sub.Done():
	case <-waitCtx.Done():
		t.Fatalf("waiting for canceled subscription: %v", waitCtx.Err())
	}
}

func TestCancellationWhileWaitingForWriterPreservesSubscription(t *testing.T) {
	serverConn, peerConn := net.Pipe()
	writeStarted := make(chan struct{})
	conn := &writeSignalConn{Conn: serverConn, started: writeStarted}
	subCtx, cancelSub := context.WithCancel(t.Context())
	sub := &Subscription{conn: conn, ctx: subCtx, cancel: cancelSub, gate: make(chan struct{}, 1)}
	sub.gate <- struct{}{}
	t.Cleanup(func() {
		_ = sub.Close()
		_ = peerConn.Close()
	})
	waitCtx, waitCancel := context.WithTimeout(t.Context(), testTimeout)
	defer waitCancel()

	firstDone := make(chan error, 1)
	go func() {
		firstDone <- sub.SendRaw(waitCtx, herdr.RawEvent{Event: "test.first", Data: json.RawMessage(`{"value":1}`)})
	}()
	receive(waitCtx, t, writeStarted, "first writer entry")

	secondBaseCtx, cancelSecond := context.WithCancel(waitCtx)
	defer cancelSecond()
	secondWaiting := make(chan struct{})
	secondCtx := &observedContext{Context: secondBaseCtx, doneCalled: secondWaiting}
	secondDone := make(chan error, 1)
	go func() {
		secondDone <- sub.SendRaw(secondCtx, herdr.RawEvent{Event: "test.second", Data: json.RawMessage(`{"value":2}`)})
	}()
	receive(waitCtx, t, secondWaiting, "second writer gate wait")
	cancelSecond()
	if err := receive(waitCtx, t, secondDone, "waiting writer cancellation"); !errors.Is(err, context.Canceled) {
		t.Errorf("second SendRaw error = %v, want context.Canceled", err)
	}
	select {
	case <-sub.Done():
		t.Fatal("canceling a writer waiting for the gate closed the subscription")
	default:
	}

	reader := bufio.NewReader(peerConn)
	readDone := make(chan error, 1)
	go func() {
		_, err := reader.ReadBytes('\n')
		readDone <- err
	}()
	if err := receive(waitCtx, t, firstDone, "first writer completion"); err != nil {
		t.Fatalf("first SendRaw: %v", err)
	}
	if err := receive(waitCtx, t, readDone, "first frame read"); err != nil {
		t.Fatalf("read first frame: %v", err)
	}

	thirdDone := make(chan error, 1)
	go func() {
		thirdDone <- sub.SendRaw(waitCtx, herdr.RawEvent{Event: "test.third", Data: json.RawMessage(`{"value":3}`)})
	}()
	readThird := make(chan error, 1)
	go func() {
		_, err := reader.ReadBytes('\n')
		readThird <- err
	}()
	if err := receive(waitCtx, t, thirdDone, "third writer completion"); err != nil {
		t.Fatalf("third SendRaw: %v", err)
	}
	if err := receive(waitCtx, t, readThird, "third frame read"); err != nil {
		t.Fatalf("read third frame: %v", err)
	}
	select {
	case <-sub.Done():
		t.Fatal("subscription closed after subsequent successful write")
	default:
	}
}

type writeSignalConn struct {
	net.Conn
	started chan struct{}
	once    sync.Once
}

type observedContext struct {
	context.Context
	doneCalled chan struct{}
	once       sync.Once
}

func (c *observedContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.doneCalled) })
	return c.Context.Done()
}

func (c *writeSignalConn) Write(data []byte) (int, error) {
	c.once.Do(func() { close(c.started) })
	return c.Conn.Write(data)
}

func TestSubscriptionCloseDisconnectsClient(t *testing.T) {
	server := NewServer(t).AllowSubscriptions()
	ctx, cancel := context.WithTimeout(t.Context(), testTimeout)
	defer cancel()
	stream, err := server.Client().Subscribe(ctx, herdr.PaneCreatedSubscription{})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	sub, err := server.WaitSubscription(ctx, 0)
	if err != nil {
		t.Fatalf("WaitSubscription: %v", err)
	}
	if err := sub.Close(); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("Subscription.Close: %v", err)
	}
	select {
	case <-sub.Done():
	case <-ctx.Done():
		t.Fatalf("waiting for subscription Done: %v", ctx.Err())
	}
	if _, err := stream.Next(ctx); !errors.Is(err, herdr.ErrStreamClosed) {
		t.Errorf("Next after server close = %v, want ErrStreamClosed", err)
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("EventStream.Close: %v", err)
	}
}

func testPane(paneID string) herdr.PaneInfo {
	return herdr.PaneInfo{
		AgentStatus: herdr.AgentStatusIdle,
		PaneID:      paneID,
		TabID:       "w1:t1",
		TerminalID:  "terminal-1",
		WorkspaceID: "w1",
	}
}
