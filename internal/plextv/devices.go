package plextv

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ether-media/ether/internal/core"
)

// PublishConnections publishes the server's connection URIs on its plex.tv
// device record: PUT /devices/{machineIdentifier}?Connection[][uri]=...
// (one Connection[][uri] per URI; an empty list publishes a single empty
// value, which clears them, as PMS does when it has no addresses).
//
// c must carry the server identity: plex.tv associates the PUT with the
// device whose client identifier matches the path.
func (c *Client) PublishConnections(ctx context.Context, serverToken string, uris []string) error {
	if c.id.ClientIdentifier == "" {
		return errors.New("plextv: machine identifier required")
	}
	if len(uris) == 0 {
		uris = []string{""}
	}
	_, err := c.do(ctx, request{
		method: http.MethodPut,
		path:   "/devices/" + url.PathEscape(c.id.ClientIdentifier),
		query:  url.Values{"Connection[][uri]": uris},
		token:  serverToken,
		accept: "application/xml",
	})
	return err
}

// ServerRegistration is the server info PMS sends to POST /servers.xml
// ("MyPlex: Sending Server Info to myPlex (user=…, ip=…, port=…)"), which
// is where plex.tv learns the public address/port and the mapping state.
type ServerRegistration struct {
	Name           string
	Address        string // public IP; empty lets plex.tv use the request's source address
	Port           int    // public port
	Version        string
	LocalAddresses []string // LAN IPs
	// MappingState mirrors PMS's states: "mapped", "unmapped", "unknown".
	MappingState string
}

// RegisterServer sends ServerRegistration: POST /servers.xml?auth_token=…&async=1.
// The endpoint and its 201 response are confirmed from PMS logs; the field
// names are the attribute names plex.tv's own /servers.xml listing uses,
// since the PMS request body itself is not publicly documented. Callers
// should treat failures as non-fatal: PublishConnections is what populates
// the resources list clients read.
func (c *Client) RegisterServer(ctx context.Context, serverToken string, reg ServerRegistration) error {
	q := url.Values{"async": {"1"}}
	form := url.Values{}
	form.Set("name", reg.Name)
	form.Set("machineIdentifier", c.id.ClientIdentifier)
	form.Set("version", reg.Version)
	if reg.Address != "" {
		form.Set("address", reg.Address)
	}
	if reg.Port > 0 {
		form.Set("port", strconv.Itoa(reg.Port))
	}
	if len(reg.LocalAddresses) > 0 {
		form.Set("localAddresses", strings.Join(reg.LocalAddresses, ","))
	}
	if reg.MappingState != "" {
		form.Set("mappingState", reg.MappingState)
	}
	_, err := c.do(ctx, request{
		method:      http.MethodPost,
		path:        "/servers.xml",
		query:       q,
		token:       serverToken,
		tokenQuery:  "auth_token",
		body:        []byte(form.Encode()),
		contentType: "application/x-www-form-urlencoded",
		accept:      "application/xml",
	})
	return err
}

// Resource is a device in the account's resources list
// (GET /api/v2/resources), which is how official apps find servers.
type Resource struct {
	Name                   string               `json:"name"`
	Product                string               `json:"product"`
	ProductVersion         string               `json:"productVersion"`
	Platform               string               `json:"platform"`
	PlatformVersion        string               `json:"platformVersion"`
	Device                 string               `json:"device"`
	ClientIdentifier       string               `json:"clientIdentifier"`
	CreatedAt              flexTime             `json:"createdAt"`
	LastSeenAt             flexTime             `json:"lastSeenAt"`
	Provides               string               `json:"provides"`
	OwnerID                *int64               `json:"ownerId"`
	SourceTitle            string               `json:"sourceTitle"`
	PublicAddress          string               `json:"publicAddress"`
	AccessToken            string               `json:"accessToken"`
	Owned                  bool                 `json:"owned"`
	Home                   bool                 `json:"home"`
	Synced                 bool                 `json:"synced"`
	Relay                  bool                 `json:"relay"`
	Presence               bool                 `json:"presence"`
	HTTPSRequired          bool                 `json:"httpsRequired"`
	PublicAddressMatches   bool                 `json:"publicAddressMatches"`
	DNSRebindingProtection bool                 `json:"dnsRebindingProtection"`
	NatLoopbackSupported   bool                 `json:"natLoopbackSupported"`
	Connections            []ResourceConnection `json:"connections"`
}

// ResourceConnection is one published way to reach a resource.
type ResourceConnection struct {
	Protocol string `json:"protocol"`
	Address  string `json:"address"`
	Port     int    `json:"port"`
	URI      string `json:"uri"`
	Local    bool   `json:"local"`
	Relay    bool   `json:"relay"`
	IPv6     bool   `json:"IPv6"`
}

// ProvidesServer reports whether the resource is a media server.
func (r Resource) ProvidesServer() bool {
	for _, p := range strings.Split(r.Provides, ",") {
		if strings.TrimSpace(p) == "server" {
			return true
		}
	}
	return false
}

// Resources lists the devices visible to token, with plex.direct https
// connections, relays and IPv6 included, as official apps request it.
func (c *Client) Resources(ctx context.Context, token string) ([]Resource, error) {
	resp, err := c.do(ctx, request{
		method: http.MethodGet,
		path:   "/api/v2/resources",
		query:  url.Values{"includeHttps": {"1"}, "includeRelay": {"1"}, "includeIPv6": {"1"}},
		token:  token,
	})
	if err != nil {
		return nil, err
	}
	var out []Resource
	if err := json.Unmarshal(resp.body, &out); err != nil {
		return nil, fmt.Errorf("plextv: decode resources: %w", err)
	}
	return out, nil
}

// FindServer returns this server's (machineID's) entry in token's resources,
// or core.ErrNotFound.
func (c *Client) FindServer(ctx context.Context, token, machineID string) (*Resource, error) {
	rs, err := c.Resources(ctx, token)
	if err != nil {
		return nil, err
	}
	for i := range rs {
		if strings.EqualFold(rs[i].ClientIdentifier, machineID) {
			return &rs[i], nil
		}
	}
	return nil, core.ErrNotFound
}

// LastSeen is when plex.tv last saw the resource; zero when unknown.
func (r Resource) LastSeen() time.Time { return r.LastSeenAt.Time }
