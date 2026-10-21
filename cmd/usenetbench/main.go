// usenetbench generates a deterministic local NNTP fixture and downloads it
// through Ether's production fetcher or streaming engine. It never needs real
// providers or media. The same NZB is consumable by external NNTP clients.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/stream"
	"github.com/braedonsaunders/ether/internal/usenet"
	"github.com/braedonsaunders/ether/internal/usenet/nntptest"
)

type segment struct {
	ID     string `json:"id"`
	Offset int64  `json:"offset"`
	Size   int64  `json:"size"`
}
type manifest struct {
	Version   int                     `json:"version"`
	File      string                  `json:"file"`
	Size      int64                   `json:"size"`
	SHA256    string                  `json:"sha256"`
	NZB       string                  `json:"nzb"`
	Scenario  string                  `json:"scenario"`
	Providers []config.UsenetProvider `json:"providers"`
	Segments  []segment               `json:"segments"`
}

type result struct {
	Mode             string                 `json:"mode"`
	Depth            int                    `json:"depth"`
	Connections      int                    `json:"connections"`
	Workers          int                    `json:"workers"`
	Size             int64                  `json:"size"`
	FirstPartSeconds float64                `json:"firstPartSeconds"`
	FullSeconds      float64                `json:"fullSeconds"`
	HashSeconds      float64                `json:"hashSeconds"`
	MiBPerSecond     float64                `json:"mibPerSecond"`
	SHA256           string                 `json:"sha256"`
	Verified         bool                   `json:"verified"`
	Providers        []usenet.ProviderStats `json:"providers"`
	Engine           *stream.Stats          `json:"engine,omitempty"`
	HotMiB           int64                  `json:"hotMiB,omitempty"`
	ReadAheadMiB     int64                  `json:"readAheadMiB,omitempty"`
	ReadAheadWorkers int                    `json:"readAheadWorkers,omitempty"`
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(ctx context.Context, args []string, out, errout io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: usenetbench serve|download [flags]")
	}
	switch args[0] {
	case "serve":
		return serve(ctx, args[1:], out, errout)
	case "download":
		return download(ctx, args[1:], out, errout)
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func serve(ctx context.Context, args []string, out, errout io.Writer) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(errout)
	dir := fs.String("dir", "", "fixture output directory (required)")
	size := fs.Int("size-mib", 64, "deterministic payload size in MiB (1..1024)")
	part := fs.Int("segment-kib", 768, "decoded article size in KiB (1..32768)")
	primaryAddr := fs.String("primary", "127.0.0.1:0", "primary NNTP listen address")
	backupAddr := fs.String("backup", "", "backup listen address; automatic for backfill")
	conns := fs.Int("connections", 16, "server maximum concurrent connections")
	scenario := fs.String("scenario", "clean", "clean|delay|truncate|stall|backfill")
	delay := fs.Duration("delay", 20*time.Millisecond, "serial server response delay (not propagation RTT)")
	stall := fs.Duration("stall", 6*time.Second, "first-attempt mid-body stall")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dir == "" || *size < 1 || *size > 1024 || *part < 1 || *part > 32768 || *conns < 1 {
		return errors.New("invalid fixture directory/size/segment/connections")
	}
	switch *scenario {
	case "clean", "delay", "truncate", "stall", "backfill":
	default:
		return fmt.Errorf("unknown scenario %q", *scenario)
	}
	abs, err := filepath.Abs(*dir)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(abs, 0755); err != nil {
		return err
	}
	payload := fixtureData(*size << 20)
	sum := sha256.Sum256(payload)
	m := manifest{Version: 1, File: "fixture.mkv", Size: int64(len(payload)), SHA256: hex.EncodeToString(sum[:]), NZB: filepath.Join(abs, "fixture.nzb"), Scenario: *scenario}
	opts := nntptest.Options{Addr: *primaryAddr, MaxConns: *conns}
	if *scenario == "delay" {
		opts.Latency = *delay
	}
	primary, err := nntptest.NewServer(opts)
	if err != nil {
		return err
	}
	defer primary.Close()
	post, err := primary.Post(map[string][]byte{m.File: payload}, *part<<10, nntptest.PostOptions{})
	if err != nil {
		return err
	}
	m.Providers = append(m.Providers, primary.Provider("primary", 4))
	for _, p := range post.Files[0].Segments {
		m.Segments = append(m.Segments, segment{ID: p.MessageID, Offset: p.Begin - 1, Size: p.End - p.Begin + 1})
	}
	var backup *nntptest.Server
	if *scenario == "backfill" && *backupAddr == "" {
		*backupAddr = "127.0.0.1:0"
	}
	if *backupAddr != "" {
		backup, err = nntptest.NewServer(nntptest.Options{Addr: *backupAddr, MaxConns: *conns})
		if err != nil {
			return err
		}
		defer backup.Close()
		if _, err = backup.Post(map[string][]byte{m.File: payload}, *part<<10, nntptest.PostOptions{}); err != nil {
			return err
		}
		provider := backup.Provider("backup", 4)
		provider.Backfill = true
		provider.Priority = 1
		m.Providers = append(m.Providers, provider)
	}
	selected := map[string]bool{}
	for i, p := range m.Segments {
		if i%7 == 0 {
			selected[p.ID] = true
			if *scenario == "backfill" {
				primary.SetMissing(true, p.ID)
			}
		}
	}
	if *scenario == "truncate" || *scenario == "stall" {
		seen := map[string]bool{}
		var mu sync.Mutex
		primary.SetFault(func(cmd, id string) nntptest.Fault {
			if cmd != "BODY" && cmd != "ARTICLE" {
				return nntptest.Fault{}
			}
			mu.Lock()
			first := selected[id] && !seen[id]
			seen[id] = true
			mu.Unlock()
			if first {
				if *scenario == "truncate" {
					return nntptest.Fault{Truncate: true}
				}
				return nntptest.Fault{BodyDelay: *stall}
			}
			return nntptest.Fault{}
		})
	}
	if err = os.WriteFile(m.NZB, post.NZB, 0644); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	manifestPath := filepath.Join(abs, "manifest.json")
	if err = os.WriteFile(manifestPath, append(data, '\n'), 0644); err != nil {
		return err
	}
	backupListen := ""
	if backup != nil {
		backupListen = backup.Addr()
	}
	if err = json.NewEncoder(out).Encode(map[string]any{"ready": true, "manifest": manifestPath, "nzb": m.NZB, "primary": primary.Addr(), "backup": backupListen}); err != nil {
		return err
	}
	<-ctx.Done()
	return nil
}

// SplitMix64 provides reproducible, incompressible-looking bytes; it is a test
// pattern, not cryptography. Little-endian encoding fixes the fixture on all CPUs.
func fixtureData(n int) []byte {
	data := make([]byte, n)
	x := uint64(0x45544845524e4e54)
	for i := 0; i < len(data); i += 8 {
		x += 0x9e3779b97f4a7c15
		z := x
		z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
		z = (z ^ (z >> 27)) * 0x94d049bb133111eb
		z ^= z >> 31
		var word [8]byte
		binary.LittleEndian.PutUint64(word[:], z)
		copy(data[i:], word[:])
	}
	return data
}

func download(ctx context.Context, args []string, out, errout io.Writer) error {
	fs := flag.NewFlagSet("download", flag.ContinueOnError)
	fs.SetOutput(errout)
	input := fs.String("manifest", "", "fixture manifest (required)")
	output := fs.String("output", "", "output file (required)")
	connections := fs.Int("connections", 4, "connections per provider")
	workers := fs.Int("workers", 16, "concurrent fetches")
	depth := fs.Int("depth", 4, "BODY pipeline depth (1..16)")
	engineMode := fs.Bool("engine", false, "download through stream.Engine sequential ReaderAt")
	hotMiB := fs.Int64("hot-mib", 256, "engine hot buffer in MiB (production default 256)")
	readAheadMiB := fs.Int64("read-ahead-mib", 64, "engine maximum read-ahead in MiB (production default 64)")
	readAheadWorkers := fs.Int("read-ahead-workers", 8, "engine read-ahead workers (production default 8)")
	timeout := fs.Duration("timeout", 2*time.Minute, "whole download deadline")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *input == "" || *output == "" || *workers < 1 || *workers > 256 || *connections < 1 || *connections > 128 || *depth < 1 || *depth > 16 || *hotMiB < 1 || *hotMiB > 4096 || *readAheadMiB < 1 || *readAheadMiB > 4096 || *readAheadWorkers < 1 || *readAheadWorkers > 256 {
		return errors.New("invalid manifest/output/workers/connections/depth")
	}
	raw, err := os.ReadFile(*input)
	if err != nil {
		return err
	}
	var m manifest
	if err = json.Unmarshal(raw, &m); err != nil {
		return err
	}
	if err = validateManifest(m); err != nil {
		return err
	}
	for i := range m.Providers {
		m.Providers[i].Connections = *connections
		m.Providers[i].MinIdle = -1
	}
	logger := slog.New(slog.NewTextHandler(errout, &slog.HandlerOptions{Level: slog.LevelError}))
	fetcher := usenet.New(m.Providers, usenet.Options{Logger: logger, MinIdle: -1, KeepAlive: -1, BodyPipelineDepth: *depth})
	defer fetcher.Close()
	file, err := os.OpenFile(*output, os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	if err = file.Truncate(m.Size); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	start := time.Now()
	var first atomic.Int64
	markFirst := func() { first.CompareAndSwap(0, max(1, time.Since(start).Nanoseconds())) }
	report := result{Mode: "fetcher", Depth: *depth, Connections: *connections, Workers: *workers, Size: m.Size}
	if *engineMode {
		report.Mode = "engine"
		report.HotMiB, report.ReadAheadMiB = *hotMiB, *readAheadMiB
		report.ReadAheadWorkers = *readAheadWorkers
		engine := stream.New(fetcher, nil, stream.Options{ReadParallel: *workers, ReadAheadParallel: *readAheadWorkers, HotBufferBytes: *hotMiB << 20, ReadAheadMax: *readAheadMiB << 20, ReadTimeout: *timeout}, logger)
		defer engine.Close()
		stop := context.AfterFunc(ctx, func() { engine.Close() })
		defer stop()
		nzb, readErr := os.ReadFile(m.NZB)
		if readErr != nil {
			return readErr
		}
		source, openErr := engine.Open(ctx, "benchmark-fixture", nzb)
		if openErr != nil {
			return openErr
		}
		defer source.Close()
		if source.Size() != m.Size {
			return fmt.Errorf("engine size %d, expected %d", source.Size(), m.Size)
		}
		buf := make([]byte, 4<<20)
		for offset := int64(0); offset < m.Size; {
			if err = ctx.Err(); err != nil {
				return err
			}
			n, readErr := source.ReadAt(buf[:min(int64(len(buf)), m.Size-offset)], offset)
			if n > 0 {
				if _, err = file.WriteAt(buf[:n], offset); err != nil {
					return err
				}
				markFirst()
				offset += int64(n)
			}
			if readErr != nil && !(errors.Is(readErr, io.EOF) && offset == m.Size) {
				return readErr
			}
			if n == 0 {
				return io.ErrNoProgress
			}
		}
		if err = engine.Close(); err != nil {
			return err
		}
		stats := engine.Stats()
		report.Engine = &stats
	} else {
		var next atomic.Int64
		var wg sync.WaitGroup
		var firstErr error
		var errMu sync.Mutex
		for i := 0; i < *workers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					index := int(next.Add(1) - 1)
					if index >= len(m.Segments) || ctx.Err() != nil {
						return
					}
					part := m.Segments[index]
					data, header, e := fetcher.BodyPart(ctx, part.ID, []string{"alt.binaries.test"}, core.PriorityLive)
					if e == nil && (int64(len(data)) != part.Size || header.Begin != part.Offset+1 || header.End != part.Offset+part.Size || header.FileSize != m.Size) {
						e = fmt.Errorf("unexpected yEnc part for %s", part.ID)
					}
					if e == nil {
						_, e = file.WriteAt(data, part.Offset)
						if e == nil {
							markFirst()
						}
					}
					if e != nil {
						errMu.Lock()
						if firstErr == nil {
							firstErr = e
						}
						errMu.Unlock()
						cancel()
						return
					}
				}
			}()
		}
		wg.Wait()
		if firstErr != nil {
			return firstErr
		}
		if err = ctx.Err(); err != nil {
			return err
		}
	}
	if err = file.Sync(); err != nil {
		return err
	}
	report.FullSeconds = time.Since(start).Seconds()
	report.FirstPartSeconds = time.Duration(first.Load()).Seconds()
	hashStart := time.Now()
	if _, err = file.Seek(0, 0); err != nil {
		return err
	}
	hash := sha256.New()
	if _, err = io.Copy(hash, file); err != nil {
		return err
	}
	report.HashSeconds = time.Since(hashStart).Seconds()
	report.SHA256 = hex.EncodeToString(hash.Sum(nil))
	report.Verified = report.SHA256 == m.SHA256
	report.MiBPerSecond = float64(m.Size) / (1 << 20) / report.FullSeconds
	report.Providers = fetcher.Stats()
	if err = json.NewEncoder(out).Encode(report); err != nil {
		return err
	}
	if !report.Verified {
		return fmt.Errorf("file checksum mismatch: got %s expected %s", report.SHA256, m.SHA256)
	}
	return nil
}

func validateManifest(m manifest) error {
	if m.Version != 1 || m.Size <= 0 || m.Size > 1<<30 || len(m.Providers) == 0 || len(m.Segments) == 0 {
		return errors.New("invalid fixture manifest")
	}
	if digest, err := hex.DecodeString(m.SHA256); err != nil || len(digest) != 32 {
		return errors.New("invalid fixture SHA256")
	}
	var offset int64
	for _, part := range m.Segments {
		if part.ID == "" || part.Offset != offset || part.Size <= 0 || part.Size > 32<<20 || part.Size > m.Size-offset {
			return errors.New("invalid/noncontiguous fixture segment")
		}
		offset += part.Size
	}
	if offset != m.Size {
		return errors.New("fixture segments do not cover expected file")
	}
	return nil
}
