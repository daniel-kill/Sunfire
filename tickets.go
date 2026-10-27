package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
)

type TicketUserEntry struct {
	ThreadID  string    `json:"threadId"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type TicketGuildStore struct {
	Users map[string]TicketUserEntry `json:"users"`
}

var (
	ticketsMu        sync.Mutex
	ticketsStore     map[string]TicketGuildStore
	ticketsStorePath string
)

func initTicketsStore() {
	ticketsStorePath = dataPath("tickets.json")
	ticketsStore = make(map[string]TicketGuildStore)

	data, err := os.ReadFile(ticketsStorePath)
	if err == nil {
		_ = json.Unmarshal(data, &ticketsStore)
	}
}

func saveTicketsStore() {
	if ticketsStorePath == "" {
		return
	}
	data, err := json.MarshalIndent(ticketsStore, "", "  ")
	if err == nil {
		_ = os.WriteFile(ticketsStorePath, data, 0644)
	}
}

func handleTicketPanelCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	btn := discordgo.Button{
		CustomID: "ticket:create",
		Label:    "Create Ticket",
		Style:    discordgo.PrimaryButton,
		Emoji:    &discordgo.ComponentEmoji{Name: "🎫"},
	}

	row := discordgo.ActionsRow{
		Components: []discordgo.MessageComponent{btn},
	}

	_, err := s.ChannelMessageSendComplex(i.ChannelID, &discordgo.MessageSend{
		Content:    "**Need staff assistance?**\nClick below to open a private support ticket thread with our team.",
		Components: []discordgo.MessageComponent{row},
	})

	msg := "✅ Ticket creation panel posted!"
	if err != nil {
		msg = "❌ Failed to post panel: " + err.Error()
	}

	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Content: msg, Flags: discordgo.MessageFlagsEphemeral},
	})
}

func handleCloseTicketCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	initTicketsStore()
	ticketsMu.Lock()
	defer ticketsMu.Unlock()

	ch, err := s.Channel(i.ChannelID)
	if err != nil || (!ch.IsThread()) {
		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "❌ This command can only be used inside a ticket thread.",
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
		return
	}

	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: fmt.Sprintf("🔒 Ticket closed by <@%s>. Archiving thread...", i.Member.User.ID),
		},
	})

	// Archive the thread
	archived := true
	locked := true
	_, _ = s.ChannelEdit(i.ChannelID, &discordgo.ChannelEdit{
		Archived: &archived,
		Locked:   &locked,
	})
}
