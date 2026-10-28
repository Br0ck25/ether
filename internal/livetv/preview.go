package livetv

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/go-chi/chi/v5"
)

// previewCaps describes the owner's browser player (hls.js over Media Source
// Extensions): H.264/AAC in MPEG-TS segments, which is what both the
// virtual-channel transcoder and typical IPTV HLS deliver.
var previewCaps = core.ClientCaps{
	Product:       "Ether Preview",
	Platform:      "Web",
	Device:        "Browser",
	DeviceName:    "Owner preview",
	MaxHeight:     1080,
	VideoCodecs:   []string{"h264"},
	AudioCodecs:   []string{"aac", "mp3"},
	Containers:    []string{"mpegts", "mp4"},
	HLSContainers: []string{"mpegts"},
}

// PreviewSession is what the owner UI needs to play a tuned channel. Stream
// URLs never leave the server; the playlist is relative to the livetv API.
type PreviewSession struct {
	ID               string     `json:"id"`
	Playlist         string     `json:"playlist"`
	ActiveSourceID   string     `json:"activeSourceId,omitempty"`
	ActiveSourceName string     `json:"activeSourceName,omitempty"`
	Programme        *Programme `json:"programme,omitempty"`
}

// previewRoutes lets the owner test a channel from the admin UI: tune it as
// an ordinary session (same source selection, failover and limits as a Plex
// client), then fetch the HLS playlist and segments through the owner API.
func (s *Service) previewRoutes(r chi.Router, owner *core.User) {
	r.Post("/channels/{id}/play", func(w http.ResponseWriter, r *http.Request) {
		caps := previewCaps
		caps.ClientID = "ether-preview-" + randomID()[:12]
		info, err := s.TuneWithSession(r.Context(), owner, chi.URLParam(r, "id"), caps, caps.ClientID)
		if err != nil {
			ownerResult(w, nil, err)
			return
		}
		out := PreviewSession{
			ID:             info.ID,
			Playlist:       "sessions/" + info.ID + "/index.m3u8",
			ActiveSourceID: info.ActiveSourceID,
			Programme:      info.Programme,
		}
		// Session snapshots carry no stream details; name the input from the
		// stored channel.
		if c, err := s.Channel(r.Context(), chi.URLParam(r, "id")); err == nil {
			for _, src := range c.StreamSources {
				if src.ID == info.ActiveSourceID {
					out.ActiveSourceName = src.Name
				}
			}
		}
		ownerResult(w, out, nil)
	})
	r.Delete("/sessions/{session}", func(w http.ResponseWriter, r *http.Request) {
		ownerResult(w, map[string]bool{"ok": true}, s.Stop(r.Context(), chi.URLParam(r, "session"), owner))
	})
	r.Get("/sessions/{session}", func(w http.ResponseWriter, r *http.Request) {
		info, err := s.Session(r.Context(), chi.URLParam(r, "session"), owner)
		if err != nil {
			ownerResult(w, nil, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"id": info.ID, "activeSourceId": info.ActiveSourceID, "programme": info.Programme})
	})
	r.Get("/sessions/{session}/index.m3u8", func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "session")
		pw := relativePlaylists(w, r, id)
		s.ServePlaylist(pw, r, id, owner)
		pw.finish()
	})
	r.Get("/sessions/{session}/relay/{segment}", func(w http.ResponseWriter, r *http.Request) {
		n, err := strconv.Atoi(strings.TrimSuffix(chi.URLParam(r, "segment"), ".ts"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		s.ServeRelaySegment(w, r, chi.URLParam(r, "session"), n, owner)
	})
	r.Get("/sessions/{session}/resource/{resource}", func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "session")
		pw := relativePlaylists(w, r, id)
		s.ServeResource(pw, r, id, chi.URLParam(r, "resource"), owner)
		pw.finish()
	})
	r.Get("/sessions/{session}/program/{start}/{segment}", func(w http.ResponseWriter, r *http.Request) {
		start, e := strconv.ParseInt(chi.URLParam(r, "start"), 10, 64)
		n, e2 := strconv.Atoi(strings.TrimSuffix(chi.URLParam(r, "segment"), ".ts"))
		if e != nil || e2 != nil {
			http.NotFound(w, r)
			return
		}
		s.ServeSegment(w, r, chi.URLParam(r, "session"), start, n, owner)
	})
}

// relativePlaylists rewrites the Plex-adapter session paths that playlists
// carry ("/livetv/sessions/{id}/0/relay/1.ts") onto this owner API's session
// path (e.g. "/ether/api/livetv/sessions/{id}/relay/1.ts"), so the browser
// fetches every nested playlist and segment through the owner API. Anything
// that is not a playlist streams through untouched.
func relativePlaylists(w http.ResponseWriter, r *http.Request, id string) *playlistRewriter {
	// The admin API mounts this handler behind StripPrefix, so the original
	// request URI is the only place the full public path survives.
	path := r.URL.Path
	if u, err := url.ParseRequestURI(r.RequestURI); err == nil && u.Path != "" {
		path = u.Path
	}
	marker := "/sessions/" + id + "/"
	base := marker
	if i := strings.Index(path, marker); i >= 0 {
		base = path[:i+len(marker)]
	}
	return &playlistRewriter{ResponseWriter: w, prefix: []byte("/livetv/sessions/" + id + "/0/"), base: []byte(base)}
}

type playlistRewriter struct {
	http.ResponseWriter
	prefix   []byte
	base     []byte
	status   int
	decided  bool
	playlist bool
	buf      bytes.Buffer
}

func (p *playlistRewriter) decide() {
	if p.decided {
		return
	}
	p.decided = true
	ct := strings.ToLower(p.Header().Get("Content-Type"))
	p.playlist = strings.Contains(ct, "mpegurl")
	if p.playlist {
		p.Header().Del("Content-Length")
	}
}

func (p *playlistRewriter) WriteHeader(code int) {
	p.decide()
	if p.playlist {
		p.status = code
		return
	}
	p.ResponseWriter.WriteHeader(code)
}

func (p *playlistRewriter) Write(b []byte) (int, error) {
	p.decide()
	if p.playlist {
		return p.buf.Write(b)
	}
	return p.ResponseWriter.Write(b)
}

func (p *playlistRewriter) Flush() {
	if p.playlist {
		return
	}
	if f, ok := p.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// finish writes a buffered playlist with session paths moved onto the owner API.
func (p *playlistRewriter) finish() {
	if !p.playlist {
		return
	}
	if p.status != 0 {
		p.ResponseWriter.WriteHeader(p.status)
	}
	p.ResponseWriter.Write(bytes.ReplaceAll(p.buf.Bytes(), p.prefix, p.base))
}
