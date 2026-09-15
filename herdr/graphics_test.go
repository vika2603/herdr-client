package herdr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// openGraphicsStream opens a stream against server and closes it in cleanup.
func openGraphicsStream(t *testing.T, server *graphicsServer, params PaneGraphicsStreamParams) *GraphicsStream {
	t.Helper()
	stream, err := New(server.path).PaneGraphicsStream(testContext(t), params)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	return stream
}

func TestPaneGraphicsStreamSendsParams(t *testing.T) {
	server := newGraphicsServer(t, graphicsServerConfig{})
	openGraphicsStream(t, server, PaneGraphicsStreamParams{
		PaneID:  "w1:p1",
		LayerID: Some("overlay"),
		ZIndex:  Some(int32(3)),
	})

	req := server.request(0)
	if req.Method != "pane.graphics.stream" {
		t.Errorf("method = %q, want pane.graphics.stream", req.Method)
	}
	var params map[string]any
	if err := json.Unmarshal(req.Params, &params); err != nil {
		t.Fatalf("decode params: %v", err)
	}
	want := map[string]any{"pane_id": "w1:p1", "layer_id": "overlay", "z_index": float64(3)}
	for key, value := range want {
		if params[key] != value {
			t.Errorf("params[%q] = %v, want %v", key, params[key], value)
		}
	}
	if len(params) != len(want) {
		t.Errorf("params = %v, want exactly %v", params, want)
	}
}

func TestPaneGraphicsStreamOmitsUnsetParams(t *testing.T) {
	server := newGraphicsServer(t, graphicsServerConfig{})
	openGraphicsStream(t, server, PaneGraphicsStreamParams{PaneID: "w1:p1"})

	var params map[string]any
	if err := json.Unmarshal(server.request(0).Params, &params); err != nil {
		t.Fatalf("decode params: %v", err)
	}
	if len(params) != 1 || params["pane_id"] != "w1:p1" {
		t.Errorf("params = %v, want only pane_id", params)
	}
}

func TestPaneGraphicsStreamReportsRefusedOpen(t *testing.T) {
	server := newGraphicsServer(t, graphicsServerConfig{openError: ErrCodeFeatureDisabled})

	_, err := New(server.path).PaneGraphicsStream(testContext(t), PaneGraphicsStreamParams{PaneID: "w1:p1"})
	if !IsCode(err, ErrCodeFeatureDisabled) {
		t.Fatalf("open error = %v, want %s", err, ErrCodeFeatureDisabled)
	}
}

func TestPaneGraphicsStreamRejectsOtherAck(t *testing.T) {
	server := newFakeServer(t, func(s *fakeSession) {
		s.success(map[string]string{"type": "subscription_started"})
	})

	_, err := New(server.path).PaneGraphicsStream(testContext(t), PaneGraphicsStreamParams{PaneID: "w1:p1"})
	var unexpected *UnexpectedResultError
	if !errors.As(err, &unexpected) {
		t.Fatalf("open error = %v, want *UnexpectedResultError", err)
	}
	if unexpected.Got != "subscription_started" {
		t.Errorf("got = %q, want subscription_started", unexpected.Got)
	}
}

func TestGraphicsStreamSendFrameFraming(t *testing.T) {
	server := newGraphicsServer(t, graphicsServerConfig{})
	stream := openGraphicsStream(t, server, PaneGraphicsStreamParams{PaneID: "w1:p1"})
	ctx := testContext(t)

	first := bytes.Repeat([]byte{0x01, 0x02, 0x03, 0x04}, 64*1024)
	second := []byte("second frame")
	frames := []GraphicsFrame{
		{
			Format:      PaneGraphicsFormatRgba,
			ImageWidth:  512,
			ImageHeight: 128,
			Data:        first,
			Placement:   Some(PaneGraphicsPlacementParams{GridCols: Some(uint32(80)), GridRows: Some(uint32(24))}),
		},
		{Format: PaneGraphicsFormatPng, ImageWidth: 4, ImageHeight: 4, Data: second},
	}
	for i, frame := range frames {
		if err := stream.SendFrame(ctx, frame); err != nil {
			t.Fatalf("send frame %d: %v", i, err)
		}
	}
	// The second frame's header can only be read once the first frame's body
	// has been consumed in full, so seeing it proves the framing.
	waitForFrames(t, server, 2)

	captured := server.captured()
	if got := captured[0].header; got.Format != PaneGraphicsFormatRgba ||
		got.ImageWidth != 512 || got.ImageHeight != 128 ||
		got.DataLength.ValueOrZero() != len(first) {
		t.Errorf("frame 0 header = %+v", got)
	}
	if placement, ok := captured[0].header.Placement.Get(); !ok ||
		placement.GridCols.ValueOrZero() != 80 || placement.GridRows.ValueOrZero() != 24 {
		t.Errorf("frame 0 placement = %+v", captured[0].header.Placement)
	}
	if !bytes.Equal(captured[0].data, first) {
		t.Errorf("frame 0 carried %d bytes, want %d", len(captured[0].data), len(first))
	}
	if !bytes.Equal(captured[1].data, second) {
		t.Errorf("frame 1 data = %q, want %q", captured[1].data, second)
	}
	if captured[1].header.Placement.IsSet() {
		t.Errorf("frame 1 placement = %+v, want none", captured[1].header.Placement)
	}
	if captured[1].header.File.IsSet() {
		t.Errorf("frame 1 file = %+v, want none", captured[1].header.File)
	}
}

func TestGraphicsStreamSendFrameRejectsUnsendableData(t *testing.T) {
	server := newGraphicsServer(t, graphicsServerConfig{})
	stream := openGraphicsStream(t, server, PaneGraphicsStreamParams{PaneID: "w1:p1"})
	ctx := testContext(t)

	if err := stream.SendFrame(ctx, GraphicsFrame{Format: PaneGraphicsFormatRgba}); err == nil {
		t.Error("empty frame accepted")
	}
	oversized := GraphicsFrame{
		Format: PaneGraphicsFormatRgba,
		Data:   make([]byte, PaneGraphicsStreamMaxBytes+1),
	}
	if err := stream.SendFrame(ctx, oversized); err == nil {
		t.Error("oversized frame accepted")
	}
	if got := len(server.captured()); got != 0 {
		t.Errorf("server read %d frames, want 0", got)
	}
}

func TestGraphicsStreamSendFileFrameReturnsAck(t *testing.T) {
	server := newGraphicsServer(t, graphicsServerConfig{})
	stream := openGraphicsStream(t, server, PaneGraphicsStreamParams{PaneID: "w1:p1"})

	ack, err := stream.SendFileFrame(testContext(t), GraphicsFileFrame{
		Format:      PaneGraphicsFormatBgra,
		ImageWidth:  8,
		ImageHeight: 8,
		Path:        "/tmp/frame.raw",
		Sequence:    7,
		Revision:    8,
	})
	if err != nil {
		t.Fatalf("send file frame: %v", err)
	}
	if ack.Sequence != 7 || ack.Revision != 8 {
		t.Errorf("ack = %+v, want sequence 7 revision 8", ack)
	}

	captured := server.captured()
	if len(captured) != 1 {
		t.Fatalf("server read %d frames, want 1", len(captured))
	}
	header := captured[0].header
	file, ok := header.File.Get()
	if !ok || file.Path != "/tmp/frame.raw" {
		t.Errorf("header file = %+v", header.File)
	}
	if header.DataLength.IsSet() {
		t.Errorf("header data_length = %v, want none", header.DataLength)
	}
	if len(captured[0].data) != 0 {
		t.Errorf("file frame carried %d body bytes, want 0", len(captured[0].data))
	}
}

func TestGraphicsStreamSendFileFrameRejectsUnsendableFrame(t *testing.T) {
	server := newGraphicsServer(t, graphicsServerConfig{})
	stream := openGraphicsStream(t, server, PaneGraphicsStreamParams{PaneID: "w1:p1"})
	ctx := testContext(t)

	if _, err := stream.SendFileFrame(ctx, GraphicsFileFrame{Format: PaneGraphicsFormatRgba}); err == nil {
		t.Error("file frame without a path accepted")
	}
	frame := GraphicsFileFrame{Format: PaneGraphicsFormatPng, Path: "/tmp/frame.png"}
	if _, err := stream.SendFileFrame(ctx, frame); err == nil {
		t.Error("png file frame accepted")
	}
	if got := len(server.captured()); got != 0 {
		t.Errorf("server read %d frames, want 0", got)
	}
}

func TestGraphicsStreamWaitReportsRefusedInlineFrame(t *testing.T) {
	server := newGraphicsServer(t, graphicsServerConfig{
		frameError:   "image_too_large",
		frameErrorAt: 1,
	})
	stream := openGraphicsStream(t, server, PaneGraphicsStreamParams{PaneID: "w1:p1"})
	ctx := testContext(t)

	frame := GraphicsFrame{Format: PaneGraphicsFormatRgba, ImageWidth: 1, ImageHeight: 1, Data: []byte("x")}
	if err := stream.SendFrame(ctx, frame); err != nil {
		t.Fatalf("send frame: %v", err)
	}
	if err := stream.Wait(ctx); !IsCode(err, "image_too_large") {
		t.Fatalf("wait = %v, want image_too_large", err)
	}
	// The same refusal is what a later send reports, since the server sends
	// nothing else after it.
	if err := stream.SendFrame(ctx, frame); !IsCode(err, "image_too_large") {
		t.Errorf("second send = %v, want image_too_large", err)
	}
}

func TestGraphicsStreamSendFileFrameReportsRefusal(t *testing.T) {
	server := newGraphicsServer(t, graphicsServerConfig{
		frameError:   ErrCodeStreamClosed,
		frameErrorAt: 1,
	})
	stream := openGraphicsStream(t, server, PaneGraphicsStreamParams{PaneID: "w1:p1"})

	frame := GraphicsFileFrame{Format: PaneGraphicsFormatRgba, Path: "/tmp/frame.raw"}
	ack, err := stream.SendFileFrame(testContext(t), frame)
	if ack != nil {
		t.Errorf("ack = %+v, want none", ack)
	}
	if !IsCode(err, ErrCodeStreamClosed) {
		t.Fatalf("send file frame = %v, want %s", err, ErrCodeStreamClosed)
	}
}

func TestGraphicsStreamWaitReportsServerClose(t *testing.T) {
	server := newFakeServer(t, func(s *fakeSession) {
		s.success(map[string]string{"type": "ok"})
	})
	stream, err := New(server.path).PaneGraphicsStream(testContext(t), PaneGraphicsStreamParams{PaneID: "w1:p1"})
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	defer func() { _ = stream.Close() }()

	if err := stream.Wait(testContext(t)); !errors.Is(err, ErrStreamClosed) {
		t.Fatalf("wait = %v, want ErrStreamClosed", err)
	}
}

func TestGraphicsStreamCloseUnblocksWait(t *testing.T) {
	server := newGraphicsServer(t, graphicsServerConfig{})
	stream := openGraphicsStream(t, server, PaneGraphicsStreamParams{PaneID: "w1:p1"})

	waited := make(chan error, 1)
	go func() { waited <- stream.Wait(context.Background()) }()
	if err := stream.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	select {
	case err := <-waited:
		if !errors.Is(err, ErrStreamClosed) {
			t.Errorf("wait = %v, want ErrStreamClosed", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Wait did not return after Close")
	}
	if err := stream.Close(); err != nil {
		t.Errorf("second close: %v", err)
	}
}

func TestGraphicsStreamSendAfterCloseFails(t *testing.T) {
	server := newGraphicsServer(t, graphicsServerConfig{})
	stream := openGraphicsStream(t, server, PaneGraphicsStreamParams{PaneID: "w1:p1"})
	if err := stream.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	frame := GraphicsFrame{Format: PaneGraphicsFormatRgba, ImageWidth: 1, ImageHeight: 1, Data: []byte("x")}
	if err := stream.SendFrame(testContext(t), frame); !errors.Is(err, ErrStreamClosed) {
		t.Errorf("send after close = %v, want ErrStreamClosed", err)
	}
}

func TestGraphicsStreamSendHonoursContext(t *testing.T) {
	server := newGraphicsServer(t, graphicsServerConfig{})
	stream := openGraphicsStream(t, server, PaneGraphicsStreamParams{PaneID: "w1:p1"})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	frame := GraphicsFrame{Format: PaneGraphicsFormatRgba, ImageWidth: 1, ImageHeight: 1, Data: []byte("x")}
	if err := stream.SendFrame(ctx, frame); !errors.Is(err, context.Canceled) {
		t.Errorf("send with cancelled context = %v, want context.Canceled", err)
	}
}

// waitForFrames blocks until the server has read count frames.
func waitForFrames(t *testing.T, server *graphicsServer, count int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if len(server.captured()) >= count {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("server read %d frames, want %d", len(server.captured()), count)
}

// TestLivePaneGraphicsStreamReachesTheServer checks that the running server
// accepts the method the schema does not declare. Opening a stream on a real
// pane would reserve a layer, so this asks for a pane that cannot exist: the
// request still passes the feature check and the pane lookup, which is as far
// as a read-only call can go.
func TestLivePaneGraphicsStreamReachesTheServer(t *testing.T) {
	client := newLiveClient(t)
	params := PaneGraphicsStreamParams{PaneID: "herdr-client-live-check:missing"}

	stream, err := client.PaneGraphicsStream(testContext(t), params)
	if err == nil {
		_ = stream.Close()
		t.Fatal("a stream opened on a pane that cannot exist")
	}
	var apiErr *Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("open error = %v, want a server error", err)
	}
	switch apiErr.Code {
	case ErrCodePaneNotFound:
	case ErrCodeFeatureDisabled:
		t.Skip("terminal.kitty_graphics is off on this server")
	default:
		t.Errorf("code = %q, want %s", apiErr.Code, ErrCodePaneNotFound)
	}
}
