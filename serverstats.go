package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bwmarrin/discordgo"
)

func handleServerStats(s *discordgo.Session, i *discordgo.InteractionCreate) {
	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	})

	guild, err := s.Guild(i.GuildID)
	if err != nil {
		_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{Content: "❌ Error fetching guild info."})
		return
	}

	channels, _ := s.GuildChannels(i.GuildID)
	catCount, textCount, voiceCount, threadCount := 0, 0, 0, 0
	var channelNames []string

	for _, c := range channels {
		switch c.Type {
		case discordgo.ChannelTypeGuildCategory:
			catCount++
		case discordgo.ChannelTypeGuildText:
			textCount++
		case discordgo.ChannelTypeGuildVoice:
			voiceCount++
		case discordgo.ChannelTypeGuildPublicThread, discordgo.ChannelTypeGuildPrivateThread:
			threadCount++
		}
		if len(channelNames) < 15 {
			channelNames = append(channelNames, fmt.Sprintf("%s (%s)", c.Name, c.ID))
		}
	}

	members, _ := s.GuildMembers(i.GuildID, "", 1000)
	botCount, humanCount := 0, 0
	for _, m := range members {
		if m.User.Bot {
			botCount++
		} else {
			humanCount++
		}
	}

	imgCount, vidCount, audCount, totalSize := getMediaStorageStats(i.GuildID)
	serverAge := fmt.Sprintf("<t:%d:R>", snowflakeToTimestamp(i.GuildID))

	embed := &discordgo.MessageEmbed{
		Title:       fmt.Sprintf("📊 Server Stats: %s", guild.Name),
		Color:       0xFF6B35,
		Description: fmt.Sprintf("**Server Age:** %s\n**Total Members:** %d (%d Humans, %d Bots)", serverAge, len(members), humanCount, botCount),
		Fields: []*discordgo.MessageEmbedField{
			{Name: "Channels", Value: fmt.Sprintf("📁 %d Categories\n💬 %d Text\n🎙️ %d Voice\n🧵 %d Threads", catCount, textCount, voiceCount, threadCount), Inline: true},
			{Name: "Data/MediaLogs/ Storage", Value: fmt.Sprintf("🖼️ Images: %d\n🎬 Videos: %d\n🎵 Audio: %d\n**Total Disk:** %.2f MB", imgCount, vidCount, audCount, float64(totalSize)/(1024*1024)), Inline: true},
			{Name: "Channel IDs (Sample)", Value: "```\n" + strings.Join(channelNames, "\n") + "\n```", Inline: false},
		},
	}

	if guild.Icon != "" {
		embed.Thumbnail = &discordgo.MessageEmbedThumbnail{URL: guild.IconURL("256")}
	}

	_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
		Embeds: []*discordgo.MessageEmbed{embed},
	})
}

func getMediaStorageStats(guildID string) (int, int, int, int64) {
	img, vid, aud := 0, 0, 0
	var totalSize int64

	dir := mediaLogsDir(guildID)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return 0, 0, 0, 0
	}

	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}

		info, err := d.Info()
		if err == nil {
			totalSize += info.Size()
		}

		pathLower := strings.ToLower(path)
		if strings.Contains(pathLower, "/images/") || strings.Contains(pathLower, "\\images\\") {
			img++
		} else if strings.Contains(pathLower, "/videos/") || strings.Contains(pathLower, "\\videos\\") {
			vid++
		} else if strings.Contains(pathLower, "/audio/") || strings.Contains(pathLower, "\\audio\\") {
			aud++
		}
		return nil
	})

	return img, vid, aud, totalSize
}