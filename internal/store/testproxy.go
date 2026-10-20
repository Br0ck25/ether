package store

import (
	"io"
	"net"
	"net/url"
	"sync"
)

// TestProxy is an in-process TCP proxy for outage tests: clients connect
// to Addr and are forwarded to the target until the test cuts or pauses
// the link. It never touches the database server itself.
type TestProxy struct {
	ln     net.Listener
	target string

	mu     sync.Mutex
	mode   proxyMode
	conns  map[net.Conn]struct{}
	paused chan struct{} // closed when forwarding resumes
	closed bool
	wg     sync.WaitGroup
}

type proxyMode int

const (
	proxyUp     proxyMode = iota
	proxyCut              // new and existing connections are closed (connection refused/reset)
	proxyPaused           // connections are accepted but no bytes flow (a black hole)
)

// NewTestProxy listens on 127.0.0.1 and forwards to target (host:port).
func NewTestProxy(target string) (*TestProxy, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	p := &TestProxy{ln: ln, target: target, conns: map[net.Conn]struct{}{}, paused: make(chan struct{})}
	close(p.paused)
	p.wg.Add(1)
	go p.accept()
	return p, nil
}

// Addr is the proxy's listening address.
func (p *TestProxy) Addr() string { return p.ln.Addr().String() }

// URL rewrites a database URL to connect through the proxy.
func (p *TestProxy) URL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	u.Host = p.Addr()
	return u.String(), nil
}

// Cut drops every connection and refuses new ones until Restore.
func (p *TestProxy) Cut() {
	p.mu.Lock()
	p.setPausedLocked(false)
	p.mode = proxyCut
	conns := p.takeConnsLocked()
	p.mu.Unlock()
	for _, c := range conns {
		c.Close()
	}
}

// Pause stops all traffic (existing connections hang, new ones are
// accepted and hang) until Restore.
func (p *TestProxy) Pause() {
	p.mu.Lock()
	p.setPausedLocked(true)
	p.mode = proxyPaused
	p.mu.Unlock()
}

// Restore forwards traffic again. Connections that hung during a pause
// resume; connections dropped by Cut stay closed.
func (p *TestProxy) Restore() {
	p.mu.Lock()
	p.setPausedLocked(false)
	p.mode = proxyUp
	p.mu.Unlock()
}

// Close stops the proxy and drops every connection.
func (p *TestProxy) Close() error {
	p.mu.Lock()
	p.closed = true
	p.setPausedLocked(false)
	conns := p.takeConnsLocked()
	p.mu.Unlock()
	err := p.ln.Close()
	for _, c := range conns {
		c.Close()
	}
	p.wg.Wait()
	return err
}

func (p *TestProxy) setPausedLocked(paused bool) {
	select {
	case <-p.paused: // currently flowing
		if paused {
			p.paused = make(chan struct{})
		}
	default:
		if !paused {
			close(p.paused)
		}
	}
}

func (p *TestProxy) takeConnsLocked() []net.Conn {
	out := make([]net.Conn, 0, len(p.conns))
	for c := range p.conns {
		out = append(out, c)
	}
	clear(p.conns)
	return out
}

func (p *TestProxy) track(c net.Conn) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.mode == proxyCut {
		return false
	}
	p.conns[c] = struct{}{}
	return true
}

func (p *TestProxy) accept() {
	defer p.wg.Done()
	for {
		c, err := p.ln.Accept()
		if err != nil {
			return
		}
		if !p.track(c) {
			c.Close()
			continue
		}
		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			p.serve(c)
		}()
	}
}

func (p *TestProxy) gate() {
	p.mu.Lock()
	ch := p.paused
	p.mu.Unlock()
	<-ch
}

func (p *TestProxy) serve(client net.Conn) {
	defer client.Close()
	p.gate()
	server, err := net.Dial("tcp", p.target)
	if err != nil {
		return
	}
	if !p.track(server) {
		server.Close()
		return
	}
	defer server.Close()
	done := make(chan struct{}, 2)
	pump := func(dst, src net.Conn) {
		buf := make([]byte, 32*1024)
		for {
			n, err := src.Read(buf)
			if n > 0 {
				p.gate() // a pause holds bytes in flight
				if _, werr := dst.Write(buf[:n]); werr != nil {
					break
				}
			}
			if err != nil {
				break
			}
		}
		done <- struct{}{}
	}
	go pump(server, client)
	go pump(client, server)
	<-done
	client.Close()
	server.Close()
	<-done
}

var _ io.Closer = (*TestProxy)(nil)
