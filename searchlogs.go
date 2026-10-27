package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bwmarrin/discordgo"
)

func handleSearchLogs(s *discordgo.Session, i *discordgo.InteractionCreate) {
	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	})

	var targetUser *discordgo.User
	keyword := ""

	for _, opt := range i.ApplicationCommandData().Options {
		if opt.Name == "user" {
			targetUser = opt.UserValue(s)
		}
		if opt.Name == "keyword" {
			keyword = strings.ToLower(opt.StringValue())
		}
	}

	logPath := filepath.Join(mediaLogsRoot(), "..", "MessageLogs", i.GuildID, fmt.Sprintf("%s.json", targetUser.ID))
	data, err := os.ReadFile(logPath)
	if err != nil {
		_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{Content: "❌ No logs found for that user."})
		return
	}

	var records []MessageRecord
	_ = json.Unmarshal(data, &records)

	var matches []string
	for idx := len(records) - 1; idx >= 0; idx-- {
		rec := records[idx]
		
		match := false
		if keyword == "" {
			match = true // If no keyword, just return the most recent messages
		} else if strings.Contains(strings.ToLower(rec.Content), keyword) {
			match = true
		} else {
			for _, edit := range rec.EditHistory {
				if strings.Contains(strings.ToLower(edit.OldContent), keyword) {
					match = true
					break
				}
			}
		}

		if match {
			status := ""
			if rec.Deleted {
				status = "🗑️ *(Deleted)*"
			} else if len(rec.EditHistory) > 0 {
				status = "✏️ *(Edited)*"
			}

			preview := truncateString(strings.ReplaceAll(rec.Content, "\n", " "), 80)
			entry := fmt.Sprintf("**%s** in <#%s> %s\n> %s", rec.Timestamp[:10], rec.ChannelID, status, preview)
			matches = append(matches, entry)
		}

		if len(matches) >= 10 { // Limit to 10 results to fit in Discord embed
			break
		}
	}

	if len(matches) == 0 {
		_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{Content: "🔍 No matching logs found."})
		return
	}

	embed := &discordgo.MessageEmbed{
		Title:       fmt.Sprintf("🔍 Log Search: %s", targetUser.Username),
		Color:       0xFF6B35,
		Description: strings.Join(matches, "\n\n"),
	}

	_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
		Embeds: []*discordgo.MessageEmbed{embed},
	})
}