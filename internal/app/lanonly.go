package app

import (
	"net"
	"net/http"
)

// lanOnly keeps the owner UI off the internet: with remote access on, the
// Plex API is reachable through the port forward, but /ether/ answers only
// loopback, private (RFC 1918/4193), link-local and CGNAT (Tailscale)
// clients. Ether binds one port, so this has to be enforced per request.
func lanOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil || !isLocalAddr(net.ParseIP(host)) {
			http.Error(w, "The Ether admin UI is only available on the local network.", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

var cgnat = &net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}

func isLocalAddr(ip net.IP) bool {
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || cgnat.Contains(ip)
}
