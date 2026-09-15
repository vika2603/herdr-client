package herdrtest

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vika2603/herdr-client/herdr"
)

func TestOpenSessionBuffersBootstrapEventAndResyncsFromNewSnapshot(t *testing.T) {
	server := NewServer(t).AllowSubscriptions()
	firstSnapshotEntered := make(chan struct{})
	releaseFirstSnapshot := make(chan struct{})
	var snapshotCalls atomic.Int32
	server.Handle(herdr.MethodSessionSnapshot, func(ctx context.Context, _ Call) (herdr.Result, error) {
		call := snapshotCalls.Add(1)
		if call == 1 {
			close(firstSnapshotEntered)
			select {
			case <-releaseFirstSnapshot:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			return herdr.SessionSnapshotResponse{Snapshot: snapshot("before", "w1", "one")}, nil
		}
		return herdr.SessionSnapshotResponse{Snapshot: snapshot("after", "w2", "two")}, nil
	})

	type openResult struct {
		session *herdr.Session
		err     error
	}
	openDone := make(chan struct{})
	var opened openResult
	bootstrapCtx, bootstrapCancel := context.WithTimeout(t.Context(), testTimeout)
	go func() {
		session, err := herdr.OpenSession(bootstrapCtx, server.Client())
		opened = openResult{session: session, err: err}
		close(openDone)
	}()
	t.Cleanup(func() {
		bootstrapCancel()
		select {
		case <-openDone:
			if opened.session != nil {
				_ = opened.session.Close()
			}
		case <-time.After(testTimeout):
			t.Error("OpenSession did not finish during cleanup")
		}
	})
	firstSub, err := server.WaitSubscription(bootstrapCtx, 0)
	if err != nil {
		t.Fatalf("first WaitSubscription: %v", err)
	}
	receive(bootstrapCtx, t, firstSnapshotEntered, "first snapshot request")
	event := herdr.PaneCreatedEvent{Pane: testPane("w1:p-new")}
	if err := firstSub.Send(bootstrapCtx, event); err != nil {
		t.Fatalf("Send during snapshot: %v", err)
	}
	close(releaseFirstSnapshot)
	receive(bootstrapCtx, t, openDone, "OpenSession")
	if opened.err != nil {
		t.Fatalf("OpenSession: %v", opened.err)
	}
	session := opened.session
	t.Cleanup(func() { _ = session.Close() })

	gotEvent, err := session.Next(bootstrapCtx)
	if err != nil {
		t.Fatalf("Next buffered event: %v", err)
	}
	created, ok := gotEvent.(*herdr.PaneCreatedEvent)
	if !ok || created.Pane.PaneID != "w1:p-new" {
		t.Fatalf("buffered event = %#v", gotEvent)
	}
	if _, ok := session.Pane("w1:p-new"); !ok {
		t.Error("buffered pane.created was not applied to the mirror")
	}
	if got := session.Snapshot().Version; got != "before" {
		t.Errorf("initial snapshot version = %q", got)
	}

	if err := firstSub.Close(); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("close first subscription: %v", err)
	}
	resyncDone := make(chan openResult, 1)
	go func() {
		event, err := session.Next(bootstrapCtx)
		var marker *herdr.ResyncEvent
		if event != nil {
			marker, _ = event.(*herdr.ResyncEvent)
		}
		resyncDone <- openResult{session: session, err: resyncError(marker, err)}
	}()
	secondSub, err := server.WaitSubscription(bootstrapCtx, 1)
	if err != nil {
		t.Fatalf("second WaitSubscription: %v", err)
	}
	if secondSub == firstSub {
		t.Error("reconnection reused the closed subscription")
	}
	if _, err := server.WaitCall(bootstrapCtx, 3); err != nil {
		t.Fatalf("wait for second snapshot: %v", err)
	}
	resynced := receive(bootstrapCtx, t, resyncDone, "session resync")
	if resynced.err != nil {
		t.Fatalf("resync: %v", resynced.err)
	}
	gotSnapshot := session.Snapshot()
	if gotSnapshot.Version != "after" || len(gotSnapshot.Workspaces) != 1 || gotSnapshot.Workspaces[0].WorkspaceID != "w2" {
		t.Errorf("resynced snapshot = %+v", gotSnapshot)
	}
	if _, ok := session.Pane("w1:p-new"); ok {
		t.Error("resync retained a pane absent from the new snapshot")
	}

	if err := session.Close(); err != nil {
		t.Fatalf("Session.Close: %v", err)
	}
	select {
	case <-secondSub.Done():
	case <-bootstrapCtx.Done():
		t.Fatalf("waiting for reconnected subscription cleanup: %v", bootstrapCtx.Err())
	}
	if err := session.Close(); err != nil {
		t.Fatalf("second Session.Close: %v", err)
	}
	if got := snapshotCalls.Load(); got != 2 {
		t.Errorf("snapshot calls = %d, want 2", got)
	}
}

func resyncError(event *herdr.ResyncEvent, err error) error {
	if err != nil {
		return err
	}
	if event == nil {
		return errors.New("Next did not return *herdr.ResyncEvent")
	}
	if event.Cause == nil {
		return errors.New("ResyncEvent.Cause is nil")
	}
	return nil
}

func snapshot(version, workspaceID, label string) herdr.SessionSnapshot {
	return herdr.SessionSnapshot{
		Version:    version,
		Protocol:   herdr.SchemaProtocol,
		Workspaces: []herdr.WorkspaceInfo{{WorkspaceID: workspaceID, Label: label}},
		Tabs:       []herdr.TabInfo{},
		Panes:      []herdr.PaneInfo{},
		Agents:     []herdr.AgentInfo{},
		Layouts:    []herdr.PaneLayoutSnapshot{},
	}
}
