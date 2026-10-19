package plextv

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

type OnlineSource struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// OnlineSources reads the authenticated account's published source settings.
// Account scope is required; a shared server access token is insufficient.
func (c *Client) OnlineSources(ctx context.Context, token, uuid string) ([]OnlineSource, error) {
	if uuid == "" {
		return nil, fmt.Errorf("account UUID is required")
	}
	r, err := c.do(ctx, request{method: http.MethodGet, path: "/api/v2/user/" + url.PathEscape(uuid) + "/settings/opt_outs", token: token, accept: "application/xml"})
	if err != nil {
		return nil, err
	}
	n, err := parseXMLNode(r.body)
	if err != nil {
		return nil, err
	}
	var out []OnlineSource
	for _, x := range n.findAll("optOut") {
		out = append(out, OnlineSource{x.attr("key"), x.attr("value")})
	}
	return out, nil
}

// CleanHome disables Plex-owned online sources for the owner and their managed
// accounts. It uses the account's advertised settings and never changes Pass.
func (c *Client) CleanHome(ctx context.Context, token string) ([]OnlineSource, error) {
	a, err := c.User(ctx, token)
	if err != nil {
		return nil, err
	}
	sources, err := c.OnlineSources(ctx, token, a.UUID)
	if err != nil {
		return nil, err
	}
	for i := range sources {
		x := &sources[i]
		if !strings.HasPrefix(x.Key, "tv.plex.provider.") && !strings.HasPrefix(x.Key, "tv.plex.providers.") {
			continue
		}
		if x.Value == "opt_out" {
			continue
		}
		_, err := c.do(ctx, request{method: http.MethodPost, path: "/api/v2/user/" + url.PathEscape(a.UUID) + "/settings/opt_outs", token: token, query: url.Values{"key": {x.Key}, "value": {"opt_out"}}})
		if err != nil {
			return sources, err
		}
		x.Value = "opt_out"
	}
	return sources, nil
}

// ShareWith shares Ether's library sections without enabling paid entitlements.
func (c *Client) ShareWith(ctx context.Context, token, email string, sections []int) error {
	if strings.TrimSpace(email) == "" || len(sections) == 0 {
		return fmt.Errorf("email and library sections are required")
	}
	body, err := marshalShare(c.id.ClientIdentifier, email, sections)
	if err != nil {
		return err
	}
	_, err = c.do(ctx, request{method: http.MethodPost, path: "/api/servers/" + url.PathEscape(c.id.ClientIdentifier) + "/shared_servers", token: token, body: body, contentType: "application/json"})
	return err
}
