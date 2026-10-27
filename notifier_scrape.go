package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
)

type scrapeState struct {
	Posted       map[string][]string `json:"posted"`
	Attempted    map[string][]string `json:"attempted"`
	LastDailyRun string              `json:"lastDailyRun"`
}

var (
	scrapeMu        sync.Mutex
	scrapeCache     scrapeState
	scrapeCachePath string
	imgRegex        = regexp.MustCompile(`https?://[^\s"'<>]+\.(?:jpg|jpeg|png|webp)`)
)

func initScrapeCache() {
	scrapeCachePath = dataPath("scrapeImagesCache.json")
	scrapeCache = scrapeState{
		Posted:    make(map[string][]string),
		Attempted: make(map[string][]string),
	}

	data, err := os.ReadFile(scrapeCachePath)
	if err == nil {
		if err := json.Unmarshal(data, &scrapeCache); err != nil {
			log.Printf("Image scrape: parse cache %s: %v", scrapeCachePath, err)
			scrapeCache = scrapeState{
				Posted:    make(map[string][]string),
				Attempted: make(map[string][]string),
			}
		}
	} else if !os.IsNotExist(err) {
		log.Printf("Image scrape: read cache %s: %v", scrapeCachePath, err)
	}
	if scrapeCache.Posted == nil {
		scrapeCache.Posted = make(map[string][]string)
	}
	if scrapeCache.Attempted == nil {
		scrapeCache.Attempted = make(map[string][]string)
	}
}

func saveScrapeCache() error {
	if scrapeCachePath == "" {
		return fmt.Errorf("scrape cache path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(scrapeCachePath), 0755); err != nil {
		return fmt.Errorf("creating scrape cache directory: %w", err)
	}
	data, err := json.MarshalIndent(scrapeCache, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding scrape cache: %w", err)
	}
	if err := os.WriteFile(scrapeCachePath, data, 0644); err != nil {
		return fmt.Errorf("writing scrape cache %s: %w", scrapeCachePath, err)
	}
	return nil
}

func runDailyImageScrape(s *discordgo.Session, cfg *Config) {
	if len(cfg.Scrape.URLs) == 0 || len(cfg.Scrape.ChannelIDs) == 0 {
		return
	}

	scrapeMu.Lock()
	defer scrapeMu.Unlock()

	client := notifierClient(20 * time.Second)

	for _, targetURL := range cfg.Scrape.URLs {
		req, err := http.NewRequest("GET", targetURL, nil)
		if err != nil {
			log.Printf("Image scrape: create request for %s: %v", targetURL, err)
			continue
		}
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")

		resp, err := client.Do(req)
		if err != nil {
			log.Printf("Image scrape: fetch %s: %v", targetURL, err)
			continue
		}
		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			log.Printf("Image scrape: read %s: %v", targetURL, readErr)
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			log.Printf("Image scrape: %s returned HTTP %d", targetURL, resp.StatusCode)
			continue
		}

		matches := imgRegex.FindAllString(string(body), -1)
		for _, chID := range cfg.Scrape.ChannelIDs {
			cacheKey := targetURL + "\x00" + chID
			postedMap := make(map[string]bool)
			for _, u := range scrapeCache.Posted[targetURL] {
				postedMap[u] = true
			}
			for _, u := range scrapeCache.Posted[cacheKey] {
				postedMap[u] = true
			}

			var freshImage string
			for _, imgURL := range matches {
				lowerURL := strings.ToLower(imgURL)
				if !postedMap[imgURL] && !strings.Contains(lowerURL, "logo") && !strings.Contains(lowerURL, "icon") {
					freshImage = imgURL
					break
				}
			}
			if freshImage == "" {
				continue
			}

			embed := &discordgo.MessageEmbed{
				Title: "📸 Daily Scraped Gallery Image",
				Image: &discordgo.MessageEmbedImage{
					URL: freshImage,
				},
				Color:     0xF47FFF,
				Timestamp: time.Now().Format(time.RFC3339),
			}
			if _, err := sendNotifierEmbed(s, chID, embed); err != nil {
				log.Printf("Image scrape: send image from %s to channel %s: %v", targetURL, chID, err)
				continue
			}

			previousPosted := append([]string(nil), scrapeCache.Posted[cacheKey]...)
			previousLastRun := scrapeCache.LastDailyRun
			scrapeCache.Posted[cacheKey] = append(scrapeCache.Posted[cacheKey], freshImage)
			scrapeCache.LastDailyRun = time.Now().Format(time.RFC3339)
			if err := saveScrapeCache(); err != nil {
				scrapeCache.Posted[cacheKey] = previousPosted
				scrapeCache.LastDailyRun = previousLastRun
				log.Printf("Image scrape: persist delivered image from %s to channel %s: %v", targetURL, chID, err)
			}
		}
	}
}

// startScrapeImagesDaemon runs daily image scrape from configured galleries
func startScrapeImagesDaemon(ctx context.Context, s *discordgo.Session) {
	initScrapeCache()
	cfg, err := loadConfig()
	if err != nil {
		log.Printf("Image scrape: load config: %v", err)
		return
	}
	if len(cfg.Scrape.URLs) == 0 || len(cfg.Scrape.ChannelIDs) == 0 {
		log.Printf("Image scrape not started: no scrape URLs or target channels configured")
		return
	}

	log.Printf("Scrape images daemon started (interval 24h)")
	runPeriodic(ctx, 90*time.Second, 24*time.Hour, func() { runDailyImageScrape(s, cfg) })
}
