package plextv

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/braedonsaunders/ether/internal/core"
)

func TestFeaturesResponse(t *testing.T) {
	for _, body := range []string{`null`, `{}`, `[{"uuid":3}]`, `not json`, `[]`} {
		t.Run(body, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/api/v2/features" || r.Header.Get("X-Plex-Token") != "server-token" {
					t.Error("incorrect features request")
				}
				_, _ = w.Write([]byte(body))
			}))
			defer srv.Close()
			c, err := New(Options{BaseURL: srv.URL})
			if err != nil {
				t.Fatal(err)
			}
			_, err = c.Features(context.Background(), "server-token")
			if (err == nil) != (body == `[]`) {
				t.Fatalf("features response: %v", err)
			}
			if _, err := c.Features(context.Background(), ""); !errors.Is(err, core.ErrUnauthorized) {
				t.Fatalf("missing token: %v", err)
			}
		})
	}
}

func TestOwnerFeaturesPreservesNamesAndDeduplicates(t *testing.T) {
	const id = "82999dd3-a2be-482e-9f44-357879b4f603"
	got := ownerFeatures([]Feature{{ID: "pass", UUID: id}, {ID: "pass", UUID: id}, {ID: "not-a-grant"}}, []string{"pass", "", "pass", id})
	if !slices.Equal(got, []string{id, "pass"}) {
		t.Fatalf("features: %v", got)
	}
}
