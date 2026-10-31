// config.go — loads secrets from the gitignored .private/config.json.
//
// Priority: .private/config.json ("botToken") → BOT_TOKEN env var (fallback).
// The env var remains a fallback so the bot also runs in environments that
// prefer environment-injected secrets (e.g. CI, systemd).
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Config is the shape of .private/config.json.
type Config struct {
	Project             string            `json:"project"`
	Notes               string            `json:"notes"`
	BotToken            string            `json:"botToken"`
	ApplicationID       string            `json:"applicationId"`
	PublicKey           string            `json:"publicKey"`
	BotInviteURL        string            `json:"botInviteUrl"`
	LoggingChannel      string            `json:"loggingChannelId"`
	OwnerID             string            `json:"ownerId"`
	AdminIDs            []string          `json:"adminIds,omitempty"`
	InfoCategoryID      string            `json:"infoCategoryId,omitempty"`
	RulesChannelID      string            `json:"rulesChannelId,omitempty"`
	VoiceChannelID      string            `json:"voiceChannelId,omitempty"`
	HermesTipsChannelID string            `json:"hermesTipsChannelId,omitempty"`
	HealthCheck         HealthCheckConfig `json:"healthCheck"`
	AnyAPI              []AnyAPIProvider  `json:"anyApi"`
	VeniceAPIKey        string            `json:"veniceApiKey"`
	VeniceBaseURL       string            `json:"veniceBaseUrl"`
	DefaultChatModel    string            `json:"defaultChatModel"`
	DefaultImageModel   string            `json:"defaultImageModel"`
	DefaultTTSModel     string            `json:"defaultTtsModel"`
	DefaultTTSVoice     string            `json:"defaultTtsVoice"`
	DefaultMusicModel   string            `json:"defaultMusicModel"`
	A2EAPIKey           string            `json:"A2E_API_KEY"`
	A2EBaseURL          string            `json:"a2eBaseUrl"`
	DefaultVideoModel   string            `json:"defaultVideoModel"`

	Cohere     ProviderConfig `json:"cohere"`
	Cloudflare ProviderConfig `json:"cloudflare"`
	Groq       ProviderConfig `json:"groq"`
	Mistral    ProviderConfig `json:"mistral"`
	Nvidia     ProviderConfig `json:"nvidia"`
	OpenRouter ProviderConfig `json:"openrouter"`

	// ElyAdmin Merged Integrations & Secrets
	YouTubeAPIKey                 string        `json:"YOUTUBE_API_KEY"`
	YouTubePollIntervalMs         int           `json:"YOUTUBE_POLL_INTERVAL"`
	KickClientID                  string        `json:"KICK_CLIENT_ID"`
	KickClientSecret              string        `json:"KICK_CLIENT_SECRET"`
	KickPollIntervalMs            int           `json:"KICK_POLL_INTERVAL"`
	TMDBAPIKey                    string        `json:"TMDB_API_KEY"`
	SpotifyClientID               string        `json:"SPOTIFY_CLIENT_ID"`
	SpotifyClientSecret           string        `json:"SPOTIFY_CLIENT_SECRET"`
	ProductHuntAPIKey             string        `json:"PRODUCTHUNT_API_KEY"`
	ProductHuntAPISecret          string        `json:"PRODUCTHUNT_API_SECRET"`
	GGDealsAPIKey                 string        `json:"GGDEALS_API_KEY"`
	PexelsAPIKey                  string        `json:"PEXELS_API_KEY"`
	GeminiAPIKey                  string        `json:"GEMINI_API_KEY"`
	AudiusAPIKey                  string        `json:"AUDIUS_API_KEY"`
	AudiusAPIBearerToken          string        `json:"AUDIUS_API_BEARER_TOKEN"`
	TarotAPIBaseURL               string        `json:"TAROT_API_BASE_URL"`
	PostgresConnectionString      string        `json:"POSTGRES_CONNECTION_STRING"`
	MessageLoggingEnabled         bool          `json:"MESSAGE_LOGGING_ENABLED"`
	RSSFeedsPollIntervalMs        int           `json:"RSS_FEEDS_POLL_INTERVAL"`
	Scrape                        ScrapeConfig  `json:"scrape"`
	Guild                         GuildSettings `json:"guild"`
}

type ScrapeConfig struct {
	URLs       []string `json:"urls"`
	ChannelIDs []string `json:"channelIds"`
}

// UnmarshalJSON allows ChannelIDs to be parsed from either strings or JSON numbers (Discord snowflake IDs).
func (sc *ScrapeConfig) UnmarshalJSON(data []byte) error {
	var raw struct {
		URLs       []string      `json:"urls"`
		ChannelIDs []interface{} `json:"channelIds"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&raw); err != nil {
		return err
	}
	sc.URLs = raw.URLs
	sc.ChannelIDs = make([]string, 0, len(raw.ChannelIDs))
	for _, item := range raw.ChannelIDs {
		switch v := item.(type) {
		case string:
			if s := strings.TrimSpace(v); s != "" {
				sc.ChannelIDs = append(sc.ChannelIDs, s)
			}
		case json.Number:
			sc.ChannelIDs = append(sc.ChannelIDs, v.String())
		case float64:
			sc.ChannelIDs = append(sc.ChannelIDs, fmt.Sprintf("%.0f", v))
		}
	}
	return nil
}

type HealthCheckConfig struct {
	Enabled   bool     `json:"enabled"`
	Interval  int      `json:"intervalMinutes"`
	Targets   []string `json:"targets"`
	ChannelID string   `json:"channelId,omitempty"`
}

// UnmarshalJSON allows ChannelID to be parsed from either a string or JSON number (Discord snowflake ID).
func (hc *HealthCheckConfig) UnmarshalJSON(data []byte) error {
	type Alias HealthCheckConfig
	var raw struct {
		Alias
		RawChannelID interface{} `json:"channelId"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&raw); err != nil {
		return err
	}
	*hc = HealthCheckConfig(raw.Alias)
	switch v := raw.RawChannelID.(type) {
	case string:
		hc.ChannelID = strings.TrimSpace(v)
	case json.Number:
		hc.ChannelID = v.String()
	case float64:
		hc.ChannelID = fmt.Sprintf("%.0f", v)
	}
	return nil
}

// TargetChannelID returns the configured health check channel ID, or falls back to the default logging channel.
func (hc HealthCheckConfig) TargetChannelID() string {
	if cid := strings.TrimSpace(hc.ChannelID); cid != "" {
		return cid
	}
	return channelID()
}

type AnyAPIProvider struct {
	Name    string `json:"name"`
	URL     string `json:"url"`
	Auth    string `json:"auth"`
	Timeout int    `json:"timeout"`
}

type ProviderConfig struct {
	APIKey           string `json:"apiKey"`
	APIManagementKey string `json:"apiManagementKey,omitempty"`
	AccountID        string `json:"accountId"`
}

// configPath honors an explicit deployment path, then the application root,
// with a source-tree fallback for local development.
func configPath() string {
	if path := os.Getenv("SUNFIRE_CONFIG_FILE"); path != "" {
		return path
	}
	legacyPath := filepath.Join(applicationDir(), ".private", "config.json")
	if _, err := os.Stat(legacyPath); err == nil {
		return legacyPath
	}
	if _, explicitlyConfigured := os.LookupEnv("SUNFIRE_HOME"); !explicitlyConfigured {
		if _, file, _, ok := runtime.Caller(0); ok {
			return filepath.Join(filepath.Dir(file), ".private", "config.json")
		}
	}
	return filepath.Join(applicationDir(), ".private", "config.json")
}

// loadConfig reads .private/config.json. Returns an empty Config if the file
// is missing (callers then fall back to env).
func loadConfig() (*Config, error) {
	return loadConfigFromPath(configPath())
}

// loadConfigFromPath reads a config file from an explicit path. A missing file
// yields an empty Config (not an error).
func loadConfigFromPath(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Config{}, nil
		}
		return nil, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err == nil {
		if cfg.InfoCategoryID == "" {
			if v, ok := raw["infoCategoryId"].(string); ok && v != "" {
				cfg.InfoCategoryID = v
			} else if v, ok := raw["infoChannelId"].(string); ok && v != "" {
				cfg.InfoCategoryID = v
			}
		}
		if cfg.RulesChannelID == "" {
			if v, ok := raw["rulesChannelId"].(string); ok && v != "" {
				cfg.RulesChannelID = v
			} else if v, ok := raw["rulesChannel"].(string); ok && v != "" {
				cfg.RulesChannelID = v
			}
		}
	}
	return &cfg, nil
}

// saveConfig writes a Config back to path as pretty JSON.
func saveConfig(path string, cfg *Config) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding config: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

// botToken resolves the token: config file first, then BOT_TOKEN env var.
func botToken() (string, error) {
	cfg, err := loadConfig()
	if err != nil {
		return "", err
	}
	return botTokenFromConfig(cfg), nil
}

// botTokenFromConfig is the pure, testable form of botToken precedence:
// config "botToken" wins; otherwise the BOT_TOKEN env var; else an error.
func botTokenFromConfig(cfg *Config) string {
	if cfg.BotToken != "" && cfg.BotToken != "PASTE_YOUR_BOT_TOKEN_HERE" {
		return cfg.BotToken
	}
	return os.Getenv("BOT_TOKEN")
}

// isBotAdmin returns true if the given Discord user ID is the owner or
// listed in the admin allowlist.
func isBotAdmin(cfg *Config, userID string) bool {
	if cfg.OwnerID != "" && cfg.OwnerID != "PASTE_YOUR_DISCORD_USER_ID_HERE" && cfg.OwnerID == userID {
		return true
	}
	for _, aid := range cfg.AdminIDs {
		if aid == userID {
			return true
		}
	}
	return false
}
