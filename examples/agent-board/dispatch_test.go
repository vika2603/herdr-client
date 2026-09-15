package main

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vika2603/herdr-client/herdr"
	"github.com/vika2603/herdr-client/plugin"
	"github.com/vika2603/herdr-client/plugin/plugintest"
)

func TestDispatchRunsBoardThroughSnapshotEventsResyncAndCancellation(t *testing.T) {
	server := plugintest.NewServer(t).
		AllowSubscriptions().
		Reply(herdr.MethodSessionSnapshot, snapshotResult("ws-1", "initial")).
		Reply(herdr.MethodPaneReportMetadata, herdr.OKResponse{})
	output := newObservedOutput()
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan struct{})
	var dispatchErr error
	go func() {
		defer close(finished)
		dispatchErr = newPluginWithOutput(output).Dispatch(ctx, server.Env(
			plugintest.PaneCommand(paneBoard),
			plugintest.Pane("pane-board"),
		))
	}()
	t.Cleanup(func() {
		cancel()
		waitDone(t, finished, "board dispatch cleanup")
	})

	waitCtx, stopWaiting := context.WithTimeout(context.Background(), 3*time.Second)
	defer stopWaiting()
	first, err := server.WaitSubscription(waitCtx, 0)
	if err != nil {
		t.Fatalf("wait for the initial subscription: %v", err)
	}
	output.waitFor(t, "workspaces 1", "initial")

	// An event from a newer Herdr is ignored without stopping the board or
	// incrementing the count of events its displayed state was built from.
	if err := first.SendRaw(waitCtx, herdr.RawEvent{
		Event: "workspace.archived",
		Data:  json.RawMessage(`{"workspace_id":"ws-future"}`),
	}); err != nil {
		t.Fatalf("send an unknown event: %v", err)
	}
	if err := first.Send(waitCtx, &herdr.WorkspaceCreatedEvent{Workspace: workspace("ws-2", "from event")}); err != nil {
		t.Fatalf("send workspace.created: %v", err)
	}
	output.waitFor(t, "workspaces 2", "events 1", "from event")

	// Replacing the scripted snapshot before dropping the stream makes the
	// reconnect observable: the next frame must come from this fresh state.
	server.Reply(herdr.MethodSessionSnapshot, snapshotResult("ws-3", "after reconnect"))
	if err := first.Close(); err != nil {
		t.Fatalf("close the initial subscription: %v", err)
	}
	second, err := server.WaitSubscription(waitCtx, 1)
	if err != nil {
		t.Fatalf("wait for the reconnected subscription: %v", err)
	}
	resynced := output.waitFor(t,
		"workspaces 1  agents 0  events 0",
		"resynced from a new snapshot after",
		"events.subscribe: read",
		herdr.ErrStreamClosed.Error(),
		"after reconnect",
	)
	if strings.Contains(resynced, "initial") || strings.Contains(resynced, "from event") {
		t.Errorf("resynced frame still contains state from an old snapshot:\n%s", resynced)
	}

	cancel()
	waitDone(t, second.Done(), "reconnected subscription to close")
	waitDone(t, finished, "Dispatch() to stop after cancellation")
	if dispatchErr != nil {
		t.Fatalf("Dispatch() after cancellation = %v, want success", dispatchErr)
	}

	wantMethods := []string{
		herdr.MethodEventsSubscribe,
		herdr.MethodSessionSnapshot,
		herdr.MethodPaneReportMetadata,
		herdr.MethodEventsSubscribe,
		herdr.MethodSessionSnapshot,
	}
	if got := server.Methods(); !slices.Equal(got, wantMethods) {
		t.Errorf("called %v, want %v", got, wantMethods)
	}
}

func TestDispatchReportsBoardAPIErrorAndClosesSubscription(t *testing.T) {
	server := plugintest.NewServer(t).
		AllowSubscriptions().
		Reply(herdr.MethodSessionSnapshot, snapshotResult("ws-1", "initial")).
		Fail(herdr.MethodPaneReportMetadata, herdr.ErrCodePaneNotFound, "board pane is gone")

	dispatchCtx, stopDispatch := context.WithTimeout(context.Background(), 3*time.Second)
	defer stopDispatch()
	err := newPluginWithOutput(newObservedOutput()).Dispatch(
		dispatchCtx,
		server.Env(plugintest.PaneCommand(paneBoard), plugintest.Pane("pane-board")),
	)
	if !herdr.IsCode(err, herdr.ErrCodePaneNotFound) {
		t.Fatalf("Dispatch() = %v, want pane_not_found", err)
	}

	waitCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	subscription, waitErr := server.WaitSubscription(waitCtx, 0)
	if waitErr != nil {
		t.Fatalf("wait for the subscription: %v", waitErr)
	}
	waitDone(t, subscription.Done(), "subscription to close after the API error")
}

func TestDispatchReportsSnapshotAPIErrorAndClosesSubscription(t *testing.T) {
	server := plugintest.NewServer(t).
		AllowSubscriptions().
		Fail(herdr.MethodSessionSnapshot, herdr.ErrCodeInternalError, "snapshot unavailable")

	dispatchCtx, stopDispatch := context.WithTimeout(context.Background(), 3*time.Second)
	defer stopDispatch()
	err := newPluginWithOutput(newObservedOutput()).Dispatch(
		dispatchCtx,
		server.Env(plugintest.PaneCommand(paneBoard)),
	)
	if !herdr.IsCode(err, herdr.ErrCodeInternalError) {
		t.Fatalf("Dispatch() = %v, want internal_error", err)
	}

	waitCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	subscription, waitErr := server.WaitSubscription(waitCtx, 0)
	if waitErr != nil {
		t.Fatalf("wait for the subscription: %v", waitErr)
	}
	waitDone(t, subscription.Done(), "subscription to close after the snapshot error")
}

func TestDispatchOpensBoardPane(t *testing.T) {
	server := plugintest.NewServer(t).
		Reply(herdr.MethodPluginPaneOpen, herdr.OKResponse{})
	env := server.Env(plugintest.Action(actionOpen), plugintest.Workspace("ws-1"))
	if got := env.Kind(); got != plugin.KindAction {
		t.Fatalf("Kind() = %q, want %q", got, plugin.KindAction)
	}
	dispatchCtx, stopDispatch := context.WithTimeout(context.Background(), 3*time.Second)
	defer stopDispatch()
	if err := newPlugin().Dispatch(dispatchCtx, env); err != nil {
		t.Fatalf("Dispatch() = %v", err)
	}

	var params herdr.PluginPaneOpenParams
	if err := json.Unmarshal(server.Calls()[0].Params, &params); err != nil {
		t.Fatalf("decode plugin.pane.open params: %v", err)
	}
	if params.PluginID != env.PluginID || params.Entrypoint != paneBoard {
		t.Errorf("PluginPaneOpenParams = %+v, want plugin %q entrypoint %q", params, env.PluginID, paneBoard)
	}
	if params.Focus == nil || !*params.Focus {
		t.Errorf("PluginPaneOpenParams.Focus = %v, want true", params.Focus)
	}
}

// An entrypoint id the binary does not serve is a dispatch error rather than a
// silent success, which is what keeps the manifest and the registry honest.
func TestDispatchRejectsAnUnservedEntrypoint(t *testing.T) {
	err := newPlugin().Dispatch(context.Background(), plugintest.Env(plugintest.PaneCommand("absent")))
	if err == nil || !strings.Contains(err.Error(), "absent") {
		t.Errorf("Dispatch() error = %v, want it to name the unserved pane id", err)
	}
}

// A pane opened as a popup carries no pane id, so the board cannot name its
// own pane and reports no title instead of failing.
func TestNoTitleWithoutAPaneID(t *testing.T) {
	env := plugintest.Env(plugintest.PaneCommand(paneBoard))
	if err := reportTitle(context.Background(), env.Client(), env); err != nil {
		t.Errorf("reportTitle() error = %v, want no call at all", err)
	}
}

func snapshotResult(workspaceID, label string) herdr.SessionSnapshotResponse {
	return herdr.SessionSnapshotResponse{Snapshot: herdr.SessionSnapshot{
		Version:    "test",
		Protocol:   herdr.SchemaProtocol,
		Workspaces: []herdr.WorkspaceInfo{workspace(workspaceID, label)},
	}}
}

func workspace(id, label string) herdr.WorkspaceInfo {
	return herdr.WorkspaceInfo{
		WorkspaceID: id,
		Label:       label,
		Number:      1,
		AgentStatus: herdr.AgentStatusIdle,
	}
}

// observedOutput is safe to read while the pane handler writes. waitFor is a
// content notification rather than a sleep, so tests advance only after the
// output they exercise is visible.
type observedOutput struct {
	mu      sync.Mutex
	frames  []string
	changed chan struct{}
}

func newObservedOutput() *observedOutput {
	return &observedOutput{changed: make(chan struct{})}
}

func (o *observedOutput) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.frames = append(o.frames, string(p))
	close(o.changed)
	o.changed = make(chan struct{})
	return len(p), nil
}

func (o *observedOutput) waitFor(t *testing.T, fragments ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for {
		o.mu.Lock()
		got := ""
		if len(o.frames) > 0 {
			got = o.frames[len(o.frames)-1]
		}
		changed := o.changed
		o.mu.Unlock()
		matched := true
		for _, fragment := range fragments {
			matched = matched && strings.Contains(got, fragment)
		}
		if matched {
			return got
		}
		select {
		case <-changed:
		case <-ctx.Done():
			t.Fatalf("output did not contain %q; got:\n%s", fragments, got)
		}
	}
}

func waitDone(t *testing.T, done <-chan struct{}, what string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatalf("timed out waiting for %s", what)
	}
}
