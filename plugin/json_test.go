package plugin

import (
	"errors"
	"reflect"
	"testing"

	"github.com/vika2603/herdr-client/herdr"
)

func TestEnvContext(t *testing.T) {
	tests := []struct {
		name    string
		env     Env
		want    *herdr.PluginInvocationContext
		wantErr error
		// wantDecodeErr expects a decode failure without a sentinel.
		wantDecodeErr bool
	}{
		{
			name: "full context",
			env:  Env{ContextJSON: []byte(`{"workspace_id":"ws-1","tab_id":"tab-1","focused_pane_status":"working","clicked_url":"https://example.test/issues/1"}`)},
			want: &herdr.PluginInvocationContext{
				WorkspaceID:       herdr.Some("ws-1"),
				TabID:             herdr.Some("tab-1"),
				FocusedPaneStatus: herdr.Some(herdr.AgentStatusWorking),
				ClickedURL:        herdr.Some("https://example.test/issues/1"),
			},
		},
		{
			name: "empty object leaves every field absent",
			env:  Env{ContextJSON: []byte(`{}`)},
			want: &herdr.PluginInvocationContext{},
		},
		{
			name: "unknown fields are ignored",
			env:  Env{ContextJSON: []byte(`{"workspace_id":"ws-1","future_field":42}`)},
			want: &herdr.PluginInvocationContext{WorkspaceID: herdr.Some("ws-1")},
		},
		{
			name: "null decodes into a zero context",
			env:  Env{ContextJSON: []byte(`null`)},
			want: &herdr.PluginInvocationContext{},
		},
		{
			name:    "absent",
			env:     Env{},
			wantErr: ErrNoContext,
		},
		{
			name:          "malformed json",
			env:           Env{ContextJSON: []byte(`{"workspace_id":`)},
			wantDecodeErr: true,
		},
		{
			name:          "wrong field type",
			env:           Env{ContextJSON: []byte(`{"workspace_id":7}`)},
			wantDecodeErr: true,
		},
		{
			name:          "not an object",
			env:           Env{ContextJSON: []byte(`["ws-1"]`)},
			wantDecodeErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.env.Context()
			switch {
			case tt.wantDecodeErr:
				if err == nil {
					t.Fatalf("Context() error = nil, want a decode error")
				}
				if errors.Is(err, ErrNoContext) {
					t.Fatalf("Context() error = %v, want a decode error", err)
				}
			case tt.wantErr != nil:
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("Context() error = %v, want %v", err, tt.wantErr)
				}
			default:
				if err != nil {
					t.Fatalf("Context() error = %v", err)
				}
			}
			if err != nil {
				if got != nil {
					t.Errorf("Context() = %+v, want nil", got)
				}
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Context() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestEnvEventEnvelope(t *testing.T) {
	tests := []struct {
		name          string
		env           Env
		wantKind      herdr.EventKind
		wantData      herdr.Event
		wantErr       error
		wantDecodeErr bool
	}{
		{
			name:     "agent status changed",
			env:      Env{EventJSON: []byte(`{"event":"pane_agent_status_changed","data":{"type":"pane_agent_status_changed","pane_id":"pane-1","workspace_id":"ws-1","agent_status":"working"}}`)},
			wantKind: herdr.EventKindPaneAgentStatusChanged,
			wantData: &herdr.PaneAgentStatusChangedEvent{
				PaneID:      "pane-1",
				WorkspaceID: "ws-1",
				AgentStatus: herdr.AgentStatusWorking,
			},
		},
		{
			name:     "envelope without data",
			env:      Env{EventJSON: []byte(`{"event":"worktree_created"}`)},
			wantKind: herdr.EventKind("worktree_created"),
		},
		{
			name:          "unknown event name",
			env:           Env{EventJSON: []byte(`{"event":"quantum_entangled","data":{"type":"quantum_entangled"}}`)},
			wantDecodeErr: true,
		},
		{
			name:    "absent",
			env:     Env{},
			wantErr: ErrNoEventEnvelope,
		},
		{
			name:    "action environment carries no envelope",
			env:     Env{ActionID: "show", ContextJSON: []byte(`{}`)},
			wantErr: ErrNoEventEnvelope,
		},
		{
			name:          "malformed json",
			env:           Env{EventJSON: []byte(`{"event":`)},
			wantDecodeErr: true,
		},
		{
			name:          "malformed payload",
			env:           Env{EventJSON: []byte(`{"event":"pane_created","data":{"type":"pane_created","pane":"pane-1"}}`)},
			wantDecodeErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.env.EventEnvelope()
			switch {
			case tt.wantDecodeErr:
				if err == nil {
					t.Fatalf("EventEnvelope() error = nil, want a decode error")
				}
				if errors.Is(err, ErrNoEventEnvelope) {
					t.Fatalf("EventEnvelope() error = %v, want a decode error", err)
				}
			case tt.wantErr != nil:
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("EventEnvelope() error = %v, want %v", err, tt.wantErr)
				}
			default:
				if err != nil {
					t.Fatalf("EventEnvelope() error = %v", err)
				}
			}
			if err != nil {
				if got != nil {
					t.Errorf("EventEnvelope() = %+v, want nil", got)
				}
				return
			}
			if got.Event != tt.wantKind {
				t.Errorf("Event = %q, want %q", got.Event, tt.wantKind)
			}
			if !reflect.DeepEqual(got.Data, tt.wantData) {
				t.Errorf("Data = %#v, want %#v", got.Data, tt.wantData)
			}
		})
	}
}

func TestEnvEventEnvelopeUnknownEventCarriesPayload(t *testing.T) {
	env := Env{EventJSON: []byte(`{"event":"quantum_entangled","data":{"type":"quantum_entangled","pane_id":"pane-1"}}`)}

	_, err := env.EventEnvelope()
	var unknown *herdr.UnknownEventError
	if !errors.As(err, &unknown) {
		t.Fatalf("error = %v, want *herdr.UnknownEventError", err)
	}
	if got := string(unknown.Data); got != `{"type":"quantum_entangled","pane_id":"pane-1"}` {
		t.Errorf("Data = %s", got)
	}
}
