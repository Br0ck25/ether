package stream

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/usenet"
	"github.com/braedonsaunders/ether/internal/usenet/nntptest"
)

// Exercise the real socket -> pipelined NNTP -> yEnc CRC -> cached ciphertext
// -> archive CBC -> plaintext path, rather than only a decoded ArticleFetcher.
func TestEncryptedNNTPFaultRecovery(t *testing.T) {
	raw := readFixture(t, "7z-crypto/content.7z")
	want := readFixture(t, "7z-crypto/video.mkv")
	for _, fault := range []string{"latency", "truncated-body", "corrupt-yenc", "mid-body-stall", "missing-primary"} {
		t.Run(fault, func(t *testing.T) {
			primary := nntptest.Start(t, nntptest.Options{Latency: 2 * time.Millisecond})
			backup := nntptest.Start(t, nntptest.Options{Latency: 2 * time.Millisecond})
			files := map[string][]byte{"video.7z": raw}
			opts := nntptest.PostOptions{Meta: map[string]string{"password": "Secret"}}
			post, err := primary.Post(files, 4093, opts)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := backup.Post(files, 4093, opts); err != nil {
				t.Fatal(err)
			}
			target := post.Files[0].Segments[20].MessageID
			switch fault {
			case "corrupt-yenc":
				if err := primary.Corrupt(target); err != nil {
					t.Fatal(err)
				}
			case "missing-primary":
				primary.SetMissing(true, target)
			default:
				primary.SetFault(func(cmd, arg string) nntptest.Fault {
					if cmd == "BODY" && arg == target {
						switch fault {
						case "truncated-body":
							return nntptest.Fault{Truncate: true}
						case "mid-body-stall":
							return nntptest.Fault{BodyDelay: time.Second}
						}
					}
					return nntptest.Fault{}
				})
			}
			p, b := primary.Provider("primary", 3), backup.Provider("backup", 2)
			b.Backfill = true
			fetcher := usenet.New([]config.UsenetProvider{p, b}, usenet.Options{MinIdle: -1, KeepAlive: -1, IdleReadTimeout: 75 * time.Millisecond, CommandTimeout: 3 * time.Second})
			t.Cleanup(func() { _ = fetcher.Close() })
			e := newTestEngine(t, fetcher, nil, Options{ReadAheadMin: 1, ReadAheadMax: 1})
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			media, err := e.Open(ctx, fault, post.NZB)
			if err != nil {
				t.Fatal(err)
			}
			defer media.Close()
			src := media.(*source)
			got := make([]byte, len(want))
			if n, err := src.ReadMetadataAtContext(ctx, got, 0); err != nil || n != len(want) || !bytes.Equal(got, want) {
				t.Fatalf("recovered plaintext: %d %v equal=%v", n, err, bytes.Equal(got, want))
			}
			if fault != "latency" && backup.BodyRequests(target) == 0 {
				t.Fatal("test did not exercise encrypted article backfill")
			}
		})
	}
}
