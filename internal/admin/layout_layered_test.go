package admin_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

type layeredFake struct {
	fakeEngine
	doc    core.HomeLayout
	draft  core.HomeLayout
	who    int64
	writes int
}

func (f *layeredFake) HomeLayout(context.Context) (core.HomeLayout, error) { return f.doc, nil }
func (f *layeredFake) SetHomeLayout(_ context.Context, d core.HomeLayout) (core.HomeLayout, error) {
	f.doc = d
	f.writes++
	return d, nil
}
func (f *layeredFake) LayoutDefaults() map[string]int {
	return map[string]int{"home": 14, "movies": 10, "shows": 10}
}
func (f *layeredFake) PreviewLayout(_ context.Context, u *core.User, hc core.HubContext, sec string, n int, d core.HomeLayout) (*core.HomePreview, error) {
	f.draft = d
	f.who = u.ID
	f.hc = hc
	f.section = sec
	f.limit = n
	p := &core.HomePreview{Context: hc, GeneratedAt: time.Date(2026, 10, 19, 1, 0, 0, 0, time.UTC), Rows: []core.HomeRow{{HubID: "a", Title: "A", Kind: "a", Layer: "global", Slot: 1, AnchorSlot: 1, Placement: "global anchor at slot 1", Policy: &core.RowPolicy{Row: "a", Status: "always", Slot: 1}, PolicyLayer: "global", Items: []core.Title{poster}}}}
	if len(d.Global) > 0 {
		p.Rows = append(p.Rows, core.HomeRow{HubID: "new", Title: "New", Kind: "new", Layer: "personalized", Slot: 2})
	}
	return p, nil
}
func TestLayeredLayoutEndpoints(t *testing.T) {
	e := &layeredFake{doc: core.HomeLayout{Global: []core.LayoutPage{}, Viewers: []core.ViewerLayout{}}}
	h := personalizationHandler(t, e)
	rec := serve(h, req(http.MethodGet, "/ether/api/layout", ""))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"layout":{"global":[]`) || !strings.Contains(rec.Body.String(), `"defaults":{"maxRows"`) {
		t.Fatalf("GET %d %s", rec.Code, rec.Body)
	}
	draft := `{"viewerId":2,"layout":{"global":[{"section":"home","rows":[{"row":"a","status":"always","slot":1}]}],"viewers":[]},"device":"mobile","at":"2026-10-17T20:30:00Z","section":"movies","count":20}`
	rec = serve(h, req(http.MethodPost, "/ether/api/layout/preview", draft))
	if rec.Code != 200 {
		t.Fatalf("preview %d %s", rec.Code, rec.Body)
	}
	if e.who != 2 || e.hc.Device != "mobile" || e.section != "movies" || e.limit != 20 || e.writes != 0 || len(e.doc.Global) != 0 {
		t.Fatalf("preview inputs/persistence %+v", e)
	}
	for _, v := range []string{`"layer":"global"`, `"slot":1`, `"anchorSlot":1`, `"placement":"global anchor at slot 1"`, `"policyLayer":"global"`, `"viewer":{"id":2,"title":"bob"}`} {
		if !strings.Contains(rec.Body.String(), v) {
			t.Fatalf("missing %s in %s", v, rec.Body)
		}
	}
	rec = serve(h, req(http.MethodPost, "/ether/api/layout/impact", draft))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"changed":2`) || !strings.Contains(rec.Body.String(), `"added":["New"]`) {
		t.Fatalf("impact %d %s", rec.Code, rec.Body)
	}
	if e.writes != 0 {
		t.Fatal("impact wrote a layout")
	}
	rec = serve(h, req(http.MethodPut, "/ether/api/layout", `{"layout":{"global":[],"viewers":[]}}`))
	if rec.Code != 200 || e.writes != 1 {
		t.Fatalf("save %d %s writes %d", rec.Code, rec.Body, e.writes)
	}
	for _, body := range []string{`{"device":"fridge"}`, `{"at":"yesterday"}`, `{"count":101}`, `{"viewerId":99}`} {
		rec = serve(h, req(http.MethodPost, "/ether/api/layout/preview", body))
		if rec.Code != 400 && rec.Code != 404 {
			t.Fatalf("bad preview %s: %d %s", body, rec.Code, rec.Body)
		}
	}
}
