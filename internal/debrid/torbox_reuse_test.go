package debrid

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
)

func TestTorBoxColdAcquireNeverListsTheAccountBeforeCreatingCachedItem(t *testing.T) {
	f := &fakeTorBox{cached: true}
	base := f.handler(t)
	var lists atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/torrents/mylist" && r.URL.Query().Get("id") == "" {
			lists.Add(1)
			<-r.Context().Done() // the old unconditional listing consumed Play's budget
			return
		}
		base.ServeHTTP(w, r)
	}))
	defer srv.Close()
	p := newTorBox(testKey, fastOpts(srv.URL))
	cfg := config.Debrid{CachedOnly: true, RemoveUncached: true, Providers: []config.DebridProvider{{ID: "tb", Kind: "torbox", APIKey: testKey, Enabled: true}}}
	svc := NewService(cfg, Options{NewProvider: func(config.DebridProvider, ProviderOptions) (Provider, error) { return p, nil }})
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	payload, err := svc.Acquire(ctx, core.Release{ID: "new-episode", Name: "Show.S01", Protocol: core.ProtocolTorrent, InfoHash: testHash, Target: &core.ReleaseTarget{Season: 1, Episode: 2}})
	if err != nil || !IsPayload(payload) || lists.Load() != 0 {
		t.Fatalf("cold Play listed account: lists=%d err=%v", lists.Load(), err)
	}
	if ctx.Err() != nil || p.findKnown(strings.ToUpper(testHash)) != "77" {
		t.Fatal("successful acquisition did not retain a reusable owned id")
	}
}

func TestTorBoxDuplicateCreateRecoversExistingTorrentByHash(t *testing.T) {
	f := &fakeTorBox{cached: true}
	base := f.handler(t)
	var lists, creates atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/torrents/createtorrent" {
			creates.Add(1)
			if err := r.ParseMultipartForm(1 << 20); err != nil || r.FormValue("add_only_if_cached") != "true" {
				t.Error("lost atomic cached-only guard")
			}
			fmt.Fprint(w, `{"success":false,"error":"DUPLICATE_ITEM","detail":"already exists","data":null}`)
			return
		}
		if r.URL.Path == "/torrents/mylist" && r.URL.Query().Get("id") == "" {
			lists.Add(1)
			if r.URL.Query().Get("bypass_cache") != "true" {
				t.Error("duplicate recovery reused a stale account snapshot")
			}
			// A similar name is not identity; only the hash can select the item.
			fmt.Fprintf(w, `{"success":true,"data":[{"id":99,"hash":"%s","name":"Show.S01"},{"id":77,"hash":"%s"}]}`, strings.Repeat("c", 40), strings.ToUpper(testHash))
			return
		}
		if r.URL.Path == "/torrents/mylist" && r.URL.Query().Get("id") != "77" {
			t.Error("duplicate matched the wrong account item")
		}
		base.ServeHTTP(w, r)
	}))
	defer srv.Close()
	p := newTorBox(testKey, fastOpts(srv.URL))
	acq, err := p.Acquire(t.Context(), acquireReq(true))
	if err != nil || acq == nil || acq.TorrentID != "77" || acq.Added || lists.Load() != 1 || creates.Load() != 1 {
		t.Fatalf("duplicate recovery: %+v err=%v lists=%d creates=%d", acq, err, lists.Load(), creates.Load())
	}
	if len(f.deleted) != 0 {
		t.Fatal("deleted an item owned by another client")
	}
}

func TestTorBoxUnlistedDuplicateRemainsTransient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			fmt.Fprint(w, `{"success":false,"error":"DUPLICATE_ITEM","data":null}`)
		} else {
			fmt.Fprint(w, `{"success":true,"data":[]}`)
		}
	}))
	defer srv.Close()
	p := newTorBox(testKey, fastOpts(srv.URL))
	_, err := p.Acquire(t.Context(), acquireReq(true))
	if !errors.Is(err, core.ErrSourceUnavailable) || errors.Is(err, core.ErrArticleMissing) || errors.Is(err, core.ErrReleaseRejected) {
		t.Fatalf("duplicate became permanent: %v", err)
	}
}
