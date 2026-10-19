package transcode

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestSRTToVTT(t *testing.T) {
	src := "\xef\xbb\xbf1\r\n00:00:01,000 --> 00:00:03,500\r\n<i>Hello</i> & <font color=\"red\">welcome</font>\r\n{\\an8}Top line\r\n\r\n" +
		"2\r\n01:02:03,04 --> 01:02:05,999\r\nA < B --> C\r\n\r\n\r\n" +
		"garbage block\r\n\r\n" +
		"3\n00:10:00,000 --> 00:10:01,000\nLast\n"
	var out bytes.Buffer
	if err := SRTToVTT(&out, strings.NewReader(src)); err != nil {
		t.Fatal(err)
	}
	want := "WEBVTT\n\n" +
		"00:00:01.000 --> 00:00:03.500\n<i>Hello</i> &amp; welcome\nTop line\n\n" +
		"01:02:03.040 --> 01:02:05.999\nA &lt; B --&gt; C\n\n" +
		"00:10:00.000 --> 00:10:01.000\nLast\n\n"
	if out.String() != want {
		t.Errorf("got:\n%q\nwant:\n%q", out.String(), want)
	}
}

func TestVTTToSRT(t *testing.T) {
	src := `WEBVTT - Example
Kind: captions

STYLE
::cue { color: yellow }

NOTE this is a comment

intro
00:01.000 --> 00:04.000 line:0 align:start
<v Roger>Hi &amp; <c.loud>bye</c></v>
<i>second</i>

00:01:05.500 --> 00:01:07.000
Two
`
	var out bytes.Buffer
	if err := VTTToSRT(&out, strings.NewReader(src)); err != nil {
		t.Fatal(err)
	}
	want := "1\n00:00:01,000 --> 00:00:04,000\nHi & bye\n<i>second</i>\n\n2\n00:01:05,500 --> 00:01:07,000\nTwo\n\n"
	if out.String() != want {
		t.Errorf("got:\n%q\nwant:\n%q", out.String(), want)
	}
}

func TestSubtitleRoundTrip(t *testing.T) {
	srt := "1\n00:00:01,000 --> 00:00:02,000\nOne\n\n2\n00:00:03,250 --> 00:00:04,000\nTwo\nlines\n\n"
	var vtt, back bytes.Buffer
	if err := SRTToVTT(&vtt, strings.NewReader(srt)); err != nil {
		t.Fatal(err)
	}
	if err := VTTToSRT(&back, &vtt); err != nil {
		t.Fatal(err)
	}
	if back.String() != srt {
		t.Errorf("round trip:\n%q\nwant\n%q", back.String(), srt)
	}
}

func TestSubtitleErrors(t *testing.T) {
	if err := SRTToVTT(&bytes.Buffer{}, strings.NewReader("no cues here\n")); err != ErrNoCues {
		t.Errorf("err = %v", err)
	}
}

func TestLatin1Subtitles(t *testing.T) {
	src := []byte("1\n00:00:01,000 --> 00:00:02,000\nCaf\xe9\n")
	var out bytes.Buffer
	if err := SRTToVTT(&out, bytes.NewReader(src)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Café") {
		t.Errorf("got %q", out.String())
	}
}

func TestParseCueTime(t *testing.T) {
	cases := map[string]time.Duration{
		"00:00:01,000": time.Second,
		"01:02.5":      time.Minute + 2*time.Second + 500*time.Millisecond,
		"10:00:00.001": 10*time.Hour + time.Millisecond,
	}
	for in, want := range cases {
		got, err := parseCueTime(in)
		if err != nil || got != want {
			t.Errorf("parseCueTime(%q) = %v,%v want %v", in, got, err, want)
		}
	}
	if _, err := parseCueTime("1"); err == nil {
		t.Error("expected error")
	}
}

func TestProgressParser(t *testing.T) {
	var p progressParser
	lines := []string{"frame=240", "fps=48.5", "bitrate=1234.5kbits/s", "out_time_us=10000000", "speed=2.5x", "progress=continue"}
	var got Progress
	var ok bool
	for _, l := range lines {
		got, ok = p.line(l)
	}
	if !ok || got.Frame != 240 || got.FPS != 48.5 || got.OutTime != 10*time.Second || got.Speed != 2.5 || got.Ended {
		t.Errorf("got %+v ok=%v", got, ok)
	}
	if got, ok = p.line("progress=end"); !ok || !got.Ended {
		t.Errorf("end: %+v", got)
	}
}

func TestLineWriter(t *testing.T) {
	var lines []string
	w := &lineWriter{fn: func(s string) { lines = append(lines, s) }}
	_, _ = w.Write([]byte("a=1\nb="))
	_, _ = w.Write([]byte("2\r\n\nc=3\r"))
	if strings.Join(lines, ",") != "a=1,b=2,c=3" {
		t.Errorf("lines %q", lines)
	}
	tb := newTailBuffer(2)
	tb.add("1")
	tb.add("2")
	tb.add("3")
	if tb.String() != "2\n3" {
		t.Errorf("tail %q", tb.String())
	}
}
