package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/usenet/nntptest"
)

func TestDownloadVerifiedFetcherAndEngine(t *testing.T) {
	s := nntptest.Start(t, nntptest.Options{})
	data := fixtureData(2 << 20)
	posting, err := s.Post(map[string][]byte{"fixture.mkv": data}, 128<<10, nntptest.PostOptions{})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	m := manifest{Version: 1, File: "fixture.mkv", Size: int64(len(data)), Providers: []config.UsenetProvider{s.Provider("primary", 4)}, NZB: filepath.Join(dir, "fixture.nzb")}
	hash := sha256.Sum256(data)
	m.SHA256 = hex.EncodeToString(hash[:])
	for _, part := range posting.Files[0].Segments {
		m.Segments = append(m.Segments, segment{part.MessageID, part.Begin - 1, part.End - part.Begin + 1})
	}
	writeJSONFixture(t, dir, m, posting.NZB)
	for _, mode := range []string{"fetcher", "engine"} {
		t.Run(mode, func(t *testing.T) {
			args := []string{"download", "-manifest", filepath.Join(dir, "manifest.json"), "-output", filepath.Join(dir, mode+".bin"), "-depth", "4", "-connections", "2", "-workers", "8"}
			if mode == "engine" {
				args = append(args, "-engine")
			}
			var output bytes.Buffer
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := run(ctx, args, &output, io.Discard); err != nil {
				t.Fatal(err)
			}
			var r result
			if err := json.Unmarshal(output.Bytes(), &r); err != nil {
				t.Fatal(err)
			}
			if !r.Verified || r.SHA256 != m.SHA256 || r.Size != m.Size || r.FirstPartSeconds <= 0 || r.FullSeconds < r.FirstPartSeconds || r.Mode != mode {
				t.Fatalf("report %+v", r)
			}
		})
	}
}

func writeJSONFixture(t *testing.T, dir string, m manifest, nzb []byte) {
	t.Helper()
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(m.NZB, nzb, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestManifestRejectsGapsAndOversize(t *testing.T) {
	m := manifest{Version: 1, Size: 4, SHA256: hex.EncodeToString(make([]byte, 32)), Providers: []config.UsenetProvider{{Host: "test"}}, Segments: []segment{{"a", 0, 4}}}
	if err := validateManifest(m); err != nil {
		t.Fatal(err)
	}
	m.Segments[0].Offset = 1
	if err := validateManifest(m); err == nil {
		t.Fatal("accepted gap")
	}
	m.Segments[0].Offset = 0
	m.Segments[0].Size = 5
	if err := validateManifest(m); err == nil {
		t.Fatal("accepted oversize part")
	}
}

func TestServeBackfillFixtureRoundTrip(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	reader, writer := io.Pipe()
	done := make(chan error, 1)
	dir := t.TempDir()
	go func() {
		err := run(ctx, []string{"serve", "-dir", dir, "-size-mib", "1", "-segment-kib", "64", "-scenario", "backfill"}, writer, io.Discard)
		writer.CloseWithError(err)
		done <- err
	}()
	defer func() {
		cancel()
		reader.Close()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	var ready struct {
		Ready    bool   `json:"ready"`
		Manifest string `json:"manifest"`
		Backup   string `json:"backup"`
	}
	if err := json.NewDecoder(reader).Decode(&ready); err != nil {
		t.Fatal(err)
	}
	if !ready.Ready || ready.Backup == "" {
		t.Fatalf("ready %+v", ready)
	}
	var output bytes.Buffer
	if err := run(context.Background(), []string{"download", "-manifest", ready.Manifest, "-output", filepath.Join(dir, "actual.bin")}, &output, io.Discard); err != nil {
		t.Fatal(err)
	}
	var result result
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !result.Verified || len(result.Providers) != 2 || result.Providers[1].BackfillSaves == 0 {
		t.Fatalf("result %+v", result)
	}
}
