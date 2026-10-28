package collections

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/braedonsaunders/ether/internal/core"
)

// A Stremio catalog addon (My Lists Addon, MDBList's addon, Cinemeta, and
// others) serves a manifest listing its catalogs, and each catalog answers
// GET {base}/catalog/{type}/{id}.json with {"metas":[{"id":"tt0816692",
// "type":"movie","name":"…"}]}, paged by /skip=N. The stremioCatalog source
// turns one such catalog into a collection. The owner pastes the addon's
// install link and names the catalog; a link to a single catalog also works.

// stremioCatalogPath matches a catalog URL, with or without an extra
// segment such as /skip=100.
var stremioCatalogPath = regexp.MustCompile(`^(.*)/catalog/([^/]+)/([^/]+?)(?:/[^/]*)?\.json$`)

// stremioCat is where one catalog lives.
type stremioCat struct {
	base *url.URL // the addon's base URL, with any settings segment
	kind string   // catalog type: "movie" or "series"; "" when the manifest must say
	id   string   // catalog id; "" when the owner has not chosen one
}

// parseStremioCatalog reads an addon install link (https://… or
// stremio://…/manifest.json) or a single catalog URL. The catalog id and
// type come from the URL when it names a catalog, otherwise from params.
func parseStremioCatalog(raw string, params map[string]string) (stremioCat, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return stremioCat{}, fmt.Errorf("paste the addon's install link")
	}
	if rest, ok := strings.CutPrefix(raw, "stremio://"); ok {
		raw = "https://" + rest
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return stremioCat{}, fmt.Errorf("use the addon's https:// or stremio:// install link")
	}
	u.RawQuery, u.Fragment, u.User = "", "", nil
	p := strings.TrimRight(u.EscapedPath(), "/")
	c := stremioCat{kind: params["type"], id: params["catalog"]}
	if m := stremioCatalogPath.FindStringSubmatch(p); m != nil {
		p = m[1]
		if c.kind == "" {
			c.kind, _ = url.PathUnescape(m[2])
		}
		if c.id == "" {
			c.id, _ = url.PathUnescape(m[3])
		}
	} else {
		for _, suffix := range []string{"/manifest.json", "/configure"} {
			p = strings.TrimSuffix(p, suffix)
		}
	}
	if c.kind != "" && c.kind != "movie" && c.kind != "series" {
		return stremioCat{}, fmt.Errorf("a catalog's type is movie or series")
	}
	u.Path, u.RawPath = "", ""
	if p != "" {
		if u.Path, err = url.PathUnescape(p); err != nil {
			return stremioCat{}, fmt.Errorf("the addon URL path is not valid")
		}
		u.RawPath = p
	}
	c.base = u
	return c, nil
}

// at returns base+suffix.
func (c stremioCat) at(suffix string) string {
	u := *c.base
	u.Path += suffix
	if u.RawPath != "" {
		u.RawPath += suffix
	}
	return u.String()
}

type stremioManifest struct {
	Name     string `json:"name"`
	Catalogs []struct {
		Type          string   `json:"type"`
		ID            string   `json:"id"`
		Name          string   `json:"name"`
		ExtraRequired []string `json:"extraRequired"`
		Extra         []struct {
			Name       string `json:"name"`
			IsRequired bool   `json:"isRequired"`
		} `json:"extra"`
	} `json:"catalogs"`
}

// browsable reports whether a catalog lists titles without needing a
// search term.
func (m stremioManifest) browsable() []string {
	var out []string
	for _, c := range m.Catalogs {
		if c.Type != "movie" && c.Type != "series" || len(c.ExtraRequired) > 0 {
			continue
		}
		need := false
		for _, e := range c.Extra {
			need = need || e.IsRequired
		}
		if !need {
			out = append(out, c.Type+"/"+c.ID+" ("+c.Name+")")
		}
	}
	return out
}

// stremioErr names only the addon's host: an addon URL carries the
// owner's settings, which may include keys.
func stremioErr(c stremioCat, err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, errHTTPNotFound):
		return fmt.Errorf("stremio catalog: %s has no such catalog", c.base.Host)
	case strings.Contains(err.Error(), "access denied"):
		return fmt.Errorf("stremio catalog: %s refused Ether; some public addon instances serve only the Stremio app", c.base.Host)
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	return fmt.Errorf("stremio catalog: %s: %v", c.base.Host, err)
}

const stremioPageSize = 100

// fetchStremioCatalog lists the titles of the chosen catalog, following
// the addon's skip paging.
func (s *Service) fetchStremioCatalog(ctx context.Context, src core.CollectionSource) ([]extRef, error) {
	c, err := parseStremioCatalog(src.URL, src.Params)
	if err != nil {
		return nil, err
	}
	if c.id == "" || c.kind == "" {
		var m stremioManifest
		body, _, err := s.web.get(ctx, c.at("/manifest.json"), map[string]string{"Accept": "application/json"})
		if err != nil {
			return nil, stremioErr(c, err)
		}
		if err := json.Unmarshal(body, &m); err != nil {
			return nil, fmt.Errorf("stremio catalog: %s did not answer with an addon manifest", c.base.Host)
		}
		if c.id == "" {
			return nil, fmt.Errorf("choose a catalog (params.catalog) from %s: %s", firstNonEmpty(m.Name, c.base.Host), strings.Join(m.browsable(), ", "))
		}
		for _, mc := range m.Catalogs {
			if mc.ID == c.id && (mc.Type == "movie" || mc.Type == "series") {
				c.kind = mc.Type
				break
			}
		}
		if c.kind == "" {
			return nil, fmt.Errorf("%s has no catalog %q; its catalogs are %s", firstNonEmpty(m.Name, c.base.Host), c.id, strings.Join(m.browsable(), ", "))
		}
	}
	var out []extRef
	seen := map[string]bool{}
	for page := 0; page < maxExternalPages; page++ {
		path := "/catalog/" + url.PathEscape(c.kind) + "/" + url.PathEscape(c.id)
		if page > 0 {
			path += "/skip=" + strconv.Itoa(page*stremioPageSize)
		}
		body, _, err := s.web.get(ctx, c.at(path+".json"), map[string]string{"Accept": "application/json"})
		if err != nil {
			if page == 0 {
				return nil, stremioErr(c, err)
			}
			break
		}
		var res struct {
			Metas []struct {
				ID          string `json:"id"`
				Type        string `json:"type"`
				Name        string `json:"name"`
				ReleaseInfo string `json:"releaseInfo"`
				Year        string `json:"year"`
			} `json:"metas"`
		}
		if err := json.Unmarshal(body, &res); err != nil {
			return nil, fmt.Errorf("stremio catalog: %s did not answer with a catalog", c.base.Host)
		}
		added := 0
		for _, m := range res.Metas {
			if seen[m.ID] {
				continue
			}
			seen[m.ID] = true
			added++
			if strings.HasPrefix(m.ID, "channel_") {
				continue // My Lists virtual channels are not TMDB titles
			}
			r := extRef{Title: m.Name, Type: "movie"}
			if firstNonEmpty(m.Type, c.kind) == "series" {
				r.Type = "show"
			}
			if n, err := strconv.Atoi(firstNonEmpty(strings.TrimSpace(m.Year), yearPrefix(m.ReleaseInfo))); err == nil {
				r.Year = n
			}
			switch {
			case strings.HasPrefix(m.ID, "tt"):
				r.IMDB = m.ID
			case strings.HasPrefix(m.ID, "tmdb:"):
				r.TMDB, _ = strconv.ParseInt(strings.TrimPrefix(m.ID, "tmdb:"), 10, 64)
			}
			// Other namespaces (kitsu:) name no TMDB id; the title and
			// year still let the resolver try.
			out = append(out, r)
		}
		if added == 0 {
			break
		}
	}
	return out, nil
}

var stremioYearRe = regexp.MustCompile(`^\d{4}`)

func yearPrefix(s string) string { return stremioYearRe.FindString(strings.TrimSpace(s)) }
