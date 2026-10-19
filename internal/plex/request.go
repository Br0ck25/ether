package plex

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/plex/model"
)

// Request is the parsed Plex request context: every X-Plex-* header (or its
// query-string equivalent), paging, the output format and the remote address.
type Request struct {
	Token              string
	ClientID           string // X-Plex-Client-Identifier
	Product            string // X-Plex-Product
	Version            string // X-Plex-Version
	Platform           string // X-Plex-Platform
	PlatformVersion    string // X-Plex-Platform-Version
	Device             string // X-Plex-Device
	DeviceName         string // X-Plex-Device-Name
	Model              string // X-Plex-Model
	Vendor             string // X-Plex-Device-Vendor
	Language           string // X-Plex-Language
	Marketplace        string // X-Plex-Marketplace
	Provides           string // X-Plex-Provides
	Features           string // X-Plex-Features
	ProfileExtra       string // X-Plex-Client-Profile-Extra
	ProfileName        string // X-Plex-Client-Profile-Name
	ClientCapabilities string // X-Plex-Client-Capabilities (legacy)
	SessionID          string // X-Plex-Session-Identifier
	PMSAPIVersion      string // X-Plex-Pms-Api-Version

	// Paging (X-Plex-Container-Start/Size); HasPage is false when the client
	// did not ask for a window.
	Start   int
	Size    int
	HasPage bool

	Format   model.Format
	RemoteIP netip.Addr
	Remote   bool // not on a private/loopback network

	User *core.User
}

type ctxKey struct{}

// FromContext returns the request context attached by the middleware.
func FromContext(ctx context.Context) *Request {
	if r, ok := ctx.Value(ctxKey{}).(*Request); ok {
		return r
	}
	return &Request{}
}

func withRequest(ctx context.Context, pr *Request) context.Context {
	return context.WithValue(ctx, ctxKey{}, pr)
}

// plexParam reads an X-Plex-* value from the header or the query string.
func plexParam(r *http.Request, name string) string {
	if v := r.Header.Get(name); v != "" {
		return strings.TrimSpace(v)
	}
	q := r.URL.Query()
	if v := q.Get(name); v != "" {
		return strings.TrimSpace(v)
	}
	// Some clients lower-case query parameter names.
	for k, vs := range q {
		if strings.EqualFold(k, name) && len(vs) > 0 {
			return strings.TrimSpace(vs[0])
		}
	}
	return ""
}

// ParseRequest builds the request context from an HTTP request.
// trustProxy controls whether X-Forwarded-For / X-Real-IP are honoured when
// the direct peer is loopback (a local reverse proxy).
func ParseRequest(r *http.Request) *Request {
	pr := &Request{
		Token:              plexParam(r, "X-Plex-Token"),
		ClientID:           plexParam(r, "X-Plex-Client-Identifier"),
		Product:            plexParam(r, "X-Plex-Product"),
		Version:            plexParam(r, "X-Plex-Version"),
		Platform:           plexParam(r, "X-Plex-Platform"),
		PlatformVersion:    plexParam(r, "X-Plex-Platform-Version"),
		Device:             plexParam(r, "X-Plex-Device"),
		DeviceName:         plexParam(r, "X-Plex-Device-Name"),
		Model:              plexParam(r, "X-Plex-Model"),
		Vendor:             plexParam(r, "X-Plex-Device-Vendor"),
		Language:           plexParam(r, "X-Plex-Language"),
		Marketplace:        plexParam(r, "X-Plex-Marketplace"),
		Provides:           plexParam(r, "X-Plex-Provides"),
		Features:           plexParam(r, "X-Plex-Features"),
		ProfileExtra:       plexParam(r, "X-Plex-Client-Profile-Extra"),
		ProfileName:        plexParam(r, "X-Plex-Client-Profile-Name"),
		ClientCapabilities: plexParam(r, "X-Plex-Client-Capabilities"),
		SessionID:          plexParam(r, "X-Plex-Session-Identifier"),
		PMSAPIVersion:      plexParam(r, "X-Plex-Pms-Api-Version"),
	}
	if pr.ClientID == "" {
		pr.ClientID = plexParam(r, "X-Plex-Client-Id")
	}
	if pr.SessionID == "" {
		pr.SessionID = plexParam(r, "X-Plex-Session-Id")
	}
	// Paging.
	startS := plexParam(r, "X-Plex-Container-Start")
	sizeS := plexParam(r, "X-Plex-Container-Size")
	if sizeS != "" || startS != "" {
		start, _ := strconv.Atoi(startS)
		size := -1
		if sizeS != "" {
			if n, err := strconv.Atoi(sizeS); err == nil {
				size = n
			}
		}
		if start < 0 {
			start = 0
		}
		pr.Start, pr.Size, pr.HasPage = start, size, true
	}
	pr.Format = model.XML
	if wantsJSON(r) {
		pr.Format = model.JSON
	}
	pr.RemoteIP = clientIP(r)
	pr.Remote = !isLocalAddr(pr.RemoteIP)
	return pr
}

func wantsJSON(r *http.Request) bool {
	for _, a := range r.Header.Values("Accept") {
		for _, part := range strings.Split(a, ",") {
			mt, _, _ := strings.Cut(strings.TrimSpace(part), ";")
			if strings.EqualFold(mt, "application/json") {
				return true
			}
		}
	}
	return false
}

// clientIP is the peer address, or the first forwarded address when the
// peer is a loopback reverse proxy.
func clientIP(r *http.Request) netip.Addr {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip, err := netip.ParseAddr(strings.Trim(host, "[]"))
	if err != nil {
		return netip.Addr{}
	}
	ip = ip.Unmap()
	if ip.IsLoopback() {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			first, _, _ := strings.Cut(xff, ",")
			if fip, err := netip.ParseAddr(strings.TrimSpace(first)); err == nil {
				return fip.Unmap()
			}
		}
		if xr := r.Header.Get("X-Real-IP"); xr != "" {
			if fip, err := netip.ParseAddr(strings.TrimSpace(xr)); err == nil {
				return fip.Unmap()
			}
		}
	}
	return ip
}

var cgnat = netip.MustParsePrefix("100.64.0.0/10") // Tailscale and other overlays

// isLocalAddr reports whether ip is on a LAN-like network.
func isLocalAddr(ip netip.Addr) bool {
	if !ip.IsValid() {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || cgnat.Contains(ip)
}

// window applies the client's paging request to a list of n items and
// returns [lo, hi).
func (pr *Request) window(n int) (lo, hi int) {
	if !pr.HasPage {
		return 0, n
	}
	lo = min(pr.Start, n)
	if pr.Size < 0 {
		return lo, n
	}
	return lo, min(lo+pr.Size, n)
}
