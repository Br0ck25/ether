// ether-repairbench performs explicitly invoked live experiments; ordinary Go
// tests remain offline. Credentials are read from the owner's existing config.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/pprof"
	"sort"
	"syscall"
	"time"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/newznab"
	"github.com/braedonsaunders/ether/internal/nzb"
	"github.com/braedonsaunders/ether/internal/par2"
	"github.com/braedonsaunders/ether/internal/store"
	"github.com/braedonsaunders/ether/internal/tmdb"
	"github.com/braedonsaunders/ether/internal/usenet"
)

type sample struct {
	Number          int     `json:"number"`
	Title           string  `json:"title"`
	Kind            string  `json:"kind"`
	Release         string  `json:"release"`
	NZBSHA256       string  `json:"nzbSHA256"`
	Bytes           int64   `json:"listedBytes"`
	Files           int     `json:"files"`
	Articles        int     `json:"articles"`
	ListingGaps     int     `json:"listingGaps"`
	BodyOK          int     `json:"bodyOK"`
	Missing         int     `json:"missing"`
	Corrupt         int     `json:"corrupt"`
	Transient       int     `json:"transient"`
	DownloadedBytes int64   `json:"downloadedBytes"`
	ParFiles        int     `json:"parFiles"`
	SliceBytes      int64   `json:"sliceBytes"`
	ProtectedFiles  int     `json:"protectedFiles"`
	Status          string  `json:"status"`
	Seconds         float64 `json:"seconds"`
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("ether-repairbench", flag.ContinueOnError)
	configDir := fs.String("config-dir", config.DefaultDataDir(), "existing owner config directory (read only)")
	dir := fs.String("dir", "", "private experiment directory (required)")
	count := fs.Int("titles", 12, "random titles to search")
	perTitle := fs.Int("releases", 2, "NZBs to sample per title")
	seed := fs.Int64("seed", 20261021, "reproducible random seed")
	mode := fs.String("mode", "sample", "sample, exercise, download or sab-config")
	sampleNo := fs.Int("sample", 0, "one saved sample number; 0 selects all for exercise")
	maxBytes := fs.Int64("max-bytes", 8<<30, "maximum downloaded bytes per experiment")
	timeout := fs.Duration("timeout", 30*time.Minute, "overall experiment deadline")
	cpuProfile := fs.String("cpu-profile", "", "optional CPU profile output for download diagnosis")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dir == "" || *count < 1 || *perTitle < 1 || *maxBytes < 1 {
		return errors.New("invalid experiment bounds")
	}
	if *cpuProfile != "" {
		profile, err := os.OpenFile(*cpuProfile, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		defer profile.Close()
		if err := pprof.StartCPUProfile(profile); err != nil {
			return err
		}
		defer pprof.StopCPUProfile()
	}
	if err := os.MkdirAll(*dir, 0700); err != nil {
		return err
	}
	cfg, err := config.Read(*configDir)
	if err != nil {
		return errors.New("cannot read owner configuration")
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	providers := append([]config.UsenetProvider(nil), cfg.Usenet.Providers...)
	enabled := 0
	for i := range providers {
		providers[i].Connections = min(4, max(1, providers[i].Connections))
		providers[i].MinIdle = -1
		if providers[i].Enabled {
			enabled++
		}
	}
	if enabled == 0 {
		return errors.New("no enabled Usenet providers")
	}
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	fetch := usenet.New(providers, usenet.Options{Logger: log})
	defer fetch.Close()
	if *mode == "exercise" {
		return exercise(ctx, *dir, fetch, *maxBytes, *sampleNo)
	}
	if *mode == "sab-config" {
		return sabConfig(*dir, cfg, *sampleNo)
	}
	if *mode == "download" {
		return downloadSample(ctx, *dir, fetch, *maxBytes, *sampleNo)
	}
	if *mode != "sample" {
		return errors.New("unknown mode")
	}
	if err := os.MkdirAll(filepath.Join(*dir, "state"), 0700); err != nil {
		return err
	}
	db, err := store.Open(ctx, filepath.Join(*dir, "state"))
	if err != nil {
		return err
	}
	defer db.Close()
	indexers := []config.Indexer{}
	for _, v := range cfg.Indexers {
		if v.Enabled {
			indexers = append(indexers, v)
		}
	}
	if len(indexers) == 0 {
		return errors.New("no enabled indexers")
	}
	idx := newznab.NewManager(db, indexers, newznab.Options{Logger: log, NZBDir: filepath.Join(*dir, "nzb-cache"), MaxParallel: 2})
	client := tmdb.NewFromConfig(cfg.Catalog, log)
	rng := rand.New(rand.NewSource(*seed))
	var total int64
	number := 0
	report, err := os.OpenFile(filepath.Join(*dir, "samples.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	defer report.Close()
	existing, _ := filepath.Glob(filepath.Join(*dir, "sample-*.nzb"))
	number = len(existing)
	for t := 0; t < *count && ctx.Err() == nil && total < *maxBytes; t++ {
		req := core.SearchRequest{}
		kind := "movie"
		page := 1 + rng.Intn(10)
		if t%2 == 0 {
			p, err := client.DiscoverMovies(ctx, tmdb.DiscoverParams{Page: page, SortBy: "popularity.desc", VoteCountGTE: 100, ReleaseDateLTE: time.Now().Format("2006-01-02")})
			if err != nil || len(p.Results) == 0 {
				fmt.Println("catalogue movie request failed")
				continue
			}
			v := p.Results[rng.Intn(len(p.Results))]
			req = core.SearchRequest{Type: core.TypeMovie, Title: v.Title, IDs: core.IDs{TMDB: v.ID}}
			if d, e := client.MovieDetails(ctx, v.ID); e == nil {
				req.IDs.IMDB = d.IMDBID
			}
		} else {
			kind = "episode"
			p, err := client.DiscoverTV(ctx, tmdb.DiscoverParams{Page: page, SortBy: "popularity.desc", VoteCountGTE: 100})
			if err != nil || len(p.Results) == 0 {
				fmt.Println("catalogue TV request failed")
				continue
			}
			v := p.Results[rng.Intn(len(p.Results))]
			req = core.SearchRequest{Type: core.TypeEpisode, Title: v.Name, IDs: core.IDs{TMDB: v.ID}, Season: 1, Episode: 1}
			if d, e := client.TVDetails(ctx, v.ID); e == nil && d.ExternalIDs != nil {
				req.IDs.IMDB = d.ExternalIDs.IMDBID
				req.IDs.TVDB = d.ExternalIDs.TVDBID
			}
		}
		results, err := idx.Search(ctx, req)
		if err != nil {
			fmt.Printf("search %d %s failed\n", t, kind)
			continue
		}
		rng.Shuffle(len(results), func(i, j int) { results[i], results[j] = results[j], results[i] })
		used := 0
		for _, rel := range results {
			if used >= *perTitle || total >= *maxBytes || ctx.Err() != nil {
				break
			}
			if rel.Size > 12<<30 || rel.Size <= 0 {
				continue
			}
			body, err := idx.FetchNZB(ctx, rel)
			if err != nil {
				continue
			}
			doc, err := nzb.Parse(body)
			if err != nil {
				continue
			}
			number++
			used++
			name := fmt.Sprintf("sample-%03d.nzb", number)
			if err := os.WriteFile(filepath.Join(*dir, name), body, 0600); err != nil {
				return err
			}
			hash := sha256.Sum256(body)
			s := sample{Number: number, Title: req.Title, Kind: kind, Release: rel.Name, NZBSHA256: hex.EncodeToString(hash[:]), Bytes: rel.Size, Files: len(doc.Files)}
			start := time.Now()
			sampleCtx, c := context.WithTimeout(ctx, 90*time.Second)
			inspect(sampleCtx, doc, fetch, rng, &s, *maxBytes-total)
			c()
			s.Seconds = time.Since(start).Seconds()
			total += s.DownloadedBytes
			if err := json.NewEncoder(report).Encode(s); err != nil {
				return err
			}
			fmt.Printf("sample=%d kind=%s files=%d par=%d slice=%d body=%d missing=%d corrupt=%d transient=%d bytes=%d\n", number, kind, s.Files, s.ParFiles, s.SliceBytes, s.BodyOK, s.Missing, s.Corrupt, s.Transient, s.DownloadedBytes)
		}
	}
	fmt.Printf("sample batch complete: downloaded=%d\n", total)
	return ctx.Err()
}
func inspect(ctx context.Context, doc *nzb.NZB, fetch core.ArticleFetcher, rng *rand.Rand, s *sample, budget int64) {
	type article struct {
		file *nzb.File
		seg  nzb.Segment
	}
	var all []article
	for _, f := range doc.Files {
		s.Articles += len(f.Segments)
		s.ListingGaps += f.SegmentCount() - len(f.Segments)
		if f.Info.Kind == nzb.KindPar2 {
			s.ParFiles++
		}
		for _, a := range f.Segments {
			all = append(all, article{f, a})
		}
	}
	rng.Shuffle(len(all), func(i, j int) { all[i], all[j] = all[j], all[i] })
	for _, a := range all[:min(32, len(all))] {
		if ctx.Err() != nil || s.DownloadedBytes >= budget {
			break
		}
		b, _, err := fetch.BodyPart(ctx, a.seg.MessageID, a.file.Groups, core.PriorityPrefetch)
		switch {
		case err == nil:
			s.BodyOK++
			s.DownloadedBytes += int64(len(b))
		case errors.Is(err, core.ErrArticleMissing):
			s.Missing++
		case errors.Is(err, core.ErrArticleCorrupt):
			s.Corrupt++
		default:
			s.Transient++
		}
	}
	files := append([]*nzb.File(nil), doc.Files...)
	sort.Slice(files, func(i, j int) bool { return files[i].Bytes() < files[j].Bytes() })
	for _, f := range files {
		if f.Info.Kind != nzb.KindPar2 || f.Bytes() > 4<<20 || s.DownloadedBytes >= budget {
			continue
		}
		var data []byte
		for _, a := range f.Segments {
			b, _, err := fetch.BodyPart(ctx, a.MessageID, f.Groups, core.PriorityPrefetch)
			if err == nil {
				data = append(data, b...)
				s.DownloadedBytes += int64(len(b))
			}
			if len(data) > 8<<20 || ctx.Err() != nil {
				break
			}
		}
		set, err := par2.Parse(data)
		if err == nil {
			for _, m := range set.Main {
				s.SliceBytes = m.SliceSize
				s.ProtectedFiles = len(m.Files)
				break
			}
			break
		}
	}
	s.Status = "sampled-healthy"
	if s.Missing+s.Corrupt+s.ListingGaps > 0 {
		s.Status = "sampled-damaged"
	} else if s.Transient > 0 {
		s.Status = "inconclusive"
	}
}
