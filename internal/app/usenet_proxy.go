package app

import (
	"context"
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"

	"golang.org/x/net/proxy"
)

// usenetProxyDialer routes NNTP through an explicitly configured node-local
// SOCKS5 gateway. HTTP_PROXY does not apply to NNTP. Provider TLS still wraps
// this connection in usenet.Dial, using the original provider hostname.
// A failed gateway never falls back to a direct connection (and a different IP).
func usenetProxyDialer(raw string) (func(context.Context, string, string) (net.Conn, error), error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	invalid := errors.New("ETHER_USENET_PROXY must be a socks5://host:port URL")
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "socks5" || u.Hostname() == "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return nil, invalid
	}
	_, port, err := net.SplitHostPort(u.Host)
	n, portErr := strconv.Atoi(port)
	if err != nil || portErr != nil || n < 1 || n > 65535 {
		return nil, invalid
	}
	d, err := proxy.FromURL(u, &net.Dialer{})
	if err != nil {
		return nil, invalid // Never echo credentials from the proxy URL.
	}
	cd, ok := d.(proxy.ContextDialer)
	if !ok {
		return nil, errors.New("Usenet proxy does not support cancellable connections")
	}
	return cd.DialContext, nil
}
