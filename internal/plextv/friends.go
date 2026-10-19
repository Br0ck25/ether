package plextv

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// Friend is an account with access to this server and the server access
// token plex.tv issued it. Official apps send that token (from their
// resources list's accessToken) as X-Plex-Token when they talk to the
// server, so the server can recognise friends without asking plex.tv.
type Friend struct {
	PlexID     int64    `json:"plexId"`
	UUID       string   `json:"uuid,omitempty"`
	Username   string   `json:"username"`
	Title      string   `json:"title"`
	Email      string   `json:"email,omitempty"`
	Thumb      string   `json:"thumb,omitempty"`
	Token      string   `json:"-"`
	Owned      bool     `json:"owned"` // the owner's own access token
	Home       bool     `json:"home"`
	Restricted bool     `json:"restricted"` // managed user
	Pending    bool     `json:"pending"`    // invite not accepted yet
	AllLibs    bool     `json:"allLibraries"`
	Sections   []string `json:"sections,omitempty"` // shared section keys
}

// DisplayName prefers title over username.
func (f Friend) DisplayName() string {
	if f.Title != "" {
		return f.Title
	}
	return f.Username
}

// AccessTokens fetches the access tokens of every account that can use this
// server: GET /servers/{machineIdentifier}/access_tokens.xml, called with
// the server token and the server identity. This is the call PMS makes
// ("MyPlex: updating with N access tokens").
func (c *Client) AccessTokens(ctx context.Context, serverToken string) ([]Friend, error) {
	resp, err := c.do(ctx, request{
		method:     http.MethodGet,
		path:       "/servers/" + url.PathEscape(c.id.ClientIdentifier) + "/access_tokens.xml",
		query:      url.Values{"includeProfiles": {"1"}, "includeProviders": {"1"}},
		token:      serverToken,
		tokenQuery: "auth_token",
		accept:     "application/xml",
	})
	if err != nil {
		return nil, err
	}
	root, err := parseXMLNode(resp.body)
	if err != nil {
		return nil, fmt.Errorf("plextv: decode access tokens: %w", err)
	}
	var out []Friend
	for _, n := range root.findAll("access_token") {
		f := Friend{
			Token:      n.attr("token", "accessToken", "authToken"),
			PlexID:     n.intAttr("id", "userID", "user_id", "invitedId"),
			UUID:       n.attr("uuid"),
			Username:   n.attr("username"),
			Title:      n.attr("title"),
			Email:      n.attr("email"),
			Thumb:      n.attr("thumb"),
			Owned:      n.boolAttr("owned"),
			Home:       n.boolAttr("home"),
			Restricted: n.boolAttr("restricted"),
			AllLibs:    n.boolAttr("allLibraries", "all_libraries"),
		}
		for _, s := range n.findAll("section") {
			if k := s.attr("key", "id"); k != "" {
				f.Sections = append(f.Sections, k)
			}
		}
		if f.Token == "" {
			continue
		}
		out = append(out, f)
	}
	return out, nil
}

// SharedServers lists the owner's shares of this server with their access
// tokens: GET /api/servers/{machineIdentifier}/shared_servers (owner token).
// It is the documented fallback for AccessTokens.
func (c *Client) SharedServers(ctx context.Context, ownerToken string) ([]Friend, error) {
	resp, err := c.do(ctx, request{
		method: http.MethodGet,
		path:   "/api/servers/" + url.PathEscape(c.id.ClientIdentifier) + "/shared_servers",
		token:  ownerToken,
		accept: "application/xml",
	})
	if err != nil {
		return nil, err
	}
	root, err := parseXMLNode(resp.body)
	if err != nil {
		return nil, fmt.Errorf("plextv: decode shared servers: %w", err)
	}
	var out []Friend
	for _, n := range root.findAll("SharedServer") {
		f := Friend{
			Token:    n.attr("accessToken", "token"),
			PlexID:   n.intAttr("userID", "user_id"),
			Username: n.attr("username"),
			Title:    n.attr("username"),
			Email:    n.attr("email"),
			Owned:    n.boolAttr("owned"),
			AllLibs:  n.boolAttr("allLibraries"),
		}
		accepted := n.attr("acceptedAt")
		f.Pending = accepted == "" || accepted == "0"
		for _, s := range n.findAll("Section") {
			if s.attr("shared") == "0" {
				continue
			}
			if k := s.attr("key", "id"); k != "" {
				f.Sections = append(f.Sections, k)
			}
		}
		if f.Token == "" || f.PlexID == 0 {
			continue
		}
		out = append(out, f)
	}
	return out, nil
}

// SharedUser is an entry of the owner's friends/home list (GET /api/users).
type SharedUser struct {
	PlexID     int64
	UUID       string
	Username   string
	Title      string
	Email      string
	Thumb      string
	Home       bool
	Restricted bool
	// Servers are the machine identifiers shared with the user.
	Servers []string
}

// Users lists the owner's friends and home users: GET /api/users.
func (c *Client) Users(ctx context.Context, ownerToken string) ([]SharedUser, error) {
	resp, err := c.do(ctx, request{method: http.MethodGet, path: "/api/users", token: ownerToken, accept: "application/xml"})
	if err != nil {
		return nil, err
	}
	root, err := parseXMLNode(resp.body)
	if err != nil {
		return nil, fmt.Errorf("plextv: decode users: %w", err)
	}
	var out []SharedUser
	for _, n := range root.findAll("User") {
		u := SharedUser{
			PlexID:     n.intAttr("id"),
			UUID:       n.attr("uuid"),
			Username:   n.attr("username"),
			Title:      n.attr("title"),
			Email:      n.attr("email"),
			Thumb:      n.attr("thumb"),
			Home:       n.boolAttr("home"),
			Restricted: n.boolAttr("restricted"),
		}
		for i := range n.Nodes {
			s := &n.Nodes[i]
			if strings.EqualFold(s.XMLName.Local, "Server") {
				if m := s.attr("machineIdentifier"); m != "" {
					u.Servers = append(u.Servers, m)
				}
			}
		}
		if u.PlexID != 0 {
			out = append(out, u)
		}
	}
	return out, nil
}

// Friends returns everyone with access to this server and their tokens.
// It uses AccessTokens (what PMS calls) and falls back to SharedServers,
// then enriches entries with profile data from Users when available.
func (c *Client) Friends(ctx context.Context, serverToken string) ([]Friend, error) {
	friends, err := c.AccessTokens(ctx, serverToken)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		c.log.Warn("access_tokens.xml failed, trying shared_servers", "err", err)
		var err2 error
		friends, err2 = c.SharedServers(ctx, serverToken)
		if err2 != nil {
			return nil, errors.Join(err, err2)
		}
	}
	users, uerr := c.Users(ctx, serverToken)
	if uerr != nil {
		c.log.Debug("users enrichment failed", "err", uerr)
		return friends, nil
	}
	byID := make(map[int64]SharedUser, len(users))
	byName := make(map[string]SharedUser, len(users))
	for _, u := range users {
		byID[u.PlexID] = u
		if u.Username != "" {
			byName[strings.ToLower(u.Username)] = u
		}
	}
	for i := range friends {
		f := &friends[i]
		u, ok := byID[f.PlexID]
		if !ok && f.Username != "" {
			u, ok = byName[strings.ToLower(f.Username)]
		}
		if !ok {
			continue
		}
		if f.PlexID == 0 {
			f.PlexID = u.PlexID
		}
		fill := func(dst *string, src string) {
			if *dst == "" {
				*dst = src
			}
		}
		fill(&f.UUID, u.UUID)
		fill(&f.Username, u.Username)
		fill(&f.Title, u.Title)
		fill(&f.Email, u.Email)
		fill(&f.Thumb, u.Thumb)
		f.Home = f.Home || u.Home
		f.Restricted = f.Restricted || u.Restricted
	}
	return friends, nil
}
