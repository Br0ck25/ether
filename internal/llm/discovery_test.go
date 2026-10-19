package llm

import (
	"context"
	"encoding/json"
	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type recapCatalog struct {
	core.Catalog
	title    core.Title
	episodes []core.Title
}

func (c recapCatalog) Get(context.Context, core.RatingKey) (*core.Title, error) {
	t := c.title
	return &t, nil
}
func (c recapCatalog) Grandchildren(context.Context, core.RatingKey) ([]core.Title, error) {
	return c.episodes, nil
}

type recapState struct {
	core.State
	statuses map[core.RatingKey]core.WatchStatus
}

func (s recapState) GetMany(context.Context, int64, []core.RatingKey) (map[core.RatingKey]core.WatchStatus, error) {
	return s.statuses, nil
}
func TestRecapNeverSendsUnseenEpisodeOverview(t *testing.T) {
	seen := core.Title{Key: core.EpisodeKey(42, 1, 1), Type: core.TypeEpisode, Season: 1, Episode: 1, Summary: "Alice found the map.", ContentRating: "PG"}
	future := seen
	future.Key = core.EpisodeKey(42, 1, 2)
	future.Episode = 2
	future.Summary = "UNSEEN-SPOILER villain identity"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req chatRequest
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			t.Error("invalid model request")
		}
		var prompts string
		for _, m := range req.Messages {
			prompts += m.Content
		}
		if strings.Contains(prompts, "UNSEEN-SPOILER") || !strings.Contains(prompts, "Alice found the map") {
			t.Error("recap boundary violated")
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"Alice discovered a map."}}]}`))
	}))
	defer srv.Close()
	client := NewClient(ClientOptions{Config: config.LLM{Enabled: true, BaseURL: srv.URL, Model: "test"}})
	catalog := recapCatalog{title: core.Title{Key: core.SeasonKey(42, 1), Type: core.TypeSeason, ShowKey: core.ShowKey(42), Season: 1}, episodes: []core.Title{seen, future}}
	s := New(ServiceOptions{Client: client, Catalog: catalog, CallTimeout: time.Second})
	st := recapState{statuses: map[core.RatingKey]core.WatchStatus{seen.Key: {Key: seen.Key, ViewCount: 1}}}
	recap, e := s.SeasonRecap(context.Background(), &core.User{ID: 1, MaxContentRating: "PG"}, catalog.title.Key, st)
	if e != nil || recap != "Alice discovered a map." {
		t.Fatalf("recap %q %v", recap, e)
	}
}
func TestNaturalSearchFailureFallsBackToCatalogue(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer srv.Close()
	title := core.Title{Key: core.MovieKey(1), Title: "Heist"}
	cat := fakeCatalog{byTitle: map[string][]core.Title{"90s heists": {title}}}
	s := New(ServiceOptions{Client: NewClient(ClientOptions{Config: config.LLM{Enabled: true, BaseURL: srv.URL, Model: "test"}}), Catalog: cat, CallTimeout: time.Second})
	titles, e := s.NaturalSearch(context.Background(), &core.User{ID: 1}, "90s heists", 10)
	if e != nil || len(titles) != 1 || titles[0].Key != title.Key {
		t.Fatalf("missing ordinary fallback %v %v", titles, e)
	}
}
