package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
)

type spotifyCacheEntry struct {
	SeenReleaseIDs []string `json:"seenReleaseIds"`
}

type spotifyTokenResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
}

type spotifyReleasesResponse struct {
	Albums struct {
		Items []struct {
			ID           string `json:"id"`
			Name         string `json:"name"`
			ReleaseDate  string `json:"release_date"`
			ExternalURLs struct {
				Spotify string `json:"spotify"`
			} `json:"external_urls"`
			Artists []struct {
				Name string `json:"name"`
			} `json:"artists"`
			Images []struct {
				URL string `json:"url"`
			} `json:"images"`
		} `json:"items"`
	} `json:"albums"`
}

var (
	spotMu          sync.Mutex
	spotCache       map[string]spotifyCacheEntry
	spotCachePath   string
	spotCachedToken string
	spotTokenExpiry time.Time
)

func initSpotifyCache() {
	spotCachePath = dataPath("spotifyReleasesCache.json")
	spotCache = make(map[string]spotifyCacheEntry)

	data, err := os.ReadFile(spotCachePath)
	if err == nil {
		if err := json.Unmarshal(data, &spotCache); err != nil {
			log.Printf("Spotify notifier: parse cache %s: %v", spotCachePath, err)
			spotCache = make(map[string]spotifyCacheEntry)
		}
	} else if !os.IsNotExist(err) {
		log.Printf("Spotify notifier: read cache %s: %v", spotCachePath, err)
	}
	if spotCache == nil {
		spotCache = make(map[string]spotifyCacheEntry)
	}
}

func saveSpotifyCache() error {
	if spotCachePath == "" {
		return fmt.Errorf("Spotify cache path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(spotCachePath), 0755); err != nil {
		return fmt.Errorf("creating Spotify cache directory: %w", err)
	}
	data, err := json.MarshalIndent(spotCache, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding Spotify cache: %w", err)
	}
	if err := os.WriteFile(spotCachePath, data, 0644); err != nil {
		return fmt.Errorf("writing Spotify cache %s: %w", spotCachePath, err)
	}
	return nil
}

func getSpotifyAccessToken(cfg *Config) (string, error) {
	if cfg.SpotifyClientID == "" || cfg.SpotifyClientSecret == "" {
		return "", fmt.Errorf("missing spotify credentials")
	}

	if spotCachedToken != "" && time.Now().Before(spotTokenExpiry) {
		return spotCachedToken, nil
	}

	data := url.Values{}
	data.Set("grant_type", "client_credentials")

	req, err := http.NewRequest("POST", "https://accounts.spotify.com/api/token", strings.NewReader(data.Encode()))
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(cfg.SpotifyClientID, cfg.SpotifyClientSecret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := notifierClient(10 * time.Second)
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading Spotify token response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("Spotify token endpoint returned HTTP %d", resp.StatusCode)
	}
	var tResp spotifyTokenResponse
	if err := json.Unmarshal(body, &tResp); err != nil {
		return "", fmt.Errorf("decoding Spotify token response: %w", err)
	}
	if tResp.AccessToken == "" || tResp.ExpiresIn <= 0 {
		return "", fmt.Errorf("Spotify token response did not include a valid token and expiry")
	}

	spotCachedToken = tResp.AccessToken
	spotTokenExpiry = time.Now().Add(time.Duration(tResp.ExpiresIn-60) * time.Second)
	return spotCachedToken, nil
}

func checkSpotifyUpdates(s *discordgo.Session, cfg *Config) {
	if cfg.SpotifyClientID == "" || cfg.SpotifyClientSecret == "" {
		return
	}

	guilds := loadGuildSettings()
	spotMu.Lock()
	defer spotMu.Unlock()

	client := notifierClient(15 * time.Second)

	for guildID, gCfg := range guilds {
		if !gCfg.SpotifyEnabled || gCfg.SpotifyChannelID == "" {
			continue
		}

		token, err := getSpotifyAccessToken(cfg)
		if err != nil {
			log.Printf("Spotify token error: %v", err)
			return
		}

		req, err := http.NewRequest("GET", "https://api.spotify.com/v1/search?q=tag:new&type=album&limit=10&market=US", nil)
		if err != nil {
			log.Printf("Spotify notifier: create release request: %v", err)
			continue
		}
		req.Header.Set("Authorization", "Bearer "+token)

		resp, err := client.Do(req)
		if err != nil {
			log.Printf("Spotify notifier: fetch releases: %v", err)
			continue
		}

		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			log.Printf("Spotify notifier: read release response: %v", readErr)
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			log.Printf("Spotify notifier: release request returned HTTP %d", resp.StatusCode)
			continue
		}

		var relResp spotifyReleasesResponse
		if err := json.Unmarshal(body, &relResp); err != nil {
			log.Printf("Spotify notifier: decode release response: %v", err)
			continue
		}

		// Retrieve seen IDs for this guild
		previousEntry, hadPreviousEntry := spotCache[guildID]
		seen := append([]string(nil), previousEntry.SeenReleaseIDs...)

		seenSet := make(map[string]bool)
		for _, id := range seen {
			seenSet[id] = true
		}

		type releaseAnnouncement struct {
			id      string
			message string
		}
		var newAlbums []releaseAnnouncement
		for _, item := range relResp.Albums.Items {
			if item.ID == "" || seenSet[item.ID] {
				continue
			}
			var artistNames []string
			for _, a := range item.Artists {
				artistNames = append(artistNames, a.Name)
			}
			artists := strings.Join(artistNames, ", ")
			newAlbums = append(newAlbums, releaseAnnouncement{
				id:      item.ID,
				message: fmt.Sprintf("🎵 **%s** by **%s**\n%s", item.Name, artists, item.ExternalURLs.Spotify),
			})
		}

		if len(newAlbums) > 0 {
			header := "**🔥 New Spotify Releases Today:**\n"
			if gCfg.SpotifyRoleID != nil && *gCfg.SpotifyRoleID != "" {
				header = fmt.Sprintf("<@&%s> %s", *gCfg.SpotifyRoleID, header)
			}

			for _, album := range newAlbums {
				if _, err := sendNotifierMessage(s, gCfg.SpotifyChannelID, header+album.message); err != nil {
					log.Printf("Spotify notifier: send release %s to guild %s: %v", album.id, guildID, err)
					continue
				}

				seen = append(seen, album.id)
				seenSet[album.id] = true
				spotCache[guildID] = spotifyCacheEntry{SeenReleaseIDs: append([]string(nil), seen...)}
				if err := saveSpotifyCache(); err != nil {
					seen = seen[:len(seen)-1]
					delete(seenSet, album.id)
					if hadPreviousEntry {
						spotCache[guildID] = previousEntry
					} else {
						delete(spotCache, guildID)
					}
					log.Printf("Spotify notifier: persist delivered release %s for guild %s: %v", album.id, guildID, err)
				} else {
					previousEntry = spotifyCacheEntry{SeenReleaseIDs: append([]string(nil), seen...)}
					hadPreviousEntry = true
				}
			}
		}
	}
}

// startSpotifyNotifierDaemon runs daily Spotify release checks
func startSpotifyNotifierDaemon(ctx context.Context, s *discordgo.Session) {
	initSpotifyCache()
	cfg, err := loadConfig()
	if err != nil {
		log.Printf("Spotify notifier: load config: %v", err)
		return
	}
	if cfg.SpotifyClientID == "" || cfg.SpotifyClientSecret == "" {
		log.Printf("Spotify notifier not started: Spotify credentials are not configured")
		return
	}

	log.Printf("Spotify notifier daemon started (interval 24h)")
	runPeriodic(ctx, 30*time.Second, 24*time.Hour, func() { checkSpotifyUpdates(s, cfg) })
}
