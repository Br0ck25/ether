package updater

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"
	"time"
)

// channelFeed serves signed manifests by channel name; a missing entry is 404.
func channelFeed(t *testing.T, versions map[string]string) (*httptest.Server, *Client) {
	t.Helper()
	pub, priv, err := GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	for channel, version := range versions {
		m := &Manifest{Version: version, Channel: channel, PublishedAt: time.Now(),
			Artifacts: []Artifact{{OS: runtime.GOOS, Arch: runtime.GOARCH, Kind: KindBinary,
				URL: srv.URL + "/ether", SHA256: shaHex([]byte("x")), Size: 1, Filename: "ether"}}}
		if err := m.Sign(priv); err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(m)
		mux.HandleFunc("/"+channel+".json", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(b) })
	}
	c, err := NewClient(Options{FeedURL: srv.URL, Channel: "beta", CurrentVersion: "1.0.0", PublicKey: pub})
	if err != nil {
		t.Fatal(err)
	}
	return srv, c
}

// A beta install takes whichever of the beta and stable manifests is newer,
// and still updates when no beta has been published yet.
func TestBetaChannelAlsoConsidersStable(t *testing.T) {
	for name, tc := range map[string]struct {
		versions map[string]string
		want     string
	}{
		"beta newer":     {map[string]string{"beta": "1.2.0-beta.1", "stable": "1.1.0"}, "1.2.0-beta.1"},
		"stable newer":   {map[string]string{"beta": "1.2.0-beta.1", "stable": "1.2.0"}, "1.2.0"},
		"no beta yet":    {map[string]string{"stable": "1.1.0"}, "1.1.0"},
		"no stable feed": {map[string]string{"beta": "1.1.0-beta.2"}, "1.1.0-beta.2"},
	} {
		_, c := channelFeed(t, tc.versions)
		u, err := c.Check(context.Background())
		if err != nil || u == nil || u.Version() != tc.want {
			t.Errorf("%s: update %v err %v, want %s", name, u, err, tc.want)
		}
	}
}

func TestDefaultFeedIsTheChannelsRelease(t *testing.T) {
	if got := manifestURL(DefaultFeedBase, "beta"); got != "https://github.com/braedonsaunders/ether/releases/download/channels/beta.json" {
		t.Fatalf("beta manifest URL %s", got)
	}
}
