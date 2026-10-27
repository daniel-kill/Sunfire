package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bwmarrin/discordgo"
)

// UserStat tracks detailed messaging metrics for a single user
type UserStat struct {
	MessageCount int
	WordCount    int
	CharCount    int
}

func handleAnalytics(s *discordgo.Session, i *discordgo.InteractionCreate) {
	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	})

	dir := messageLogsDir(i.GuildID)
	entries, err := os.ReadDir(dir)
	if err != nil {
		_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{Content: "❌ Could not read message logs."})
		return
	}

	var totalMessages, totalWords, totalChars, editedMessages, deletedMessages int
	userStats := make(map[string]*UserStat)
	hourlyCounts := make([]int, 24)
	
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		loc = time.Local
	}

	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		
		var records []MessageRecord
		_ = json.Unmarshal(data, &records)
		
		for _, rec := range records {
			if rec.Bot {
				continue
			}

			// Calculate lengths
			wordCount := len(strings.Fields(rec.Content))
			charCount := utf8.RuneCountInString(rec.Content) // RuneCount handles emojis correctly

			// Global Totals
			totalMessages++
			totalWords += wordCount
			totalChars += charCount

			if rec.Deleted {
				deletedMessages++
			}
			if len(rec.EditHistory) > 0 {
				editedMessages++
			}

			// User Totals
			if _, exists := userStats[rec.AuthorUsername]; !exists {
				userStats[rec.AuthorUsername] = &UserStat{}
			}
			us := userStats[rec.AuthorUsername]
			us.MessageCount++
			us.WordCount += wordCount
			us.CharCount += charCount

			// Time Data
			t, err := time.Parse(time.RFC3339, rec.Timestamp)
			if err == nil {
				hourlyCounts[t.In(loc).Hour()]++
			}
		}
	}

	if totalMessages == 0 {
		_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{Content: "❌ No human message logs found to analyze."})
		return
	}

	// 1. Generate the ASCII Heatmap & Find Peak Hour
	blocks := []rune{' ', '▂', '▃', '▄', '▅', '▆', '▇', '█'}
	maxHour := 0
	peakHourIdx := 0
	
	for h, count := range hourlyCounts {
		if count > maxHour {
			maxHour = count
			peakHourIdx = h
		}
	}

	heatmapStr := ""
	for hour, count := range hourlyCounts {
		if maxHour == 0 {
			heatmapStr += string(blocks[0])
			continue
		}
		idx := (count * 7) / maxHour
		heatmapStr += string(blocks[idx])
		
		if (hour+1)%6 == 0 && hour != 23 {
			heatmapStr += " "
		}
	}

	// Format the peak time (e.g., 14 -> 2:00 PM)
	peakTime := time.Date(0, 1, 1, peakHourIdx, 0, 0, 0, time.UTC).Format("3:00 PM")

	// 2. Format the User Breakdown String
	var userStatsBuilder strings.Builder
	for user, st := range userStats {
		percentage := (float64(st.MessageCount) / float64(totalMessages)) * 100
		
		// Avoid divide-by-zero if somehow a user has 0 messages
		avgWords, avgChars := 0.0, 0.0
		if st.MessageCount > 0 {
			avgWords = float64(st.WordCount) / float64(st.MessageCount)
			avgChars = float64(st.CharCount) / float64(st.MessageCount)
		}

		userStatsBuilder.WriteString(fmt.Sprintf("**%s**: %d msgs (%.1f%%)\n", user, st.MessageCount, percentage))
		userStatsBuilder.WriteString(fmt.Sprintf("└ 📝 Words: %d *(Avg: %.1f)* | 🔤 Chars: %d *(Avg: %.1f)*\n\n", 
			st.WordCount, avgWords, st.CharCount, avgChars))
	}

	// 3. Build the Embed
	overallAvgWords := float64(totalWords) / float64(totalMessages)
	
	embed := &discordgo.MessageEmbed{
		Title: "📈 Live Chat Analytics",
		Color: 0xFF6B35,
		Description: fmt.Sprintf("Total Messages: **%d**\nTotal Words: **%d**\nTotal Chars: **%d**\nEdited: **%d** | Deleted: **%d**", 
			totalMessages, totalWords, totalChars, editedMessages, deletedMessages),
		Fields: []*discordgo.MessageEmbedField{
			{
				Name: "👥 User Breakdown", 
				Value: userStatsBuilder.String(), 
				Inline: false,
			},
			{
				Name: "💡 Fun Facts",
				Value: fmt.Sprintf("• **Peak Activity Hour:** %s (EST/EDT)\n• **Overall Avg Message:** %.1f words", peakTime, overallAvgWords),
				Inline: false,
			},
			{
				Name: "🕒 24-Hour Activity Heatmap (EST/EDT)", 
				Value: fmt.Sprintf("```text\n%s\n12a   6a    12p   6p    11p\n```", heatmapStr), 
				Inline: false,
			},
		},
	}

	_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
		Embeds: []*discordgo.MessageEmbed{embed},
	})
}
