package personalize

import (
	"math"
	"sort"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

// viewing is one title watched on one local day.
type viewing struct {
	key       core.RatingKey
	tk        core.RatingKey
	at        time.Time // local
	completed bool
	minutes   float64
}

// viewingsFrom folds the history log (which records a "started" and a
// "finished" entry per viewing) into one viewing per title per local day,
// with minutes watched taken from observed plays when available and
// estimated from the runtime otherwise.
func viewingsFrom(in tasteInput) []viewing {
	type dk struct {
		key core.RatingKey
		day int
	}
	playMinutes := map[dk]float64{}
	for _, p := range in.plays {
		playMinutes[dk{p.key, dayOf(p.started, in.loc)}] += p.watched().Minutes()
	}
	byDay := map[dk]*viewing{}
	var order []dk
	for _, h := range in.history {
		at := h.ViewedAt.In(in.loc)
		k := dk{h.Key, dayOf(at, in.loc)}
		v := byDay[k]
		if v == nil {
			v = &viewing{key: h.Key, tk: tasteKey(h.Key), at: at}
			byDay[k] = v
			order = append(order, k)
		}
		if at.Before(v.at) {
			v.at = at
		}
		v.completed = v.completed || h.Completed
	}
	out := make([]viewing, 0, len(order))
	for _, k := range order {
		v := byDay[k]
		if m, ok := playMinutes[k]; ok && m > 0 {
			v.minutes = m
		} else {
			rt := 0.0
			if ws, ok := in.statuses[v.key]; ok && ws.Duration > 0 {
				rt = ws.Duration.Minutes()
			} else if t, ok := in.titles[v.tk]; ok && t.Runtime > 0 {
				rt = t.Runtime.Minutes()
			}
			if v.completed {
				v.minutes = rt
			} else {
				v.minutes = rt * 0.4
			}
		}
		out = append(out, *v)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].at.Before(out[j].at) })
	return out
}

// habitsFrom derives viewing habits.
func habitsFrom(views []viewing, in tasteInput, evs []Evidence) core.TasteHabits {
	h := core.TasteHabits{PeakHours: []int{}}
	weeknight := map[int]float64{}
	weekend := map[int]float64{}
	hours := map[int]int{}
	for _, v := range views {
		d := dayOf(v.at, in.loc)
		hours[v.at.Hour()]++
		if core.IsWeekend(v.at) {
			weekend[d] += v.minutes
			continue
		}
		if dp := core.DaypartAt(v.at); dp == core.DaypartEvening || dp == core.DaypartLate {
			weeknight[d] += v.minutes
		}
	}
	h.WeeknightMinutes = math.Round(meanOf(weeknight))
	h.WeekendMinutes = math.Round(meanOf(weekend))

	if len(views) >= 3 {
		type hc struct{ hour, n int }
		var hs []hc
		for hr, n := range hours {
			hs = append(hs, hc{hr, n})
		}
		sort.Slice(hs, func(i, j int) bool {
			if hs[i].n != hs[j].n {
				return hs[i].n > hs[j].n
			}
			return hs[i].hour < hs[j].hour
		})
		for i := 0; i < len(hs) && i < 3; i++ {
			h.PeakHours = append(h.PeakHours, hs[i].hour)
		}
	}

	devices := map[string]int{}
	for _, p := range in.plays {
		if p.device != "" {
			devices[p.device]++
		}
	}
	best := 0
	for _, d := range []string{core.DeviceTV, core.DeviceDesktop, core.DeviceWeb, core.DeviceMobile} {
		if devices[d] > best {
			best, h.PreferredDevice = devices[d], d
		}
	}

	// Binge: episodes watched in same-day runs of 3+ of one show.
	type sd struct {
		show core.RatingKey
		day  int
	}
	runs := map[sd]int{}
	eps := 0
	for _, v := range views {
		if v.key.Kind() != core.KindEpisode {
			continue
		}
		eps++
		runs[sd{v.tk, dayOf(v.at, in.loc)}]++
	}
	if eps > 0 {
		inRuns := 0
		for _, n := range runs {
			if n >= 3 {
				inRuns += n
			}
		}
		h.BingeScore = round3(float64(inRuns) / float64(eps))
	}

	finished, rewatched := 0, 0
	for _, e := range evs {
		if e.Kind == EvidenceFinished {
			finished++
			if e.Rewatched {
				rewatched++
			}
		}
	}
	if finished > 0 {
		h.RewatchScore = round3(float64(rewatched) / float64(finished))
	}
	return h
}

func meanOf(m map[int]float64) float64 {
	if len(m) == 0 {
		return 0
	}
	sum := 0.0
	for _, v := range m {
		sum += v
	}
	return sum / float64(len(m))
}

// ctxBucket names the weekday/weekend × daypart bucket of a time.
func ctxBucket(weekend bool, daypart string) string {
	if weekend {
		return "weekend/" + daypart
	}
	return "weekday/" + daypart
}

// learnContext learns when the viewer watches what: genre lifts per
// daypart, per weekday/weekend × daypart sub-persona, and the film share per
// weekday/weekend × daypart bucket.
func (ts *Taste) learnContext(views []viewing, titles map[core.RatingKey]core.Title) {
	n := 0
	genreAll := map[string]float64{}
	dpN := map[string]float64{}
	dpGenre := map[string]map[string]float64{}
	movies := 0.0
	bucketN := map[string]float64{}
	bucketMovies := map[string]float64{}
	bucketGenre := map[string]map[string]float64{}
	for _, v := range views {
		t, ok := titles[v.tk]
		if !ok {
			continue
		}
		n++
		dp := core.DaypartAt(v.at)
		dpN[dp]++
		if dpGenre[dp] == nil {
			dpGenre[dp] = map[string]float64{}
		}
		b := ctxBucket(core.IsWeekend(v.at), dp)
		bucketN[b]++
		if bucketGenre[b] == nil {
			bucketGenre[b] = map[string]float64{}
		}
		for _, g := range normGenres(t.Genres) {
			genreAll[g]++
			dpGenre[dp][g]++
			bucketGenre[b][g]++
		}
		if v.key.Kind() == core.KindMovie {
			movies++
			bucketMovies[b]++
		}
	}
	if n == 0 {
		return
	}
	for dp, nd := range dpN {
		if nd < 4 {
			continue
		}
		lifts := map[string]float64{}
		for g, c := range dpGenre[dp] {
			share := (c + 0.5) / (nd + 1)
			base := (genreAll[g] + 0.5) / (float64(n) + 1)
			if v := clamp(math.Log(share/base)*0.5, -0.5, 0.5); math.Abs(v) >= 0.02 {
				lifts[g] = round3(v)
			}
		}
		ts.DaypartGenre[dp] = lifts
	}
	for b, nb := range bucketN {
		if nb < 6 {
			continue
		}
		lifts := map[string]float64{}
		for g, c := range bucketGenre[b] {
			share := (c + 0.5) / (nb + 1)
			base := (genreAll[g] + 0.5) / (float64(n) + 1)
			if v := clamp(math.Log(share/base)*0.5, -0.5, 0.5); math.Abs(v) >= 0.02 {
				lifts[g] = round3(v)
			}
		}
		ts.ContextGenre[b] = lifts
	}
	overall := (movies + 1) / (float64(n) + 2)
	for _, wk := range []bool{false, true} {
		for _, dp := range []string{core.DaypartMorning, core.DaypartAfternoon, core.DaypartEvening, core.DaypartLate} {
			b := ctxBucket(wk, dp)
			ts.MovieShare[b] = round3((bucketMovies[b] + 3*overall) / (bucketN[b] + 3))
		}
	}
}

// learnDeviceContext learns device-specific genre affinities (e.g. TV vs mobile).
func (ts *Taste) learnDeviceContext(plays []playRecord, titles map[core.RatingKey]core.Title) {
	n := 0.0
	genreAll := map[string]float64{}
	devN := map[string]float64{}
	devGenre := map[string]map[string]float64{}
	for _, p := range plays {
		if p.abandoned || p.device == "" {
			continue
		}
		t, ok := titles[p.tasteKey]
		if !ok {
			continue
		}
		n++
		devN[p.device]++
		if devGenre[p.device] == nil {
			devGenre[p.device] = map[string]float64{}
		}
		for _, g := range normGenres(t.Genres) {
			genreAll[g]++
			devGenre[p.device][g]++
		}
	}
	if n < 4 {
		return
	}
	for dev, nd := range devN {
		if nd < 3 {
			continue
		}
		lifts := map[string]float64{}
		for g, c := range devGenre[dev] {
			share := (c + 0.5) / (nd + 1)
			base := (genreAll[g] + 0.5) / (n + 1)
			if v := clamp(math.Log(share/base)*0.5, -0.5, 0.5); math.Abs(v) >= 0.02 {
				lifts[g] = round3(v)
			}
		}
		ts.DeviceGenre[dev] = lifts
	}
}

// ContextGenreLift returns the viewer's contextual persona lift (-0.5..0.5)
// for a title's normalized genres in hc, blending the specific
// (weekend × daypart) mode, daypart lift, and device mode.
func (ts *Taste) ContextGenreLift(hc core.HubContext, gs []string) float64 {
	if ts == nil || len(gs) == 0 {
		return 0
	}
	dpLifts := ts.DaypartGenre[hc.Daypart]
	ctxLifts := ts.ContextGenre[ctxBucket(hc.Weekend, hc.Daypart)]
	devLifts := ts.DeviceGenre[hc.Device]
	if len(dpLifts) == 0 && len(ctxLifts) == 0 && len(devLifts) == 0 {
		return 0
	}
	sum := 0.0
	for _, g := range gs {
		v := dpLifts[g]
		if cv, ok := ctxLifts[g]; ok {
			v = 0.45*v + 0.55*cv
		}
		if dv, ok := devLifts[g]; ok {
			v = 0.7*v + 0.3*dv
		}
		sum += v
	}
	return clamp(sum/float64(len(gs)), -0.5, 0.5)
}

// movieShareFor is the viewer's expected film share in a context (0.5
// when unknown).
func (ts *Taste) movieShareFor(hc core.HubContext) float64 {
	if ts == nil {
		return 0.5
	}
	if v, ok := ts.MovieShare[ctxBucket(hc.Weekend, hc.Daypart)]; ok {
		return v
	}
	return 0.5
}

// targetMinutes is how long a title should be to fit the viewer's
// available time in this context: phones and weeknights favour short
// titles; weekend evenings on the TV allow a full film.
func (ts *Taste) targetMinutes(hc core.HubContext) float64 {
	t := 0.0
	switch {
	case hc.Device == core.DeviceMobile:
		t = 40
	case !hc.Weekend && hc.Daypart == core.DaypartLate:
		t = 45
	case !hc.Weekend && hc.Daypart == core.DaypartMorning:
		t = 45
	case !hc.Weekend:
		t = 60
	case hc.Daypart == core.DaypartEvening:
		t = 140
	default:
		t = 100
	}
	if ts != nil {
		habit := ts.Habits.WeeknightMinutes
		if hc.Weekend {
			habit = ts.Habits.WeekendMinutes
		}
		if habit > 0 {
			// Lean toward the viewer's real session length.
			t = 0.5*t + 0.5*clamp(habit, 20, 180)
		}
	}
	return t
}
