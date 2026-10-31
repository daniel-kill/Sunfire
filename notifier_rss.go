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
	defaultRSSPollInterval = 20 * time.Minute
)

type rssFeedXML struct {
	XMLName xml.Name `xml:"rss"`
	Channel struct {
		Title       string        `xml:"title"`
		Link        string        `xml:"link"`
		Description string        `xml:"description"`
		Items       []rssItemXML  `xml:"item"`
	} `xml:"channel"`
}

type rssItemXML struct {
	Title       string   `xml:"title"`
	Description string   `xml:"description"`
	Link        string   `xml:"link"`
	GUID        string   `xml:"guid"`
	Categories  []string `xml:"category"`
	PubDate     string   `xml:"pubDate"`
}

type rssFeedGuildEntry struct {
	Initialized bool     `json:"initialized"`
	SeenGUIDs   []string `json:"seenGuids"`
}

type rssFeedCacheState struct {
	Guilds map[string]map[string]rssFeedGuildEntry `json:"guilds"`
}

var (
	rssFeedMu        sync.Mutex
	rssFeedCache     rssFeedCacheState
	rssFeedCachePath string
)

func initRSSCache() {
	rssFeedCachePath = dataPath("rssFeedNotifierCache.json")
	rssFeedCache.Guilds = make(map[string]map[string]rssFeedGuildEntry)

	data, err := os.ReadFile(rssFeedCachePath)
	if err == nil {
		if err := json.Unmarshal(data, &rssFeedCache); err != nil {
			log.Printf("RSS notifier: parse cache %s: %v", rssFeedCachePath, err)
			rssFeedCache.Guilds = make(map[string]map[string]rssFeedGuildEntry)
		}
	} else if !os.IsNotExist(err) {
		log.Printf("RSS notifier: read cache %s: %v", rssFeedCachePath, err)
	}
	if rssFeedCache.Guilds == nil {
		rssFeedCache.Guilds = make(map[string]map[string]rssFeedGuildEntry)
	}
}

func saveRSSCache() error {
	if rssFeedCachePath == "" {
		return fmt.Errorf("RSS cache path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(rssFeedCachePath), 0755); err != nil {
		return fmt.Errorf("creating RSS cache directory: %w", err)
	}
	data, err := json.MarshalIndent(rssFeedCache, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding RSS cache: %w", err)
	}
	if err := os.WriteFile(rssFeedCachePath, data, 0644); err != nil {
		return fmt.Errorf("writing RSS cache %s: %w", rssFeedCachePath, err)
	}
	return nil
}

func normalizeFeedURL(rawURL string) string {
	u := strings.TrimSpace(rawURL)
	if strings.HasPrefix(u, "https://docs.discord.com/") && strings.HasSuffix(u, "/rss") {
		return u + ".xml"
	}
	return u
}

func cleanFeedDescription(s string) string {
	s = strings.ReplaceAll(s, "<br>", "\n")
	s = strings.ReplaceAll(s, "<br/>", "\n")
	s = strings.ReplaceAll(s, "<br />", "\n")
	s = strings.ReplaceAll(s, "</p>", "\n\n")

	var b strings.Builder
	inTag := false
	for _, r := range s {
		if r == '<' {
			inTag = true
			continue
		}
		if r == '>' {
			inTag = false
			continue
		}
		if !inTag {
			b.WriteRune(r)
		}
	}
	cleaned := strings.TrimSpace(b.String())
	if len(cleaned) > 2048 {
		cleaned = cleaned[:2045] + "..."
	}
	return cleaned
}

func getFeedColor(sourceURL, feedTitle string) int {
	low := strings.ToLower(sourceURL + " " + feedTitle)
	if strings.Contains(low, "discord") {
		return 0x5865F2
	}
	if strings.Contains(low, "openrouter") {
		return 0x6366F1
	}
	if strings.Contains(low, "github") {
		return 0x24292E
	}
	if strings.Contains(low, "google") || strings.Contains(low, "antigravity") {
		return 0x4285F4
	}
	return 0x5865F2
}

func checkRSSFeedUpdates(s *discordgo.Session, cfg *Config) {
	guilds := loadGuildSettings()

	client := notifierClient(15 * time.Second)

	for guildID, gCfg := range guilds {
		if !gCfg.RSSFeedsEnabled || gCfg.RSSFeedsChannelID == "" || len(gCfg.RSSFeeds) == 0 {
			continue
		}

		for _, rawFeedURL := range gCfg.RSSFeeds {
			feedURL := normalizeFeedURL(rawFeedURL)
			if feedURL == "" {
				continue
			}

			resp, err := client.Get(feedURL)
			if err != nil {
				log.Printf("RSS notifier: fetch feed %s: %v", feedURL, err)
				continue
			}

			body, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil {
				log.Printf("RSS notifier: read feed %s: %v", feedURL, err)
				continue
			}
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				log.Printf("RSS notifier: feed %s returned HTTP %d", feedURL, resp.StatusCode)
				continue
			}

			var feed rssFeedXML
			if err := xml.Unmarshal(body, &feed); err != nil {
				log.Printf("RSS notifier: parse RSS xml for %s: %v", feedURL, err)
				continue
			}

			items := feed.Channel.Items
			if len(items) == 0 {
				continue
			}

			feedTitle := strings.TrimSpace(feed.Channel.Title)
			if feedTitle == "" {
				feedTitle = "RSS Feed"
			}

			rssFeedMu.Lock()
			if rssFeedCache.Guilds[guildID] == nil {
				rssFeedCache.Guilds[guildID] = make(map[string]rssFeedGuildEntry)
			}
			fEntry, exists := rssFeedCache.Guilds[guildID][feedURL]

			if !exists || !fEntry.Initialized {
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
				rssFeedCache.Guilds[guildID][feedURL] = rssFeedGuildEntry{
					Initialized: true,
					SeenGUIDs:   allGuids,
				}
				if err := saveRSSCache(); err != nil {
					log.Printf("RSS notifier: persist initial seed for %s (%s): %v", feedURL, guildID, err)
				}
				rssFeedMu.Unlock()
				continue
			}

			seenMap := make(map[string]bool, len(fEntry.SeenGUIDs))
			for _, id := range fEntry.SeenGUIDs {
				seenMap[id] = true
			}

			var newItems []rssItemXML
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
				rssFeedMu.Unlock()
				continue
			}

			// Reverse so oldest unseen entry is posted first
			for i, j := 0, len(newItems)-1; i < j; i, j = i+1, j-1 {
				newItems[i], newItems[j] = newItems[j], newItems[i]
			}

			header := fmt.Sprintf("📰 **%s** Update", feedTitle)
			if gCfg.RSSFeedsRoleID != nil && *gCfg.RSSFeedsRoleID != "" {
				header = fmt.Sprintf("<@&%s> %s", *gCfg.RSSFeedsRoleID, header)
			}

			rssFeedMu.Unlock()

			color := getFeedColor(feedURL, feedTitle)

			for _, itm := range newItems {
				id := strings.TrimSpace(itm.GUID)
				if id == "" {
					id = strings.TrimSpace(itm.Link)
				}

				desc := cleanFeedDescription(itm.Description)

				embed := &discordgo.MessageEmbed{
					Title:       itm.Title,
					URL:         itm.Link,
					Description: desc,
					Color:       color,
					Footer: &discordgo.MessageEmbedFooter{
						Text: fmt.Sprintf("%s • RSS", feedTitle),
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

				if _, err := sendNotifierComplex(s, gCfg.RSSFeedsChannelID, msgSend); err != nil {
					log.Printf("RSS notifier: send update %s to channel %s: %v", id, gCfg.RSSFeedsChannelID, err)
					continue
				}

				rssFeedMu.Lock()
				curEntry := rssFeedCache.Guilds[guildID][feedURL]
				curEntry.SeenGUIDs = append(curEntry.SeenGUIDs, id)
				rssFeedCache.Guilds[guildID][feedURL] = curEntry
				if err := saveRSSCache(); err != nil {
					log.Printf("RSS notifier: persist seen item %s: %v", id, err)
				}
				rssFeedMu.Unlock()
			}
		}
	}
}

func startRSSNotifierDaemon(ctx context.Context, s *discordgo.Session) {
	initRSSCache()
	cfg, err := loadConfig()
	if err != nil {
		log.Printf("RSS notifier: load config: %v", err)
		return
	}

	interval := defaultRSSPollInterval
	if cfg != nil && cfg.RSSFeedsPollIntervalMs > 0 {
		interval = time.Duration(cfg.RSSFeedsPollIntervalMs) * time.Millisecond
	}

	log.Printf("RSS feeds notifier daemon started (interval %v)", interval)
	runPeriodic(ctx, 15*time.Second, interval, func() { checkRSSFeedUpdates(s, cfg) })
}
