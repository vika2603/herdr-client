package herdr

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"testing"
)

// graphicsCapture is one frame a graphicsServer read.
type graphicsCapture struct {
	header graphicsFrameHeader
	data   []byte
}

// graphicsServerConfig chooses where the fake server refuses something. A
// refusal is always the last thing it writes, which is how herdr behaves.
type graphicsServerConfig struct {
	// openError is the code the open request is refused with.
	openError string
	// frameError is the code frame number frameErrorAt is refused with,
	// counting from one. frameErrorAt zero refuses nothing.
	frameError   string
	frameErrorAt int
}

// graphicsServer speaks pane.graphics.stream as herdr 0.9.0 does: it answers
// the open request with "ok", then reads a JSON header line per frame and,
// for an inline frame, exactly data_length raw bytes after it. Only file
// frames are answered, with a frame acknowledgement.
type graphicsServer struct {
	*fakeServer
	cfg graphicsServerConfig

	mu     sync.Mutex
	frames []graphicsCapture
}

func newGraphicsServer(t *testing.T, cfg graphicsServerConfig) *graphicsServer {
	t.Helper()
	g := &graphicsServer{cfg: cfg}
	g.fakeServer = newFakeServer(t, g.handle)
	return g
}

func (g *graphicsServer) handle(s *fakeSession) {
	if s.request().Method != MethodPaneGraphicsStream {
		s.fail(ErrCodeInvalidRequest, "unexpected method "+s.request().Method)
		return
	}
	if g.cfg.openError != "" {
		s.fail(g.cfg.openError, "refused")
		return
	}
	s.success(map[string]string{"type": "ok"})
	g.serveFrames(s)
}

func (g *graphicsServer) serveFrames(s *fakeSession) {
	frames, inline := 0, 0
	for {
		line, err := s.reader.ReadBytes('\n')
		line = bytes.TrimRight(line, "\r\n")
		if len(bytes.TrimSpace(line)) == 0 {
			if err != nil {
				return
			}
			continue
		}

		var header graphicsFrameHeader
		if jsonErr := json.Unmarshal(line, &header); jsonErr != nil {
			failFrame(s, s.request().ID, "invalid_frame", "invalid frame header")
			return
		}

		var data []byte
		_, fileFrame := header.File.Get()
		if !fileFrame {
			dataLength, ok := header.DataLength.Get()
			if !ok || dataLength == 0 {
				failFrame(s, s.request().ID, "invalid_frame", "frame requires data_length or file")
				return
			}
			data = make([]byte, dataLength)
			if _, readErr := io.ReadFull(s.reader, data); readErr != nil {
				return
			}
			inline++
		}

		frames++
		g.mu.Lock()
		g.frames = append(g.frames, graphicsCapture{header: header, data: data})
		g.mu.Unlock()

		// herdr answers a file frame under the id it dispatched it with, and
		// an inline frame only when the app refused it.
		id := fmt.Sprintf("%s:frame:%d", s.request().ID, inline)
		if fileFrame {
			id = fmt.Sprintf("%s:file:%d", s.request().ID, header.Sequence)
		}
		if g.cfg.frameErrorAt == frames && g.cfg.frameError != "" {
			failFrame(s, id, g.cfg.frameError, "refused")
			return
		}
		if fileFrame {
			s.writeJSON(map[string]any{"id": id, "result": map[string]any{
				"type":     "pane_graphics_frame_ack",
				"sequence": header.Sequence,
				"revision": header.Revision,
			}})
		}
		if err != nil {
			return
		}
	}
}

// captured returns the frames the server has read so far.
func (g *graphicsServer) captured() []graphicsCapture {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]graphicsCapture(nil), g.frames...)
}

// failFrame writes an error response under the id herdr dispatched the frame
// with, which is not the id of the open request.
func failFrame(s *fakeSession, id, code, message string) {
	s.writeJSON(map[string]any{"id": id, "error": map[string]string{"code": code, "message": message}})
}
