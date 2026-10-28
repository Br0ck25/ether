package core

import (
	"testing"
	"time"
)

func TestClassifyDevice(t *testing.T) {
	cases := []struct {
		product, platform, device string
		want                      string
	}{
		{"Plex for Apple TV", "tvOS", "Apple TV", DeviceTV},
		{"Plex for Android (TV)", "Android", "SHIELD Android TV", DeviceTV},
		{"Plex for Android (TV)", "Android", "AFTMM", DeviceTV},
		{"Plex", "Android", "AFTKA", DeviceTV}, // Fire TV model number
		{"Plex for Roku", "Roku", "Roku Ultra", DeviceTV},
		{"Plex for Samsung", "Tizen", "Samsung TV", DeviceTV},
		{"Plex for LG", "webOS", "OLED65C1", DeviceTV},
		{"Plex for Vizio", "SmartCast", "", DeviceTV},
		{"Plex for Xbox", "Xbox", "Xbox Series X", DeviceTV},
		{"Plex for PlayStation", "PlayStation", "PS5", DeviceTV},
		{"Plex for Windows", "Windows", "PC", DeviceDesktop},
		{"Plex for Mac", "macOS", "Mac", DeviceDesktop},
		{"Plex HTPC", "Linux", "HTPC", DeviceDesktop},
		{"Plex Media Player", "Windows", "", DeviceDesktop},
		{"Plex Web", "Chrome", "OSX", DeviceWeb},
		{"Plex Web", "Safari", "iPad", DeviceWeb},
		{"", "Firefox", "Linux", DeviceWeb},
		{"Plex for iOS", "iOS", "iPhone", DeviceMobile},
		{"Plex for iOS", "iOS", "iPad", DeviceMobile},
		{"Plex for Android (Mobile)", "Android", "Pixel 8", DeviceMobile},
		{"Plex", "Android", "SM-G991B", DeviceMobile},
		{"Some Client", "Linux", "", DeviceDesktop},
		{"", "", "", DeviceWeb},
	}
	for _, c := range cases {
		if got := ClassifyDevice(c.product, c.platform, c.device); got != c.want {
			t.Errorf("ClassifyDevice(%q, %q, %q) = %q, want %q", c.product, c.platform, c.device, got, c.want)
		}
	}
}

func TestDaypartAndWeekend(t *testing.T) {
	at := func(day time.Weekday, hour int) time.Time {
		// 2026-10-18 is a Sunday.
		return time.Date(2026, 10, 18+int(day), hour, 30, 0, 0, time.UTC)
	}
	parts := map[int]string{0: DaypartLate, 4: DaypartLate, 5: DaypartMorning, 11: DaypartMorning, 12: DaypartAfternoon,
		16: DaypartAfternoon, 17: DaypartEvening, 21: DaypartEvening, 22: DaypartLate, 23: DaypartLate}
	for h, want := range parts {
		if got := DaypartAt(at(time.Monday, h)); got != want {
			t.Errorf("DaypartAt(%02d:30) = %s, want %s", h, got, want)
		}
	}
	weekend := []struct {
		day  time.Weekday
		hour int
		want bool
	}{
		{time.Monday, 20, false}, {time.Thursday, 23, false}, {time.Friday, 16, false}, {time.Friday, 17, true},
		{time.Saturday, 9, true}, {time.Sunday, 21, true},
	}
	for _, c := range weekend {
		if got := IsWeekend(at(c.day, c.hour)); got != c.want {
			t.Errorf("IsWeekend(%s %02d:30) = %v, want %v", c.day, c.hour, got, c.want)
		}
	}
	hc := NewHubContext(at(time.Saturday, 20), "Plex for Roku", "Roku", "")
	if hc.Device != DeviceTV || hc.Daypart != DaypartEvening || !hc.Weekend {
		t.Fatalf("NewHubContext = %+v", hc)
	}
	if b := hc.Bucket(); b != "home/tv/evening/weekend" {
		t.Fatalf("Bucket = %q", b)
	}
	hc.SectionID = 2
	if b := hc.Bucket(); b != "s2/tv/evening/weekend" {
		t.Fatalf("section Bucket = %q", b)
	}
}
