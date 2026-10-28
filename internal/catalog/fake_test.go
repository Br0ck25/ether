package catalog_test

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/catalog"
	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/store"
	"github.com/braedonsaunders/ether/internal/tmdb"
)

// fakeTMDB serves realistic TMDB JSON fixtures for a small catalogue:
// Fjord Harbor (550, R, Drama, 1999, collection 10 member "Saga"),
// a family movie (551, PG), and show 100 with Specials + 2 seasons.
func fakeTMDB(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	json := func(w http.ResponseWriter, body string) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, body)
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		q := r.URL.Query()
		switch {
		case p == "/genre/movie/list":
			json(w, `{"genres":[{"id":18,"name":"Drama"},{"id":878,"name":"Science Fiction"},{"id":10751,"name":"Family"},{"id":28,"name":"Action"}]}`)
		case p == "/genre/tv/list":
			json(w, `{"genres":[{"id":18,"name":"Drama"},{"id":10765,"name":"Sci-Fi & Fantasy"}]}`)
		case p == "/movie/550":
			json(w, `{"id":550,"title":"Fjord Harbor","original_title":"Fjord Harbor","overview":"An insomniac and a soap maker.","poster_path":"/fc.jpg","backdrop_path":"/fcb.jpg","release_date":"1999-10-15","runtime":139,"status":"Released","tagline":"Quiet waters run deep.","vote_average":8.4,"vote_count":26000,"popularity":61.0,"original_language":"en","imdb_id":"tt0137523","belongs_to_collection":{"id":10,"name":"Saga Collection","poster_path":"/saga.jpg"},"genres":[{"id":18,"name":"Drama"}],"production_companies":[{"id":1,"name":"Northlight"}],"production_countries":[{"iso_3166_1":"US","name":"United States of America"}],"credits":{"cast":[{"id":287,"name":"Theo Varga","character":"Alex Mercer","profile_path":"/bp.jpg"}],"crew":[{"id":7467,"name":"Mara Ellison","job":"Director"},{"id":7468,"name":"Jim Uhls","job":"Screenplay"}]},"release_dates":{"results":[{"iso_3166_1":"US","release_dates":[{"certification":"R","type":3}]}]},"external_ids":{"imdb_id":"tt0137523","tvdb_id":0},"images":{"logos":[{"file_path":"/logo.png","iso_639_1":"en"}]},"keywords":{"keywords":[{"id":825,"name":"support group"},{"id":851,"name":"dual identity"}]}}`)
		case p == "/movie/551":
			json(w, `{"id":551,"title":"Family Fun","overview":"Fun for all.","poster_path":"/ff.jpg","release_date":"2020-05-01","runtime":95,"status":"Released","vote_average":7.2,"vote_count":500,"popularity":20.0,"original_language":"en","imdb_id":"tt9999999","genres":[{"id":10751,"name":"Family"}],"release_dates":{"results":[{"iso_3166_1":"US","release_dates":[{"certification":"PG","type":3}]}]},"credits":{"cast":[],"crew":[]}}`)
		case p == "/movie/552":
			json(w, `{"id":552,"title":"Saga Two","overview":"Sequel.","release_date":"2001-01-01","runtime":120,"status":"Released","vote_average":7.0,"vote_count":900,"popularity":15.0,"original_language":"en","belongs_to_collection":{"id":10,"name":"Saga Collection"},"genres":[{"id":18,"name":"Drama"}],"credits":{"cast":[],"crew":[]}}`)
		case p == "/tv/100":
			json(w, `{"id":100,"name":"Space Show","original_name":"Spectacle Spatial","overview":"To the stars.","poster_path":"/ss.jpg","backdrop_path":"/ssb.jpg","first_air_date":"2020-01-10","status":"Returning Series","episode_run_time":[45],"number_of_seasons":2,"number_of_episodes":3,"vote_average":8.8,"vote_count":5000,"popularity":80.0,"original_language":"en","origin_country":["US"],"genres":[{"id":10765,"name":"Sci-Fi & Fantasy"}],"seasons":[{"id":1000,"name":"Specials","season_number":0,"episode_count":1,"air_date":"2019-12-25"},{"id":1001,"name":"Season 1","season_number":1,"episode_count":2,"air_date":"2020-01-10","poster_path":"/s1.jpg"},{"id":1002,"name":"Season 2","season_number":2,"episode_count":1,"air_date":"2021-03-01"}],"content_ratings":{"results":[{"iso_3166_1":"US","rating":"TV-MA"}]},"external_ids":{"imdb_id":"tt1001001","tvdb_id":777},"credits":{"cast":[{"id":42,"name":"Ada Star","character":"Captain"}],"crew":[{"id":43,"name":"Show Runner","job":"Director"}]},"networks":[{"id":9,"name":"HBO-ish"}]}`)
		case strings.HasPrefix(p, "/tv/100/season/") && strings.Contains(p, "/episode/") && strings.HasSuffix(p, "/external_ids"):
			json(w, `{"id":5001,"imdb_id":"tt5001001","tvdb_id":5001}`)
		case p == "/tv/100/season/0":
			json(w, `{"id":1000,"name":"Specials","season_number":0,"episodes":[{"id":5000,"name":"Christmas Special","episode_number":1,"season_number":0,"air_date":"2019-12-25","runtime":30}]}`)
		case p == "/tv/100/season/1":
			json(w, `{"id":1001,"name":"Season 1","season_number":1,"air_date":"2020-01-10","episodes":[{"id":5001,"name":"Pilot","overview":"Begin.","episode_number":1,"season_number":1,"air_date":"2020-01-10","runtime":45,"still_path":"/pilot.jpg","vote_average":9.0},{"id":5002,"name":"Second","episode_number":2,"season_number":1,"air_date":"2020-01-17","runtime":44,"still_path":"/second.jpg"}]}`)
		case p == "/tv/100/season/2":
			json(w, `{"id":1002,"name":"Season 2","season_number":2,"episodes":[{"id":5003,"name":"Return","episode_number":1,"season_number":2,"air_date":"2021-03-01","runtime":50}]}`)
		case p == "/discover/movie":
			// Filter by year slice when asked; vote_count.gte honoured loosely.
			year := q.Get("primary_release_year")
			results := []string{
				`{"id":550,"title":"Fjord Harbor","release_date":"1999-10-15","genre_ids":[18],"popularity":61.0,"vote_average":8.4,"vote_count":26000,"original_language":"en","poster_path":"/fc.jpg"}`,
				`{"id":551,"title":"Family Fun","release_date":"2020-05-01","genre_ids":[10751],"popularity":20.0,"vote_average":7.2,"vote_count":500,"original_language":"en"}`,
				`{"id":553,"title":"Obscure Nothing","release_date":"2020-06-01","genre_ids":[18],"popularity":0.4,"vote_average":5.0,"vote_count":3,"original_language":"en"}`,
				`{"id":554,"title":"Old Classic","release_date":"1975-01-01","genre_ids":[18],"popularity":1.0,"vote_average":9.0,"vote_count":8000,"original_language":"en"}`,
			}
			var keep []string
			for _, res := range results {
				if year != "" && !strings.Contains(res, `"release_date":"`+year) {
					continue
				}
				keep = append(keep, res)
			}
			json(w, `{"page":1,"results":[`+strings.Join(keep, ",")+`],"total_pages":1,"total_results":`+strconv.Itoa(len(keep))+`}`)
		case p == "/discover/tv":
			json(w, `{"page":1,"results":[{"id":100,"name":"Space Show","first_air_date":"2020-01-10","genre_ids":[10765],"popularity":80.0,"vote_average":8.8,"vote_count":5000,"original_language":"en"}],"total_pages":1,"total_results":1}`)
		case p == "/search/multi":
			json(w, `{"page":1,"results":[{"id":559,"media_type":"movie","title":"Hidden Gem","release_date":"2010-01-01","genre_ids":[18],"popularity":0.9,"vote_average":7.9,"vote_count":12,"original_language":"en"},{"id":287,"media_type":"person","name":"Theo Varga"}],"total_pages":1,"total_results":2}`)
		case p == "/search/movie":
			json(w, `{"page":1,"results":[{"id":559,"title":"Hidden Gem","release_date":"2010-01-01","popularity":0.9,"vote_count":12}],"total_pages":1,"total_results":1}`)
		case p == "/search/tv":
			json(w, `{"page":1,"results":[],"total_pages":1,"total_results":0}`)
		case p == "/collection/10":
			json(w, `{"id":10,"name":"Saga Collection","overview":"The saga.","poster_path":"/saga.jpg","parts":[{"id":552,"title":"Saga Two","release_date":"2001-01-01","genre_ids":[18],"popularity":15.0,"vote_count":900},{"id":550,"title":"Fjord Harbor","release_date":"1999-10-15","genre_ids":[18],"popularity":61.0,"vote_count":26000}]}`)
		case strings.HasPrefix(p, "/find/tt0137523"):
			json(w, `{"movie_results":[{"id":550,"title":"Fjord Harbor","release_date":"1999-10-15","popularity":61.0,"vote_count":26000}]}`)
		case strings.HasPrefix(p, "/find/777"):
			json(w, `{"tv_results":[{"id":100,"name":"Space Show","first_air_date":"2020-01-10","popularity":80.0,"vote_count":5000}]}`)
		case strings.HasPrefix(p, "/find/"):
			json(w, `{}`)
		case p == "/movie/550/recommendations":
			json(w, `{"page":1,"results":[{"id":551,"title":"Family Fun","release_date":"2020-05-01","popularity":20.0,"vote_count":500}],"total_pages":1,"total_results":1}`)
		case p == "/movie/550/similar":
			json(w, `{"page":1,"results":[{"id":551,"title":"Family Fun","release_date":"2020-05-01","popularity":20.0,"vote_count":500},{"id":552,"title":"Saga Two","release_date":"2001-01-01","popularity":15.0,"vote_count":900}],"total_pages":1,"total_results":2}`)
		case p == "/tv/100/recommendations", p == "/tv/100/similar":
			json(w, `{"page":1,"results":[{"id":101,"name":"Other Show","first_air_date":"2022-01-01","popularity":30.0,"vote_count":200}],"total_pages":1,"total_results":1}`)
		case strings.HasPrefix(p, "/trending/movie/"):
			json(w, `{"page":1,"results":[{"id":550,"title":"Fjord Harbor","release_date":"1999-10-15","genre_ids":[18],"popularity":61.0,"vote_count":26000}],"total_pages":1,"total_results":1}`)
		case strings.HasPrefix(p, "/trending/tv/"):
			json(w, `{"page":1,"results":[{"id":100,"name":"Space Show","first_air_date":"2020-01-10","popularity":80.0,"vote_count":5000}],"total_pages":1,"total_results":1}`)
		case strings.HasPrefix(p, "/trending/all/"):
			json(w, `{"page":1,"results":[{"id":550,"media_type":"movie","title":"Fjord Harbor","release_date":"1999-10-15","popularity":61.0,"vote_count":26000},{"id":100,"media_type":"tv","name":"Space Show","first_air_date":"2020-01-10","popularity":80.0,"vote_count":5000}],"total_pages":1,"total_results":2}`)
		case strings.HasPrefix(p, "/movie/"):
			json(w, `{"page":1,"results":[{"id":551,"title":"Family Fun","release_date":"2020-05-01","popularity":20.0,"vote_count":500}],"total_pages":1,"total_results":1}`)
		case strings.HasPrefix(p, "/tv/"):
			json(w, `{"page":1,"results":[{"id":100,"name":"Space Show","first_air_date":"2020-01-10","popularity":80.0,"vote_count":5000}],"total_pages":1,"total_results":1}`)
		default:
			w.WriteHeader(404)
			json(w, `{"status_message":"not found"}`)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func newTestCatalog(t *testing.T, srv *httptest.Server) *catalog.Catalog {
	t.Helper()
	return newTestCatalogCfg(t, srv, config.Catalog{Language: "en-US", Region: "US", MinPopularity: 5, MinVotes: 100, RefreshInterval: config.Duration(24 * time.Hour)})
}

func newTestCatalogCfg(t *testing.T, srv *httptest.Server, cfg config.Catalog) *catalog.Catalog {
	t.Helper()
	db, err := store.OpenMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	var client *tmdb.Client
	if srv != nil {
		client = tmdb.New("testkey", slog.Default(), tmdb.WithBaseURL(srv.URL), tmdb.WithCacheTTL(0))
		t.Cleanup(client.Close)
	}
	var api catalog.TMDB
	if client != nil {
		api = client
	}
	c, err := catalog.New(db, api, cfg, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	return c
}
