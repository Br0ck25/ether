package model

import "reflect"

// StringAttrer is implemented by elements some of whose attributes PMS
// renders as JSON strings: decision responses quote ids and a few flags
// ("id": "221632", "allowSync": "1").
type StringAttrer interface {
	StringAttrs() []string
}

// SetDecision switches the container to PMS's decision-response typing.
func (m *MediaContainer) SetDecision() { m.decision = true }

// StringAttrs implements StringAttrer.
func (m MediaContainer) StringAttrs() []string {
	if !m.decision {
		return nil
	}
	return []string{"allowSync", "librarySectionID", "mediaTagVersion"}
}

// StringAttrs implements StringAttrer.
func (m Metadata) StringAttrs() []string {
	if !m.alpha {
		return nil
	}
	return []string{"librarySectionID"}
}

// StringAttrs implements StringAttrer.
func (m Media) StringAttrs() []string {
	if !m.alpha {
		return nil
	}
	return []string{"id", "hasVoiceActivity"}
}

// StringAttrs implements StringAttrer.
func (p Part) StringAttrs() []string {
	if !p.alpha {
		return nil
	}
	return []string{"id"}
}

// StringAttrs implements StringAttrer.
func (s Stream) StringAttrs() []string {
	if !s.alpha {
		return nil
	}
	return []string{"id", "canNormalizeLoudness"}
}

// quoteAttrs renders StringAttrer-listed attributes as JSON strings, with
// booleans in their XML form ("1"/"0").
func quoteAttrs(v reflect.Value, attrs []jsonAttr) []jsonAttr {
	if !v.CanInterface() {
		return attrs
	}
	s, ok := v.Interface().(StringAttrer)
	if !ok {
		return attrs
	}
	names := s.StringAttrs()
	if len(names) == 0 {
		return attrs
	}
	for i, a := range attrs {
		for _, n := range names {
			if a.name != n || a.quoted {
				continue
			}
			switch a.value {
			case "true":
				a.value = "1"
			case "false":
				a.value = "0"
			}
			a.quoted = true
			attrs[i] = a
		}
	}
	return attrs
}
