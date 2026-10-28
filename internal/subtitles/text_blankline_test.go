package subtitles

import (
	"testing"
	"time"
)

// SRT files often separate cues with a line holding only whitespace; each
// cue must still be parsed separately.
func TestParseCuesWhitespaceSeparator(t *testing.T) {
	srt := "1\r\n00:00:01,000 --> 00:00:02,000\r\nHello\r\n \r\n2\r\n00:00:03,000 --> 00:00:04,000\r\nWorld\r\n\t\r\n"
	cues, err := ParseCues(srt)
	if err != nil {
		t.Fatal(err)
	}
	if len(cues) != 2 || cues[0].Text != "Hello" || cues[1].Text != "World" || cues[1].Start != 3*time.Second {
		t.Fatalf("cues = %+v", cues)
	}
}

// ASS centisecond times must not lose a millisecond to float truncation.
func TestASSTimeRounding(t *testing.T) {
	if got := assTime("0:00:02.01"); got != 2010*time.Millisecond {
		t.Fatalf("assTime = %v, want 2.01s", got)
	}
}
