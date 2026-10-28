package service

import (
	"sort"
	"strings"
)

// dataDirEnv is the variable the service reads its data directory from.
const dataDirEnv = "ETHER_DATA_DIR"

// mergeServiceEnv computes the environment block ("KEY=value" entries) for a
// service registration that is being updated in place. existing is what the
// service manager holds now; want is what this install renders. Entries in
// want replace or add to existing ones, except that keepDataDir leaves the
// registered ETHER_DATA_DIR (or its absence) untouched: an upgrade must not
// point an owner's service at a different, empty data directory. Unrelated
// existing entries (an owner's own additions) are kept. The result is
// sorted so the rendering is deterministic.
func mergeServiceEnv(existing []string, want map[string]string, keepDataDir bool) []string {
	merged := make(map[string]string, len(existing)+len(want))
	var order []string
	set := func(k, v string) {
		if _, ok := merged[k]; !ok {
			order = append(order, k)
		}
		merged[k] = v
	}
	for _, kv := range existing {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			continue
		}
		set(k, v)
	}
	for k, v := range want {
		if keepDataDir && strings.EqualFold(k, dataDirEnv) {
			continue
		}
		// Environment names are case-insensitive on Windows: replace an
		// existing spelling rather than adding a duplicate.
		for _, old := range order {
			if old != k && strings.EqualFold(old, k) {
				delete(merged, old)
			}
		}
		set(k, v)
	}
	out := make([]string, 0, len(merged))
	for k, v := range merged {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}

// windowsBinaryPath renders the SCM ImagePath for exe and args: the
// executable always quoted (Program Files has a space; an unquoted path is
// also the classic unquoted-service-path hole) and arguments quoted per
// the Windows command-line rules when they need it.
func windowsBinaryPath(exe string, args []string) string {
	var b strings.Builder
	b.WriteString(`"` + exe + `"`)
	for _, a := range args {
		b.WriteByte(' ')
		b.WriteString(windowsQuoteArg(a))
	}
	return b.String()
}

// windowsQuoteArg quotes one argument like syscall.EscapeArg (which only
// exists in Windows builds).
func windowsQuoteArg(s string) string {
	if s == "" {
		return `""`
	}
	if !strings.ContainsAny(s, " \t\"") {
		return s
	}
	var b strings.Builder
	b.WriteByte('"')
	slashes := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '\\':
			slashes++
		case '"':
			b.WriteString(strings.Repeat(`\`, slashes+1))
			slashes = 0
		default:
			slashes = 0
		}
		b.WriteByte(c)
	}
	b.WriteString(strings.Repeat(`\`, slashes))
	b.WriteByte('"')
	return b.String()
}
