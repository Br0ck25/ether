package retryafter

import (
	"net/http"
	"testing"
	"time"
)

func TestParse(t *testing.T) {
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	for _, tt := range []struct {
		v     string
		limit time.Duration
		want  time.Duration
		ok    bool
	}{
		{"", 0, 0, false},
		{"  7 ", 0, 7 * time.Second, true},
		{"0", 0, 0, true},
		{"-3", 0, 0, false},
		{"soon", 0, 0, false},
		{"9223372036854775807", 0, time.Duration(9223372036) * time.Second, true},
		{"99999999999", 5 * time.Minute, 5 * time.Minute, true},
		{now.Add(3 * time.Minute).Format(http.TimeFormat), 0, 3 * time.Minute, true},
		{now.Add(240 * time.Hour).Format(http.TimeFormat), time.Hour, time.Hour, true},
		{now.Add(-time.Minute).Format(http.TimeFormat), 0, 0, false},
		{now.Format(http.TimeFormat), 0, 0, false},
	} {
		got, ok := Parse(tt.v, now, tt.limit)
		if got != tt.want || ok != tt.ok {
			t.Errorf("Parse(%q, %v) = %v, %v; want %v, %v", tt.v, tt.limit, got, ok, tt.want, tt.ok)
		}
	}
}
