package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/nzb"
	"github.com/braedonsaunders/ether/internal/segcache"
	"github.com/braedonsaunders/ether/internal/stream"
	"github.com/braedonsaunders/ether/internal/usenet"
)

func sabConfig(dir string, cfg config.Config, number int) error {
	if number <= 0 {
		return errors.New("-sample required")
	}
	folder, err := os.MkdirTemp(dir, fmt.Sprintf("sab-%03d-", number))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(folder, 0700); err != nil {
		return err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	quote := func(s string) string { b, _ := json.Marshal(s); return string(b) }
	lines := []string{"[misc]", "host = 127.0.0.1", fmt.Sprintf("port = %d", port), "api_key = local-repair-benchmark-only", "auto_browser = 0", "check_new_rel = 0", "enable_https = 0", "ipv6_servers = 0", "enable_unrar = 0", "direct_unpack = 0", "enable_par_cleanup = 0", "fast_fail = 0", "safe_postproc = 1", "pre_check = 0", "folder_rename = 0", "pause_on_post_processing = 0", "ignore_samples = 0", "download_dir = " + quote(filepath.Join(folder, "incomplete")), "complete_dir = " + quote(filepath.Join(folder, "complete")), "admin_dir = " + quote(filepath.Join(folder, "admin")), "log_dir = " + quote(filepath.Join(folder, "logs")), "[servers]"}
	backfillPriority := 0
	for _, p := range cfg.Usenet.Providers {
		if p.Enabled && !p.Backfill {
			backfillPriority = max(backfillPriority, p.Priority+1)
		}
	}
	for i, p := range cfg.Usenet.Providers {
		if !p.Enabled {
			continue
		}
		priority := max(0, p.Priority)
		if p.Backfill {
			priority += backfillPriority
		}
		ssl := 0
		if p.TLS {
			ssl = 1
		}
		lines = append(lines, fmt.Sprintf("[[provider%d]]", i), "host = "+quote(p.Host), fmt.Sprintf("port = %d", p.Port), fmt.Sprintf("ssl = %d", ssl), "ssl_verify = 2", "username = "+quote(p.Username), "password = "+quote(p.Password), fmt.Sprintf("connections = %d", min(4, max(1, p.Connections))), "enable = 1", fmt.Sprintf("priority = %d", priority), "timeout = 20", "retention = 0")
	}
	if err := os.WriteFile(filepath.Join(folder, "sab.ini"), []byte(strings.Join(lines, "\n")+"\n"), 0600); err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"directory": folder, "port": port, "nzb": filepath.Join(dir, fmt.Sprintf("sample-%03d.nzb", number))})
}
func downloadSample(ctx context.Context, dir string, fetch core.ArticleFetcher, budget int64, number int) error {
	if number <= 0 {
		return errors.New("-sample required")
	}
	body, err := os.ReadFile(filepath.Join(dir, fmt.Sprintf("sample-%03d.nzb", number)))
	if err != nil {
		return err
	}
	doc, err := nzb.Parse(body)
	if err != nil {
		return err
	}
	ff := &faultFetcher{ArticleFetcher: fetch, limit: budget}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	folder, err := os.MkdirTemp(dir, fmt.Sprintf("ether-files-%03d-", number))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(folder, 0700); err != nil {
		return err
	}
	cache, err := segcache.Open(filepath.Join(folder, "segments"), 1<<30, log)
	if err != nil {
		return err
	}
	defer cache.Close()
	eng := stream.New(ff, cache, stream.Options{HotBufferBytes: 32 << 20, ReadParallel: 16, Repair: config.Repair{Enabled: true, SummaryRows: 2, AheadWait: config.Duration(3 * time.Minute), MaxDownloadBytes: min(budget, 4<<30)}}, log)
	defer eng.Close()
	start := time.Now()
	_ = eng.InitRepair(ctx, "full-download", body)
	type fileResult struct {
		Name   string `json:"name"`
		Bytes  int64  `json:"bytes"`
		SHA256 string `json:"sha256"`
	}
	files := make([]fileResult, 0)
	var failure string
	var firstByteSeconds, maxReadSeconds float64
	var slowReads int
	for _, file := range doc.Files {
		if file.Info.Kind == nzb.KindPar2 {
			continue
		}
		h, err := eng.PostedFileInfo(ctx, "full-download", body, file.Index)
		if err != nil || h.FileSize <= 0 {
			failure = "file-header-unavailable"
			break
		}
		output, err := os.OpenFile(filepath.Join(folder, fmt.Sprintf("file-%03d.bin", file.Index)), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		hash := sha256.New()
		sink := io.MultiWriter(hash, output)
		buf := make([]byte, 16<<20)
		var off int64
		for off < h.FileSize && ctx.Err() == nil {
			n := min(int64(len(buf)), h.FileSize-off)
			readStart := time.Now()
			got, e := eng.ReadPostedRange(ctx, "full-download", body, file.Index, off, buf[:n])
			readSeconds := time.Since(readStart).Seconds()
			maxReadSeconds = max(maxReadSeconds, readSeconds)
			if readSeconds > 1 {
				slowReads++
			}
			if got > 0 {
				if firstByteSeconds == 0 {
					firstByteSeconds = time.Since(start).Seconds()
				}
				if _, err := sink.Write(buf[:got]); err != nil {
					output.Close()
					return err
				}
				off += int64(got)
			}
			if e != nil || got == 0 {
				failure = "download-failed"
				break
			}
		}
		if err := output.Sync(); err != nil {
			output.Close()
			return err
		}
		if err := output.Close(); err != nil {
			return err
		}
		files = append(files, fileResult{h.Name, off, hex.EncodeToString(hash.Sum(nil))})
		if failure != "" {
			break
		}
	}
	if ctx.Err() != nil {
		failure = "deadline"
	}
	result := map[string]any{"client": "ether", "sample": number, "seconds": time.Since(start).Seconds(), "downloadedBytes": ff.bytes.Load(), "files": files, "failure": failure, "engine": eng.Stats(), "cache": "cold", "segmentCacheBytes": 1 << 30, "hotBufferBytes": 32 << 20, "firstByteSeconds": firstByteSeconds, "maxReadSeconds": maxReadSeconds, "readsOverOneSecond": slowReads, "connectionsPerProvider": 4, "outputDirectory": folder}
	if measured, ok := fetch.(interface{ Stats() []usenet.ProviderStats }); ok {
		stats := measured.Stats()
		for i := range stats {
			stats[i].ID = fmt.Sprintf("provider-%d", i)
			stats[i].Name, stats[i].Host, stats[i].LastError = "", "", ""
		}
		result["providers"] = stats
	}
	data, _ := json.MarshalIndent(result, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("ether-download-%03d.json", number)), data, 0600); err != nil {
		return err
	}
	fmt.Println(string(data))
	return nil
}
