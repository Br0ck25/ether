package app

import (
	"context"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/transcode"
)

// Playback can use release estimates when optional ffprobe is unavailable.
// The Plex API receives a nil transcoder and never advertises HLS availability.
type directOnly struct{}

func (directOnly) Probe(context.Context, string) (*core.MediaInfo, error) {
	return nil, core.ErrNotAvailable
}
func (directOnly) Decide(info core.MediaInfo, caps core.ClientCaps, opts core.DecideOptions) core.Decision {
	return transcode.Decide(info, caps, opts, transcode.Policy{})
}
func (directOnly) StartHLS(context.Context, core.HLSRequest) (core.HLSSession, error) {
	return nil, core.ErrNotAvailable
}
func (directOnly) Session(string) (core.HLSSession, bool) { return nil, false }
func (directOnly) Stop(string)                            {}
func (directOnly) HWAccel() core.HWAccel                  { return core.HWAccel{} }
