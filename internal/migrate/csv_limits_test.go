package migrate

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/braedonsaunders/ether/internal/store"
)

func TestCSVRejectsOversizeInput(t *testing.T) {
	for _, viaHTTP := range []bool{false, true} {
		name := "reader"
		if viaHTTP {
			name = "http"
		}
		t.Run(name, func(t *testing.T) {
			db := store.OpenTest(t)
			if _, err := db.Exec(`INSERT INTO auth_users(id,plex_id,created_at,updated_at) VALUES(1,99,0,0)`); err != nil {
				t.Fatal(err)
			}
			service := New(db, catalogFake{})
			// End a syntactically complete record exactly at the byte limit. The
			// overflow is also valid CSV, so neither parser errors nor row limits help.
			header := "tmdb_id,rating,notes\n"
			prefix := header + "11,8,"
			input := prefix + strings.Repeat("x", (16<<20)-len(prefix)-1) + "\n12,7,extra\n"
			if viaHTTP {
				req := httptest.NewRequest("POST", "/csv/preview?viewer=1&format=trakt&kind=ratings", strings.NewReader(input))
				rec := httptest.NewRecorder()
				service.Handler().ServeHTTP(rec, req)
				if rec.Code != 400 {
					t.Fatalf("oversized CSV status = %d, want 400", rec.Code)
				}
			} else if p, err := service.PreviewCSV(context.Background(), 1, "trakt", "ratings", strings.NewReader(input)); err == nil {
				t.Fatalf("oversized CSV accepted as a partial plan with %d items", len(p.Items))
			}
			var count int
			if err := db.QueryRow(`SELECT COUNT(*) FROM migrate_plans`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatalf("oversized input saved %d partial plans", count)
			}
		})
	}
}
