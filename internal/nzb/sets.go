package nzb

import (
	"sort"
)

// Set is the ordered volumes of one archive (RAR, 7z or plain split).
type Set struct {
	Kind Kind
	// Name is the lower-cased set name shared by the volumes
	// ("movie.2024" for movie.2024.part01.rar, "movie.mkv" for movie.mkv.001).
	Name string
	// NewStyle is true for .partNN.rar sets.
	NewStyle bool
	// Volumes in order. Volumes[i].Info.Volume is normalised so the first
	// expected volume is 0; gaps are listed in Missing.
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
		f.Info.Volume = v
		s.Volumes = append(s.Volumes, f)
	}
	return s
}
