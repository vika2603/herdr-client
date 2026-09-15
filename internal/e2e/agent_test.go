//go:build e2e

package e2e

import (
	"strings"
	"testing"

	"github.com/vika2603/herdr-client/herdr"
)

const (
	// reportSource identifies the suite as the authority that reports pane
	// metadata, which is what pane.clear_agent_authority later withdraws.
	reportSource = "herdr-client-e2e"
	// reportedAgent is the agent the pane reports itself as running. No
	// agent process is started; agent.start, agent.prompt and
	// agent.send_keys need one and stay out of reach.
	reportedAgent = "claude"
	agentName     = "e2eagent"
)

// stageAgent reports the root pane as an agent through the pane reporting
// methods and then calls the agent methods that read that registration.
func stageAgent(t *testing.T, h *harness, st *state) {
	reported, err := h.client.PaneReportAgent(h.ctx(t), herdr.PaneReportAgentParams{
		PaneID:  st.paneID,
		Source:  reportSource,
		Agent:   reportedAgent,
		State:   herdr.PaneAgentStateIdle,
		Message: herdr.Some("end-to-end verification"),
	})
	if !h.cover(t, herdr.MethodPaneReportAgent, reported, err) {
		t.Fatal("no agent registration to continue with")
	}

	session, err := h.client.PaneReportAgentSession(h.ctx(t), herdr.PaneReportAgentSessionParams{
		PaneID:             st.paneID,
		Source:             reportSource,
		Agent:              reportedAgent,
		AgentSessionID:     herdr.Some("e2e-session"),
		SessionStartSource: herdr.Some(reportSource),
	})
	h.cover(t, herdr.MethodPaneReportAgentSession, session, err)

	metadata, err := h.client.PaneReportMetadata(h.ctx(t), herdr.PaneReportMetadataParams{
		PaneID: st.paneID,
		Source: reportSource,
		Title:  herdr.Some("e2e pane"),
		Tokens: herdr.Some(map[string]*string{"e2e": herdr.Ptr("1")}),
	})
	h.cover(t, herdr.MethodPaneReportMetadata, metadata, err)

	list, err := h.client.AgentList(h.ctx(t))
	if h.cover(t, herdr.MethodAgentList, list, err) {
		seen := false
		for _, agent := range list.Agents {
			seen = seen || agent.PaneID == st.paneID
		}
		if !seen {
			t.Errorf("agent.list is missing the reported pane %s: %+v", st.paneID, list.Agents)
		}
	}

	info, err := h.client.AgentGet(h.ctx(t), herdr.AgentTarget{Target: st.paneID})
	if h.cover(t, herdr.MethodAgentGet, info, err) {
		if info.Agent.PaneID != st.paneID {
			t.Errorf("agent.get returned pane %s, asked for %s", info.Agent.PaneID, st.paneID)
		}
		if agent, ok := info.Agent.Agent.Get(); !ok || agent != reportedAgent {
			t.Errorf("agent.get reports agent %q, the pane reported %s", agent, reportedAgent)
		}
	}

	read, err := h.client.AgentRead(h.ctx(t), herdr.AgentReadParams{
		Target: st.paneID,
		Source: herdr.ReadSourceVisible,
		Format: herdr.Some(herdr.ReadFormatText),
	})
	if h.cover(t, herdr.MethodAgentRead, read, err) && !strings.Contains(read.Read.Text, markerSendInput) {
		t.Errorf("agent.read did not return %s:\n%s", markerSendInput, read.Read.Text)
	}

	explain, err := h.client.AgentExplain(h.ctx(t), herdr.AgentTarget{Target: st.paneID})
	if h.cover(t, herdr.MethodAgentExplain, explain, err) && len(explain.Explain) == 0 {
		t.Errorf("agent.explain returned no explanation")
	}

	renamed, err := h.client.AgentRename(h.ctx(t), herdr.AgentRenameParams{
		Target: st.paneID,
		Name:   herdr.Some(agentName),
	})
	if h.cover(t, herdr.MethodAgentRename, renamed, err) {
		if name, ok := renamed.Agent.Name.Get(); !ok || name != agentName {
			t.Errorf("agent.rename reports name %q", name)
		}
	}

	// The name the rename assigned has to address the same agent.
	focused, err := h.client.AgentFocus(h.ctx(t), herdr.AgentTarget{Target: agentName})
	if h.cover(t, herdr.MethodAgentFocus, focused, err) && focused.Agent.PaneID != st.paneID {
		t.Errorf("agent.focus returned pane %s, expected %s", focused.Agent.PaneID, st.paneID)
	}

	// The reported state is idle, so the wait returns without waiting.
	waited, err := h.client.AgentWait(h.ctx(t), herdr.AgentWaitParams{
		Target:    st.paneID,
		Until:     herdr.Some([]herdr.AgentStatus{herdr.AgentStatusIdle}),
		TimeoutMs: herdr.Some(uint64(10000)),
	})
	if h.cover(t, herdr.MethodAgentWait, waited, err) && waited.Agent.AgentStatus != herdr.AgentStatusIdle {
		t.Errorf("agent.wait returned status %q, waited for idle", waited.Agent.AgentStatus)
	}

	view, err := h.client.AgentViewSet(h.ctx(t), herdr.AgentViewSetParams{
		Source: reportSource,
		Label:  herdr.Some("e2e view"),
		Filter: herdr.Some[herdr.AgentViewFilter](herdr.AgentViewFilterAll{Filters: []herdr.AgentViewFilter{
			herdr.AgentViewFilterExists{Field: herdr.AgentViewFieldOf(herdr.AgentViewBuiltinFieldAgent)},
			herdr.AgentViewFilterEq{
				Field: herdr.AgentViewFieldOf(herdr.AgentViewBuiltinFieldPaneID),
				Value: herdr.AgentViewText(st.paneID),
			},
		}}),
		Sort: herdr.Some([]herdr.AgentViewSort{{
			Field: herdr.AgentViewSortFieldOf(herdr.AgentViewBuiltinSortFieldStatus),
		}}),
	})
	if h.cover(t, herdr.MethodAgentViewSet, view, err) && !view.Active {
		t.Errorf("agent.view.set reports no active view")
	}

	cleared, err := h.client.AgentViewClear(h.ctx(t), herdr.AgentViewClearParams{Source: herdr.Some(reportSource)})
	if h.cover(t, herdr.MethodAgentViewClear, cleared, err) && cleared.Active {
		t.Errorf("agent.view.clear left a view active")
	}
}
