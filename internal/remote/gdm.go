package remote

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/textproto"
	"strconv"
	"strings"
	"sync"
	"time"
)

// GDM ("G'Day Mate") is Plex's LAN discovery protocol. Clients send
// "M-SEARCH * HTTP/1.1" to UDP 32414 on multicast group 239.0.0.250 (and by
// broadcast); every PMS answers unicast with an "HTTP/1.0 200 OK" block of
// headers describing itself. PMS listens on 32410, 32412, 32413 and 32414.
const (
	GDMMulticastGroup = "239.0.0.250"
	GDMServerPort     = 32414
	// GDMAnnouncePort receives HELLO/BYE announcements.
	GDMAnnouncePort = 32413
)

// DefaultGDMPorts are the UDP ports PMS answers GDM on.
var DefaultGDMPorts = []int{32410, 32412, 32413, 32414}

// GDMInfo is what the responder advertises.
type GDMInfo struct {
	Name              string
	MachineIdentifier string
	Port              int
	Version           string
	UpdatedAt         time.Time
	// ContentType defaults to "plex/media-server".
	ContentType string
}

func (i GDMInfo) headers() string {
	ct := i.ContentType
	if ct == "" {
		ct = "plex/media-server"
	}
	updated := i.UpdatedAt
	if updated.IsZero() {
		updated = time.Now()
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Content-Type: %s\r\n", ct)
	fmt.Fprintf(&b, "Resource-Identifier: %s\r\n", i.MachineIdentifier)
	fmt.Fprintf(&b, "Name: %s\r\n", sanitizeHeader(i.Name))
	fmt.Fprintf(&b, "Port: %d\r\n", i.Port)
	fmt.Fprintf(&b, "Updated-At: %d\r\n", updated.Unix())
	fmt.Fprintf(&b, "Version: %s\r\n", i.Version)
	return b.String()
}

func sanitizeHeader(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' {
			return ' '
		}
		return r
	}, s)
}

// GDMResponse renders the reply to an M-SEARCH.
func GDMResponse(i GDMInfo) []byte {
	return []byte("HTTP/1.0 200 OK\r\n" + i.headers() + "\r\n")
}

// GDMHello renders an announcement ("HELLO * HTTP/1.0") or, with bye, a
// departure ("BYE * HTTP/1.0").
func GDMHello(i GDMInfo, bye bool) []byte {
	verb := "HELLO"
	if bye {
		verb = "BYE"
	}
	return []byte(verb + " * HTTP/1.0\r\n" + i.headers() + "\r\n")
}

// GDMOptions configures a GDMResponder.
type GDMOptions struct {
	// Info is called per request so name/version changes apply live.
	Info func() GDMInfo
	// Ports to listen on; DefaultGDMPorts when empty.
	Ports []int
	// ListenAddrs, when set, replaces Ports/multicast with plain unicast
	// sockets on these addresses (e.g. "127.0.0.1:0" in tests).
	ListenAddrs []string
	// Announce sends HELLO on start and every AnnounceInterval (default
	// 15m) to the multicast group, and BYE on stop. Off by default: clients
	// discover servers by searching, and current PMS builds rely on that.
	Announce         bool
	AnnounceInterval time.Duration
	Logger           *slog.Logger
}

// GDMResponder answers GDM discovery on the LAN.
type GDMResponder struct {
	o      GDMOptions
	mu     sync.Mutex
	conns  []*net.UDPConn
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewGDMResponder returns a responder; call Start.
func NewGDMResponder(o GDMOptions) *GDMResponder {
	if len(o.Ports) == 0 {
		o.Ports = DefaultGDMPorts
	}
	if o.AnnounceInterval <= 0 {
		o.AnnounceInterval = 15 * time.Minute
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	return &GDMResponder{o: o}
}

// Start opens the sockets and serves until ctx is done or Stop is called.
// Ports that cannot be bound (e.g. a real PMS on the same host) are logged
// and skipped; Start fails only when no socket could be opened.
func (g *GDMResponder) Start(ctx context.Context) error {
	if g.o.Info == nil {
		return errors.New("gdm: Info is required")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.cancel != nil {
		return errors.New("gdm: already started")
	}
	var conns []*net.UDPConn
	var errs []error
	if len(g.o.ListenAddrs) > 0 {
		for _, a := range g.o.ListenAddrs {
			ua, err := net.ResolveUDPAddr("udp4", a)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			c, err := net.ListenUDP("udp4", ua)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			conns = append(conns, c)
		}
	} else {
		group := net.ParseIP(GDMMulticastGroup)
		for _, p := range g.o.Ports {
			// ListenMulticastUDP binds the wildcard address with
			// SO_REUSEADDR, so it receives multicast, broadcast and unicast.
			c, err := net.ListenMulticastUDP("udp4", nil, &net.UDPAddr{IP: group, Port: p})
			if err != nil {
				c, err = net.ListenUDP("udp4", &net.UDPAddr{Port: p})
			}
			if err != nil {
				errs = append(errs, fmt.Errorf("port %d: %w", p, err))
				continue
			}
			conns = append(conns, c)
		}
	}
	for _, err := range errs {
		g.o.Logger.Warn("gdm listen failed", "err", err)
	}
	if len(conns) == 0 {
		return fmt.Errorf("gdm: no sockets: %w", errors.Join(errs...))
	}
	g.conns = conns
	ctx, cancel := context.WithCancel(ctx)
	g.cancel = cancel
	for _, c := range conns {
		g.wg.Add(1)
		go g.serve(ctx, c)
	}
	if g.o.Announce && len(g.o.ListenAddrs) == 0 {
		g.wg.Add(1)
		go g.announce(ctx)
	}
	go func() {
		<-ctx.Done()
		for _, c := range conns {
			_ = c.Close()
		}
	}()
	return nil
}

// Addrs returns the bound local addresses.
func (g *GDMResponder) Addrs() []net.Addr {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]net.Addr, len(g.conns))
	for i, c := range g.conns {
		out[i] = c.LocalAddr()
	}
	return out
}

// Stop closes the sockets and waits for the serve loops.
func (g *GDMResponder) Stop() {
	g.mu.Lock()
	cancel := g.cancel
	g.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	g.mu.Lock()
	for _, c := range g.conns {
		_ = c.Close()
	}
	g.mu.Unlock()
	g.wg.Wait()
	g.mu.Lock()
	g.cancel = nil
	g.conns = nil
	g.mu.Unlock()
}

func (g *GDMResponder) serve(ctx context.Context, c *net.UDPConn) {
	defer g.wg.Done()
	buf := make([]byte, 2048)
	for {
		n, src, err := c.ReadFromUDP(buf)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return
			}
			g.o.Logger.Debug("gdm read", "err", err)
			continue
		}
		if !isMSearch(buf[:n]) {
			continue
		}
		if _, err := c.WriteToUDP(GDMResponse(g.o.Info()), src); err != nil {
			g.o.Logger.Debug("gdm reply failed", "to", src, "err", err)
		}
	}
}

func isMSearch(b []byte) bool {
	line, _, _ := bytes.Cut(b, []byte("\n"))
	return bytes.HasPrefix(bytes.TrimSpace(line), []byte("M-SEARCH * HTTP/1."))
}

func (g *GDMResponder) announce(ctx context.Context) {
	defer g.wg.Done()
	dst := &net.UDPAddr{IP: net.ParseIP(GDMMulticastGroup), Port: GDMAnnouncePort}
	send := func(bye bool) {
		c, err := net.DialUDP("udp4", nil, dst)
		if err != nil {
			g.o.Logger.Debug("gdm announce", "err", err)
			return
		}
		defer c.Close()
		_, _ = c.Write(GDMHello(g.o.Info(), bye))
	}
	send(false)
	t := time.NewTicker(g.o.AnnounceInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			send(true)
			return
		case <-t.C:
			send(false)
		}
	}
}

// GDMReply is one server's answer to a GDM search.
type GDMReply struct {
	From              *net.UDPAddr
	ContentType       string
	Name              string
	MachineIdentifier string
	Port              int
	Version           string
	UpdatedAt         time.Time
	Headers           http.Header
}

// ParseGDMReply parses an "HTTP/1.0 200 OK" GDM reply.
func ParseGDMReply(b []byte) (*GDMReply, error) {
	r := textproto.NewReader(bufio.NewReader(bytes.NewReader(b)))
	status, err := r.ReadLine()
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(status, "HTTP/1.") || !strings.Contains(status, " 200") {
		return nil, fmt.Errorf("gdm: unexpected status line %q", status)
	}
	h, err := r.ReadMIMEHeader()
	if err != nil && len(h) == 0 {
		return nil, err
	}
	rep := &GDMReply{
		Headers:           http.Header(h),
		ContentType:       h.Get("Content-Type"),
		Name:              h.Get("Name"),
		MachineIdentifier: h.Get("Resource-Identifier"),
		Version:           h.Get("Version"),
	}
	rep.Port, _ = strconv.Atoi(h.Get("Port"))
	if s, err := strconv.ParseInt(h.Get("Updated-At"), 10, 64); err == nil {
		rep.UpdatedAt = time.Unix(s, 0)
	}
	return rep, nil
}

// DiscoverGDM sends one M-SEARCH to target ("239.0.0.250:32414" when empty)
// and collects replies until ctx is done or wait elapses.
func DiscoverGDM(ctx context.Context, target string, wait time.Duration) ([]GDMReply, error) {
	if target == "" {
		target = net.JoinHostPort(GDMMulticastGroup, strconv.Itoa(GDMServerPort))
	}
	if wait <= 0 {
		wait = 2 * time.Second
	}
	raddr, err := net.ResolveUDPAddr("udp4", target)
	if err != nil {
		return nil, err
	}
	conn, err := net.ListenUDP("udp4", nil)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	deadline := time.Now().Add(wait)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Now()) })
	defer stop()
	if _, err := conn.WriteToUDP([]byte("M-SEARCH * HTTP/1.1\r\n\r\n"), raddr); err != nil {
		return nil, err
	}
	var out []GDMReply
	buf := make([]byte, 2048)
	for {
		n, src, err := conn.ReadFromUDP(buf)
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				return out, nil
			}
			return out, err
		}
		rep, err := ParseGDMReply(buf[:n])
		if err != nil {
			continue
		}
		rep.From = src
		out = append(out, *rep)
	}
}
