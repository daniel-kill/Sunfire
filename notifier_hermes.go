// notifier_hermes.go — Hermes tips fetching, storage, deduplication, and scheduled posting.
// Fetches tips from docs/llms.txt and GitHub releases every 24 hours, updates Data/hermes_tips.json,
// and posts any newly discovered tips to the Hermes Tips channel (1555337100273123408)
// rate-limited to 3 tips every 3 seconds to avoid overflowing the Discord API.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

// HermesTipsChannelID is the fallback or test-overridden Discord channel for Hermes tips.
var HermesTipsChannelID string

// getHermesTipsChannelID returns the configured Discord channel for Hermes tips from config, or HermesTipsChannelID.
func getHermesTipsChannelID() string {
	cfg, err := loadConfig()
	if err == nil && cfg != nil && strings.TrimSpace(cfg.HermesTipsChannelID) != "" {
		return strings.TrimSpace(cfg.HermesTipsChannelID)
	}
	return strings.TrimSpace(HermesTipsChannelID)
}

// HermesTip represents the structure of each tip in hermes_tips.json.
type HermesTip struct {
	Source string `json:"source"`
	Text   string `json:"tip"`
}

// HermesTipState stores the known tips and history of posted hashes to guarantee no duplicates.
type HermesTipState struct {
	Tips       []HermesTip `json:"tips"`
	PostedKeys []string    `json:"postedKeys"`
}

// HermesGithubRelease represents the GitHub API release payload.
type HermesGithubRelease struct {
	Name string `json:"name"`
	Body string `json:"body"`
}

var (
	hermesMu             sync.Mutex
	hermesState          HermesTipState
	hermesJSONPath       string
	hermesRateLimitDelay = 3 * time.Second
	hermesHTTPTimeout    = 15 * time.Minute
)

func hermesTipsFilePath() string {
	if hermesJSONPath != "" {
		return hermesJSONPath
	}
	return dataPath("hermes_tips.json")
}

func initHermesTipsState() error {
	path := hermesTipsFilePath()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("creating Hermes state directory: %w", err)
	}

	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("reading Hermes state %s: %w", path, err)
	}
	if err == nil {
		var st HermesTipState
		if stateErr := json.Unmarshal(data, &st); stateErr == nil {
			hermesState = st
			return nil
		}
		var tips []HermesTip
		if legacyErr := json.Unmarshal(data, &tips); legacyErr != nil {
			return fmt.Errorf("parsing Hermes state %s: %w", path, legacyErr)
		}
		hermesState = HermesTipState{Tips: tips}
		for _, t := range tips {
			hermesState.PostedKeys = append(hermesState.PostedKeys, hashHermesTip(t))
		}
		return nil
	}

	hermesState = HermesTipState{
		Tips:       []HermesTip{},
		PostedKeys: []string{},
	}
	return nil
}

func saveHermesTipsState() error {
	path := hermesTipsFilePath()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("creating Hermes state directory: %w", err)
	}
	data, err := json.MarshalIndent(hermesState, "", "    ")
	if err != nil {
		return fmt.Errorf("encoding Hermes state: %w", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("writing Hermes state %s: %w", path, err)
	}
	return nil
}

func hashHermesTip(t HermesTip) string {
	norm := strings.TrimSpace(t.Source) + "|" + strings.TrimSpace(t.Text)
	h := sha256.Sum256([]byte(norm))
	return hex.EncodeToString(h[:])
}

func fetchHermesDocsTips() []HermesTip {
	var tips []HermesTip
	client := notifierClient(hermesHTTPTimeout)

	resp, err := client.Get("https://hermes-agent.nousresearch.com/docs/llms.txt")
	if err != nil {
		log.Printf("[Hermes] Error fetching docs: %v\n", err)
		return tips
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		if err != nil {
			log.Printf("[Hermes] Error reading docs response: %v", err)
			return tips
		}
		lines := strings.Split(string(bodyBytes), "\n")
		for _, line := range lines {
			// Grab markdown bullet points that look like "- [Topic](link): Description"
			if strings.HasPrefix(line, "- [") && strings.Contains(line, ":") {
				tips = append(tips, HermesTip{
					Source: "Hermes Docs",
					Text:   strings.TrimSpace(line),
				})
			}
		}
	} else {
		log.Printf("[Hermes] Docs returned HTTP %d", resp.StatusCode)
	}
	return tips
}

func fetchHermesGithubTips() []HermesTip {
	var tips []HermesTip
	req, err := http.NewRequest("GET", "https://api.github.com/repos/NousResearch/hermes-agent/releases", nil)
	if err != nil {
		log.Printf("[Hermes] Error creating GitHub request: %v\n", err)
		return tips
	}
	req.Header.Set("User-Agent", "Sunfire-Hermes-Tips-Scraper/1.0")

	client := notifierClient(hermesHTTPTimeout)
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("[Hermes] Error fetching GitHub releases: %v\n", err)
		return tips
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		var releases []HermesGithubRelease
		if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
			log.Printf("[Hermes] Error decoding GitHub releases JSON: %v\n", err)
			return tips
		}

		reUser := regexp.MustCompile(`(?i) by @[\w-]+`)
		rePR1 := regexp.MustCompile(`(?i) in #\d+`)
		rePR2 := regexp.MustCompile(`(?i)\(#\d+\)`)

		limit := 10
		if len(releases) < limit {
			limit = len(releases)
		}

		for _, release := range releases[:limit] {
			version := release.Name
			if version == "" {
				version = "New Release"
			}

			lines := strings.Split(release.Body, "\n")
			for _, line := range lines {
				line = strings.TrimSpace(line)

				// Find lines that are bullet points
				if strings.HasPrefix(line, "* ") || strings.HasPrefix(line, "- ") {
					cleanLine := strings.TrimSpace(strings.TrimLeft(line, "*- "))
					cleanLine = reUser.ReplaceAllString(cleanLine, "")
					cleanLine = rePR1.ReplaceAllString(cleanLine, "")
					cleanLine = rePR2.ReplaceAllString(cleanLine, "")

					if len(cleanLine) > 20 && !strings.Contains(cleanLine, "Full Changelog") {
						tips = append(tips, HermesTip{
							Source: fmt.Sprintf("GitHub (%s)", version),
							Text:   strings.TrimSpace(cleanLine),
						})
					}
				}
			}
		}
	} else {
		log.Printf("[Hermes] GitHub releases returned HTTP %d", resp.StatusCode)
	}

	if len(tips) > 20 {
		return tips[:20]
	}
	return tips
}

func collectAllHermesTips() []HermesTip {
	var allTips []HermesTip

	// 1. Manual baseline custom workflow tip
	customWorkflowTip := `# everyday ops
hermes config set model.default deepseek/deepseek-v4-flash
hermes config set agent.reasoning_effort low
hermes gateway restart
- close the terminal

# heavy session
hermes config set model.default deepseek/deepseek-v4-pro
hermes config set agent.reasoning_effort medium
hermes gateway restart
- close the terminal`

	allTips = append(allTips, HermesTip{
		Source: "My Workflows",
		Text:   customWorkflowTip,
	})

	// 2. Dynamic tips from Docs and GitHub releases
	allTips = append(allTips, fetchHermesDocsTips()...)
	allTips = append(allTips, fetchHermesGithubTips()...)
	return allTips
}

// checkHermesTipsUpdates fetches the latest tips, updates the JSON file, and posts newly
// discovered tips in batches of 3 every 3 seconds to avoid Discord API rate-limiting.
func checkHermesTipsUpdates(s *discordgo.Session) (newTipsCount int, err error) {
	hermesMu.Lock()
	defer hermesMu.Unlock()

	if err := initHermesTipsState(); err != nil {
		return 0, err
	}

	fetched := collectAllHermesTips()
	if len(fetched) == 0 {
		return 0, nil
	}

	postedSet := make(map[string]bool)
	for _, k := range hermesState.PostedKeys {
		postedSet[k] = true
	}

	var newTips []HermesTip
	for _, tip := range fetched {
		key := hashHermesTip(tip)
		if !postedSet[key] {
			newTips = append(newTips, tip)
		}
	}

	// Update stored tips with all current tips (merging unique items)
	existingMap := make(map[string]bool)
	for _, t := range hermesState.Tips {
		existingMap[hashHermesTip(t)] = true
	}
	for _, t := range fetched {
		k := hashHermesTip(t)
		if !existingMap[k] {
			hermesState.Tips = append(hermesState.Tips, t)
			existingMap[k] = true
		}
	}
	if err := saveHermesTipsState(); err != nil {
		return 0, err
	}

	if len(newTips) == 0 {
		return 0, nil
	}

	channelID := getHermesTipsChannelID()
	if s != nil && channelID == "" {
		log.Printf("[Hermes] Discovered %d new tips, but hermesTipsChannelId is not configured; gracefully pausing Discord posting", len(newTips))
		return 0, nil
	}
	if channelID != "" {
		log.Printf("[Hermes] Discovered %d new tips to post to channel %s", len(newTips), channelID)
	}

	// Post newly found tips: 3 tips every 3 seconds
	batchSize := 3
	for i := 0; i < len(newTips); i += batchSize {
		end := i + batchSize
		if end > len(newTips) {
			end = len(newTips)
		}

		for j := i; j < end; j++ {
			tip := newTips[j]
			key := hashHermesTip(tip)

			var content string
			if strings.Contains(tip.Text, "\n") {
				content = fmt.Sprintf("💁‍♀️ **Hermes Tip** • *%s*\n```bash\n%s\n```", tip.Source, tip.Text)
			} else {
				content = fmt.Sprintf("💁‍♀️ **Hermes Tip** • *%s*\n> %s", tip.Source, tip.Text)
			}

			if s != nil {
				_, sendErr := sendNotifierMessage(s, channelID, content)
				if sendErr != nil {
					log.Printf("[Hermes] Failed to post tip to Discord: %v", sendErr)
					return newTipsCount, nil
				} else {
					hermesState.PostedKeys = append(hermesState.PostedKeys, key)
					postedSet[key] = true
					if err := saveHermesTipsState(); err != nil {
						hermesState.PostedKeys = hermesState.PostedKeys[:len(hermesState.PostedKeys)-1]
						delete(postedSet, key)
						return newTipsCount, err
					}
					newTipsCount++
				}
			} else {
				hermesState.PostedKeys = append(hermesState.PostedKeys, key)
				postedSet[key] = true
				newTipsCount++
			}
		}

		if s == nil {
			if err := saveHermesTipsState(); err != nil {
				return newTipsCount, err
			}
		}

		// If there are more batches to send, pause 3 seconds before sending next batch
		if end < len(newTips) {
			time.Sleep(hermesRateLimitDelay)
		}
	}

	return newTipsCount, nil
}

// startHermesTipsDaemon runs every 24 hours to fetch and post new tips.
func startHermesTipsDaemon(ctx context.Context, s *discordgo.Session) {
	log.Println("[Hermes] Starting 24h tips daemon...")
	runPeriodic(ctx, 10*time.Second, 24*time.Hour, func() {
		if _, err := checkHermesTipsUpdates(s); err != nil {
			log.Printf("[Hermes] poll pass error: %v", err)
		}
	})
}
