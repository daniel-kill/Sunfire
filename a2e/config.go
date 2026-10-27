// config.go — A2E subpackage config: reads the shared repo .private/config.json.
package a2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
)

// Config holds the A2E-relevant settings from the shared config file.
type Config struct {
	A2EAPIKey         string `json:"A2E_API_KEY"`
	A2EBaseURL        string `json:"a2eBaseUrl"`
	DefaultImageModel string `json:"defaultImageModel"`
	DefaultVideoModel string `json:"defaultVideoModel"`
}

// cfg is the package-level config, initialized by Init.
var cfg *Config

// ac is the package-level A2E API client, initialized by Init.
var ac *a2eClient

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
		A2EAPIKey         string `json:"A2E_API_KEY"`
		A2EBaseURL        string `json:"a2eBaseUrl"`
		DefaultImageModel string `json:"defaultImageModel"`
		DefaultVideoModel string `json:"defaultVideoModel"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	c := &Config{
		A2EAPIKey:         raw.A2EAPIKey,
		A2EBaseURL:        raw.A2EBaseURL,
		DefaultImageModel: raw.DefaultImageModel,
		DefaultVideoModel: raw.DefaultVideoModel,
	}
	if c.A2EBaseURL == "" {
		c.A2EBaseURL = "https://video.a2e.ai"
	}
	if c.DefaultImageModel == "" {
		c.DefaultImageModel = "seedream"
	}
	if c.DefaultVideoModel == "" {
		c.DefaultVideoModel = "wan3.0-video"
	}
	return c, nil
}
