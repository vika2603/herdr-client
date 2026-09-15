package plugintest

import (
	"testing"

	"github.com/vika2603/herdr-client/herdr"
	"github.com/vika2603/herdr-client/herdrtest"
	"github.com/vika2603/herdr-client/plugin"
)

// Server adapts a protocol test server to the environment Herdr injects into
// plugins. The embedded server exposes call recording, synchronization,
// subscriptions and cleanup. Ordinary client tests can use herdrtest directly.
// NewServer skips on Windows, where a named-pipe test listener is not provided.
type Server struct {
	*herdrtest.Server
}

// Call is one request recorded by the protocol server.
type Call = herdrtest.Call

// NewServer starts an empty server and registers cleanup with the test.
func NewServer(t testing.TB) *Server {
	t.Helper()
	return &Server{Server: herdrtest.NewServer(t)}
}

// Reply answers method with result. Repeated registration replaces the response.
func (s *Server) Reply(method string, result herdr.Result) *Server {
	s.Server.Reply(method, result)
	return s
}

// Fail answers method with a server API error.
func (s *Server) Fail(method, code, message string) *Server {
	s.Server.Fail(method, code, message)
	return s
}

// Handle supplies a dynamic response. See herdrtest.Handler for the context
// and concurrency contract.
func (s *Server) Handle(method string, handler herdrtest.Handler) *Server {
	s.Server.Handle(method, handler)
	return s
}

// AllowSubscriptions acknowledges subscriptions; WaitSubscription returns
// their handles for sending events and simulating disconnects.
func (s *Server) AllowSubscriptions() *Server {
	s.Server.AllowSubscriptions()
	return s
}

// Env builds a plugin environment whose real client reaches this server.
func (s *Server) Env(opts ...Option) *plugin.Env {
	env := Env(opts...)
	env.SocketPath = s.SocketPath()
	return env
}
