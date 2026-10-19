package plex

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
	"github.com/braedonsaunders/ether/internal/livetv"
	"github.com/braedonsaunders/ether/internal/store"
	"github.com/braedonsaunders/ether/internal/transcode"
)

func liveFixture(t *testing.T, transcoders ...core.Transcoder) (*fixture, *livetv.Service, livetv.Channel) {
	t.Helper()
	f := newFixture()
	t.Cleanup(func() { f.srv.Close() })
	db, e := store.OpenMemory(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.Close() })
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		fmt.Fprint(w, "#EXTM3U\n#EXT-X-TARGETDURATION:4\n#EXTINF:4,\nsegment.ts\n")
	}))
	t.Cleanup(up.Close)
	var tc core.Transcoder
	if len(transcoders) > 0 {
		tc = transcoders[0]
	}
	service, e := livetv.New(livetv.Options{Transcoder: tc, DB: db, Now: func() time.Time { return fixedNow }, HTTPClient: up.Client()})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { service.Close() })
	src := livetv.Source{Name: "Owner IPTV", Playlist: fmt.Sprintf("#EXTM3U\n#EXTINF:-1 tvg-id=\"owner\" tvg-chno=\"7\",Owner channel\n%s/master.m3u8\n", up.URL), Guide: `<tv><channel id="owner"><display-name>Owner channel</display-name></channel><programme channel="owner" start="20261018110000 +0000" stop="20261018130000 +0000"><title>Owner programme</title><desc>Owner-supplied guide</desc><rating><value>PG</value></rating></programme></tv>`}
	if _, e = service.ImportSource(context.Background(), src); e != nil {
		t.Fatal(e)
	}
	cs, e := service.Channels(context.Background())
	if e != nil || len(cs) != 1 {
		t.Fatalf("channels: %v %+v", e, cs)
	}
	f.srv.d.LiveTV = service
	f.srv.d.Identity.MyPlexSubscription = true
	return f, service, cs[0]
}
func TestLiveTVDVRGuideProviderAndTuneShapes(t *testing.T) {
	f, _, channel := liveFixture(t)
	// Official PMS contract uses a Dvr list, Device children, Channel grid keys,
	// broadcast bounds on Media, and Media.uuid on the tune metadata.
	dvr := f.request(t, "GET", "/livetv/dvrs", map[string]string{"Accept": "application/json"})
	if dvr.Code != 200 {
		t.Fatalf("DVR: %d %s", dvr.Code, dvr.Body.String())
	}
	mc := decodeJSON(t, dvr)
	dvrs, ok := mc["Dvr"].([]any)
	if !ok || len(dvrs) != 1 {
		t.Fatalf("Dvr shape: %s", dvr.Body.String())
	}
	dv := dvrs[0].(map[string]any)
	if dv["key"] != "1" || dv["lineup"] == nil || dv["Device"] == nil {
		t.Fatalf("DVR fields %s", dvr.Body.String())
	}
	channels := f.request(t, "GET", liveProviderPath+"/lineups/dvr/channels", map[string]string{"Accept": "application/json"})
	cm := decodeJSON(t, channels)
	chs := cm["Channel"].([]any)
	ch := chs[0].(map[string]any)
	if ch["id"] != channel.ID || ch["vcn"] != "7" || ch["gridKey"] != channel.ID {
		t.Fatalf("channel shape: %s", channels.Body.String())
	}
	if strings.Contains(channels.Body.String(), "master.m3u8") {
		t.Fatal("owner upstream URL exposed")
	}
	grid := f.request(t, "GET", liveProviderPath+"/grid?date=2026-10-18&channelGridKey="+channel.ID, map[string]string{"Accept": "application/json"})
	gm := decodeJSON(t, grid)
	programmes := gm["Metadata"].([]any)
	p := programmes[0].(map[string]any)
	media := p["Media"].([]any)[0].(map[string]any)
	if p["title"] != "Owner programme" || media["beginsAt"] != float64(time.Date(2026, 10, 18, 11, 0, 0, 0, time.UTC).Unix()) || media["channelIdentifier"] != channel.ID || media["protocol"] != "livetv" {
		t.Fatalf("EPG shape: %s", grid.Body.String())
	}
	providers := f.request(t, "GET", "/media/providers", map[string]string{"Accept": "application/json"})
	if !strings.Contains(providers.Body.String(), livetv.Provider) {
		t.Fatal("Live TV provider undiscoverable")
	}
	home := f.request(t, "GET", "/hubs", map[string]string{"Accept": "application/json"})
	if !strings.Contains(home.Body.String(), "livetv.onnow") {
		t.Fatalf("On Now row absent: %s", home.Body.String())
	}
	tune := f.request(t, "POST", "/livetv/dvrs/1/channels/7/tune", map[string]string{"Accept": "application/json"})
	if tune.Code != 200 {
		t.Fatalf("tune %d %s", tune.Code, tune.Body.String())
	}
	tm := decodeJSON(t, tune)
	subscriptions, ok := tm["MediaSubscription"].([]any)
	if !ok || len(subscriptions) != 1 || tune.Header().Get("X-Plex-Activity") == "" {
		t.Fatalf("Plex Web tune needs subscription + activity: %s", tune.Body.String())
	}
	subscription := subscriptions[0].(map[string]any)
	operation, ok := subscription["MediaGrabOperation"].(map[string]any)
	if !ok || operation["mediaIndex"] != float64(0) || operation["Metadata"] == nil || subscription["Timeline"] == nil {
		t.Fatalf("Plex Web grab/timeline shape: %s", tune.Body.String())
	}
	metadata := tm["Metadata"].([]any)[0].(map[string]any)
	if metadata["live"] != true || metadata["genuineMediaAnalysis"] != false {
		t.Fatalf("tune flags %s", tune.Body.String())
	}
	sessionPath := metadata["key"].(string)
	sessionID := strings.TrimPrefix(sessionPath, "/livetv/sessions/")
	subget := f.request(t, "GET", "/media/subscriptions/"+sessionID, map[string]string{"Accept": "application/json"})
	if subget.Code != 200 || !strings.Contains(subget.Body.String(), "MediaGrabOperation") {
		t.Fatalf("subscription polling %d %s", subget.Code, subget.Body.String())
	}
	tuneMedia := metadata["Media"].([]any)[0].(map[string]any)
	if tuneMedia["uuid"] != sessionID {
		t.Fatal("session UUID absent")
	}
	part := tuneMedia["Part"].([]any)[0].(map[string]any)
	if part["key"] != sessionPath+"/0/index.m3u8" {
		t.Fatal("incorrect consumer playlist key")
	}
	decision := f.request(t, "GET", "/video/:/transcode/universal/decision?path="+sessionPath, map[string]string{"Accept": "application/json"})
	if decision.Code != 200 || !strings.Contains(decision.Body.String(), "generalDecisionCode") {
		t.Fatalf("live decision %d %s", decision.Code, decision.Body.String())
	}
	start := f.request(t, "GET", "/video/:/transcode/universal/start.m3u8?path="+sessionPath, nil)
	if start.Code != 307 || !strings.Contains(start.Header().Get("Location"), sessionID+"/0/index.m3u8") {
		t.Fatalf("live start %d %s", start.Code, start.Header().Get("Location"))
	}
	playlist := f.request(t, "GET", sessionPath+"/0/index.m3u8", nil)
	if playlist.Code != 200 || !strings.Contains(playlist.Body.String(), "/resource/") {
		t.Fatalf("playlist %d %s", playlist.Code, playlist.Body.String())
	}
	other := f.request(t, "GET", sessionPath+"/0/index.m3u8", map[string]string{"X-Plex-Token": friendToken})
	if other.Code != 403 {
		t.Fatalf("foreign consumer access: %d", other.Code)
	}
	unknown := f.request(t, "GET", sessionPath+"/9/index.m3u8", nil)
	if unknown.Code != 404 {
		t.Fatal("unknown consumer accepted")
	}
}
func TestLiveTVHonestPassAndViewerRestrictions(t *testing.T) {
	f, service, c := liveFixture(t)
	f.srv.d.Identity.MyPlexSubscription = false
	for _, route := range []string{"/livetv/dvrs", liveProviderPath + "/grid", "/media/grabbers"} {
		rec := f.get(t, route)
		if rec.Code != 404 {
			t.Fatalf("Pass-disabled route advertised %s: %d", route, rec.Code)
		}
	}
	providers := f.request(t, "GET", "/media/providers", map[string]string{"Accept": "application/json"})
	if strings.Contains(providers.Body.String(), livetv.Provider) {
		t.Fatal("Pass spoofed")
	}
	f.srv.d.Identity.MyPlexSubscription = true
	c.AllowedUsers = []int64{ownerUser.ID}
	if _, e := service.SaveChannel(context.Background(), c); e != nil {
		t.Fatal(e)
	}
	channels := f.request(t, "GET", liveProviderPath+"/lineups/dvr/channels", map[string]string{"Accept": "application/json", "X-Plex-Token": friendToken})
	if strings.Contains(channels.Body.String(), "Owner channel") {
		t.Fatal("channel access policy leaked")
	}
	tune := f.request(t, "POST", "/livetv/dvrs/1/channels/7/tune", map[string]string{"X-Plex-Token": friendToken})
	if tune.Code != 404 {
		t.Fatal("restricted channel tuned")
	}
	f.srv.d.Auth.(*fakeAuth).users["kid-live"] = core.User{ID: 99, MaxContentRating: "G"}
	c.AllowedUsers = nil
	if _, e := service.SaveChannel(context.Background(), c); e != nil {
		t.Fatal(e)
	}
	guide := f.request(t, "GET", liveProviderPath+"/grid?date=2026-10-18", map[string]string{"X-Plex-Token": "kid-live"})
	if strings.Contains(guide.Body.String(), "Owner programme") {
		t.Fatal("programme rating restriction leaked")
	}
}

// Native clients require usable source analysis at tune, before requesting media.
type liveProbeTranscoder struct {
	*fakeTranscoder
	info   core.MediaInfo
	probes int
}

func (t *liveProbeTranscoder) Probe(context.Context, string) (*core.MediaInfo, error) {
	t.probes++
	v := t.info
	return &v, nil
}
func (t *liveProbeTranscoder) Decide(info core.MediaInfo, caps core.ClientCaps, opts core.DecideOptions) core.Decision {
	return transcode.Decide(info, caps, opts, transcode.Policy{})
}
func TestAppleTVTuneReturnsAnalyzedMediaAndCanonicalUUID(t *testing.T) {
	tc := &liveProbeTranscoder{fakeTranscoder: newFakeTranscoder(core.Decision{}), info: sampleInfo}
	f, service, c := liveFixture(t, tc)
	for _, format := range []string{"application/json", "application/xml"} {
		tune := f.request(t, "POST", "/livetv/dvrs/1/channels/"+c.ID+"/tune", map[string]string{"Accept": format, "X-Plex-Product": "Plex for Apple TV", "X-Plex-Platform": "tvOS"})
		if tune.Code != 200 {
			t.Fatalf("tune: %d %s", tune.Code, tune.Body.String())
		}
		body := tune.Body.String()
		if strings.Contains(body, "MediaSubscription") || strings.Contains(body, `decision="copy"`) {
			t.Fatalf("native response contains web bootstrap or invalid part decision: %s", body)
		}
		if format == "application/json" {
			mc := decodeJSON(t, tune)
			md := mc["Metadata"].([]any)[0].(map[string]any)
			if md["ratingKey"] != nil || md["type"] != nil || tune.Header().Get("X-Plex-Activity") != "" {
				t.Fatal("native tune must be source analysis, without a fake library identity or asynchronous activity")
			}
			if md["genuineMediaAnalysis"] != true {
				t.Fatal(body)
			}
			media := md["Media"].([]any)[0].(map[string]any)
			uuid := media["uuid"].(string)
			if len(uuid) != 36 || uuid[8] != '-' || uuid[13] != '-' || uuid[14] != '4' || uuid[18] != '-' || uuid[23] != '-' {
				t.Fatalf("invalid UUID %q", uuid)
			}
			part := media["Part"].([]any)[0].(map[string]any)
			if media["protocol"] != nil || part["key"] != nil || part["decision"] != nil || len(part["Stream"].([]any)) < 2 {
				t.Fatal(body)
			}
		} else if !strings.Contains(body, `genuineMediaAnalysis="1"`) || !strings.Contains(body, `streamType="1"`) || !strings.Contains(body, `streamType="2"`) {
			t.Fatal(body)
		}
	}
	if len(service.Sessions(context.Background(), &ownerUser)) != 1 {
		t.Fatal("abandoned native tune was not reclaimed")
	}
}

func TestModernAppleTVTuneReturnsPlayableQueue(t *testing.T) {
	tc := &liveProbeTranscoder{fakeTranscoder: newFakeTranscoder(core.Decision{}), info: sampleInfo}
	f, _, channel := liveFixture(t, tc)
	headers := map[string]string{"Accept": "application/json", "X-Plex-Product": "Plex for Apple TV", "X-Plex-Platform": "tvOS", "X-Plex-Version": "2026.20.0"}
	tune := f.request(t, "POST", "/livetv/dvrs/1/channels/"+channel.ID+"/tune", headers)
	if tune.Code != 200 || tune.Header().Get("X-Plex-Activity") != "" {
		t.Fatalf("modern tune must complete synchronously: %d %s", tune.Code, tune.Body.String())
	}
	mc := decodeJSON(t, tune)
	analysis := mc["Metadata"].([]any)[0].(map[string]any)
	queueID, hasQueue := mc["playQueueID"].(float64)
	if !hasQueue || queueID <= 0 || mc["identifier"] != "com.plexapp.plugins.library" || mc["playQueueTotalCount"] != float64(1) || analysis["type"] != "clip" || analysis["key"] == "" || analysis["playQueueItemID"] != mc["playQueueSelectedItemID"] {
		t.Fatalf("modern player cannot load tune as a playable queue: %s", tune.Body.String())
	}
	id := analysis["Media"].([]any)[0].(map[string]any)["uuid"].(string)
	if analysis["guid"] != "plex://livetv/session/"+id || mc["playQueueSelectedMetadataItemID"] != nil {
		t.Fatalf("live session lacks its native GUID or uses a UUID in the integer library-metadata selector: %s", tune.Body.String())
	}
	if mc["MediaSubscription"] != nil || analysis["genuineMediaAnalysis"] != true {
		t.Fatalf("native tune contains asynchronous Web subscription data: %s", tune.Body.String())
	}
	path := analysis["key"].(string)
	queuePath := fmt.Sprintf("/playQueues/%.0f", queueID)
	queue := f.request(t, "GET", queuePath, headers)
	if queue.Code != 200 || decodeJSON(t, queue)["Metadata"].([]any)[0].(map[string]any)["key"] != path {
		t.Fatalf("live queue refetch lost the selected item: %d %s", queue.Code, queue.Body.String())
	}
	if foreign := f.request(t, "GET", queuePath, map[string]string{"X-Plex-Token": friendToken}); foreign.Code == 200 {
		t.Fatal("another viewer can read the live queue")
	}
	decision := f.request(t, "GET", "/video/:/transcode/universal/decision?path="+url.QueryEscape(path)+"&hasMDE=1", headers)
	if decision.Code != 200 || !strings.Contains(decision.Body.String(), path) {
		t.Fatalf("selected live item cannot reach MDE: %d %s", decision.Code, decision.Body.String())
	}
	start := f.request(t, "GET", "/video/:/transcode/universal/start.m3u8?path="+url.QueryEscape(path), headers)
	if start.Code != http.StatusTemporaryRedirect || !strings.Contains(start.Header().Get("Location"), id+"/0/index.m3u8") {
		t.Fatalf("live playback bootstrap did not reach media: %d %s", start.Code, start.Body.String())
	}
	if media := f.request(t, "GET", start.Header().Get("Location"), headers); media.Code != 200 || !strings.Contains(media.Body.String(), "#EXTM3U") {
		t.Fatalf("live queue cannot obtain a media playlist: %d %s", media.Code, media.Body.String())
	}
	for _, state := range []string{"playing", "stopped"} {
		timeline := f.request(t, "GET", "/:/timeline?key="+url.QueryEscape(path)+"&ratingKey="+id+"&state="+state, headers)
		if timeline.Code != 200 {
			t.Fatalf("live timeline %s: %d %s", state, timeline.Code, timeline.Body.String())
		}
	}
	if len(f.state.progress) != 0 {
		t.Fatal("live channel entered catalogue watch history")
	}
	if ended := f.request(t, "GET", queuePath, headers); ended.Code == 200 {
		t.Fatal("ended live session remained playable through its queue")
	}
}
