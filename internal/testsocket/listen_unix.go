//go:build !windows

package testsocket

import (
	"net"
	"os"
	"testing"
)

func listen(t testing.TB) (net.Listener, string, string) {
	t.Helper()
	// Short names keep even long test names within macOS's Unix socket limit.
	dir, err := os.MkdirTemp("", "herdr")
	if err != nil {
		t.Fatalf("testsocket: create socket directory: %v", err)
	}
	path := socketPath(dir)
	listener, err := net.Listen("unix", path)
	if err != nil {
		_ = os.RemoveAll(dir)
		t.Fatalf("testsocket: listen on %s: %v", path, err)
	}
	return listener, path, dir
}
