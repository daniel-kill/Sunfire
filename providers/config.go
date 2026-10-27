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

// loadConfig reads the repo-root .private/config.json (relative to this file).
func loadConfig() (*Config, error) {
	_, file, _, ok := runtime.Caller(0)
	path := ""
	if ok {
		path = filepath.Join(filepath.Dir(file), "..", ".private", "config.json")
	} else {
		path = filepath.Join("..", ".private", "config.json")
	}
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
