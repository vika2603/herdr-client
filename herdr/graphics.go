package herdr

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
)

// MethodPaneGraphicsStream opens a graphics frame stream on a pane.
//
// The herdr schema marks this method as skipped, so no wrapper is generated
// for it; the framing after the acknowledgement is not newline-delimited JSON
// and could not be expressed there either.
const MethodPaneGraphicsStream = "pane.graphics.stream"

// PaneGraphicsStreamMaxBytes is the largest inline frame the server accepts,
// PANE_GRAPHICS_STREAM_MAX_BYTES in herdr 0.9.0. A larger frame is refused
// with image_too_large and ends the stream.
const PaneGraphicsStreamMaxBytes = 16 << 20

// PaneGraphicsStreamParams are the parameters of pane.graphics.stream. The
// stream owns the layer it names for as long as it is open, so a second
// stream on the same layer is refused with stream_conflict.
//
// The owner field the herdr struct carries is not part of the wire form: the
// server assigns it per connection.
type PaneGraphicsStreamParams struct {
	LayerID Optional[string] `json:"layer_id,omitzero"`
	PaneID  string           `json:"pane_id"`
	ZIndex  Optional[int32]  `json:"z_index,omitzero"`
}

// GraphicsFrame is one inline frame: a JSON header followed by exactly
// len(Data) raw bytes on the same connection.
//
// Data holds the pixels in Format. For rgb, rgba and bgra the server requires
// exactly image_width*image_height*bytes-per-pixel bytes; png data is taken as
// it is.
type GraphicsFrame struct {
	Format      PaneGraphicsFormat
	ImageWidth  uint32
	ImageHeight uint32
	Data        []byte
	Placement   Optional[PaneGraphicsPlacementParams]
}

// GraphicsFileFrame is one frame whose pixels the server reads from a file
// the client wrote. Only rgba and bgra are accepted, the file must hold a
// complete image_width*image_height*4 byte image, and the server must be able
// to read Path itself.
//
// Sequence and Revision are echoed in the acknowledgement, which is the only
// way to pair it with the frame that produced it.
type GraphicsFileFrame struct {
	Format      PaneGraphicsFormat
	ImageWidth  uint32
	ImageHeight uint32
	Path        string
	Sequence    uint64
	Revision    uint64
	Placement   Optional[PaneGraphicsPlacementParams]
}

// graphicsFrameHeader is the JSON line that precedes a frame.
type graphicsFrameHeader struct {
	Format      PaneGraphicsFormat                    `json:"format"`
	ImageWidth  uint32                                `json:"image_width"`
	ImageHeight uint32                                `json:"image_height"`
	DataLength  Optional[int]                         `json:"data_length,omitzero"`
	File        Optional[graphicsFrameFile]           `json:"file,omitzero"`
	Sequence    uint64                                `json:"sequence,omitzero"`
	Revision    uint64                                `json:"revision,omitzero"`
	Placement   Optional[PaneGraphicsPlacementParams] `json:"placement,omitzero"`
}

type graphicsFrameFile struct {
	Path string `json:"path"`
}

// GraphicsStream is an open pane.graphics.stream connection.
//
// Unlike Stream, whose connection only ever carries pushed lines, this one is
// written to after the acknowledgement: each frame is a header line and, for
// an inline frame, the raw bytes that follow it. The server answers a file
// frame with a frame acknowledgement and an inline frame with nothing at all,
// so a failed inline frame is reported by the next SendFrame or by Wait.
//
// A GraphicsStream is safe for concurrent use, but frames are serialised, so
// a send blocks while another is in flight.
type GraphicsStream struct {
	conn io.ReadWriteCloser

	acks  chan *PaneGraphicsFrameAckResponse
	ended chan struct{}
	// closed is closed by Close and releases the reading goroutine.
	closed chan struct{}

	writeMu sync.Mutex

	closeOnce sync.Once
	closeErr  error

	mu   sync.Mutex
	done error
}

// PaneGraphicsStream opens a graphics frame stream on the pane named by
// params and keeps the connection open for the frames that follow.
//
// ctx bounds the opening request only; each send takes its own context. The
// server refuses the request with feature_disabled when kitty graphics are
// off, pane_not_found for an unknown pane, stream_conflict when the layer
// already has a stream, and layer_limit when the pane has no room for one.
func (c *Client) PaneGraphicsStream(ctx context.Context, params PaneGraphicsStreamParams) (*GraphicsStream, error) {
	opened, err := c.open(ctx, MethodPaneGraphicsStream, params)
	if err != nil {
		return nil, err
	}
	if err := expectOKResult(MethodPaneGraphicsStream, opened.result); err != nil {
		_ = opened.conn.Close()
		return nil, err
	}

	s := &GraphicsStream{
		conn:   opened.conn,
		acks:   make(chan *PaneGraphicsFrameAckResponse),
		ended:  make(chan struct{}),
		closed: make(chan struct{}),
	}
	go s.read(opened.reader)
	return s, nil
}

// expectOKResult checks that raw is the "ok" result the server acknowledges a
// stream with.
func expectOKResult(method string, raw json.RawMessage) error {
	result, err := decodeResult(method, raw)
	if err != nil {
		return err
	}
	if _, ok := result.(*OKResponse); !ok {
		return opError(method, OpDecode, &UnexpectedResultError{Method: method, Want: "ok", Got: result.ResultType()})
	}
	return nil
}

// read turns every line the server sends into either a frame acknowledgement
// or the reason the stream ended. The server writes at most one line per
// frame and only ever writes an error as its last, so a line that is not an
// acknowledgement terminates the stream.
func (s *GraphicsStream) read(r *bufio.Reader) {
	defer close(s.ended)
	for {
		line, err := r.ReadBytes('\n')
		if trimmed := bytes.TrimRight(line, "\r\n"); len(bytes.TrimSpace(trimmed)) > 0 {
			ack, ackErr := decodeFrameAck(trimmed)
			if ackErr != nil {
				s.setDone(ackErr)
				return
			}
			select {
			case s.acks <- ack:
			case <-s.closed:
				return
			}
		}
		if err != nil {
			s.setDone(streamReadError(MethodPaneGraphicsStream, err))
			return
		}
	}
}

// decodeFrameAck reads one response line as a frame acknowledgement. A server
// error response comes back as *Error.
func decodeFrameAck(line []byte) (*PaneGraphicsFrameAckResponse, error) {
	raw, err := decodeResponseLine(MethodPaneGraphicsStream, line)
	if err != nil {
		return nil, err
	}
	result, err := decodeResult(MethodPaneGraphicsStream, raw)
	if err != nil {
		return nil, err
	}
	ack, ok := result.(*PaneGraphicsFrameAckResponse)
	if !ok {
		return nil, opError(MethodPaneGraphicsStream, OpDecode, &UnexpectedResultError{
			Method: MethodPaneGraphicsStream,
			Want:   "pane_graphics_frame_ack",
			Got:    result.ResultType(),
		})
	}
	return ack, nil
}

func (s *GraphicsStream) setDone(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done == nil {
		s.done = err
	}
}

// endErr reports why the stream ended, falling back to ErrStreamClosed when
// the server simply closed the connection.
func (s *GraphicsStream) endErr(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return opError(MethodPaneGraphicsStream, OpRead, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done != nil {
		return s.done
	}
	return streamReadError(MethodPaneGraphicsStream, nil)
}

// SendFrame writes one inline frame.
//
// The server acknowledges nothing on success, so a nil error means the frame
// reached the connection, not that it was displayed. A frame the server
// refuses ends the stream, and that error is reported by the next send or by
// Wait.
//
// Cancelling ctx closes the connection, which is the only way to unblock a
// write, and ends the stream.
func (s *GraphicsStream) SendFrame(ctx context.Context, frame GraphicsFrame) error {
	switch {
	case len(frame.Data) == 0:
		return opError(MethodPaneGraphicsStream, OpValidate, errors.New("frame carries no data"))
	case len(frame.Data) > PaneGraphicsStreamMaxBytes:
		return opError(MethodPaneGraphicsStream, OpValidate, fmt.Errorf("frame is %d bytes, over the %d byte limit", len(frame.Data), PaneGraphicsStreamMaxBytes))
	}
	length := len(frame.Data)
	header := graphicsFrameHeader{
		Format:      frame.Format,
		ImageWidth:  frame.ImageWidth,
		ImageHeight: frame.ImageHeight,
		DataLength:  Some(length),
		Placement:   frame.Placement,
	}
	_, err := s.send(ctx, header, frame.Data, false)
	return err
}

// SendFileFrame writes one frame whose pixels the server reads from
// frame.Path, and waits for the acknowledgement the server sends once the
// terminal has accepted the file or an inline copy has been installed.
//
// The path must name a file the server can read; it is not sent as data. A
// frame the server refuses ends the stream and is returned as *Error.
func (s *GraphicsStream) SendFileFrame(ctx context.Context, frame GraphicsFileFrame) (*PaneGraphicsFrameAckResponse, error) {
	switch {
	case frame.Path == "":
		return nil, opError(MethodPaneGraphicsStream, OpValidate, errors.New("file frame carries no path"))
	case frame.Format != PaneGraphicsFormatRgba && frame.Format != PaneGraphicsFormatBgra:
		return nil, opError(MethodPaneGraphicsStream, OpValidate, fmt.Errorf("file frames require rgba or bgra, got %q", frame.Format))
	}
	header := graphicsFrameHeader{
		Format:      frame.Format,
		ImageWidth:  frame.ImageWidth,
		ImageHeight: frame.ImageHeight,
		File:        Some(graphicsFrameFile{Path: frame.Path}),
		Sequence:    frame.Sequence,
		Revision:    frame.Revision,
		Placement:   frame.Placement,
	}
	return s.send(ctx, header, nil, true)
}

// send writes one frame and, when the server answers it, waits for that
// answer. Frames are serialised so that an acknowledgement cannot be taken
// for another frame's.
func (s *GraphicsStream) send(ctx context.Context, header graphicsFrameHeader, body []byte, wantAck bool) (*PaneGraphicsFrameAckResponse, error) {
	line, err := json.Marshal(header)
	if err != nil {
		return nil, opError(MethodPaneGraphicsStream, OpEncode, err)
	}
	line = append(line, '\n')

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	if err := ctx.Err(); err != nil {
		return nil, opError(MethodPaneGraphicsStream, OpWrite, err)
	}
	select {
	case <-s.closed:
		return nil, requestError(ctx, MethodPaneGraphicsStream, OpWrite, ErrStreamClosed)
	case <-s.ended:
		return nil, s.endErr(ctx)
	default:
	}

	// The watch stays in place until the acknowledgement has been read: a
	// cancelled wait would otherwise leave that line for the next frame to
	// mistake for its own.
	defer watchContext(ctx, s.conn)()

	if err := writeAll(s.conn, line, body); err != nil {
		// A partial header or body loses the frame boundary. No later frame
		// can safely reuse the connection, even if its writer accepts more data.
		_ = s.Close()
		return nil, requestError(ctx, MethodPaneGraphicsStream, OpWrite, err)
	}
	if !wantAck {
		return nil, nil
	}

	select {
	case <-ctx.Done():
		return nil, opError(MethodPaneGraphicsStream, OpRead, ctx.Err())
	case ack := <-s.acks:
		return ack, nil
	case <-s.ended:
		return nil, s.endErr(ctx)
	case <-s.closed:
		return nil, requestError(ctx, MethodPaneGraphicsStream, OpRead, ErrStreamClosed)
	}
}

// Wait blocks until the server ends the stream and reports why: the *Error it
// sent, or an OpError matching ErrStreamClosed when it closed without one.
// Cancellation matches ctx.Err() through errors.Is. Wait is the only way to
// learn that an inline frame was refused, because those are not acknowledged.
func (s *GraphicsStream) Wait(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return opError(MethodPaneGraphicsStream, OpRead, err)
	}
	select {
	case <-ctx.Done():
		return opError(MethodPaneGraphicsStream, OpRead, ctx.Err())
	case <-s.ended:
		return s.endErr(ctx)
	case <-s.closed:
		return requestError(ctx, MethodPaneGraphicsStream, OpRead, ErrStreamClosed)
	}
}

// Close closes the connection, which makes the server drop the layer the
// stream owned. A blocked send or Wait matches ErrStreamClosed through
// errors.Is; a connection close failure carries OpClose.
func (s *GraphicsStream) Close() error {
	s.closeOnce.Do(func() {
		close(s.closed)
		s.closeErr = opError(MethodPaneGraphicsStream, OpClose, s.conn.Close())
	})
	return s.closeErr
}
