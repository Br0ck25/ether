package nntptest

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"hash/crc32"
	"slices"
	"strconv"
	"time"

	"github.com/ether-media/ether/internal/yenc"
)

// PostOptions control Post. The zero value is valid.
type PostOptions struct {
	Poster string    // default "Ether Test <test@ether.invalid>"
	Groups []string  // default ["alt.binaries.test"]
	Date   time.Time // default 2024-01-01T00:00:00Z
	// Meta becomes <head><meta type="k">v</meta></head> (e.g. "title",
	// "password", "category").
	Meta map[string]string
	// LineLength for yEnc (0 = server default).
	LineLength int
	// Order lists file names in posting order; files not listed follow in
	// name order. Default: name order.
	Order []string
}

// PostedSegment is one article of a posted file.
type PostedSegment struct {
	Number    int    // 1-based
	MessageID string // without angle brackets, as in an NZB
	Bytes     int    // encoded article size (the NZB bytes attribute)
	Begin     int64  // 1-based inclusive offset in the file
	End       int64
}

// PostedFile describes one posted file.
type PostedFile struct {
	Name     string
	Subject  string
	Size     int64
	CRC32    uint32
	Segments []PostedSegment
}

// Posting is the result of Post.
type Posting struct {
	NZB   []byte
	Files []PostedFile
}

// Post stores every file as multipart yEnc articles of segmentSize decoded
// bytes (the last one shorter) with correct =ypart ranges, pcrc32 and
// whole-file crc32, and returns a newzbin NZB 1.1 document describing them.
func (s *Server) Post(files map[string][]byte, segmentSize int, opts PostOptions) (*Posting, error) {
	if segmentSize <= 0 {
		return nil, fmt.Errorf("nntptest: segment size must be positive")
	}
	if opts.Poster == "" {
		opts.Poster = "Ether Test <test@ether.invalid>"
	}
	if len(opts.Groups) == 0 {
		opts.Groups = []string{"alt.binaries.test"}
	}
	if opts.Date.IsZero() {
		opts.Date = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	names := make([]string, 0, len(files))
	for _, n := range opts.Order {
		if _, ok := files[n]; ok && !slices.Contains(names, n) {
			names = append(names, n)
		}
	}
	var rest []string
	for n := range files {
		if !slices.Contains(names, n) {
			rest = append(rest, n)
		}
	}
	slices.Sort(rest)
	names = append(names, rest...)

	post := &Posting{}
	batch := s.seq.Add(1)
	for fi, name := range names {
		data := files[name]
		nseg := max(1, (len(data)+segmentSize-1)/segmentSize)
		fileCRC := crc32.ChecksumIEEE(data)
		pf := PostedFile{
			Name:    name,
			Size:    int64(len(data)),
			CRC32:   fileCRC,
			Subject: fmt.Sprintf("[%d/%d] - %q yEnc (1/%d)", fi+1, len(names), name, nseg),
		}
		for i := 0; i < nseg; i++ {
			begin := i * segmentSize
			end := min(begin+segmentSize, len(data))
			id := fmt.Sprintf("part%dof%d.f%d.b%d.%08x@nntptest.ether", i+1, nseg, fi+1, batch, crc32.ChecksumIEEE([]byte(name)))
			n, err := s.AddYenc(id, data[begin:end], yenc.EncodeOptions{
				Name: name, LineLength: opts.LineLength,
				Part: i + 1, Total: nseg, Begin: int64(begin + 1), FileSize: int64(len(data)),
				FileCRC: fileCRC, HasFileCRC: true,
			})
			if err != nil {
				return nil, err
			}
			pf.Segments = append(pf.Segments, PostedSegment{
				Number: i + 1, MessageID: id, Bytes: n,
				Begin: int64(begin + 1), End: int64(end),
			})
		}
		post.Files = append(post.Files, pf)
	}
	post.NZB = BuildNZB(post.Files, opts)
	return post, nil
}

// PostFiles is Post with default options, returning just the NZB.
func (s *Server) PostFiles(files map[string][]byte, segmentSize int) ([]byte, error) {
	p, err := s.Post(files, segmentSize, PostOptions{})
	if err != nil {
		return nil, err
	}
	return p.NZB, nil
}

// BuildNZB renders files as a newzbin NZB 1.1 document. Empty fields of
// opts take Post's defaults.
func BuildNZB(files []PostedFile, opts PostOptions) []byte {
	if opts.Poster == "" {
		opts.Poster = "Ether Test <test@ether.invalid>"
	}
	if len(opts.Groups) == 0 {
		opts.Groups = []string{"alt.binaries.test"}
	}
	if opts.Date.IsZero() {
		opts.Date = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	var b bytes.Buffer
	esc := func(s string) string {
		var e bytes.Buffer
		_ = xml.EscapeText(&e, []byte(s))
		return e.String()
	}
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<!DOCTYPE nzb PUBLIC "-//newzBin//DTD NZB 1.1//EN" "http://www.newzbin.com/DTD/nzb/nzb-1.1.dtd">` + "\n")
	b.WriteString(`<nzb xmlns="http://www.newzbin.com/DTD/2003/nzb">` + "\n")
	if len(opts.Meta) > 0 {
		keys := make([]string, 0, len(opts.Meta))
		for k := range opts.Meta {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		b.WriteString("  <head>\n")
		for _, k := range keys {
			fmt.Fprintf(&b, "    <meta type=\"%s\">%s</meta>\n", esc(k), esc(opts.Meta[k]))
		}
		b.WriteString("  </head>\n")
	}
	date := strconv.FormatInt(opts.Date.Unix(), 10)
	for _, f := range files {
		fmt.Fprintf(&b, "  <file poster=\"%s\" date=\"%s\" subject=\"%s\">\n", esc(opts.Poster), date, esc(f.Subject))
		b.WriteString("    <groups>\n")
		for _, g := range opts.Groups {
			fmt.Fprintf(&b, "      <group>%s</group>\n", esc(g))
		}
		b.WriteString("    </groups>\n    <segments>\n")
		for _, sg := range f.Segments {
			fmt.Fprintf(&b, "      <segment bytes=\"%d\" number=\"%d\">%s</segment>\n", sg.Bytes, sg.Number, esc(sg.MessageID))
		}
		b.WriteString("    </segments>\n  </file>\n")
	}
	b.WriteString("</nzb>\n")
	return b.Bytes()
}
