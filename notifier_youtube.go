package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
)

type youtubeMonitorCacheEntry struct {
	LastVideoID string `json:"lastVideoId"`
}

type youtubeSearchResponse struct {
	Items []struct {
		ID struct {
			VideoID string `json:"videoId"`
		} `json:"id"`
		Snippet struct {
			Title        string    `json:"title"`
			PublishedAt  time.Time `json:"publishedAt"`
			ChannelTitle string    `json:"channelTitle"`
		} `json:"snippet"`
	} `json:"items"`
}

type youtubeChannelResponse struct {
	Items []struct {
		ID string `json:"id"`
	} `json:"items"`
}

var (
	ytMu        sync.Mutex
	ytCache     map[string]youtubeMonitorCacheEntry
	ytCachePath string
)

func initYTCache() {
	ytCachePath = dataPath("youtubeMonitorCache.json")
	ytCache = make(map[string]youtubeMonitorCacheEntry)

	data, err := os.ReadFile(ytCachePath)
	if err == nil {
		if err := json.Unmarshal(data, &ytCache); err != nil {
			log.Printf("YouTube notifier: parse cache %s: %v", ytCachePath, err)
			ytCache = make(map[string]youtubeMonitorCacheEntry)
		}
	} else if !os.IsNotExist(err) {
		log.Printf("YouTube notifier: read cache %s: %v", ytCachePath, err)
	}
	if ytCache == nil {
		ytCache = make(map[string]youtubeMonitorCacheEntry)
	}
}

func saveYTCache() error {
	if ytCachePath == "" {
		return fmt.Errorf("YouTube cache path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(ytCachePath), 0755); err != nil {
		return fmt.Errorf("creating YouTube cache directory: %w", err)
	}
	data, err := json.MarshalIndent(ytCache, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding YouTube cache: %w", err)
	}
	if err := os.WriteFile(ytCachePath, data, 0644); err != nil {
		return fmt.Errorf("writing YouTube cache %s: %w", ytCachePath, err)
	}
	return nil
}

// resolveYouTubeChannelID resolves an @handle or returns channel ID directly
func resolveYouTubeChannelID(source, apiKey string) (string, error) {
	s := strings.TrimSpace(source)
	if strings.HasPrefix(s, "https://www.youtube.com/") || strings.HasPrefix(s, "https://youtube.com/") {
		parts := strings.Split(strings.TrimRight(s, "/"), "/")
		if len(parts) > 0 {
			s = parts[len(parts)-1]
		}
	}

	if strings.HasPrefix(s, "UC") && len(s) >= 20 {
		return s, nil
	}

	if strings.HasPrefix(s, "@") {
		handle := strings.TrimPrefix(s, "@")
		query := url.Values{
			"part":      {"id"},
			"forHandle": {handle},
			"key":       {apiKey},
		}
		apiURL := "https://www.googleapis.com/youtube/v3/channels?" + query.Encode()
		client := notifierClient(15 * time.Second)
		resp, err := client.Get(apiURL)
		if err != nil {
			var urlErr *url.Error
			if errors.As(err, &urlErr) {
				return "", fmt.Errorf("YouTube channel request failed: %w", urlErr.Err)
			}
			return "", err
		}
		defer resp.Body.Close()

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return "", fmt.Errorf("reading YouTube channel response: %w", err)
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return "", fmt.Errorf("YouTube channel lookup returned HTTP %d", resp.StatusCode)
		}
		var cResp youtubeChannelResponse
		if err := json.Unmarshal(body, &cResp); err != nil {
			return "", fmt.Errorf("decoding YouTube channel response: %w", err)
		}
		if len(cResp.Items) > 0 {
			return cResp.Items[0].ID, nil
		}
	}

	return "", fmt.Errorf("unable to resolve channel ID for %s", source)
}

// checkYouTubeUpdates polls configured channels for new videos
func checkYouTubeUpdates(s *discordgo.Session, cfg *Config) {
	if cfg.YouTubeAPIKey == "" {
		return
	}

	guilds := loadGuildSettings()
	ytMu.Lock()
	defer ytMu.Unlock()

	for guildID, gCfg := range guilds {
		if !gCfg.YouTubeEnabled || gCfg.YouTubeChannelID == "" || len(gCfg.YouTubers) == 0 {
			continue
		}

		for _, source := range gCfg.YouTubers {
			chanID, err := resolveYouTubeChannelID(source, cfg.YouTubeAPIKey)
			if err != nil {
				log.Printf("YouTube notifier: resolve channel %q: %v", source, err)
				continue
			}

			cacheKey := fmt.Sprintf("%s:%s", guildID, chanID)
			cached := ytCache[cacheKey]

			query := url.Values{
				"part":       {"snippet"},
				"channelId":  {chanID},
				"order":      {"date"},
				"type":       {"video"},
				"maxResults": {"1"},
				"key":        {cfg.YouTubeAPIKey},
			}
			apiURL := "https://www.googleapis.com/youtube/v3/search?" + query.Encode()
			client := notifierClient(15 * time.Second)
			resp, err := client.Get(apiURL)
			if err != nil {
				var urlErr *url.Error
				if errors.As(err, &urlErr) {
					err = urlErr.Err
				}
				log.Printf("YouTube notifier: fetch latest video for channel %s: %v", chanID, err)
				continue
			}

			body, readErr := io.ReadAll(resp.Body)
			resp.Body.Close()
			if readErr != nil {
				log.Printf("YouTube notifier: read latest video response for channel %s: %v", chanID, readErr)
				continue
			}
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				log.Printf("YouTube notifier: latest video request for channel %s returned HTTP %d", chanID, resp.StatusCode)
				continue
			}

			var sResp youtubeSearchResponse
			if err := json.Unmarshal(body, &sResp); err != nil {
				log.Printf("YouTube notifier: decode latest video response for channel %s: %v", chanID, err)
				continue
			}
			if len(sResp.Items) == 0 {
				log.Printf("YouTube notifier: no videos returned for channel %s", chanID)
				continue
			}

			latest := sResp.Items[0]
			videoID := latest.ID.VideoID
			if videoID == "" {
				continue
			}

			if cached.LastVideoID == "" {
				// Initial seed
				ytCache[cacheKey] = youtubeMonitorCacheEntry{LastVideoID: videoID}
				if err := saveYTCache(); err != nil {
					delete(ytCache, cacheKey)
					log.Printf("YouTube notifier: persist initial video state for channel %s: %v", chanID, err)
				}
				continue
			}

			if cached.LastVideoID != videoID {
				videoURL := fmt.Sprintf("https://www.youtube.com/watch?v=%s", videoID)
				content := fmt.Sprintf("🎬 **%s** uploaded a new video!\n**%s**\n%s", latest.Snippet.ChannelTitle, latest.Snippet.Title, videoURL)
				if gCfg.YouTubeRoleID != nil && *gCfg.YouTubeRoleID != "" {
					content = fmt.Sprintf("<@&%s> %s", *gCfg.YouTubeRoleID, content)
				}

				if _, err := sendNotifierMessage(s, gCfg.YouTubeChannelID, content); err != nil {
					log.Printf("YouTube notifier: send upload announcement for channel %s: %v", chanID, err)
					continue
				}
				ytCache[cacheKey] = youtubeMonitorCacheEntry{LastVideoID: videoID}
				if err := saveYTCache(); err != nil {
					ytCache[cacheKey] = cached
					log.Printf("YouTube notifier: persist delivered video state for channel %s: %v", chanID, err)
				}
			}
		}
	}
}

// startYouTubeNotifierDaemon runs periodic YouTube video checks
func startYouTubeNotifierDaemon(ctx context.Context, s *discordgo.Session) {
	initYTCache()
	cfg, err := loadConfig()
	if err != nil {
		log.Printf("YouTube notifier: load config: %v", err)
		return
	}
	if cfg.YouTubeAPIKey == "" {
		log.Printf("YouTube notifier not started: YouTube API key is not configured")
		return
	}

	interval := 20 * time.Minute
	if cfg.YouTubePollIntervalMs > 0 {
		interval = time.Duration(cfg.YouTubePollIntervalMs) * time.Millisecond
	}

	log.Printf("YouTube notifier daemon started (interval %v)", interval)
	runPeriodic(ctx, 10*time.Second, interval, func() { checkYouTubeUpdates(s, cfg) })
}
