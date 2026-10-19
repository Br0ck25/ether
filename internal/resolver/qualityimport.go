package resolver

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/braedonsaunders/ether/internal/core"
)

// QualityImport preserves format references so an owner can fetch the referenced
// TRaSH definitions before activating the imported profile.
type QualityImport struct {
	Profile   core.QualityProfile `json:"profile"`
	FormatIDs map[string]string   `json:"formatIds"`
}

// ImportQualityProfile translates native TRaSH quality groups into exact
// source/resolution gates. Download upgrade/cutoff fields have no streaming
// equivalent; source ordering and minimum format score do.
func ImportQualityProfile(data []byte) (QualityImport, error) {
	var native struct {
		Name           string            `json:"name"`
		MinFormatScore int               `json:"minFormatScore"`
		Items          []json.RawMessage `json:"items"`
		FormatItems    map[string]string `json:"formatItems"`
	}
	if len(data) > 8<<20 {
		return QualityImport{}, fmt.Errorf("quality profile: too large")
	}
	if err := json.Unmarshal(data, &native); err != nil {
		return QualityImport{}, err
	}
	if native.Name == "" || len(native.Items) == 0 {
		return QualityImport{}, fmt.Errorf("quality profile: name and items are required")
	}
	p := core.QualityProfile{Name: native.Name, PreferredCodecs: []string{"h264", "hevc"}, PreferredAudio: []string{"eac3", "ac3", "aac"}, MinCustomFormatScore: &native.MinFormatScore}
	seen := map[string]bool{}
	var walk func(json.RawMessage, bool) error
	walk = func(raw json.RawMessage, parent bool) error {
		var name string
		if json.Unmarshal(raw, &name) == nil {
			if !parent {
				return nil
			}
			q, source, height, err := parseImportedQuality(name)
			if err != nil {
				return err
			}
			if !seen[q] {
				p.AllowedQualities = append(p.AllowedQualities, q)
				seen[q] = true
			}
			if height > p.MaxHeight {
				p.MaxHeight = height
			}
			if height > 0 && (p.MinHeight == 0 || height < p.MinHeight) {
				p.MinHeight = height
			}
			hasSource := false
			for _, s := range p.PreferredSources {
				if s == source {
					hasSource = true
				}
			}
			if !hasSource {
				p.PreferredSources = append(p.PreferredSources, source)
			}
			return nil
		}
		var item struct {
			Name    string            `json:"name"`
			Allowed bool              `json:"allowed"`
			Items   []json.RawMessage `json:"items"`
		}
		if err := json.Unmarshal(raw, &item); err != nil {
			return err
		}
		if !item.Allowed || !parent {
			return nil
		}
		if len(item.Items) > 0 {
			for _, child := range item.Items {
				if err := walk(child, true); err != nil {
					return err
				}
			}
			return nil
		}
		b, _ := json.Marshal(item.Name)
		return walk(b, true)
	}
	for _, item := range native.Items {
		if err := walk(item, true); err != nil {
			return QualityImport{}, err
		}
	}
	if len(p.AllowedQualities) == 0 {
		return QualityImport{}, fmt.Errorf("quality profile: no playable qualities enabled")
	}
	return QualityImport{Profile: p, FormatIDs: native.FormatItems}, nil
}
func parseImportedQuality(name string) (string, core.Source, int, error) {
	sourceName, resolution, _ := strings.Cut(name, "-")
	var source core.Source
	switch strings.ToLower(sourceName) {
	case "webdl":
		source = core.SourceWEBDL
	case "webrip":
		source = core.SourceWEBRip
	case "bluray":
		source = core.SourceBluRay
	case "remux":
		source = core.SourceRemux
	case "hdtv":
		source = core.SourceHDTV
	case "dvd":
		source = core.SourceDVD
	default:
		return "", "", 0, fmt.Errorf("quality profile: unsupported enabled quality %q", name)
	}
	height := 480
	if resolution != "" {
		var err error
		height, err = strconv.Atoi(strings.TrimSuffix(resolution, "p"))
		if err != nil || height <= 0 {
			return "", "", 0, fmt.Errorf("quality profile: invalid resolution %q", name)
		}
	}
	return fmt.Sprintf("%s-%d", source, height), source, height, nil
}
func releaseQuality(info core.ReleaseInfo) string {
	return fmt.Sprintf("%s-%d", info.Source, info.Height)
}
