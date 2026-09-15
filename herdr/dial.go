package herdr

import (
	"context"
	"errors"
	"io"
	"sync"
)

// DialFunc establishes a fresh connection for a request. address is the value
// passed to New. The function must be safe for concurrent calls, honor ctx,
// and return promptly when it is canceled. The client does not run the dialer
// in a detached goroutine or retry failed dials.
//
// ctx bounds dialing only. A successfully returned connection must remain
// usable after that context is canceled. Read and Write must work concurrently.
// Close must safely interrupt both; the client requires no deadline APIs.
// The client owns returned connections, including one returned with an error.
// It invokes the underlying Close at most once, even when cancellation and
// explicit stream closure race.
// Each successful dial must return a different, exclusively owned connection.
type DialFunc func(ctx context.Context, address string) (io.ReadWriteCloser, error)

// WithDialer replaces local IPC connection establishment. It changes neither
// request encoding nor the one-request-per-connection protocol. A nil dialer
// restores the platform default (Unix socket or Windows named pipe).
func WithDialer(dialer DialFunc) Option {
	return func(c *Client) { c.dialer = dialer }
}

func (c *Client) dial(ctx context.Context) (io.ReadWriteCloser, error) {
	if c.dialTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.dialTimeout)
		defer cancel()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dialer := c.dialer
	if dialer == nil {
		dialer = dialSocket
	}
	conn, err := dialer(ctx, c.socketPath)
	if ctxErr := ctx.Err(); ctxErr != nil {
		err = ctxErr
	}
	if err != nil {
		if conn != nil {
			_ = conn.Close()
		}
		return nil, err
	}
	if conn == nil {
		return nil, errors.New("herdr: dialer returned no connection and no error")
	}
	return &ownedConnection{ReadWriteCloser: conn}, nil
}

// ownedConnection gives all client-side owners the same Close result without
// imposing repeated/concurrent Close semantics on an injected connection.
type ownedConnection struct {
	io.ReadWriteCloser
	once     sync.Once
	closeErr error
}

func (c *ownedConnection) Close() error {
	c.once.Do(func() { c.closeErr = c.ReadWriteCloser.Close() })
	return c.closeErr
}
