package plex

import (
	"bytes"
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/transcode"
)

type fakeTrickplay struct {
	bif       []byte
	frame     []byte
	available bool
}

func (f *fakeTrickplay) EnsureTrickplay(context.Context, core.TrickplaySource) {}
func (f *fakeTrickplay) TrickplayAvailable(string) bool                        { return f.available }
func (f *fakeTrickplay) TrickplayBIF(string) ([]byte, bool) {
	return append([]byte(nil), f.bif...), len(f.bif) > 0
}
func (f *fakeTrickplay) TrickplayFrame(string, time.Duration) ([]byte, bool) {
	return append([]byte(nil), f.frame...), len(f.frame) > 0
}
func (f *fakeTrickplay) SetTrickplayFallback(string, []byte) {}

func addIndexSession(f *fixture, key core.RatingKey) {
	f.playback.mu.Lock()
	defer f.playback.mu.Unlock()
	f.playback.sessions["index-session"] = &core.PlaySession{
		ID: "index-session", UserID: ownerUser.ID, ClientID: "client-1", Key: key,
		Resolution: &core.Resolution{Release: core.Release{ID: "release-1"}},
		StartedAt:  fixedNow,
	}
}

func TestIndexRoutesPrecedePartRouteAndServeBIFAndFrame(t *testing.T) {
	f := newFixture()
	defer f.srv.Close()
	frame := []byte{0xff, 0xd8, 0xff, 0xd9}
	bif, err := transcode.MarshalBIF([]transcode.BIFFrame{
		{Time: 0, JPEG: frame}, {Time: 10 * time.Second, JPEG: frame},
		{Time: 20 * time.Second, JPEG: frame}, {Time: 30 * time.Second, JPEG: frame},
	})
	if err != nil {
		t.Fatal(err)
	}
	provider := &fakeTrickplay{bif: bif, frame: frame}
	f.srv.d.Trickplay = provider
	addIndexSession(f, fightClub.Key)

	// This path would otherwise be consumed as /library/parts/{part}/{file}.
	rec := f.get(t, "/library/parts/"+fightClub.Key.String()+"/indexes/sd")
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/octet-stream" || !bytes.Equal(rec.Body.Bytes(), provider.bif) {
		t.Fatalf("BIF response: status=%d content-type=%q body=%q", rec.Code, rec.Header().Get("Content-Type"), rec.Body.Bytes())
	}
	variantPart := f.srv.variantIDs.idFor(fightClub.Key, "release-1")
	rec = f.get(t, "/library/parts/"+strconv.FormatInt(variantPart, 10)+"/indexes/sd")
	if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), provider.bif) {
		t.Fatalf("variant BIF response: status=%d body=%q", rec.Code, rec.Body.Bytes())
	}
	rec = f.request(t, http.MethodGet, "/library/parts/"+fightClub.Key.String()+"/indexes/sd", map[string]string{"Range": "bytes=0-2"})
	if rec.Code != http.StatusPartialContent || !bytes.Equal(rec.Body.Bytes(), provider.bif[:3]) {
		t.Fatalf("BIF range: status=%d body=%q", rec.Code, rec.Body.Bytes())
	}
	rec = f.get(t, "/library/parts/"+fightClub.Key.String()+"/indexes/sd?interval=20000")
	downsampled, err := transcode.ParseBIF(rec.Body.Bytes())
	if rec.Code != http.StatusOK || err != nil || downsampled.Len() != 2 || downsampled.Time(1) != 20*time.Second {
		t.Fatalf("interval response: status=%d BIF=%v err=%v", rec.Code, downsampled, err)
	}
	rec = f.get(t, "/library/parts/"+fightClub.Key.String()+"/indexes/sd/10000")
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/jpeg" || !bytes.Equal(rec.Body.Bytes(), frame) {
		t.Fatalf("frame response: status=%d content-type=%q body=%v", rec.Code, rec.Header().Get("Content-Type"), rec.Body.Bytes())
	}
	if rec = f.get(t, "/library/parts/9999999999999999/indexes/sd"); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown part status=%d", rec.Code)
	}
}

func TestPartIndexesAttributeOnlyWhenAvailable(t *testing.T) {
	f := newFixture()
	defer f.srv.Close()
	provider := &fakeTrickplay{available: false}
	f.srv.d.Trickplay = provider
	addIndexSession(f, fightClub.Key)
	path := "/library/metadata/" + fightClub.Key.String()
	if rec := f.get(t, path); rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), `indexes="sd"`) {
		t.Fatalf("unexpected indexes attribute: status=%d body=%s", rec.Code, rec.Body.String())
	}
	provider.available = true
	if rec := f.get(t, path); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `indexes="sd"`) {
		t.Fatalf("missing indexes attribute: status=%d body=%s", rec.Code, rec.Body.String())
	}
}
