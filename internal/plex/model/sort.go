package model

import (
	"reflect"
	"sort"
)

// AttrSorter is implemented by elements whose attributes PMS emits in
// alphabetical order: MediaContainer always (after size/totalSize/offset),
// and items inside transcode decision responses (with the decision
// attributes appended at the end).
type AttrSorter interface {
	AttrOrder() (sorted bool, leading, trailing []string)
}

// AttrOrder implements AttrSorter.
func (MediaContainer) AttrOrder() (bool, []string, []string) {
	return true, []string{"size", "totalSize", "offset"}, nil
}

var decisionTrailing = []string{"decision", "selected", "location"}

// SetAlphabetical switches the element to PMS's decision-response ordering.
func (m *Metadata) SetAlphabetical() { m.alpha = true }

// AttrOrder implements AttrSorter.
func (m Metadata) AttrOrder() (bool, []string, []string) { return m.alpha, nil, decisionTrailing }

// SetAlphabetical switches the element to PMS's decision-response ordering.
func (m *Media) SetAlphabetical() { m.alpha = true }

// AttrOrder implements AttrSorter.
func (m Media) AttrOrder() (bool, []string, []string) { return m.alpha, nil, decisionTrailing }

// SetAlphabetical switches the element to PMS's decision-response ordering.
func (p *Part) SetAlphabetical() { p.alpha = true }

// AttrOrder implements AttrSorter.
func (p Part) AttrOrder() (bool, []string, []string) { return p.alpha, nil, decisionTrailing }

// SetAlphabetical switches the element to PMS's decision-response ordering.
func (s *Stream) SetAlphabetical() { s.alpha = true }

// AttrOrder implements AttrSorter.
func (s Stream) AttrOrder() (bool, []string, []string) { return s.alpha, nil, decisionTrailing }

// sortAttrs applies AttrSorter ordering: leading names first (in the given
// order), then the rest alphabetically, then trailing names (in order).
// Elements that don't sort keep struct-field order.
func sortAttrs[T any](v reflect.Value, attrs []T, name func(T) string) []T {
	if !v.CanInterface() {
		return attrs
	}
	s, ok := v.Interface().(AttrSorter)
	if !ok {
		return attrs
	}
	sorted, leading, trailing := s.AttrOrder()
	if !sorted {
		return attrs
	}
	rank := func(n string) int {
		for i, l := range leading {
			if l == n {
				return i - len(leading) // negative: before everything
			}
		}
		for i, t := range trailing {
			if t == n {
				return 1 + i // after the alphabetical middle (rank 0)
			}
		}
		return 0
	}
	out := append([]T(nil), attrs...)
	sort.SliceStable(out, func(i, j int) bool {
		ri, rj := rank(name(out[i])), rank(name(out[j]))
		if ri != rj {
			return ri < rj
		}
		if ri != 0 {
			return false
		}
		return name(out[i]) < name(out[j])
	})
	return out
}
