// Command herdr-agent-status is a worked example of a Herdr plugin whose
// entrypoints are all served by one binary: the plugin registry picks the
// handler from the environment Herdr injects.
//
// The event hook appends one record per agent status change to a log in
// HERDR_PLUGIN_STATE_DIR, the startup hook starts a fresh log for each Herdr
// session, and the "show" action prints the log. See README.md.
package main

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/vika2603/herdr-client/herdr"
	"github.com/vika2603/herdr-client/plugin"
)

// actionShow is the action id herdr-plugin.toml declares. The event hook and
// the startup hook need no constant: the event name comes from the payload
// type and there is one startup hook per plugin.
const actionShow = "show"

// logName is the log file inside HERDR_PLUGIN_STATE_DIR.
const logName = "agent-status.jsonl"

// record is one line of the log.
type record struct {
	UnixMs      int64  `json:"unix_ms"`
	WorkspaceID string `json:"workspace_id"`
	PaneID      string `json:"pane_id"`
	Agent       string `json:"agent,omitempty"`
	Status      string `json:"status"`
}

func main() {
	os.Exit(newPlugin().Run(context.Background()))
}

// newPlugin registers one handler per entrypoint of herdr-plugin.toml.
// TestManifest checks the two against each other.
func newPlugin() *plugin.Plugin {
	p := plugin.New()
	p.Startup(onStartup)
	p.Action(actionShow, onShow)
	plugin.OnEvent(p, onStatusChanged)
	return p
}

// onStartup empties the log so it only ever describes the running session.
func onStartup(_ context.Context, env *plugin.Env) error {
	return env.WriteState(logName, nil)
}

// onStatusChanged appends the status change the hook was invoked for.
func onStatusChanged(_ context.Context, env *plugin.Env, changed *herdr.PaneAgentStatusChangedEvent) error {
	return env.AppendStateJSONL(logName, record{
		UnixMs:      time.Now().UnixMilli(),
		WorkspaceID: changed.WorkspaceID,
		PaneID:      changed.PaneID,
		// Prefer the label Herdr displays over the detected agent id.
		Agent:  cmp.Or(changed.DisplayAgent.ValueOrZero(), changed.Agent.ValueOrZero()),
		Status: string(changed.AgentStatus),
	})
}

// onShow prints the log, restricted to the workspace the action was invoked
// from when Herdr named one.
func onShow(_ context.Context, env *plugin.Env) error {
	records, err := readRecords(env)
	if err != nil {
		return err
	}
	workspace := env.Invocation().WorkspaceID

	shown := 0
	for _, entry := range records {
		if workspace != "" && entry.WorkspaceID != workspace {
			continue
		}
		fmt.Printf("%s  %-8s  %-12s  %s\n",
			time.UnixMilli(entry.UnixMs).Format(time.TimeOnly), entry.Status, entry.PaneID, entry.Agent)
		shown++
	}
	if shown == 0 {
		fmt.Println("no agent status changes recorded in this session")
	}
	return nil
}

// readRecords reads the log. A missing log means no event hook has fired yet;
// a line that does not decode is skipped so one bad record cannot hide the
// rest.
func readRecords(env *plugin.Env) ([]record, error) {
	data, err := env.ReadState(logName)
	if err != nil {
		return nil, err
	}
	var records []record
	for _, line := range strings.Split(string(data), "\n") {
		var entry record
		if line == "" || json.Unmarshal([]byte(line), &entry) != nil {
			continue
		}
		records = append(records, entry)
	}
	return records, nil
}
