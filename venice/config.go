// config.go — Venice subpackage config: reads the shared repo .private/config.json.
package venice

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
)

// Config holds the Venice-relevant settings from the shared config file.
type Config struct {
	VeniceAPIKey      string `json:"veniceApiKey"`
	VeniceBaseURL     string `json:"veniceBaseUrl"`
	DefaultChatModel  string `json:"defaultChatModel"`
	DefaultImageModel string `json:"defaultImageModel"`
	DefaultTTSModel   string `json:"defaultTtsModel"`
	DefaultTTSVoice   string `json:"defaultTtsVoice"`
	DefaultMusicModel string `json:"defaultMusicModel"`
}

// cfg is the package-level config, initialized by Init.
var cfg *Config

// configPath resolves the configuration file path.
// Priority: SUNFIRE_CONFIG_FILE env var -> SUNFIRE_HOME/.private/config.json -> source-tree relative path -> fallback.
func configPath() string {
	if path := os.Getenv("SUNFIRE_CONFIG_FILE"); path != "" {
		return path
	}
	if home := os.Getenv("SUNFIRE_HOME"); home != "" {
		p := filepath.Join(home, ".private", "config.json")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	if _, file, _, ok := runtime.Caller(0); ok {
		p := filepath.Join(filepath.Dir(file), "..", ".private", "config.json")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return filepath.Join("..", ".private", "config.json")
}

// loadConfig reads configuration from the resolved configPath.
func loadConfig() (*Config, error) {
	path := configPath()
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var raw struct {
		VeniceAPIKey      string `json:"veniceApiKey"`
		VeniceBaseURL     string `json:"veniceBaseUrl"`
		DefaultChatModel  string `json:"defaultChatModel"`
		DefaultImageModel string `json:"defaultImageModel"`
		DefaultTTSModel   string `json:"defaultTtsModel"`
		DefaultTTSVoice   string `json:"defaultTtsVoice"`
		DefaultMusicModel string `json:"defaultMusicModel"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	// NOTE: we do NOT read the shared "defaultImageModel" config key, because
	// that key now holds the A2E default (seedream) which Venice doesn't
	// support. Venice's default image model is always flux-2-pro unless the
	// user passes model: explicitly.
	c := &Config{
		VeniceAPIKey:      raw.VeniceAPIKey,
		VeniceBaseURL:     raw.VeniceBaseURL,
		DefaultChatModel:  raw.DefaultChatModel,
		DefaultImageModel: "flux-2-pro",
		DefaultTTSModel:   raw.DefaultTTSModel,
		DefaultTTSVoice:   raw.DefaultTTSVoice,
		DefaultMusicModel: raw.DefaultMusicModel,
	}
	if c.VeniceBaseURL == "" {
		c.VeniceBaseURL = "https://api.venice.ai/api/v1"
	}
	if c.DefaultChatModel == "" {
		c.DefaultChatModel = "gemini-3-6-flash"
	}
	if c.DefaultTTSModel == "" {
		c.DefaultTTSModel = "tts-kokoro"
	}
	if c.DefaultTTSVoice == "" {
		c.DefaultTTSVoice = "af_bella"
	}
	if c.DefaultMusicModel == "" {
		c.DefaultMusicModel = "minimax-music-v26"
	}
	return c, nil
}
