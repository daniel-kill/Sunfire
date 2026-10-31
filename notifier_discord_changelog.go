package main

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
)

const (
	defaultDiscordChangeLogFeedURL      = "https://docs.discord.com/developers/change-log/rss.xml"
	defaultDiscordChangeLogPollInterval = 30 * time.Minute
)

type discordChangeLogRSS struct {
	XMLName xml.Name `xml:"rss"`
	Channel struct {
		Title string                 `xml:"title"`
		Items []discordChangeLogItem `xml:"item"`
	} `xml:"channel"`
}

type discordChangeLogItem struct {
	Title       string   `xml:"title"`
	Description string   `xml:"description"`
	Link        string   `xml:"link"`
	GUID        string   `xml:"guid"`
	Categories  []string `xml:"category"`
	PubDate     string   `xml:"pubDate"`
}

type discordChangeLogState struct {
	Guilds map[string]struct {
		Initialized bool     `json:"initialized"`
		SeenGUIDs   []string `json:"seenGuids"`
	} `json:"guilds"`
}

var (
	discordChangeLogMu        sync.Mutex
	discordChangeLogCache     discordChangeLogState
	discordChangeLogCachePath string
)

func initDiscordChangeLogCache() {
	discordChangeLogCachePath = dataPath("discordChangeLogCache.json")
	discordChangeLogCache.Guilds = make(map[string]struct {
		Initialized bool     `json:"initialized"`
		SeenGUIDs   []string `json:"seenGuids"`
	})

	data, err := os.ReadFile(discordChangeLogCachePath)
	if err == nil {
		if err := json.Unmarshal(data, &discordChangeLogCache); err != nil {
			log.Printf("Discord changelog notifier: parse cache %s: %v", discordChangeLogCachePath, err)
			discordChangeLogCache.Guilds = make(map[string]struct {
				Initialized bool     `json:"initialized"`
				SeenGUIDs   []string `json:"seenGuids"`
			})
		}
	} else if !os.IsNotExist(err) {
		log.Printf("Discord changelog notifier: read cache %s: %v", discordChangeLogCachePath, err)
	}
	if discordChangeLogCache.Guilds == nil {
		discordChangeLogCache.Guilds = make(map[string]struct {
			Initialized bool     `json:"initialized"`
			SeenGUIDs   []string `json:"seenGuids"`
		})
	}
}

func saveDiscordChangeLogCache() error {
	if discordChangeLogCachePath == "" {
		return fmt.Errorf("Discord changelog cache path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(discordChangeLogCachePath), 0755); err != nil {
		return fmt.Errorf("creating Discord changelog cache directory: %w", err)
	}
	data, err := json.MarshalIndent(discordChangeLogCache, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding Discord changelog cache: %w", err)
	}
	if err := os.WriteFile(discordChangeLogCachePath, data, 0644); err != nil {
		return fmt.Errorf("writing Discord changelog cache %s: %w", discordChangeLogCachePath, err)
	}
	return nil
}

func checkDiscordChangeLogUpdates(s *discordgo.Session, cfg *Config) {
	guilds := loadGuildSettings()

	anyEnabled := false
	for _, gCfg := range guilds {
		if gCfg.DiscordChangeLogEnabled && gCfg.DiscordChangeLogChannelID != "" {
			anyEnabled = true
			break
		}
	}
	if !anyEnabled {
		return
	}

	feedURL := defaultDiscordChangeLogFeedURL
	if cfg != nil && cfg.DiscordChangeLogFeedURL != "" {
		feedURL = cfg.DiscordChangeLogFeedURL
	}

	client := notifierClient(15 * time.Second)
	resp, err := client.Get(feedURL)
	if err != nil {
		log.Printf("Discord changelog notifier: fetch feed %s: %v", feedURL, err)
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("Discord changelog notifier: read feed: %v", err)
		return
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Printf("Discord changelog notifier: feed returned HTTP %d", resp.StatusCode)
		return
	}

	var feed discordChangeLogRSS
	if err := xml.Unmarshal(body, &feed); err != nil {
		log.Printf("Discord changelog notifier: parse RSS xml: %v", err)
		return
	}

	items := feed.Channel.Items
	if len(items) == 0 {
		return
	}

	discordChangeLogMu.Lock()
	defer discordChangeLogMu.Unlock()

	for guildID, gCfg := range guilds {
		if !gCfg.DiscordChangeLogEnabled || gCfg.DiscordChangeLogChannelID == "" {
			continue
		}

		gState, exists := discordChangeLogCache.Guilds[guildID]
		if !exists || !gState.Initialized {
			allGuids := make([]string, 0, len(items))
			for _, itm := range items {
				id := strings.TrimSpace(itm.GUID)
				if id == "" {
					id = strings.TrimSpace(itm.Link)
				}
				if id != "" {
					allGuids = append(allGuids, id)
				}
			}
			discordChangeLogCache.Guilds[guildID] = struct {
				Initialized bool     `json:"initialized"`
				SeenGUIDs   []string `json:"seenGuids"`
			}{
				Initialized: true,
				SeenGUIDs:   allGuids,
			}
			if err := saveDiscordChangeLogCache(); err != nil {
				log.Printf("Discord changelog notifier: persist initial seed for guild %s: %v", guildID, err)
			}
			continue
		}

		seenMap := make(map[string]bool, len(gState.SeenGUIDs))
		for _, id := range gState.SeenGUIDs {
			seenMap[id] = true
		}

		var newItems []discordChangeLogItem
		for _, itm := range items {
			id := strings.TrimSpace(itm.GUID)
			if id == "" {
				id = strings.TrimSpace(itm.Link)
			}
			if id != "" && !seenMap[id] {
				newItems = append(newItems, itm)
			}
		}

		if len(newItems) == 0 {
			continue
		}

		// Reverse so oldest unseen entry is posted first
		for i, j := 0, len(newItems)-1; i < j; i, j = i+1, j-1 {
			newItems[i], newItems[j] = newItems[j], newItems[i]
		}

		header := "📢 **Discord Developer Changelog Update**"
		if gCfg.DiscordChangeLogRoleID != nil && *gCfg.DiscordChangeLogRoleID != "" {
			header = fmt.Sprintf("<@&%s> %s", *gCfg.DiscordChangeLogRoleID, header)
		}

		for _, itm := range newItems {
			id := strings.TrimSpace(itm.GUID)
			if id == "" {
				id = strings.TrimSpace(itm.Link)
			}

			desc := itm.Description
			if len(desc) > 2048 {
				desc = desc[:2045] + "..."
			}

			embed := &discordgo.MessageEmbed{
				Title:       itm.Title,
				URL:         itm.Link,
				Description: desc,
				Color:       0x5865F2, // Discord Blurple
				Footer: &discordgo.MessageEmbedFooter{
					Text: "Discord Developer Documentation • Change Log",
				},
			}

			if len(itm.Categories) > 0 {
				embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
					Name:   "Categories",
					Value:  strings.Join(itm.Categories, ", "),
					Inline: true,
				})
			}
			if itm.PubDate != "" {
				embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
					Name:   "Date",
					Value:  itm.PubDate,
					Inline: true,
				})
			}

			msgSend := &discordgo.MessageSend{
				Content: header,
				Embeds:  []*discordgo.MessageEmbed{embed},
			}

			if _, err := sendNotifierComplex(s, gCfg.DiscordChangeLogChannelID, msgSend); err != nil {
				log.Printf("Discord changelog notifier: send update %s to channel %s: %v", id, gCfg.DiscordChangeLogChannelID, err)
				continue
			}

			gState.SeenGUIDs = append(gState.SeenGUIDs, id)
			discordChangeLogCache.Guilds[guildID] = gState
			if err := saveDiscordChangeLogCache(); err != nil {
				log.Printf("Discord changelog notifier: persist seen item %s: %v", id, err)
			}
		}
	}
}

func startDiscordChangeLogNotifierDaemon(ctx context.Context, s *discordgo.Session) {
	initDiscordChangeLogCache()
	cfg, err := loadConfig()
	if err != nil {
		log.Printf("Discord changelog notifier: load config: %v", err)
		return
	}

	interval := defaultDiscordChangeLogPollInterval
	if cfg != nil && cfg.DiscordChangeLogPollIntervalMs > 0 {
		interval = time.Duration(cfg.DiscordChangeLogPollIntervalMs) * time.Millisecond
	}

	log.Printf("Discord changelog notifier daemon started (interval %v)", interval)
	runPeriodic(ctx, 15*time.Second, interval, func() { checkDiscordChangeLogUpdates(s, cfg) })
}
