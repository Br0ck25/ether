package transcode

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/braedonsaunders/ether/internal/core"
)

// ProbeLive accepts complete audio/video parameters without a finite duration.
// Requiring a duration on an endless feed always forces the expensive retry.
// This optional capability leaves ordinary file probing unchanged.
func (m *Manager) ProbeLive(ctx context.Context, inputURL string) (*core.MediaInfo, error) {
	// Live URLs (Xtream, tokenized M3U) carry account credentials.
	info, err := probeMode(ctx, redactingRunner{Runner: m.runner, input: inputURL}, m.ffprobe, inputURL, true)
	if err != nil && ctx.Err() == nil {
		err = fmt.Errorf("live probe: %s", strings.ReplaceAll(err.Error(), inputURL, "[source]"))
	}
	return info, err
}

// redactingRunner removes the input URL and secret header values from a
// command's stderr before anything truncates it: a secret cut in half by
// a tail no longer matches a later replace.
type redactingRunner struct {
	Runner
	input   string
	secrets []string
}

func (r redactingRunner) Run(ctx context.Context, c Command) error {
	if c.Stderr == nil {
		return r.Runner.Run(ctx, c)
	}
	var buf bytes.Buffer
	orig := c.Stderr
	c.Stderr = &buf
	err := r.Runner.Run(ctx, c)
	_, _ = io.WriteString(orig, r.redact(buf.String()))
	return err
}

func (r redactingRunner) redact(s string) string {
	if r.input != "" {
		s = strings.ReplaceAll(s, r.input, "[source]")
	}
	for _, v := range r.secrets {
		if v != "" {
			s = strings.ReplaceAll(s, v, "[redacted]")
		}
	}
	return s
}
