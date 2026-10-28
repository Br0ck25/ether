package plex

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

// hideFromDiscovery makes the availability-discovery filter drop key, as it
// does for a recent title recorded unavailable.
func hideFromDiscovery(f *fixture, key core.RatingKey) {
	f.srv.d.FilterAvailable = func(_ context.Context, ts []core.Title) []core.Title {
		var out []core.Title
		for _, v := range ts {
			if v.Key != key {
				out = append(out, v)
			}
		}
		return out
	}
}

// Discovery evidence hides a recent title from browse and search, but a
// viewer who presses Play still gets an attempt: the friendly decision, a
// play queue, and a part request that resolves (or fails with 503).
func TestDiscoveryHiddenTitleStaysPlayable(t *testing.T) {
	f := newFixture()
	defer f.srv.Close()
	hideFromDiscovery(f, fightClub.Key)
	f.playback.failures[fightClub.Key] = core.ErrNotAvailable

	rec := f.get(t, "/video/:/transcode/universal/decision?path=/library/metadata/1000000000000550&session=d1", jsonAccept...)
	if rec.Code != http.StatusOK {
		t.Fatalf("decision on a discovery-hidden title: %d %s", rec.Code, rec.Body.String())
	}
	mc := decodeJSON(t, rec)
	if mc["generalDecisionCode"].(float64) != codeGeneralError || mc["generalDecisionText"] != msgNotAvailable {
		t.Fatalf("decision: %v", mc)
	}

	uri := urlEsc("server://m/com.plexapp.plugins.library/library/metadata/" + fightClub.Key.String())
	if rec := f.request(t, http.MethodPost, "/playQueues?type=video&uri="+uri, map[string]string{"Accept": "application/json"}); rec.Code != http.StatusOK {
		t.Fatalf("play queue for a discovery-hidden title: %d", rec.Code)
	}

	if rec := f.get(t, "/library/parts/1000000000000550/1759276800/file.mkv"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("unavailable part of a discovery-hidden title: %d", rec.Code)
	}
	// Lists still hide it exactly as before (until the viewer has it open).
	for _, path := range []string{"/hubs/search?query=Fjord", "/library/sections/1/all"} {
		r := f.get(t, path)
		if r.Code != http.StatusOK || strings.Contains(r.Body.String(), `title="Fjord Harbor"`) {
			t.Fatalf("%s leaked the hidden title: %d", path, r.Code)
		}
	}
	delete(f.playback.failures, fightClub.Key)
	if rec := f.get(t, "/library/parts/1000000000000550/1759276800/file.mkv", "Range", "bytes=0-9"); rec.Code != http.StatusPartialContent {
		t.Fatalf("part of a discovery-hidden title that resolves: %d", rec.Code)
	}

}

// Content policy still blocks playback outright.
func TestPlayAttemptStillEnforcesPolicy(t *testing.T) {
	f := newFixture(func(d *Deps) { d.AccessPolicy = keywordPolicy{blocked: "blocked"} })
	defer f.srv.Close()
	blocked := fightClub
	blocked.Keywords = []string{"blocked"}
	f.catalog.titles[blocked.Key] = blocked
	if rec := f.get(t, "/video/:/transcode/universal/decision?path=/library/metadata/1000000000000550", "X-Plex-Token", friendToken); rec.Code != http.StatusNotFound {
		t.Fatalf("blocked decision: %d", rec.Code)
	}
	if rec := f.get(t, "/library/parts/1000000000000550/1759276800/file.mkv", "X-Plex-Token", friendToken); rec.Code != http.StatusNotFound {
		t.Fatalf("blocked part: %d", rec.Code)
	}
}

// The start request that follows a failed decision for the same session
// gets that failure at once; it is one-shot and expires.
func TestFailedDecisionAnswersFollowingStart(t *testing.T) {
	f := newFixture()
	defer f.srv.Close()
	// Background work (artwork) reads the clock too: advance it atomically.
	var clock atomic.Int64
	clock.Store(fixedNow.UnixNano())
	f.srv.d.Now = func() time.Time { return time.Unix(0, clock.Load()) }
	const decision = "/video/:/transcode/universal/decision?path=%2Flibrary%2Fmetadata%2F1000000000000550&session=s1"
	const start = "/video/:/transcode/universal/start.m3u8?path=%2Flibrary%2Fmetadata%2F1000000000000550&session=s1&protocol=hls"

	f.playback.failures[fightClub.Key] = core.ErrNotAvailable
	f.get(t, decision)
	// Were start to resolve again it would now succeed: the 503 proves it
	// answered from the remembered decision.
	delete(f.playback.failures, fightClub.Key)
	rec := f.get(t, start)
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("start after a failed decision: %d retry-after=%q", rec.Code, rec.Header().Get("Retry-After"))
	}
	if f.playback.prepared != 0 {
		t.Fatalf("start resolved again: prepared=%d", f.playback.prepared)
	}
	// One-shot: the next start resolves.
	if rec := f.get(t, start); rec.Code != http.StatusOK {
		t.Fatalf("second start: %d %s", rec.Code, rec.Body.String())
	}

	// Expired: a start long after the failed decision resolves afresh.
	f.playback.failures[fightClub.Key] = core.ErrNotAvailable
	f.get(t, strings.Replace(decision, "s1", "s2", 1))
	delete(f.playback.failures, fightClub.Key)
	clock.Add(int64(decisionFailureTTL + time.Second))
	if rec := f.get(t, strings.Replace(start, "s1", "s2", 1)); rec.Code != http.StatusOK {
		t.Fatalf("start after the remembered failure expired: %d", rec.Code)
	}
}

func TestRememberedFailureIsPerViewerAndTitle(t *testing.T) {
	tt := newTranscodeTable()
	now := fixedNow
	if !tt.claim("s", 1, "c", now) {
		t.Fatal("claim")
	}
	tt.fail("s", 2, "c", fightClub.Key, core.ErrNotAvailable, now) // another viewer: ignored
	if _, ok := tt.takeFailure("s", 1, fightClub.Key, now); ok {
		t.Fatal("another viewer's failure recorded on this binding")
	}
	tt.fail("s", 1, "c", fightClub.Key, core.ErrNotAvailable, now)
	if _, ok := tt.takeFailure("s", 1, alien.Key, now); ok {
		t.Fatal("failure answered a different title")
	}
	tt.fail("s", 1, "c", fightClub.Key, core.ErrNotAvailable, now)
	if err, ok := tt.takeFailure("s", 1, fightClub.Key, now); !ok || !errors.Is(err, core.ErrNotAvailable) {
		t.Fatalf("take: %v %v", err, ok)
	}
	// A successful decision replaces the binding and clears the failure.
	tt.fail("s", 1, "c", fightClub.Key, core.ErrNotAvailable, now)
	tt.put(&transcodeEntry{id: "s", userID: 1, clientID: "c", key: fightClub.Key, updatedAt: now})
	if _, ok := tt.takeFailure("s", 1, fightClub.Key, now); ok {
		t.Fatal("successful decision kept the failure")
	}
}

func TestPlaybackMessageNamesTheCause(t *testing.T) {
	notCached := func(name string) error { return fmt.Errorf("%q: %w", name, core.ErrNotCached) }
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"no indexers", fmt.Errorf("resolve: %w", core.ErrNoSources), msgNoSources},
		{"debrid without torrent indexer", core.ErrNoTorrentSources, msgNoTorrentSources},
		{"debrid key rejected through failover",
			fmt.Errorf("%w: 3 releases failed (last: real-debrid: bad_token): %w", core.ErrNotAvailable, core.ErrDebridAuth), msgDebridAuth},
		{"debrid key rejected in validation", errors.Join(fmt.Errorf("Real-Debrid: %w", core.ErrDebridAuth)), msgDebridAuth},
		{"every candidate uncached", errors.Join(errors.Join(notCached("a")), notCached("b")), msgNotCached},
		{"uncached after failover", fmt.Errorf("%w: 2 releases failed (last: x)", core.ErrNotCached), msgNotCached},
		{"uncached and a timeout", errors.Join(notCached("a"), errors.New("timeout")), msgNotAvailable},
		{"uncached among not-available", errors.Join(notCached("a"), fmt.Errorf("x: %w", core.ErrNotAvailable)), msgNotAvailable},
		{"plain not available", fmt.Errorf("%q: %w", "x", core.ErrNotAvailable), msgNotAvailable},
		{"stream limit", core.ErrStreamLimit, msgStreamLimit},
	}
	for _, c := range cases {
		if got := playbackMessage(c.err); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestDecisionExplainsMissingSources(t *testing.T) {
	f := newFixture()
	defer f.srv.Close()
	f.playback.failures[fightClub.Key] = fmt.Errorf("resolve: %w", core.ErrNoTorrentSources)
	mc := decodeJSON(t, f.get(t, "/video/:/transcode/universal/decision?path=/library/metadata/1000000000000550", jsonAccept...))
	if mc["generalDecisionText"] != msgNoTorrentSources || mc["transcodeDecisionText"] != msgNoTorrentSources {
		t.Fatalf("decision: %v", mc)
	}
}
