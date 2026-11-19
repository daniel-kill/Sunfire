package main

import (
	"net/http"
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"
)

func TestBuildNistEmbedSeverityColors(t *testing.T) {
	tests := []struct {
		name      string
		score     float64
		severity  string
		wantColor int
	}{
		{"Critical", 9.8, "CRITICAL", 0xE74C3C},
		{"High", 8.1, "HIGH", 0xE67E22},
		{"Medium", 5.5, "MEDIUM", 0xF1C40F},
		{"Low", 2.1, "LOW", 0x3498DB},
		{"Unknown", 0.0, "UNKNOWN", 0x95A5A6},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cve := nistCVEItem{
				ID:        "CVE-2026-TEST",
				Published: "2026-11-19T12:00:00.000",
				Metrics: nistMetrics{
					CVSSMetricV31: []nistCVSSMetricV3{
						{
							CVSSData: nistCVSSData{
								BaseScore:    tt.score,
								BaseSeverity: tt.severity,
							},
						},
					},
				},
				Descriptions: []nistDescription{
					{Lang: "en", Value: "A test vulnerability description."},
				},
			}

			embed := buildNistEmbed(cve)
			if embed.Color != tt.wantColor {
				t.Errorf("expected color 0x%X, got 0x%X", tt.wantColor, embed.Color)
			}
			if !strings.Contains(embed.Title, "CVE-2026-TEST") {
				t.Errorf("expected title to contain CVE ID, got %s", embed.Title)
			}
			if embed.URL != "https://nvd.nist.gov/vuln/detail/CVE-2026-TEST" {
				t.Errorf("unexpected embed URL: %s", embed.URL)
			}
		})
	}
}

func TestNistNotifierDeliveryMock(t *testing.T) {
	mockJSON := `{
		"resultsPerPage": 1,
		"startIndex": 0,
		"totalResults": 1,
		"format": "NVD_CVE",
		"version": "2.0",
		"vulnerabilities": [
			{
				"cve": {
					"id": "CVE-2026-99999",
					"published": "2026-11-19T12:00:00.000",
					"vulnStatus": "Received",
					"descriptions": [
						{"lang": "en", "value": "A critical test vulnerability in mock-pkg."}
					],
					"metrics": {
						"cvssMetricV31": [
							{
								"cvssData": {
									"baseScore": 9.8,
									"baseSeverity": "CRITICAL"
								}
							}
						]
					}
				}
			}
		]
	}`

	var deliveredEmbeds []*discordgo.MessageEmbed
	var deliveredChannel string

	// Temporarily override test seams
	oldClient := notifierHTTPClient
	oldComplex := sendNotifierComplex
	defer func() {
		notifierHTTPClient = oldClient
		sendNotifierComplex = oldComplex
	}()

	notifierHTTPClient = &http.Client{
		Transport: notifierRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			return notifierResponse(req, http.StatusOK, mockJSON), nil
		}),
	}

	sendNotifierComplex = func(_ *discordgo.Session, channelID string, message *discordgo.MessageSend) (*discordgo.Message, error) {
		deliveredChannel = channelID
		deliveredEmbeds = append(deliveredEmbeds, message.Embeds...)
		return &discordgo.Message{ID: "msg-123"}, nil
	}

	guildSettingsMu.Lock()
	oldGuilds := guildSettings
	guildSettings = map[string]*GuildSettings{
		SunfireServerID: {
			NistEnabled:   true,
			NistChannelID: "ch-nist-test",
		},
	}
	guildSettingsMu.Unlock()
	defer func() {
		guildSettingsMu.Lock()
		guildSettings = oldGuilds
		guildSettingsMu.Unlock()
	}()

	nistMu.Lock()
	nistCache.Guilds = map[string]*nistGuildState{
		SunfireServerID: {
			Initialized: false,
			SeenCVEs:    []string{},
		},
	}
	nistMu.Unlock()

	checkNistUpdates(&discordgo.Session{}, &Config{NistAPIKey: "test-key"})

	if deliveredChannel != "ch-nist-test" {
		t.Errorf("expected delivery to channel 'ch-nist-test', got '%s'", deliveredChannel)
	}
	if len(deliveredEmbeds) != 1 {
		t.Fatalf("expected 1 delivered embed, got %d", len(deliveredEmbeds))
	}
	if !strings.Contains(deliveredEmbeds[0].Title, "CVE-2026-99999") {
		t.Errorf("expected embed for CVE-2026-99999, got title: %s", deliveredEmbeds[0].Title)
	}
}
