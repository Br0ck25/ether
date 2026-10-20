package plex

import (
	"net/http/httptest"
	"net/url"
	"slices"
	"testing"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/plex/model"
)

func TestParseRequestHeadersAndQuery(t *testing.T) {
	r := httptest.NewRequest("GET", "/library/sections?X-Plex-Token=qtok&X-Plex-Product=Plex%20Web&x-plex-client-identifier=cid&X-Plex-Container-Start=20&X-Plex-Container-Size=10", nil)
	r.Header.Set("X-Plex-Platform", "Chrome")
	r.Header.Set("X-Plex-Device-Name", "Office")
	r.Header.Set("X-Plex-Session-Identifier", "sess")
	r.Header.Set("Accept", "text/html, application/json;q=0.9")
	r.RemoteAddr = "203.0.113.9:4444"
	pr := ParseRequest(r)
	if pr.Token != "qtok" || pr.Product != "Plex Web" || pr.ClientID != "cid" || pr.Platform != "Chrome" || pr.DeviceName != "Office" || pr.SessionID != "sess" {
		t.Fatalf("headers: %+v", pr)
	}
	if !pr.HasPage || pr.Start != 20 || pr.Size != 10 {
		t.Fatalf("paging: %+v", pr)
	}
	if pr.Format != model.JSON {
		t.Fatal("json not detected")
	}
	if !pr.Remote {
		t.Fatal("public address should be remote")
	}
	if lo, hi := pr.window(25); lo != 20 || hi != 25 {
		t.Fatalf("window %d %d", lo, hi)
	}
}

func TestRemoteDetection(t *testing.T) {
	cases := map[string]bool{
		"192.168.1.5:1":     false,
		"10.0.0.2:1":        false,
		"172.20.1.1:1":      false,
		"100.101.102.103:1": false, // tailscale CGNAT
		"127.0.0.1:1":       false,
		"[::1]:1":           false,
		"[fd00::1]:1":       false,
		"8.8.8.8:1":         true,
		"[2001:db8::1]:1":   true,
	}
	for addr, remote := range cases {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = addr
		if got := ParseRequest(r).Remote; got != remote {
			t.Errorf("%s: remote=%v", addr, got)
		}
	}
	// A loopback reverse proxy forwards the real client.
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "127.0.0.1:1"
	r.Header.Set("X-Forwarded-For", "8.8.4.4, 10.0.0.1")
	if !ParseRequest(r).Remote {
		t.Error("forwarded public client should be remote")
	}
	// But a remote peer can't spoof being local.
	r = httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "8.8.8.8:1"
	r.Header.Set("X-Forwarded-For", "192.168.1.1")
	if !ParseRequest(r).Remote {
		t.Error("spoofed XFF honoured")
	}
}

func TestClientCapsByProduct(t *testing.T) {
	cases := []struct {
		product, platform, device string
		hevc, hdr, dv             bool
		maxHeight                 int
		mkv                       bool
	}{
		{"Plex for Apple TV", "tvOS", "Apple TV", true, true, true, 2160, true},
		{"Plex for iOS", "iOS", "iPhone", true, true, true, 2160, true},
		{"Plex Web", "Chrome", "OSX", true, false, false, 2160, true},
		{"Plex Web", "Safari", "OSX", true, true, false, 2160, false},
		{"Plex Web", "Firefox", "Windows", false, false, false, 2160, true},
		{"Plex for Android (TV)", "Android", "SHIELD Android TV", true, true, false, 2160, true},
		{"Plex for Android (Mobile)", "Android", "Pixel 8", true, false, false, 1080, true},
		{"Plex for Roku", "Roku", "Roku Ultra", true, true, false, 2160, true},
		{"Unknown Thing", "", "", false, false, false, 1080, false},
	}
	for _, c := range cases {
		pr := &Request{Product: c.product, Platform: c.platform, Device: c.device}
		caps := pr.ClientCaps(0)
		if caps.SupportsVideo("hevc") != c.hevc || caps.HDR10 != c.hdr || caps.DolbyVision != c.dv || caps.MaxHeight != c.maxHeight || caps.SupportsContainer("mkv") != c.mkv {
			t.Errorf("%s/%s: %+v", c.product, c.platform, caps)
		}
		if !caps.SupportsVideo("h264") || !caps.SupportsAudio("aac") {
			t.Errorf("%s: missing baseline codecs", c.product)
		}
	}
}

func TestProfileExtra(t *testing.T) {
	raw := "add-transcode-target(type=videoProfile&context=streaming&protocol=hls&container=mpegts&videoCodec=h264,hevc&audioCodec=aac,ac3,eac3)" +
		"+append-transcode-target-codec(type=videoProfile&context=streaming&protocol=hls&audioCodec=dca)" +
		"+add-direct-play-profile(type=videoProfile&container=mkv&videoCodec=av1&audioCodec=opus&subtitleCodec=*)" +
		"+add-limitation(scope=videoCodec&scopeName=*&type=upperBound&name=video.height&value=720)" +
		"+add-limitation(scope=videoCodec&scopeName=hevc&type=upperBound&name=video.bitDepth&value=8)" +
		"+add-limitation(scope=videoCodec&scopeName=h264&type=upperBound&name=video.bitrate&value=6000)"
	for name, extra := range map[string]string{
		"raw":     raw,
		"encoded": url.PathEscape(raw),
		"query":   func() string { v, _ := url.QueryUnescape(url.QueryEscape(raw)); return v }(),
	} {
		pr := &Request{Product: "Plex for Apple TV", Platform: "tvOS", ProfileExtra: extra}
		c := pr.ClientCaps(0)
		if !c.SupportsVideo("av1") || !c.SupportsAudio("dts") || !c.SupportsAudio("opus") || !c.SupportsContainer("mkv") {
			t.Errorf("%s: codecs %+v", name, c)
		}
		if c.MaxHeight != 720 || c.MaxBitrateKbps != 6000 || c.HDR10 || c.DolbyVision {
			t.Errorf("%s: limits %+v", name, c)
		}
	}
	ds := ParseProfileExtra(raw)
	if len(ds) != 6 || ds[0].Verb != "add-transcode-target" || ds[0].Args["videoCodec"] != "h264,hevc" {
		t.Fatalf("directives: %+v", ds)
	}
}

func TestLegacyCapabilities(t *testing.T) {
	pr := &Request{Product: "Old Client", ClientCapabilities: "protocols=http-live-streaming,http-mp4-streaming;videoDecoders=h264{profile:high&resolution:720&level:51},h265{resolution:720};audioDecoders=mp3,aac{bitrate:160000},ac3"}
	c := pr.ClientCaps(0)
	if !c.SupportsVideo("hevc") || !c.SupportsAudio("ac3") || c.MaxHeight != 720 {
		t.Fatalf("legacy: %+v", c)
	}
}

func TestRemoteBitrateCap(t *testing.T) {
	c := (&Request{Product: "Plex for iOS", Platform: "iOS", Remote: true}).ClientCaps(8000)
	if c.MaxBitrateKbps != 8000 || !c.Remote {
		t.Fatalf("remote cap: %+v", c)
	}
	c = (&Request{Product: "Plex for iOS", Platform: "iOS"}).ClientCaps(8000)
	if c.MaxBitrateKbps != 0 {
		t.Fatalf("local capped: %+v", c)
	}
	q := url.Values{"maxVideoBitrate": {"3000"}, "videoResolution": {"1280x720"}, "location": {"cellular"}}
	applyDecisionQuery(&c, q)
	if c.MaxBitrateKbps != 3000 || c.MaxHeight != 720 || !c.Remote {
		t.Fatalf("decision query: %+v", c)
	}
	for in, want := range map[string]int{"1920x1080": 1080, "1080x1920": 1080, "720": 720, "1080p": 1080, "bad": 0, "": 0} {
		if got := parseResolutionHeight(in); got != want {
			t.Errorf("%q: %d", in, got)
		}
	}
	if !slices.Contains(c.VideoCodecs, "hevc") {
		t.Error("caps lost")
	}
}

func TestPickAudioPrefersLanguageOverDefaultFlag(t *testing.T) {
	info := core.MediaInfo{Streams: []core.StreamInfo{
		{Index: 0, Type: "video", Codec: "h264"},
		{Index: 1, Type: "audio", Codec: "eac3", Language: "tur", Default: true, Channels: 2},
		{Index: 2, Type: "audio", Codec: "eac3", Language: "eng", Channels: 6},
	}}
	if got := pickAudio(info, []string{"en"}); got != 2 {
		t.Fatalf("pickAudio(en) = %d, want the English track", got)
	}
	if got := pickAudio(info, nil); got != 1 {
		t.Fatalf("pickAudio(no prefs) = %d, want the default-flagged track", got)
	}
}

func TestDecisionVideoBitrateRefinesSourceCapabilities(t *testing.T) {
	caps := core.ClientCaps{MaxBitrateKbps: 8000}
	applyDecisionQuery(&caps, url.Values{"videoBitrate": {"2000"}, "maxVideoBitrate": {"4000"}})
	if caps.MaxBitrateKbps != 2000 {
		t.Fatalf("resolver would ignore requested output bitrate: %+v", caps)
	}
	applyDecisionQuery(&caps, url.Values{"videoBitrate": {"12000"}})
	if caps.MaxBitrateKbps != 2000 {
		t.Fatal("query raised an existing limit")
	}
}
