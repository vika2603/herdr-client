package main

import (
	"context"
	"os"
	"testing"

	"github.com/vika2603/herdr-client/herdr"
	"github.com/vika2603/herdr-client/plugin"
	"github.com/vika2603/herdr-client/plugin/plugintest"
)

func statusChange(paneID, workspaceID string, status herdr.AgentStatus) *herdr.PaneAgentStatusChangedEvent {
	return &herdr.PaneAgentStatusChangedEvent{
		PaneID:       paneID,
		WorkspaceID:  workspaceID,
		DisplayAgent: herdr.Some("claude"),
		AgentStatus:  status,
	}
}

func TestStartupAndEventHooks(t *testing.T) {
	env := plugintest.Env(plugintest.StateDir(t.TempDir()))
	ctx := context.Background()

	if err := onStartup(ctx, env); err != nil {
		t.Fatalf("onStartup() error = %v", err)
	}
	if err := onStatusChanged(ctx, env, statusChange("pane-1", "ws-1", herdr.AgentStatusWorking)); err != nil {
		t.Fatalf("onStatusChanged() error = %v", err)
	}
	if err := onStatusChanged(ctx, env, statusChange("pane-2", "ws-2", herdr.AgentStatusBlocked)); err != nil {
		t.Fatalf("onStatusChanged() error = %v", err)
	}

	records, err := readRecords(env)
	if err != nil {
		t.Fatalf("readRecords() error = %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("records = %+v, want 2", records)
	}
	if records[0].PaneID != "pane-1" || records[0].Status != "working" || records[0].Agent != "claude" {
		t.Errorf("records[0] = %+v", records[0])
	}
	if records[1].WorkspaceID != "ws-2" || records[1].Status != "blocked" {
		t.Errorf("records[1] = %+v", records[1])
	}

	// A new session starts from an empty log.
	if err := onStartup(ctx, env); err != nil {
		t.Fatalf("onStartup() error = %v", err)
	}
	if records, err = readRecords(env); err != nil || len(records) != 0 {
		t.Errorf("readRecords() = %+v, %v, want no records", records, err)
	}
}

func TestHooksNeedAStateDirectory(t *testing.T) {
	env := plugintest.Env()
	ctx := context.Background()

	if err := onStartup(ctx, env); err == nil {
		t.Error("onStartup() error = nil, want one")
	}
	if err := onStatusChanged(ctx, env, statusChange("pane-1", "ws-1", herdr.AgentStatusIdle)); err == nil {
		t.Error("onStatusChanged() error = nil, want one")
	}
	if err := onShow(ctx, env); err == nil {
		t.Error("onShow() error = nil, want one")
	}
}

func TestActionFiltersByInvokingWorkspace(t *testing.T) {
	env := plugintest.Env(
		plugintest.Action(actionShow),
		plugintest.Workspace("ws-2"),
		plugintest.StateDir(t.TempDir()),
	)
	ctx := context.Background()
	for _, change := range []*herdr.PaneAgentStatusChangedEvent{
		statusChange("pane-1", "ws-1", herdr.AgentStatusWorking),
		statusChange("pane-2", "ws-2", herdr.AgentStatusDone),
	} {
		if err := onStatusChanged(ctx, env, change); err != nil {
			t.Fatalf("onStatusChanged() error = %v", err)
		}
	}

	if got := env.Invocation().WorkspaceID; got != "ws-2" {
		t.Errorf("WorkspaceID = %q, want %q", got, "ws-2")
	}
	if err := onShow(ctx, env); err != nil {
		t.Errorf("onShow() error = %v", err)
	}
}

// Invoked outside a workspace, the action prints every record.
func TestActionWithoutAWorkspace(t *testing.T) {
	env := plugintest.Env(plugintest.Action(actionShow), plugintest.StateDir(t.TempDir()))
	ctx := context.Background()
	if err := onStatusChanged(ctx, env, statusChange("pane-1", "ws-1", herdr.AgentStatusWorking)); err != nil {
		t.Fatal(err)
	}

	if got := env.Invocation().WorkspaceID; got != "" {
		t.Errorf("WorkspaceID = %q, want the empty string", got)
	}
	if err := onShow(ctx, env); err != nil {
		t.Errorf("onShow() error = %v", err)
	}
}

func TestReadRecordsSkipsBrokenLines(t *testing.T) {
	env := plugintest.Env(plugintest.StateDir(t.TempDir()))
	content := "{\"pane_id\":\"pane-1\",\"status\":\"idle\"}\nnot json\n\n{\"pane_id\":\"pane-2\",\"status\":\"done\"}\n"
	if err := os.WriteFile(env.StatePath(logName), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	records, err := readRecords(env)
	if err != nil {
		t.Fatalf("readRecords() error = %v", err)
	}
	if len(records) != 2 || records[0].PaneID != "pane-1" || records[1].PaneID != "pane-2" {
		t.Errorf("records = %+v", records)
	}
}

// The registry dispatches an event hook by the name Herdr passes and hands
// the handler the decoded payload, which is what lets main serve three
// entrypoints from one binary.
func TestEventHookReachesTheHandler(t *testing.T) {
	env := plugintest.Env(
		plugintest.EventHook(statusChange("pane-1", "ws-1", herdr.AgentStatusDone)),
		plugintest.StateDir(t.TempDir()),
	)
	if env.Kind() != plugin.KindEvent || env.Event != "pane.agent_status_changed" {
		t.Fatalf("kind = %q, event = %q", env.Kind(), env.Event)
	}

	envelope, err := env.EventEnvelope()
	if err != nil {
		t.Fatalf("EventEnvelope() error = %v", err)
	}
	changed, ok := envelope.Data.(*herdr.PaneAgentStatusChangedEvent)
	if !ok {
		t.Fatalf("payload = %T", envelope.Data)
	}
	if err := onStatusChanged(context.Background(), env, changed); err != nil {
		t.Fatalf("onStatusChanged() error = %v", err)
	}
	records, err := readRecords(env)
	if err != nil || len(records) != 1 || records[0].Status != "done" {
		t.Errorf("records = %+v, %v", records, err)
	}
}
