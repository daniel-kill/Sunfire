package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/bwmarrin/discordgo"
)

func handleBackup(s *discordgo.Session, i *discordgo.InteractionCreate) {
	// 1. Immediately defer the response so Discord doesn't time out
	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	})

	// 2. Skip the strict subcommand check!
	// If Discord's cache sends `/backup` without `create`, we just run it anyway.

	// 3. Proceed with the backup
	guild, err := s.Guild(i.GuildID)
	if err != nil {
		_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{Content: "❌ Failed to fetch guild info."})
		return
	}

	channels, _ := s.GuildChannels(i.GuildID)
	roles, _ := s.GuildRoles(i.GuildID)

	backupData := map[string]interface{}{
		"guild_name": guild.Name,
		"guild_id":   guild.ID,
		"timestamp":  time.Now().Format(time.RFC3339),
		"channels":   channels,
		"roles":      roles,
	}

	data, err := json.MarshalIndent(backupData, "", "  ")
	if err != nil {
		_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{Content: "❌ Serialization error: " + err.Error()})
		return
	}

	backupDir := dataPath("Backups", i.GuildID)
	_ = os.MkdirAll(backupDir, 0755)

	filename := fmt.Sprintf("backup_%s.json", time.Now().Format("20060102_150405"))
	filepath := filepath.Join(backupDir, filename)

	if err := os.WriteFile(filepath, data, 0644); err != nil {
		_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{Content: "❌ Failed to write backup: " + err.Error()})
		return
	}

	f, _ := os.Open(filepath)
	defer f.Close()

	_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
		Content: "✅ **Server Snapshot Created** and saved under the configured data directory's `Backups/` folder.",
		Files:   []*discordgo.File{{Name: filename, Reader: f}},
	})
}

func handleTranscript(s *discordgo.Session, i *discordgo.InteractionCreate) {
	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	})

	limit := 100
	for _, opt := range i.ApplicationCommandData().Options {
		if opt.Name == "limit" {
			limit = int(opt.IntValue())
		}
	}
	if limit > 5000 {
		limit = 5000
	}

	var allMessages []*discordgo.Message
	beforeID := ""

	for len(allMessages) < limit {
		fetch := 100
		if limit-len(allMessages) < 100 {
			fetch = limit - len(allMessages)
		}

		msgs, err := s.ChannelMessages(i.ChannelID, fetch, beforeID, "", "")
		if err != nil || len(msgs) == 0 {
			break
		}

		allMessages = append(allMessages, msgs...)
		beforeID = msgs[len(msgs)-1].ID
	}

	// Reverse to chronological order
	for x, y := 0, len(allMessages)-1; x < y; x, y = x+1, y-1 {
		allMessages[x], allMessages[y] = allMessages[y], allMessages[x]
	}

	transcript := fmt.Sprintf("# Transcript for channel %s\nGenerated: %s\n\n", i.ChannelID, time.Now().Format(time.RFC1123))
	for _, m := range allMessages {
		// m.Timestamp is already a time.Time object, so we just format it directly!
		transcript += fmt.Sprintf("[%s] **%s**: %s\n", m.Timestamp.Format("2006-01-02 15:04:05"), m.Author.Username, m.Content)
	}

	filename := fmt.Sprintf("transcript_%s.md", i.ChannelID)
	_ = os.WriteFile(filename, []byte(transcript), 0644)
	defer os.Remove(filename)

	f, _ := os.Open(filename)
	defer f.Close()

	_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
		Content: fmt.Sprintf("✅ Exported %d messages.", len(allMessages)),
		Files:   []*discordgo.File{{Name: filename, Reader: f}},
	})
}
