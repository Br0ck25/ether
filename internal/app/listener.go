package app

import (
	"bufio"
	"crypto/tls"
	"errors"
	"net"
	"sync"
	"time"
)

// dualListener serves plain HTTP and TLS on one port, as PMS does on 32400:
// a connection whose first byte is a TLS handshake record (0x16) is wrapped
// in tls.Server, everything else is passed through as plain HTTP.
type dualListener struct {
	net.Listener
	tlsConfig func() *tls.Config

	once  sync.Once
	conns chan net.Conn
	errs  chan error
	done  chan struct{}
}

func newDualListener(l net.Listener, tlsConfig func() *tls.Config) *dualListener {
	d := &dualListener{
		Listener:  l,
		tlsConfig: tlsConfig,
		conns:     make(chan net.Conn),
		errs:      make(chan error, 1),
		done:      make(chan struct{}),
	}
	go d.acceptLoop()
	return d
}

func (d *dualListener) acceptLoop() {
	for {
		c, err := d.Listener.Accept()
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				time.Sleep(10 * time.Millisecond)
				continue
			}
			select {
			case d.errs <- err:
			case <-d.done:
			}
			return
		}
		// Sniff off the accept loop so a slow client can't stall others.
		go d.sniff(c)
	}
}

func (d *dualListener) sniff(c net.Conn) {
	_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
	br := bufio.NewReader(c)
	first, err := br.Peek(1)
	_ = c.SetReadDeadline(time.Time{})
	if err != nil {
		c.Close()
		return
	}
	var out net.Conn = &peekedConn{Conn: c, r: br}
	if first[0] == 0x16 {
		cfg := d.tlsConfig()
		if cfg == nil {
			c.Close()
			return
		}
		out = tls.Server(out, cfg)
	}
	select {
	case d.conns <- out:
	case <-d.done:
		c.Close()
	}
}

func (d *dualListener) Accept() (net.Conn, error) {
	select {
	case c := <-d.conns:
		return c, nil
	case err := <-d.errs:
		return nil, err
	case <-d.done:
		return nil, net.ErrClosed
	}
}

func (d *dualListener) Close() error {
	d.once.Do(func() { close(d.done) })
	return d.Listener.Close()
}

type peekedConn struct {
	net.Conn
	r *bufio.Reader
}

func (p *peekedConn) Read(b []byte) (int, error) { return p.r.Read(b) }
