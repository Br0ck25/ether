package subtitles

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// MovieHash reads exactly the first and last 64 KiB. Call only on cached
// source ranges so hashing does not fetch a release during first-frame startup.
// Implemented from the published OpenSubtitles algorithm, without copied code.
func MovieHash(r io.ReaderAt, size int64) (string, error) {
	if size < 131072 {
		return "", errors.New("moviehash requires at least 128 KiB")
	}
	sum := uint64(size)
	buf := make([]byte, 65536)
	for _, off := range []int64{0, size - 65536} {
		n, err := r.ReadAt(buf, off)
		if n != len(buf) {
			if err == nil {
				err = io.ErrUnexpectedEOF
			}
			return "", err
		}
		for i := 0; i < len(buf); i += 8 {
			sum += binary.LittleEndian.Uint64(buf[i : i+8])
		}
	}
	return fmt.Sprintf("%016x", sum), nil
}

// ParseASS converts Dialogue events using the declared Format field. Styling,
// positioning and karaoke effects are deliberately dropped for text fallbacks.
func ParseASS(text string) ([]Cue, error) {
	var out []Cue
	var fields []string
	events := false
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") {
			events = strings.EqualFold(line, "[Events]")
			continue
		}
		if !events {
			continue
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		if strings.EqualFold(name, "Format") {
			fields = strings.Split(strings.ToLower(value), ",")
			for i := range fields {
				fields[i] = strings.TrimSpace(fields[i])
			}
			continue
		}
		if !strings.EqualFold(name, "Dialogue") || len(fields) == 0 {
			continue
		}
		parts := strings.SplitN(value, ",", len(fields))
		if len(parts) != len(fields) {
			continue
		}
		var start, end time.Duration
		body := ""
		for i, f := range fields {
			switch f {
			case "start":
				start = assTime(parts[i])
			case "end":
				end = assTime(parts[i])
			case "text":
				body = parts[i]
			}
		}
		body = tagRe.ReplaceAllString(body, "")
		body = strings.NewReplacer(`\N`, "\n", `\n`, "\n", `\h`, " ").Replace(body)
		if start >= 0 && end > start && strings.TrimSpace(body) != "" {
			out = append(out, Cue{start, end, body})
		}
	}
	if len(out) == 0 {
		return nil, errors.New("no ASS dialogue cues found")
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Start < out[j].Start })
	return out, nil
}
func assTime(raw string) time.Duration {
	parts := strings.Split(strings.TrimSpace(raw), ":")
	if len(parts) != 3 {
		return -1
	}
	h, e1 := strconv.ParseInt(parts[0], 10, 32)
	m, e2 := strconv.ParseInt(parts[1], 10, 32)
	sec, e3 := strconv.ParseFloat(parts[2], 64)
	if e1 != nil || e2 != nil || e3 != nil || h < 0 || h > 999 || m < 0 || m > 59 || math.IsNaN(sec) || math.IsInf(sec, 0) || sec < 0 || sec >= 60 {
		return -1
	}
	// Round to whole milliseconds: 2.01 s is 2.009999999 s in float64, which
	// would truncate to a cue one millisecond early.
	return time.Duration(h)*time.Hour + time.Duration(m)*time.Minute + time.Duration(math.Round(sec*1000))*time.Millisecond
}

// Shift applies an offset and playback-rate correction without mutating input.
// Negative cues are dropped or clipped, preserving valid SRT/WebVTT times.
func Shift(cues []Cue, offset time.Duration, rate float64) ([]Cue, error) {
	if math.IsNaN(rate) || math.IsInf(rate, 0) || rate < 0.5 || rate > 2 {
		return nil, errors.New("subtitle rate must be between 0.5 and 2")
	}
	var out []Cue
	for _, c := range cues {
		c.Start = time.Duration(float64(c.Start)*rate) + offset
		c.End = time.Duration(float64(c.End)*rate) + offset
		if c.End <= 0 {
			continue
		}
		if c.Start < 0 {
			c.Start = 0
		}
		if c.End > c.Start {
			out = append(out, c)
		}
	}
	return out, nil
}

// AlignAgainst estimates a constant offset using exact normalized text matches
// against embedded reference subtitles. It requires several independent cues
// and rejects inconsistent matches rather than guessing from unrelated dialogue.
func AlignAgainst(cues, reference []Cue) (time.Duration, error) {
	norm := func(s string) string {
		return strings.Join(strings.Fields(strings.ToLower(htmlTagRe.ReplaceAllString(s, ""))), " ")
	}
	matches := map[string][]time.Duration{}
	for _, c := range reference {
		k := norm(c.Text)
		if len(k) >= 12 {
			matches[k] = append(matches[k], c.Start)
		}
	}
	var offsets []time.Duration
	counts := map[string]int{}
	for _, c := range cues {
		counts[norm(c.Text)]++
	}
	for _, c := range cues {
		k := norm(c.Text)
		if ts := matches[k]; len(ts) == 1 && counts[k] == 1 {
			offsets = append(offsets, ts[0]-c.Start)
		}
	}
	if len(offsets) < 3 {
		return 0, errors.New("not enough unique matching subtitle cues")
	}
	sort.Slice(offsets, func(i, j int) bool { return offsets[i] < offsets[j] })
	median := offsets[len(offsets)/2]
	inliers := 0
	for _, d := range offsets {
		if d >= median-500*time.Millisecond && d <= median+500*time.Millisecond {
			inliers++
		}
	}
	if inliers < 3 || inliers*4 < len(offsets)*3 {
		return 0, errors.New("subtitle alignment is inconsistent; use manual offset")
	}
	return median, nil
}

// SyncOptions adjusts a cached sidecar or estimates the offset from supplied
// embedded-reference SRT/VTT/ASS. It creates a new cache entry, preserving the
// original download and every other viewer's selection.
type SyncOptions struct {
	Apply bool `json:"apply"` // apply adjusted copy to the cached release for every viewer

	OffsetMS  int64   `json:"offsetMs"`
	Rate      float64 `json:"rate"`
	Reference string  `json:"reference,omitempty"`
}

func (s *Service) Sync(id string, opts SyncOptions) (string, error) {
	r, _, err := s.Open(id)
	if err != nil {
		return "", err
	}
	defer r.Close()
	raw, err := io.ReadAll(io.LimitReader(r, 16<<20))
	if err != nil {
		return "", err
	}
	cues, err := ParseCues(string(raw))
	if err != nil {
		return "", err
	}
	offset := time.Duration(opts.OffsetMS) * time.Millisecond
	if opts.Reference != "" {
		ref, err := ParseCues(opts.Reference)
		if err != nil {
			ref, err = ParseASS(opts.Reference)
		}
		if err != nil {
			return "", err
		}
		offset, err = AlignAgainst(cues, ref)
		if err != nil {
			return "", err
		}
	}
	rate := opts.Rate
	if rate == 0 {
		rate = 1
	}
	cues, err = Shift(cues, offset, rate)
	if err != nil {
		return "", err
	}
	if len(cues) == 0 {
		return "", errors.New("sync removed every subtitle cue")
	}
	sum := sha256.Sum256([]byte(RenderSRT(cues)))
	newID := hex.EncodeToString(sum[:16])
	if err = os.MkdirAll(s.dir, 0755); err != nil {
		return "", err
	}
	if err = writeFileAtomic(filepath.Join(s.dir, newID+".srt"), []byte(RenderSRT(cues))); err != nil {
		return "", err
	}
	if err = writeFileAtomic(filepath.Join(s.dir, newID+".vtt"), []byte(RenderVTT(cues))); err != nil {
		return "", err
	}
	if opts.Apply {
		original := strings.TrimPrefix(id, URLPrefix)
		if err = writeFileAtomic(filepath.Join(s.dir, original+".sync.json"), []byte(newID)); err != nil {
			return "", err
		}
	}
	return URLPrefix + newID, nil
}

// SyncHandler serves POST /{cachedSubtitleID}; mount behind owner auth/CSRF.
func (s *Service) SyncHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && strings.Trim(r.URL.Path, "/") == "" {
			items, err := s.CachedSubtitles()
			if err != nil {
				http.Error(w, "Subtitles unavailable", 500)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(items)
			return
		}
		if r.Method != "POST" {
			w.Header().Set("Allow", "POST")
			w.WriteHeader(405)
			return
		}
		var opts SyncOptions
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&opts); err != nil {
			http.Error(w, "invalid sync options", 400)
			return
		}
		if opts.OffsetMS < -3600000 || opts.OffsetMS > 3600000 {
			http.Error(w, "offset exceeds one hour", 400)
			return
		}
		id, err := s.Sync(strings.Trim(r.URL.Path, "/"), opts)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"externalUrl": id})
	})
}
