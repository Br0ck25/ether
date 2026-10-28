package app

import (
	"github.com/braedonsaunders/ether/internal/cluster"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClusterNodeControlsValidateAndPreserveExistingPlayback(t *testing.T) {
	a := setupTestApp(t, true)
	a.cluster = &fakeMembers{Solo: a.cluster.(*cluster.Solo)}
	for _, test := range []struct {
		body     string
		code     int
		weight   int
		draining bool
	}{
		{`{"weight":0,"draining":true}`, 400, 0, false},
		{`{"weight":101}`, 400, 0, false},
		{`{"weight":3,"draining":true}`, 200, 3, true},
		{`{"draining":false}`, 200, 3, false},
	} {
		w := httptest.NewRecorder()
		a.clusterManageHandler().ServeHTTP(w, httptest.NewRequest("PATCH", "/node", strings.NewReader(test.body)))
		if w.Code != test.code {
			t.Fatalf("status %d: %s", w.Code, w.Body.String())
		}
		if a.nodeDraining() != test.draining || a.playback.Draining() != test.draining {
			t.Fatal("playback admission and heartbeat disagree")
		}
		if a.cfg.Get().Server.ClusterWeight != test.weight {
			t.Fatal("unexpected node weight")
		}
	}
}
