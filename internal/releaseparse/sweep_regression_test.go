package releaseparse

import (
	"testing"

	"github.com/braedonsaunders/ether/internal/core"
)

func TestYearAfterReleaseTagIsNotTheReleaseYear(t *testing.T) {
	for _, tc := range []struct {
		name, title string
		year        int
	}{
		{"Movie.1984.Remastered.2014.1080p.BluRay-GRP", "Movie", 1984},
		{"Movie.1999.PROPER.2003.1080p.BluRay-GRP", "Movie", 1999},
		{"Wonder.Woman.1984.2020.2160p.WEB-DL-GRP", "Wonder Woman 1984", 2020},
		{"2001.A.Space.Odyssey.1968.1080p.BluRay-GRP", "2001 A Space Odyssey", 1968},
	} {
		got := Parse(tc.name)
		if got.Title != tc.title || got.Year != tc.year {
			t.Errorf("%s: title=%q year=%d; want %q %d", tc.name, got.Title, got.Year, tc.title, tc.year)
		}
	}
}

func TestCamVariantsAreCam(t *testing.T) {
	for _, name := range []string{
		"Movie.Title.2019.1080p.HDTC.x264-GRP",
		"Movie.Title.2019.CAMRip.x264-GRP",
		"Movie.Title.2019.HQCAM.x264-GRP",
		"Movie.Title.2019.TSRip.x264-GRP",
	} {
		if got := Parse(name).Source; got != core.SourceCam {
			t.Errorf("%s: source %q, want cam", name, got)
		}
	}
	if got := Parse("Movie.Title.2019.BDRemux.1080p-GRP").Source; got != core.SourceRemux {
		t.Errorf("BDRemux: source %q", got)
	}
}

func TestAnimeChecksumIsNotTheGroup(t *testing.T) {
	got := Parse("[SubsPlease] Frieren - S01E12 (1080p) [ABCDEF12].mkv")
	if got.Group != "SubsPlease" || got.Title != "Frieren" {
		t.Fatalf("group=%q title=%q", got.Group, got.Title)
	}
}

func TestEpisodeTitleSkipsReleaseTagsBeforeResolution(t *testing.T) {
	for _, name := range []string{
		"Show.S01E05.DV.2160p.WEB.H265-GRP",
		"Show.S01E05.DV.HDR.2160p.WEB.H265-GRP",
		"Show.S01E05.Hybrid.1080p.WEB-DL-GRP",
		"Show.S01E05.RERIP.720p.HDTV-GRP",
		"Show.S01E05.AMZN.1080p.WEB-DL-GRP",
	} {
		if !EpisodeTitleMatches(name, "The Long Night") {
			t.Errorf("%s rejected", name)
		}
	}
	if EpisodeTitleMatches("Show.S01E05.Other.Night.DV.2160p.WEB.H265-GRP", "The Long Night") {
		t.Error("a different episode title matched")
	}
}

func TestTitleWordsBeforeEpisodeMarkerAreTitle(t *testing.T) {
	for _, tc := range []struct{ name, title string }{
		{"The.Spanish.Princess.S01E01.1080p.WEB.H264-GRP", "The Spanish Princess"},
		{"The.French.Show.S01.1080p.WEB-DL-GRP", "The French Show"},
		{"Show.Name.Complete.S01-S05.1080p.BluRay-GRP", "Show Name"},
	} {
		got := Parse(tc.name)
		if got.Title != tc.title || len(got.Languages) != 0 {
			t.Errorf("%s: title=%q languages=%v; want %q", tc.name, got.Title, got.Languages, tc.title)
		}
	}
}

func TestExplicitResolutionWinsOverUHDSource(t *testing.T) {
	if got := Parse("Movie.2019.1080p.UHD.BluRay.x265-GRP").Height; got != 1080 {
		t.Fatalf("height %d", got)
	}
	if got := Parse("Movie.2019.UHD.BluRay.x265-GRP").Height; got != 2160 {
		t.Fatalf("bare UHD height %d", got)
	}
}

func TestHDRBeforeGroupSuffix(t *testing.T) {
	if !Parse("Movie.2019.2160p.WEB-DL.H265.HDR-GRP").HDR10 {
		t.Fatal("HDR before the group suffix missed")
	}
}

func TestTrailingUploaderTagIsNotTheGroup(t *testing.T) {
	for name, want := range map[string]string{
		"Movie.2019.1080p.BluRay.x264-SPARKS[rarbg]":                 "SPARKS",
		"Movie.2019.1080p.WEBRip.x264-GRP[TGx]":                      "GRP",
		"[Judas] Show - S01E01 [1080p][HEVC x265 10bit][Multi-Subs]": "Judas",
		"Movie Name (2020) [1080p] [YTS.MX]":                         "YTS.MX",
	} {
		if got := Parse(name).Group; got != want {
			t.Errorf("%s: group %q, want %q", name, got, want)
		}
	}
}
