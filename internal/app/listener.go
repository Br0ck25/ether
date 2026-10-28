package app

import (
	"bufio"
	"crypto/tls"
	"errors"
	"net"
	"sync"
	"syscall"
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

// Accept backoff bounds for temporary errors (EMFILE and friends).
const (
	acceptBackoffMin = 5 * time.Millisecond
	acceptBackoffMax = time.Second
)

// acceptLoop must survive transient Accept failures: http.Server treats
// them as temporary and calls our Accept again, so if this loop exited the
// server would block on d.conns forever while the process stayed up.
func (d *dualListener) acceptLoop() {
	var backoff time.Duration
	for {
		c, err := d.Listener.Accept()
		if err != nil {
			if isTemporaryAcceptErr(err) {
				if backoff == 0 {
					backoff = acceptBackoffMin
				} else if backoff *= 2; backoff > acceptBackoffMax {
					backoff = acceptBackoffMax
				}
				t := time.NewTimer(backoff)
				select {
				case <-t.C:
					continue
				case <-d.done:
					t.Stop()
					return
				}
			}
			select {
			case d.errs <- err:
			case <-d.done:
			}
			return
		}
		backoff = 0
		// Sniff off the accept loop so a slow client can't stall others.
		go d.sniff(c)
	}
}

// isTemporaryAcceptErr reports whether Accept may succeed if retried.
func isTemporaryAcceptErr(err error) bool {
	if errors.Is(err, net.ErrClosed) {
		return false
	}
	var te interface{ Temporary() bool }
	if errors.As(err, &te) && te.Temporary() {
		return true
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return true
	}
	for _, errno := range []error{syscall.EMFILE, syscall.ENFILE, syscall.ECONNABORTED, syscall.ECONNRESET} {
		if errors.Is(err, errno) {
			return true
		}
	}
	return false
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
