package jellyfin

import (
	"crypto/sha1"
	"encoding/hex"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

func ticks(d time.Duration) int64 { return d.Nanoseconds() / 100 }

func fromTicks(t int64) time.Duration { return time.Duration(t) * 100 }

// The JSON below is the BaseItemDto both servers use. Apps read a field by
// its exact name and several (Kotlin and Swift SDKs) fail on a missing
// required one, so Name, Id, ServerId and Type are never omitted.

type userData struct {
	PlaybackPositionTicks int64      `json:"PlaybackPositionTicks"`
	PlayCount             int        `json:"PlayCount"`
	IsFavorite            bool       `json:"IsFavorite"`
	Played                bool       `json:"Played"`
	Key                   string     `json:"Key"`
	ItemID                string     `json:"ItemId"`
	LastPlayedDate        *time.Time `json:"LastPlayedDate,omitempty"`
	PlayedPercentage      float64    `json:"PlayedPercentage,omitempty"`
	UnplayedItemCount     int        `json:"UnplayedItemCount,omitempty"`
}

type nameID struct {
	Name string `json:"Name"`
	ID   string `json:"Id"`
}

type person struct {
	Name            string `json:"Name"`
	ID              string `json:"Id"`
	Role            string `json:"Role,omitempty"`
	Type            string `json:"Type"`
	PrimaryImageTag string `json:"PrimaryImageTag,omitempty"`
}

type mediaStream struct {
	Codec                  string  `json:"Codec,omitempty"`
	Language               string  `json:"Language,omitempty"`
	Title                  string  `json:"Title,omitempty"`
	DisplayTitle           string  `json:"DisplayTitle,omitempty"`
	IsInterlaced           bool    `json:"IsInterlaced"`
	BitRate                int     `json:"BitRate,omitempty"`
	BitDepth               int     `json:"BitDepth,omitempty"`
	Channels               int     `json:"Channels,omitempty"`
	ChannelLayout          string  `json:"ChannelLayout,omitempty"`
	SampleRate             int     `json:"SampleRate,omitempty"`
	IsDefault              bool    `json:"IsDefault"`
	IsForced               bool    `json:"IsForced"`
	Height                 int     `json:"Height,omitempty"`
	Width                  int     `json:"Width,omitempty"`
	AverageFrameRate       float64 `json:"AverageFrameRate,omitempty"`
	RealFrameRate          float64 `json:"RealFrameRate,omitempty"`
	Profile                string  `json:"Profile,omitempty"`
	Type                   string  `json:"Type"`
	AspectRatio            string  `json:"AspectRatio,omitempty"`
	Index                  int     `json:"Index"`
	IsExternal             bool    `json:"IsExternal"`
	IsTextSubtitleStream   bool    `json:"IsTextSubtitleStream"`
	SupportsExternalStream bool    `json:"SupportsExternalStream"`
	DeliveryMethod         string  `json:"DeliveryMethod,omitempty"`
	DeliveryURL            string  `json:"DeliveryUrl,omitempty"`
	Level                  int     `json:"Level,omitempty"`
	PixelFormat            string  `json:"PixelFormat,omitempty"`
	ColorSpace             string  `json:"ColorSpace,omitempty"`
	ColorTransfer          string  `json:"ColorTransfer,omitempty"`
	ColorPrimaries         string  `json:"ColorPrimaries,omitempty"`
	ColorRange             string  `json:"ColorRange,omitempty"`
	VideoRange             string  `json:"VideoRange,omitempty"`
	VideoRangeType         string  `json:"VideoRangeType,omitempty"`
	DvProfile              int     `json:"DvProfile,omitempty"`
	DvLevel                int     `json:"DvLevel,omitempty"`
	Path                   string  `json:"Path,omitempty"`
}

type mediaSource struct {
	Protocol                   string            `json:"Protocol"`
	ID                         string            `json:"Id"`
	Path                       string            `json:"Path,omitempty"`
	Type                       string            `json:"Type"`
	Container                  string            `json:"Container,omitempty"`
	Size                       int64             `json:"Size,omitempty"`
	Name                       string            `json:"Name"`
	IsRemote                   bool              `json:"IsRemote"`
	RunTimeTicks               int64             `json:"RunTimeTicks,omitempty"`
	SupportsTranscoding        bool              `json:"SupportsTranscoding"`
	SupportsDirectStream       bool              `json:"SupportsDirectStream"`
	SupportsDirectPlay         bool              `json:"SupportsDirectPlay"`
	IsInfiniteStream           bool              `json:"IsInfiniteStream"`
	RequiresOpening            bool              `json:"RequiresOpening"`
	RequiresClosing            bool              `json:"RequiresClosing"`
	RequiresLooping            bool              `json:"RequiresLooping"`
	SupportsProbing            bool              `json:"SupportsProbing"`
	VideoType                  string            `json:"VideoType"`
	MediaStreams               []mediaStream     `json:"MediaStreams"`
	MediaAttachments           []struct{}        `json:"MediaAttachments"`
	Formats                    []string          `json:"Formats"`
	Bitrate                    int               `json:"Bitrate,omitempty"`
	RequiredHTTPHeaders        map[string]string `json:"RequiredHttpHeaders"`
	TranscodingURL             string            `json:"TranscodingUrl,omitempty"`
	TranscodingSubProtocol     string            `json:"TranscodingSubProtocol,omitempty"`
	TranscodingContainer       string            `json:"TranscodingContainer,omitempty"`
	DirectStreamURL            string            `json:"DirectStreamUrl,omitempty"`
	ReadAtNativeFramerate      bool              `json:"ReadAtNativeFramerate"`
	DefaultAudioStreamIndex    int               `json:"DefaultAudioStreamIndex,omitempty"`
	DefaultSubtitleStreamIndex *int              `json:"DefaultSubtitleStreamIndex,omitempty"`
	ETag                       string            `json:"ETag,omitempty"`
	Chapters                   []chapter         `json:"-"`
}

type chapter struct {
	StartPositionTicks int64  `json:"StartPositionTicks"`
	Name               string `json:"Name"`
	ImageTag           string `json:"ImageTag,omitempty"`
}

type item struct {
	Name                    string            `json:"Name"`
	OriginalTitle           string            `json:"OriginalTitle,omitempty"`
	ServerID                string            `json:"ServerId"`
	ID                      string            `json:"Id"`
	Etag                    string            `json:"Etag,omitempty"`
	DateCreated             *time.Time        `json:"DateCreated,omitempty"`
	CanDelete               bool              `json:"CanDelete"`
	CanDownload             bool              `json:"CanDownload"`
	SortName                string            `json:"SortName,omitempty"`
	PremiereDate            *time.Time        `json:"PremiereDate,omitempty"`
	ExternalURLs            []struct{}        `json:"ExternalUrls,omitempty"`
	MediaSources            []mediaSource     `json:"MediaSources,omitempty"`
	CriticRating            int               `json:"CriticRating,omitempty"`
	OfficialRating          string            `json:"OfficialRating,omitempty"`
	Overview                string            `json:"Overview,omitempty"`
	Taglines                []string          `json:"Taglines,omitempty"`
	Genres                  []string          `json:"Genres,omitempty"`
	CommunityRating         float64           `json:"CommunityRating,omitempty"`
	RunTimeTicks            int64             `json:"RunTimeTicks,omitempty"`
	ProductionYear          int               `json:"ProductionYear,omitempty"`
	IndexNumber             *int              `json:"IndexNumber,omitempty"`
	ParentIndexNumber       *int              `json:"ParentIndexNumber,omitempty"`
	IsFolder                bool              `json:"IsFolder"`
	ParentID                string            `json:"ParentId,omitempty"`
	Type                    string            `json:"Type"`
	People                  []person          `json:"People,omitempty"`
	Studios                 []nameID          `json:"Studios,omitempty"`
	GenreItems              []nameID          `json:"GenreItems,omitempty"`
	ProviderIDs             map[string]string `json:"ProviderIds,omitempty"`
	ChildCount              int               `json:"ChildCount,omitempty"`
	RecursiveItemCount      int               `json:"RecursiveItemCount,omitempty"`
	UserData                *userData         `json:"UserData,omitempty"`
	SeriesName              string            `json:"SeriesName,omitempty"`
	SeriesID                string            `json:"SeriesId,omitempty"`
	SeasonID                string            `json:"SeasonId,omitempty"`
	SeasonName              string            `json:"SeasonName,omitempty"`
	SeriesPrimaryImageTag   string            `json:"SeriesPrimaryImageTag,omitempty"`
	Status                  string            `json:"Status,omitempty"`
	MediaType               string            `json:"MediaType,omitempty"`
	LocationType            string            `json:"LocationType,omitempty"`
	VideoType               string            `json:"VideoType,omitempty"`
	CollectionType          string            `json:"CollectionType,omitempty"`
	ImageTags               map[string]string `json:"ImageTags"`
	BackdropImageTags       []string          `json:"BackdropImageTags,omitempty"`
	ParentBackdropImageTags []string          `json:"ParentBackdropImageTags,omitempty"`
	ParentBackdropItemID    string            `json:"ParentBackdropItemId,omitempty"`
	ImageBlurHashes         map[string]any    `json:"ImageBlurHashes,omitempty"`
	Chapters                []chapter         `json:"Chapters,omitempty"`
	MediaStreams            []mediaStream     `json:"MediaStreams,omitempty"`
	PrimaryImageAspectRatio float64           `json:"PrimaryImageAspectRatio,omitempty"`
	LockData                bool              `json:"LockData"`
	DisplayPreferencesID    string            `json:"DisplayPreferencesId,omitempty"`
	LocalTrailerCount       int               `json:"LocalTrailerCount"`
	SpecialFeatureCount     int               `json:"SpecialFeatureCount"`
	PartCount               int               `json:"PartCount,omitempty"`
	// Live TV channels ("TvChannel") and their programmes ("Program").
	ChannelID      string     `json:"ChannelId,omitempty"`
	ChannelName    string     `json:"ChannelName,omitempty"`
	ChannelNumber  string     `json:"ChannelNumber,omitempty"`
	StartDate      *time.Time `json:"StartDate,omitempty"`
	EndDate        *time.Time `json:"EndDate,omitempty"`
	IsLive         bool       `json:"IsLive,omitempty"`
	IsMovie        bool       `json:"IsMovie,omitempty"`
	IsSeries       bool       `json:"IsSeries,omitempty"`
	CurrentProgram *item      `json:"CurrentProgram,omitempty"`
}

type itemsResult struct {
	Items            []item `json:"Items"`
	TotalRecordCount int    `json:"TotalRecordCount"`
	StartIndex       int    `json:"StartIndex"`
}

func imageTag(path string) string {
	sum := sha1.Sum([]byte(path))
	return hex.EncodeToString(sum[:8])
}

func itemType(t core.MediaType) string {
	switch t {
	case core.TypeShow:
		return "Series"
	case core.TypeSeason:
		return "Season"
	case core.TypeEpisode:
		return "Episode"
	}
	return "Movie"
}

func dateOrNil(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func intp(n int) *int { return &n }

// view is a synthetic library.
type view struct {
	key  core.RatingKey
	name string
	kind string // CollectionType
}

var views = []view{
	{viewMovies, "Movies", "movies"},
	{viewShows, "TV Shows", "tvshows"},
	{viewBoxes, "Collections", "boxsets"},
}

func (s *Server) viewItem(v view) item {
	return item{
		Name: v.name, ServerID: s.serverID, ID: itemID(v.key), Type: "CollectionFolder", IsFolder: true,
		CollectionType: v.kind, SortName: v.name, DisplayPreferencesID: itemID(v.key),
		ImageTags: map[string]string{}, LocationType: "FileSystem", LockData: false,
	}
}

func (s *Server) userData(st core.WatchStatus, key core.RatingKey, t *core.Title, fav bool) *userData {
	ud := &userData{Key: key.String(), ItemID: itemID(key), IsFavorite: fav}
	ud.PlaybackPositionTicks = ticks(st.ViewOffset)
	ud.PlayCount = st.ViewCount
	ud.Played = st.ViewCount > 0 && st.ViewOffset == 0
	if !st.LastViewedAt.IsZero() {
		d := st.LastViewedAt
		ud.LastPlayedDate = &d
	}
	if rt := t.Runtime; rt > 0 && st.ViewOffset > 0 {
		ud.PlayedPercentage = float64(st.ViewOffset) / float64(rt) * 100
	}
	return ud
}

// itemFor converts a catalogue title. detail adds the fields only a detail
// page needs (cast, studios, chapters).
func (s *Server) itemFor(t *core.Title, st core.WatchStatus, fav, detail bool) item {
	out := item{
		Name: t.Title, OriginalTitle: t.OriginalTitle, ServerID: s.serverID, ID: itemID(t.Key),
		Type: itemType(t.Type), SortName: firstNonEmpty(t.SortTitle, t.Title),
		PremiereDate: dateOrNil(t.Released), DateCreated: dateOrNil(firstTime(t.UpdatedAt, t.Released)),
		OfficialRating: t.ContentRating, Overview: t.Summary, CommunityRating: round1(t.Rating),
		RunTimeTicks: ticks(t.Runtime), ProductionYear: t.Year, Genres: t.Genres,
		LocationType: "Virtual", LockData: false,
		ImageTags: map[string]string{}, ProviderIDs: map[string]string{},
	}
	out.Etag = itemID(t.Key)[16:] + imageTag(t.UpdatedAt.String())[:4]
	if t.Tagline != "" {
		out.Taglines = []string{t.Tagline}
	}
	if t.IDs.TMDB > 0 {
		out.ProviderIDs["Tmdb"] = strconv.FormatInt(t.IDs.TMDB, 10)
	}
	if t.IDs.IMDB != "" {
		out.ProviderIDs["Imdb"] = t.IDs.IMDB
	}
	if t.IDs.TVDB > 0 {
		out.ProviderIDs["Tvdb"] = strconv.FormatInt(t.IDs.TVDB, 10)
	}
	for _, g := range t.Genres {
		out.GenreItems = append(out.GenreItems, nameID{Name: g, ID: genreID(g)})
	}
	switch t.Type {
	case core.TypeMovie:
		out.MediaType, out.VideoType, out.CanDownload = "Video", "VideoFile", true
		out.ParentID = itemID(viewMovies)
		if t.PosterPath != "" {
			out.PrimaryImageAspectRatio = 2.0 / 3.0
		}
	case core.TypeShow:
		out.IsFolder, out.ParentID = true, itemID(viewShows)
		out.ChildCount, out.RecursiveItemCount = t.ChildCount, t.LeafCount
		out.Status = showStatus(t.Status)
		if t.PosterPath != "" {
			out.PrimaryImageAspectRatio = 2.0 / 3.0
		}
	case core.TypeSeason:
		out.IsFolder, out.ParentID = true, itemID(t.ShowKey)
		out.SeriesName, out.SeriesID = t.ShowTitle, itemID(t.ShowKey)
		out.IndexNumber = intp(t.Season)
		out.ChildCount = t.ChildCount
		out.Name = firstNonEmpty(t.SeasonTitle, t.Title)
	case core.TypeEpisode:
		out.MediaType, out.VideoType, out.CanDownload = "Video", "VideoFile", true
		season := core.SeasonKey(t.Key.TMDB(), t.Season)
		out.ParentID, out.SeasonID = itemID(season), itemID(season)
		out.SeriesName, out.SeriesID = t.ShowTitle, itemID(t.ShowKey)
		out.SeasonName = firstNonEmpty(t.SeasonTitle, "Season "+strconv.Itoa(t.Season))
		out.IndexNumber, out.ParentIndexNumber = intp(t.Episode), intp(t.Season)
		if t.StillPath != "" {
			out.PrimaryImageAspectRatio = 16.0 / 9.0
		}
	}
	if t.Type == core.TypeEpisode {
		// An episode's Primary image is its still; fall back to the series poster.
		if p := firstNonEmpty(t.StillPath, t.PosterPath); p != "" {
			out.ImageTags["Primary"] = imageTag(p)
		}
	} else if t.PosterPath != "" {
		out.ImageTags["Primary"] = imageTag(t.PosterPath)
	}
	if t.BackdropPath != "" {
		out.BackdropImageTags = []string{imageTag(t.BackdropPath)}
		out.ImageTags["Thumb"] = imageTag(t.BackdropPath)
	}
	if t.LogoPath != "" {
		out.ImageTags["Logo"] = imageTag(t.LogoPath)
	}
	if t.Type == core.TypeSeason || t.Type == core.TypeEpisode {
		out.SeriesPrimaryImageTag = out.ImageTags["Primary"]
		if t.Type == core.TypeEpisode {
			out.ParentBackdropItemID = itemID(t.ShowKey)
		}
	}
	for _, st := range t.Studios {
		out.Studios = append(out.Studios, nameID{Name: st, ID: genreID(st)})
	}
	out.UserData = s.userData(st, t.Key, t, fav)
	if st.ViewCount > 0 && st.ViewOffset == 0 {
		out.UserData.Played = true
	}
	if detail {
		for _, p := range t.Directors {
			out.People = append(out.People, personFor(p, "Director"))
		}
		for _, p := range t.Writers {
			out.People = append(out.People, personFor(p, "Writer"))
		}
		for _, p := range t.Cast {
			out.People = append(out.People, personFor(p, "Actor"))
		}
	}
	return out
}

func personFor(p core.Person, typ string) person {
	out := person{Name: p.Name, ID: personID(p), Role: p.Role, Type: typ}
	if p.ThumbPath != "" {
		out.PrimaryImageTag = imageTag(p.ThumbPath)
	}
	return out
}

func personID(p core.Person) string {
	if p.TMDB > 0 {
		return strconv.FormatInt(p.TMDB, 10)
	}
	return genreID(p.Name)
}

func firstTime(ts ...time.Time) time.Time {
	for _, t := range ts {
		if !t.IsZero() {
			return t
		}
	}
	return time.Time{}
}

func round1(f float64) float64 { return float64(int(f*10+0.5)) / 10 }

// genreID is a stable id for a name, so a genre can be browsed by id.
func genreID(name string) string {
	sum := sha1.Sum([]byte(strings.ToLower(name)))
	return hex.EncodeToString(sum[:16])
}

func showStatus(s string) string {
	switch strings.ToLower(s) {
	case "ended", "canceled", "cancelled":
		return "Ended"
	case "":
		return ""
	}
	return "Continuing"
}

// ---------------------------------------------------------------------------
// Streams

var subtitleText = map[string]bool{"srt": true, "subrip": true, "ass": true, "ssa": true, "vtt": true, "webvtt": true, "mov_text": true, "text": true}

func streamDisplay(st core.StreamInfo) string {
	var parts []string
	if st.Language != "" {
		parts = append(parts, strings.ToUpper(st.Language[:min(len(st.Language), 3)]))
	}
	if st.Title != "" {
		parts = append(parts, st.Title)
	}
	switch st.Type {
	case "video":
		if st.Height > 0 {
			parts = append(parts, strconv.Itoa(st.Height)+"p")
		}
		parts = append(parts, strings.ToUpper(st.Codec))
	case "audio":
		parts = append(parts, strings.ToUpper(st.Codec))
		if st.ChannelLayout != "" {
			parts = append(parts, st.ChannelLayout)
		}
	default:
		parts = append(parts, strings.ToUpper(st.Codec))
	}
	if st.Default {
		parts = append(parts, "Default")
	}
	if st.Forced {
		parts = append(parts, "Forced")
	}
	return strings.Join(parts, " - ")
}

func videoRange(st core.StreamInfo) (rng, typ string) {
	switch {
	case st.DolbyVision:
		return "HDR", "DOVI"
	case st.ColorTransfer == "smpte2084":
		return "HDR", "HDR10"
	case st.ColorTransfer == "arib-std-b67":
		return "HDR", "HLG"
	}
	return "SDR", "SDR"
}

// streamsFor converts probed streams. Jellyfin numbers streams by Index,
// which is what the apps send back as AudioStreamIndex/SubtitleStreamIndex.
func streamsFor(info core.MediaInfo, subURL func(index int, ext string) string, embedSubs bool) []mediaStream {
	out := make([]mediaStream, 0, len(info.Streams))
	for _, st := range info.Streams {
		ms := mediaStream{
			Codec: st.Codec, Language: st.Language, Title: st.Title, DisplayTitle: streamDisplay(st),
			BitRate: st.BitrateKbps * 1000, BitDepth: st.BitDepth, Channels: st.Channels, ChannelLayout: st.ChannelLayout,
			SampleRate: st.SampleRate, IsDefault: st.Default, IsForced: st.Forced, Height: st.Height, Width: st.Width,
			AverageFrameRate: st.FrameRate, RealFrameRate: st.FrameRate, Profile: st.Profile, Level: st.Level,
			Index: st.Index, IsInterlaced: st.Interlaced, IsExternal: st.External,
			ColorSpace: st.ColorSpace, ColorTransfer: st.ColorTransfer, ColorPrimaries: st.ColorPrimaries, ColorRange: st.ColorRange,
		}
		switch st.Type {
		case "video":
			ms.Type = "Video"
			ms.VideoRange, ms.VideoRangeType = videoRange(st)
			ms.DvProfile, ms.DvLevel = st.DolbyVisionProfile, st.DolbyVisionLevel
			if st.Width > 0 && st.Height > 0 {
				ms.AspectRatio = strconv.Itoa(st.Width) + ":" + strconv.Itoa(st.Height)
			}
		case "audio":
			ms.Type = "Audio"
		case "subtitle":
			ms.Type = "Subtitle"
			ms.IsTextSubtitleStream = subtitleText[strings.ToLower(st.Codec)]
			// Only a sidecar can be fetched on its own; a track inside the
			// video is the player's to show.
			ms.SupportsExternalStream = st.External
			if st.External && subURL != nil {
				ms.DeliveryMethod = "External"
				ms.DeliveryURL = subURL(st.Index, "srt")
			} else if embedSubs {
				ms.DeliveryMethod = "Embed"
			}
		default:
			continue
		}
		out = append(out, ms)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Index < out[j].Index })
	return out
}
