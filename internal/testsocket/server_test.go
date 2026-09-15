package testsocket_test

import (
	"bufio"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vika2603/herdr-client/internal/testsocket"
)

func TestCloseJoinsPartialRequestsAndRemovesSocket(t *testing.T) {
	entered := make(chan struct{})
	readDone := make(chan error, 1)
	server := testsocket.New(t, func(_ context.Context, conn net.Conn) {
		close(entered)
		_, err := bufio.NewReader(conn).ReadString('\n')
		readDone <- err
	})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "unix", server.Path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if _, err := conn.Write([]byte(`{"id":"unfinished"`)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("connection was not accepted")
	}

	// Every Close caller must wait for the same cleanup, including a handler
	// whose client has never finished writing a request line.
	closed := make(chan struct{}, 3)
	for range 3 {
		go func() {
			server.Close()
			closed <- struct{}{}
		}()
	}
	for range 3 {
		select {
		case <-closed:
		case <-ctx.Done():
			t.Fatal("Close did not finish for a partially written request")
		}
	}
	select {
	case err := <-readDone:
		if err == nil {
			t.Error("partial request unexpectedly completed without a read error")
		}
	default:
		t.Fatal("Close returned before the connection handler exited")
	}
	select {
	case <-server.Done():
	default:
		t.Error("Close did not cancel the server context")
	}
	if _, err := os.Stat(filepath.Dir(server.Path)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("socket directory after Close: %v, want not found", err)
	}
	if again, err := dialer.DialContext(ctx, "unix", server.Path); err == nil {
		_ = again.Close()
		t.Error("closed server accepted another connection")
	}
}
