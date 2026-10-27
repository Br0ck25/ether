package debrid

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

// One revoked link seen by several in-flight requests is one failure, not
// one per request: a single refresh must recover, and the release must not
// be reported as missing (which marks it bad).
func TestEngineConcurrentRevokedLinkRefreshesOnce(t *testing.T) {
	size := 4 << 20
	r := newEngineRig(t, size, -1)
	r.media.mu.Lock()
	r.media.latency = 50 * time.Millisecond
	r.media.mu.Unlock()
	r.media.revokeAll()
	src, err := r.eng.Open(context.Background(), r.rel.ID, r.pl)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	meta := src.(interface {
		ReadMetadataAtContext(context.Context, []byte, int64) (int, error)
	})
	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			off := int64(i) << 20
			buf := make([]byte, 4096)
			_, errs[i] = meta.ReadMetadataAtContext(context.Background(), buf, off)
			if errs[i] == nil && !bytes.Equal(buf, r.media.data[off:off+4096]) {
				errs[i] = errors.New("content mismatch")
			}
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("reader %d: %v", i, err)
		}
	}
}

// A partial season pack whose only file is another episode never stands in
// for the requested one.
func TestChooseFileRejectsOtherEpisodeInPartialPack(t *testing.T) {
	files := []File{{Path: "Show.S01.1080p/Show.S01E01.1080p.WEB.mkv", Size: 2 << 30}}
	if _, err := ChooseFile(files, &core.ReleaseTarget{Season: 1, Episode: 5}); err == nil {
		t.Fatal("E01 was chosen for E05")
	}
	got, err := ChooseFile(files, &core.ReleaseTarget{Season: 1, Episode: 1})
	if err != nil || got.Path != files[0].Path {
		t.Fatalf("E01 for E01: %+v %v", got, err)
	}
	// A lone file with no episode number is still the episode.
	plain := []File{{Path: "Show.Episode.Title.mkv", Size: 1 << 30}}
	if _, err := ChooseFile(plain, &core.ReleaseTarget{Season: 1, Episode: 5}); err != nil {
		t.Fatalf("unnumbered single file: %v", err)
	}
}

func TestRealDebridErrorCodes(t *testing.T) {
	p := newRealDebrid("k", ProviderOptions{})
	for code, want := range map[int]error{
		25: nil, // service unavailable: transient
		35: ErrBlocked, // infringing_file: not cached on this account
		24: ErrGone,
		22: ErrAuth,
		36: ErrAuth,
		5:  ErrRateLimited,
		30: core.ErrReleaseRejected,
	} {
		err := p.classify(http.StatusServiceUnavailable, rdError{Error: "x", ErrorCode: code})
		for _, sentinel := range []error{ErrGone, ErrBlocked, ErrAuth, ErrRateLimited, core.ErrReleaseRejected} {
			if errors.Is(err, sentinel) != (sentinel == want) {
				t.Errorf("code %d: %v (is %v = %v)", code, err, sentinel, errors.Is(err, sentinel))
			}
		}
	}
}

// A dead link answered with 200 and a page (not the file) is refreshed,
// not reported as corrupt media.
func TestEngineTreatsWrongSized200AsDeadLink(t *testing.T) {
	r := newEngineRig(t, 256<<10, -1)
	r.media.mu.Lock()
	r.media.status["realdebrid-1"] = http.StatusOK // the stored link now serves an error page
	r.media.mu.Unlock()
	src, err := r.eng.Open(context.Background(), r.rel.ID, r.pl)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	buf := make([]byte, 10)
	if _, err := src.ReadAt(buf, 0); err != nil || !bytes.Equal(buf, r.media.data[:10]) {
		t.Fatalf("read after dead link: %v", err)
	}
}

// With the head cached, a health check still proves the link over the
// network instead of passing on cached bytes.
func TestEngineCheckProbesLinkWhenHeadCached(t *testing.T) {
	r := newEngineRig(t, 256<<10, -1)
	ctx := context.Background()
	if ok, err := r.eng.Check(ctx, r.pl); !ok || err != nil {
		t.Fatalf("first check: %v %v", ok, err)
	}
	before := r.media.requests.Load()
	if ok, err := r.eng.Check(ctx, r.pl); !ok || err != nil {
		t.Fatalf("second check: %v %v", ok, err)
	}
	if r.media.requests.Load() == before {
		t.Fatal("cached check made no request")
	}
	r.media.mu.Lock()
	r.media.size = 999 << 20 // the link now serves another file
	r.media.mu.Unlock()
	if ok, err := r.eng.Check(ctx, r.pl); ok || err == nil {
		t.Fatal("check passed on cached bytes for a link serving another file")
	}
}
