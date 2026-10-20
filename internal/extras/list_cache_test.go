package extras

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/tmdb"
)

type flakyVideos struct {
	fakeVideos
	fail bool
}

func (f *flakyVideos) MovieVideos(context.Context, int64, string) ([]tmdb.Video, error) {
	if f.fail {
		return nil, errors.New("tmdb: HTTP 503")
	}
	return f.vs, nil
}

// A transient TMDB failure must not be cached as "no extras" for the TTL.
func TestListDoesNotCacheFetchFailure(t *testing.T) {
	v := &flakyVideos{fakeVideos: fakeVideos{[]tmdb.Video{{Site: "YouTube", Key: "trlOffEN001", Type: "Trailer", Official: true}}}, fail: true}
	s := New(Options{Videos: v, Config: enabledCfg("yt-dlp"), LookPath: lookOK, Now: func() time.Time { return testNow }})
	if got, _ := s.List(context.Background(), movie(), "en"); len(got) != 0 {
		t.Fatalf("failed fetch returned extras: %+v", got)
	}
	v.fail = false
	if got, _ := s.List(context.Background(), movie(), "en"); len(got) != 1 {
		t.Fatalf("recovered fetch still served cached failure: %+v", got)
	}
}
