package remote

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// Secure-connection modes, matching config.Plex.SecureConnections and PMS's
// "Secure connections" preference.
const (
	SecureRequired  = "required"
	SecurePreferred = "preferred"
	SecureDisabled  = "disabled"
)

// Connection is one way to reach the server, in the shape plex.tv's
// resources API returns it.
type Connection struct {
	Protocol string `json:"protocol"` // "http" or "https"
	Address  string `json:"address"`  // IP or host
	Port     int    `json:"port"`
	URI      string `json:"uri"`
	Local    bool   `json:"local"`
	Relay    bool   `json:"relay,omitempty"`
	IPv6     bool   `json:"IPv6,omitempty"`
	Custom   bool   `json:"custom,omitempty"` // owner-supplied custom URL
}

// PlexDirectHost returns the plex.direct host for ip under a certificate
// hash: 192.168.1.5 -> "192-168-1-5.<hash>.plex.direct". IPv6 addresses use
// their expanded colon groups joined by dashes, as plex.tv does.
func PlexDirectHost(ip net.IP, hash string) string {
	return PlexDirectLabel(ip) + "." + strings.ToLower(hash) + ".plex.direct"
}

// PlexDirectLabel is the IP-encoding first label of a plex.direct host.
func PlexDirectLabel(ip net.IP) string {
	if v4 := ip.To4(); v4 != nil {
		return strings.ReplaceAll(v4.String(), ".", "-")
	}
	ip16 := ip.To16()
	if ip16 == nil {
		return ""
	}
	parts := make([]string, 8)
	for i := 0; i < 8; i++ {
		parts[i] = strconv.FormatUint(uint64(ip16[2*i])<<8|uint64(ip16[2*i+1]), 16)
	}
	return strings.Join(parts, "-")
}

// ParsePlexDirectHost reverses PlexDirectHost. ok is false for hosts that
// are not "<ip-label>.<hash>.plex.direct".
func ParsePlexDirectHost(host string) (ip net.IP, hash string, ok bool) {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if !strings.HasSuffix(host, ".plex.direct") {
		return nil, "", false
	}
	labels := strings.Split(strings.TrimSuffix(host, ".plex.direct"), ".")
	if len(labels) != 2 || labels[0] == "" || labels[1] == "" {
		return nil, "", false
	}
	l := labels[0]
	if strings.Count(l, "-") == 3 {
		ip = net.ParseIP(strings.ReplaceAll(l, "-", ".")).To4()
	} else {
		ip = net.ParseIP(strings.ReplaceAll(l, "-", ":"))
	}
	if ip == nil {
		return nil, "", false
	}
	return ip, labels[1], true
}

// BuildOptions describes the server's addresses for BuildConnections.
type BuildOptions struct {
	LAN        []net.IP
	WAN        net.IP // nil when unknown or remote access is off
	LocalPort  int    // port the server listens on (32400)
	PublicPort int    // external port on the WAN; 0 = LocalPort
	// CertHash is the plex.direct hash of a valid plex.direct certificate.
	// Without it no https plex.direct URIs are produced.
	CertHash string
	// Secure is SecureRequired, SecurePreferred (default) or SecureDisabled.
	Secure string
	// CustomURLs are owner-supplied URLs (reverse proxies, DDNS names).
	CustomURLs []string
	// HTTPSOnPlainIP adds https://<ip>:port URIs (self-signed setups). Off
	// by default because clients cannot validate a certificate for a bare IP.
	HTTPSOnPlainIP bool
}

// BuildConnections returns the connection list for the server, best first:
// LAN before WAN, https plex.direct before plain http (unless secure
// connections are disabled), then custom URLs.
func BuildConnections(o BuildOptions) []Connection {
	if o.LocalPort == 0 {
		o.LocalPort = 32400
	}
	if o.PublicPort == 0 {
		o.PublicPort = o.LocalPort
	}
	secure := o.Secure
	if secure == "" {
		secure = SecurePreferred
	}
	wantHTTPS := secure != SecureDisabled && o.CertHash != ""
	wantHTTP := secure != SecureRequired || (o.CertHash == "" && !o.HTTPSOnPlainIP)

	var out []Connection
	seen := map[string]bool{}
	add := func(c Connection) {
		if c.URI == "" || seen[c.URI] {
			return
		}
		seen[c.URI] = true
		out = append(out, c)
	}
	addIP := func(ip net.IP, port int, local bool) {
		if ip == nil {
			return
		}
		v6 := ip.To4() == nil
		addr := ip.String()
		if wantHTTPS {
			host := PlexDirectHost(ip, o.CertHash)
			add(Connection{Protocol: "https", Address: addr, Port: port, Local: local, IPv6: v6,
				URI: "https://" + net.JoinHostPort(host, strconv.Itoa(port))})
		}
		if o.HTTPSOnPlainIP && secure != SecureDisabled && !wantHTTPS {
			add(Connection{Protocol: "https", Address: addr, Port: port, Local: local, IPv6: v6,
				URI: "https://" + net.JoinHostPort(addr, strconv.Itoa(port))})
		}
		if wantHTTP {
			add(Connection{Protocol: "http", Address: addr, Port: port, Local: local, IPv6: v6,
				URI: "http://" + net.JoinHostPort(addr, strconv.Itoa(port))})
		}
	}
	for _, ip := range o.LAN {
		addIP(ip, o.LocalPort, true)
	}
	if o.WAN != nil {
		addIP(o.WAN, o.PublicPort, false)
	}
	for _, raw := range o.CustomURLs {
		c, err := ParseCustomURL(raw)
		if err != nil {
			continue
		}
		add(c)
	}
	return out
}

// ParseCustomURL validates an owner-supplied connection URL.
func ParseCustomURL(raw string) (Connection, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return Connection{}, err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return Connection{}, fmt.Errorf("custom url %q: scheme must be http or https", raw)
	}
	if u.Hostname() == "" {
		return Connection{}, fmt.Errorf("custom url %q: missing host", raw)
	}
	port := 80
	if u.Scheme == "https" {
		port = 443
	}
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n <= 0 || n > 65535 {
			return Connection{}, fmt.Errorf("custom url %q: bad port", raw)
		}
		port = n
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawQuery, u.Fragment = "", ""
	ip := net.ParseIP(u.Hostname())
	return Connection{
		Protocol: u.Scheme,
		Address:  u.Hostname(),
		Port:     port,
		URI:      u.String(),
		Local:    ip != nil && (ip.IsPrivate() || ip.IsLoopback()),
		IPv6:     ip != nil && ip.To4() == nil,
		Custom:   true,
	}, nil
}

// URIs extracts the URI of each connection.
func URIs(cs []Connection) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.URI
	}
	return out
}
