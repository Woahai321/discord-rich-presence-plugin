// Discord Rich Presence Plugin for Navidrome
//
// This plugin integrates Navidrome with Discord Rich Presence. It shows how a plugin can
// keep a real-time connection to an external service while remaining completely stateless.
//
// Capabilities: Scrobbler, SchedulerCallback, WebSocketCallback
//
// NOTE: This plugin is for demonstration purposes only. It relies on the user's Discord
// token being stored in the Navidrome configuration file, which is not secure and may be
// against Discord's terms of service. Use it at your own risk.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/pdk"
	"github.com/navidrome/navidrome/plugins/pdk/go/scheduler"
	"github.com/navidrome/navidrome/plugins/pdk/go/scrobbler"
	"github.com/navidrome/navidrome/plugins/pdk/go/websocket"
)

// Configuration keys
const (
	clientIDKey        = "clientid"
	usersKey           = "users"
	activityNameKey    = "activityname"
	navLogoOverlayKey  = "navlogooverlay"
)

// navidromeLogoURL is the small overlay image shown in the bottom-right of the album art.
// The file is stored in the plugin repository so Discord can fetch it as an external asset.
const navidromeLogoURL = "https://cdn.jsdelivr.net/gh/homarr-labs/dashboard-icons/webp/navidrome.webp"

// Activity name display options
const (
	activityNameDefault = "Default"
	activityNameTrack   = "Track"
	activityNameArtist  = "Artist"
	activityNameAlbum   = "Album"
)

// userToken represents a user-token mapping from the config
type userToken struct {
	Username string `json:"username"`
	Token    string `json:"token"`
}

// discordPlugin implements the scrobbler and scheduler interfaces.
type discordPlugin struct{}

// rpc handles Discord gateway communication (via websockets).
var rpc = &discordRPC{}

// init registers the plugin capabilities
func init() {
	scrobbler.Register(&discordPlugin{})
	scheduler.Register(&discordPlugin{})
	websocket.Register(rpc)
}

// buildSpotifySearchURL constructs a Spotify search URL using artist and title.
// Used as the ultimate fallback when ListenBrainz resolution fails.
func buildSpotifySearchURL(title, artist string) string {
	query := strings.TrimSpace(strings.Join([]string{artist, title}, " "))
	if query == "" {
		return "https://open.spotify.com/search/"
	}
	return fmt.Sprintf("https://open.spotify.com/search/%s", url.PathEscape(query))
}

// spotifySearch builds a Spotify search URL for a single search term.
func spotifySearch(term string) string {
	term = strings.TrimSpace(term)
	if term == "" {
		return ""
	}
	return "https://open.spotify.com/search/" + url.PathEscape(term)
}

const (
	spotifyCacheTTLHit  int64 = 30 * 24 * 60 * 60 // 30 days for resolved track IDs
	spotifyCacheTTLMiss int64 = 4 * 60 * 60        // 4 hours for misses (retry later)
)

// spotifyCacheKey returns a deterministic cache key for a track's Spotify URL.
func spotifyCacheKey(artist, title, album string) string {
	h := sha256.Sum256([]byte(strings.ToLower(artist) + "\x00" + strings.ToLower(title) + "\x00" + strings.ToLower(album)))
	return "spotify.url." + hex.EncodeToString(h[:8])
}

// listenBrainzResult captures the relevant field from ListenBrainz Labs JSON responses.
// The API returns spotify_track_ids as an array of strings.
type listenBrainzResult struct {
	SpotifyTrackIDs []string `json:"spotify_track_ids"`
}

// trySpotifyFromMBID calls the ListenBrainz spotify-id-from-mbid endpoint.
func trySpotifyFromMBID(mbid string) string {
	body := fmt.Sprintf(`[{"recording_mbid":"%s"}]`, mbid)
	req := pdk.NewHTTPRequest(pdk.MethodPost, "https://labs.api.listenbrainz.org/spotify-id-from-mbid/json")
	req.SetHeader("Content-Type", "application/json")
	req.SetBody([]byte(body))

	resp := req.Send()
	status := resp.Status()
	if status < 200 || status >= 300 {
		pdk.Log(pdk.LogInfo, fmt.Sprintf("ListenBrainz MBID lookup failed: HTTP %d, body=%s", status, string(resp.Body())))
		return ""
	}
	id := parseSpotifyID(resp.Body())
	if id == "" {
		pdk.Log(pdk.LogInfo, fmt.Sprintf("ListenBrainz MBID lookup returned no spotify_track_id for mbid=%s, body=%s", mbid, string(resp.Body())))
	}
	return id
}

// trySpotifyFromMetadata calls the ListenBrainz spotify-id-from-metadata endpoint.
func trySpotifyFromMetadata(artist, title, album string) string {
	payload := fmt.Sprintf(`[{"artist_name":%q,"track_name":%q,"release_name":%q}]`, artist, title, album)
	req := pdk.NewHTTPRequest(pdk.MethodPost, "https://labs.api.listenbrainz.org/spotify-id-from-metadata/json")
	req.SetHeader("Content-Type", "application/json")
	req.SetBody([]byte(payload))

	pdk.Log(pdk.LogInfo, fmt.Sprintf("ListenBrainz metadata request: %s", payload))

	resp := req.Send()
	status := resp.Status()
	if status < 200 || status >= 300 {
		pdk.Log(pdk.LogInfo, fmt.Sprintf("ListenBrainz metadata lookup failed: HTTP %d, body=%s", status, string(resp.Body())))
		return ""
	}
	pdk.Log(pdk.LogInfo, fmt.Sprintf("ListenBrainz metadata response: HTTP %d, body=%s", status, string(resp.Body())))
	id := parseSpotifyID(resp.Body())
	if id == "" {
		pdk.Log(pdk.LogInfo, fmt.Sprintf("ListenBrainz metadata returned no spotify_track_id for %q - %q", artist, title))
	}
	return id
}

// parseSpotifyID extracts the first spotify track ID from a ListenBrainz Labs JSON response.
// The response is an array of objects with spotify_track_ids arrays; we take the first non-empty ID.
func parseSpotifyID(body []byte) string {
	var results []listenBrainzResult
	if err := json.Unmarshal(body, &results); err != nil {
		return ""
	}
	for _, r := range results {
		for _, id := range r.SpotifyTrackIDs {
			if id != "" {
				return id
			}
		}
	}
	return ""
}

// resolveSpotifyURL resolves a direct Spotify track URL via ListenBrainz Labs,
// falling back to a search URL. Results are cached.
func resolveSpotifyURL(track scrobbler.TrackInfo) string {
	primary, _ := parsePrimaryArtist(track.Artist)
	if primary == "" && len(track.Artists) > 0 {
		primary = track.Artists[0].Name
	}

	cacheKey := spotifyCacheKey(primary, track.Title, track.Album)

	if cached, exists, err := host.CacheGetString(cacheKey); err == nil && exists {
		pdk.Log(pdk.LogInfo, fmt.Sprintf("Spotify URL cache hit for %q - %q → %s", primary, track.Title, cached))
		return cached
	}

	pdk.Log(pdk.LogInfo, fmt.Sprintf("Resolving Spotify URL for: artist=%q title=%q album=%q mbid=%q", primary, track.Title, track.Album, track.MBZRecordingID))

	// 1. Try MBID lookup (most accurate)
	if track.MBZRecordingID != "" {
		if trackID := trySpotifyFromMBID(track.MBZRecordingID); trackID != "" {
			directURL := "https://open.spotify.com/track/" + trackID
			_ = host.CacheSetString(cacheKey, directURL, spotifyCacheTTLHit)
			pdk.Log(pdk.LogInfo, fmt.Sprintf("Resolved Spotify via MBID for %q: %s", track.Title, directURL))
			return directURL
		}
		pdk.Log(pdk.LogInfo, "MBID lookup did not return a Spotify ID, trying metadata…")
	} else {
		pdk.Log(pdk.LogInfo, "No MBZRecordingID available, skipping MBID lookup")
	}

	// 2. Try metadata lookup
	if primary != "" && track.Title != "" {
		if trackID := trySpotifyFromMetadata(primary, track.Title, track.Album); trackID != "" {
			directURL := "https://open.spotify.com/track/" + trackID
			_ = host.CacheSetString(cacheKey, directURL, spotifyCacheTTLHit)
			pdk.Log(pdk.LogInfo, fmt.Sprintf("Resolved Spotify via metadata for %q - %q: %s", primary, track.Title, directURL))
			return directURL
		}
	}

	// 3. Fallback to search URL
	searchURL := buildSpotifySearchURL(track.Title, track.Artist)
	_ = host.CacheSetString(cacheKey, searchURL, spotifyCacheTTLMiss)
	pdk.Log(pdk.LogInfo, fmt.Sprintf("Spotify resolution missed, falling back to search URL for %q - %q: %s", primary, track.Title, searchURL))
	return searchURL
}

// parsePrimaryArtist returns the primary artist (before "Feat." / "Ft." / "Featuring")
// and the optional feat suffix. For artist resolution, only the primary artist is used;
// co-artists identified by "Feat.", "Ft.", "Featuring", "&", or "/" are stripped.
func parsePrimaryArtist(artist string) (primary, featSuffix string) {
	artist = strings.TrimSpace(artist)
	if artist == "" {
		return "", ""
	}
	lower := strings.ToLower(artist)
	for _, sep := range []string{" feat. ", " ft. ", " featuring "} {
		if i := strings.Index(lower, sep); i >= 0 {
			primary = strings.TrimSpace(artist[:i])
			featSuffix = strings.TrimSpace(artist[i:])
			return primary, featSuffix
		}
	}
	// Split on co-artist separators; take only the first artist.
	for _, sep := range []string{" & ", " / "} {
		if i := strings.Index(artist, sep); i >= 0 {
			return strings.TrimSpace(artist[:i]), ""
		}
	}
	return artist, ""
}

// getConfig loads the plugin configuration.
func getConfig() (clientID string, users map[string]string, err error) {
	clientID, ok := pdk.GetConfig(clientIDKey)
	if !ok || clientID == "" {
		pdk.Log(pdk.LogWarn, "missing ClientID in configuration")
		return "", nil, nil
	}

	// Get the users array from config
	usersJSON, ok := pdk.GetConfig(usersKey)
	if !ok || usersJSON == "" {
		pdk.Log(pdk.LogWarn, "no users configured")
		return clientID, nil, nil
	}

	// Parse the JSON array
	var userTokens []userToken
	if err := json.Unmarshal([]byte(usersJSON), &userTokens); err != nil {
		pdk.Log(pdk.LogError, fmt.Sprintf("failed to parse users config: %v", err))
		return clientID, nil, nil
	}

	if len(userTokens) == 0 {
		pdk.Log(pdk.LogWarn, "no users configured")
		return clientID, nil, nil
	}

	// Build the users map
	users = make(map[string]string)
	for _, ut := range userTokens {
		if ut.Username != "" && ut.Token != "" {
			users[ut.Username] = ut.Token
		}
	}

	if len(users) == 0 {
		pdk.Log(pdk.LogWarn, "no valid users configured")
		return clientID, nil, nil
	}

	return clientID, users, nil
}

// ============================================================================
// Scrobbler Implementation
// ============================================================================

// IsAuthorized checks if a user is authorized for Discord Rich Presence.
func (p *discordPlugin) IsAuthorized(input scrobbler.IsAuthorizedRequest) (bool, error) {
	_, users, err := getConfig()
	if err != nil {
		return false, fmt.Errorf("failed to check user authorization: %w", err)
	}

	_, authorized := users[input.Username]
	pdk.Log(pdk.LogInfo, fmt.Sprintf("IsAuthorized for user %s: %v", input.Username, authorized))
	return authorized, nil
}

// NowPlaying sends a now playing notification to Discord.
func (p *discordPlugin) NowPlaying(input scrobbler.NowPlayingRequest) error {
	pdk.Log(pdk.LogInfo, fmt.Sprintf("Setting presence for user %s, track: %s", input.Username, input.Track.Title))

	// Load configuration
	clientID, users, err := getConfig()
	if err != nil {
		return fmt.Errorf("%w: failed to get config: %v", scrobbler.ScrobblerErrorRetryLater, err)
	}

	// Check authorization
	userToken, authorized := users[input.Username]
	if !authorized {
		return fmt.Errorf("%w: user '%s' not authorized", scrobbler.ScrobblerErrorNotAuthorized, input.Username)
	}

	// Connect to Discord
	if err := rpc.connect(input.Username, userToken); err != nil {
		return fmt.Errorf("%w: failed to connect to Discord: %v", scrobbler.ScrobblerErrorRetryLater, err)
	}

	// Cancel any existing completion schedule
	_ = host.SchedulerCancelSchedule(fmt.Sprintf("%s-clear", input.Username))

	// Calculate timestamps
	now := time.Now().Unix()
	startTime := (now - int64(input.Position)) * 1000
	endTime := startTime + int64(input.Track.Duration)*1000

	// Resolve the activity name based on configuration
	activityName := "Navidrome"
	activityNameOption, _ := pdk.GetConfig(activityNameKey)
	switch activityNameOption {
	case activityNameTrack:
		activityName = input.Track.Title
	case activityNameAlbum:
		activityName = input.Track.Album
	case activityNameArtist:
		activityName = input.Track.Artist
	}

	// Navidrome logo overlay: shown by default; disabled only when explicitly set to "false"
	navLogoOption, _ := pdk.GetConfig(navLogoOverlayKey)
	smallImage, smallText := "", ""
	if navLogoOption != "false" {
		smallImage = navidromeLogoURL
		smallText = "Navidrome"
	}

	// Send activity update
	statusDisplayType := 2
	if err := rpc.sendActivity(clientID, input.Username, userToken, activity{
		Application:       clientID,
		Name:              activityName,
		Type:              2, // Listening
		Details:           input.Track.Title,
		DetailsURL:        spotifySearch(input.Track.Title),
		State:             input.Track.Artist,
		StateURL:          spotifySearch(input.Track.Artist),
		StatusDisplayType: &statusDisplayType,
		Timestamps: activityTimestamps{
			Start: startTime,
			End:   endTime,
		},
		Assets: activityAssets{
			LargeImage: getImageURL(input.Username, input.Track.ID),
			LargeText:  input.Track.Album,
			LargeURL:   resolveSpotifyURL(input.Track),
			SmallImage: smallImage,
			SmallText:  smallText,
		},
	}); err != nil {
		return fmt.Errorf("%w: failed to send activity: %v", scrobbler.ScrobblerErrorRetryLater, err)
	}

	// Schedule a timer to clear the activity after the track completes
	remainingSeconds := int32(input.Track.Duration) - input.Position + 5
	_, err = host.SchedulerScheduleOneTime(remainingSeconds, payloadClearActivity, fmt.Sprintf("%s-clear", input.Username))
	if err != nil {
		pdk.Log(pdk.LogWarn, fmt.Sprintf("Failed to schedule completion timer: %v", err))
	}

	return nil
}

// Scrobble handles scrobble requests (no-op for Discord).
func (p *discordPlugin) Scrobble(_ scrobbler.ScrobbleRequest) error {
	// Discord Rich Presence doesn't need scrobble events
	return nil
}

// ============================================================================
// Scheduler Callback Implementation
// ============================================================================

// OnCallback handles scheduler callbacks.
func (p *discordPlugin) OnCallback(input scheduler.SchedulerCallbackRequest) error {
	pdk.Log(pdk.LogDebug, fmt.Sprintf("Scheduler callback: id=%s, payload=%s, recurring=%v", input.ScheduleID, input.Payload, input.IsRecurring))

	// Route based on payload
	switch input.Payload {
	case payloadHeartbeat:
		// Heartbeat callback - scheduleId is the username
		if err := rpc.handleHeartbeatCallback(input.ScheduleID); err != nil {
			return err
		}

	case payloadClearActivity:
		// Clear activity callback - scheduleId is "username-clear"
		username := strings.TrimSuffix(input.ScheduleID, "-clear")
		if err := rpc.handleClearActivityCallback(username); err != nil {
			return err
		}

	default:
		pdk.Log(pdk.LogWarn, fmt.Sprintf("Unknown scheduler callback payload: %s", input.Payload))
	}

	return nil
}

func main() {}
