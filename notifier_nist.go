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

const (
	defaultNistPollInterval = 15 * time.Minute
	nistBaseURL             = "https://services.nvd.nist.gov/rest/json/cves/2.0"
)

type nistState struct {
	Guilds map[string]*nistGuildState `json:"guilds"`
}

type nistGuildState struct {
	Initialized bool     `json:"initialized"`
	SeenCVEs    []string `json:"seenCves"`
	LastPoll    string   `json:"lastPoll"`
}

var (
	nistMu        sync.Mutex
	nistCache     nistState
	nistCachePath string
)

func initNistCache() {
	nistCachePath = dataPath("nistNotifierCache.json")
	nistCache.Guilds = make(map[string]*nistGuildState)

	data, err := os.ReadFile(nistCachePath)
	if err == nil {
		if err := json.Unmarshal(data, &nistCache); err != nil {
			log.Printf("NIST notifier: parse cache %s: %v", nistCachePath, err)
			nistCache.Guilds = make(map[string]*nistGuildState)
		}
	} else if !os.IsNotExist(err) {
		log.Printf("NIST notifier: read cache %s: %v", nistCachePath, err)
	}
	if nistCache.Guilds == nil {
		nistCache.Guilds = make(map[string]*nistGuildState)
	}
}

func saveNistCache() error {
	if nistCachePath == "" {
		return fmt.Errorf("NIST cache path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(nistCachePath), 0755); err != nil {
		return fmt.Errorf("creating NIST cache directory: %w", err)
	}
	data, err := json.MarshalIndent(nistCache, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding NIST cache: %w", err)
	}
	if err := os.WriteFile(nistCachePath, data, 0644); err != nil {
		return fmt.Errorf("writing NIST cache %s: %w", nistCachePath, err)
	}
	return nil
}

// NIST API v2.0 JSON models
type nistAPIResponse struct {
	TotalResults    int `json:"totalResults"`
	Vulnerabilities []struct {
		CVE nistCVEItem `json:"cve"`
	} `json:"vulnerabilities"`
}

type nistCVEItem struct {
	ID               string            `json:"id"`
	SourceIdentifier string            `json:"sourceIdentifier"`
	Published        string            `json:"published"`
	LastModified     string            `json:"lastModified"`
	VulnStatus       string            `json:"vulnStatus"`
	Descriptions     []nistDescription `json:"descriptions"`
	Metrics          nistMetrics       `json:"metrics"`
	References       []nistReference   `json:"references"`
}

type nistDescription struct {
	Lang  string `json:"lang"`
	Value string `json:"value"`
}

type nistMetrics struct {
	CVSSMetricV31 []nistCVSSMetricV3 `json:"cvssMetricV31"`
	CVSSMetricV30 []nistCVSSMetricV3 `json:"cvssMetricV30"`
	CVSSMetricV2  []nistCVSSMetricV2 `json:"cvssMetricV2"`
}

type nistCVSSMetricV3 struct {
	Source   string       `json:"source"`
	Type     string       `json:"type"`
	CVSSData nistCVSSData `json:"cvssData"`
}

type nistCVSSMetricV2 struct {
	Source   string       `json:"source"`
	Type     string       `json:"type"`
	CVSSData nistCVSSData `json:"cvssData"`
}

type nistCVSSData struct {
	Version      string  `json:"version"`
	VectorString string  `json:"vectorString"`
	BaseScore    float64 `json:"baseScore"`
	BaseSeverity string  `json:"baseSeverity"`
}

type nistReference struct {
	URL string `json:"url"`
}

func fetchNistCVEs(client *http.Client, apiKey string, pubStart, pubEnd time.Time, limit int) ([]nistCVEItem, error) {
	params := url.Values{}
	params.Set("pubStartDate", pubStart.UTC().Format("2006-01-02T15:04:05.000"))
	params.Set("pubEndDate", pubEnd.UTC().Format("2006-01-02T15:04:05.000"))
	if limit > 0 {
		params.Set("resultsPerPage", fmt.Sprintf("%d", limit))
	} else {
		params.Set("resultsPerPage", "20")
	}

	reqURL := fmt.Sprintf("%s?%s", nistBaseURL, params.Encode())
	req, err := http.NewRequest("GET", reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("creating NIST request: %w", err)
	}
	req.Header.Set("User-Agent", "Sunfire-Discord-Bot/1.0")
	if apiKey != "" {
		req.Header.Set("apiKey", apiKey)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("executing NIST request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("NIST API returned HTTP %d: %s", resp.StatusCode, string(body))
	}

	var parsed nistAPIResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("decoding NIST response: %w", err)
	}

	var items []nistCVEItem
	for _, v := range parsed.Vulnerabilities {
		if v.CVE.ID != "" {
			items = append(items, v.CVE)
		}
	}
	return items, nil
}

func buildNistEmbed(cve nistCVEItem) *discordgo.MessageEmbed {
	score := 0.0
	severity := "UNKNOWN"
	if len(cve.Metrics.CVSSMetricV31) > 0 {
		score = cve.Metrics.CVSSMetricV31[0].CVSSData.BaseScore
		severity = cve.Metrics.CVSSMetricV31[0].CVSSData.BaseSeverity
	} else if len(cve.Metrics.CVSSMetricV30) > 0 {
		score = cve.Metrics.CVSSMetricV30[0].CVSSData.BaseScore
		severity = cve.Metrics.CVSSMetricV30[0].CVSSData.BaseSeverity
	} else if len(cve.Metrics.CVSSMetricV2) > 0 {
		score = cve.Metrics.CVSSMetricV2[0].CVSSData.BaseScore
		severity = cve.Metrics.CVSSMetricV2[0].CVSSData.BaseSeverity
	}

	if severity == "" || severity == "UNKNOWN" {
		if score >= 9.0 {
			severity = "CRITICAL"
		} else if score >= 7.0 {
			severity = "HIGH"
		} else if score >= 4.0 {
			severity = "MEDIUM"
		} else if score > 0.0 {
			severity = "LOW"
		} else {
			severity = "UNKNOWN"
		}
	}

	color := 0x95A5A6 // Gray default
	switch strings.ToUpper(severity) {
	case "CRITICAL":
		color = 0xE74C3C // Red
	case "HIGH":
		color = 0xE67E22 // Orange
	case "MEDIUM":
		color = 0xF1C40F // Yellow
	case "LOW":
		color = 0x3498DB // Blue
	}

	desc := "No English description provided."
	for _, d := range cve.Descriptions {
		if d.Lang == "en" && d.Value != "" {
			desc = d.Value
			break
		}
	}
	if len(desc) > 400 {
		desc = desc[:397] + "..."
	}

	scoreStr := "N/A"
	if score > 0 {
		scoreStr = fmt.Sprintf("%.1f (%s)", score, strings.ToUpper(severity))
	} else {
		scoreStr = strings.ToUpper(severity)
	}

	status := cve.VulnStatus
	if status == "" {
		status = "Published"
	}

	fields := []*discordgo.MessageEmbedField{
		{
			Name:   "📊 CVSS Severity",
			Value:  fmt.Sprintf("**%s**", scoreStr),
			Inline: true,
		},
		{
			Name:   "🔍 Status",
			Value:  status,
			Inline: true,
		},
	}

	if cve.Published != "" {
		fields = append(fields, &discordgo.MessageEmbedField{
			Name:   "📅 Published",
			Value:  cve.Published,
			Inline: true,
		})
	}

	return &discordgo.MessageEmbed{
		Title:       fmt.Sprintf("🛡️ %s", cve.ID),
		Description: desc,
		URL:         fmt.Sprintf("https://nvd.nist.gov/vuln/detail/%s", cve.ID),
		Color:       color,
		Fields:      fields,
		Footer: &discordgo.MessageEmbedFooter{
			Text: "NIST National Vulnerability Database • nvd.nist.gov",
		},
		Timestamp: cve.Published,
	}
}

func checkNistUpdates(s *discordgo.Session, cfg *Config) {
	guilds := loadGuildSettings()
	nistMu.Lock()
	defer nistMu.Unlock()

	client := notifierClient(20 * time.Second)
	now := time.Now().UTC()

	for guildID, gCfg := range guilds {
		if !gCfg.NistEnabled || gCfg.NistChannelID == "" {
			continue
		}

		gState, exists := nistCache.Guilds[guildID]
		if !exists || gState == nil {
			gState = &nistGuildState{
				Initialized: false,
				SeenCVEs:    []string{},
			}
			nistCache.Guilds[guildID] = gState
		}

		seenMap := make(map[string]bool)
		for _, id := range gState.SeenCVEs {
			seenMap[id] = true
		}

		// Calculate lookback window
		lookback := 2 * time.Hour
		if !gState.Initialized {
			// On initial run: look back 24 hours to populate recent CVEs immediately
			lookback = 24 * time.Hour
		} else if gState.LastPoll != "" {
			if t, err := time.Parse(time.RFC3339, gState.LastPoll); err == nil {
				lookback = now.Sub(t) + (10 * time.Minute) // 10-minute overlap buffer
				if lookback > 72*time.Hour {
					lookback = 72 * time.Hour
				}
			}
		}

		pubStart := now.Add(-lookback)
		log.Printf("NIST notifier: querying CVEs for guild %s (lookback %v, initialized=%t)...", guildID, lookback, gState.Initialized)
		cves, err := fetchNistCVEs(client, cfg.NistAPIKey, pubStart, now, 25)
		if err != nil {
			log.Printf("NIST notifier: fetch CVEs: %v", err)
			continue
		}
		log.Printf("NIST notifier: fetched %d CVEs from NIST NVD API", len(cves))

		var newCVEs []nistCVEItem
		for _, cve := range cves {
			if !seenMap[cve.ID] {
				newCVEs = append(newCVEs, cve)
				seenMap[cve.ID] = true
			}
		}

		// On first run, deliver top 3 latest CVEs so channel has content immediately
		maxToDeliver := 5
		if !gState.Initialized {
			maxToDeliver = 3
		}

		deliveredCount := 0
		for _, cve := range newCVEs {
			if deliveredCount >= maxToDeliver {
				// Mark remaining as seen so we don't spam later
				gState.SeenCVEs = append(gState.SeenCVEs, cve.ID)
				continue
			}

			embed := buildNistEmbed(cve)
			var msgSend *discordgo.MessageSend
			if gCfg.NistRoleID != nil && *gCfg.NistRoleID != "" {
				msgSend = &discordgo.MessageSend{
					Content: fmt.Sprintf("<@&%s> 🚨 **NIST Vulnerability Alert**", *gCfg.NistRoleID),
					Embeds:  []*discordgo.MessageEmbed{embed},
				}
			} else {
				msgSend = &discordgo.MessageSend{
					Embeds: []*discordgo.MessageEmbed{embed},
				}
			}

			if _, err := sendNotifierComplex(s, gCfg.NistChannelID, msgSend); err != nil {
				log.Printf("NIST notifier: send CVE %s to guild %s: %v", cve.ID, guildID, err)
				continue
			}

			gState.SeenCVEs = append(gState.SeenCVEs, cve.ID)
			deliveredCount++
			time.Sleep(500 * time.Millisecond) // Polite delay between Discord messages
		}

		// Keep seenCVEs list bounded to last 500
		if len(gState.SeenCVEs) > 500 {
			gState.SeenCVEs = gState.SeenCVEs[len(gState.SeenCVEs)-500:]
		}

		if deliveredCount > 0 {
			log.Printf("NIST notifier: successfully delivered %d CVE alert(s) to channel %s", deliveredCount, gCfg.NistChannelID)
		}

		gState.Initialized = true
		gState.LastPoll = now.Format(time.RFC3339)
		_ = saveNistCache()
	}
}

// startNistNotifierDaemon runs periodic NIST NVD vulnerability polling
func startNistNotifierDaemon(ctx context.Context, s *discordgo.Session) {
	initNistCache()
	cfg, err := loadConfig()
	if err != nil {
		log.Printf("NIST notifier: load config: %v", err)
		return
	}

	interval := defaultNistPollInterval
	if cfg.NistPollIntervalMs > 0 {
		interval = time.Duration(cfg.NistPollIntervalMs) * time.Millisecond
	}

	log.Printf("NIST (NVD) notifier daemon started (interval %v)", interval)
	runPeriodic(ctx, 15*time.Second, interval, func() { checkNistUpdates(s, cfg) })
}
