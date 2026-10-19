package transcode

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Cue is one timed subtitle.
type Cue struct {
	Start, End time.Duration
	// Settings are WebVTT cue settings ("line:0 align:start"); dropped in SRT.
	Settings string
	Text     string // lines joined with \n
}

// ErrNoCues means the input held no parsable subtitle cues.
var ErrNoCues = errors.New("transcode: no subtitle cues found")

var (
	// 00:01:02,345 --> 00:01:04,000 (SRT) or 01:02.345 --> 01:04.000 X:Y (VTT)
	cueTimeRE = regexp.MustCompile(`^\s*((?:\d+:)?\d{1,2}:\d{1,2}[,.]\d{1,3})\s*-->\s*((?:\d+:)?\d{1,2}:\d{1,2}[,.]\d{1,3})(.*)$`)
	vttTagRE  = regexp.MustCompile(`</?(?:c|v|lang|ruby|rt)(?:[.\s][^>]*)?>|<\d{2}:[\d:.]+>`)
)

func parseCueTime(s string) (time.Duration, error) {
	s = strings.ReplaceAll(strings.TrimSpace(s), ",", ".")
	parts := strings.Split(s, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, fmt.Errorf("bad timestamp %q", s)
	}
	var h, m int
	var err error
	if len(parts) == 3 {
		if h, err = strconv.Atoi(parts[0]); err != nil {
			return 0, err
		}
		parts = parts[1:]
	}
	if m, err = strconv.Atoi(parts[0]); err != nil {
		return 0, err
	}
	secStr, frac, _ := strings.Cut(parts[1], ".")
	sec, err := strconv.Atoi(secStr)
	if err != nil {
		return 0, err
	}
	ms := 0
	if frac != "" {
		for len(frac) < 3 {
			frac += "0"
		}
		if ms, err = strconv.Atoi(frac[:3]); err != nil {
			return 0, err
		}
	}
	return time.Duration(h)*time.Hour + time.Duration(m)*time.Minute + time.Duration(sec)*time.Second +
		time.Duration(ms)*time.Millisecond, nil
}

func formatCueTime(d time.Duration, sep string) string {
	if d < 0 {
		d = 0
	}
	ms := d.Milliseconds()
	h := ms / 3600000
	m := ms / 60000 % 60
	s := ms / 1000 % 60
	return fmt.Sprintf("%02d:%02d:%02d%s%03d", h, m, s, sep, ms%1000)
}

// readBlocks splits subtitle text into blank-line separated blocks,
// normalising BOM, CRLF and invalid UTF-8 (Latin-1 fallback).
func readBlocks(r io.Reader) ([][]string, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	b = bytes.TrimPrefix(b, []byte("\xef\xbb\xbf"))
	if !utf8.Valid(b) {
		b = latin1ToUTF8(b)
	}
	var blocks [][]string
	var cur []string
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if strings.TrimSpace(line) == "" {
			if len(cur) > 0 {
				blocks = append(blocks, cur)
				cur = nil
			}
			continue
		}
		cur = append(cur, line)
	}
	if len(cur) > 0 {
		blocks = append(blocks, cur)
	}
	return blocks, sc.Err()
}

func latin1ToUTF8(b []byte) []byte {
	var out bytes.Buffer
	for _, c := range b {
		out.WriteRune(rune(c))
	}
	return out.Bytes()
}

// parseCues reads SRT or WebVTT cues.
func parseCues(r io.Reader) ([]Cue, error) {
	blocks, err := readBlocks(r)
	if err != nil {
		return nil, err
	}
	var cues []Cue
	for _, bl := range blocks {
		first := strings.TrimSpace(bl[0])
		if strings.HasPrefix(first, "WEBVTT") || strings.HasPrefix(first, "NOTE") ||
			first == "STYLE" || first == "REGION" {
			continue
		}
		// Find the timing line: first or second (after an index / cue id).
		ti := -1
		for i := 0; i < len(bl) && i < 2; i++ {
			if cueTimeRE.MatchString(bl[i]) {
				ti = i
				break
			}
		}
		if ti < 0 {
			continue
		}
		m := cueTimeRE.FindStringSubmatch(bl[ti])
		start, err1 := parseCueTime(m[1])
		end, err2 := parseCueTime(m[2])
		if err1 != nil || err2 != nil {
			continue
		}
		text := strings.Join(bl[ti+1:], "\n")
		cues = append(cues, Cue{Start: start, End: end, Settings: strings.TrimSpace(m[3]), Text: text})
	}
	if len(cues) == 0 {
		return nil, ErrNoCues
	}
	return cues, nil
}

// SRTToVTT converts SubRip to WebVTT (OpenSubtitles sidecars to HLS/Plex
// web clients).
func SRTToVTT(dst io.Writer, src io.Reader) error {
	cues, err := parseCues(src)
	if err != nil {
		return err
	}
	for i := range cues {
		cues[i].Settings = ""
		cues[i].Text = srtTextToVTT(cues[i].Text)
	}
	return WriteVTT(dst, cues)
}

var (
	assOverrideRE = regexp.MustCompile(`\{\\[^}]*\}`)
	fontTagRE     = regexp.MustCompile(`(?i)</?font[^>]*>`)
	allowedTagRE  = regexp.MustCompile(`(?i)^</?(?:i|b|u)>`)
	entityRE      = regexp.MustCompile(`^&(?:[a-zA-Z]+|#\d+|#x[0-9a-fA-F]+);`)
)

// srtTextToVTT drops SRT markup WebVTT lacks ({\an8}, <font>) and escapes
// stray & and < so the cue text is valid WebVTT.
func srtTextToVTT(t string) string {
	t = assOverrideRE.ReplaceAllString(t, "")
	t = fontTagRE.ReplaceAllString(t, "")
	var b strings.Builder
	for i := 0; i < len(t); i++ {
		switch t[i] {
		case '&':
			if entityRE.MatchString(t[i:]) {
				b.WriteByte('&')
			} else {
				b.WriteString("&amp;")
			}
		case '<':
			if m := allowedTagRE.FindString(t[i:]); m != "" {
				b.WriteString(strings.ToLower(m))
				i += len(m) - 1
			} else {
				b.WriteString("&lt;")
			}
		case '>':
			b.WriteString("&gt;")
		default:
			b.WriteByte(t[i])
		}
	}
	return b.String()
}

// VTTToSRT converts WebVTT to SubRip. Cue settings and WebVTT-only markup
// (voice, class, timestamp tags) are dropped; <i>, <b>, <u> are kept.
func VTTToSRT(dst io.Writer, src io.Reader) error {
	cues, err := parseCues(src)
	if err != nil {
		return err
	}
	return WriteSRT(dst, cues)
}

// WriteVTT writes cues as a WebVTT file.
func WriteVTT(dst io.Writer, cues []Cue) error {
	bw := bufio.NewWriter(dst)
	bw.WriteString("WEBVTT\n\n")
	for _, c := range cues {
		bw.WriteString(formatCueTime(c.Start, "."))
		bw.WriteString(" --> ")
		bw.WriteString(formatCueTime(c.End, "."))
		if c.Settings != "" {
			bw.WriteString(" " + c.Settings)
		}
		bw.WriteString("\n")
		// "-->" may not appear in cue text; a blank line would end the cue.
		text := strings.ReplaceAll(c.Text, "-->", "->")
		bw.WriteString(text)
		bw.WriteString("\n\n")
	}
	return bw.Flush()
}

// WriteSRT writes cues as a SubRip file.
func WriteSRT(dst io.Writer, cues []Cue) error {
	bw := bufio.NewWriter(dst)
	for i, c := range cues {
		fmt.Fprintf(bw, "%d\n%s --> %s\n", i+1, formatCueTime(c.Start, ","), formatCueTime(c.End, ","))
		text := vttTagRE.ReplaceAllString(c.Text, "")
		text = strings.NewReplacer("&amp;", "&", "&lt;", "<", "&gt;", ">", "&nbsp;", " ", "&lrm;", "‎", "&rlm;", "‏").Replace(text)
		bw.WriteString(text)
		bw.WriteString("\n\n")
	}
	return bw.Flush()
}
