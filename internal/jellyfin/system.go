package jellyfin

import (
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/braedonsaunders/ether/internal/core"
)

func (s *Server) version(r *http.Request) string {
	return jellyfinVersion
}

func (s *Server) localAddress(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func (s *Server) handlePublicInfo(w http.ResponseWriter, r *http.Request) {
	info := map[string]any{
		"LocalAddress": s.localAddress(r), "ServerName": s.name, "Version": s.version(r), "Id": s.serverID,
		"StartupWizardCompleted": true,
	}
	info["ProductName"] = "Jellyfin Server"
	info["OperatingSystem"] = ""
	writeJSON(w, http.StatusOK, info)
}

// handleLanding is what a browser sees when no Jellyfin web client is
// installed (see Deps.WebDir): only the API the Jellyfin apps use.
func (s *Server) handleLanding(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>%[1]s</title><body style="font:16px system-ui;max-width:34rem;margin:15vh auto;padding:0 1rem;line-height:1.5">
<h1>%[1]s</h1><p>This is a Jellyfin compatible server. Open a Jellyfin app, add this address
(<code>%[2]s</code>) as a server, and sign in with your Plex user name and the access code the server owner gave you.</p>`,
		html.EscapeString(s.name), html.EscapeString(s.localAddress(r)))
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	_, _ = w.Write([]byte("Healthy"))
}

func (s *Server) handlePing(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	_, _ = w.Write([]byte("Jellyfin Server"))
}

func (s *Server) handleSystemInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"LocalAddress": s.localAddress(r), "ServerName": s.name, "Version": s.version(r), "Id": s.serverID,
		"ProductName": "Jellyfin Server", "OperatingSystem": "", "StartupWizardCompleted": true,
		"CanSelfRestart": false, "CanLaunchWebBrowser": false, "HasPendingRestart": false, "IsShuttingDown": false,
		"SupportsLibraryMonitor": false, "WebSocketPortNumber": 0, "TranscodingTempPath": "", "HasUpdateAvailable": false,
	})
}

func (s *Server) handleEndpoint(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	writeJSON(w, http.StatusOK, map[string]bool{"IsLocal": !sess.Remote, "IsInNetwork": !sess.Remote})
}

func (s *Server) handleServerConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"EnableCaseSensitiveItemIds": false, "ServerName": s.name,
		"PreferredMetadataLanguage": "en", "MetadataCountryCode": "US", "IsStartupWizardCompleted": true})
}

func (s *Server) handleBranding(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"LoginDisclaimer": "", "CustomCss": "", "SplashscreenEnabled": false})
}

func (s *Server) handleQuickConnectEnabled(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, false)
}

// A bitrate test is a run of bytes the app times; apps size it with ?Size=.
func (s *Server) handleBitrateTest(w http.ResponseWriter, r *http.Request) {
	size := queryParams(r).int("size", 500_000)
	if size < 1 || size > 10_000_000 {
		size = 500_000
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.Itoa(size))
	buf := make([]byte, 32*1024)
	for left := size; left > 0; {
		n := min(left, len(buf))
		if _, err := w.Write(buf[:n]); err != nil {
			return
		}
		left -= n
	}
}

func (s *Server) handleThemeMedia(w http.ResponseWriter, r *http.Request) {
	empty := itemsResult{Items: []item{}}
	writeJSON(w, http.StatusOK, map[string]any{"ThemeVideosResult": empty, "ThemeSongsResult": empty, "SoundtrackSongsResult": empty})
}

func (s *Server) handleThemeSongs(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"Items": []item{}, "TotalRecordCount": 0, "StartIndex": 0, "OwnerId": chi.URLParam(r, "id")})
}

// ---------------------------------------------------------------------------
// Users

func userID(id int64) string { return fmt.Sprintf("%032x", uint64(id)) }

func displayName(u *core.User) string { return firstNonEmpty(u.Username, u.Title, u.Email) }

func (s *Server) userDTO(u *core.User) map[string]any {
	policy := map[string]any{
		"IsAdministrator": u.IsOwner, "IsHidden": false, "IsDisabled": false, "EnableUserPreferenceAccess": true,
		"EnableRemoteControlOfOtherUsers": false, "EnableSharedDeviceControl": false, "EnableRemoteAccess": true,
		"EnableLiveTvManagement": false, "EnableLiveTvAccess": s.d.LiveTV != nil, "EnableMediaPlayback": true,
		"EnableAudioPlaybackTranscoding": true, "EnableVideoPlaybackTranscoding": true, "EnablePlaybackRemuxing": true,
		"EnableContentDeletion": false, "EnableContentDownloading": true, "EnableSyncTranscoding": false,
		"EnableMediaConversion": false, "EnableAllDevices": true, "EnableAllChannels": true, "EnableAllFolders": true,
		"EnablePublicSharing": false, "EnableCollectionManagement": false, "EnableSubtitleManagement": false,
		"EnableLyricManagement": false, "InvalidLoginAttemptCount": 0, "LoginAttemptsBeforeLockout": maxFailures,
		"MaxActiveSessions": 0, "RemoteClientBitrateLimit": 0, "SyncPlayAccess": "None",
		"AuthenticationProviderId": "Jellyfin.Server.Implementations.Users.DefaultAuthenticationProvider",
		"PasswordResetProviderId":  "Jellyfin.Server.Implementations.Users.DefaultPasswordResetProvider",
		"EnabledDevices":           []string{}, "EnabledChannels": []string{}, "EnabledFolders": []string{}, "BlockedTags": []string{},
		"BlockedMediaFolders": []string{}, "BlockedChannels": []string{}, "AccessSchedules": []any{}, "BlockUnratedItems": []string{},
	}
	cfg := map[string]any{
		"PlayDefaultAudioTrack": true, "SubtitleLanguagePreference": "", "DisplayMissingEpisodes": false,
		"GroupedFolders": []string{}, "SubtitleMode": "Default", "DisplayCollectionsView": false,
		"EnableLocalPassword": false, "OrderedViews": []string{}, "LatestItemsExcludes": []string{},
		"MyMediaExcludes": []string{}, "HidePlayedInLatest": true, "RememberAudioSelections": true,
		"RememberSubtitleSelections": true, "EnableNextEpisodeAutoPlay": true,
	}
	if len(u.SubtitleLanguages) > 0 {
		cfg["SubtitleLanguagePreference"] = u.SubtitleLanguages[0]
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	return map[string]any{
		"Name": displayName(u), "ServerId": s.serverID, "Id": userID(u.ID), "HasPassword": true,
		"HasConfiguredPassword": true, "HasConfiguredEasyPassword": false, "EnableAutoLogin": false,
		"LastLoginDate": now, "LastActivityDate": now, "Configuration": cfg, "Policy": policy,
	}
}

// handleUsersPublic hides the viewer list: apps then ask for a user name.
func (s *Server) handleUsersPublic(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, []any{})
}

func (s *Server) handleUsers(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, []any{s.userDTO(sessionFrom(r.Context()).User)})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.userDTO(sessionFrom(r.Context()).User))
}

func (s *Server) handleUser(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	if !s.isSelf(sess, chi.URLParam(r, "uid")) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	writeJSON(w, http.StatusOK, s.userDTO(sess.User))
}

// isSelf reports whether a {uid} path value names the signed-in viewer. An
// app may also omit it. Nobody reads another viewer's library or state.
func (s *Server) isSelf(sess *session, uid string) bool {
	if uid == "" {
		return true
	}
	trim := func(v string) string { return strings.TrimLeft(strings.ReplaceAll(strings.ToLower(v), "-", ""), "0") }
	return trim(uid) == trim(userID(sess.User.ID))
}

func (s *Server) handleAuthByName(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"Username"`
		Pw       string `json:"Pw"`
		Password string `json:"Password"`
	}
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		body.Username, body.Pw = r.PostForm.Get("Username"), firstNonEmpty(r.PostForm.Get("Pw"), r.PostForm.Get("Password"))
	} else if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	code := firstNonEmpty(body.Pw, body.Password)
	name := strings.ToLower(strings.TrimSpace(body.Username))
	ip := clientIP(r)
	key, ipKey := name+"|"+ip.String(), "ip|"+ip.String()
	if s.throttle.blocked(key) || s.throttle.blocked(ipKey) {
		http.Error(w, "too many failed sign-ins; try again later", http.StatusTooManyRequests)
		return
	}
	var user *core.User
	if users, err := s.d.Auth.Users(r.Context()); err == nil && name != "" {
		for i := range users {
			for _, n := range []string{users[i].Username, users[i].Title, users[i].Email} {
				if n != "" && strings.EqualFold(n, name) {
					user = &users[i]
				}
			}
			if user != nil {
				break
			}
		}
	}
	ok := false
	if user != nil && code != "" {
		ok = s.d.Store.verify(r.Context(), user.ID, code)
	} else {
		s.d.Store.verify(r.Context(), -1, code) // same cost whether or not the name exists
	}
	s.throttle.record(key, ok, maxFailures)
	s.throttle.record(ipKey, ok, maxIPFailures)
	if !ok {
		w.Header().Set("X-Application-Error-Code", "Unauthorized")
		writeJSON(w, http.StatusUnauthorized, map[string]string{"Message": "Invalid username or access code"})
		return
	}
	if s.activeUser(r.Context(), user.ID) == nil {
		http.Error(w, "this account no longer has access", http.StatusForbidden)
		return
	}
	a := parseAuth(r, queryParams(r))
	sess := &session{DeviceID: a.DeviceID, Device: a.Device, Client: a.Client, Version: a.Version}
	token, err := s.d.Store.issue(r.Context(), user.ID, sess)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	s.log.Info("jellyfin: signed in", "user", user.ID, "client", a.Client, "device", a.Device, "deviceId", a.DeviceID, "token", tokenHint(token))
	now := s.now().UTC().Format(time.RFC3339Nano)
	writeJSON(w, http.StatusOK, map[string]any{
		"User": s.userDTO(user), "AccessToken": token, "ServerId": s.serverID,
		"SessionInfo": map[string]any{
			"PlayState":       map[string]any{"CanSeek": false, "IsPaused": false, "IsMuted": false, "RepeatMode": "RepeatNone"},
			"AdditionalUsers": []any{}, "Capabilities": map[string]any{}, "RemoteEndPoint": ip.String(),
			"PlayableMediaTypes": []string{"Video"}, "Id": tokenHash(token)[:32], "UserId": userID(user.ID),
			"UserName": displayName(user), "Client": a.Client, "LastActivityDate": now, "DeviceName": a.Device,
			"DeviceId": sess.DeviceID, "ApplicationVersion": a.Version, "IsActive": true, "SupportsMediaControl": false,
			"SupportsRemoteControl": false, "NowPlayingQueue": []any{}, "ServerId": s.serverID,
		},
	})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	_ = s.d.Store.Revoke(r.Context(), sessionFrom(r.Context()).Token)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleSessionsList(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	writeJSON(w, http.StatusOK, []any{map[string]any{
		"Id": tokenHash(sess.Token)[:32], "UserId": userID(sess.User.ID), "UserName": displayName(sess.User),
		"Client": sess.Client, "DeviceName": sess.Device, "DeviceId": sess.DeviceID, "IsActive": true,
		"ServerId": s.serverID, "PlayableMediaTypes": []string{"Video"}, "SupportsMediaControl": false,
	}})
}

// handleCapabilities acknowledges what an app says it can do. Ether does not
// remote-control apps, so nothing is kept.
func (s *Server) handleCapabilities(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDisplayPrefs(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"Id": chi.URLParam(r, "id"), "SortBy": "SortName", "SortOrder": "Ascending", "RememberIndexing": false,
		"PrimaryImageHeight": 250, "PrimaryImageWidth": 250, "CustomPrefs": map[string]string{},
		"ScrollDirection": "Horizontal", "ShowBackdrop": true, "RememberSorting": false, "ViewType": "", "Client": "emby",
	})
}
