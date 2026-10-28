package plextv

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// Feature is an entitlement returned for the authenticated server by plex.tv.
// Modern native clients identify these by UUID; older clients use names.
type Feature struct {
	ID   string `json:"id"`
	UUID string `json:"uuid"`
}

func (c *Client) Features(ctx context.Context, token string) ([]Feature, error) {
	if token == "" {
		return nil, &APIError{Method: http.MethodGet, Path: "/api/v2/features", Status: http.StatusUnauthorized, Message: "no token"}
	}
	resp, err := c.do(ctx, request{method: http.MethodGet, path: "/api/v2/features", token: token})
	if err != nil {
		return nil, err
	}
	var features []Feature
	if err := json.Unmarshal(resp.body, &features); err != nil {
		return nil, fmt.Errorf("plextv: decode features: %w", err)
	}
	if features == nil {
		return nil, fmt.Errorf("plextv: features response is not an array")
	}
	return features, nil
}

// ownerFeatures preserves legacy subscription names alongside the UUIDs that
// plex.tv actually returned. No entitlement identifiers are synthesized.
func ownerFeatures(features []Feature, names []string) []string {
	var out []string
	seen := make(map[string]bool, len(features)+len(names))
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	for _, f := range features {
		add(f.UUID)
	}
	for _, name := range names {
		add(name)
	}
	return out
}
