package newznab

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/braedonsaunders/ether/internal/config"
)

type prowlarrIndexer struct {
	ID           int             `json:"id"`
	Name         string          `json:"name"`
	Enable       bool            `json:"enable"`
	Protocol     json.RawMessage `json:"protocol"`
	Priority     int             `json:"priority"`
	Capabilities struct {
		Categories []struct {
			ID int `json:"id"`
		} `json:"categories"`
	} `json:"capabilities"`
}

// DiscoverProwlarr imports only enabled Usenet indexers. Each receives a stable
// source-scoped ID and its own Ether hit/grab budget, cache and reputation.
// Prowlarr's /{id}/api alias is intentionally used so ordinary Newznab URL
// construction and all existing budget enforcement apply unchanged.
func DiscoverProwlarr(ctx context.Context, cfg config.Prowlarr, opts Options) ([]config.Indexer, error) {
	base, err := url.Parse(strings.TrimRight(cfg.URL, "/"))
	if err != nil || base.Host == "" || (base.Scheme != "http" && base.Scheme != "https") || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return nil, fmt.Errorf("prowlarr: URL must be an HTTP(S) base URL")
	}
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("prowlarr: API key is required")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base.String()+"/api/v1/indexer", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Api-Key", cfg.APIKey)
	req.Header.Set("Accept", "application/json")
	cl := opts.HTTPClient
	if cl == nil {
		cl = &http.Client{Timeout: defaultHTTPTimeout}
	}
	resp, err := cl.Do(req)
	if err != nil {
		return nil, fmt.Errorf("prowlarr: discovery failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("prowlarr: discovery HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, (4<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 4<<20 {
		return nil, fmt.Errorf("prowlarr: response exceeds 4 MiB")
	}
	var items []prowlarrIndexer
	if err = json.Unmarshal(data, &items); err != nil {
		return nil, fmt.Errorf("prowlarr: invalid indexer response: %w", err)
	}
	sum := sha256.Sum256([]byte(base.String()))
	scope := hex.EncodeToString(sum[:6])
	var out []config.Indexer
	seen := map[int]bool{}
	for _, item := range items {
		// Prowlarr's public API serializes protocol as "usenet" (older exports
		// can contain its enum value 1); torrent indexers are never imported.
		protocol := strings.ToLower(strings.Trim(string(item.Protocol), "\""))
		if !item.Enable || item.ID <= 0 || seen[item.ID] || (protocol != "usenet" && protocol != "1") {
			continue
		}
		seen[item.ID] = true
		id := "prowlarr-" + scope + "-" + strconv.Itoa(item.ID)
		c := config.Indexer{ID: id, Name: "Prowlarr / " + item.Name + " [" + scope + ":" + strconv.Itoa(item.ID) + "]", URL: base.String() + "/" + strconv.Itoa(item.ID), APIKey: cfg.APIKey, DailyLimit: cfg.DailyLimit, DailyGrabs: cfg.DailyGrabs, Priority: cfg.Priority + item.Priority, Enabled: true}
		for _, cat := range item.Capabilities.Categories {
			if cat.ID >= 2000 && cat.ID < 3000 {
				c.MovieCategories = append(c.MovieCategories, cat.ID)
			}
			if cat.ID >= 5000 && cat.ID < 6000 {
				c.TVCategories = append(c.TVCategories, cat.ID)
			}
		}
		out = append(out, c)
	}
	return out, nil
}
