package plugintest_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/vika2603/herdr-client/herdr"
	"github.com/vika2603/herdr-client/plugin"
	"github.com/vika2603/herdr-client/plugin/plugintest"
)

// twoCallPlugin serves one action that splits a pane and then writes to it,
// which is the shape a sequence test has to cover: the second call takes an
// id only the first response carries.
func twoCallPlugin() *plugin.Plugin {
	p := plugin.New()
	p.Action("run", func(ctx context.Context, env *plugin.Env) error {
		client := env.Client()
		split, err := client.PaneSplit(ctx, herdr.PaneSplitParams{Direction: herdr.SplitDirectionRight})
		if err != nil {
			return err
		}
		_, err = client.PaneSendInput(ctx, herdr.PaneSendInputParams{
			PaneID: split.Pane.PaneID,
			Text:   herdr.Some("go test ./..."),
			Keys:   herdr.Some([]string{"enter"}),
		})
		return err
	})
	return p
}

func TestServerAnswersASequenceOfCalls(t *testing.T) {
	server := plugintest.NewServer(t).
		Reply(herdr.MethodPaneSplit, herdr.PaneInfoResponse{Pane: herdr.PaneInfo{PaneID: "w1:p2"}}).
		Reply(herdr.MethodPaneSendInput, herdr.OKResponse{})

	if err := twoCallPlugin().Dispatch(context.Background(), server.Env(plugintest.Action("run"))); err != nil {
		t.Fatalf("Dispatch() = %v", err)
	}

	want := []string{herdr.MethodPaneSplit, herdr.MethodPaneSendInput}
	if got := server.Methods(); !slices.Equal(got, want) {
		t.Fatalf("called %v, want %v", got, want)
	}

	// The second call has to carry the id the first response reported.
	var input herdr.PaneSendInputParams
	if err := json.Unmarshal(server.Calls()[1].Params, &input); err != nil {
		t.Fatalf("decode the pane.send_input params: %v", err)
	}
	if input.PaneID != "w1:p2" {
		t.Errorf("pane.send_input targeted %q, want the pane pane.split reported", input.PaneID)
	}
}

func TestServerReportsAScriptedFailure(t *testing.T) {
	server := plugintest.NewServer(t).
		Fail(herdr.MethodPaneSplit, herdr.ErrCodePaneNotFound, "no such pane")

	err := twoCallPlugin().Dispatch(context.Background(), server.Env(plugintest.Action("run")))
	if !herdr.IsCode(err, herdr.ErrCodePaneNotFound) {
		t.Fatalf("Dispatch() = %v, want a pane_not_found error", err)
	}
	if got := server.Methods(); !slices.Equal(got, []string{herdr.MethodPaneSplit}) {
		t.Errorf("called %v, want the sequence to stop at the failed call", got)
	}
}

// A method nobody scripted is answered rather than left to hang, so the test
// that forgot it reads as a handler error naming the method.
func TestServerRejectsAnUnscriptedCall(t *testing.T) {
	server := plugintest.NewServer(t)

	err := twoCallPlugin().Dispatch(context.Background(), server.Env(plugintest.Action("run")))
	var apiErr *herdr.Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("Dispatch() = %v, want *herdr.Error", err)
	}
	if want := herdr.MethodPaneSplit; !strings.Contains(apiErr.Message, want) {
		t.Errorf("error message = %q, want it to name %s", apiErr.Message, want)
	}
}
