package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync/atomic"
	"time"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/nzb"
	"github.com/braedonsaunders/ether/internal/stream"
)

type faultFetcher struct {
	core.ArticleFetcher
	deny  map[string]bool
	bytes atomic.Int64
	calls atomic.Int64
	limit int64
}

func (f *faultFetcher) BodyPart(ctx context.Context, id string, g []string, p core.Priority) ([]byte, core.ArticlePart, error) {
	if f.deny[id] {
		return nil, core.ArticlePart{}, core.ErrArticleMissing
	}
	if f.limit > 0 && f.bytes.Load() >= f.limit {
		return nil, core.ArticlePart{}, errors.New("experiment byte budget exhausted")
	}
	f.calls.Add(1)
	b, h, e := f.ArticleFetcher.BodyPart(ctx, id, g, p)
	f.bytes.Add(int64(len(b)))
	return b, h, e
}
func (f *faultFetcher) BodyPartBurst(ctx context.Context, id string, g []string, p core.Priority, until time.Time) ([]byte, core.ArticlePart, error) {
	if f.deny[id] {
		return nil, core.ArticlePart{}, core.ErrArticleMissing
	}
	if f.limit > 0 && f.bytes.Load() >= f.limit {
		return nil, core.ArticlePart{}, errors.New("experiment byte budget exhausted")
	}
	if burst, ok := f.ArticleFetcher.(core.BurstArticleFetcher); ok {
		f.calls.Add(1)
		b, h, err := burst.BodyPartBurst(ctx, id, g, p, until)
		f.bytes.Add(int64(len(b)))
		return b, h, err
	}
	return f.BodyPart(ctx, id, g, p)
}

func (f *faultFetcher) Body(ctx context.Context, id string, g []string, p core.Priority) ([]byte, error) {
	b, _, e := f.BodyPart(ctx, id, g, p)
	return b, e
}

type exerciseResult struct {
	Sample          string       `json:"sample"`
	Scenario        string       `json:"scenario"`
	Cache           string       `json:"cache"`
	Outcome         string       `json:"outcome"`
	Verified        bool         `json:"verified"`
	TargetBytes     int          `json:"targetBytes"`
	Seconds         float64      `json:"seconds"`
	DownloadedBytes int64        `json:"downloadedBytes"`
	Calls           int64        `json:"calls"`
	WarmSeconds     float64      `json:"warmSeconds"`
	WarmBytes       int64        `json:"warmBytes"`
	Engine          stream.Stats `json:"engine"`
}

func exercise(ctx context.Context, dir string, fetch core.ArticleFetcher, budget int64, sampleNo int) error {
	names, err := filepath.Glob(filepath.Join(dir, "sample-*.nzb"))
	if err != nil {
		return err
	}
	sort.Strings(names)
	if sampleNo > 0 {
		names = []string{filepath.Join(dir, fmt.Sprintf("sample-%03d.nzb", sampleNo))}
	}
	out, err := os.OpenFile(filepath.Join(dir, "exercise.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer out.Close()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	var used int64
	for _, path := range names {
		if ctx.Err() != nil || used >= budget {
			break
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		doc, err := nzb.Parse(body)
		if err != nil {
			continue
		}
		var file *nzb.File
		for _, f := range doc.Files {
			if f.Info.Kind != nzb.KindPar2 && (file == nil || f.Bytes() > file.Bytes()) {
				file = f
			}
		}
		if file == nil || len(file.Segments) < 8 {
			continue
		}
		a := file.Segments[len(file.Segments)/2]
		baseCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		want, header, err := fetch.BodyPart(baseCtx, a.MessageID, file.Groups, core.PriorityPrefetch)
		cancel()
		if err != nil || header.Begin < 1 {
			r := exerciseResult{Sample: filepath.Base(path), Scenario: "natural", Cache: "cold", Outcome: "reference-article-unavailable"}
			json.NewEncoder(out).Encode(r)
			continue
		}
		for _, variant := range []string{"selective", "whole", "summary"} {
			if ctx.Err() != nil || used >= budget {
				break
			}
			ff := &faultFetcher{ArticleFetcher: fetch, deny: map[string]bool{a.MessageID: true}, limit: min(budget-used, 4<<30)}
			cfg := config.Repair{Enabled: true, WholeSlices: variant == "whole", AheadWait: config.Duration(3 * time.Minute), MaxDownloadBytes: ff.limit}
			if variant == "summary" {
				cfg.SummaryRows = 8
			}
			eng := stream.New(ff, nil, stream.Options{Repair: cfg, HotBufferBytes: 8 << 20, ReadParallel: 4, Retries: -1}, log)
			r := exerciseResult{Sample: filepath.Base(path), Scenario: "injected-single-article-" + variant, Cache: "cold", TargetBytes: len(want)}
			runCtx, c := context.WithTimeout(ctx, 3*time.Minute)
			start := time.Now()
			err := eng.InitRepair(runCtx, "experiment", body)
			if err == nil && variant == "summary" {
				buf := make([]byte, 1<<20)
				for off := int64(0); off < min(header.Begin-1, 32<<20); off += int64(len(buf)) {
					if _, err = eng.ReadPostedFile(runCtx, "experiment", body, file.Index, off, buf); err != nil {
						break
					}
				}
			}
			r.WarmSeconds = time.Since(start).Seconds()
			r.WarmBytes = ff.bytes.Load()
			start = time.Now()
			dst := make([]byte, len(want))
			if err == nil {
				var n int
				n, err = eng.ReadPostedFile(runCtx, "experiment", body, file.Index, header.Begin-1, dst)
				r.Verified = err == nil && n == len(dst) && bytes.Equal(dst, want)
			}
			r.Seconds = time.Since(start).Seconds()
			r.Engine = eng.Stats()
			r.DownloadedBytes = ff.bytes.Load()
			r.Calls = ff.calls.Load()
			used += r.DownloadedBytes
			r.Outcome = "failed"
			if r.Verified {
				r.Outcome = "repaired"
			} else if errors.Is(runCtx.Err(), context.Canceled) {
				r.Outcome = "canceled"
			} else if errors.Is(err, context.DeadlineExceeded) || runCtx.Err() != nil {
				r.Outcome = "deadline"
			} else if r.Engine.RepairAttempts == 0 {
				r.Outcome = "metadata-unavailable"
			}
			c()
			eng.Close()
			if err := json.NewEncoder(out).Encode(r); err != nil {
				return err
			}
			fmt.Printf("exercise %s %s %s verified=%v seconds=%.3f bytes=%d summarySaved=%d\n", r.Sample, variant, r.Outcome, r.Verified, r.Seconds, r.DownloadedBytes, r.Engine.RepairSummaryBytes)
		}
	}
	return ctx.Err()
}
