package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParsePrimaryArtist(t *testing.T) {
	tests := []struct {
		input       string
		wantPrimary string
		wantFeat    string
	}{
		{"Radiohead", "Radiohead", ""},
		{"Wretch 32 Feat. Badness & Ghetts", "Wretch 32", "Feat. Badness & Ghetts"},
		{"Artist Ft. Guest", "Artist", "Ft. Guest"},
		{"Artist Featuring Someone", "Artist", "Featuring Someone"},
		{"PinkPantheress & Ice Spice", "PinkPantheress", ""},
		{"Artist A / Artist B", "Artist A", ""},
		{"", "", ""},
	}
	for _, tt := range tests {
		gotPrimary, gotFeat := parsePrimaryArtist(tt.input)
		if gotPrimary != tt.wantPrimary {
			t.Errorf("parsePrimaryArtist(%q) primary = %q, want %q", tt.input, gotPrimary, tt.wantPrimary)
		}
		if gotFeat != tt.wantFeat {
			t.Errorf("parsePrimaryArtist(%q) feat = %q, want %q", tt.input, gotFeat, tt.wantFeat)
		}
	}
}

func TestBuildSpotifySearchURL(t *testing.T) {
	tests := []struct {
		title, artist string
		wantPrefix    string
		wantContains  string
	}{
		{"Never Gonna Give You Up", "Rick Astley", "https://open.spotify.com/search/", "Rick%20Astley"},
		{"Karma Police", "Radiohead", "https://open.spotify.com/search/", "Radiohead"},
		{"", "Solo Artist", "https://open.spotify.com/search/", "Solo%20Artist"},
		{"Only Title", "", "https://open.spotify.com/search/", "Only%20Title"},
		{"", "", "https://open.spotify.com/search/", ""},
	}
	for _, tt := range tests {
		got := buildSpotifySearchURL(tt.title, tt.artist)
		if !strings.HasPrefix(got, tt.wantPrefix) {
			t.Errorf("buildSpotifySearchURL(%q, %q) = %q, want prefix %q", tt.title, tt.artist, got, tt.wantPrefix)
		}
		if tt.wantContains != "" && !strings.Contains(got, tt.wantContains) {
			t.Errorf("buildSpotifySearchURL(%q, %q) = %q, want to contain %q", tt.title, tt.artist, got, tt.wantContains)
		}
	}
}

func TestSpotifyCacheKey(t *testing.T) {
	key1 := spotifyCacheKey("Radiohead", "Karma Police", "OK Computer")
	key2 := spotifyCacheKey("Radiohead", "Karma Police", "OK Computer")
	key3 := spotifyCacheKey("Radiohead", "Karma Police", "The Bends")

	if key1 != key2 {
		t.Error("identical inputs should produce identical cache keys")
	}
	if key1 == key3 {
		t.Error("different albums should produce different cache keys")
	}
	if !strings.HasPrefix(key1, "spotify.url.") {
		t.Errorf("cache key %q should start with 'spotify.url.'", key1)
	}

	// Case-insensitive: "Radiohead" == "radiohead"
	keyUpper := spotifyCacheKey("Radiohead", "Karma Police", "OK Computer")
	keyLower := spotifyCacheKey("radiohead", "karma police", "ok computer")
	if keyUpper != keyLower {
		t.Error("cache key should be case-insensitive")
	}
}

func TestParseSpotifyID(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "valid single result",
			body: `[{"spotify_track_ids":["4tIGK5G9hNDA50ZdGioZRG"]}]`,
			want: "4tIGK5G9hNDA50ZdGioZRG",
		},
		{
			name: "multiple IDs picks first",
			body: `[{"artist_name":"Lil Baby & Drake","track_name":"Yes Indeed","spotify_track_ids":["6vN77lE9LK6HP2DewaN6HZ","4wlLbLeDWbA6TzwZFp1UaK"]}]`,
			want: "6vN77lE9LK6HP2DewaN6HZ",
		},
		{
			name: "valid result with extra fields",
			body: `[{"artist_name":"Radiohead","track_name":"Karma Police","spotify_track_ids":["63OQupATfueTdZMWIV7nzz"],"release_name":"OK Computer"}]`,
			want: "63OQupATfueTdZMWIV7nzz",
		},
		{
			name: "empty spotify_track_ids array",
			body: `[{"spotify_track_ids":[]}]`,
			want: "",
		},
		{
			name: "no spotify_track_ids field",
			body: `[{"artist_name":"Unknown"}]`,
			want: "",
		},
		{
			name: "empty array",
			body: `[]`,
			want: "",
		},
		{
			name: "invalid JSON",
			body: `not json`,
			want: "",
		},
		{
			name: "null spotify_track_ids with next result valid",
			body: `[{"spotify_track_ids":[]},{"spotify_track_ids":["abc123"]}]`,
			want: "abc123",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseSpotifyID([]byte(tt.body))
			if got != tt.want {
				t.Errorf("parseSpotifyID(%s) = %q, want %q", tt.body, got, tt.want)
			}
		})
	}
}

func TestListenBrainzRequestPayloads(t *testing.T) {
	// Verify MBID request body is valid JSON
	mbid := "a1b2c3d4-e5f6-7890-abcd-ef1234567890"
	mbidBody := []byte(`[{"recording_mbid":"` + mbid + `"}]`)
	var mbidParsed []map[string]string
	if err := json.Unmarshal(mbidBody, &mbidParsed); err != nil {
		t.Fatalf("MBID request body is not valid JSON: %v", err)
	}
	if mbidParsed[0]["recording_mbid"] != mbid {
		t.Errorf("MBID body recording_mbid = %q, want %q", mbidParsed[0]["recording_mbid"], mbid)
	}

	// Verify metadata request body handles special characters via %q formatting
	artist := `Guns N' Roses`
	title := `Sweet Child O' Mine`
	album := `Appetite for Destruction`
	metaBody := []byte(`[{"artist_name":` + jsonQuote(artist) + `,"track_name":` + jsonQuote(title) + `,"release_name":` + jsonQuote(album) + `}]`)
	var metaParsed []map[string]string
	if err := json.Unmarshal(metaBody, &metaParsed); err != nil {
		t.Fatalf("Metadata request body is not valid JSON: %v", err)
	}
	if metaParsed[0]["artist_name"] != artist {
		t.Errorf("artist_name = %q, want %q", metaParsed[0]["artist_name"], artist)
	}
}

func jsonQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
