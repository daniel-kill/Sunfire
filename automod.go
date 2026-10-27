package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
)

var autoModMu sync.Mutex

type WarningRecord struct {
	Timestamp string `json:"timestamp"`
	WarnerID  string `json:"warner_id"`
	Reason    string `json:"reason"`
}

func getWarningsFile(guildID string) string {
	dir := dataPath("Warnings")
	return filepath.Join(dir, fmt.Sprintf("%s.json", guildID))
}

func addWarning(guildID, userID, warnerID, reason string) (int, error) {
	autoModMu.Lock()
	defer autoModMu.Unlock()

	filePath := getWarningsFile(guildID)
	warnings := make(map[string][]WarningRecord)
	data, err := os.ReadFile(filePath)
	if err != nil && !os.IsNotExist(err) {
		return 0, fmt.Errorf("reading warning records: %w", err)
	}
	if err == nil {
		if err := json.Unmarshal(data, &warnings); err != nil {
			return 0, fmt.Errorf("parsing warning records: %w", err)
		}
	}

	warnings[userID] = append(warnings[userID], WarningRecord{
		Timestamp: time.Now().Format(time.RFC3339),
		WarnerID:  warnerID,
		Reason:    reason,
	})

	out, err := json.MarshalIndent(warnings, "", "  ")
	if err != nil {
		return 0, fmt.Errorf("encoding warning records: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
		return 0, fmt.Errorf("creating warning records directory: %w", err)
	}
	if err := os.WriteFile(filePath, out, 0o600); err != nil {
		return 0, fmt.Errorf("writing warning records: %w", err)
	}
	return len(warnings[userID]), nil
}

func handleWarn(s *discordgo.Session, i *discordgo.InteractionCreate) {
	var user *discordgo.User
	reason := "No reason provided."

	for _, opt := range i.ApplicationCommandData().Options {
		if opt.Name == "user" {
			user = opt.UserValue(s)
		}
		if opt.Name == "reason" {
			reason = opt.StringValue()
		}
	}

	warnerID := ""
	if i.Member != nil {
		warnerID = i.Member.User.ID
	}

	strikeCount, err := addWarning(i.GuildID, user.ID, warnerID, reason)
	if err != nil {
		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{Content: "❌ Failed to save warning."},
		})
		return
	}

	msg := fmt.Sprintf("⚠️ **%s** has been warned. (Strike %d)\n**Reason:** %s", user.Username, strikeCount, reason)

	// --- AUTO ESCALATION ---
	if strikeCount == 3 {
		until := time.Now().Add(10 * time.Minute)
		_ = s.GuildMemberTimeout(i.GuildID, user.ID, &until)
		msg += "\n🛑 *Auto-Escalation:* User has been timed out for 10 minutes."
	} else if strikeCount >= 5 {
		_ = s.GuildMemberDeleteWithReason(i.GuildID, user.ID, "Auto-Escalation: 5 Strikes reached.")
		msg += "\n🔨 *Auto-Escalation:* User has been kicked from the server."
	}

	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Content: msg},
	})
}

type autoModConfig struct {
	Enabled     bool     `json:"enabled"`
	BannedWords []string `json:"banned_words"`
}

type autoModEvent struct {
	Timestamp      time.Time `json:"timestamp"`
	ChannelID      string    `json:"channel_id"`
	MessageID      string    `json:"message_id"`
	MatchedPhrase  string    `json:"matched_phrase"`
	MessageContent string    `json:"message_content"`
	MessageDeleted bool      `json:"message_deleted"`
}

func autoModEventsPath(guildID, userID string) (string, error) {
	if !isDiscordSnowflake(guildID) || !isDiscordSnowflake(userID) {
		return "", fmt.Errorf("invalid guild or user ID")
	}
	return dataPath("AutoModEvents", guildID, userID+".jsonl"), nil
}

func isDiscordSnowflake(id string) bool {
	if id == "" || len(id) > 20 {
		return false
	}
	for _, r := range id {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func truncateAutoModContent(content string, maxRunes int) string {
	runes := []rune(content)
	if len(runes) <= maxRunes {
		return content
	}
	return string(runes[:maxRunes]) + "…"
}

func recordAutoModEvent(guildID, userID string, event autoModEvent) error {
	path, err := autoModEventsPath(guildID, userID)
	if err != nil {
		return err
	}
	event.MessageContent = truncateAutoModContent(event.MessageContent, 500)
	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("encoding automod event: %w", err)
	}

	autoModMu.Lock()
	defer autoModMu.Unlock()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating automod events directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("opening automod events file: %w", err)
	}
	if _, err := file.Write(append(data, '\n')); err != nil {
		_ = file.Close()
		return fmt.Errorf("writing automod event: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("closing automod events file: %w", err)
	}
	return nil
}

func loadAutoModEvents(guildID, userID string, limit int) ([]autoModEvent, int, error) {
	path, err := autoModEventsPath(guildID, userID)
	if err != nil {
		return nil, 0, err
	}

	autoModMu.Lock()
	defer autoModMu.Unlock()
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return []autoModEvent{}, 0, nil
	}
	if err != nil {
		return nil, 0, fmt.Errorf("opening automod events file: %w", err)
	}
	defer file.Close()

	var events []autoModEvent
	total := 0
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var event autoModEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return nil, total, fmt.Errorf("parsing automod event %d: %w", total+1, err)
		}
		total++
		if limit <= 0 {
			continue
		}
		if len(events) < limit {
			events = append(events, event)
		} else {
			copy(events, events[1:])
			events[len(events)-1] = event
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, total, fmt.Errorf("reading automod events: %w", err)
	}
	return events, total, nil
}

func autoModConfigPath(guildID string) string {
	return dataPath("AutoMod", guildID+".json")
}

func defaultAutoModConfig() autoModConfig {
	return autoModConfig{
		Enabled:     true,
		BannedWords: []string{"discord.gg/", "http://scam-link"},
	}
}

func readAutoModConfig(path string) (autoModConfig, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return defaultAutoModConfig(), nil
	}
	if err != nil {
		return autoModConfig{}, fmt.Errorf("reading automod config: %w", err)
	}

	var config autoModConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return autoModConfig{}, fmt.Errorf("parsing automod config: %w", err)
	}
	if config.BannedWords == nil {
		config.BannedWords = []string{}
	}

	normalized := make([]string, 0, len(config.BannedWords))
	seen := make(map[string]struct{}, len(config.BannedWords))
	for _, word := range config.BannedWords {
		word = strings.ToLower(strings.TrimSpace(word))
		if word == "" {
			continue
		}
		if _, exists := seen[word]; exists {
			continue
		}
		seen[word] = struct{}{}
		normalized = append(normalized, word)
	}
	sort.Strings(normalized)
	config.BannedWords = normalized
	return config, nil
}

func writeAutoModConfig(path string, config autoModConfig) error {
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding automod config: %w", err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating automod config directory: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".automod-*.tmp")
	if err != nil {
		return fmt.Errorf("creating temporary automod config: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("securing temporary automod config: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing automod config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing automod config: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replacing automod config: %w", err)
	}
	return nil
}

func loadAutoModConfig(path string) (autoModConfig, error) {
	autoModMu.Lock()
	defer autoModMu.Unlock()
	return readAutoModConfig(path)
}

func updateAutoModConfig(path string, update func(*autoModConfig) error) error {
	autoModMu.Lock()
	defer autoModMu.Unlock()

	config, err := readAutoModConfig(path)
	if err != nil {
		return err
	}
	if err := update(&config); err != nil {
		return err
	}
	return writeAutoModConfig(path, config)
}

func matchedBannedWord(content string, words []string) string {
	content = strings.ToLower(content)
	for _, word := range words {
		if strings.Contains(content, word) {
			return word
		}
	}
	return ""
}

func handleAutoModCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if i.Member == nil || i.Member.User == nil {
		respondAutoMod(s, i, "❌ This command can only be used by a server member.")
		return
	}
	permissions, err := s.UserChannelPermissions(i.Member.User.ID, i.ChannelID)
	if err != nil {
		log.Printf("failed to check automod permissions for member %s: %v", i.Member.User.ID, err)
		respondAutoMod(s, i, "❌ Could not verify your permissions; please try again later.")
		return
	}
	if permissions&discordgo.PermissionAdministrator == 0 && permissions&discordgo.PermissionManageMessages == 0 {
		respondAutoMod(s, i, "❌ You need Manage Messages or Administrator permission to configure AutoMod.")
		return
	}

	options := i.ApplicationCommandData().Options
	if len(options) == 0 {
		respondAutoMod(s, i, "❌ Choose an AutoMod command.")
		return
	}
	subcommand := options[0]
	path := autoModConfigPath(i.GuildID)

	switch subcommand.Name {
	case "banned-word-add":
		word := autoModWordOption(subcommand)
		word = strings.ToLower(strings.TrimSpace(word))
		if word == "" {
			respondAutoMod(s, i, "❌ The banned word or phrase cannot be empty.")
			return
		}
		err := updateAutoModConfig(path, func(config *autoModConfig) error {
			for _, existing := range config.BannedWords {
				if existing == word {
					return fmt.Errorf("that word or phrase is already banned")
				}
			}
			config.BannedWords = append(config.BannedWords, word)
			sort.Strings(config.BannedWords)
			return nil
		})
		if err != nil {
			log.Printf("failed to add automod word for guild %s: %v", i.GuildID, err)
			respondAutoMod(s, i, "❌ Could not add that banned word or phrase: "+err.Error())
			return
		}
		respondAutoMod(s, i, fmt.Sprintf("✅ Added `%s` to the AutoMod banned-word list.", word))

	case "banned-word-remove":
		word := strings.ToLower(strings.TrimSpace(autoModWordOption(subcommand)))
		if word == "" {
			respondAutoMod(s, i, "❌ Provide the banned word or phrase to remove.")
			return
		}
		removed := false
		err := updateAutoModConfig(path, func(config *autoModConfig) error {
			words := config.BannedWords[:0]
			for _, existing := range config.BannedWords {
				if existing == word {
					removed = true
					continue
				}
				words = append(words, existing)
			}
			config.BannedWords = words
			if !removed {
				return fmt.Errorf("that word or phrase is not in the banned list")
			}
			return nil
		})
		if err != nil {
			log.Printf("failed to remove automod word for guild %s: %v", i.GuildID, err)
			respondAutoMod(s, i, "❌ Could not remove that banned word or phrase: "+err.Error())
			return
		}
		respondAutoMod(s, i, fmt.Sprintf("✅ Removed `%s` from the AutoMod banned-word list.", word))

	case "banned-word-list":
		config, err := loadAutoModConfig(path)
		if err != nil {
			log.Printf("failed to load automod config for guild %s: %v", i.GuildID, err)
			respondAutoMod(s, i, "❌ Could not load the AutoMod banned-word list.")
			return
		}
		if len(config.BannedWords) == 0 {
			respondAutoMod(s, i, "The AutoMod banned-word list is empty.")
			return
		}
		const maxListedWords = 15
		words := config.BannedWords
		message := strings.Join(words[:min(len(words), maxListedWords)], "\n")
		if len(words) > maxListedWords {
			message += fmt.Sprintf("\n…and %d more.", len(words)-maxListedWords)
		}
		respondAutoMod(s, i, fmt.Sprintf("**AutoMod banned words (%d):**\n%s", len(words), message))

	case "enable", "disable":
		enabled := subcommand.Name == "enable"
		err := updateAutoModConfig(path, func(config *autoModConfig) error {
			config.Enabled = enabled
			return nil
		})
		if err != nil {
			log.Printf("failed to update automod state for guild %s: %v", i.GuildID, err)
			respondAutoMod(s, i, "❌ Could not update AutoMod settings.")
			return
		}
		state := "disabled"
		if enabled {
			state = "enabled"
		}
		respondAutoMod(s, i, "✅ AutoMod is now "+state+".")

	case "status":
		config, err := loadAutoModConfig(path)
		if err != nil {
			log.Printf("failed to load automod config for guild %s: %v", i.GuildID, err)
			respondAutoMod(s, i, "❌ Could not load AutoMod settings.")
			return
		}
		state := "disabled"
		if config.Enabled {
			state = "enabled"
		}
		respondAutoMod(s, i, fmt.Sprintf("AutoMod is **%s** with **%d** banned words or phrases.", state, len(config.BannedWords)))

	case "view":
		var user *discordgo.User
		for _, option := range subcommand.Options {
			if option.Name == "user" {
				user = option.UserValue(s)
				break
			}
		}
		if user == nil {
			respondAutoMod(s, i, "❌ Select a user whose AutoMod events you want to view.")
			return
		}
		const maxEventsToShow = 5
		events, total, err := loadAutoModEvents(i.GuildID, user.ID, maxEventsToShow)
		if err != nil {
			log.Printf("failed to load automod events for member %s in guild %s: %v", user.ID, i.GuildID, err)
			respondAutoMod(s, i, "❌ Could not load AutoMod events for that user.")
			return
		}
		if total == 0 {
			respondAutoMod(s, i, fmt.Sprintf("No AutoMod events recorded for <@%s>.", user.ID))
			return
		}
		var output strings.Builder
		fmt.Fprintf(&output, "**AutoMod events for <@%s> (%d total; showing latest %d):**", user.ID, total, len(events))
		for index := len(events) - 1; index >= 0; index-- {
			event := events[index]
			deleted := "message not deleted"
			if event.MessageDeleted {
				deleted = "message deleted"
			}
			phrase := strings.ReplaceAll(event.MatchedPhrase, "`", "\\`")
			fmt.Fprintf(&output, "\n\n<t:%d:f> in <#%s> — matched `%s`; %s.",
				event.Timestamp.Unix(), event.ChannelID, phrase, deleted)
			if event.MessageContent != "" {
				fmt.Fprintf(&output, "\n> %s", truncateAutoModContent(event.MessageContent, 160))
			}
		}
		if total > len(events) {
			fmt.Fprintf(&output, "\n\n%d older event(s) not shown.", total-len(events))
		}
		respondAutoMod(s, i, output.String())

	default:
		respondAutoMod(s, i, "❌ Unknown AutoMod command.")
	}
}

func autoModWordOption(command *discordgo.ApplicationCommandInteractionDataOption) string {
	for _, option := range command.Options {
		if option.Name == "word" {
			return option.StringValue()
		}
	}
	return ""
}

func respondAutoMod(s *discordgo.Session, i *discordgo.InteractionCreate, content string) {
	if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content:         content,
			Flags:           discordgo.MessageFlagsEphemeral,
			AllowedMentions: &discordgo.MessageAllowedMentions{},
		},
	}); err != nil {
		log.Printf("failed to respond to /automod in guild %s: %v", i.GuildID, err)
	}
}

// checkAutoMod runs on every message to block configured banned words or phrases.
func checkAutoMod(s *discordgo.Session, m *discordgo.MessageCreate) {
	if m.GuildID == "" || m.Author == nil || m.Author.Bot {
		return
	}

	config, err := loadAutoModConfig(autoModConfigPath(m.GuildID))
	if err != nil {
		log.Printf("failed to load automod config for guild %s: %v", m.GuildID, err)
		return
	}
	matchedPhrase := matchedBannedWord(m.Content, config.BannedWords)
	if !config.Enabled || matchedPhrase == "" {
		return
	}

	deleteErr := s.ChannelMessageDelete(m.ChannelID, m.ID)
	if deleteErr != nil {
		log.Printf("failed to delete automod message %s in channel %s: %v", m.ID, m.ChannelID, deleteErr)
	}
	event := autoModEvent{
		Timestamp:      time.Now().UTC(),
		ChannelID:      m.ChannelID,
		MessageID:      m.ID,
		MatchedPhrase:  matchedPhrase,
		MessageContent: m.Content,
		MessageDeleted: deleteErr == nil,
	}
	if err := recordAutoModEvent(m.GuildID, m.Author.ID, event); err != nil {
		log.Printf("failed to record automod event for member %s in guild %s: %v", m.Author.ID, m.GuildID, err)
	}
	if deleteErr != nil {
		return
	}
	warnerID := ""
	if s.State != nil && s.State.User != nil {
		warnerID = s.State.User.ID
	}
	if _, err := addWarning(m.GuildID, m.Author.ID, warnerID, "AutoMod: Banned phrase detected."); err != nil {
		log.Printf("failed to record automod warning for member %s in guild %s: %v", m.Author.ID, m.GuildID, err)
	}

	ch, err := s.UserChannelCreate(m.Author.ID)
	if err != nil {
		log.Printf("failed to open automod notification DM for member %s: %v", m.Author.ID, err)
		return
	}
	if _, err := s.ChannelMessageSend(ch.ID, "⚠️ Your message was deleted for matching a banned word or phrase."); err != nil {
		log.Printf("failed to send automod notification DM to member %s: %v", m.Author.ID, err)
	}
}
