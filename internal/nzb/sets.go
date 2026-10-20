package nzb

import (
	"path"
	"sort"
	"strings"
)

// Set is the ordered volumes of one archive (RAR, 7z or plain split).
type Set struct {
	Kind Kind
	// Name is the lower-cased set name shared by the volumes
	// ("movie.2024" for movie.2024.part01.rar, "movie.mkv" for movie.mkv.001).
	Name string
	// NewStyle is true for .partNN.rar sets.
	NewStyle bool
	// Volumes in order; gaps are listed in Missing. The files' Info is left
	// untouched (a part00/.000 set keeps numbering from -1), so grouping the
	// same files again gives the same result and concurrent callers do not
	// write shared files.
	Volumes []*File
	// Missing lists absent 0-based volume numbers below the highest present.
	Missing []int
	// Duplicates are reposted volumes that lost to a larger copy.
	Duplicates []*File
}

// Bytes sums the listed sizes of the set's volumes.
func (s *Set) Bytes() int64 {
	var n int64
	for _, f := range s.Volumes {
		n += f.Bytes()
	}
	return n
}

// Complete reports whether no volume is missing (as far as names tell).
func (s *Set) Complete() bool { return len(s.Missing) == 0 }

type setKey struct {
	kind     Kind
	name     string
	newStyle bool
}

// ArchiveSets groups archive volumes by set and orders them. Sets are
// returned largest first.
func (n *NZB) ArchiveSets() []*Set {
	return GroupSets(n.Files)
}

// GroupSets groups the archive volumes among files (classified by their
// Info) into ordered sets, largest first.
func GroupSets(files []*File) []*Set {
	groups := map[setKey][]*File{}
	var order []setKey
	for _, f := range files {
		if !f.Info.Kind.IsArchive() || f.Info.Volume < -1 {
			continue
		}
		k := setKey{f.Info.Kind, f.Info.SetName, f.Info.NewStyle}
		if _, ok := groups[k]; !ok {
			order = append(order, k)
		}
		groups[k] = append(groups[k], f)
	}
	sets := make([]*Set, 0, len(order))
	for _, k := range order {
		sets = append(sets, buildSet(k, groups[k]))
	}
	sort.SliceStable(sets, func(i, j int) bool { return sets[i].Bytes() > sets[j].Bytes() })
	return sets
}

func buildSet(k setKey, files []*File) *Set {
	s := &Set{Kind: k.kind, Name: k.name, NewStyle: k.newStyle}
	// Lowest volume number present; part00/.000 numbering starts at -1.
	minVol := 0
	for _, f := range files {
		if f.Info.Volume < minVol {
			minVol = f.Info.Volume
		}
	}
	byVol := map[int]*File{}
	for _, f := range files {
		v := f.Info.Volume - minVol
		if cur, ok := byVol[v]; ok {
			if f.Bytes() > cur.Bytes() {
				s.Duplicates = append(s.Duplicates, cur)
				byVol[v] = f
			} else {
				s.Duplicates = append(s.Duplicates, f)
			}
			continue
		}
		byVol[v] = f
	}
	maxVol := -1
	for v := range byVol {
		if v > maxVol {
			maxVol = v
		}
	}
	for v := 0; v <= maxVol; v++ {
		f, ok := byVol[v]
		if !ok {
			s.Missing = append(s.Missing, v)
			continue
		}
		s.Volumes = append(s.Volumes, f)
	}
	return s
}

// IsPar2Volume reports whether name is a PAR2 recovery volume
// ("x.vol03+04.par2") rather than an index file ("x.par2").
func IsPar2Volume(name string) bool {
	m := rePar2.FindStringSubmatch(strings.ToLower(path.Base(strings.ReplaceAll(name, `\`, "/"))))
	return m != nil && m[2] != ""
}

// Par2Index returns the PAR2 index file among files: the smallest file
// classified as PAR2 that is not a recovery volume (preferring one that is
// not a sample's), or nil. The index holds only metadata packets (file
// names, sizes, hashes), typically a few KB.
func Par2Index(files []*File) *File {
	var best *File
	for _, f := range files {
		if f.Info.Kind != KindPar2 || IsPar2Volume(f.Name) || len(f.Segments) == 0 {
			continue
		}
		if best == nil || (best.Info.Sample && !f.Info.Sample) ||
			(best.Info.Sample == f.Info.Sample && f.Bytes() < best.Bytes()) {
			best = f
		}
	}
	return best
}
