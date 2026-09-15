// Package plugintest builds the environment Herdr injects into a plugin
// command, so that a handler can be tested by calling it rather than by
// setting a dozen environment variables and running a process.
//
// It is a separate package from plugin so that importing it, and through it
// testing, cannot reach a plugin binary.
package plugintest

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/vika2603/herdr-client/herdr"
	"github.com/vika2603/herdr-client/plugin"
)

// defaultPluginID stands in for HERDR_PLUGIN_ID, which Herdr always sets.
const defaultPluginID = "test.plugin"

// Option configures the environment Env builds. Options apply in order, so a
// later one overrides what an earlier one set.
type Option func(*fake)

// fake collects what the options describe. The invocation context is kept as
// a struct and encoded once, so that an option can set a single field of it.
type fake struct {
	env        plugin.Env
	context    herdr.PluginInvocationContext
	hasContext bool
}

// Env builds the environment of one plugin invocation.
//
// Without options it is a plugin environment with no entrypoint marker and no
// invocation context, which Kind reports as KindUnknown. Every field of the
// result is exported, so anything the options below do not cover can be
// assigned afterwards:
//
//	env := plugintest.Env(plugintest.Action("show"), plugintest.Workspace("ws-1"))
//	env.BinPath = "/usr/local/bin/herdr"
func Env(opts ...Option) *plugin.Env {
	built := fake{env: plugin.Env{PluginID: defaultPluginID}}
	for _, opt := range opts {
		opt(&built)
	}
	if built.hasContext {
		encoded, err := json.Marshal(built.context)
		if err != nil {
			panic("plugintest: encode invocation context: " + err.Error())
		}
		built.env.ContextJSON = encoded
	}
	return &built.env
}

// Startup makes the environment that of a [[startup]] hook.
func Startup() Option {
	return func(f *fake) { f.env.Event = "startup" }
}

// Action makes the environment that of the [[actions]] entry with this id.
func Action(id string) Option {
	return func(f *fake) { f.env.ActionID = id }
}

// PaneCommand makes the environment that of the [[panes]] entry with this id.
func PaneCommand(id string) Option {
	return func(f *fake) { f.env.EntrypointID = id }
}

// EventHook makes the environment that of an [[events]] hook invoked for
// payload, setting both the event name and the envelope Herdr passes in
// HERDR_PLUGIN_EVENT_JSON.
//
// It panics for a payload Herdr never delivers to a hook, which is every
// payload carried only by a dedicated subscription: PaneOutputMatchedEvent
// and PaneScrollChangedEvent have no discriminator, so the hook envelope
// cannot express them. Building one would produce an environment no Herdr
// version can produce.
func EventHook(payload herdr.Event) Option {
	return func(f *fake) {
		name := payload.EventName()
		envelope := herdr.EventEnvelope{Event: herdr.EventKind(underscoreName(name)), Data: payload}
		encoded, err := json.Marshal(envelope)
		if err != nil {
			panic("plugintest: encode event envelope: " + err.Error())
		}
		var decoded herdr.EventEnvelope
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			panic(fmt.Sprintf("plugintest: herdr fires no hook for %s: %s", name, err))
		}
		f.env.Event = name
		f.env.EventJSON = encoded
	}
}

// Context sets the whole invocation context, for a field the options below do
// not cover. Options applied after it override the fields they set.
func Context(invocation herdr.PluginInvocationContext) Option {
	return func(f *fake) {
		f.context = invocation
		f.hasContext = true
	}
}

// Workspace sets the workspace the entrypoint was invoked from, in the
// invocation context and in HERDR_WORKSPACE_ID, as Herdr sets both.
func Workspace(id string) Option {
	return func(f *fake) {
		f.env.WorkspaceID = id
		f.withContext().WorkspaceID = herdr.Some(id)
	}
}

// Tab sets the tab the entrypoint was invoked from.
func Tab(id string) Option {
	return func(f *fake) {
		f.env.TabID = id
		f.withContext().TabID = herdr.Some(id)
	}
}

// Pane sets HERDR_PANE_ID, the pane the entrypoint runs in. The invocation
// context has no pane of its own; see FocusedPane.
func Pane(id string) Option {
	return func(f *fake) { f.env.PaneID = id }
}

// FocusedPane sets the focused pane of the invocation context.
func FocusedPane(id string) Option {
	return func(f *fake) { f.withContext().FocusedPaneID = herdr.Some(id) }
}

// SelectedText sets the terminal selection an action in the "selection"
// context was invoked on.
func SelectedText(text string) Option {
	return func(f *fake) { f.withContext().SelectedText = herdr.Some(text) }
}

// ClickedURL sets the URL a link handler was invoked for, in the invocation
// context and in HERDR_PLUGIN_CLICKED_URL.
func ClickedURL(url string) Option {
	return func(f *fake) {
		f.env.ClickedURL = url
		f.withContext().ClickedURL = herdr.Some(url)
	}
}

// LinkHandler sets the link handler that invoked the action.
func LinkHandler(id string) Option {
	return func(f *fake) {
		f.env.LinkHandlerID = id
		f.withContext().LinkHandlerID = herdr.Some(id)
	}
}

// StateDir sets HERDR_PLUGIN_STATE_DIR, the directory the state helpers of
// Env write to.
func StateDir(dir string) Option {
	return func(f *fake) { f.env.StateDir = dir }
}

// ConfigDir sets HERDR_PLUGIN_CONFIG_DIR.
func ConfigDir(dir string) Option {
	return func(f *fake) { f.env.ConfigDir = dir }
}

// withContext marks the invocation context as present and returns it for a
// single field to be set.
func (f *fake) withContext() *herdr.PluginInvocationContext {
	f.hasContext = true
	return &f.context
}

// underscoreName is the inverse of herdr.EventKind.DotName: the "event" field
// of a hook envelope spells the event with an underscore.
func underscoreName(dotted string) string {
	return strings.Replace(dotted, ".", "_", 1)
}
