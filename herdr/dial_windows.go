//go:build windows

package herdr

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// pipePrefix is prepended to the socket path to form the named pipe that the
// server listens on.
const pipePrefix = `\\.\pipe\`

// dialSocket opens the named pipe that corresponds to path.
func dialSocket(ctx context.Context, path string) (io.ReadWriteCloser, error) {
	name := pipePrefix + path
	type opened struct {
		file *os.File
		err  error
	}
	// The open cannot be interrupted, so it runs on its own goroutine and its
	// result is discarded once ctx is done.
	result := make(chan opened, 1)
	go func() {
		file, err := os.OpenFile(name, os.O_RDWR, 0)
		result <- opened{file: file, err: err}
	}()

	select {
	case res := <-result:
		if res.err != nil {
			return nil, res.err
		}
		return res.file, nil
	case <-ctx.Done():
		go func() {
			if res := <-result; res.err == nil {
				_ = res.file.Close()
			}
		}()
		return nil, fmt.Errorf("herdr: dial %s: %w", name, ctx.Err())
	}
}

// platformConfigDir mirrors herdr's config directory on Windows.
func platformConfigDir() string {
	if dir := os.Getenv("APPDATA"); dir != "" {
		return filepath.Join(dir, appDirName)
	}
	if profile := os.Getenv("USERPROFILE"); profile != "" {
		return filepath.Join(profile, "AppData", "Roaming", appDirName)
	}
	if home := os.Getenv("HOME"); home != "" {
		return filepath.Join(home, ".config", appDirName)
	}
	return filepath.Join(os.TempDir(), appDirName)
}
