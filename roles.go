package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
)

type RoleMenuOption struct {
	Label       string `json:"label"`
	Value       string `json:"value"`
	Description string `json:"description"`
}

type RoleMenuData struct {
	ChannelID string           `json:"channelId"`
	Options   []RoleMenuOption `json:"options"`
	UpdatedAt time.Time        `json:"updatedAt"`
}

var (
	roleMenusMu        sync.Mutex
	roleMenusStore     map[string]map[string]RoleMenuData // guildID -> messageID -> data
	roleMenusStorePath string
)

func initRoleMenusStore() {
	roleMenusStorePath = dataPath("roleMenus.json")
	roleMenusStore = make(map[string]map[string]RoleMenuData)

	data, err := os.ReadFile(roleMenusStorePath)
	if err == nil {
		_ = json.Unmarshal(data, &roleMenusStore)
	}
}

func saveRoleMenusStore() {
	if roleMenusStorePath == "" {
		return
	}
	data, err := json.MarshalIndent(roleMenusStore, "", "  ")
	if err == nil {
		_ = os.WriteFile(roleMenusStorePath, data, 0644)
	}
}

func parseRoleMenuPairs(text string, guild *discordgo.Guild) []RoleMenuOption {
	items := strings.Split(text, ";")
	var options []RoleMenuOption

	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}

		parts := strings.Split(item, ":")
		if len(parts) != 2 {
			continue
		}

		label := strings.TrimSpace(parts[0])
		roleIdentifier := strings.TrimSpace(parts[1])

		var targetRole *discordgo.Role
		for _, r := range guild.Roles {
			if r.ID == roleIdentifier || strings.EqualFold(r.Name, roleIdentifier) || fmt.Sprintf("<@&%s>", r.ID) == roleIdentifier {
				targetRole = r
				break
			}
		}

		if targetRole != nil {
			options = append(options, RoleMenuOption{
				Label:       label,
				Value:       targetRole.ID,
				Description: fmt.Sprintf("Toggle @%s", targetRole.Name),
			})
			if len(options) >= 25 {
				break
			}
		}
	}

	return options
}

func handleRoleCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	options := i.ApplicationCommandData().Options
	if len(options) == 0 {
		return
	}

	sub := options[0]
	var targetUser *discordgo.User
	var targetRole *discordgo.Role
	reason := "Managed via /role command"

	for _, opt := range sub.Options {
		switch opt.Name {
		case "user":
			targetUser = opt.UserValue(s)
		case "role":
			targetRole = opt.RoleValue(s, i.GuildID)
		case "reason":
			reason = opt.StringValue()
		}
	}

	if targetUser == nil || targetRole == nil {
		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{Content: "❌ Both user and role are required.", Flags: discordgo.MessageFlagsEphemeral},
		})
		return
	}

	switch sub.Name {
	case "add":
		err := s.GuildMemberRoleAdd(i.GuildID, targetUser.ID, targetRole.ID)
		if err != nil {
			_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{Content: "❌ Failed to add role: " + err.Error(), Flags: discordgo.MessageFlagsEphemeral},
			})
			return
		}
		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{Content: fmt.Sprintf("✅ Added <@&%s> to **%s**. Reason: %s", targetRole.ID, targetUser.Username, reason)},
		})

	case "remove":
		err := s.GuildMemberRoleRemove(i.GuildID, targetUser.ID, targetRole.ID)
		if err != nil {
			_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{Content: "❌ Failed to remove role: " + err.Error(), Flags: discordgo.MessageFlagsEphemeral},
			})
			return
		}
		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{Content: fmt.Sprintf("✅ Removed <@&%s> from **%s**. Reason: %s", targetRole.ID, targetUser.Username, reason)},
		})
	}
}

func handleRoleMenuCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	initRoleMenusStore()
	options := i.ApplicationCommandData().Options
	var title, pairs string

	for _, opt := range options {
		switch opt.Name {
		case "title":
			title = opt.StringValue()
		case "pairs":
			pairs = opt.StringValue()
		}
	}

	guild, err := s.Guild(i.GuildID)
	if err != nil {
		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{Content: "❌ Failed to load guild information."},
		})
		return
	}

	parsed := parseRoleMenuPairs(pairs, guild)
	if len(parsed) == 0 {
		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "❌ No valid roles found. Format: `Label:RoleName; Label:@Role`",
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
		return
	}

	var menuOptions []discordgo.SelectMenuOption
	for _, p := range parsed {
		menuOptions = append(menuOptions, discordgo.SelectMenuOption{
			Label:       p.Label,
			Value:       p.Value,
			Description: p.Description,
		})
	}

	selectMenu := discordgo.SelectMenu{
		CustomID:    "rolemenu:select",
		Placeholder: "Choose your roles...",
		MinValues:   &[]int{0}[0],
		MaxValues:   len(menuOptions),
		Options:     menuOptions,
	}

	row := discordgo.ActionsRow{
		Components: []discordgo.MessageComponent{selectMenu},
	}

	msg, err := s.ChannelMessageSendComplex(i.ChannelID, &discordgo.MessageSend{
		Content:    fmt.Sprintf("**%s**\nSelect your roles from the dropdown below:", title),
		Components: []discordgo.MessageComponent{row},
	})

	if err != nil {
		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{Content: "❌ Failed to send role menu: " + err.Error(), Flags: discordgo.MessageFlagsEphemeral},
		})
		return
	}

	roleMenusMu.Lock()
	if roleMenusStore[i.GuildID] == nil {
		roleMenusStore[i.GuildID] = make(map[string]RoleMenuData)
	}
	roleMenusStore[i.GuildID][msg.ID] = RoleMenuData{
		ChannelID: i.ChannelID,
		Options:   parsed,
		UpdatedAt: time.Now(),
	}
	saveRoleMenusStore()
	roleMenusMu.Unlock()

	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Content: "✅ Role menu created successfully!", Flags: discordgo.MessageFlagsEphemeral},
	})
}

func handleAutoRoleCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	options := i.ApplicationCommandData().Options
	if len(options) == 0 {
		return
	}

	sub := options[0]
	cfg := getGuildSettings(i.GuildID)

	switch sub.Name {
	case "view":
		content := "Auto-role is currently **disabled**."
		if cfg.AutoRoleID != nil && *cfg.AutoRoleID != "" {
			content = fmt.Sprintf("Current auto-role: <@&%s>", *cfg.AutoRoleID)
		}
		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{Content: content, Flags: discordgo.MessageFlagsEphemeral},
		})

	case "set":
		var role *discordgo.Role
		for _, opt := range sub.Options {
			if opt.Name == "role" {
				role = opt.RoleValue(s, i.GuildID)
			}
		}
		if role == nil {
			_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{Content: "❌ Role is required.", Flags: discordgo.MessageFlagsEphemeral},
			})
			return
		}

		cfg.AutoRoleID = &role.ID
		_ = saveGuildSettings()

		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{Content: fmt.Sprintf("✅ Auto-role set to <@&%s>.", role.ID)},
		})

	case "clear":
		cfg.AutoRoleID = nil
		_ = saveGuildSettings()

		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{Content: "✅ Auto-role disabled."},
		})
	}
}
