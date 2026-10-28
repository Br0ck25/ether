package jellyfin

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

type playbackInfo struct {
	MediaSources  []mediaSource
	PlaySessionID string `json:"PlaySessionId"`
	ErrorCode     string
}

// webProfile is what a browser-like app declares: MKV, H.264, E-AC-3 and AAC.
func webProfile() map[string]any {
	return map[string]any{
		"MaxStreamingBitrate": 20_000_000,
		"DirectPlayProfiles": []map[string]string{
			{"Type": "Video", "Container": "mkv,mp4", "VideoCodec": "h264,hevc", "AudioCodec": "aac,eac3"},
		},
		"TranscodingProfiles": []map[string]string{
			{"Type": "Video", "Container": "ts", "Protocol": "hls", "VideoCodec": "h264", "AudioCodec": "aac"},
		},
		"CodecProfiles": []map[string]any{
			{"Type": "Video", "Codec": "h264", "Conditions": []map[string]string{{"Condition": "LessThanEqual", "Property": "Height", "Value": "1080"}}},
		},
		"SubtitleProfiles": []map[string]string{{"Format": "srt", "Method": "External"}},
	}
}

func (f *fixture) playbackInfo(tok string, key core.RatingKey, body map[string]any) (playbackInfo, int) {
	f.t.Helper()
	w := f.call("POST", "/Items/"+itemID(key)+"/PlaybackInfo?UserId="+userID(ownerUser.ID), tok, body)
	if w.Code != 200 {
		return playbackInfo{}, w.Code
	}
	return decode[playbackInfo](f.t, w), w.Code
}

func TestDirectPlay(t *testing.T) {
	f := newFixture(t)
	tok := f.login(ownerUser)
	pi, code := f.playbackInfo(tok, fightClub.Key, map[string]any{"DeviceProfile": webProfile()})
	if code != 200 || len(pi.MediaSources) != 1 || pi.PlaySessionID == "" {
		t.Fatalf("playback info: %d %+v", code, pi)
	}
	src := pi.MediaSources[0]
	if !src.SupportsDirectPlay || src.TranscodingURL != "" || src.Container != "mkv" || src.ID != itemID(fightClub.Key) {
		t.Fatalf("direct play source: %+v", src)
	}
	// The decision saw what the app declared, not our defaults.
	caps := f.trans.lastCaps
	if strings.Join(caps.Containers, ",") != "mkv,mp4" || strings.Join(caps.AudioCodecs, ",") != "aac,eac3" || caps.MaxHeight != 1080 || caps.MaxBitrateKbps != 20000 || caps.ClientID != "dev-1" {
		t.Fatalf("client capabilities: %+v", caps)
	}

	// The stream URL serves the bytes with range support, with only the
	// token in the URL (players follow it without our headers).
	// Jellyfin apps build the stream URL themselves; Emby's read DirectStreamUrl.
	if src.DirectStreamURL != "" {
		t.Fatalf("Jellyfin source carries Emby's DirectStreamUrl: %q", src.DirectStreamURL)
	}
	w0 := f.call("POST", "/emby/Items/"+itemID(fightClub.Key)+"/PlaybackInfo", tok, map[string]any{"DeviceProfile": webProfile()})
	embySrc := decode[playbackInfo](t, w0).MediaSources[0]
	u, err := url.Parse(embySrc.DirectStreamURL)
	if err != nil || u.Query().Get("api_key") != tok {
		t.Fatalf("emby direct stream url: %q", embySrc.DirectStreamURL)
	}
	r := httptest.NewRequest("GET", embySrc.DirectStreamURL, nil)
	r.Header.Set("Range", "bytes=10-19")
	r.RemoteAddr = "192.168.1.20:5555"
	w := httptest.NewRecorder()
	f.srv.ServeHTTP(w, r)
	if w.Code != http.StatusPartialContent || w.Body.String() != "0123456789" {
		t.Fatalf("range read: %d %q", w.Code, w.Body)
	}
	if f.playback.prepared != 2 { // one per PlaybackInfo; streaming reuses the session
		t.Fatalf("the stream request re-resolved the title (%d prepares)", f.playback.prepared)
	}
	// Apps also ask for stream.mkv, Emby's original.mkv and the download link.
	for _, p := range []string{"/Videos/%s/stream.mkv", "/emby/Videos/%s/original.mkv", "/Items/%s/Download"} {
		w := f.call("GET", fmt.Sprintf(p, itemID(fightClub.Key))+"?api_key="+tok, "", nil)
		if w.Code != 200 || w.Body.Len() != 10000 {
			t.Errorf("%s: %d (%d bytes)", p, w.Code, w.Body.Len())
		}
	}
}

func TestTranscodeUsesHLSWithTokenInEveryURL(t *testing.T) {
	f := newFixture(t)
	f.trans.decision = core.Decision{Kind: core.DecisionTranscode, VideoCodec: "h264", AudioCodec: "aac", Container: "mpegts", MaxHeight: 720}
	tok := f.login(ownerUser)
	pi, _ := f.playbackInfo(tok, fightClub.Key, map[string]any{"DeviceProfile": webProfile(), "AudioStreamIndex": 2})
	src := pi.MediaSources[0]
	if src.SupportsDirectPlay || !src.SupportsTranscoding || src.TranscodingSubProtocol != "hls" || src.TranscodingContainer != "ts" {
		t.Fatalf("transcode source: %+v", src)
	}
	if src.DefaultAudioStreamIndex != 2 || f.trans.lastOpts.AudioStream != 2 {
		t.Fatalf("the chosen audio track was lost: %d / %d", src.DefaultAudioStreamIndex, f.trans.lastOpts.AudioStream)
	}
	if !strings.HasPrefix(src.TranscodingURL, "/videos/"+itemID(fightClub.Key)+"/master.m3u8?") || !strings.Contains(src.TranscodingURL, "PlaySessionId="+pi.PlaySessionID) {
		t.Fatalf("transcoding url: %s", src.TranscodingURL)
	}

	w := f.call("GET", src.TranscodingURL, "", nil)
	if w.Code != 200 || w.Header().Get("Content-Type") != "application/vnd.apple.mpegurl" {
		t.Fatalf("master: %d %s", w.Code, w.Body)
	}
	var variant string
	for _, line := range strings.Split(w.Body.String(), "\n") {
		if strings.HasPrefix(line, "hls/") {
			variant = line
		}
	}
	if !strings.HasPrefix(variant, "hls/"+pi.PlaySessionID+"/base/index.m3u8?api_key="+tok) {
		t.Fatalf("variant uri must carry the token: %q in %s", variant, w.Body)
	}
	if req, ok := f.trans.sessions[pi.PlaySessionID]; !ok || req.req.Decision.Kind != core.DecisionTranscode || req.req.Info.Container != "mkv" {
		t.Fatalf("transcoder session: %+v", f.trans.sessions)
	}

	// The media playlist and its segments are fetched without our headers.
	base := "/videos/" + itemID(fightClub.Key) + "/"
	pl := f.call("GET", base+variant, "", nil)
	if pl.Code != 200 || !strings.Contains(pl.Body.String(), "00000.ts?api_key="+tok) {
		t.Fatalf("media playlist: %d %s", pl.Code, pl.Body)
	}
	seg := f.call("GET", base+"hls/"+pi.PlaySessionID+"/base/00007.ts?api_key="+tok, "", nil)
	if seg.Code != 200 || seg.Body.String() != "segment-7" {
		t.Fatalf("segment: %d %q", seg.Code, seg.Body)
	}
	if bad := f.call("GET", base+"hls/"+pi.PlaySessionID+"/base/00007.ts", "", nil); bad.Code != http.StatusUnauthorized {
		t.Fatalf("segment without a token: %d", bad.Code)
	}

	// Another viewer cannot read this viewer's stream even with a token of their own.
	friend := f.login(friendUser)
	if other := f.call("GET", base+"hls/"+pi.PlaySessionID+"/base/00007.ts", friend, nil); other.Code != http.StatusNotFound {
		t.Fatalf("another viewer's stream: %d", other.Code)
	}

	// A seek makes the app ask for a new master at the new position: the
	// encoder restarts there instead of serving the old run.
	seek := src.TranscodingURL + "&StartTimeTicks=" + "6000000000"
	f.call("GET", seek, "", nil)
	if got := f.trans.sessions[pi.PlaySessionID].req.StartOffset; got != 10*time.Minute {
		t.Fatalf("seek restarted at %v", got)
	}
	if len(f.trans.stopped) == 0 || f.trans.stopped[0] != pi.PlaySessionID {
		t.Fatalf("the old encoder kept running: %v", f.trans.stopped)
	}
}

func TestPictureSubtitleIsBurnedInWhenTranscoding(t *testing.T) {
	f := newFixture(t)
	f.playback.info.Streams = append(append([]core.StreamInfo(nil), f.playback.info.Streams...),
		core.StreamInfo{Index: 4, Type: "subtitle", Codec: "hdmv_pgs_subtitle", Language: "en"})
	f.trans.decision = core.Decision{Kind: core.DecisionTranscode, VideoCodec: "h264", AudioCodec: "aac", Container: "mpegts"}
	tok := f.login(ownerUser)
	deliveries := func(pi playbackInfo) map[int]string {
		out := map[int]string{}
		for _, st := range pi.MediaSources[0].MediaStreams {
			if st.Type == "Subtitle" {
				out[st.Index] = st.DeliveryMethod
			}
		}
		return out
	}

	pi, _ := f.playbackInfo(tok, fightClub.Key, map[string]any{"DeviceProfile": webProfile(), "SubtitleStreamIndex": 4})
	if !f.trans.lastOpts.BurnSubtitles || f.trans.lastOpts.SubtitleStream != 4 {
		t.Fatalf("subtitle choice not applied: %+v", f.trans.lastOpts)
	}
	if got := deliveries(pi); got[4] != "Encode" || got[3] != "" {
		t.Fatalf("delivery while transcoding: %v", got)
	}
	// A text track inside the video is not burned: it would need the whole
	// remote file read, and Ether never does that for a subtitle.
	f.playbackInfo(tok, fightClub.Key, map[string]any{"DeviceProfile": webProfile(), "SubtitleStreamIndex": 3})
	if f.trans.lastOpts.BurnSubtitles {
		t.Fatal("a text subtitle was burned in")
	}

	// On direct play the app shows either track itself.
	f.trans.decision = core.Decision{Kind: core.DecisionDirectPlay, VideoCopy: true, AudioCopy: true, Container: "mkv"}
	pi, _ = f.playbackInfo(tok, fightClub.Key, map[string]any{"DeviceProfile": webProfile(), "SubtitleStreamIndex": 4})
	if f.trans.lastOpts.BurnSubtitles {
		t.Fatal("a direct-played title had its subtitle burned in")
	}
	if got := deliveries(pi); got[3] != "Embed" || got[4] != "Embed" {
		t.Fatalf("delivery on direct play: %v", got)
	}
}

func TestPlaybackFailuresAreReportedTheWayAppsExpect(t *testing.T) {
	f := newFixture(t)
	tok := f.login(ownerUser)
	for err, want := range map[error]string{
		core.ErrNotAvailable: "NoCompatibleStream",
		core.ErrStreamLimit:  "RateLimitExceeded",
		core.ErrUnauthorized: "NotAllowed",
	} {
		f.playback.failures[fightClub.Key] = err
		pi, code := f.playbackInfo(tok, fightClub.Key, map[string]any{"DeviceProfile": webProfile()})
		if code != 200 || pi.ErrorCode != want || len(pi.MediaSources) != 0 {
			t.Errorf("%v: %d %+v", err, code, pi)
		}
	}
	// Transcoding switched off with only a transcode possible is no stream at all.
	delete(f.playback.failures, fightClub.Key)
	f.trans.decision = core.Decision{Kind: core.DecisionTranscode, Container: "mpegts"}
	pi, _ := f.playbackInfo(tok, fightClub.Key, map[string]any{"DeviceProfile": webProfile(), "EnableTranscoding": false})
	if pi.ErrorCode != "NoCompatibleStream" {
		t.Fatalf("transcoding disabled: %+v", pi)
	}
	if len(f.playback.sessions) != 0 {
		t.Fatalf("a refused playback left its source open: %v", f.playback.stopped)
	}
}

func TestDetailPageDoesNotResolveARelease(t *testing.T) {
	f := newFixture(t)
	tok := f.login(ownerUser)
	f.call("GET", "/Items/"+itemID(fightClub.Key), tok, nil)
	pi, _ := f.playbackInfo(tok, fightClub.Key, map[string]any{"DeviceProfile": webProfile(), "IsPlayback": false})
	if f.playback.prepared != 0 {
		t.Fatalf("browsing resolved %d releases", f.playback.prepared)
	}
	if len(pi.MediaSources) != 1 || pi.PlaySessionID != "" || len(pi.MediaSources[0].MediaStreams) == 0 {
		t.Fatalf("estimate: %+v", pi)
	}
}

func TestEpisodesThatHaveNotAiredCannotBePlayed(t *testing.T) {
	f := newFixture(t)
	tok := f.login(ownerUser)
	// Unaired titles are not special-cased by the catalogue's Get, so the
	// listing is what hides them; playing one directly must still resolve
	// through the normal path and fail there when nothing exists.
	f.playback.failures[bbE3.Key] = core.ErrNotAvailable
	pi, _ := f.playbackInfo(tok, bbE3.Key, map[string]any{"DeviceProfile": webProfile()})
	if pi.ErrorCode == "" || len(pi.MediaSources) != 0 {
		t.Fatalf("unaired episode: %+v", pi)
	}
}

func TestPlayingStoppedReleasesTheSource(t *testing.T) {
	f := newFixture(t)
	f.trans.decision = core.Decision{Kind: core.DecisionTranscode, VideoCodec: "h264", AudioCodec: "aac", Container: "mpegts"}
	tok := f.login(ownerUser)
	pi, _ := f.playbackInfo(tok, fightClub.Key, map[string]any{"DeviceProfile": webProfile()})
	f.call("GET", pi.MediaSources[0].TranscodingURL, "", nil)
	body := map[string]any{"ItemId": itemID(fightClub.Key), "PlaySessionId": pi.PlaySessionID, "PositionTicks": 90 * ticksPerSecond}
	if w := f.call("POST", "/Sessions/Playing", tok, body); w.Code != http.StatusNoContent {
		t.Fatalf("start: %d", w.Code)
	}
	if len(f.playback.touched) == 0 {
		t.Fatal("playing did not keep the source alive")
	}
	if w := f.call("POST", "/Sessions/Playing/Stopped", tok, body); w.Code != http.StatusNoContent {
		t.Fatalf("stop: %d", w.Code)
	}
	last := f.state.progress[len(f.state.progress)-1]
	if last.state != "stopped" || last.offset != 90*time.Second {
		t.Fatalf("final progress: %+v", last)
	}
	if len(f.playback.stopped) != 1 || len(f.trans.stopped) != 1 || f.trans.stopped[0] != pi.PlaySessionID {
		t.Fatalf("source or encoder left running: playback %v transcoder %v", f.playback.stopped, f.trans.stopped)
	}
	if _, ok := f.trans.Session(pi.PlaySessionID); ok {
		t.Fatal("encoder session still registered")
	}
}

func TestIdlePlaysAreSwept(t *testing.T) {
	f := newFixture(t)
	tok := f.login(ownerUser)
	f.playbackInfo(tok, fightClub.Key, map[string]any{"DeviceProfile": webProfile()})
	f.srv.Sweep()
	if len(f.playback.stopped) != 0 {
		t.Fatal("a fresh play was swept")
	}
	f.now = f.now.Add(idlePlay + time.Minute)
	f.srv.Sweep()
	if len(f.playback.stopped) != 1 {
		t.Fatalf("a quiet play was kept: %v", f.playback.stopped)
	}
}

func TestProfileCaps(t *testing.T) {
	sess := &session{DeviceID: "d", Device: "TV", Client: "Findroid"}
	c := capsFor(sess, nil, 0)
	if c.MaxHeight != 2160 || !c.SupportsVideo("hevc") || c.Product != "Findroid" {
		t.Fatalf("defaults: %+v", c)
	}
	prof := &deviceProfile{MaxStreamingBitrate: 8_000_000}
	prof.DirectPlayProfiles = append(prof.DirectPlayProfiles, struct {
		Container  string `json:"Container"`
		AudioCodec string `json:"AudioCodec"`
		VideoCodec string `json:"VideoCodec"`
		Type       string `json:"Type"`
	}{Container: "matroska,ts", VideoCodec: "H264", Type: "Video"})
	c = capsFor(sess, prof, 0)
	if strings.Join(c.Containers, ",") != "mkv,mpegts" || strings.Join(c.VideoCodecs, ",") != "h264" || c.MaxBitrateKbps != 8000 {
		t.Fatalf("profile: %+v", c)
	}
	// A profile that lists no audio codecs does not strip all audio.
	if len(c.AudioCodecs) == 0 {
		t.Fatal("audio codecs emptied")
	}
	if c.HDR10 {
		t.Fatal("HDR claimed without HEVC")
	}
}

func TestAccessCodesFromTheOwnerAPI(t *testing.T) {
	f := newFixture(t)
	admin := f.srv.AdminHandler()
	do := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		w := httptest.NewRecorder()
		admin.ServeHTTP(w, r)
		return w
	}
	w := do("PUT", "/viewers/2/code", `{}`)
	code := decode[map[string]string](t, w)["code"]
	if w.Code != 200 || len(code) != generatedLen || strings.ContainsAny(code, "01OIL") {
		t.Fatalf("generated code: %d %q", w.Code, code)
	}
	// The generated code signs that viewer in, and only that viewer.
	signin := func(user, pw string) int {
		return f.call("POST", "/Users/AuthenticateByName", "", map[string]string{"Username": user, "Pw": pw}).Code
	}
	if signin("friend", code) != 200 || signin("owner", code) != 401 {
		t.Fatal("generated code does not behave as a per-viewer sign-in")
	}
	list := decode[struct {
		Viewers []struct {
			ID      int64
			HasCode bool
			Devices int
		}
	}](t, do("GET", "/viewers", ""))
	got := map[int64][2]int{}
	for _, v := range list.Viewers {
		n := 0
		if v.HasCode {
			n = 1
		}
		got[v.ID] = [2]int{n, v.Devices}
	}
	if got[2] != [2]int{1, 1} || got[1] != [2]int{0, 0} {
		t.Fatalf("viewer list: %v", got)
	}
	if do("POST", "/viewers/2/signout", "").Code != 204 {
		t.Fatal("signout")
	}
	if d, _ := f.store.Devices(context.Background()); d[2] != 0 {
		t.Fatalf("devices after sign-out: %v", d)
	}
	if do("DELETE", "/viewers/2/code", "").Code != 204 || signin("friend", code) != 401 {
		t.Fatal("a removed code still signs in")
	}
	if do("PUT", "/viewers/99/code", `{}`).Code != 404 {
		t.Fatal("a code was created for a viewer that does not exist")
	}
	if do("PUT", "/viewers/2/code", `{"code":"abc"}`).Code != 400 {
		t.Fatal("a weak chosen code was accepted")
	}
}
