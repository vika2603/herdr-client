//go:build windows

package testsocket

import (
	"net"
	"testing"
)

func listen(t testing.TB) (net.Listener, string, string) {
	t.Helper()
	t.Skip("testsocket: the Herdr Windows API requires a named-pipe listener, which this test server does not implement")
	return nil, "", ""
}
