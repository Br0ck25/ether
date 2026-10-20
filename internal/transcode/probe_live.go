package transcode

import (
	"context"

	"github.com/braedonsaunders/ether/internal/core"
)

// ProbeLive accepts complete audio/video parameters without a finite duration.
// Requiring a duration on an endless feed always forces the expensive retry.
// This optional capability leaves ordinary file probing unchanged.
func (m *Manager) ProbeLive(ctx context.Context, inputURL string) (*core.MediaInfo, error) {
	return probeMode(ctx, m.runner, m.ffprobe, inputURL, true)
}
