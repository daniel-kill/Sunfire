package main

import (
	"context"
	"encoding/json"
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

type freeStuffState struct {
	Guilds map[string]struct {
		InitializedSources map[string]bool `json:"initializedSources"`
		SeenItems          struct {
			GGDeals     []string `json:"ggDeals"`
			ProductHunt []string `json:"productHunt"`
		} `json:"seenItems"`
	} `json:"guilds"`
}

type ggDealsResponse struct {
	Title string `json:"title"`
	URL   string `json:"url"`
}

var (
	freeStuffMu        sync.Mutex
	freeStuffCache     freeStuffState
	freeStuffCachePath string
)

func initFreeStuffCache() {
	freeStuffCachePath = dataPath("freeStuffNotifierCache.json")
	freeStuffCache.Guilds = make(map[string]struct {
		InitializedSources map[string]bool `json:"initializedSources"`
		SeenItems          struct {
			GGDeals     []string `json:"ggDeals"`
			ProductHunt []string `json:"productHunt"`
		} `json:"seenItems"`
	})

	data, err := os.ReadFile(freeStuffCachePath)
	if err == nil {
		if err := json.Unmarshal(data, &freeStuffCache); err != nil {
			log.Printf("FreeStuff notifier: parse cache %s: %v", freeStuffCachePath, err)
			freeStuffCache.Guilds = make(map[string]struct {
				InitializedSources map[string]bool `json:"initializedSources"`
				SeenItems          struct {
					GGDeals     []string `json:"ggDeals"`
					ProductHunt []string `json:"productHunt"`
				} `json:"seenItems"`
			})
		}
	} else if !os.IsNotExist(err) {
		log.Printf("FreeStuff notifier: read cache %s: %v", freeStuffCachePath, err)
	}
	if freeStuffCache.Guilds == nil {
		freeStuffCache.Guilds = make(map[string]struct {
			InitializedSources map[string]bool `json:"initializedSources"`
			SeenItems          struct {
				GGDeals     []string `json:"ggDeals"`
				ProductHunt []string `json:"productHunt"`
			} `json:"seenItems"`
		})
	}
}

func saveFreeStuffCache() error {
	if freeStuffCachePath == "" {
		return fmt.Errorf("FreeStuff cache path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(freeStuffCachePath), 0755); err != nil {
		return fmt.Errorf("creating FreeStuff cache directory: %w", err)
	}
	data, err := json.MarshalIndent(freeStuffCache, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding FreeStuff cache: %w", err)
	}
	if err := os.WriteFile(freeStuffCachePath, data, 0644); err != nil {
		return fmt.Errorf("writing FreeStuff cache %s: %w", freeStuffCachePath, err)
	}
	return nil
}

func checkFreeStuffUpdates(s *discordgo.Session, cfg *Config) {
	guilds := loadGuildSettings()
	freeStuffMu.Lock()
	defer freeStuffMu.Unlock()

	client := notifierClient(15 * time.Second)
	feedURL := "https://gg.deals/us/news/feed/"

	resp, err := client.Get(feedURL)
	if err != nil {
		log.Printf("FreeStuff notifier: fetch GG.deals feed: %v", err)
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("FreeStuff notifier: read GG.deals feed: %v", err)
		return
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Printf("FreeStuff notifier: GG.deals feed returned HTTP %d", resp.StatusCode)
		return
	}
	xmlContent := string(body)

	// Simple RSS item parser for <item><title>...</title><link>...</link></item>
	var items []struct {
		Title string
		Link  string
	}

	rawItems := strings.Split(xmlContent, "<item>")
	for _, raw := range rawItems[1:] {
		titleStart := strings.Index(raw, "<title>")
		titleEnd := strings.Index(raw, "</title>")
		linkStart := strings.Index(raw, "<link>")
		linkEnd := strings.Index(raw, "</link>")

		if titleStart != -1 && titleEnd != -1 && linkStart != -1 && linkEnd != -1 {
			title := raw[titleStart+7 : titleEnd]
			title = strings.TrimPrefix(title, "<![CDATA[")
			title = strings.TrimSuffix(title, "]]>")
			link := raw[linkStart+6 : linkEnd]
			link = strings.TrimPrefix(link, "<![CDATA[")
			link = strings.TrimSuffix(link, "]]>")

			// Only process freebies / deals
			if strings.Contains(strings.ToLower(title), "free") || strings.Contains(strings.ToLower(link), "freebie") {
				items = append(items, struct {
					Title string
					Link  string
				}{Title: strings.TrimSpace(title), Link: strings.TrimSpace(link)})
			}
		}
	}

	for guildID, gCfg := range guilds {
		if !gCfg.FreeStuffEnabled || gCfg.FreeStuffChannelID == "" {
			continue
		}

		gState, exists := freeStuffCache.Guilds[guildID]
		if !exists {
			gState.InitializedSources = map[string]bool{"ggDeals": true}
			gState.SeenItems.GGDeals = []string{}
			gState.SeenItems.ProductHunt = []string{}
		}

		seenMap := make(map[string]bool)
		for _, link := range gState.SeenItems.GGDeals {
			seenMap[link] = true
		}

		type freePost struct {
			link    string
			content string
		}
		var newPosts []freePost
		for _, item := range items {
			if !seenMap[item.Link] {
				newPosts = append(newPosts, freePost{
					link:    item.Link,
					content: fmt.Sprintf("🎁 **Freebie Deal Alert!**\n**%s**\n%s", item.Title, item.Link),
				})
				seenMap[item.Link] = true
				if len(newPosts) >= 3 {
					break
				}
			}
		}

		if len(newPosts) > 0 {
			header := ""
			if gCfg.FreeStuffRoleID != nil && *gCfg.FreeStuffRoleID != "" {
				header = fmt.Sprintf("<@&%s> ", *gCfg.FreeStuffRoleID)
			}
			for _, post := range newPosts {
				if _, err := sendNotifierMessage(s, gCfg.FreeStuffChannelID, header+post.content); err != nil {
					log.Printf("FreeStuff notifier: send deal %s to guild %s: %v", post.link, guildID, err)
					continue
				}
				previousState := gState
				previousState.SeenItems.GGDeals = append([]string(nil), gState.SeenItems.GGDeals...)
				gState.SeenItems.GGDeals = append(gState.SeenItems.GGDeals, post.link)
				freeStuffCache.Guilds[guildID] = gState
				if err := saveFreeStuffCache(); err != nil {
					gState = previousState
					if !exists {
						delete(freeStuffCache.Guilds, guildID)
					} else {
						freeStuffCache.Guilds[guildID] = previousState
					}
					log.Printf("FreeStuff notifier: persist delivered deal %s for guild %s: %v", post.link, guildID, err)
				} else {
					exists = true
				}
			}
		}
	}
}

// startFreeStuffNotifierDaemon runs periodic free game / software checks
func startFreeStuffNotifierDaemon(ctx context.Context, s *discordgo.Session) {
	initFreeStuffCache()
	cfg, err := loadConfig()
	if err != nil {
		log.Printf("FreeStuff notifier: load config: %v", err)
		return
	}

	log.Printf("FreeStuff notifier daemon started (interval 30m)")
	runPeriodic(ctx, time.Minute, 30*time.Minute, func() { checkFreeStuffUpdates(s, cfg) })
}
