package core

import (
	"context"
	"errors"
)

// Offline downloads (Plex Downloads / the Download Queue API): a title is
// resolved like a playback session (ClientCaps.Download set), then its
// streams are copied into a standalone MP4 the client fetches whole.

// ErrDownloadNeedsTranscode is returned by DownloadRemuxer when the
// resolved streams cannot be copied into an MP4 a mobile client plays
// (e.g. AV1/VP9 video, TrueHD/DTS audio). Ether downloads never transcode
// offline, so the download decision reports this honestly as an error
// instead of silently re-encoding.
var ErrDownloadNeedsTranscode = errors.New("download: source codecs cannot be copied to MP4")

// ErrDownloadBudget is returned when a download would exceed the
// temporary disk budget for remuxed files.
var ErrDownloadBudget = errors.New("download: temporary disk budget exceeded")

// DownloadRemuxRequest is one whole-file remux to MP4.
type DownloadRemuxRequest struct {
	// InputURL is the loopback URL of the playback session feeding ffmpeg.
	InputURL string
	// Info is the probed media info of the resolved source.
	Info MediaInfo
	// AudioStream is the source stream index to keep; -1 picks the
	// default (first) audio stream.
	AudioStream int
}

// DownloadRemuxer copies a resolved source into a temporary faststart MP4.
// Implemented by internal/transcode. The returned path is owned by the
// remuxer until ReleaseDownload is called; files are never kept
// permanently and are bounded by a disk budget.
type DownloadRemuxer interface {
	// RemuxDownload runs ffmpeg -c copy to a temp MP4, reporting progress
	// as a fraction in [0,1] when progress is non-nil. Cancelling ctx
	// kills ffmpeg and removes the partial file.
	RemuxDownload(ctx context.Context, req DownloadRemuxRequest, progress func(fraction float64)) (path string, size int64, err error)
	// ReleaseDownload deletes a remuxed file and frees its budget.
	// Releasing an unknown path is a no-op.
	ReleaseDownload(path string)
}
