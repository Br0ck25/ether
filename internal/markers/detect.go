package markers

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/bits"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/store"
)

func init() {
	store.Register("markers", 3, `CREATE TABLE markers_detected(rating_key INTEGER NOT NULL,runtime_ms INTEGER NOT NULL,expires_at INTEGER NOT NULL,PRIMARY KEY(rating_key,runtime_ms));`)
	store.Register("markers", 2, `CREATE TABLE markers_fingerprints(rating_key INTEGER PRIMARY KEY,show_id INTEGER NOT NULL,season INTEGER NOT NULL,runtime_ms INTEGER NOT NULL,fingerprint BLOB NOT NULL);`)
}

// Runner is injectable so detection tests never invoke live binaries.
type Runner interface {
	Run(context.Context, string, []string, io.Writer, io.Writer) error
}
type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, path string, args []string, out, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Stdout = out
	cmd.Stderr = stderr
	return cmd.Run()
}

// DetectOptions requires a local cached file. It never reads a streaming URL or
// fetches missing Usenet ranges. The caller controls background scheduling.
type DetectOptions struct {
	FFmpeg string
	Runner Runner
}
type cappedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > b.limit {
		return 0, errors.New("detector output limit exceeded")
	}
	return b.Buffer.Write(p)
}

// fingerprint uses pure-Go Goertzel band energies of decoded 8 kHz mono PCM.
// Each 250 ms frame records changes in six spectral bands (12 bits). This is
// acoustic matching, not a claim to implement Chromaprint's format/algorithm.
func fingerprint(pcm []byte) []uint32 {
	const n = 2000
	freqs := []float64{150, 300, 600, 1200, 2000, 3000}
	var previous [6]float64
	out := make([]uint32, 0, len(pcm)/(n*2))
	for off := 0; off+n*2 <= len(pcm); off += n * 2 {
		var energy [6]float64
		total := float64(0)
		for b, f := range freqs {
			coeff := 2 * math.Cos(2*math.Pi*f/8000)
			a, c := float64(0), float64(0)
			for i := 0; i < n; i++ {
				sample := float64(int16(binary.LittleEndian.Uint16(pcm[off+2*i:]))) / 32768
				v := sample + coeff*a - c
				c, a = a, v
			}
			energy[b] = a*a + c*c - coeff*a*c
			total += energy[b]
		}
		var code uint32
		if total < 0.01 {
			out = append(out, 0)
			previous = energy
			continue
		}
		for b := range energy {
			if energy[b] > previous[b] {
				code |= 1 << b
			}
			if b > 0 && energy[b] > energy[b-1] {
				code |= 1 << (b + 6)
			}
		}
		out = append(out, code)
		previous = energy
	}
	return out
}

// MatchIntro finds a long contiguous acoustic match in two episode openings.
// Silent/near-constant runs are rejected. Returned offsets use 250 ms frames.
func MatchIntro(a, b []uint32) (aStart, bStart, length int, ok bool) {
	if len(a) > 2400 {
		a = a[:2400]
	}
	if len(b) > 2400 {
		b = b[:2400]
	}
	prev := make([]int, len(b)+1)
	best, ai, bi := 0, 0, 0
	for i, x := range a {
		next := make([]int, len(b)+1)
		for j, y := range b {
			if x != 0 && y != 0 && bits.OnesCount32(x^y) <= 1 {
				next[j+1] = prev[j] + 1
				if next[j+1] > best {
					best, ai, bi = next[j+1], i+1, j+1
				}
			}
		}
		prev = next
	}
	if best < 80 {
		return 0, 0, 0, false
	}
	startA, startB := ai-best, bi-best
	if startA > 1200 || startB > 1200 {
		return 0, 0, 0, false
	}
	if best > 720 {
		best = 720
	}
	unique := map[uint32]bool{}
	for _, v := range a[startA : startA+best] {
		unique[v] = true
	}
	if len(unique) < 8 {
		return 0, 0, 0, false
	}
	return startA, startB, best, true
}

// AnalyzeCached performs bounded acoustic intro matching across a season and
// conservative black+silence tail detection. It is deliberately off the play
// request path; call from a bounded background worker after a file is cached.
func (s *Service) AnalyzeCached(ctx context.Context, key core.RatingKey, info core.MediaInfo, path string, o DetectOptions) error {
	if s.o.DB == nil {
		return errors.New("detector requires database")
	}
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() {
		return errors.New("detector requires regular cached file")
	}
	if o.FFmpeg == "" {
		return errors.New("ffmpeg unavailable")
	}
	if o.Runner == nil {
		o.Runner = ExecRunner{}
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	var pcm = cappedBuffer{limit: 10 << 20}
	var log = cappedBuffer{limit: 1 << 20}
	if key.Kind() == core.KindEpisode {
		args := []string{"-nostdin", "-hide_banner", "-v", "error", "-threads", "1", "-protocol_whitelist", "file,pipe", "-i", path, "-t", "600", "-vn", "-sn", "-ac", "1", "-ar", "8000", "-f", "s16le", "pipe:1"}
		if err = o.Runner.Run(ctx, o.FFmpeg, args, &pcm, &log); err != nil {
			return fmt.Errorf("intro audio decode: %w", err)
		}
		fp := fingerprint(pcm.Bytes())
		rows, err := s.o.DB.QueryContext(ctx, `SELECT rating_key,runtime_ms,fingerprint FROM markers_fingerprints WHERE show_id=? AND season=? AND rating_key<>? ORDER BY rating_key LIMIT 12`, key.TMDB(), key.Season(), int64(key))
		if err != nil {
			return err
		}
		type reference struct {
			key     core.RatingKey
			runtime int64
			fp      []uint32
		}
		var refs []reference
		for rows.Next() {
			var r reference
			var raw []byte
			if err = rows.Scan(&r.key, &r.runtime, &raw); err != nil {
				rows.Close()
				return err
			}
			if err = json.Unmarshal(raw, &r.fp); err != nil {
				rows.Close()
				return err
			}
			refs = append(refs, r)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, r := range refs {
			a, b, n, ok := MatchIntro(fp, r.fp)
			if !ok {
				continue
			}
			intro := Marker{Type: "intro", StartMS: int64(a) * 250, EndMS: int64(a+n) * 250, Source: "acoustic"}
			other := Marker{Type: "intro", StartMS: int64(b) * 250, EndMS: int64(b+n) * 250, Source: "acoustic"}
			if valid(intro, info.Duration) && valid(other, time.Duration(r.runtime)*time.Millisecond) {
				existing, _, _ := s.cached(ctx, key, info.Duration)
				if err = s.Put(ctx, key, info.Duration, merge(existing, []Marker{intro})); err != nil {
					return err
				}
				existing, _, _ = s.cached(ctx, r.key, time.Duration(r.runtime)*time.Millisecond)
				if err = s.Put(ctx, r.key, time.Duration(r.runtime)*time.Millisecond, merge(existing, []Marker{other})); err != nil {
					return err
				}
			}
			break
		}
		raw, _ := json.Marshal(fp)
		if _, err = s.o.DB.ExecContext(ctx, `INSERT INTO markers_fingerprints VALUES(?,?,?,?,?) ON CONFLICT(rating_key) DO UPDATE SET runtime_ms=excluded.runtime_ms,fingerprint=excluded.fingerprint`, int64(key), key.TMDB(), key.Season(), info.Duration.Milliseconds(), raw); err != nil {
			return err
		}
	}
	if info.Duration < 5*time.Minute {
		return nil
	}
	start := max(time.Duration(0), info.Duration-10*time.Minute)
	log = cappedBuffer{limit: 2 << 20}
	args := []string{"-nostdin", "-hide_banner", "-threads", "1", "-protocol_whitelist", "file,pipe", "-ss", strconv.FormatFloat(start.Seconds(), 'f', 3, 64), "-i", path, "-t", "600", "-vf", "blackdetect=d=2:pix_th=0.10", "-af", "silencedetect=n=-40dB:d=2", "-f", "null", "-"}
	if err = o.Runner.Run(ctx, o.FFmpeg, args, io.Discard, &log); err != nil {
		return fmt.Errorf("credits detection: %w", err)
	}
	if m, ok := CreditsFromLog(log.String(), start, info.Duration); ok {
		existing, _, _ := s.cached(ctx, key, info.Duration)
		return s.Put(ctx, key, info.Duration, merge(existing, []Marker{m}))
	}
	return nil
}

var blackRE = regexp.MustCompile(`black_start:([0-9.]+) black_end:([0-9.]+)`)
var silenceRE = regexp.MustCompile(`silence_start: ([0-9.]+)[\s\S]*?silence_end: ([0-9.]+)`)

// CreditsFromLog emits a final marker only for overlapping black+silence at
// least 5s long that reaches the actual tail. This is a conservative end-card
// heuristic, not reliable rolling-credits recognition.
func CreditsFromLog(log string, offset, duration time.Duration) (Marker, bool) {
	for _, b := range blackRE.FindAllStringSubmatch(log, -1) {
		bs, _ := strconv.ParseFloat(b[1], 64)
		be, _ := strconv.ParseFloat(b[2], 64)
		for _, s := range silenceRE.FindAllStringSubmatch(log, -1) {
			ss, _ := strconv.ParseFloat(s[1], 64)
			se, _ := strconv.ParseFloat(s[2], 64)
			start := offset + time.Duration(max(bs, ss)*float64(time.Second))
			end := offset + time.Duration(min(be, se)*float64(time.Second))
			if end-start >= 5*time.Second && start >= duration*4/5 && end >= duration-time.Second {
				return Marker{Type: "credits", StartMS: start.Milliseconds(), EndMS: duration.Milliseconds(), Final: true, Source: "black-silence"}, true
			}
		}
	}
	return Marker{}, false
}

type detectJob struct {
	key  core.RatingKey
	info core.MediaInfo
	path string
}

var ErrQueueFull = errors.New("marker detector queue full")

// ScheduleCached queues at most eight files behind a single background worker.
// Duplicate in-flight jobs coalesce; successful detection is remembered for
// the cache TTL. Call Close before closing the shared database.
func (s *Service) ScheduleCached(ctx context.Context, key core.RatingKey, info core.MediaInfo, path string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.o.Detector.FFmpeg == "" {
		return errors.New("marker detector disabled: no bundled ffmpeg")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("marker detector closed")
	}
	if s.pending[key] {
		return nil
	}
	if s.o.DB == nil {
		return errors.New("marker detector requires database")
	}
	var expires int64
	err := s.o.DB.QueryRowContext(ctx, `SELECT expires_at FROM markers_detected WHERE rating_key=? AND runtime_ms=?`, int64(key), info.Duration.Milliseconds()).Scan(&expires)
	if err == nil && expires > s.o.Now().Unix() {
		return nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if s.jobs == nil {
		s.jobs = make(chan detectJob, 8)
		s.pending = map[core.RatingKey]bool{}
		workerCtx, cancel := context.WithCancel(context.Background())
		s.cancel = cancel
		s.wg.Add(1)
		go s.detectWorker(workerCtx)
	}
	select {
	case s.jobs <- detectJob{key, info, path}:
		s.pending[key] = true
		return nil
	default:
		return ErrQueueFull
	}
}
func (s *Service) detectWorker(ctx context.Context) {
	defer s.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case j := <-s.jobs:
			err := s.AnalyzeCached(ctx, j.key, j.info, j.path, s.o.Detector)
			if err == nil {
				_, err = s.o.DB.ExecContext(ctx, `INSERT INTO markers_detected VALUES(?,?,?) ON CONFLICT(rating_key,runtime_ms) DO UPDATE SET expires_at=excluded.expires_at`, int64(j.key), j.info.Duration.Milliseconds(), s.o.Now().Add(s.o.TTL).Unix())
			}
			if err != nil && ctx.Err() == nil {
				s.o.Logger.Warn("background marker detection failed", "key", j.key, "error", err)
			}
			s.mu.Lock()
			delete(s.pending, j.key)
			s.mu.Unlock()
		}
	}
}
func (s *Service) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	cancel := s.cancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	s.wg.Wait()
	return nil
}
