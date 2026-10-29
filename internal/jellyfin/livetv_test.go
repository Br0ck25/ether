package jellyfin

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/livetv"
)

// fakeLive is one virtual channel with a programme on now and one next.
type fakeLive struct {
	now     time.Time
	tunes   int
	stopped []string
	live    map[string]bool
}

func (f *fakeLive) channel() livetv.Channel {
	return livetv.Channel{ID: "chan1", Number: "7", Name: "Action", Kind: "virtual", Enabled: true}
}

func (f *fakeLive) AllowedChannels(context.Context, *core.User) ([]livetv.Channel, error) {
	iptv := livetv.Channel{ID: "ip1", Number: "8", Name: "IPTV", Kind: "iptv", Enabled: true}
	return []livetv.Channel{iptv, f.channel()}, nil
}

func (f *fakeLive) progs() []livetv.Programme {
	mk := func(start time.Time, title string, key core.RatingKey) livetv.Programme {
		return livetv.Programme{ID: fmt.Sprintf("chan1-%d", start.UnixMilli()), ChannelID: "chan1", Start: start, End: start.Add(time.Hour),
			Item: livetv.Item{Key: key, Title: title, Summary: "about " + title}}
	}
	a := f.now.Add(-30 * time.Minute)
	return []livetv.Programme{mk(a, "Now Showing", core.MovieKey(550)), mk(a.Add(time.Hour), "Up Next", core.EpisodeKey(7, 1, 2))}
}

func (f *fakeLive) Guide(_ context.Context, _ *core.User, from, until time.Time) ([]livetv.Programme, error) {
	var out []livetv.Programme
	for _, p := range f.progs() {
		if p.End.After(from) && p.Start.Before(until) {
			out = append(out, p)
		}
	}
	return out, nil
}

func (f *fakeLive) TuneWithSession(_ context.Context, _ *core.User, ch string, _ core.ClientCaps, _ string) (livetv.SessionInfo, error) {
	f.tunes++
	if f.live == nil {
		f.live = map[string]bool{}
	}
	id := fmt.Sprintf("sess%d", f.tunes)
	f.live[id] = true
	return livetv.SessionInfo{ID: id}, nil
}

func (f *fakeLive) Session(_ context.Context, id string, _ *core.User) (livetv.SessionInfo, error) {
	if !f.live[id] {
		return livetv.SessionInfo{}, core.ErrNotFound
	}
	return livetv.SessionInfo{ID: id}, nil
}

func (f *fakeLive) ServePlaylist(w http.ResponseWriter, _ *http.Request, id string, _ *core.User) {
	fmt.Fprintf(w, "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:6\n#EXT-X-MEDIA-SEQUENCE:3\n#EXTINF:6.000,\nprogram/1000/3.ts\n")
}

func (f *fakeLive) ServeSegment(w http.ResponseWriter, _ *http.Request, id string, start int64, n int, _ *core.User) {
	fmt.Fprintf(w, "seg %s %d %d", id, start, n)
}

func (f *fakeLive) Stop(_ context.Context, id string, _ *core.User) error {
	f.stopped = append(f.stopped, id)
	delete(f.live, id)
	return nil
}

func TestLiveTVChannelsGuideAndPlayback(t *testing.T) {
	f := newFixture(t)
	live := &fakeLive{now: f.now}
	tok := f.login(ownerUser)

	// Without Live TV the apps are told it is off.
	if info := decode[map[string]any](t, f.call("GET", "/LiveTv/Info", tok, nil)); info["IsEnabled"] != false {
		t.Fatalf("live tv enabled without a service: %v", info)
	}
	f.srv.d.LiveTV = live
	if info := decode[map[string]any](t, f.call("GET", "/LiveTv/Info", tok, nil)); info["IsEnabled"] != true {
		t.Fatalf("live tv not enabled: %v", info)
	}

	// Only virtual channels are offered, each with what is on now.
	chans := decode[itemsResult](t, f.call("GET", "/LiveTv/Channels?AddCurrentProgram=true", tok, nil))
	if len(chans.Items) != 1 || chans.Items[0].Name != "Action" || chans.Items[0].Type != "TvChannel" || chans.Items[0].ChannelNumber != "7" {
		t.Fatalf("channels: %+v", chans.Items)
	}
	if cp := chans.Items[0].CurrentProgram; cp == nil || cp.Name != "Now Showing" || cp.ChannelID != chans.Items[0].ID {
		t.Fatalf("current programme: %+v", cp)
	}
	chID := chans.Items[0].ID

	// The guide, by GET and by POST, for that channel.
	body := map[string]any{"ChannelIds": []string{chID}, "MinEndDate": f.now.Format(time.RFC3339), "MaxStartDate": f.now.Add(3 * time.Hour).Format(time.RFC3339)}
	for _, w := range []*httptest.ResponseRecorder{
		f.call("POST", "/LiveTv/Programs", tok, body),
		f.call("GET", "/LiveTv/Programs?ChannelIds="+chID+"&MinEndDate="+f.now.Format(time.RFC3339)+"&MaxStartDate="+f.now.Add(3*time.Hour).Format(time.RFC3339), tok, nil),
	} {
		ps := decode[itemsResult](t, w)
		if len(ps.Items) != 2 || ps.Items[0].Name != "Now Showing" || ps.Items[1].Type != "Program" || !ps.Items[0].IsMovie || !ps.Items[1].IsSeries {
			t.Fatalf("programmes: %+v", ps.Items)
		}
	}
	if rec := decode[itemsResult](t, f.call("GET", "/LiveTv/Programs/Recommended?Limit=5", tok, nil)); len(rec.Items) != 1 || rec.Items[0].Name != "Now Showing" {
		t.Fatalf("recommended: %+v", rec.Items)
	}
	// The channel and its programme open as ordinary items.
	if it := decode[item](t, f.call("GET", "/Users/"+userID(ownerUser.ID)+"/Items/"+chID, tok, nil)); it.Type != "TvChannel" {
		t.Fatalf("channel item: %+v", it)
	}

	// Playing the channel: PlaybackInfo gives its HLS stream, with the token.
	w := f.call("POST", "/Items/"+chID+"/PlaybackInfo", tok, map[string]any{"DeviceProfile": webProfile()})
	pi := decode[playbackInfo](t, w)
	if len(pi.MediaSources) != 1 {
		t.Fatalf("playback info: %d %s", w.Code, w.Body)
	}
	src := pi.MediaSources[0]
	if !src.SupportsTranscoding || src.SupportsDirectPlay || src.TranscodingSubProtocol != "hls" || !strings.Contains(src.TranscodingURL, "/livetv/channels/"+chID+"/master.m3u8") || !strings.Contains(src.TranscodingURL, "api_key="+tok) {
		t.Fatalf("live source: %+v", src)
	}
	pl := f.call("GET", src.TranscodingURL, "", nil)
	if pl.Code != 200 || !strings.Contains(pl.Body.String(), "program/1000/3.ts?api_key="+tok) {
		t.Fatalf("playlist: %d %s", pl.Code, pl.Body)
	}
	seg := f.call("GET", "/livetv/channels/"+chID+"/program/1000/3.ts?api_key="+tok, "", nil)
	if seg.Code != 200 || seg.Body.String() != "seg sess1 1000 3" {
		t.Fatalf("segment: %d %q", seg.Code, seg.Body)
	}
	// Refreshing the playlist reuses the tuned channel.
	f.call("GET", src.TranscodingURL, "", nil)
	if live.tunes != 1 {
		t.Fatalf("tuned %d times, want 1", live.tunes)
	}
	// A channel nobody fetches is released.
	f.now = f.now.Add(liveIdle + time.Minute)
	f.srv.Sweep()
	if len(live.stopped) != 1 || live.stopped[0] != "sess1" {
		t.Fatalf("idle channel not released: %v", live.stopped)
	}
	// Unknown ids and signed-out requests are refused.
	if f.call("POST", "/Items/00000000000000000000000000000abc/PlaybackInfo", tok, nil).Code == http.StatusOK {
		t.Fatal("playback info for an unknown id")
	}
	if f.call("GET", "/LiveTv/Channels", "", nil).Code != http.StatusUnauthorized {
		t.Fatal("channels served without signing in")
	}
}
