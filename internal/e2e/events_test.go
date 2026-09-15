//go:build e2e

package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/vika2603/herdr-client/herdr"
)

// stageEvents opens a subscription and waits for a single event. Both need a
// change made while the call is open, which a second client performs.
func stageEvents(t *testing.T, h *harness, st *state) {
	stageEventsSubscribe(t, h, st)
	stageEventsWait(t, h, st)
}

// stageEventsSubscribe subscribes to a lifecycle event and to a dedicated
// subscription at once: the two arrive in different envelopes, so decoding
// both proves DecodeEvent handles the dotted and the discriminated form.
func stageEventsSubscribe(t *testing.T, h *harness, st *state) {
	stream, err := h.client.EventsSubscribe(h.ctx(t), herdr.EventsSubscribeParams{
		Subscriptions: []herdr.Subscription{
			herdr.WorkspaceCreatedSubscription{},
			herdr.PaneAgentStatusChangedSubscription{PaneID: st.paneID},
		},
	})
	if !h.coverStream(t, herdr.MethodEventsSubscribe, stream, err) {
		return
	}
	defer func() { _ = stream.Close() }()

	created, err := h.trigger.WorkspaceCreate(h.ctx(t), herdr.WorkspaceCreateParams{
		Label: herdr.Some("e2e-events"),
		Cwd:   herdr.Some(h.root),
		Focus: herdr.Some(false),
	})
	if err != nil {
		t.Fatalf("trigger workspace.created: %v", err)
	}
	if _, err := h.trigger.PaneReportAgent(h.ctx(t), herdr.PaneReportAgentParams{
		PaneID: st.paneID,
		Source: reportSource,
		Agent:  reportedAgent,
		State:  herdr.PaneAgentStateWorking,
	}); err != nil {
		t.Fatalf("trigger pane.agent_status_changed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var sawWorkspace, sawStatus bool
	for !sawWorkspace || !sawStatus {
		event, err := stream.NextEvent(ctx)
		if err != nil {
			t.Fatalf("events.subscribe: workspace event seen: %v, status event seen: %v: %v",
				sawWorkspace, sawStatus, err)
		}
		switch payload := event.(type) {
		case *herdr.WorkspaceCreatedEvent:
			sawWorkspace = true
			if payload.Workspace.WorkspaceID != created.Workspace.WorkspaceID {
				t.Errorf("workspace.created reports %s, created %s",
					payload.Workspace.WorkspaceID, created.Workspace.WorkspaceID)
			}
		case *herdr.PaneAgentStatusChangedEvent:
			sawStatus = true
			if payload.PaneID != st.paneID {
				t.Errorf("pane.agent_status_changed reports pane %s, subscribed to %s",
					payload.PaneID, st.paneID)
			}
		default:
			t.Errorf("events.subscribe pushed %T, subscribed to two event kinds", event)
		}
	}

	if _, err := h.trigger.WorkspaceClose(h.ctx(t), herdr.WorkspaceCloseParams{
		WorkspaceID: created.Workspace.WorkspaceID,
	}); err != nil {
		t.Errorf("close the workspace the subscription triggered: %v", err)
	}
}

// stageEventsWait blocks in events.wait until a second client reports the
// status the call matches on.
func stageEventsWait(t *testing.T, h *harness, st *state) {
	triggered := make(chan error, 1)
	go func() {
		time.Sleep(300 * time.Millisecond)
		ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
		defer cancel()
		_, err := h.trigger.PaneReportAgent(ctx, herdr.PaneReportAgentParams{
			PaneID: st.paneID,
			Source: reportSource,
			Agent:  reportedAgent,
			State:  herdr.PaneAgentStateBlocked,
		})
		triggered <- err
	}()

	matched, err := h.client.EventsWait(h.ctx(t), herdr.EventsWaitParams{
		MatchEvent: herdr.EventMatchPaneAgentStatusChanged{
			PaneID:      st.paneID,
			AgentStatus: herdr.AgentStatusBlocked,
		},
		TimeoutMs: herdr.Some(uint64(15000)),
	})
	if err := <-triggered; err != nil {
		t.Fatalf("trigger the awaited status change: %v", err)
	}
	if !h.cover(t, herdr.MethodEventsWait, matched, err) {
		return
	}
	status, ok := matched.Event.Data.(*herdr.PaneAgentStatusChangedEvent)
	if !ok {
		t.Fatalf("events.wait matched %T, waited for a pane agent status change", matched.Event.Data)
	}
	if status.AgentStatus != herdr.AgentStatusBlocked {
		t.Errorf("events.wait matched status %q, waited for blocked", status.AgentStatus)
	}
}
