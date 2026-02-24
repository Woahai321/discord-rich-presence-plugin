package main

import (
	"encoding/json"
	"fmt"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/pdk"
	"github.com/navidrome/navidrome/plugins/pdk/go/scrobbler"
	"github.com/stretchr/testify/mock"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Spotify", func() {
	Describe("parsePrimaryArtist", func() {
		DescribeTable("extracts primary artist and feat suffix",
			func(input, expectedPrimary, expectedFeat string) {
				primary, feat := parsePrimaryArtist(input)
				Expect(primary).To(Equal(expectedPrimary))
				Expect(feat).To(Equal(expectedFeat))
			},
			Entry("simple artist", "Radiohead", "Radiohead", ""),
			Entry("Feat. separator", "Wretch 32 Feat. Badness & Ghetts", "Wretch 32", "Feat. Badness & Ghetts"),
			Entry("Ft. separator", "Artist Ft. Guest", "Artist", "Ft. Guest"),
			Entry("Featuring separator", "Artist Featuring Someone", "Artist", "Featuring Someone"),
			Entry("& co-artist", "PinkPantheress & Ice Spice", "PinkPantheress", ""),
			Entry("/ co-artist", "Artist A / Artist B", "Artist A", ""),
			Entry("empty string", "", "", ""),
		)
	})

	Describe("buildSpotifySearchURL", func() {
		DescribeTable("constructs Spotify search URL",
			func(title, artist, expectedSubstring string) {
				url := buildSpotifySearchURL(title, artist)
				Expect(url).To(HavePrefix("https://open.spotify.com/search/"))
				if expectedSubstring != "" {
					Expect(url).To(ContainSubstring(expectedSubstring))
				}
			},
			Entry("artist and title", "Never Gonna Give You Up", "Rick Astley", "Rick%20Astley"),
			Entry("another track", "Karma Police", "Radiohead", "Radiohead"),
			Entry("empty title", "", "Solo Artist", "Solo%20Artist"),
			Entry("empty artist", "Only Title", "", "Only%20Title"),
			Entry("both empty", "", "", ""),
		)
	})

	Describe("spotifyCacheKey", func() {
		It("produces identical keys for identical inputs", func() {
			key1 := spotifyCacheKey("Radiohead", "Karma Police", "OK Computer")
			key2 := spotifyCacheKey("Radiohead", "Karma Police", "OK Computer")
			Expect(key1).To(Equal(key2))
		})

		It("produces different keys for different albums", func() {
			key1 := spotifyCacheKey("Radiohead", "Karma Police", "OK Computer")
			key2 := spotifyCacheKey("Radiohead", "Karma Police", "The Bends")
			Expect(key1).ToNot(Equal(key2))
		})

		It("uses the correct prefix", func() {
			key := spotifyCacheKey("Radiohead", "Karma Police", "OK Computer")
			Expect(key).To(HavePrefix("spotify.url."))
		})

		It("is case-insensitive", func() {
			keyUpper := spotifyCacheKey("Radiohead", "Karma Police", "OK Computer")
			keyLower := spotifyCacheKey("radiohead", "karma police", "ok computer")
			Expect(keyUpper).To(Equal(keyLower))
		})
	})

	Describe("parseSpotifyID", func() {
		DescribeTable("extracts first Spotify track ID from ListenBrainz response",
			func(body, expectedID string) {
				Expect(parseSpotifyID([]byte(body))).To(Equal(expectedID))
			},
			Entry("valid single result",
				`[{"spotify_track_ids":["4tIGK5G9hNDA50ZdGioZRG"]}]`, "4tIGK5G9hNDA50ZdGioZRG"),
			Entry("multiple IDs picks first",
				`[{"artist_name":"Lil Baby & Drake","track_name":"Yes Indeed","spotify_track_ids":["6vN77lE9LK6HP2DewaN6HZ","4wlLbLeDWbA6TzwZFp1UaK"]}]`, "6vN77lE9LK6HP2DewaN6HZ"),
			Entry("valid result with extra fields",
				`[{"artist_name":"Radiohead","track_name":"Karma Police","spotify_track_ids":["63OQupATfueTdZMWIV7nzz"],"release_name":"OK Computer"}]`, "63OQupATfueTdZMWIV7nzz"),
			Entry("empty spotify_track_ids array",
				`[{"spotify_track_ids":[]}]`, ""),
			Entry("no spotify_track_ids field",
				`[{"artist_name":"Unknown"}]`, ""),
			Entry("empty array",
				`[]`, ""),
			Entry("invalid JSON",
				`not json`, ""),
			Entry("null first result falls through to second",
				`[{"spotify_track_ids":[]},{"spotify_track_ids":["abc123"]}]`, "abc123"),
		)
	})

	Describe("ListenBrainz request payloads", func() {
		It("builds valid JSON for MBID requests", func() {
			mbid := "a1b2c3d4-e5f6-7890-abcd-ef1234567890"
			body := []byte(fmt.Sprintf(`[{"recording_mbid":%q}]`, mbid))
			var parsed []map[string]string
			Expect(json.Unmarshal(body, &parsed)).To(Succeed())
			Expect(parsed[0]["recording_mbid"]).To(Equal(mbid))
		})

		It("builds valid JSON for metadata requests with special characters", func() {
			artist := `Guns N' Roses`
			title := `Sweet Child O' Mine`
			album := `Appetite for Destruction`
			payload := fmt.Sprintf(`[{"artist_name":%q,"track_name":%q,"release_name":%q}]`, artist, title, album)
			var parsed []map[string]string
			Expect(json.Unmarshal([]byte(payload), &parsed)).To(Succeed())
			Expect(parsed[0]["artist_name"]).To(Equal(artist))
			Expect(parsed[0]["track_name"]).To(Equal(title))
			Expect(parsed[0]["release_name"]).To(Equal(album))
		})
	})

	Describe("resolveSpotifyURL", func() {
		BeforeEach(func() {
			pdk.ResetMock()
			host.CacheMock.ExpectedCalls = nil
			host.CacheMock.Calls = nil
			pdk.PDKMock.On("Log", mock.Anything, mock.Anything).Maybe()
		})

		It("returns cached URL on cache hit", func() {
			host.CacheMock.On("GetString", mock.Anything).Return("https://open.spotify.com/track/cached123", true, nil)

			url := resolveSpotifyURL(scrobbler.TrackInfo{
				Title:  "Karma Police",
				Artist: "Radiohead",
				Album:  "OK Computer",
			})
			Expect(url).To(Equal("https://open.spotify.com/track/cached123"))
		})

		It("resolves via MBID when available", func() {
			host.CacheMock.On("GetString", mock.Anything).Return("", false, nil)
			host.CacheMock.On("SetString", mock.Anything, mock.Anything, mock.Anything).Return(nil)

			// Mock the MBID HTTP request
			mbidReq := &pdk.HTTPRequest{}
			pdk.PDKMock.On("NewHTTPRequest", pdk.MethodPost, "https://labs.api.listenbrainz.org/spotify-id-from-mbid/json").Return(mbidReq)
			pdk.PDKMock.On("Send", mbidReq).Return(pdk.NewStubHTTPResponse(200, nil,
				[]byte(`[{"spotify_track_ids":["track123"]}]`)))

			url := resolveSpotifyURL(scrobbler.TrackInfo{
				Title:          "Karma Police",
				Artist:         "Radiohead",
				Album:          "OK Computer",
				MBZRecordingID: "mbid-123",
			})
			Expect(url).To(Equal("https://open.spotify.com/track/track123"))
			host.CacheMock.AssertCalled(GinkgoT(), "SetString", mock.Anything, "https://open.spotify.com/track/track123", spotifyCacheTTLHit)
		})

		It("falls back to metadata lookup when MBID fails", func() {
			host.CacheMock.On("GetString", mock.Anything).Return("", false, nil)
			host.CacheMock.On("SetString", mock.Anything, mock.Anything, mock.Anything).Return(nil)

			// MBID request fails
			mbidReq := &pdk.HTTPRequest{}
			pdk.PDKMock.On("NewHTTPRequest", pdk.MethodPost, "https://labs.api.listenbrainz.org/spotify-id-from-mbid/json").Return(mbidReq)
			pdk.PDKMock.On("Send", mbidReq).Return(pdk.NewStubHTTPResponse(404, nil, []byte(`[]`)))

			// Metadata request succeeds
			metaReq := &pdk.HTTPRequest{}
			pdk.PDKMock.On("NewHTTPRequest", pdk.MethodPost, "https://labs.api.listenbrainz.org/spotify-id-from-metadata/json").Return(metaReq)
			pdk.PDKMock.On("Send", metaReq).Return(pdk.NewStubHTTPResponse(200, nil,
				[]byte(`[{"spotify_track_ids":["meta456"]}]`)))

			url := resolveSpotifyURL(scrobbler.TrackInfo{
				Title:          "Karma Police",
				Artist:         "Radiohead",
				Album:          "OK Computer",
				MBZRecordingID: "mbid-123",
			})
			Expect(url).To(Equal("https://open.spotify.com/track/meta456"))
		})

		It("falls back to search URL when both lookups fail", func() {
			host.CacheMock.On("GetString", mock.Anything).Return("", false, nil)
			host.CacheMock.On("SetString", mock.Anything, mock.Anything, mock.Anything).Return(nil)

			// No MBID, metadata request fails
			metaReq := &pdk.HTTPRequest{}
			pdk.PDKMock.On("NewHTTPRequest", pdk.MethodPost, "https://labs.api.listenbrainz.org/spotify-id-from-metadata/json").Return(metaReq)
			pdk.PDKMock.On("Send", metaReq).Return(pdk.NewStubHTTPResponse(500, nil, []byte(`error`)))

			url := resolveSpotifyURL(scrobbler.TrackInfo{
				Title:  "Karma Police",
				Artist: "Radiohead",
				Album:  "OK Computer",
			})
			Expect(url).To(HavePrefix("https://open.spotify.com/search/"))
			Expect(url).To(ContainSubstring("Radiohead"))
			host.CacheMock.AssertCalled(GinkgoT(), "SetString", mock.Anything, mock.Anything, spotifyCacheTTLMiss)
		})

		It("uses Artists fallback when primary artist parse is empty", func() {
			host.CacheMock.On("GetString", mock.Anything).Return("", false, nil)
			host.CacheMock.On("SetString", mock.Anything, mock.Anything, mock.Anything).Return(nil)

			metaReq := &pdk.HTTPRequest{}
			pdk.PDKMock.On("NewHTTPRequest", pdk.MethodPost, "https://labs.api.listenbrainz.org/spotify-id-from-metadata/json").Return(metaReq)
			pdk.PDKMock.On("Send", metaReq).Return(pdk.NewStubHTTPResponse(200, nil,
				[]byte(`[{"spotify_track_ids":["fromArtists789"]}]`)))

			url := resolveSpotifyURL(scrobbler.TrackInfo{
				Title:   "Some Song",
				Artist:  "",
				Album:   "Some Album",
				Artists: []scrobbler.ArtistRef{{Name: "Fallback Artist"}},
			})
			Expect(url).To(Equal("https://open.spotify.com/track/fromArtists789"))
		})
	})
})
