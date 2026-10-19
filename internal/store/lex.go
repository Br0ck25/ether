package store

import (
	"strconv"
	"strings"
	"sync"
)

// segment is a run of SQL text that is either plain code or an opaque
// literal/comment that rewriting must leave alone.
type segment struct {
	text string
	code bool
}

// splitSQL cuts q into code and non-code segments. Non-code segments are
// single-quoted strings (with ” escapes, and backslash escapes for E'...'),
// double-quoted identifiers, dollar-quoted strings, -- line comments and
// (nested) /* block comments */. An unterminated literal or comment runs to
// the end of the text, which is how both engines would read it too.
func splitSQL(q string) []segment {
	var out []segment
	start := 0
	emit := func(end int, code bool) {
		if end > start {
			out = append(out, segment{q[start:end], code})
		}
		start = end
	}
	for i := 0; i < len(q); {
		c := q[i]
		switch {
		case c == '\'':
			escapes := i > 0 && (q[i-1] == 'E' || q[i-1] == 'e') && (i < 2 || !isIdentByte(q[i-2]))
			emit(i, true)
			i = endQuoted(q, i, '\'', escapes)
			emit(i, false)
		case c == '"':
			emit(i, true)
			i = endQuoted(q, i, '"', false)
			emit(i, false)
		case c == '-' && i+1 < len(q) && q[i+1] == '-':
			emit(i, true)
			if nl := strings.IndexByte(q[i:], '\n'); nl >= 0 {
				i += nl + 1
			} else {
				i = len(q)
			}
			emit(i, false)
		case c == '/' && i+1 < len(q) && q[i+1] == '*':
			emit(i, true)
			i = endBlockComment(q, i)
			emit(i, false)
		case c == '$' && (i == 0 || !isIdentByte(q[i-1])):
			if tag, ok := dollarTag(q[i:]); ok {
				emit(i, true)
				if end := strings.Index(q[i+len(tag):], tag); end >= 0 {
					i += len(tag) + end + len(tag)
				} else {
					i = len(q)
				}
				emit(i, false)
				continue
			}
			i++
		default:
			i++
		}
	}
	emit(len(q), true)
	return out
}

func isIdentByte(c byte) bool {
	return c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80
}

// endQuoted returns the index just past the literal opened by q[i]==quote.
// A doubled quote is an escaped quote; with escapes a backslash escapes the
// next byte.
func endQuoted(q string, i int, quote byte, escapes bool) int {
	for j := i + 1; j < len(q); j++ {
		switch q[j] {
		case '\\':
			if escapes {
				j++
			}
		case quote:
			if j+1 < len(q) && q[j+1] == quote {
				j++
				continue
			}
			return j + 1
		}
	}
	return len(q)
}

func endBlockComment(q string, i int) int {
	depth := 0
	for j := i; j+1 < len(q); j++ {
		switch {
		case q[j] == '/' && q[j+1] == '*':
			depth++
			j++
		case q[j] == '*' && q[j+1] == '/':
			depth--
			j++
			if depth == 0 {
				return j + 1
			}
		}
	}
	return len(q)
}

// dollarTag recognises a PostgreSQL dollar-quote opener ($$ or $tag$) at
// the start of s. A $ followed by a digit is a positional parameter.
func dollarTag(s string) (string, bool) {
	if len(s) < 2 || s[0] != '$' {
		return "", false
	}
	if s[1] >= '0' && s[1] <= '9' {
		return "", false
	}
	for j := 1; j < len(s); j++ {
		switch c := s[j]; {
		case c == '$':
			return s[:j+1], true
		case isIdentByte(c):
		default:
			return "", false
		}
	}
	return "", false
}

// Rebind rewrites `?` placeholders as PostgreSQL's $1..$n, leaving any `?`
// inside string literals, quoted identifiers and comments untouched. The
// second result is the number of placeholders.
func Rebind(q string) (string, int) {
	if strings.IndexByte(q, '?') < 0 {
		return q, 0
	}
	var b strings.Builder
	b.Grow(len(q) + 16)
	n := 0
	for _, seg := range splitSQL(q) {
		if !seg.code {
			b.WriteString(seg.text)
			continue
		}
		s := seg.text
		for {
			k := strings.IndexByte(s, '?')
			if k < 0 {
				b.WriteString(s)
				break
			}
			b.WriteString(s[:k])
			n++
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(n))
			s = s[k+1:]
		}
	}
	if n == 0 {
		return q, 0
	}
	return b.String(), n
}

// rebindCache memoises Rebind. Queries are overwhelmingly static strings;
// the cache is cleared wholesale if dynamically built SQL ever fills it.
type rebindCache struct {
	mu sync.RWMutex
	m  map[string]string
}

const rebindCacheMax = 4096

func (c *rebindCache) get(q string) string {
	if strings.IndexByte(q, '?') < 0 {
		return q
	}
	c.mu.RLock()
	r, ok := c.m[q]
	c.mu.RUnlock()
	if ok {
		return r
	}
	r, _ = Rebind(q)
	c.mu.Lock()
	if c.m == nil || len(c.m) >= rebindCacheMax {
		c.m = make(map[string]string, 256)
	}
	c.m[q] = r
	c.mu.Unlock()
	return r
}
