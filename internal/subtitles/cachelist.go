package subtitles

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type CachedSubtitle struct {
	ID        string    `json:"id"`
	Language  string    `json:"language"`
	Release   string    `json:"release"`
	FetchedAt time.Time `json:"fetchedAt"`
}

func (s *Service) CachedSubtitles() ([]CachedSubtitle, error) {
	out := []CachedSubtitle{}
	entries, err := os.ReadDir(s.dir)
	if os.IsNotExist(err) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if len(out) >= 1000 {
			break
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") || strings.HasSuffix(entry.Name(), ".sync.json") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		if !s.cached(id, "srt") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(s.dir, entry.Name()))
		if err != nil || len(raw) > 64<<10 {
			continue
		}
		var c CachedSubtitle
		if json.Unmarshal(raw, &c) != nil {
			continue
		}
		c.ID = id
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].FetchedAt.After(out[j].FetchedAt) })
	return out, nil
}
