package main

import (
	"fmt"
	"math/rand"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
)

func handlePickCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	options := i.ApplicationCommandData().Options
	if len(options) == 0 {
		return
	}

	sub := options[0]
	count := 1
	var countdownMinutes int
	includeBots := false
	var roleFilter *discordgo.Role
	excludeRaw := ""

	for _, opt := range sub.Options {
		switch opt.Name {
		case "count":
			count = int(opt.IntValue())
		case "include-bots":
			includeBots = opt.BoolValue()
		case "role":
			roleFilter = opt.RoleValue(s, i.GuildID)
		case "exclude":
			excludeRaw = opt.StringValue()
		case "countdown-minutes":
			countdownMinutes = int(opt.IntValue())
		}
	}

	if count < 1 {
		count = 1
	}

	countdownLine := ""
	if countdownMinutes > 0 {
		unixSeconds := time.Now().Add(time.Duration(countdownMinutes) * time.Minute).Unix()
		countdownLine = fmt.Sprintf("⏱️ **Expires <t:%d:R>**\n\n", unixSeconds)
	}

	switch sub.Name {
	case "random-member":
		guild, err := s.Guild(i.GuildID)
		if err != nil {
			_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{Content: "❌ Failed to fetch guild members."},
			})
			return
		}

		members, err := s.GuildMembers(i.GuildID, "", 1000)
		if err != nil {
			_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{Content: "❌ Failed to fetch members: " + err.Error()},
			})
			return
		}

		excludeSet := make(map[string]bool)
		for _, token := range strings.Split(excludeRaw, ",") {
			token = strings.TrimSpace(token)
			token = strings.TrimPrefix(token, "<@")
			token = strings.TrimPrefix(token, "!")
			token = strings.TrimSuffix(token, ">")
			if token != "" {
				excludeSet[token] = true
			}
		}

		var pool []*discordgo.Member
		for _, m := range members {
			if m.User == nil {
				continue
			}
			if !includeBots && m.User.Bot {
				continue
			}
			if excludeSet[m.User.ID] {
				continue
			}
			if roleFilter != nil {
				hasRole := false
				for _, rID := range m.Roles {
					if rID == roleFilter.ID {
						hasRole = true
						break
					}
				}
				if !hasRole {
					continue
				}
			}
			pool = append(pool, m)
		}

		if len(pool) == 0 {
			_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{Content: "❌ No eligible members found matching criteria."},
			})
			return
		}

		r := rand.New(rand.NewSource(time.Now().UnixNano()))
		r.Shuffle(len(pool), func(i, j int) { pool[i], pool[j] = pool[j], pool[i] })

		if count > len(pool) {
			count = len(pool)
		}

		var picked []string
		for idx := 0; idx < count; idx++ {
			picked = append(picked, fmt.Sprintf("%d. <@%s> (`%s`)", idx+1, pool[idx].User.ID, pool[idx].User.Username))
		}

		title := fmt.Sprintf("🎲 Picked %d Random Member(s) in %s", count, guild.Name)
		content := countdownLine + strings.Join(picked, "\n")

		embed := &discordgo.MessageEmbed{
			Title:       title,
			Description: content,
			Color:       0x00FF88,
		}

		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Embeds: []*discordgo.MessageEmbed{embed},
			},
		})

	case "random-role":
		guild, err := s.Guild(i.GuildID)
		if err != nil {
			_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{Content: "❌ Failed to fetch roles."},
			})
			return
		}

		var pool []*discordgo.Role
		for _, r := range guild.Roles {
			if r.Name != "@everyone" {
				pool = append(pool, r)
			}
		}

		if len(pool) == 0 {
			_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{Content: "❌ No eligible roles found."},
			})
			return
		}

		rng := rand.New(rand.NewSource(time.Now().UnixNano()))
		rng.Shuffle(len(pool), func(i, j int) { pool[i], pool[j] = pool[j], pool[i] })

		if count > len(pool) {
			count = len(pool)
		}

		var picked []string
		for idx := 0; idx < count; idx++ {
			picked = append(picked, fmt.Sprintf("%d. <@&%s> (`%s`)", idx+1, pool[idx].ID, pool[idx].Name))
		}

		embed := &discordgo.MessageEmbed{
			Title:       fmt.Sprintf("🎲 Picked %d Random Role(s)", count),
			Description: countdownLine + strings.Join(picked, "\n"),
			Color:       0x3498DB,
		}

		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Embeds: []*discordgo.MessageEmbed{embed},
			},
		})
	}
}
