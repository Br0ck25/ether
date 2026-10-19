package tmdb

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestEpisodeGroupEndpoints(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/tv/10/episode_groups":
			w.Write([]byte(`{"results":[{"id":"dvd","type":3,"name":"DVD","episode_count":1,"group_count":1}]}`))
		case "/tv/episode_group/dvd":
			w.Write([]byte(`{"id":"dvd","groups":[{"id":"disc","order":0,"name":"Disc","episodes":[{"id":8,"season_number":0,"episode_number":1,"order":0}]}]}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	c := New("test", nil, WithBaseURL(srv.URL))
	groups, e := c.EpisodeGroups(context.Background(), 10)
	if e != nil || len(groups) != 1 || groups[0].Type != 3 {
		t.Fatal(groups, e)
	}
	group, e := c.EpisodeGroup(context.Background(), "dvd")
	if e != nil || len(group.Groups) != 1 || group.Groups[0].Episodes[0].SeasonNumber != 0 {
		t.Fatal(group, e)
	}
}
