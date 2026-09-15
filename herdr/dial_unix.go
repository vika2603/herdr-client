//go:build !windows

package herdr

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
)

// dialSocket connects to the Unix domain socket at path.
func dialSocket(ctx context.Context, path string) (io.ReadWriteCloser, error) {
	var dialer net.Dialer
	return dialer.DialContext(ctx, "unix", path)
}

// platformConfigDir mirrors herdr's config directory for non-Windows targets.
func platformConfigDir() string {
	if home := os.Getenv("HOME"); home != "" {
		return filepath.Join(home, ".config", appDirName)
	}
	return filepath.Join(os.TempDir(), appDirName)
}
