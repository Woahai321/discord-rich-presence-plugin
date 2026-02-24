package main

import (
	"strings"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/stretchr/testify/mock"
)

func TestDiscordPlugin(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Discord Plugin Main Suite")
}

// Shared matchers for tighter mock expectations across all test files.
var (
	discordImageKey   = mock.MatchedBy(func(key string) bool { return strings.HasPrefix(key, "discord.image.") })
	externalAssetsURL = mock.MatchedBy(func(url string) bool { return strings.Contains(url, "external-assets") })
	spotifyURLKey     = mock.MatchedBy(func(key string) bool { return strings.HasPrefix(key, "spotify.url.") })
)
