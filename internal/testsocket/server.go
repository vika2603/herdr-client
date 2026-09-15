// Package testsocket owns the sockets and goroutines shared by protocol tests.
// It has no dependency on the client or its wire types.
package testsocket

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// Server accepts connections until Close. Handlers must return when their
// context is canceled or their connection is closed.
type Server struct {
	Listener net.Listener
	Path     string
	ctx      context.Context
	cancel   context.CancelFunc
	dir      string
	mu       sync.Mutex
	conns    map[net.Conn]struct{}
	closed   bool
	wg       sync.WaitGroup
	once     sync.Once
}

// New starts a local IPC listener and registers Close with the test.
func New(t testing.TB, handle func(context.Context, net.Conn)) *Server {
	t.Helper()
	listener, path, dir := listen(t)
	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{Listener: listener, Path: path, dir: dir, ctx: ctx, cancel: cancel, conns: make(map[net.Conn]struct{})}
	t.Cleanup(s.Close)
	s.wg.Add(1)
	go s.serve(handle)
	return s
}

// Done closes when shutdown starts.
func (s *Server) Done() <-chan struct{} { return s.ctx.Done() }

func (s *Server) serve(handle func(context.Context, net.Conn)) {
	defer s.wg.Done()
	for {
		conn, err := s.Listener.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			_ = conn.Close()
			return
		}
		s.conns[conn] = struct{}{}
		s.wg.Add(1)
		s.mu.Unlock()
		go func() {
			defer s.wg.Done()
			defer func() {
				_ = conn.Close()
				s.mu.Lock()
				delete(s.conns, conn)
				s.mu.Unlock()
			}()
			handle(s.ctx, conn)
		}()
	}
}

// Close cancels handlers, closes all sockets, joins the server goroutines,
// and removes its temporary directory. Concurrent calls wait for the same cleanup.
// A handler must not call Close itself, because Close waits for handlers.
func (s *Server) Close() {
	s.once.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.cancel()
		_ = s.Listener.Close()
		for conn := range s.conns {
			_ = conn.Close()
		}
		s.mu.Unlock()
		s.wg.Wait()
		_ = os.RemoveAll(s.dir)
	})
}

func socketPath(dir string) string { return filepath.Join(dir, "herdr.sock") }
