package embedding

import (
	"context"
	"net"
	"sync"
)

// requestConnection prevents the HTTP/1 transport's initial idle reader from
// consuming unsolicited provider bytes before roundTrip registers a request.
// In this private plaintext/no-proxy graph, the first write is the registered
// request, never TLS or CONNECT. Every request also closes its connection.
// Closing before that write wakes the reader without exposing any peer bytes.
type requestConnection struct {
	net.Conn
	written   chan struct{}
	closed    chan struct{}
	writeOnce sync.Once
	closeOnce sync.Once
	closeErr  error
}

func newRequestConnection(conn net.Conn) *requestConnection {
	return &requestConnection{Conn: conn, written: make(chan struct{}), closed: make(chan struct{})}
}

func (c *requestConnection) Read(bytes []byte) (int, error) {
	select {
	case <-c.written:
		select {
		case <-c.closed:
			return 0, net.ErrClosed
		default:
		}
		return c.Conn.Read(bytes)
	case <-c.closed:
		return 0, net.ErrClosed
	}
}

func (c *requestConnection) Write(bytes []byte) (int, error) {
	c.writeOnce.Do(func() { close(c.written) })
	return c.Conn.Write(bytes)
}

func (c *requestConnection) Close() error {
	c.closeOnce.Do(func() {
		close(c.closed)
		c.closeErr = c.Conn.Close()
	})
	return c.closeErr
}

// Caller trace hooks are arbitrary callbacks, not a prepared adapter capability.
// Preserve deadline/cancellation but do not compose ambient context values into
// HTTP tracing (which could extend owned lifetime or perform unrelated I/O).
type requestContext struct{ context.Context }

func (requestContext) Value(any) any { return nil }
