package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestConfigLoadAndSave(t *testing.T) {
	tmpDir := t.TempDir()
	cfgFile := filepath.Join(tmpDir, "config.json")

	initialJSON := `{
  "project": "Sunfire",
  "notes": "Test notes",
  "botToken": "dummy_token",
  "applicationId": "123456",
  "publicKey": "pubkey",
  "botInviteUrl": "https://discord.com",
  "loggingChannelId": "1001",
  "ownerId": "2002",
  "infoCategoryId": "3003",
  "rulesChannelId": "4004"
}`

	if err := os.WriteFile(cfgFile, []byte(initialJSON), 0o600); err != nil {
		t.Fatalf("failed to write test config: %v", err)
	}

	cfg, err := loadConfigFromPath(cfgFile)
	if err != nil {
		t.Fatalf("loadConfigFromPath failed: %v", err)
	}

	if cfg.LoggingChannel != "1001" {
		t.Errorf("expected LoggingChannel 1001, got %s", cfg.LoggingChannel)
	}
	if cfg.InfoCategoryID != "3003" {
		t.Errorf("expected InfoCategoryID 3003, got %s", cfg.InfoCategoryID)
	}
	if cfg.RulesChannelID != "4004" {
		t.Errorf("expected RulesChannelID 4004, got %s", cfg.RulesChannelID)
	}

	// Test modifying and saving
	cfg.InfoCategoryID = "5005"
	cfg.RulesChannelID = "6006"
	if err := saveConfig(cfgFile, cfg); err != nil {
		t.Fatalf("saveConfig failed: %v", err)
	}

	reloaded, err := loadConfigFromPath(cfgFile)
	if err != nil {
		t.Fatalf("reloading config failed: %v", err)
	}
	if reloaded.InfoCategoryID != "5005" {
		t.Errorf("expected reloaded InfoCategoryID 5005, got %s", reloaded.InfoCategoryID)
	}
	if reloaded.RulesChannelID != "6006" {
		t.Errorf("expected reloaded RulesChannelID 6006, got %s", reloaded.RulesChannelID)
	}
}

func TestConfigAliases(t *testing.T) {
	tmpDir := t.TempDir()
	cfgFile := filepath.Join(tmpDir, "config_alias.json")

	aliasJSON := `{
  "infoChannelId": "7007",
  "rulesChannel": "8008"
}`
	if err := os.WriteFile(cfgFile, []byte(aliasJSON), 0o600); err != nil {
		t.Fatalf("failed to write test config: %v", err)
	}

	cfg, err := loadConfigFromPath(cfgFile)
	if err != nil {
		t.Fatalf("loadConfigFromPath failed: %v", err)
	}
	if cfg.InfoCategoryID != "7007" {
		t.Errorf("expected InfoCategoryID 7007 from alias, got %s", cfg.InfoCategoryID)
	}
	if cfg.RulesChannelID != "8008" {
		t.Errorf("expected RulesChannelID 8008 from alias, got %s", cfg.RulesChannelID)
	}
}

func TestPublicCommands(t *testing.T) {
	publicCmds := []string{
		"help", "userinfo", "media", "emojigg",
		"analytics", "automod", "dice", "remind", "serverstats", "sysinfo", "transcript",
		"ia", "lyrics", "cloudflare", "cohere", "groq", "mistral", "nvidia",
	}
	for _, cmd := range publicCmds {
		if !isPublicCommand(cmd) {
			t.Errorf("expected command %q to be public", cmd)
		}
	}

	ownerOnlyCmds := []string{"purge", "setup", "dedup", "unknown"}
	for _, cmd := range ownerOnlyCmds {
		if isPublicCommand(cmd) {
			t.Errorf("expected command %q to be owner-only", cmd)
		}
	}
}

func TestScrapeConfigUnmarshal(t *testing.T) {
	jsonData := `{
		"urls": ["https://example.com/gallery"],
		"channelIds": [123456789012345678, "987654321098765432"]
	}`

	var sc ScrapeConfig
	if err := json.Unmarshal([]byte(jsonData), &sc); err != nil {
		t.Fatalf("failed to unmarshal ScrapeConfig: %v", err)
	}

	if len(sc.ChannelIDs) != 2 {
		t.Fatalf("expected 2 channel IDs, got %d", len(sc.ChannelIDs))
	}
	if sc.ChannelIDs[0] != "123456789012345678" {
		t.Errorf("expected channel ID '123456789012345678', got %q", sc.ChannelIDs[0])
	}
	if sc.ChannelIDs[1] != "987654321098765432" {
		t.Errorf("expected channel ID '987654321098765432', got %q", sc.ChannelIDs[1])
	}
}
