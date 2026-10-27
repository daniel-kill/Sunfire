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

type kickCacheEntry struct {
	IsLive        bool   `json:"isLive"`
	LastStreamID  *int   `json:"lastStreamId"`
	LastMessageID string `json:"lastMessageId"`
	LastTitle     string `json:"lastTitle"`
}

type kickTokenResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
	TokenType   string `json:"token_type"`
}

type kickChannelsResponse struct {
	Data []struct {
		BroadcasterUserID int    `json:"broadcaster_user_id"`
		Slug              string `json:"slug"`
		ChannelDesc       string `json:"channel_description"`
		BannerPicture     string `json:"banner_picture"`
		StreamTitle       string `json:"stream_title"`
		Stream            struct {
			IsLive      bool   `json:"is_live"`
			IsMature    bool   `json:"is_mature"`
			Language    string `json:"language"`
			StartTime   string `json:"start_time"`
			ViewerCount int    `json:"viewer_count"`
			Thumbnail   string `json:"thumbnail"`
			Key         string `json:"key"`
		} `json:"stream"`
		Category struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
		} `json:"category"`
	} `json:"data"`
	Message string `json:"message"`
}

var (
	kickMu          sync.Mutex
	kickCache       map[string]kickCacheEntry
	kickCachePath   string
	kickCachedToken string
	kickTokenExpiry time.Time
)

func initKickCache() {
	kickCachePath = dataPath("kickNotifier-cache.json")
	kickCache = make(map[string]kickCacheEntry)

	data, err := os.ReadFile(kickCachePath)
	if err == nil {
		if err := json.Unmarshal(data, &kickCache); err != nil {
			log.Printf("Kick notifier: parse cache %s: %v", kickCachePath, err)
			kickCache = make(map[string]kickCacheEntry)
		}
	} else if !os.IsNotExist(err) {
		log.Printf("Kick notifier: read cache %s: %v", kickCachePath, err)
	}
	if kickCache == nil {
		kickCache = make(map[string]kickCacheEntry)
	}
}

func saveKickCache() error {
	if kickCachePath == "" {
		return fmt.Errorf("Kick cache path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(kickCachePath), 0755); err != nil {
		return fmt.Errorf("creating Kick cache directory: %w", err)
	}
	data, err := json.MarshalIndent(kickCache, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding Kick cache: %w", err)
	}
	if err := os.WriteFile(kickCachePath, data, 0644); err != nil {
		return fmt.Errorf("writing Kick cache %s: %w", kickCachePath, err)
	}
	return nil
}

func getKickAccessToken(cfg *Config) (string, error) {
	if cfg.KickClientID == "" || cfg.KickClientSecret == "" {
		return "", fmt.Errorf("missing kick credentials")
	}

	if kickCachedToken != "" && time.Now().Before(kickTokenExpiry) {
		return kickCachedToken, nil
	}

	data := url.Values{}
	data.Set("grant_type", "client_credentials")
	data.Set("client_id", cfg.KickClientID)
	data.Set("client_secret", cfg.KickClientSecret)

	req, err := http.NewRequest("POST", "https://id.kick.com/oauth/token", strings.NewReader(data.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := notifierClient(10 * time.Second)
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading Kick token response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("Kick token endpoint returned HTTP %d", resp.StatusCode)
	}
	var tResp kickTokenResponse
	if err := json.Unmarshal(body, &tResp); err != nil {
		return "", fmt.Errorf("decoding Kick token response: %w", err)
	}
	if tResp.AccessToken == "" || tResp.ExpiresIn <= 0 {
		return "", fmt.Errorf("Kick token response did not include a valid token and expiry")
	}

	kickCachedToken = tResp.AccessToken
	kickTokenExpiry = time.Now().Add(time.Duration(tResp.ExpiresIn-60) * time.Second)
	return kickCachedToken, nil
}

func checkKickUpdates(s *discordgo.Session, cfg *Config) {
	if cfg.KickClientID == "" || cfg.KickClientSecret == "" {
		return
	}

	guilds := loadGuildSettings()
	kickMu.Lock()
	defer kickMu.Unlock()

	client := notifierClient(10 * time.Second)

	for guildID, gCfg := range guilds {
		if !gCfg.KickEnabled || gCfg.KickChannelID == "" || len(gCfg.KickStreamers) == 0 {
			continue
		}

		token, err := getKickAccessToken(cfg)
		if err != nil {
			log.Printf("Kick notifier token error: %v", err)
			return
		}

		for _, streamer := range gCfg.KickStreamers {
			slug := strings.ToLower(strings.TrimSpace(streamer))
			if slug == "" {
				continue
			}

			cacheKey := fmt.Sprintf("%s:%s", guildID, slug)
			cached := kickCache[cacheKey]

			apiURL := fmt.Sprintf("https://api.kick.com/public/v1/channels?slug=%s", url.QueryEscape(slug))
			req, err := http.NewRequest("GET", apiURL, nil)
			if err != nil {
				continue
			}
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("Accept", "application/json")

			resp, err := client.Do(req)
			if err != nil {
				log.Printf("Kick notifier: fetch channel %s: %v", slug, err)
				continue
			}

			body, readErr := io.ReadAll(resp.Body)
			resp.Body.Close()
			if readErr != nil {
				log.Printf("Kick notifier: read channel response for %s: %v", slug, readErr)
				continue
			}
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				log.Printf("Kick notifier: channel request for %s returned HTTP %d", slug, resp.StatusCode)
				continue
			}

			var kResp kickChannelsResponse
			if err := json.Unmarshal(body, &kResp); err != nil {
				log.Printf("Kick notifier: decode channel response for %s: %v", slug, err)
				continue
			}
			if len(kResp.Data) == 0 {
				log.Printf("Kick notifier: no channel data returned for %s", slug)
				continue
			}

			ch := kResp.Data[0]
			isLive := ch.Stream.IsLive
			if isLive && !cached.IsLive {
				streamTitle := "Streaming on Kick"
				if ch.StreamTitle != "" {
					streamTitle = ch.StreamTitle
				}

				kickURL := fmt.Sprintf("https://kick.com/%s", slug)
				embed := &discordgo.MessageEmbed{
					Title:       fmt.Sprintf("%s is LIVE on Kick!", streamer),
					URL:         kickURL,
					Description: streamTitle,
					Color:       0x53FC18, // Kick green
					Timestamp:   time.Now().Format(time.RFC3339),
				}

				if ch.Stream.Thumbnail != "" {
					embed.Image = &discordgo.MessageEmbedImage{
						URL: ch.Stream.Thumbnail,
					}
				}

				var mention string
				if gCfg.KickStreamingRoleID != nil && *gCfg.KickStreamingRoleID != "" {
					mention = fmt.Sprintf("<@&%s>", *gCfg.KickStreamingRoleID)
				}

				msg, err := sendNotifierComplex(s, gCfg.KickChannelID, &discordgo.MessageSend{
					Content: mention,
					Embed:   embed,
				})
				if err != nil {
					log.Printf("Kick notifier: send live announcement for %s: %v", slug, err)
					continue
				}
				if msg == nil || msg.ID == "" {
					log.Printf("Kick notifier: live announcement for %s returned no message ID", slug)
					continue
				}
				kickCache[cacheKey] = kickCacheEntry{
					IsLive:        true,
					LastStreamID:  nil,
					LastMessageID: msg.ID,
					LastTitle:     streamTitle,
				}
				if err := saveKickCache(); err != nil {
					kickCache[cacheKey] = cached
					log.Printf("Kick notifier: persist live state for %s after delivery: %v", slug, err)
				}
			} else if !isLive && cached.IsLive {
				// Update Discord message to indicate stream ended if we have the message ID
				if cached.LastMessageID != "" && gCfg.KickChannelID != "" {
					kickURL := fmt.Sprintf("https://kick.com/%s", slug)
					title := "Streaming on Kick"
					if cached.LastTitle != "" {
						title = cached.LastTitle
					}
					offlineEmbed := &discordgo.MessageEmbed{
						Title:       fmt.Sprintf("%s was LIVE on Kick (Offline)", streamer),
						URL:         kickURL,
						Description: fmt.Sprintf("~~%s~~\n\n🔴 *Stream has ended.*", title),
						Color:       0x747F8D, // Discord muted gray
						Timestamp:   time.Now().Format(time.RFC3339),
					}
					emptyContent := ""
					if _, err := editNotifierComplex(s, &discordgo.MessageEdit{
						Channel: gCfg.KickChannelID,
						ID:      cached.LastMessageID,
						Content: &emptyContent,
						Embed:   offlineEmbed,
					}); err != nil {
						log.Printf("Kick notifier: update offline announcement for %s: %v", slug, err)
						continue
					}
				} else {
					log.Printf("Kick notifier: stream %s is offline but no announcement message ID is cached", slug)
				}

				kickCache[cacheKey] = kickCacheEntry{
					IsLive:        false,
					LastStreamID:  cached.LastStreamID,
					LastMessageID: cached.LastMessageID,
					LastTitle:     cached.LastTitle,
				}
				if err := saveKickCache(); err != nil {
					kickCache[cacheKey] = cached
					log.Printf("Kick notifier: persist offline state for %s after delivery: %v", slug, err)
				}
			}
		}
	}
}

// startKickNotifierDaemon runs periodic Kick livestream checks
func startKickNotifierDaemon(ctx context.Context, s *discordgo.Session) {
	initKickCache()
	cfg, err := loadConfig()
	if err != nil {
		log.Printf("Kick notifier: load config: %v", err)
		return
	}
	if cfg.KickClientID == "" || cfg.KickClientSecret == "" {
		log.Printf("Kick notifier not started: Kick credentials are not configured")
		return
	}

	interval := 5 * time.Minute
	if cfg.KickPollIntervalMs > 0 {
		interval = time.Duration(cfg.KickPollIntervalMs) * time.Millisecond
	}

	log.Printf("Kick notifier daemon started (interval %v)", interval)
	runPeriodic(ctx, 15*time.Second, interval, func() { checkKickUpdates(s, cfg) })
}
