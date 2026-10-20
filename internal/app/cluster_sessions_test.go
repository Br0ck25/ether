package app

import (
	"bufio"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/cluster"
	"github.com/braedonsaunders/ether/internal/clusterconfig"
	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/plextv"
)

// twoClusterApps boots two nodes of one cluster (the second joined with a
// token) on a throwaway PostgreSQL schema and starts their cluster runtimes,
// so events travel over the real LISTEN/NOTIFY bus.
func twoClusterApps(t *testing.T) (*App, *App) {
	t.Helper()
	ctx := context.Background()
	pgURL := clusterSchemaURL(t)
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	t.Setenv(config.DatabaseURLEnv, "")
	dirA := t.TempDir()
	cfgA, err := config.Open(dirA)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfgA.Update(func(c *config.Config) error {
		c.Transcode.FFmpegPath = "/not-installed/ffmpeg"
		c.Plex.RemoteAccess = false
		c.SetupComplete = true
		c.Database = config.Database{Driver: config.DatabasePostgres, URL: pgURL}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	a, err := New(ctx, Options{DataDir: dirA, Logger: quiet})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	tok, _, err := clusterconfig.NewToken(ctx, a.db, a.cfg, pgURL, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	dirB := t.TempDir()
	if _, err := clusterconfig.Join(ctx, clusterconfig.JoinOptions{DataDir: dirB, Token: tok.String()}); err != nil {
		t.Fatal(err)
	}
	cfgB, _ := config.Open(dirB)
	if err := cfgB.Update(func(c *config.Config) error { c.Transcode.FFmpegPath = "/not-installed/ffmpeg"; return nil }); err != nil {
		t.Fatal(err)
	}
	b, err := New(ctx, Options{DataDir: dirB, Logger: quiet})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close() })
	for _, n := range []*App{a, b} {
		if err := n.cluster.Start(ctx); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(15 * time.Second)
	for !a.cluster.(*cluster.Postgres).Health().BusConnected || !b.cluster.(*cluster.Postgres).Health().BusConnected {
		if time.Now().After(deadline) {
			t.Fatal("cluster bus never connected")
		}
		time.Sleep(20 * time.Millisecond)
	}
	return a, b
}

func until(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out: %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Over the real PostgreSQL bus: users and tokens written on one node reach
// the other's auth cache (grant and revoke), and a Plex notification
// published on one node reaches an event-stream client connected to the
// other.
func TestClusterNodesShareAuthAndNotificationsPostgres(t *testing.T) {
	a, b := twoClusterApps(t)
	ctx := context.Background()
	if err := a.users.SetOwner(ctx, plextv.Account{ID: 4242, Username: "owner"}, "owner-tok"); err != nil {
		t.Fatal(err)
	}
	// Poll B's caches only (an unknown token would be checked against
	// plex.tv, and tests stay offline); then the token resolves locally.
	until(t, "B learns the owner", func() bool { o, err := b.users.Owner(ctx); return err == nil && o.PlexID == 4242 })
	if _, err := b.users.Authenticate(ctx, "owner-tok"); err != nil {
		t.Fatalf("owner token on B: %v", err)
	}
	if err := a.users.SyncFriends(ctx, []plextv.Friend{{PlexID: 77, Username: "bob", Token: "bob-tok", AllLibs: true}}); err != nil {
		t.Fatal(err)
	}
	var bobID int64
	until(t, "B learns bob", func() bool {
		us, _ := b.users.Users(ctx)
		for _, u := range us {
			if u.PlexID == 77 {
				bobID = u.ID
				return true
			}
		}
		return false
	})
	if u, err := b.users.Authenticate(ctx, "bob-tok"); err != nil || u.ID != bobID {
		t.Fatalf("bob on B: %v %v", u, err)
	}
	if err := a.users.SyncFriends(ctx, nil); err != nil {
		t.Fatal(err)
	}
	until(t, "B revokes bob", func() bool {
		_, err := b.users.ActiveUser(ctx, bobID)
		return errors.Is(err, core.ErrUnauthorized)
	})

	// A notification stream on node B...
	srvB := httptest.NewServer(b.handler)
	defer srvB.Close()
	sctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(sctx, http.MethodGet, srvB.URL+"/:/eventsource/notifications", nil)
	req.Header.Set("X-Plex-Token", "owner-tok")
	req.Header.Set("X-Plex-Client-Identifier", "web-b")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("event stream on B: %d", resp.StatusCode)
	}
	got := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			if strings.HasPrefix(sc.Text(), "event: timeline") {
				got <- sc.Text()
				return
			}
		}
	}()
	time.Sleep(200 * time.Millisecond) // let the subscription register
	// ...receives a watched-state change made through node A.
	r := httptest.NewRequest(http.MethodGet, "/:/scrobble?key="+core.MovieKey(603).String()+"&identifier=com.plexapp.plugins.library", nil)
	r.RemoteAddr = "192.168.1.20:5000"
	r.Header.Set("X-Plex-Token", "owner-tok")
	r.Header.Set("X-Plex-Client-Identifier", "web-a")
	w := httptest.NewRecorder()
	a.handler.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("scrobble on A: %d %s", w.Code, w.Body.String())
	}
	select {
	case <-got:
	case <-time.After(10 * time.Second):
		t.Fatal("notification published on A never reached B's client")
	}
}

// The owner's now-playing view on one node includes the other node's
// streams, and a stream limit holds across both.
func TestClusterOwnerStatusAndLimitsSpanNodesPostgres(t *testing.T) {
	a, b := twoClusterApps(t)
	if err := a.cfg.Update(func(c *config.Config) error { c.Server.MaxStreams = 1; return nil }); err != nil {
		t.Fatal(err)
	}
	until(t, "B applies the shared limit", func() bool {
		_ = b.cfg.RefreshShared(context.Background())
		return b.cfg.Get().Server.MaxStreams == 1
	})
	viewer := &core.User{ID: 9, Username: "viewer"}
	group := "0123456789abcdef0123456789abcdef"
	if err := b.playback.BeginChannel(viewer, core.ClientCaps{ClientID: "tv"}, group); err != nil {
		t.Fatal(err)
	}
	var mine []*core.PlaySession
	until(t, "A lists B's stream", func() bool {
		mine = (ownerPlayback{Playback: a.playback, app: a}).Sessions()
		return len(mine) == 1
	})
	if mine[0].ID != group || mine[0].Node != b.clusterNodeID() || mine[0].UserID != 9 {
		t.Fatalf("remote stream %+v", mine[0])
	}
	if err := a.playback.BeginChannel(&core.User{ID: 10}, core.ClientCaps{ClientID: "x"}, "fedcba9876543210fedcba9876543210"); !errors.Is(err, core.ErrStreamLimit) {
		t.Fatalf("second stream on A admitted past the cluster limit: %v", err)
	}
	b.playback.EndChannel(group)
	until(t, "slot frees cluster-wide", func() bool {
		return a.playback.BeginChannel(&core.User{ID: 10}, core.ClientCaps{ClientID: "x"}, "fedcba9876543210fedcba9876543210") == nil
	})
}
