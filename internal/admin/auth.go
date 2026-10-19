package admin

import (
	"crypto/subtle"
	"net"
	"net/http"
	"strings"
)

// tokenFromRequest extracts the owner credential from headers. Cookies are
// never consulted (CSRF posture). Query strings are never consulted either,
// so tokens stay out of access logs.
func tokenFromRequest(r *http.Request) string {
	if t := strings.TrimSpace(r.Header.Get("X-Plex-Token")); t != "" {
		return t
	}
	if t := strings.TrimSpace(r.Header.Get("X-Ether-Admin-Token")); t != "" {
		return t
	}
	if a := strings.TrimSpace(r.Header.Get("Authorization")); a != "" {
		if rest, ok := strings.CutPrefix(a, "Bearer "); ok {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}

func (h *Handler) isOwner(r *http.Request, token string) bool {
	if token == "" {
		return false
	}
	if h.deps.IsOwnerToken != nil {
		return h.deps.IsOwnerToken(r.Context(), token)
	}
	stored := h.deps.Config.Get().Plex.Token
	if stored == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(token), []byte(stored)) == 1
}

// isLocalOrLAN reports whether the direct peer is loopback, RFC-1918
// private, link-local, or a ULA address. Forwarded headers are ignored:
// the peer that opened the connection is what matters.
func isLocalOrLAN(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified()
}

// requireAuth implements the admin access policy:
//
//   - A valid owner token always passes, from anywhere.
//   - Before setup is complete, requests from localhost/LAN pass without a
//     token (there is no owner credential yet); remote requests do not.
//   - After setup, a valid owner token is mandatory.
//   - Mutating requests additionally must pass the CSRF header check.
func (h *Handler) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !csrfOK(r) {
			writeError(w, http.StatusForbidden, "csrf", "mutating requests require the X-Ether-Request: 1 header and a JSON content type")
			return
		}
		token := tokenFromRequest(r)
		if h.isOwner(r, token) {
			next.ServeHTTP(w, r)
			return
		}
		if !h.deps.Config.Get().SetupComplete {
			if isLocalOrLAN(r) {
				next.ServeHTTP(w, r)
				return
			}
			writeError(w, http.StatusForbidden, "setup_local_only", "setup is only available from the local network until it is complete")
			return
		}
		w.Header().Set("WWW-Authenticate", `Bearer realm="ether-admin"`)
		writeError(w, http.StatusUnauthorized, "unauthorized", "a valid owner X-Plex-Token is required")
	}
}

// handleBootstrap is intentionally public and minimal: version, whether
// setup is complete, and whether the caller will need a token. It carries
// no secrets and no configuration.
func (h *Handler) handleBootstrap(w http.ResponseWriter, r *http.Request) {
	cfg := h.deps.Config.Get()
	token := tokenFromRequest(r)
	authed := h.isOwner(r, token) || (!cfg.SetupComplete && isLocalOrLAN(r))
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":            true,
		"version":       h.version,
		"setupComplete": cfg.SetupComplete,
		"authenticated": authed,
		"authRequired":  cfg.SetupComplete,
		"localTrusted":  false,
		"signIn":        h.deps.SignIn != nil,
		"serverName":    cfg.Server.FriendlyName,
	})
}
