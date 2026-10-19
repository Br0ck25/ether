// Command ether-livebench measures time-to-playable against real services:
// it resolves a title over the owner's indexers and Usenet providers, opens
// it, reads the first bytes, seeks, and probes it. Credentials come from a
// JSON file (-ref) that is never committed:
//
//	{"servers":[{"name","host","port","tls","username","password"}],
//	 "indexers":[{"name","url","apiKey"}]}
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"time"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/newznab"
	"github.com/braedonsaunders/ether/internal/resolver"
	"github.com/braedonsaunders/ether/internal/segcache"
	"github.com/braedonsaunders/ether/internal/store"
	"github.com/braedonsaunders/ether/internal/stream"
	"github.com/braedonsaunders/ether/internal/transcode"
	"github.com/braedonsaunders/ether/internal/usenet"
)

type ref struct {
	Servers []struct {
		Name, Host, Username, Password string
		Port, Connections, Priority    int
		TLS                            bool
	}
	Indexers []struct{ Name, URL, APIKey string }
}

func main() {
	refPath := flag.String("ref", "", "usenet-ref.json")
	tmdbID := flag.Int64("tmdb", 603, "movie tmdb id")
	imdb := flag.String("imdb", "tt0133093", "imdb id")
	title := flag.String("title", "The Matrix", "title")
	year := flag.Int("year", 1999, "year")
	runtime := flag.Duration("runtime", 136*time.Minute, "runtime")
	product := flag.String("product", "Plex Web", "client product")
	indexer := flag.String("indexer", "api.nzbgeek.info", "only this indexer host")
	data := flag.String("data", "", "persistent benchmark data directory (keeps search/NZB caches)")
	trace := flag.Bool("trace", false, "print coarse phase timing breakdown")
	retry := flag.Bool("retry", false, "bypass a persisted unavailable result while reusing cached indexer results")
	freshSearch := flag.Bool("fresh-search", false, "bypass cached indexer results (counts against the configured indexer budget)")
	flag.Parse()
	logLevel := slog.LevelWarn
	if *trace {
		logLevel = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel}))
	var r ref
	b, _ := os.ReadFile(*refPath)
	json.Unmarshal(b, &r)

	dir := *data
	if dir == "" {
		var err error
		dir, err = os.MkdirTemp("", "ether-e2e")
		must(err)
		defer os.RemoveAll(dir)
	} else if err := os.MkdirAll(dir, 0o700); err != nil {
		must(err)
	}
	ctx := context.Background()
	cfg := config.Defaults(dir)
	for i, s := range r.Servers {
		connections := s.Connections
		if connections <= 0 {
			connections = 4
		}
		cfg.Usenet.Providers = append(cfg.Usenet.Providers, config.UsenetProvider{ID: fmt.Sprint(i), Name: s.Name, Host: s.Host, Port: s.Port, TLS: s.TLS, Username: s.Username, Password: s.Password, Connections: connections, Priority: i, Enabled: true})
	}
	for i, x := range r.Indexers {
		if filepath.Base(x.URL) != *indexer && x.Name != *indexer {
			continue
		}
		cfg.Indexers = append(cfg.Indexers, config.Indexer{ID: fmt.Sprint(i), Name: x.Name, URL: x.URL, APIKey: x.APIKey, DailyLimit: 20, DailyGrabs: 20, Enabled: true})
	}
	db, err := store.Open(ctx, dir)
	must(err)
	fetcher := usenet.New(cfg.Usenet.Providers, usenet.Options{Logger: log, TraceProtocol: *trace})
	defer fetcher.Close()
	cache, err := segcache.Open(filepath.Join(dir, "cache"), 2<<30, log)
	must(err)
	eng := stream.New(fetcher, cache, stream.OptionsFromConfig(cfg), log)
	newznabOptions := newznab.Options{Logger: log, NZBDir: filepath.Join(dir, "nzb")}
	if *freshSearch {
		newznabOptions.SearchCacheTTL = -1
	}
	idx := newznab.NewManager(db, cfg.Indexers, newznabOptions)
	res := resolver.New(idx, eng, db, cfg, resolver.Options{Logger: log})
	warmStart := time.Now()
	if err := fetcher.Warm(ctx, 4); err != nil {
		fmt.Printf("startup warm warning: %v\n", err)
	}
	warmDuration := time.Since(warmStart)

	t := &core.Title{Key: core.MovieKey(*tmdbID), Type: core.TypeMovie, IDs: core.IDs{TMDB: *tmdbID, IMDB: *imdb}, Title: *title, Year: *year, Released: time.Date(*year, 1, 1, 0, 0, 0, 0, time.UTC), Runtime: *runtime}
	caps := core.ClientCaps{Product: *product, Platform: "Chrome", MaxHeight: 1080, VideoCodecs: []string{"h264"}, AudioCodecs: []string{"aac", "ac3", "eac3"}, Containers: []string{"mp4", "mkv"}}
	if *product == "Plex for Apple TV" {
		caps = core.ClientCaps{Product: *product, Platform: "tvOS", MaxHeight: 2160, HDR10: true, DolbyVision: true, VideoCodecs: []string{"h264", "hevc"}, AudioCodecs: []string{"aac", "ac3", "eac3", "truehd"}, Containers: []string{"mp4", "mkv", "mpegts"}}
	}
	start := time.Now()
	req := core.ResolveRequest{Title: t, Client: caps, User: &core.User{ID: 1, IsOwner: true}}
	if *retry {
		req.Exclude = []string{"livebench-retry-sentinel"}
	}
	sol, err := res.Resolve(ctx, req)
	tResolve := time.Since(start)
	if *trace {
		ht := eng.HealthTimings()
		fmt.Printf("trace startup_warm=%s resolve_total=%s health_warm=%s nzb_parse=%s archive_plan_and_headers=%s stat=%s samples=%d\n", warmDuration.Round(time.Millisecond), tResolve.Round(time.Millisecond), ht.Warm.Round(time.Millisecond), ht.Parse.Round(time.Millisecond), ht.Plan.Round(time.Millisecond), ht.STAT.Round(time.Millisecond), ht.Samples)
		for _, st := range idx.SearchTimings() {
			fmt.Printf("  indexer=%s search=%s err=%s\n", st.Indexer, st.Duration.Round(time.Millisecond), st.Err)
		}
	}
	for _, tr := range res.Traces() {
		if *trace {
			fmt.Printf("trace search=%s winner=%s\n", tr.SearchDuration.Round(time.Millisecond), tr.Winner)
		}
		n := 0
		for _, c := range tr.Candidates {
			if !c.Rejected || c.HealthChecked {
				if *trace {
					fmt.Printf("  cand fetch=%s health=%s score=%.1f rej=%v hc=%v healthy=%v %s %s\n", c.FetchDuration.Round(time.Millisecond), c.HealthDuration.Round(time.Millisecond), c.Score, c.Rejected, c.HealthChecked, c.Healthy, c.Name, c.Reason)
				} else {
					fmt.Printf("  cand score=%.1f rej=%v hc=%v healthy=%v %s %s\n", c.Score, c.Rejected, c.HealthChecked, c.Healthy, c.Name, c.Reason)
				}
				if n++; n > 30 {
					break
				}
			}
		}
	}
	if *trace && err != nil {
		for _, s := range fetcher.Stats() {
			fmt.Printf("provider=%s dials=%d tcp=%s tls=%s greeting=%s auth=%s stat_batches=%d stat_batch_time=%s misses=%d avg_rtt=%s\n", s.Name, s.Dials, s.TCPDialTime.Round(time.Millisecond), s.TLSHandshakeTime.Round(time.Millisecond), s.GreetingTime.Round(time.Millisecond), s.AuthTime.Round(time.Millisecond), s.STATBatches, s.STATBatchTime.Round(time.Millisecond), s.Misses, s.AvgLatency.Round(time.Millisecond))
		}
	}
	must(err)
	fmt.Printf("resolved in %s: %s (%.1f GB) alternates=%d\n", tResolve.Round(time.Millisecond), sol.Release.Name, float64(sol.Release.Size)/1e9, len(sol.Alternates))
	t0 := time.Now()
	src, err := eng.Open(ctx, sol.Release.ID, sol.NZB)
	must(err)
	tOpen := time.Since(t0)
	buf := make([]byte, 4<<20)
	t1 := time.Now()
	n, err := src.ReadAt(buf, 0)
	tFirst := time.Since(t1)
	fmt.Printf("open %s; file %s (%.2f GB); first %d bytes in %s err=%v\n", tOpen.Round(time.Millisecond), src.Name(), float64(src.Size())/1e9, n, tFirst.Round(time.Millisecond), err)
	t2 := time.Now()
	_, err = src.ReadAt(buf, src.Size()/2)
	fmt.Printf("seek to middle: 4 MiB in %s err=%v\n", time.Since(t2).Round(time.Millisecond), err)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, src.Name(), time.Time{}, io.NewSectionReader(src, 0, src.Size()))
	}))
	defer srv.Close()
	tc, err := transcode.New(cfg.Transcode, log, transcode.WithoutHWDetection())
	must(err)
	t3 := time.Now()
	info, err := tc.Probe(ctx, srv.URL+"/"+filepath.Base(src.Name()))
	must(err)
	fmt.Printf("probe %s: container=%s duration=%s streams=%d\n", time.Since(t3).Round(time.Millisecond), info.Container, info.Duration.Round(time.Second), len(info.Streams))
	for _, s := range info.Streams {
		fmt.Printf("   %s %s %dx%d %s ch=%d lang=%s\n", s.Type, s.Codec, s.Width, s.Height, s.ColorTransfer, s.Channels, s.Language)
	}
	d := tc.Decide(*info, caps, core.DecideOptions{DirectPlay: true, DirectStream: true, AudioStream: -1, SubtitleStream: -1})
	fmt.Printf("decision for %s: %s (%s)\n", caps.Product, d.Kind, d.Reason)
	fmt.Printf("TIME TO PLAYABLE (resolve+open+first read+probe): %s\n", (tResolve + tOpen + tFirst + time.Since(t3)).Round(time.Millisecond))
	for _, s := range fetcher.Stats() {
		if *trace {
			fmt.Printf("provider=%s dials=%d tcp=%s tls=%s greeting=%s auth=%s stat_batches=%d stat_batch_time=%s articles=%d misses=%d avg_rtt=%s\n", s.Name, s.Dials, s.TCPDialTime.Round(time.Millisecond), s.TLSHandshakeTime.Round(time.Millisecond), s.GreetingTime.Round(time.Millisecond), s.AuthTime.Round(time.Millisecond), s.STATBatches, s.STATBatchTime.Round(time.Millisecond), s.Articles, s.Misses, s.AvgLatency.Round(time.Millisecond))
		} else {
			fmt.Printf("provider %+v\n", s)
		}
	}
}

func must(err error) {
	if err != nil {
		fmt.Println("FATAL:", err)
		os.Exit(1)
	}
}
