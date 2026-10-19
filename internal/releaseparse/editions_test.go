package releaseparse

import "testing"

func TestParseEdition(t *testing.T) {
	cases := []struct{ name, want string }{
		{"Blade.Runner.1982.Directors.Cut.1080p.BluRay.x264-GRP", "Director's Cut"},
		{"Blade.Runner.1982.Director's.Cut.2160p.REMUX-GRP", "Director's Cut"},
		{"Movie.2020.DC.1080p.WEB-DL.x264-GRP", "Director's Cut"},
		{"Blade.Runner.1982.The.Final.Cut.2160p.UHD.BluRay-GRP", "Final Cut"},
		{"Movie.2001.Extended.Edition.1080p.BluRay-GRP", "Extended"},
		{"Movie.2001.EXTENDED.2160p.WEB-DL-GRP", "Extended"},
		{"Movie.2001.Extended.Cut.1080p-GRP", "Extended"},
		{"Dune.2021.IMAX.2160p.WEB-DL-GRP", "IMAX"},
		{"Dune.2021.IMAX.Enhanced.2160p.WEB-DL-GRP", "IMAX"},
		{"Dune.2021.IMAX.Extended.2160p.WEB-DL-GRP", "Extended"}, // cut wins over presentation
		{"Movie.1999.Unrated.1080p.BluRay-GRP", "Unrated"},
		{"Movie.1999.UNCUT.1080p.BluRay-GRP", "Uncut"},
		{"Movie.1980.Remastered.1080p.BluRay-GRP", "Remastered"},
		{"Movie.1980.4K.Restoration.2160p.BluRay-GRP", "Restored"},
		{"Movie.1975.Theatrical.Cut.1080p.BluRay-GRP", "Theatrical"},
		{"Movie.1975.Special.Edition.DVDRip-GRP", "Special Edition"},
		{"Watchmen.2009.Ultimate.Cut.1080p.BluRay-GRP", "Ultimate Edition"},
		{"Movie.1962.Criterion.Collection.1080p.BluRay-GRP", "Criterion"},
		{"Movie.2010.Open.Matte.1080p.WEB-DL-GRP", "Open Matte"},
		{"Alien.3.1992.Assembly.Cut.1080p.BluRay-GRP", "Assembly Cut"},
		{"Movie.1990.International.Cut.1080p.BluRay-GRP", "International Cut"},
		{"Movie.2011.10th.Anniversary.Edition.BluRay-GRP", "Anniversary Edition"},
		{"The.Matrix.1999.1080p.BluRay.x264-GRP", ""},
		{"Extended.Family.2020.1080p.WEB-DL-GRP", "Extended"}, // title word; accepted heuristic
		{"DC.League.of.Super.Pets.2022.1080p.WEB-DL-GRP", ""},  // leading DC is the brand, not a cut
		{"Show.S01E01.720p.HDTV.x264-GRP", ""},
	}
	for _, c := range cases {
		if got := ParseEdition(c.name); got != c.want {
			t.Errorf("ParseEdition(%q) = %q, want %q", c.name, got, c.want)
		}
		if got := Parse(c.name).Edition; got != c.want {
			t.Errorf("Parse(%q).Edition = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestNormalizeEditionIdempotent(t *testing.T) {
	for _, e := range []string{"Director's Cut", "Extended", "IMAX", "Unrated", "Remastered", "Theatrical", "Final Cut", "Criterion", ""} {
		if got := NormalizeEdition(e); got != e {
			t.Errorf("NormalizeEdition(%q) = %q", e, got)
		}
	}
}
