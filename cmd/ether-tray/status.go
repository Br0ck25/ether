package main

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Status is what the tray displays about the local server.
type Status struct {
	Running       bool
	MachineID     string
	FriendlyName  string
	Version       string
	SetupComplete *bool // nil when /ether/api/status is unreachable/unknown
	Streams       int   // active streams, -1 when unknown
}

// StatusClient polls the local Ether server: Plex's GET /identity (always
// served once the process is up) and the admin API's GET /ether/api/status
// (richer; optional — the tray still works against identity alone).
type StatusClient struct {
	baseURL string
	http    *http.Client
}

func NewStatusClient(baseURL string, hc *http.Client) *StatusClient {
	if hc == nil {
		hc = http.DefaultClient
	}
	return &StatusClient{baseURL: strings.TrimSuffix(baseURL, "/"), http: hc}
}

type identityXML struct {
	XMLName           xml.Name `xml:"MediaContainer"`
	MachineIdentifier string   `xml:"machineIdentifier,attr"`
	FriendlyName      string   `xml:"friendlyName,attr"`
	Version           string   `xml:"version,attr"`
}

// apiStatus mirrors the admin status endpoint loosely: unknown fields are
// ignored so tray and server can evolve independently.
type apiStatus struct {
	Version       string `json:"version"`
	FriendlyName  string `json:"friendlyName"`
	SetupComplete *bool  `json:"setupComplete"`
	ActiveStreams *int   `json:"activeStreams"`
	Streams       *int   `json:"streams"`
}

// Poll fetches current status. A transport error means "not running" and
// is reported as (Status{Running:false}, err) — callers that only render
// may ignore err.
func (c *StatusClient) Poll(ctx context.Context) (Status, error) {
	st := Status{Streams: -1}
	body, err := c.get(ctx, "/identity")
	if err != nil {
		return st, err
	}
	var id identityXML
	if err := xml.Unmarshal(body, &id); err != nil {
		return st, fmt.Errorf("parse /identity: %w", err)
	}
	st.Running = true
	st.MachineID = id.MachineIdentifier
	st.FriendlyName = id.FriendlyName
	st.Version = strings.TrimPrefix(id.Version, "v")

	// Richer status is best-effort.
	if body, err := c.get(ctx, "/ether/api/status"); err == nil {
		var api apiStatus
		if json.Unmarshal(body, &api) == nil {
			if api.Version != "" {
				st.Version = strings.TrimPrefix(api.Version, "v")
			}
			if api.FriendlyName != "" {
				st.FriendlyName = api.FriendlyName
			}
			st.SetupComplete = api.SetupComplete
			switch {
			case api.ActiveStreams != nil:
				st.Streams = *api.ActiveStreams
			case api.Streams != nil:
				st.Streams = *api.Streams
			}
		}
	}
	return st, nil
}

func (c *StatusClient) get(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	if path == "/identity" {
		// Identity is decoded as XML. Ether and PMS prefer JSON whenever
		// application/json appears anywhere in Accept, even after XML.
		req.Header.Set("Accept", "application/xml")
	} else {
		req.Header.Set("Accept", "application/json")
	}
	req.Header.Set("X-Plex-Product", "Ether Tray")
	req.Header.Set("X-Plex-Client-Identifier", "ether-tray")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", path, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
}
