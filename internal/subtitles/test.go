package subtitles

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/braedonsaunders/ether/internal/config"
)

// Test checks the owner's OpenSubtitles settings: with account credentials
// it logs in (which also validates the API key); otherwise it makes one
// API-key-only request.
func Test(ctx context.Context, cfg config.OpenSubtitles) error {
	return test(ctx, &http.Client{Timeout: 10 * time.Second}, defaultBaseURL, cfg)
}

func test(ctx context.Context, hc *http.Client, base string, cfg config.OpenSubtitles) error {
	key := strings.TrimSpace(cfg.APIKey)
	if key == "" {
		return errors.New("an OpenSubtitles API key is required")
	}
	base = strings.TrimSuffix(base, "/")
	var req *http.Request
	var err error
	if cfg.Username != "" {
		body, _ := json.Marshal(map[string]string{"username": cfg.Username, "password": cfg.Password})
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, base+"/login", bytes.NewReader(body))
		if err == nil {
			req.Header.Set("Content-Type", "application/json")
		}
	} else {
		req, err = http.NewRequestWithContext(ctx, http.MethodGet, base+"/infos/formats", nil)
	}
	if err != nil {
		return err
	}
	req.Header.Set("Api-Key", key)
	req.Header.Set("User-Agent", "Ether v1")
	req.Header.Set("Accept", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("opensubtitles: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	switch {
	case resp.StatusCode == http.StatusOK:
		return nil
	case resp.StatusCode == http.StatusUnauthorized && cfg.Username != "":
		return errors.New("OpenSubtitles rejected the username or password")
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return errors.New("OpenSubtitles rejected the API key")
	default:
		return fmt.Errorf("opensubtitles: %s", resp.Status)
	}
}
