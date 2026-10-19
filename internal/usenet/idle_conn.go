package usenet

import (
	"net"
	"sync"
	"time"
)

// idleReadConn keeps the absolute operation deadline set by begin/context
// cancellation, while renewing the shorter inactivity window on each wire read.
// The mutex prevents a read from overwriting a concurrent cancellation deadline.
type idleReadConn struct {
	net.Conn
	mu       sync.Mutex
	idle     time.Duration
	deadline time.Time
}

func (c *idleReadConn) SetDeadline(t time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.deadline = t
	return c.Conn.SetDeadline(t)
}
func (c *idleReadConn) Read(p []byte) (int, error) {
	c.mu.Lock()
	deadline := time.Now().Add(c.idle)
	if !c.deadline.IsZero() && c.deadline.Before(deadline) {
		deadline = c.deadline
	}
	err := c.Conn.SetReadDeadline(deadline)
	c.mu.Unlock()
	if err != nil {
		return 0, err
	}
	return c.Conn.Read(p)
}
