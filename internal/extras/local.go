package extras

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/braedonsaunders/ether/internal/core"
)

// Local trailer files follow the Plex naming conventions: a file sharing
// the feature's name with a "-trailer" suffix, or any video inside a
// "Trailers" (or "Extras") folder next to the feature.

func isTrailerFile(path string) bool {
	base := strings.ToLower(strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)))
	if base == "trailer" || strings.HasSuffix(base, "-trailer") || strings.HasSuffix(base, "_trailer") ||
		strings.HasSuffix(base, " trailer") || strings.HasSuffix(base, "-teaser") {
		return true
	}
	for _, seg := range strings.Split(filepath.ToSlash(path), "/") {
		switch strings.ToLower(seg) {
		case "trailers", "trailer", "extras":
			return true
		}
	}
	return false
}

// featureDir is the directory the feature file lives in for a trailer
// path: the parent of a Trailers/Extras folder, else the same directory.
func featureDir(path string) string {
	dir := filepath.Dir(path)
	switch strings.ToLower(filepath.Base(dir)) {
	case "trailers", "trailer", "extras":
		return filepath.Dir(dir)
	}
	return dir
}

func localTitle(parent *core.Title, path string) string {
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	base = strings.TrimSpace(strings.NewReplacer("-", " ", "_", " ").Replace(base))
	if base == "" || strings.EqualFold(base, "trailer") {
		return parent.Title + " Trailer"
	}
	return base
}

// localExtras attributes trailer files to the parent title: directly when
// the scanner matched the trailer file to the title, or through a feature
// file in the same folder matched to it.
func localExtras(parent *core.Title, files []LocalFile) []Extra {
	byDir := map[string][]core.RatingKey{}
	for _, f := range files {
		if f.Key != 0 && !isTrailerFile(f.Path) {
			byDir[filepath.Dir(f.Path)] = append(byDir[filepath.Dir(f.Path)], f.Key)
		}
	}
	var out []Extra
	for _, f := range files {
		if !isTrailerFile(f.Path) {
			continue
		}
		owner := f.Key
		if owner == 0 {
			for _, k := range byDir[featureDir(f.Path)] {
				owner = k
				break
			}
		}
		if owner != parent.Key {
			continue
		}
		sub := "trailer"
		if strings.Contains(strings.ToLower(filepath.Base(f.Path)), "teaser") {
			sub = "teaser"
		}
		out = append(out, Extra{
			ParentKey: parent.Key, ParentTitle: parent.Title,
			Title: localTitle(parent, f.Path), Subtype: sub, ExtraType: 1,
			Duration: f.Duration, Source: SourceLocal, localPath: f.Path,
			identity: fmt.Sprintf("%d|l|%s", int64(parent.Key), f.Path),
		})
	}
	return out
}

// filepathAbsStat reports whether path is an existing regular file.
func filepathAbsStat(path string) (bool, error) {
	st, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	return st.Mode().IsRegular(), nil
}
