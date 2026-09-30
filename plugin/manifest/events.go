package manifest

import "strings"

// schemaEventKinds is the complete EventKind enum of the herdr 0.9.0 API
// schema (schema/herdr-api.schema.json) in underscore form, in schema order.
// It is the set of events the socket API pushes to subscribers, which is wider
// than the set a manifest event hook may name; see hookEventKinds.
var schemaEventKinds = []string{
	"workspace_created",
	"workspace_updated",
	"workspace_metadata_updated",
	"workspace_closed",
	"workspace_renamed",
	"workspace_moved",
	"workspace_reordered",
	"workspace_focused",
	"worktree_created",
	"worktree_opened",
	"worktree_removed",
	"tab_created",
	"tab_closed",
	"tab_renamed",
	"tab_moved",
	"tab_focused",
	"pane_created",
	"pane_closed",
	"pane_updated",
	"pane_focused",
	"pane_moved",
	"pane_output_changed",
	"pane_exited",
	"pane_agent_detected",
	"pane_agent_status_changed",
	"layout_updated",
}

// hookEventKinds is PLUGIN_HOOK_EVENT_KINDS of herdr v0.9.3
// (src/api/schema/events.rs), the events a manifest event hook may name.
// Herdr keeps it narrower than the full EventKind enum until the semantics of
// hooks on high-volume events are settled, and never fires a hook on one of
// the four events left out: workspace_metadata_updated, pane_updated,
// pane_output_changed and layout_updated.
//
// schemaEventKinds stays local to this package; the hook set is published as
// HookEventNames, because a caller holding the manifest cannot otherwise tell
// an event herdr hooks from one it only pushes to subscribers.
var hookEventKinds = []string{
	"workspace_created",
	"workspace_updated",
	"workspace_closed",
	"workspace_renamed",
	"workspace_moved",
	"workspace_reordered",
	"workspace_focused",
	"worktree_created",
	"worktree_opened",
	"worktree_removed",
	"tab_created",
	"tab_closed",
	"tab_renamed",
	"tab_moved",
	"tab_focused",
	"pane_created",
	"pane_closed",
	"pane_focused",
	"pane_moved",
	"pane_exited",
	"pane_agent_detected",
	"pane_agent_status_changed",
}

// HookEventNames returns the dotted names of the events a manifest [[events]]
// hook may name, in schema order. Herdr fires a plugin hook only for these,
// so a handler registered for any other event can never run; see
// plugintest.CheckManifest, which reports that against a registry.
//
// The result is a fresh slice the caller may keep or modify.
func HookEventNames() []string {
	names := make([]string, len(hookEventKinds))
	for i, kind := range hookEventKinds {
		names[i] = dotName(kind)
	}
	return names
}

// dotName converts an EventKind to the dotted name a manifest event hook
// refers to, matching EventKind::dot_name in herdr.
func dotName(kind string) string { return strings.Replace(kind, "_", ".", 1) }

// isHookEventName reports whether name is the dotted form of an event a
// manifest hook may name. Names outside that set, including EventKind values
// herdr excludes from hooks, are reported as a warning by Validate.
func isHookEventName(name string) bool {
	for _, kind := range hookEventKinds {
		if dotName(kind) == name {
			return true
		}
	}
	return false
}
