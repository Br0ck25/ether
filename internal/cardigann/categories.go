package cardigann

import (
	"sort"
	"strconv"
	"strings"
)

// newznabCategories is the standard Newznab/Torznab category table that
// definitions name in caps.categorymappings ("Movies/HD", "TV/Anime", ...).
var newznabCategories = map[string]int{
	"console": 1000, "console/nds": 1010, "console/psp": 1020, "console/wii": 1030,
	"console/xbox": 1040, "console/xbox 360": 1050, "console/wiiware": 1060,
	"console/xbox 360 dlc": 1070, "console/ps3": 1080, "console/other": 1090,
	"console/3ds": 1110, "console/ps vita": 1120, "console/wiiu": 1130,
	"console/xbox one": 1140, "console/ps4": 1180,

	"movies": 2000, "movies/foreign": 2010, "movies/other": 2020, "movies/sd": 2030,
	"movies/hd": 2040, "movies/uhd": 2045, "movies/bluray": 2050, "movies/3d": 2060,
	"movies/dvd": 2070, "movies/web-dl": 2080, "movies/x265": 2090,

	"audio": 3000, "audio/mp3": 3010, "audio/video": 3020, "audio/audiobook": 3030,
	"audio/lossless": 3040, "audio/other": 3050, "audio/foreign": 3060,

	"pc": 4000, "pc/0day": 4010, "pc/iso": 4020, "pc/mac": 4030,
	"pc/mobile-other": 4040, "pc/games": 4050, "pc/mobile-ios": 4060,
	"pc/mobile-android": 4070,

	"tv": 5000, "tv/web-dl": 5010, "tv/foreign": 5020, "tv/sd": 5030, "tv/hd": 5040,
	"tv/uhd": 5045, "tv/other": 5050, "tv/sport": 5060, "tv/anime": 5070,
	"tv/documentary": 5080, "tv/x265": 5090,

	"xxx": 6000, "xxx/dvd": 6010, "xxx/wmv": 6020, "xxx/xvid": 6030, "xxx/x264": 6040,
	"xxx/uhd": 6045, "xxx/pack": 6050, "xxx/imageset": 6060, "xxx/other": 6070,
	"xxx/sd": 6080, "xxx/web-dl": 6090,

	"books": 7000, "books/mags": 7010, "books/ebook": 7020, "books/comics": 7030,
	"books/technical": 7040, "books/other": 7050, "books/foreign": 7060,

	"other": 8000, "other/misc": 8010, "other/hashed": 8020,
}

// newznabCategoryID resolves a category name or numeric id; 0 when unknown.
func newznabCategoryID(cat string) int {
	cat = strings.TrimSpace(cat)
	if n, err := strconv.Atoi(cat); err == nil && n >= 1000 && n < 100000 {
		return n
	}
	key := strings.ToLower(cat)
	if id, ok := newznabCategories[key]; ok {
		return id
	}
	// Tolerate spacing around the slash ("Movies / HD").
	key = strings.Join(strings.Fields(strings.ReplaceAll(key, " / ", "/")), " ")
	return newznabCategories[key]
}

// family returns the Newznab top-level category (2040 → 2000).
func family(cat int) int { return cat / 1000 * 1000 }

// trackerCategories lists the site category ids to search for a request:
// those mapped into any of the wanted Newznab categories (or their
// families when wanted holds top-level ids). Default mappings come first.
func (d *Definition) trackerCategories(wanted []int) []string {
	want := map[int]bool{}
	wantFamily := map[int]bool{}
	for _, w := range wanted {
		want[w] = true
		if w%1000 == 0 {
			wantFamily[w] = true
		}
	}
	seen := map[string]bool{}
	var out []string
	for _, m := range d.Caps.CategoryMappings {
		if m.Newznab == 0 || seen[m.ID] {
			continue
		}
		if want[m.Newznab] || wantFamily[family(m.Newznab)] {
			seen[m.ID] = true
			out = append(out, m.ID)
		}
	}
	return out
}

// mapSiteCategory maps a site category id to its Newznab categories.
func (d *Definition) mapSiteCategory(siteID string) []int {
	siteID = strings.TrimSpace(siteID)
	var out []int
	for _, m := range d.Caps.CategoryMappings {
		if m.Newznab != 0 && m.ID == siteID {
			out = append(out, m.Newznab)
		}
	}
	return out
}

// mapSiteCategoryDesc maps a site category description to Newznab categories.
func (d *Definition) mapSiteCategoryDesc(desc string) []int {
	desc = strings.TrimSpace(desc)
	var out []int
	for _, m := range d.Caps.CategoryMappings {
		if m.Newznab != 0 && m.Desc != "" && strings.EqualFold(m.Desc, desc) {
			out = append(out, m.Newznab)
		}
	}
	if len(out) == 0 {
		if id := newznabCategoryID(desc); id != 0 {
			out = append(out, id)
		}
	}
	return out
}

// bestCategory picks the most specific category of a release.
func bestCategory(cats []int) int {
	if len(cats) == 0 {
		return 0
	}
	sorted := append([]int(nil), cats...)
	sort.SliceStable(sorted, func(i, j int) bool {
		// Subcategories (2040) before families (2000), else keep order.
		return sorted[i]%1000 != 0 && sorted[j]%1000 == 0
	})
	return sorted[0]
}
