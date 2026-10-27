package main

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
)

func TestAutoModConfigDefaultsAndPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "automod.json")

	config, err := loadAutoModConfig(path)
	if err != nil {
		t.Fatalf("loadAutoModConfig failed: %v", err)
	}
	if !config.Enabled {
		t.Fatal("AutoMod should be enabled by default")
	}
	if !reflect.DeepEqual(config.BannedWords, []string{"discord.gg/", "http://scam-link"}) {
		t.Fatalf("default banned words = %#v", config.BannedWords)
	}

	if err := updateAutoModConfig(path, func(config *autoModConfig) error {
		config.BannedWords = append(config.BannedWords, "  BAd phrase  ")
		config.Enabled = false
		return nil
	}); err != nil {
		t.Fatalf("updateAutoModConfig failed: %v", err)
	}

	config, err = loadAutoModConfig(path)
	if err != nil {
		t.Fatalf("loading persisted config failed: %v", err)
	}
	if config.Enabled {
		t.Fatal("disabled state was not persisted")
	}
	wantWords := []string{"bad phrase", "discord.gg/", "http://scam-link"}
	if !reflect.DeepEqual(config.BannedWords, wantWords) {
		t.Fatalf("persisted banned words = %#v; want %#v", config.BannedWords, wantWords)
	}
}

func TestAutoModConfigErrorsOnMalformedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "automod.json")
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadAutoModConfig(path); err == nil {
		t.Fatal("expected malformed config to return an error")
	}
}

func TestMatchedBannedWordIsCaseInsensitive(t *testing.T) {
	if got := matchedBannedWord("This contains BAD phrase.", []string{"bad phrase"}); got != "bad phrase" {
		t.Fatalf("matched banned word = %q; want %q", got, "bad phrase")
	}
	if got := matchedBannedWord("This is fine.", []string{"bad phrase"}); got != "" {
		t.Fatal("unexpected match")
	}
}

func TestAutoModEventsPersistAndLoadLatestFirst(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("SUNFIRE_DATA_DIR", dataDir)

	for index := 1; index <= 7; index++ {
		err := recordAutoModEvent("123", "456", autoModEvent{
			Timestamp:      time.Date(2026, time.January, index, 12, 0, 0, 0, time.UTC),
			ChannelID:      "789",
			MessageID:      fmt.Sprintf("message-%d", index),
			MatchedPhrase:  "bad phrase",
			MessageContent: "This is bad phrase.",
			MessageDeleted: index%2 == 0,
		})
		if err != nil {
			t.Fatalf("recordAutoModEvent(%d) failed: %v", index, err)
		}
	}

	events, total, err := loadAutoModEvents("123", "456", 5)
	if err != nil {
		t.Fatalf("loadAutoModEvents failed: %v", err)
	}
	if total != 7 || len(events) != 5 {
		t.Fatalf("loaded %d events (total %d); want 5 of 7", len(events), total)
	}
	if events[0].MessageID != "message-3" || events[4].MessageID != "message-7" {
		t.Fatalf("events are not the latest in chronological order: %#v", events)
	}

	path, err := autoModEventsPath("123", "../456")
	if err == nil || path != "" {
		t.Fatalf("expected invalid user ID to be rejected, got path %q and error %v", path, err)
	}
}

func TestAddWarningPersistsAndRejectsMalformedData(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("SUNFIRE_DATA_DIR", dataDir)

	count, err := addWarning("guild", "member", "moderator", "test warning")
	if err != nil {
		t.Fatalf("addWarning failed: %v", err)
	}
	if count != 1 {
		t.Fatalf("warning count = %d; want 1", count)
	}

	path := filepath.Join(dataDir, "Warnings", "guild.json")
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := addWarning("guild", "member", "moderator", "another warning"); err == nil {
		t.Fatal("expected malformed warning data to return an error")
	}
}

func TestAutoModCommandRegistration(t *testing.T) {
	buildCommandSpecs()
	for _, spec := range commandSpecs {
		if spec.Command.Name != "automod" {
			continue
		}
		if spec.Handler == nil {
			t.Fatal("/automod has no handler")
		}
		subcommands := make(map[string]bool)
		for _, option := range spec.Command.Options {
			subcommands[option.Name] = true
		}
		for _, name := range []string{
			"banned-word-add", "banned-word-remove", "banned-word-list",
			"enable", "disable", "status", "view",
		} {
			if !subcommands[name] {
				t.Errorf("/automod is missing subcommand %q", name)
			}
		}
		if spec.Command.Options[0].Type != discordgo.ApplicationCommandOptionSubCommand {
			t.Fatal("/automod options must be subcommands")
		}
		return
	}
	t.Fatal("/automod is not registered")
}
