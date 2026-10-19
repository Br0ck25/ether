package core

import (
	"fmt"
	"strconv"
)

// RatingKey is the Plex ratingKey of an Ether item. It is derived from TMDB
// identifiers so a title keeps the same key before and after anything about
// it is resolved or cached.
//
// Layout (decimal, kept below 2^53 so JavaScript clients parse it safely):
//
//	kind * 10^15 + payload
//
//	KindMovie       payload = tmdb movie id
//	KindShow        payload = tmdb tv id
//	KindSeason      payload = show*10^4 + season           (season < 10^4)
//	KindEpisode     payload = show*10^7 + season*10^4 + ep  (show < 10^8, season < 1000, ep < 10^4)
//	KindCollection  payload = tmdb collection id (franchise)
//	KindSmart       payload = local smart-collection / generated hub id;
//	                payloads >= EphemeralSmartBase are reserved for
//	                ephemeral (in-memory, per-viewer) generated
//	                collections and never collide with database ids
//	KindPlaylist    payload = local playlist id
//	KindExtra       payload = compact extra id from the extras registry
//	KindLocal       payload = local file id (spike / test library)
type RatingKey int64

type KeyKind int

const (
	KindMovie      KeyKind = 1
	KindShow       KeyKind = 2
	KindSeason     KeyKind = 3
	KindEpisode    KeyKind = 4
	KindCollection KeyKind = 5
	KindSmart      KeyKind = 6
	KindPlaylist   KeyKind = 7
	KindExtra      KeyKind = 8
	KindLocal      KeyKind = 9
)

const keyBase = 1_000_000_000_000_000 // 10^15

func MovieKey(tmdb int64) RatingKey    { return RatingKey(int64(KindMovie)*keyBase + tmdb) }
func ShowKey(tmdb int64) RatingKey     { return RatingKey(int64(KindShow)*keyBase + tmdb) }
func CollectionKey(id int64) RatingKey { return RatingKey(int64(KindCollection)*keyBase + id) }
func SmartKey(id int64) RatingKey      { return RatingKey(int64(KindSmart)*keyBase + id) }
func PlaylistKey(id int64) RatingKey   { return RatingKey(int64(KindPlaylist)*keyBase + id) }
func ExtraKey(id int64) RatingKey      { return RatingKey(int64(KindExtra)*keyBase + id) }
func LocalKey(id int64) RatingKey      { return RatingKey(int64(KindLocal)*keyBase + id) }

// EphemeralSmartBase is the first KindSmart payload reserved for
// ephemeral generated collections. Database smart-collection ids stay
// far below it.
const EphemeralSmartBase int64 = 900_000_000_000_000

// EphemeralSmartKey builds an ephemeral collection key from a sequence
// number (1 .. 10^14-1).
func EphemeralSmartKey(seq int64) RatingKey {
	return SmartKey(EphemeralSmartBase + seq)
}

// IsEphemeral reports whether k is an ephemeral generated collection key.
func (k RatingKey) IsEphemeral() bool {
	return k.Kind() == KindSmart && k.payload() >= EphemeralSmartBase
}
func SeasonKey(show int64, season int) RatingKey {
	return RatingKey(int64(KindSeason)*keyBase + show*10_000 + int64(season))
}
func EpisodeKey(show int64, season, episode int) RatingKey {
	return RatingKey(int64(KindEpisode)*keyBase + show*10_000_000 + int64(season)*10_000 + int64(episode))
}

func (k RatingKey) Kind() KeyKind  { return KeyKind(int64(k) / keyBase) }
func (k RatingKey) payload() int64 { return int64(k) % keyBase }

// TMDB returns the TMDB id for movie, show, and collection keys, and the show
// id for season and episode keys.
func (k RatingKey) TMDB() int64 {
	switch k.Kind() {
	case KindSeason:
		return k.payload() / 10_000
	case KindEpisode:
		return k.payload() / 10_000_000
	default:
		return k.payload()
	}
}

// Season returns the season number for season and episode keys.
func (k RatingKey) Season() int {
	switch k.Kind() {
	case KindSeason:
		return int(k.payload() % 10_000)
	case KindEpisode:
		return int(k.payload() / 10_000 % 1000)
	}
	return 0
}

// Episode returns the episode number for episode keys.
func (k RatingKey) Episode() int {
	if k.Kind() == KindEpisode {
		return int(k.payload() % 10_000)
	}
	return 0
}

// Parent returns the season key of an episode and the show key of a season.
func (k RatingKey) Parent() RatingKey {
	switch k.Kind() {
	case KindEpisode:
		return SeasonKey(k.TMDB(), k.Season())
	case KindSeason:
		return ShowKey(k.TMDB())
	}
	return 0
}

// ExtraID returns the extras-registry id of an extra key.
func (k RatingKey) ExtraID() int64 {
	if k.Kind() == KindExtra {
		return k.payload()
	}
	return 0
}

func (k RatingKey) String() string { return strconv.FormatInt(int64(k), 10) }

func (k RatingKey) Valid() bool {
	switch k.Kind() {
	case KindMovie, KindShow, KindSeason, KindEpisode, KindCollection, KindSmart, KindPlaylist, KindExtra, KindLocal:
		return k.payload() > 0 || k.Kind() == KindSeason
	}
	return false
}

func ParseRatingKey(s string) (RatingKey, error) {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("ratingKey %q: %w", s, err)
	}
	k := RatingKey(n)
	if !k.Valid() {
		return 0, fmt.Errorf("ratingKey %q: unknown kind", s)
	}
	return k, nil
}
