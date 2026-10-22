package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/braedonsaunders/ether/internal/core"
)

type fixtureFetcher struct{ missing bool }

func (f fixtureFetcher) BodyPart(context.Context, string, []string, core.Priority) ([]byte, core.ArticlePart, error) {
	if f.missing {
		return nil, core.ArticlePart{}, core.ErrArticleMissing
	}
	return []byte("data"), core.ArticlePart{Name: "test.bin", FileSize: 4, Part: 1, Total: 1, Begin: 1, End: 4}, nil
}
func (f fixtureFetcher) Body(c context.Context, id string, g []string, p core.Priority) ([]byte, error) {
	b, _, e := f.BodyPart(c, id, g, p)
	return b, e
}
func (f fixtureFetcher) Stat(context.Context, string) (bool, error) { return !f.missing, nil }

func TestFullDownloadReportsVerifiedOutputAndFailure(t *testing.T) {
	for _, missing := range []bool{false, true} {
		dir := t.TempDir()
		doc := `<nzb><file subject="&quot;test.bin&quot;"><groups><group>alt.test</group></groups><segments><segment bytes="4" number="1">test</segment></segments></file></nzb>`
		if err := os.WriteFile(filepath.Join(dir, "sample-001.nzb"), []byte(doc), 0600); err != nil {
			t.Fatal(err)
		}
		if err := downloadSample(context.Background(), dir, fixtureFetcher{missing}, 1024, 1); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(filepath.Join(dir, "ether-download-001.json"))
		if err != nil {
			t.Fatal(err)
		}
		var result struct {
			Failure string
			Files   []struct {
				Bytes  int64
				SHA256 string
			}
			OutputDirectory string
		}
		if err := json.Unmarshal(data, &result); err != nil {
			t.Fatal(err)
		}
		if missing {
			if result.Failure == "" {
				t.Fatal("missing source reported complete")
			}
			continue
		}
		sum := sha256.Sum256([]byte("data"))
		if result.Failure != "" || len(result.Files) != 1 || result.Files[0].Bytes != 4 || result.Files[0].SHA256 != hex.EncodeToString(sum[:]) {
			t.Fatal(result)
		}
		payload, err := os.ReadFile(filepath.Join(result.OutputDirectory, "file-000.bin"))
		if err != nil || string(payload) != "data" {
			t.Fatal(string(payload), err)
		}
	}
}
