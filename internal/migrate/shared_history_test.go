package migrate

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/braedonsaunders/ether/internal/store"
)

func TestSharedHistoryUsesOwnerForAccountScopedHistory(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`INSERT INTO auth_users(id,plex_id,username,created_at,updated_at) VALUES(1,19,'alice',0,0),(2,88,'bob',0,0)`); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Error("source must be read-only")
			w.WriteHeader(405)
			return
		}
		token := r.Header.Get("X-Plex-Token")
		switch r.URL.Path {
		case "/identity", "/accounts", "/library/sections", "/status/sessions/history/all":
			if token != "owner-secret" {
				w.WriteHeader(403)
				return
			}
		default:
			if token != "alice-secret" && token != "bob-secret" {
				t.Error("owner credentials used for personal library state")
				w.WriteHeader(403)
				return
			}
		}
		switch r.URL.Path {
		case "/identity":
			fmt.Fprint(w, `<MediaContainer machineIdentifier="classic"/>`)
		case "/accounts":
			fmt.Fprint(w, `<MediaContainer><Account id="19" name="alice"/><Account id="88" name="bob"/></MediaContainer>`)
		case "/library/sections":
			fmt.Fprint(w, `<MediaContainer><Directory key="1" type="movie" title="Movies"/></MediaContainer>`)
		case "/library/sections/1/all":
			if r.URL.Query().Get("unwatched") != "0" {
				t.Error("expected watched-only query")
			}
			id, at := 11, 1700000000
			if token == "bob-secret" {
				id, at = 22, 1700000001
			}
			fmt.Fprintf(w, `<MediaContainer size="1"><Video ratingKey="%d" type="movie" title="Film" viewCount="2" lastViewedAt="%d"><Guid id="tmdb://%d"/></Video></MediaContainer>`, id, at, id)
		case "/status/sessions/history/all":
			account := r.URL.Query().Get("accountID")
			if account != "19" && account != "88" {
				t.Error("unscoped source history")
			}
			// Deliberately return both users: the importer must enforce the filter too.
			fmt.Fprint(w, `<MediaContainer><Video ratingKey="11" type="movie" accountID="19" viewedAt="1700000000"><Guid id="tmdb://11"/></Video><Video ratingKey="22" type="movie" accountID="88" viewedAt="1700000001"><Guid id="tmdb://22"/></Video></MediaContainer>`)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	s := New(db, catalogFake{})
	q := Request{URL: server.URL, Token: "owner-secret", HistoryOnly: true, Viewers: []Viewer{{LocalUserID: 1, PlexID: 19, Name: "alice", Token: "alice-secret"}, {LocalUserID: 2, PlexID: 88, Name: "bob", Token: "bob-secret"}}}
	for repeat := 0; repeat < 2; repeat++ {
		plan, err := s.Preview(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		if len(plan.Items) != 2 || len(plan.History) != 2 {
			t.Fatalf("incomplete plan: items=%d history=%d warnings=%v", len(plan.Items), len(plan.History), plan.Warnings)
		}
		for _, item := range append(plan.Items, plan.History...) {
			want := int64(11)
			if item.Viewer == 2 {
				want = 22
			}
			if item.Key.TMDB() != want {
				t.Fatal("cross-viewer history")
			}
		}
		var doc string
		if err := db.QueryRow(`SELECT document FROM migrate_plans WHERE id=?`, plan.ID).Scan(&doc); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(doc, "-secret") {
			t.Fatal("persisted source credential")
		}
		if _, err = s.Import(ctx, plan.ID); err != nil {
			t.Fatal(err)
		}
	}
	for _, uid := range []int{1, 2} {
		var n int
		if err = db.QueryRow(`SELECT count(*) FROM state_history WHERE user_id=?`, uid).Scan(&n); err != nil || n != 1 {
			t.Fatalf("viewer %d has %d events: %v", uid, n, err)
		}
	}
}
