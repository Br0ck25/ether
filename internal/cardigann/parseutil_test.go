package cardigann

import (
	"testing"
	"time"
)

func TestParseSize(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int64
	}{
		{"1.4 GiB", 1503238554},
		{"1.4 GB", 1503238554},
		{"700 MB", 700 << 20},
		{"700MB", 700 << 20},
		{"700 MiB", 700 << 20},
		{"1,234.5 KB", 1264128},
		{"1.234,5 KB", 1264128},
		{"1,5 GB", 1610612736},
		{"2 TB", 2 << 40},
		{"512 B", 512},
		{"123456789", 123456789},
		{"1.5 GB (1610612736 bytes)", 1610612736},
		{"Size: 4.2 GB", 4509715661},
		{"3,5 ГБ", 3758096384},
		{"", 0},
		{"unknown", 0},
	} {
		if got := parseSize(tc.in); got != tc.want {
			t.Errorf("parseSize(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestCoerceInt(t *testing.T) {
	for in, want := range map[string]int{"12": 12, " 1,234 ": 1234, "5 seeders": 5, "": 0, "n/a": 0, "7.0": 7, "-": 0} {
		if got := coerceInt(in); got != want {
			t.Errorf("coerceInt(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestParseLayout(t *testing.T) {
	want := time.Date(2024, 3, 7, 14, 5, 9, 0, time.UTC)
	for _, tc := range []struct{ value, layout string }{
		{"2024-03-07 14:05:09", "2006-01-02 15:04:05"},
		{"2024-03-07 14:05:09", "yyyy-MM-dd HH:mm:ss"},
		{"07.03.2024 14:05:09", "dd.MM.yyyy HH:mm:ss"},
		{"Mar 7, 2024 2:05:09 PM", "MMM d, yyyy h:mm:ss tt"},
		{"MAR 7, 2024 2:05:09 PM", "Jan 2, 2006 3:04:05 PM"},
		{"07-Mar-24 14:05:09", "02-Jan-06 15:04:05"},
		{"2024-03-07T14:05:09 x", "yyyy-MM-ddTHH:mm:ss 'x'"},
	} {
		got, err := parseLayout(tc.value, tc.layout)
		if err != nil {
			t.Errorf("parseLayout(%q, %q): %v", tc.value, tc.layout, err)
			continue
		}
		if !got.Equal(want) {
			t.Errorf("parseLayout(%q, %q) = %v", tc.value, tc.layout, got)
		}
	}
	if got, err := parseLayout("2024-03-07 14:05 +02:00", "yyyy-MM-dd HH:mm zzz"); err != nil || !got.Equal(time.Date(2024, 3, 7, 12, 5, 0, 0, time.UTC)) {
		t.Errorf("zoned layout = %v, %v", got, err)
	}
}

func TestFuzzyTime(t *testing.T) {
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		in   string
		want time.Time
	}{
		{"1700000000", time.Unix(1700000000, 0).UTC()},
		{"1700000000123", time.UnixMilli(1700000000123).UTC()},
		{"today", time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)},
		{"Today 08:30", time.Date(2026, 10, 19, 8, 30, 0, 0, time.UTC)},
		{"Yesterday at 9:15 pm", time.Date(2026, 10, 18, 21, 15, 0, 0, time.UTC)},
		{"2 hours ago", now.Add(-2 * time.Hour)},
		{"1 day, 3 hours ago", now.Add(-27 * time.Hour)},
		{"an hour ago", now.Add(-time.Hour)},
		{"3 weeks ago", now.Add(-21 * 24 * time.Hour)},
		{"2 months ago", now.AddDate(0, -2, 0)},
		{"just now", now},
		{"2024-03-07 14:05:09", time.Date(2024, 3, 7, 14, 5, 9, 0, time.UTC)},
		{"Thu, 07 Mar 2024 14:05:09 +0000", time.Date(2024, 3, 7, 14, 5, 9, 0, time.UTC)},
		{"March 7th, 2024", time.Date(2024, 3, 7, 0, 0, 0, 0, time.UTC)},
		{"7 Mar 2024", time.Date(2024, 3, 7, 0, 0, 0, 0, time.UTC)},
		{"03/07/2024", time.Date(2024, 3, 7, 0, 0, 0, 0, time.UTC)},
		{"2024-03-07T14:05:09Z", time.Date(2024, 3, 7, 14, 5, 9, 0, time.UTC)},
	} {
		got, err := fuzzyTime(tc.in, now)
		if err != nil {
			t.Errorf("fuzzyTime(%q): %v", tc.in, err)
			continue
		}
		if !got.Equal(tc.want) {
			t.Errorf("fuzzyTime(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
	if _, err := fuzzyTime("not a date", now); err == nil {
		t.Error("fuzzyTime accepted garbage")
	}
}

func TestTimeAgoUnits(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for in, want := range map[string]time.Duration{
		"5 mins":      5 * time.Minute,
		"30 seconds":  30 * time.Second,
		"1.5 hours":   90 * time.Minute,
		"2h":          2 * time.Hour,
		"4 d":         96 * time.Hour,
		"1 wk 1 day":  8 * 24 * time.Hour,
		"10 min ago":  10 * time.Minute,
		"1 year ago":  now.Sub(now.AddDate(-1, 0, 0)),
		"3 hrs 2 min": 3*time.Hour + 2*time.Minute,
	} {
		got, ok := timeAgo(in, now)
		if !ok || now.Sub(got) != want {
			t.Errorf("timeAgo(%q) = %v (%v), want %v ago", in, got, ok, want)
		}
	}
}

func TestNewznabCategories(t *testing.T) {
	for in, want := range map[string]int{"Movies/HD": 2040, "movies/uhd": 2045, "TV/Anime": 5070, "TV": 5000, "Movies / BluRay": 2050, "2040": 2040, "PC/Mobile-iOS": 4060, "Nope": 0} {
		if got := newznabCategoryID(in); got != want {
			t.Errorf("newznabCategoryID(%q) = %d, want %d", in, got, want)
		}
	}
	if got := bestCategory([]int{2000, 2040}); got != 2040 {
		t.Errorf("bestCategory = %d", got)
	}
}
