package debrid

import (
	"context"
	"log/slog"
	"net"
	"sync"
	"time"
)

// defaultHostConnections caps open TCP connections per media host across
// the primary and hedge transports. Debrid CDNs count connections, not
// requests: TorBox answers the 21st connection to one host with HTTP 429
// and a five-minute pause for the whole host. Idle keep-alive connections
// count too, so two transports with generous idle pools could trip that
// limit while only a handful of reads were in flight. Twelve leaves room
// for the owner's other devices and apps on the same account.
const defaultHostConnections = 12

// connCap counts open connections per dial address and makes a dial wait,
// after asking the transports to drop idle connections, rather than exceed
// the limit.
type connCap struct {
	limit     int
	closeIdle func()
	log       *slog.Logger

	mu    sync.Mutex
	open  map[string]int
	freed chan struct{} // closed and replaced whenever a connection closes
}

func newConnCap(limit int) *connCap {
	return &connCap{limit: limit, open: map[string]int{}, freed: make(chan struct{})}
}

type dialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

func (c *connCap) wrap(next dialFunc) dialFunc {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		if err := c.acquire(ctx, addr); err != nil {
			return nil, err
		}
		conn, err := next(ctx, network, addr)
		if err != nil {
			c.release(addr)
			return nil, err
		}
		return &cappedConn{Conn: conn, release: func() { c.release(addr) }}, nil
	}
}

func (c *connCap) acquire(ctx context.Context, addr string) error {
	asked := false
	began := time.Now()
	defer func() {
		if wait := time.Since(began); wait > 250*time.Millisecond && c.log != nil {
			c.log.Info("media connection waited for the host cap", "host", addr, "wait", wait, "limit", c.limit)
		}
	}()
	for {
		c.mu.Lock()
		if c.open[addr] < c.limit {
			c.open[addr]++
			c.mu.Unlock()
			return nil
		}
		freed := c.freed
		c.mu.Unlock()
		if !asked && c.closeIdle != nil {
			// Idle keep-alives are the cheapest connections to give back.
			asked = true
			go c.closeIdle()
		}
		select {
		case <-freed:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (c *connCap) release(addr string) {
	c.mu.Lock()
	if c.open[addr] <= 1 {
		delete(c.open, addr)
	} else {
		c.open[addr]--
	}
	close(c.freed)
	c.freed = make(chan struct{})
	c.mu.Unlock()
}

// inUse reports open connections to addr (tests and diagnostics).
func (c *connCap) inUse(addr string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.open[addr]
}

type cappedConn struct {
	net.Conn
	once    sync.Once
	release func()
}

func (c *cappedConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.release)
	return err
}
