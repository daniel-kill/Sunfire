package main

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
)

func handleTimeout(s *discordgo.Session, i *discordgo.InteractionCreate) {
	var user *discordgo.User
	minutes := 0
	reason := "No reason provided."

	for _, opt := range i.ApplicationCommandData().Options {
		if opt.Name == "user" {
			user = opt.UserValue(s)
		}
		if opt.Name == "duration" {
			minutes = int(opt.IntValue())
		}
		if opt.Name == "reason" {
			reason = opt.StringValue()
		}
	}

	until := time.Now().Add(time.Duration(minutes) * time.Minute)
	err := s.GuildMemberTimeout(i.GuildID, user.ID, &until)

	msg := fmt.Sprintf("✅ **%s** has been timed out for %d minutes. Reason: %s", user.Username, minutes, reason)
	if err != nil {
		msg = "❌ Failed to timeout user: " + err.Error()
	}

	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Content: msg},
	})
}

func handleKick(s *discordgo.Session, i *discordgo.InteractionCreate) {
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

	err := s.GuildMemberDeleteWithReason(i.GuildID, user.ID, reason)
	msg := fmt.Sprintf("✅ **%s** was kicked. Reason: %s", user.Username, reason)
	if err != nil {
		msg = "❌ Failed to kick user: " + err.Error()
	}

	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Content: msg},
	})
}

func handleBan(s *discordgo.Session, i *discordgo.InteractionCreate) {
	options := i.ApplicationCommandData().Options
	if len(options) == 0 {
		respondBan(s, i, "❌ Choose `add`, `remove`, or `view`.")
		return
	}

	subcommand := options[0]
	switch subcommand.Name {
	case "add":
		var user *discordgo.User
		days := 0
		reason := "No reason provided."
		for _, opt := range subcommand.Options {
			switch opt.Name {
			case "user":
				user = opt.UserValue(s)
			case "days":
				days = int(opt.IntValue())
			case "reason":
				reason = opt.StringValue()
			}
		}
		if user == nil || user.ID == "" {
			respondBan(s, i, "❌ A valid user is required.")
			return
		}
		if err := s.GuildBanCreateWithReason(i.GuildID, user.ID, reason, days); err != nil {
			respondBan(s, i, "❌ Failed to ban user: "+err.Error())
			return
		}
		respondBan(s, i, fmt.Sprintf("🔨 **%s** was banned. Reason: %s", user.Username, reason))

	case "remove":
		userID := ""
		for _, opt := range subcommand.Options {
			if opt.Name == "user" {
				userID = parseBanUserID(opt.StringValue())
			}
		}
		if userID == "" {
			respondBan(s, i, "❌ Enter a valid Discord user ID or mention.")
			return
		}
		if err := s.GuildBanDelete(i.GuildID, userID); err != nil {
			respondBan(s, i, "❌ Failed to remove ban: "+err.Error())
			return
		}
		respondBan(s, i, fmt.Sprintf("✅ Removed the ban for user `%s`.", userID))

	case "view":
		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{Flags: discordgo.MessageFlagsEphemeral},
		})
		bans, err := fetchGuildBans(s, i.GuildID)
		if err != nil {
			_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
				Content: "❌ Failed to fetch server bans: " + err.Error(),
			})
			return
		}
		content, components := banPage(bans, 0)
		_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
			Content:    content,
			Components: components,
		})

	default:
		respondBan(s, i, "❌ Choose `add`, `remove`, or `view`.")
	}
}

func respondBan(s *discordgo.Session, i *discordgo.InteractionCreate, content string) {
	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Content: content, Flags: discordgo.MessageFlagsEphemeral},
	})
}

func parseBanUserID(value string) string {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "<@") && strings.HasSuffix(value, ">") {
		value = strings.TrimSuffix(strings.TrimPrefix(value, "<@"), ">")
		value = strings.TrimPrefix(value, "!")
	}
	if _, err := strconv.ParseUint(value, 10, 64); err != nil {
		return ""
	}
	return value
}

const banPageContentLimit = 1750

func fetchGuildBans(s *discordgo.Session, guildID string) ([]*discordgo.GuildBan, error) {
	const limit = 1000
	var bans []*discordgo.GuildBan
	beforeID := ""
	for {
		page, err := s.GuildBans(guildID, limit, beforeID, "")
		if err != nil {
			return nil, err
		}
		bans = append(bans, page...)
		if len(page) < limit {
			break
		}
		beforeID = page[len(page)-1].User.ID
	}
	sort.Slice(bans, func(a, b int) bool {
		return strings.ToLower(bans[a].User.Username) < strings.ToLower(bans[b].User.Username)
	})
	return bans, nil
}

func banPage(bans []*discordgo.GuildBan, page int) (string, []discordgo.MessageComponent) {
	pages := splitBanPages(bans)
	if len(pages) == 0 {
		return "There are no active server bans.", nil
	}
	if page < 0 {
		page = 0
	}
	if page >= len(pages) {
		page = len(pages) - 1
	}

	content := fmt.Sprintf("🔨 **Server bans — page %d/%d**\n%s", page+1, len(pages), pages[page])
	if len(pages) == 1 {
		return content, nil
	}
	components := []discordgo.MessageComponent{
		discordgo.ActionsRow{Components: []discordgo.MessageComponent{
			discordgo.Button{
				Label:    "Previous",
				Style:    discordgo.PrimaryButton,
				CustomID: "ban:view:" + strconv.Itoa(page-1),
				Disabled: page == 0,
			},
			discordgo.Button{
				Label:    "Next",
				Style:    discordgo.PrimaryButton,
				CustomID: "ban:view:" + strconv.Itoa(page+1),
				Disabled: page+1 >= len(pages),
			},
		}},
	}
	return content, components
}

func splitBanPages(bans []*discordgo.GuildBan) []string {
	var pages []string
	var current strings.Builder
	for _, ban := range bans {
		line := fmt.Sprintf("• %s (`%s`)\n", ban.User.Username, ban.User.ID)
		if current.Len() > 0 && current.Len()+len(line) > banPageContentLimit {
			pages = append(pages, current.String())
			current.Reset()
		}
		current.WriteString(line)
	}
	if current.Len() > 0 {
		pages = append(pages, current.String())
	}
	return pages
}

func handleBanPageComponent(s *discordgo.Session, i *discordgo.InteractionCreate) {
	customID := i.MessageComponentData().CustomID
	pageText := strings.TrimPrefix(customID, "ban:view:")
	page, err := strconv.Atoi(pageText)
	if err != nil || page < 0 {
		respondBan(s, i, "❌ Invalid ban-list page.")
		return
	}
	bans, err := fetchGuildBans(s, i.GuildID)
	if err != nil {
		respondBan(s, i, "❌ Failed to fetch server bans: "+err.Error())
		return
	}
	content, components := banPage(bans, page)
	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseUpdateMessage,
		Data: &discordgo.InteractionResponseData{Content: content, Components: components},
	})
}

func handleSlowmode(s *discordgo.Session, i *discordgo.InteractionCreate) {
	seconds := 0
	for _, opt := range i.ApplicationCommandData().Options {
		if opt.Name == "seconds" {
			seconds = int(opt.IntValue())
		}
	}

	edit := discordgo.ChannelEdit{RateLimitPerUser: &seconds}
	_, err := s.ChannelEdit(i.ChannelID, &edit)

	msg := fmt.Sprintf("🐌 Slowmode set to %d seconds.", seconds)
	if seconds == 0 {
		msg = "🏃 Slowmode disabled."
	}
	if err != nil {
		msg = "❌ Failed to set slowmode: " + err.Error()
	}

	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Content: msg},
	})
}

func handleChannel(s *discordgo.Session, i *discordgo.InteractionCreate) {
	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	})

	options := i.ApplicationCommandData().Options
	if len(options) == 0 {
		return
	}

	subCommand := options[0]

	switch subCommand.Name {
	case "create":
		var cType, cName, parentID string

		for _, opt := range subCommand.Options {
			switch opt.Name {
			case "type":
				cType = opt.StringValue()
			case "name":
				cName = opt.StringValue()
			case "in":
				parentID = opt.ChannelValue(s).ID
			}
		}

		var dType discordgo.ChannelType
		switch cType {
		case "text":
			dType = discordgo.ChannelTypeGuildText
		case "voice":
			dType = discordgo.ChannelTypeGuildVoice
		case "category":
			dType = discordgo.ChannelTypeGuildCategory
			parentID = "" // Categories cannot have parent categories
		}

		ch, err := s.GuildChannelCreateComplex(i.GuildID, discordgo.GuildChannelCreateData{
			Name:     cName,
			Type:     dType,
			ParentID: parentID,
		})
		if err != nil {
			_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{Content: "❌ Failed to create channel: " + err.Error()})
			return
		}
		_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{Content: fmt.Sprintf("✅ Created %s channel: <#%s>", cType, ch.ID)})

	case "delete":
		var targetID string
		for _, opt := range subCommand.Options {
			if opt.Name == "target" {
				targetID = opt.ChannelValue(s).ID
			}
		}

		_, err := s.ChannelDelete(targetID)
		if err != nil {
			_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{Content: "❌ Failed to delete channel: " + err.Error()})
			return
		}
		_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{Content: "✅ Channel deleted successfully."})

	case "rename":
		var targetID, newName string
		for _, opt := range subCommand.Options {
			switch opt.Name {
			case "target":
				targetID = opt.ChannelValue(s).ID
			case "name":
				newName = opt.StringValue()
			}
		}

		ch, err := s.ChannelEdit(targetID, &discordgo.ChannelEdit{Name: newName})
		if err != nil {
			_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{Content: "❌ Failed to rename channel: " + err.Error()})
			return
		}
		_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{Content: fmt.Sprintf("✅ Channel renamed to **%s** (<#%s>).", ch.Name, ch.ID)})

	case "lock", "unlock":
		var channelID string
		for _, opt := range subCommand.Options {
			if opt.Name == "channel" {
				channel := opt.ChannelValue(s)
				if channel != nil {
					channelID = channel.ID
				}
			}
		}
		if channelID == "" {
			_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{Content: "❌ A valid channel is required."})
			return
		}
		lock := subCommand.Name == "lock"
		allow, deny := int64(discordgo.PermissionSendMessages), int64(0)
		action := "unlocked"
		if lock {
			allow, deny = 0, discordgo.PermissionSendMessages
			action = "locked"
		}
		if err := s.ChannelPermissionSet(channelID, i.GuildID, discordgo.PermissionOverwriteTypeRole, allow, deny); err != nil {
			_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{Content: fmt.Sprintf("❌ Failed to %s channel: %s", action, err.Error())})
			return
		}
		_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{Content: fmt.Sprintf("✅ Channel <#%s> %s.", channelID, action)})
	}
}
