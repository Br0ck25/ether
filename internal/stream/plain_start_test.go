package stream

import (
	"errors"
	"strings"
	"testing"

	"github.com/braedonsaunders/ether/internal/core"
)

func TestPlainFileCannotBeHealthyWhenFirstArticleStartsMidFile(t *testing.T) {
	f := newFakeFetcher()
	doc := post(t, f, 10000, postFile{name: "example.mkv", data: mkvBytes(1, 100000)})
	// The NZB calls this slot one, but the referenced yEnc article belongs
	// to the middle of the upload. STAT still succeeds for every article.
	doc = []byte(strings.Replace(string(doc), postID("example.mkv", 1), postID("example.mkv", 7), 1))
	e := newTestEngine(t, f, nil, Options{})
	ok, err := e.CheckRelease(t.Context(), "wrong-start", doc)
	if ok || !errors.Is(err, ErrCorrupt) || !errors.Is(err, core.ErrReleaseRejected) {
		t.Fatalf("mid-file start became healthy: %v %v", ok, err)
	}
	if src, err := e.Open(t.Context(), "wrong-start", doc); err == nil {
		src.Close()
		t.Fatal("mid-file start became playable")
	} else if !errors.Is(err, core.ErrReleaseRejected) {
		t.Fatalf("open lost typed rejection: %v", err)
	}
}
