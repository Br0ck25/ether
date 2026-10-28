package subtitles

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
)

func TestCachedSidecarsListWithoutNetwork(t *testing.T) {
	ctx := context.Background()
	f := newOSFake(t)
	svc := newSvc(t, f, config.OpenSubtitles{}, t.TempDir())
	user := &core.User{SubtitleLanguages: []string{"de", "fr"}}

	if got := svc.CachedSidecars(episode, user, nil); got != nil {
		t.Fatalf("empty cache: %+v", got)
	}
	if _, err := svc.SidecarsForRelease(ctx, episode, user, nil, "Show.S01E02.1080p.WEB"); err != nil {
		t.Fatal(err)
	}
	searches, downloads := f.searches.Load(), f.downloads.Load()

	// Before play: the estimate has no subtitle tracks, so every cached
	// preferred language is listed, whatever release it was matched to.
	estimate := []core.StreamInfo{{Index: 0, Type: "video"}, {Index: 1, Type: "audio", Language: "en"}}
	got := svc.CachedSidecars(episode, user, estimate)
	if len(got) != 2 || got[0].Language != "de" || got[1].Language != "fr" || !got[0].External ||
		!strings.HasPrefix(got[0].ExternalURL, URLPrefix) || got[0].Index != 2 || got[1].Index != 3 {
		t.Fatalf("cached: %+v", got)
	}
	// A session's enriched info already lists them: nothing to add.
	if more := svc.CachedSidecars(episode, user, append(estimate, got...)); len(more) != 0 {
		t.Fatalf("duplicates: %+v", more)
	}
	// An embedded German track covers German.
	withDE := append(estimate, core.StreamInfo{Index: 2, Type: "subtitle", Language: "ger"})
	if only := svc.CachedSidecars(episode, user, withDE); len(only) != 1 || only[0].Language != "fr" {
		t.Fatalf("embedded language: %+v", only)
	}
	if f.searches.Load() != searches || f.downloads.Load() != downloads {
		t.Fatal("CachedSidecars touched the network")
	}
	svc.UpdateConfig(config.OpenSubtitles{Enabled: false})
	if got := svc.CachedSidecars(episode, user, nil); got != nil {
		t.Fatalf("disabled: %+v", got)
	}
}

func TestOnDemandSearchPickAndReuse(t *testing.T) {
	ctx := context.Background()
	f := newOSFake(t)
	svc := newSvc(t, f, config.OpenSubtitles{}, t.TempDir())

	// Only files a search offered for this title can be picked.
	if _, err := svc.DownloadOnDemand(ctx, episode, 12); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("unoffered pick: %v", err)
	}
	cands, err := svc.SearchOnDemand(ctx, episode, "ger", "")
	if err != nil || len(cands) != 2 || f.lastQuery["languages"] != "de" {
		t.Fatalf("search: %+v %v %v", cands, err, f.lastQuery)
	}
	other := &core.Title{Key: core.MovieKey(7), Type: core.TypeMovie}
	if _, err := svc.DownloadOnDemand(ctx, other, 12); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("pick for another title: %v", err)
	}
	st, err := svc.DownloadOnDemand(ctx, episode, 12) // the lower-ranked file
	if err != nil || st.Language != "de" || !st.External || st.Type != "subtitle" || !strings.Contains(st.Title, "Other") {
		t.Fatalf("pick: %+v %v", st, err)
	}

	// The pick is listed even for a viewer who does not prefer German, and
	// even when the release embeds German.
	embeddedDE := []core.StreamInfo{{Index: 0, Type: "video"}, {Index: 1, Type: "subtitle", Language: "de"}}
	if got := svc.CachedSidecars(episode, &core.User{}, embeddedDE); len(got) != 1 || got[0].ExternalURL != st.ExternalURL {
		t.Fatalf("cached pick: %+v", got)
	}
	// Playback enrichment uses it instead of searching for German.
	searches := f.searches.Load()
	out, err := svc.SidecarsForRelease(ctx, episode, &core.User{SubtitleLanguages: []string{"de"}}, nil, "Show.S01E02.1080p.WEB")
	if err != nil || len(out) != 1 || out[0].ExternalURL != st.ExternalURL || f.searches.Load() != searches {
		t.Fatalf("enrich with pick: %+v %v (searches %d->%d)", out, err, searches, f.searches.Load())
	}
	out, err = svc.SidecarsForRelease(ctx, episode, &core.User{}, embeddedDE, "Show.S01E02.1080p.WEB")
	if err != nil || len(out) != 1 || out[0].ExternalURL != st.ExternalURL || out[0].Index != 2 {
		t.Fatalf("enrich pick alongside embedded: %+v %v", out, err)
	}

	// Picking another file for the language replaces the pick.
	st2, err := svc.DownloadOnDemand(ctx, episode, 11)
	if err != nil {
		t.Fatal(err)
	}
	if got := svc.CachedSidecars(episode, nil, nil); len(got) != 1 || got[0].ExternalURL != st2.ExternalURL {
		t.Fatalf("replaced pick: %+v", got)
	}
}

func TestOpenFormatAtShiftsToSessionTimeline(t *testing.T) {
	ctx := context.Background()
	f := newOSFake(t)
	f.body = func(int64) []byte {
		return []byte("1\n00:00:10,000 --> 00:00:12,000\nEarly\n\n2\n00:01:40,500 --> 00:01:42,000\nLate\n\n")
	}
	svc := newSvc(t, f, config.OpenSubtitles{}, t.TempDir())
	out, err := svc.SidecarsForRelease(ctx, episode, &core.User{SubtitleLanguages: []string{"de"}}, nil, "")
	if err != nil || len(out) != 1 {
		t.Fatalf("%+v %v", out, err)
	}
	read := func(format string, offset time.Duration) (string, string) {
		rc, actual, err := svc.OpenFormatAt(out[0].ExternalURL, format, offset)
		if err != nil {
			t.Fatal(err)
		}
		defer rc.Close()
		b, _ := io.ReadAll(rc)
		return string(b), actual
	}
	if body, actual := read("srt", time.Minute); actual != "srt" || body != "1\n00:00:40,500 --> 00:00:42,000\nLate\n\n" {
		t.Fatalf("srt shift: %s %q", actual, body)
	}
	if body, actual := read("vtt", time.Minute); actual != "vtt" || !strings.HasPrefix(body, "WEBVTT\n\n00:00:40.500 --> 00:00:42.000\nLate") {
		t.Fatalf("vtt shift: %s %q", actual, body)
	}
	if body, _ := read("srt", 0); !strings.Contains(body, "Early") {
		t.Fatalf("no offset: %q", body)
	}
}

func TestPrefetchSidecarsIsBudgeted(t *testing.T) {
	f := newOSFake(t)
	svc := newSvc(t, f, config.OpenSubtitles{}, t.TempDir())
	user := &core.User{SubtitleLanguages: []string{"de"}}
	if svc.PrefetchSidecars(episode, &core.User{}) {
		t.Fatal("prefetch without preferred languages")
	}
	if !svc.PrefetchSidecars(episode, user) {
		t.Fatal("prefetch not started")
	}
	svc.prefetchWG.Wait()
	if got := svc.CachedSidecars(episode, user, nil); len(got) != 1 || got[0].Language != "de" {
		t.Fatalf("prefetched: %+v", got)
	}
	// Cached: nothing to do.
	if svc.PrefetchSidecars(episode, user) {
		t.Fatal("prefetch of a cached title")
	}
	// Hourly budget.
	for i := 0; i < prefetchPerHour; i++ {
		svc.prefetchRuns = append(svc.prefetchRuns, svc.now())
	}
	movie := &core.Title{Key: core.MovieKey(99), Type: core.TypeMovie}
	if svc.PrefetchSidecars(movie, user) {
		t.Fatal("prefetch over the hourly budget")
	}
	svc.prefetchRuns = nil
	// Quota exhausted.
	svc.mu.Lock()
	svc.quotaTill = svc.now().Add(time.Hour)
	svc.mu.Unlock()
	if svc.PrefetchSidecars(movie, user) {
		t.Fatal("prefetch with the download quota exhausted")
	}
}
