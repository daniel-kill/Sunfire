package main

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
)

type notifierRoundTripFunc func(*http.Request) (*http.Response, error)

func (f notifierRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func notifierResponse(req *http.Request, status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}
}

func setNotifierTestState(t *testing.T, settings *GuildSettings, transport notifierRoundTripFunc) {
	t.Helper()
	oldClient := notifierHTTPClient
	oldMessage := sendNotifierMessage
	oldEmbed := sendNotifierEmbed
	oldComplex := sendNotifierComplex
	oldEdit := editNotifierComplex
	guildSettingsMu.Lock()
	oldGuildSettings := guildSettings
	guildSettings = map[string]*GuildSettings{SunfireServerID: settings}
	guildSettingsMu.Unlock()

	notifierHTTPClient = &http.Client{Transport: transport}
	sendNotifierMessage = func(_ *discordgo.Session, _, _ string) (*discordgo.Message, error) {
		return &discordgo.Message{ID: "sent-message"}, nil
	}
	sendNotifierEmbed = func(_ *discordgo.Session, _ string, _ *discordgo.MessageEmbed) (*discordgo.Message, error) {
		return &discordgo.Message{ID: "sent-message"}, nil
	}
	sendNotifierComplex = func(_ *discordgo.Session, _ string, _ *discordgo.MessageSend) (*discordgo.Message, error) {
		return &discordgo.Message{ID: "sent-message"}, nil
	}
	editNotifierComplex = func(_ *discordgo.Session, _ *discordgo.MessageEdit) (*discordgo.Message, error) {
		return &discordgo.Message{ID: "sent-message"}, nil
	}
	t.Cleanup(func() {
		notifierHTTPClient = oldClient
		sendNotifierMessage = oldMessage
		sendNotifierEmbed = oldEmbed
		sendNotifierComplex = oldComplex
		editNotifierComplex = oldEdit
		guildSettingsMu.Lock()
		guildSettings = oldGuildSettings
		guildSettingsMu.Unlock()
	})
}

func TestYouTubeNotifierCachesOnlyAfterSuccessfulDelivery(t *testing.T) {
	dir := t.TempDir()
	oldCache, oldPath := ytCache, ytCachePath
	ytCache = map[string]youtubeMonitorCacheEntry{SunfireServerID + ":UC12345678901234567890": {LastVideoID: "old"}}
	ytCachePath = filepath.Join(dir, "youtube.json")
	t.Cleanup(func() { ytCache, ytCachePath = oldCache, oldPath })
	setNotifierTestState(t, &GuildSettings{
		YouTubeEnabled: true, YouTubeChannelID: "channel", YouTubers: []string{"UC12345678901234567890"},
	}, func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/youtube/v3/playlistItems" {
			t.Fatalf("unexpected request URL: %s", req.URL)
		}
		return notifierResponse(req, http.StatusOK, `{"items":[{"snippet":{"title":"Latest","channelTitle":"Creator","resourceId":{"videoId":"new"}}}]}`), nil
	})

	sends := 0
	sendNotifierMessage = func(_ *discordgo.Session, channelID, _ string) (*discordgo.Message, error) {
		sends++
		if channelID != "channel" {
			t.Fatalf("sent to channel %q", channelID)
		}
		if sends == 1 {
			return nil, io.ErrUnexpectedEOF
		}
		return &discordgo.Message{ID: "sent"}, nil
	}
	cfg := &Config{YouTubeAPIKey: "test-key"}

	checkYouTubeUpdates(nil, cfg)
	if got := ytCache[SunfireServerID+":UC12345678901234567890"].LastVideoID; got != "old" {
		t.Fatalf("cache advanced after failed send: %q", got)
	}
	checkYouTubeUpdates(nil, cfg)
	if got := ytCache[SunfireServerID+":UC12345678901234567890"].LastVideoID; got != "new" {
		t.Fatalf("cache not advanced after successful send: %q", got)
	}
}

func TestYouTubeNotifierResolvesHandleWithoutDeadlock(t *testing.T) {
	dir := t.TempDir()
	oldCache, oldPath := ytCache, ytCachePath
	ytCache = map[string]youtubeMonitorCacheEntry{}
	ytCachePath = filepath.Join(dir, "youtube.json")
	t.Cleanup(func() { ytCache, ytCachePath = oldCache, oldPath })

	setNotifierTestState(t, &GuildSettings{
		YouTubeEnabled:   true,
		YouTubeChannelID: "channel",
		YouTubers:        []string{"https://www.youtube.com/@testhandle"},
	}, func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/youtube/v3/channels":
			if req.URL.Query().Get("forHandle") != "testhandle" {
				t.Fatalf("unexpected forHandle query: %s", req.URL.Query().Get("forHandle"))
			}
			return notifierResponse(req, http.StatusOK, `{"items":[{"id":"UCresolved1234567890"}]}`), nil
		case "/youtube/v3/playlistItems":
			if req.URL.Query().Get("playlistId") != "UUresolved1234567890" {
				t.Fatalf("unexpected playlistId: %s", req.URL.Query().Get("playlistId"))
			}
			return notifierResponse(req, http.StatusOK, `{"items":[{"snippet":{"title":"Latest Video","channelTitle":"Test Creator","resourceId":{"videoId":"v12345"}}}]}`), nil
		default:
			t.Fatalf("unexpected request URL: %s", req.URL)
			return nil, nil
		}
	})

	cfg := &Config{YouTubeAPIKey: "test-key"}
	checkYouTubeUpdates(nil, cfg)

	if got := ytCache[SunfireServerID+":UCresolved1234567890"].LastVideoID; got != "v12345" {
		t.Fatalf("expected initial seed v12345, got %q", got)
	}
}

func TestMoviesNotifierDoesNotCacheFailedDeliveryAndRollsBackFailedPersistence(t *testing.T) {
	dir := t.TempDir()
	oldCache, oldPath := moviesCache, moviesCachePath
	moviesCache = make(map[string]map[string][]string)
	moviesCachePath = filepath.Join(dir, "movies.json")
	t.Cleanup(func() { moviesCache, moviesCachePath = oldCache, oldPath })
	setNotifierTestState(t, &GuildSettings{MoviesThisWeekChannelID: "channel"}, func(req *http.Request) (*http.Response, error) {
		return notifierResponse(req, http.StatusOK, `{"results":[{"id":42,"title":"Test Movie","release_date":"2026-10-05"}]}`), nil
	})

	failSend := true
	sendNotifierComplex = func(_ *discordgo.Session, _ string, _ *discordgo.MessageSend) (*discordgo.Message, error) {
		if failSend {
			return nil, io.ErrUnexpectedEOF
		}
		return &discordgo.Message{ID: "sent"}, nil
	}
	cfg := &Config{TMDBAPIKey: "test-key"}
	checkMoviesUpdates(nil, cfg)
	if len(moviesCache[SunfireServerID]) != 0 {
		t.Fatalf("cache changed after failed send: %#v", moviesCache)
	}

	failSend = false
	blockedDir := filepath.Join(dir, "blocked")
	if err := os.WriteFile(blockedDir, []byte("block"), 0o600); err != nil {
		t.Fatal(err)
	}
	moviesCachePath = filepath.Join(blockedDir, "movies.json")
	checkMoviesUpdates(nil, cfg)
	if len(moviesCache[SunfireServerID]["seenMovieIds"]) != 0 {
		t.Fatalf("cache not rolled back after persistence error: %#v", moviesCache)
	}

	moviesCachePath = filepath.Join(dir, "movies.json")
	checkMoviesUpdates(nil, cfg)
	if got := moviesCache[SunfireServerID]["seenMovieIds"]; len(got) != 1 || got[0] != "42" {
		t.Fatalf("successful delivery was not persisted: %#v", got)
	}
}

func TestSpotifyNotifierCachesOnlyAfterSuccessfulDelivery(t *testing.T) {
	dir := t.TempDir()
	oldCache, oldPath, oldToken, oldExpiry := spotCache, spotCachePath, spotCachedToken, spotTokenExpiry
	spotCache = make(map[string]spotifyCacheEntry)
	spotCachePath = filepath.Join(dir, "spotify.json")
	spotCachedToken = "cached"
	spotTokenExpiry = time.Now().Add(time.Hour)
	t.Cleanup(func() {
		spotCache, spotCachePath, spotCachedToken, spotTokenExpiry = oldCache, oldPath, oldToken, oldExpiry
	})
	setNotifierTestState(t, &GuildSettings{SpotifyEnabled: true, SpotifyChannelID: "channel"}, func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/v1/search" {
			t.Fatalf("unexpected request URL: %s", req.URL)
		}
		return notifierResponse(req, http.StatusOK, `{"albums":{"items":[{"id":"release","name":"Album","artists":[{"name":"Artist"}],"external_urls":{"spotify":"https://open.spotify.com/album/release"}}]}}`), nil
	})
	sends := 0
	sendNotifierMessage = func(_ *discordgo.Session, _, _ string) (*discordgo.Message, error) {
		sends++
		if sends == 1 {
			return nil, io.ErrUnexpectedEOF
		}
		return &discordgo.Message{ID: "sent"}, nil
	}
	cfg := &Config{SpotifyClientID: "id", SpotifyClientSecret: "secret"}

	checkSpotifyUpdates(nil, cfg)
	if len(spotCache[SunfireServerID].SeenReleaseIDs) != 0 {
		t.Fatalf("cache changed after failed send: %#v", spotCache)
	}
	checkSpotifyUpdates(nil, cfg)
	if got := spotCache[SunfireServerID].SeenReleaseIDs; len(got) != 1 || got[0] != "release" {
		t.Fatalf("cache not advanced after successful send: %#v", got)
	}
}

func TestKickNotifierCachesOnlyAfterSuccessfulDelivery(t *testing.T) {
	dir := t.TempDir()
	oldCache, oldPath, oldToken, oldExpiry := kickCache, kickCachePath, kickCachedToken, kickTokenExpiry
	kickCache = make(map[string]kickCacheEntry)
	kickCachePath = filepath.Join(dir, "kick.json")
	kickCachedToken = "cached"
	kickTokenExpiry = time.Now().Add(time.Hour)
	t.Cleanup(func() {
		kickCache, kickCachePath, kickCachedToken, kickTokenExpiry = oldCache, oldPath, oldToken, oldExpiry
	})
	setNotifierTestState(t, &GuildSettings{KickEnabled: true, KickChannelID: "channel", KickStreamers: []string{"Streamer"}}, func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/public/v1/channels" {
			t.Fatalf("unexpected request URL: %s", req.URL)
		}
		return notifierResponse(req, http.StatusOK, `{"data":[{"slug":"streamer","stream_title":"Live now","stream":{"is_live":true}}]}`), nil
	})
	sends := 0
	sendNotifierComplex = func(_ *discordgo.Session, _ string, _ *discordgo.MessageSend) (*discordgo.Message, error) {
		sends++
		if sends == 1 {
			return nil, io.ErrUnexpectedEOF
		}
		return &discordgo.Message{ID: "sent"}, nil
	}
	cfg := &Config{KickClientID: "id", KickClientSecret: "secret"}

	checkKickUpdates(nil, cfg)
	if kickCache[SunfireServerID+":streamer"].IsLive {
		t.Fatal("cache marked live after failed send")
	}
	checkKickUpdates(nil, cfg)
	if !kickCache[SunfireServerID+":streamer"].IsLive || kickCache[SunfireServerID+":streamer"].LastMessageID != "sent" {
		t.Fatalf("cache not updated after successful send: %#v", kickCache[SunfireServerID+":streamer"])
	}
}

func TestFreeStuffNotifierCachesOnlyAfterSuccessfulDelivery(t *testing.T) {
	dir := t.TempDir()
	oldCache, oldPath := freeStuffCache, freeStuffCachePath
	freeStuffCache = freeStuffState{Guilds: make(map[string]struct {
		InitializedSources map[string]bool `json:"initializedSources"`
		SeenItems          struct {
			GGDeals     []string `json:"ggDeals"`
			ProductHunt []string `json:"productHunt"`
		} `json:"seenItems"`
	})}
	freeStuffCachePath = filepath.Join(dir, "free.json")
	t.Cleanup(func() { freeStuffCache, freeStuffCachePath = oldCache, oldPath })
	setNotifierTestState(t, &GuildSettings{FreeStuffEnabled: true, FreeStuffChannelID: "channel"}, func(req *http.Request) (*http.Response, error) {
		return notifierResponse(req, http.StatusOK, `<rss><channel><item><title>Free game</title><link>https://example.test/freebie</link></item></channel></rss>`), nil
	})
	sends := 0
	sendNotifierMessage = func(_ *discordgo.Session, _, _ string) (*discordgo.Message, error) {
		sends++
		if sends == 1 {
			return nil, io.ErrUnexpectedEOF
		}
		return &discordgo.Message{ID: "sent"}, nil
	}

	checkFreeStuffUpdates(nil, &Config{})
	if len(freeStuffCache.Guilds[SunfireServerID].SeenItems.GGDeals) != 0 {
		t.Fatal("cache changed after failed send")
	}
	checkFreeStuffUpdates(nil, &Config{})
	if got := freeStuffCache.Guilds[SunfireServerID].SeenItems.GGDeals; len(got) != 1 || got[0] != "https://example.test/freebie" {
		t.Fatalf("cache not advanced after successful send: %#v", got)
	}
}

func TestScrapeNotifierCachesOnlyAfterSuccessfulDelivery(t *testing.T) {
	dir := t.TempDir()
	oldCache, oldPath := scrapeCache, scrapeCachePath
	scrapeCache = scrapeState{Posted: make(map[string][]string), Attempted: make(map[string][]string)}
	scrapeCachePath = filepath.Join(dir, "scrape.json")
	t.Cleanup(func() { scrapeCache, scrapeCachePath = oldCache, oldPath })
	setNotifierTestState(t, &GuildSettings{}, func(req *http.Request) (*http.Response, error) {
		return notifierResponse(req, http.StatusOK, `<html>https://example.test/image.jpg</html>`), nil
	})
	failSend := true
	sendNotifierEmbed = func(_ *discordgo.Session, _ string, _ *discordgo.MessageEmbed) (*discordgo.Message, error) {
		if failSend {
			return nil, io.ErrUnexpectedEOF
		}
		return &discordgo.Message{ID: "sent"}, nil
	}
	cfg := &Config{Scrape: ScrapeConfig{
		URLs:       []string{"https://example.test/gallery"},
		ChannelIDs: []string{"channel"},
	}}

	runDailyImageScrape(nil, cfg)
	if len(scrapeCache.Posted["https://example.test/gallery\x00channel"]) != 0 {
		t.Fatal("cache changed after failed send")
	}
	failSend = false
	runDailyImageScrape(nil, cfg)
	if got := scrapeCache.Posted["https://example.test/gallery\x00channel"]; len(got) != 1 || got[0] != "https://example.test/image.jpg" {
		t.Fatalf("cache not advanced after successful send: %#v", got)
	}
}

func TestDiscordChangeLogNotifierSeedsInitialAndDeliversNewEntries(t *testing.T) {
	dir := t.TempDir()
	oldCache, oldPath := discordChangeLogCache, discordChangeLogCachePath
	discordChangeLogCache = discordChangeLogState{
		Guilds: make(map[string]struct {
			Initialized bool     `json:"initialized"`
			SeenGUIDs   []string `json:"seenGuids"`
		}),
	}
	discordChangeLogCachePath = filepath.Join(dir, "discord_changelog.json")
	t.Cleanup(func() { discordChangeLogCache, discordChangeLogCachePath = oldCache, oldPath })

	sampleRSS := `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0">
	<channel>
		<title>Change Log</title>
		<item>
			<title>First Entry</title>
			<description>First description</description>
			<link>https://docs.discord.com/developers/change-log#one</link>
			<guid>guid-1</guid>
			<category>HTTP API</category>
			<pubDate>Fri, 30 Oct 2026 12:00:00 GMT</pubDate>
		</item>
	</channel>
</rss>`

	currentRSS := sampleRSS
	setNotifierTestState(t, &GuildSettings{
		DiscordChangeLogEnabled:   true,
		DiscordChangeLogChannelID: "target-channel",
	}, func(req *http.Request) (*http.Response, error) {
		return notifierResponse(req, http.StatusOK, currentRSS), nil
	})

	sentCount := 0
	var lastSent *discordgo.MessageSend
	sendNotifierComplex = func(_ *discordgo.Session, channelID string, message *discordgo.MessageSend) (*discordgo.Message, error) {
		if channelID != "target-channel" {
			t.Fatalf("unexpected target channel: %s", channelID)
		}
		sentCount++
		lastSent = message
		return &discordgo.Message{ID: "msg-1"}, nil
	}

	// 1. Initial run seeds the cache without sending announcements
	checkDiscordChangeLogUpdates(nil, &Config{})
	if sentCount != 0 {
		t.Fatalf("expected 0 messages on initial seed, got %d", sentCount)
	}
	gState := discordChangeLogCache.Guilds[SunfireServerID]
	if !gState.Initialized || len(gState.SeenGUIDs) != 1 || gState.SeenGUIDs[0] != "guid-1" {
		t.Fatalf("initial seed not recorded properly: %#v", gState)
	}

	// 2. Add second entry to feed
	secondRSS := `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0">
	<channel>
		<title>Change Log</title>
		<item>
			<title>Second Entry</title>
			<description>Second description</description>
			<link>https://docs.discord.com/developers/change-log#two</link>
			<guid>guid-2</guid>
			<category>Gateway</category>
			<pubDate>Sat, 31 Oct 2026 12:00:00 GMT</pubDate>
		</item>
		<item>
			<title>First Entry</title>
			<description>First description</description>
			<link>https://docs.discord.com/developers/change-log#one</link>
			<guid>guid-1</guid>
			<category>HTTP API</category>
			<pubDate>Fri, 30 Oct 2026 12:00:00 GMT</pubDate>
		</item>
	</channel>
</rss>`

	currentRSS = secondRSS
	checkDiscordChangeLogUpdates(nil, &Config{})
	if sentCount != 1 {
		t.Fatalf("expected 1 message for new item, got %d", sentCount)
	}
	if lastSent == nil || len(lastSent.Embeds) == 0 || lastSent.Embeds[0].Title != "Second Entry" {
		t.Fatalf("expected embed with title 'Second Entry', got %#v", lastSent)
	}
	gState = discordChangeLogCache.Guilds[SunfireServerID]
	if len(gState.SeenGUIDs) != 2 || gState.SeenGUIDs[1] != "guid-2" {
		t.Fatalf("cache not updated with new GUID: %#v", gState)
	}
}
