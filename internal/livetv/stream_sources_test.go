package livetv

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

func TestChannelSourcesFallbackCooldownRecoveryAndViewerIsolation(t *testing.T) {
	var primaryHits atomic.Int64
	var recovered atomic.Bool
	var clock atomic.Int64
	clock.Store(100)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/primary" {
			primaryHits.Add(1)
			if !recovered.Load() {
				http.Error(w, "unavailable", http.StatusForbidden)
				return
			}
		}
		fmt.Fprint(w, "#EXTM3U\n#EXT-X-TARGETDURATION:4\n#EXTINF:4,\nmedia.ts\n")
	}))
	defer up.Close()
	s := testService(t, Options{HTTPClient: up.Client(), Now: func() time.Time { return time.Unix(clock.Load(), 0) }})
	c, err := s.SaveChannel(context.Background(), Channel{ID: "news", Name: "News", Number: "7", Kind: "iptv", Enabled: true, StreamSources: []StreamSource{
		{ID: "backup", URL: up.URL + "/backup?private=backup-secret", Enabled: true, Priority: 1},
		{ID: "primary", URL: up.URL + "/primary?private=primary-secret", Enabled: true, Priority: 0},
	}})
	if err != nil {
		t.Fatal(err)
	}
	u := &core.User{ID: 7}
	tune := func(want string) SessionInfo {
		t.Helper()
		info, err := s.Tune(context.Background(), u, c.ID, core.ClientCaps{})
		if err != nil || info.ActiveSourceID != want {
			t.Fatalf("active source = %q, want %q; err=%v", info.ActiveSourceID, want, err)
		}
		b, _ := json.Marshal(info)
		if strings.Contains(string(b), "secret") || len(info.Channel.StreamSources) != 0 || info.Channel.StreamURL != "" {
			t.Fatal("tune exposed upstream credentials")
		}
		return info
	}
	first := tune("backup")
	if primaryHits.Load() != 1 {
		t.Fatal("primary was not tried exactly once")
	}
	ss, err := s.getSession(context.Background(), first.ID, u)
	if err != nil || ss.resources["index.m3u8"] != c.StreamSources[1].URL {
		t.Fatal("session did not pin the selected backup")
	}
	if _, err := s.Session(context.Background(), first.ID, &core.User{ID: 8}); !errors.Is(err, core.ErrUnauthorized) {
		t.Fatal("another viewer could inspect the session")
	}
	allowed, _ := s.AllowedChannels(context.Background(), u)
	b, _ := json.Marshal(allowed)
	if strings.Contains(string(b), "secret") {
		t.Fatal("channel listing exposed source URLs")
	}
	status, _ := s.StreamSourceStatuses(context.Background(), c.ID)
	if status[0].State != "cooling_down" || status[0].Failures != 1 || status[1].Failures != 0 {
		t.Fatalf("source health was not independent: %+v", status)
	}
	s.Stop(context.Background(), first.ID, u)
	second := tune("backup")
	if primaryHits.Load() != 1 {
		t.Fatal("a cooling-down primary delayed the second tune")
	}
	s.Stop(context.Background(), second.ID, u)
	recovered.Store(true)
	clock.Store(116)
	third := tune("primary")
	if primaryHits.Load() != 2 {
		t.Fatal("primary did not recover after its short cooldown")
	}
	s.Stop(context.Background(), third.ID, u)
	if len(s.ViewingSessions()) != 0 {
		t.Fatal("source attempts leaked viewing reservations")
	}
}

func TestStalledPrimaryLeavesBudgetForBackupAndUsesSelectedRelayInput(t *testing.T) {
	client := &http.Client{Transport: lifecycleTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "primary.invalid" {
			deadline, ok := r.Context().Deadline()
			if !ok || time.Until(deadline) > 5*time.Second+100*time.Millisecond {
				t.Error("primary was allowed to consume the entire tune deadline")
			}
			return nil, context.DeadlineExceeded
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("transport stream fixture")), Request: r}, nil
	})}
	tc := &fakeTranscoder{}
	s := testService(t, Options{HTTPClient: client, Transcoder: tc})
	c, err := s.SaveChannel(context.Background(), Channel{ID: "fixture", Name: "Fixture", Number: "1", Kind: "iptv", Enabled: true, StreamSources: []StreamSource{
		{ID: "primary", URL: "https://primary.invalid/live", Enabled: true},
		{ID: "backup", URL: "https://backup.invalid/live", Enabled: true, Priority: 1},
	}})
	if err != nil {
		t.Fatal(err)
	}
	u := &core.User{ID: 1}
	info, err := s.Tune(context.Background(), u, c.ID, core.ClientCaps{})
	if err != nil || info.ActiveSourceID != "backup" || len(tc.requests) != 1 || tc.requests[0].InputURL != "https://backup.invalid/live" {
		t.Fatalf("backup relay input not selected: active=%q err=%v requests=%+v", info.ActiveSourceID, err, tc.requests)
	}
	s.Stop(context.Background(), info.ID, u)
	if !tc.hls[0].closed || len(s.ViewingSessions()) != 0 {
		t.Fatal("selected relay survived session stop")
	}
}

func TestPlaylistRefreshPreservesBackupOrderAndChannelIdentity(t *testing.T) {
	ctx := context.Background()
	s := testService(t, Options{})
	src := Source{ID: "playlist", Name: "Playlist", Playlist: "#EXTM3U\n#EXTINF:-1 tvg-id=\"news\",News\nhttps://primary.invalid/old\n"}
	if _, err := s.ImportSource(ctx, src); err != nil {
		t.Fatal(err)
	}
	channels, _ := s.Channels(ctx)
	c := channels[0]
	importID := c.StreamSources[0].ID
	c.StreamSources[0].Priority = 1
	c.StreamSources = append(c.StreamSources, StreamSource{ID: "owner-backup", URL: "https://backup.invalid/live", Name: "Preferred backup", Enabled: true})
	c.Number, c.Favorite, c.GuideID = "99", true, "owner-guide"
	c.AllowedUsers = []int64{9}
	if _, err := s.SaveChannel(ctx, c); err != nil {
		t.Fatal(err)
	}
	src.Playlist = strings.ReplaceAll(src.Playlist, "/old", "/new")
	if _, err := s.ImportSource(ctx, src); err != nil {
		t.Fatal(err)
	}
	// A new service instance reads the same persisted channel contract.
	reopened := testService(t, Options{DB: s.o.DB})
	saved, err := reopened.Channel(ctx, c.ID)
	if err != nil || saved.Number != "99" || !saved.Favorite || saved.GuideID != "owner-guide" || len(saved.AllowedUsers) != 1 {
		t.Fatalf("refresh changed channel identity/policy: %+v err=%v", saved, err)
	}
	if len(saved.StreamSources) != 2 || saved.StreamSources[0].ID != "owner-backup" || saved.StreamSources[1].ID != importID || saved.StreamSources[1].URL != "https://primary.invalid/new" || saved.StreamURL != "https://backup.invalid/live" {
		t.Fatalf("refresh lost source ownership/order: %+v", saved.StreamSources)
	}
	// Deleting the import-owned input is explicit; refreshing must not add it back.
	saved.StreamSources = saved.StreamSources[:1]
	if _, err := s.SaveChannel(ctx, saved); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ImportSource(ctx, src); err != nil {
		t.Fatal(err)
	}
	saved, _ = s.Channel(ctx, c.ID)
	if len(saved.StreamSources) != 1 || saved.StreamSources[0].ID != "owner-backup" {
		t.Fatal("refresh recreated a removed input")
	}
}

func TestStreamSourcesRejectUnsafeAndAmbiguousInputs(t *testing.T) {
	for _, inputs := range [][]StreamSource{
		{{URL: "file:///private/secret", Enabled: true}},
		{{ID: "same", URL: "https://one.invalid/live", Enabled: true}, {ID: "same", URL: "https://two.invalid/live", Enabled: true}},
		{{URL: "https://one.invalid/live", Enabled: true}, {URL: "https://one.invalid/live", Enabled: true}},
		{{URL: "https://one.invalid/live", Enabled: false}},
	} {
		c := Channel{ID: "fixture", Name: "Fixture", Number: "1", Kind: "iptv", Enabled: true, StreamSources: inputs}
		if validateChannel(&c) == nil {
			t.Fatal("invalid source list accepted")
		}
	}
}

func TestFailedMediaCoolsOnlyItsPinnedSource(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/gone.ts" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, "#EXTM3U\n#EXTINF:4,\ngone.ts\n")
	}))
	defer up.Close()
	s := testService(t, Options{HTTPClient: up.Client()})
	c, err := s.SaveChannel(context.Background(), Channel{ID: "news", Name: "News", Number: "1", Kind: "iptv", Enabled: true, StreamSources: []StreamSource{
		{ID: "primary", URL: up.URL + "/primary", Enabled: true},
		{ID: "backup", URL: up.URL + "/backup", Enabled: true, Priority: 1},
	}})
	if err != nil {
		t.Fatal(err)
	}
	u := &core.User{ID: 1}
	first, err := s.Tune(context.Background(), u, c.ID, core.ClientCaps{})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/", nil)
	playlist := httptest.NewRecorder()
	s.ServePlaylist(playlist, r, first.ID, u)
	media := httptest.NewRecorder()
	s.ServeResource(media, r, first.ID, stableID(up.URL+"/gone.ts"), u)
	if media.Code != 404 {
		t.Fatalf("missing media status=%d", media.Code)
	}
	status, _ := s.StreamSourceStatuses(context.Background(), c.ID)
	if status[0].State != "cooling_down" || status[1].State != "unchecked" {
		t.Fatalf("failed media changed the wrong input's health: %+v", status)
	}
	s.Stop(context.Background(), first.ID, u)
	second, err := s.Tune(context.Background(), u, c.ID, core.ClientCaps{})
	if err != nil || second.ActiveSourceID != "backup" {
		t.Fatalf("retune did not use backup: %+v err=%v", second, err)
	}
	s.Stop(context.Background(), second.ID, u)
}
