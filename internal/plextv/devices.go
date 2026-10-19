package plextv

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"net/url"
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
	PrivateAddress string // primary LAN address
	PrivatePort    int    // listener port
	Version        string
	LocalAddresses []string // LAN IPs
	// MappingState mirrors PMS's states: "mapped", "unmapped", "unknown".
	MappingState string
}

// RegisterServer sends ServerRegistration: POST /servers.xml?auth_token=…&async=1.
// The endpoint and its 201 response are confirmed from PMS logs; the field
// names follow the registration/resource vocabulary found in the PMS
// executable. The live request body is not present in DEBUG logs; failures
// remain non-fatal because clients discover connections through resources.
func (c *Client) RegisterServer(ctx context.Context, serverToken string, reg ServerRegistration) error {
	q := url.Values{"async": {"1"}}
	// The PMS registration request uses XML. Keep this to resource fields whose
	// names are present in the PMS vocabulary rather than sending guessed
	// aliases for the separate connectivity status record.
	type serverElement struct {
		XMLName           xml.Name `xml:"Server"`
		Name              string   `xml:"name,attr"`
		Address           string   `xml:"address,attr,omitempty"`
		Port              int      `xml:"port,attr,omitempty"`
		Version           string   `xml:"version,attr,omitempty"`
		MachineIdentifier string   `xml:"machineIdentifier,attr"`
		LocalAddresses    string   `xml:"localAddresses,attr,omitempty"`
	}
	body, err := xml.Marshal(serverElement{Name: reg.Name, Address: reg.Address, Port: reg.Port,
		Version: reg.Version, MachineIdentifier: c.id.ClientIdentifier, LocalAddresses: strings.Join(reg.LocalAddresses, ",")})
	if err != nil {
		return fmt.Errorf("plextv: encode server registration: %w", err)
	}
	_, err = c.do(ctx, request{
		method:      http.MethodPost,
		host:        c.serversHost(),
		path:        "/servers.xml",
		query:       q,
		token:       serverToken,
		tokenQuery:  "auth_token",
		body:        body,
		contentType: "application/xml",
		accept:      "application/xml",
	})
	return err
}

// ReportConnectivity starts Plex's asynchronous reachability check. The PMS
// sends PUT /api/servers/{machineIdentifier}/connectivity with its server
// token and a fresh asyncIdentifier immediately after servers.xml registration.
func (c *Client) ReportConnectivity(ctx context.Context, serverToken string) (string, error) {
	if c.id.ClientIdentifier == "" {
		return "", errors.New("plextv: machine identifier required")
	}
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("plextv: connectivity id: %w", err)
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	identifier := fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:16])
	_, err := c.do(ctx, request{method: http.MethodPut, host: c.serversHost(), path: "/api/servers/" + url.PathEscape(c.id.ClientIdentifier) + "/connectivity", query: url.Values{"asyncIdentifier": {identifier}}, token: serverToken, tokenQuery: "X-Plex-Token", accept: "application/json, application/xml;q=0.9"})
	if err != nil {
		return "", err
	}
	return identifier, nil
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

// UnmarshalJSON accepts Plex's boolean flags serialized as either JSON
// booleans or numeric 0/1 values.
func (r *Resource) UnmarshalJSON(b []byte) error {
	type wire struct {
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
		Owned                  json.RawMessage      `json:"owned"`
		Home                   json.RawMessage      `json:"home"`
		Synced                 json.RawMessage      `json:"synced"`
		Relay                  json.RawMessage      `json:"relay"`
		Presence               json.RawMessage      `json:"presence"`
		HTTPSRequired          json.RawMessage      `json:"httpsRequired"`
		PublicAddressMatches   json.RawMessage      `json:"publicAddressMatches"`
		DNSRebindingProtection json.RawMessage      `json:"dnsRebindingProtection"`
		NatLoopbackSupported   json.RawMessage      `json:"natLoopbackSupported"`
		Connections            []ResourceConnection `json:"connections"`
	}
	var v wire
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	*r = Resource{Name: v.Name, Product: v.Product, ProductVersion: v.ProductVersion, Platform: v.Platform, PlatformVersion: v.PlatformVersion, Device: v.Device, ClientIdentifier: v.ClientIdentifier, CreatedAt: v.CreatedAt, LastSeenAt: v.LastSeenAt, Provides: v.Provides, OwnerID: v.OwnerID, SourceTitle: v.SourceTitle, PublicAddress: v.PublicAddress, AccessToken: v.AccessToken, Owned: plexBool(v.Owned), Home: plexBool(v.Home), Synced: plexBool(v.Synced), Relay: plexBool(v.Relay), Presence: plexBool(v.Presence), HTTPSRequired: plexBool(v.HTTPSRequired), PublicAddressMatches: plexBool(v.PublicAddressMatches), DNSRebindingProtection: plexBool(v.DNSRebindingProtection), NatLoopbackSupported: plexBool(v.NatLoopbackSupported), Connections: v.Connections}
	return nil
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

// UnmarshalJSON handles the integer IPv6 flag emitted by current resources
// responses as well as boolean variants used by other Plex API versions.
func (c *ResourceConnection) UnmarshalJSON(b []byte) error {
	var v struct {
		Protocol string          `json:"protocol"`
		Address  string          `json:"address"`
		Port     int             `json:"port"`
		URI      string          `json:"uri"`
		Local    json.RawMessage `json:"local"`
		Relay    json.RawMessage `json:"relay"`
		IPv6     json.RawMessage `json:"IPv6"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	*c = ResourceConnection{Protocol: v.Protocol, Address: v.Address, Port: v.Port, URI: v.URI, Local: plexBool(v.Local), Relay: plexBool(v.Relay), IPv6: plexBool(v.IPv6)}
	return nil
}

func plexBool(raw json.RawMessage) bool {
	if len(raw) == 0 || string(raw) == "null" {
		return false
	}
	var b bool
	if json.Unmarshal(raw, &b) == nil {
		return b
	}
	var n int
	if json.Unmarshal(raw, &n) == nil {
		return n != 0
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s == "1" || strings.EqualFold(s, "true")
	}
	return false
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
