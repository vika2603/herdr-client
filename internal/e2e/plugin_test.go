//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/vika2603/herdr-client/herdr"
)

const (
	fixturePluginID = "e2e.fixture"
	fixtureActionID = "noop"
	fixturePaneID   = "board"
)

// pluginManifestTemplate is the herdr-plugin.toml the suite links, holding one
// action and one pane entrypoint. The commands run through the same /bin/sh
// the harness configures for panes, so the manifest declares only the
// platforms that shell exists on.
//
// The pane command has to outlive the calls made on the pane it starts:
// herdr deregisters a plugin pane the moment its command exits, and
// plugin.pane.focus then answers plugin_pane_not_found. The duration bounds a
// pane that escapes the suite; nothing waits for it.
const pluginManifestTemplate = `id = %q
name = "E2E Fixture"
version = "0.1.0"
min_herdr_version = "0.9.0"
description = "Fixture plugin the herdr-client end-to-end suite links"
platforms = ["linux", "macos"]

[[actions]]
id = %q
title = "Do nothing"
contexts = ["global"]
command = ["/bin/sh", "-c", "echo e2e-action"]

[[panes]]
id = %q
title = "E2E Board"
placement = "overlay"
command = ["/bin/sh", "-c", "sleep 300"]
`

// stagePlugin links a fixture plugin and drives every plugin method against
// it: the registry, the action and its command log, and the panes the
// manifest declares.
//
// The link is safe because the plugin registry follows XDG_CONFIG_HOME, which
// the harness points at its temporary root, and because the manifest the
// suite links is one it wrote inside that root. The registry of whoever runs
// the suite is asserted unchanged around the link, so the isolation is
// checked rather than trusted.
func stagePlugin(t *testing.T, h *harness, st *state) {
	h.assertCallerRegistryUnchanged(t)
	dir := writePluginFixture(t, h)

	linked, err := h.client.PluginLink(h.ctx(t), herdr.PluginLinkParams{
		Path:    dir,
		Enabled: herdr.Some(true),
	})
	if !h.cover(t, herdr.MethodPluginLink, linked, err) {
		t.Fatal("no linked plugin to continue with")
	}
	h.assertCallerRegistryUnchanged(t)
	if linked.Plugin.PluginID != fixturePluginID || !linked.Plugin.Enabled {
		t.Fatalf("plugin.link returned %+v, expected %s enabled", linked.Plugin, fixturePluginID)
	}
	manifestActions := linked.Plugin.Actions.ValueOrZero()
	manifestPanes := linked.Plugin.Panes.ValueOrZero()
	if len(manifestActions) != 1 || len(manifestPanes) != 1 {
		t.Errorf("plugin.link read %d actions and %d panes out of the manifest, expected one of each",
			len(manifestActions), len(manifestPanes))
	}

	list, err := h.client.PluginList(h.ctx(t), herdr.PluginListParams{PluginID: herdr.Some(fixturePluginID)})
	if h.cover(t, herdr.MethodPluginList, list, err) {
		if len(list.Plugins) != 1 || list.Plugins[0].PluginID != fixturePluginID {
			t.Errorf("plugin.list returned %+v, expected the linked fixture alone", list.Plugins)
		}
	}

	actions, err := h.client.PluginActionList(h.ctx(t), herdr.PluginActionListParams{PluginID: herdr.Some(fixturePluginID)})
	if h.cover(t, herdr.MethodPluginActionList, actions, err) {
		if len(actions.Actions) != 1 || actions.Actions[0].ActionID != fixtureActionID {
			t.Errorf("plugin.action.list returned %+v, expected the action the manifest declares", actions.Actions)
		}
	}

	logID := invokePluginAction(t, h)
	assertPluginLog(t, h, logID)

	disabled, err := h.client.PluginDisable(h.ctx(t), herdr.PluginSetEnabledParams{PluginID: fixturePluginID})
	if h.cover(t, herdr.MethodPluginDisable, disabled, err) && disabled.Plugin.Enabled {
		t.Errorf("plugin.disable left %s enabled", fixturePluginID)
	}

	enabled, err := h.client.PluginEnable(h.ctx(t), herdr.PluginSetEnabledParams{PluginID: fixturePluginID})
	if h.cover(t, herdr.MethodPluginEnable, enabled, err) && !enabled.Plugin.Enabled {
		t.Errorf("plugin.enable left %s disabled", fixturePluginID)
	}

	stagePluginPanes(t, h, st)

	unlinked, err := h.client.PluginUnlink(h.ctx(t), herdr.PluginUnlinkParams{PluginID: fixturePluginID})
	if h.cover(t, herdr.MethodPluginUnlink, unlinked, err) && !unlinked.Removed {
		t.Errorf("plugin.unlink removed nothing for %s", fixturePluginID)
	}
	h.assertCallerRegistryUnchanged(t)
}

// stagePluginPanes opens the pane entrypoint the manifest declares in the two
// placements a server without an attached client accepts, and closes each pane
// again.
func stagePluginPanes(t *testing.T, h *harness, st *state) {
	overlay := openPluginPane(t, h, herdr.PluginPaneOpenParams{
		PluginID:   fixturePluginID,
		Entrypoint: fixturePaneID,
	})
	if overlay == "" {
		return
	}

	focused, err := h.client.PluginPaneFocus(h.ctx(t), herdr.PluginPaneFocusParams{PaneID: overlay})
	if h.cover(t, herdr.MethodPluginPaneFocus, focused, err) {
		if focused.PluginPane.Pane.PaneID != overlay || focused.PluginPane.Entrypoint != fixturePaneID {
			t.Errorf("plugin.pane.focus returned %+v, asked for %s", focused.PluginPane, overlay)
		}
	}
	closePluginPane(t, h, overlay)

	// A split plugin pane takes the pane it splits and nothing else: herdr
	// rejects a workspace_id or a direction beside target_pane_id with
	// invalid_params.
	split := openPluginPane(t, h, herdr.PluginPaneOpenParams{
		PluginID:     fixturePluginID,
		Entrypoint:   fixturePaneID,
		Placement:    herdr.Some(herdr.PluginPanePlacementSplit),
		TargetPaneID: herdr.Some(st.paneID),
	})
	if split != "" {
		closePluginPane(t, h, split)
	}
}

// writePluginFixture writes the manifest inside the harness root and returns
// the directory plugin.link is pointed at. Staying inside that root is what
// keeps the link out of the registry of the caller.
func writePluginFixture(t *testing.T, h *harness) string {
	t.Helper()
	dir := filepath.Join(h.root, "plugin")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("create the plugin fixture directory: %v", err)
	}
	manifest := fmt.Sprintf(pluginManifestTemplate, fixturePluginID, fixtureActionID, fixturePaneID)
	if err := os.WriteFile(filepath.Join(dir, "herdr-plugin.toml"), []byte(manifest), 0o600); err != nil {
		t.Fatalf("write the plugin fixture manifest: %v", err)
	}
	return dir
}

// invokePluginAction runs the action the manifest declares and returns the id
// of the command log entry the server started for it.
func invokePluginAction(t *testing.T, h *harness) string {
	t.Helper()
	invoked, err := h.client.PluginActionInvoke(h.ctx(t), herdr.PluginActionInvokeParams{
		ActionID: fixtureActionID,
		PluginID: herdr.Some(fixturePluginID),
	})
	if !h.cover(t, herdr.MethodPluginActionInvoke, invoked, err) {
		return ""
	}
	if invoked.Action.ActionID != fixtureActionID || invoked.Log.PluginID != fixturePluginID {
		t.Errorf("plugin.action.invoke returned action %+v with log %+v", invoked.Action, invoked.Log)
	}
	return invoked.Log.LogID
}

// assertPluginLog checks that the listing holds the entry the invocation
// started. plugin.action.invoke answers with that entry, so it exists by the
// time the listing runs and nothing waits for the command to finish.
func assertPluginLog(t *testing.T, h *harness, logID string) {
	t.Helper()
	logs, err := h.client.PluginLogList(h.ctx(t), herdr.PluginLogListParams{
		PluginID: herdr.Some(fixturePluginID),
		Limit:    herdr.Some(uint64(10)),
	})
	if !h.cover(t, herdr.MethodPluginLogList, logs, err) || logID == "" {
		return
	}
	for _, entry := range logs.Logs {
		if entry.LogID == logID {
			return
		}
	}
	t.Errorf("plugin.log.list does not hold the entry %s the invocation started: %+v", logID, logs.Logs)
}

// openPluginPane calls plugin.pane.open and returns the pane the server
// reported. The method answers either plugin_pane_opened or ok, so the result
// is a union and only the first variant carries a pane.
func openPluginPane(t *testing.T, h *harness, params herdr.PluginPaneOpenParams) string {
	t.Helper()
	result, err := h.client.PluginPaneOpen(h.ctx(t), params)
	if !h.cover(t, herdr.MethodPluginPaneOpen, result, err) {
		return ""
	}
	opened, ok := result.(*herdr.PluginPaneOpenedResponse)
	if !ok {
		t.Errorf("plugin.pane.open answered %s, which carries no pane", result.ResultType())
		return ""
	}
	if opened.PluginPane.PluginID != fixturePluginID || opened.PluginPane.Pane.PaneID == "" {
		t.Errorf("plugin.pane.open returned %+v", opened.PluginPane)
		return ""
	}
	return opened.PluginPane.Pane.PaneID
}

func closePluginPane(t *testing.T, h *harness, paneID string) {
	t.Helper()
	closed, err := h.client.PluginPaneClose(h.ctx(t), herdr.PluginPaneCloseParams{PaneID: paneID})
	if h.cover(t, herdr.MethodPluginPaneClose, closed, err) && closed.PaneID != paneID {
		t.Errorf("plugin.pane.close returned %s, asked for %s", closed.PaneID, paneID)
	}
}
