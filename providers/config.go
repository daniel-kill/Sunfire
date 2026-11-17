// config.go — multi-provider config loader for the /cohere-*, /cloudflare-*,
// /groq-*, /mistral-*, /nvidia-*, /openrouter-* commands.
//
// Reads the same repo-root .private/config.json used by the rest of the bot.
// The provider credentials were merged from ElyProjectX's .private/config.json:
// each is a nested object { apiKey, ... } (cloudflare also has accountId).
package providers

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
)

// ProviderConfig holds one provider's credentials block.
type ProviderConfig struct {
	APIKey    string `json:"apiKey"`
	AccountID string `json:"accountId"` // cloudflare only
}

// Config holds the provider credentials for all six services.
type Config struct {
	Cohere     ProviderConfig `json:"cohere"`
	Cloudflare ProviderConfig `json:"cloudflare"`
	Groq       ProviderConfig `json:"groq"`
	Mistral    ProviderConfig `json:"mistral"`
	Nvidia     ProviderConfig `json:"nvidia"`
	OpenRouter ProviderConfig `json:"openrouter"`
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
		Cohere     ProviderConfig `json:"cohere"`
		Cloudflare ProviderConfig `json:"cloudflare"`
		Groq       ProviderConfig `json:"groq"`
		Mistral    ProviderConfig `json:"mistral"`
		Nvidia     ProviderConfig `json:"nvidia"`
		OpenRouter ProviderConfig `json:"openrouter"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	return &Config{
		Cohere:     raw.Cohere,
		Cloudflare: raw.Cloudflare,
		Groq:       raw.Groq,
		Mistral:    raw.Mistral,
		Nvidia:     raw.Nvidia,
		OpenRouter: raw.OpenRouter,
	}, nil
}
