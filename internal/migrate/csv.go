package migrate

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/watchlist"
	"io"
	"strconv"
	"strings"
	"time"
)

// PreviewCSV accepts Letterboxd diary/ratings/watchlist and Trakt CSV columns.
// Letterboxd's export lacks external IDs, so exact title AND year are required.
func (s *Service) PreviewCSV(ctx context.Context, viewer int64, format, kind string, r io.Reader) (Plan, error) {
	p := Plan{Source: "csv:" + format + ":" + kind, Items: []Item{}, Lists: []List{}, Warnings: []string{}}
	if viewer <= 0 {
		return p, errors.New("viewer required")
	}
	if _, err := s.viewer(ctx, viewer); err != nil {
		return p, errors.New("unknown viewer")
	}
	if format != "letterboxd" && format != "trakt" {
		return p, errors.New("format must be letterboxd or trakt")
	}
	if kind != "history" && kind != "ratings" && kind != "watchlist" {
		return p, errors.New("kind must be history, ratings or watchlist")
	}
	// Read one byte beyond the limit so an oversized file cannot masquerade
	// as EOF and leave the owner with a silently truncated import plan.
	input, e := io.ReadAll(io.LimitReader(r, (16<<20)+1))
	if e != nil {
		return p, e
	}
	if len(input) > 16<<20 {
		return p, errors.New("CSV size limit exceeded")
	}
	rd := csv.NewReader(bytes.NewReader(input))
	header, e := rd.Read()
	if e != nil {
		return p, e
	}
	cols := map[string]int{}
	for i, v := range header {
		cols[strings.ToLower(strings.TrimSpace(strings.TrimPrefix(v, "\ufeff")))] = i
	}
	get := func(row []string, names ...string) string {
		for _, name := range names {
			if i, ok := cols[name]; ok && i < len(row) {
				return row[i]
			}
		}
		return ""
	}
	for line := 2; line <= 100002; line++ {
		row, e := rd.Read()
		if e == io.EOF {
			break
		}
		if e != nil {
			return p, fmt.Errorf("CSV row %d: %w", line, e)
		}
		if line == 100002 {
			return p, errors.New("CSV row limit exceeded")
		}
		title := get(row, "name", "title")
		year, _ := strconv.Atoi(get(row, "year"))
		typ := get(row, "type")
		if typ == "" {
			typ = "movie"
		}
		m := media{Title: title, Type: typ}
		if id := get(row, "tmdb id", "tmdb_id", "tmdb"); id != "" {
			m.GUID = "tmdb://" + id
		} else if id := get(row, "imdb id", "imdb_id", "imdb"); id != "" {
			m.GUID = "imdb://" + id
		}
		key, _ := s.mapMedia(ctx, "", "", m)
		if key == 0 && s.catalog != nil && title != "" && year > 0 {
			ts, se := s.catalog.Search(ctx, title, []core.MediaType{core.MediaType(typ)}, 20)
			if se != nil {
				return p, se
			}
			for _, t := range ts {
				if strings.EqualFold(t.Title, title) && t.Year == year {
					if key != 0 {
						key = 0
						break
					}
					key = t.Key
				}
			}
		}
		if key == 0 {
			p.Warnings = append(p.Warnings, fmt.Sprintf("Row %d unmatched: %s (%d)", line, title, year))
			continue
		}
		rating, _ := strconv.ParseFloat(get(row, "rating"), 64)
		if format == "letterboxd" {
			rating *= 2
		}
		if rating < 0 || rating > 10 {
			return p, fmt.Errorf("CSV row %d rating out of range", line)
		}
		date := get(row, "watched date", "watched_at", "rated_at", "date")
		var at time.Time
		for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02"} {
			at, e = time.Parse(layout, date)
			if e == nil {
				break
			}
		}
		if at.IsZero() && kind == "history" {
			p.Warnings = append(p.Warnings, fmt.Sprintf("Row %d missing valid watched date", line))
			continue
		}
		it := Item{External: fingerprint([]byte(strings.Join(row, "\x00"))), Key: key, Viewer: viewer, Title: title, Rating: rating, ViewedAt: at.Unix()}
		if at.IsZero() {
			it.ViewedAt = 0
		}
		if kind == "history" {
			it.Views = 1
		}
		it.Watchlist = kind == "watchlist"
		p.Items = append(p.Items, it)
	}
	return s.savePlan(ctx, p)
}
func (s *Service) ExportCSV(ctx context.Context, viewer int64, format, kind string, w io.Writer) error {
	if format != "letterboxd" && format != "trakt" {
		return errors.New("invalid format")
	}
	if kind != "history" && kind != "ratings" && kind != "watchlist" {
		return errors.New("invalid kind")
	}
	type entry struct {
		k      core.RatingKey
		rating float64
		at     int64
	}
	entries := []entry{}
	var e error
	if kind == "watchlist" {
		if err := s.RecoverWatchlist(ctx, viewer); err != nil {
			return err
		}
		keys, err := watchlist.ExportKeys(ctx, s.db, viewer)
		if err != nil {
			return err
		}
		for _, key := range keys {
			entries = append(entries, entry{k: key})
		}
	} else {
		exported, err := s.state.ExportEntries(ctx, viewer, kind == "ratings")
		if err != nil {
			return err
		}
		for _, v := range exported {
			at := int64(0)
			if !v.At.IsZero() {
				at = v.At.UnixNano()
			}
			entries = append(entries, entry{k: v.Key, rating: v.Rating, at: at})
		}
	}
	wr := csv.NewWriter(w)
	if format == "letterboxd" {
		e = wr.Write([]string{"Date", "Name", "Year", "Rating", "Watched Date", "TMDB ID"})
	} else {
		e = wr.Write([]string{"title", "year", "type", "tmdb_id", "imdb_id", "rating", "watched_at"})
	}
	if e != nil {
		return e
	}
	for _, v := range entries {
		if s.catalog == nil {
			return errors.New("catalog unavailable")
		}
		t, e := s.catalog.Get(ctx, v.k)
		if e != nil {
			return e
		}
		date := ""
		if v.at > 0 {
			date = time.Unix(0, v.at).UTC().Format(time.RFC3339)
		}
		rating := v.rating
		if format == "letterboxd" {
			rating /= 2
			if date != "" {
				date = date[:10]
			}
			e = wr.Write([]string{date, t.Title, strconv.Itoa(t.Year), strconv.FormatFloat(rating, 'f', -1, 64), date, strconv.FormatInt(t.IDs.TMDB, 10)})
		} else {
			e = wr.Write([]string{t.Title, strconv.Itoa(t.Year), string(t.Type), strconv.FormatInt(t.IDs.TMDB, 10), t.IDs.IMDB, strconv.FormatFloat(rating, 'f', -1, 64), date})
		}
		if e != nil {
			return e
		}
	}
	wr.Flush()
	return wr.Error()
}
