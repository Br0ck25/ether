package trakt

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/braedonsaunders/ether/internal/config"
)

// Test checks the owner's Trakt application credentials: Trakt accepts the
// client ID on a lightweight public request. The client secret is only
// exercised when a viewer connects their account.
func Test(ctx context.Context, cfg config.Trakt) error {
	return test(ctx, &http.Client{Timeout: 10 * time.Second}, defaultBaseURL, cfg)
}

func test(ctx context.Context, hc *http.Client, base string, cfg config.Trakt) error {
	id := strings.TrimSpace(cfg.ClientID)
	if id == "" {
		return errors.New("a Trakt client ID is required")
	}
	if strings.TrimSpace(cfg.ClientSecret) == "" {
		return errors.New("a Trakt client secret is required")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(base, "/")+"/movies/trending?limit=1", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("trakt-api-version", "2")
	req.Header.Set("trakt-api-key", id)
	req.Header.Set("User-Agent", "Ether")
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("trakt: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	switch {
	case resp.StatusCode == http.StatusOK:
		return nil
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return errors.New("Trakt rejected the client ID")
	default:
		return fmt.Errorf("trakt: %s", resp.Status)
	}
}
