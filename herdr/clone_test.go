package herdr

import (
	"reflect"
	"testing"
)

func TestSessionSnapshotCloneOwnsAllNestedData(t *testing.T) {
	t.Run("mutating clone leaves source intact", func(t *testing.T) {
		source := ownershipSnapshot()
		want := ownershipSnapshot()
		cloned := source.Clone()
		if !reflect.DeepEqual(cloned, source) {
			t.Fatal("Clone changed snapshot values")
		}
		mutateOwnedSnapshot(&cloned)
		if !reflect.DeepEqual(source, want) {
			t.Fatal("mutating the clone changed the source snapshot")
		}
	})
	t.Run("mutating source leaves clone intact", func(t *testing.T) {
		source := ownershipSnapshot()
		cloned := source.Clone()
		want := ownershipSnapshot()
		mutateOwnedSnapshot(&source)
		if !reflect.DeepEqual(cloned, want) {
			t.Fatal("mutating the source changed the cloned snapshot")
		}
	})
}

func TestClonePreservesNilAndEmptyValues(t *testing.T) {
	for _, source := range []SessionSnapshot{
		{},
		{
			Agents: []AgentInfo{}, Layouts: []PaneLayoutSnapshot{},
			Panes: []PaneInfo{}, Tabs: []TabInfo{}, Workspaces: []WorkspaceInfo{},
		},
	} {
		if cloned := source.Clone(); !reflect.DeepEqual(source, cloned) {
			t.Errorf("Clone changed nil, empty or optional zero values:\nsource: %#v\nclone: %#v", source, cloned)
		}
	}
}

func TestCloneCopiesUnionAndManualValues(t *testing.T) {
	source := LayoutApplyParams{Root: LayoutNodeSplit{
		First: LayoutNodePane{Cwd: Ptr("first")}, Second: &LayoutNodePane{Cwd: Ptr("second")},
	}}
	cloned := source.Clone().Root.(LayoutNodeSplit)
	*cloned.First.(LayoutNodePane).Cwd = "changed"
	*cloned.Second.(*LayoutNodePane).Cwd = "changed"
	original := source.Root.(LayoutNodeSplit)
	if *original.First.(LayoutNodePane).Cwd != "first" || *original.Second.(*LayoutNodePane).Cwd != "second" {
		t.Fatal("union variant references still alias source")
	}
	typedNil := LayoutApplyParams{Root: (*LayoutNodePane)(nil)}
	if !reflect.DeepEqual(typedNil, typedNil.Clone()) {
		t.Fatal("Clone changed a typed nil union variant")
	}
	filter := AgentViewFilterIn{Values: []AgentViewValue{AgentViewText("source"), AgentViewBool(false)}}
	filterCopy := filter.Clone()
	*filterCopy.Values[0].Text = "changed"
	*filterCopy.Values[1].Bool = true
	if *filter.Values[0].Text != "source" || *filter.Values[1].Bool {
		t.Fatal("manual field adapter did not detach its pointers")
	}
	event := EventEnvelope{Data: &WorkspaceUpdatedEvent{Workspace: WorkspaceInfo{Tokens: map[string]string{"key": "source"}}}}
	eventCopy := event.Clone()
	eventCopy.Data.(*WorkspaceUpdatedEvent).Workspace.Tokens["key"] = "changed"
	if event.Data.(*WorkspaceUpdatedEvent).Workspace.Tokens["key"] != "source" {
		t.Fatal("event interface payload still aliases source")
	}
}
