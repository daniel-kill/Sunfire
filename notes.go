package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
)

type NoteRecord struct {
	NoteID       int       `json:"noteId"`
	ModeratorID  string    `json:"moderatorId"`
	ModeratorTag string    `json:"moderatorTag"`
	Text         string    `json:"text"`
	Timestamp    time.Time `json:"timestamp"`
}

type GuildNotesBucket struct {
	Meta struct {
		NextNoteID int `json:"nextNoteId"`
	} `json:"_meta"`
	Users map[string][]NoteRecord `json:"users"`
}

var (
	notesMu        sync.Mutex
	notesStore     map[string]*GuildNotesBucket
	notesStorePath string
)

func initNotesStore() {
	notesStorePath = dataPath("notes.json")
	notesStore = make(map[string]*GuildNotesBucket)

	data, err := os.ReadFile(notesStorePath)
	if err == nil {
		_ = json.Unmarshal(data, &notesStore)
	}
}

func saveNotesStore() {
	if notesStorePath == "" {
		return
	}
	data, err := json.MarshalIndent(notesStore, "", "  ")
	if err == nil {
		_ = os.WriteFile(notesStorePath, data, 0644)
	}
}

func getGuildNotesBucket(guildID string) *GuildNotesBucket {
	if b, ok := notesStore[guildID]; ok && b != nil {
		if b.Users == nil {
			b.Users = make(map[string][]NoteRecord)
		}
		if b.Meta.NextNoteID <= 0 {
			b.Meta.NextNoteID = 1
		}
		return b
	}

	b := &GuildNotesBucket{
		Users: make(map[string][]NoteRecord),
	}
	b.Meta.NextNoteID = 1
	notesStore[guildID] = b
	return b
}

func handleNoteCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	initNotesStore()
	notesMu.Lock()
	defer notesMu.Unlock()

	options := i.ApplicationCommandData().Options
	if len(options) == 0 {
		return
	}

	sub := options[0]
	bucket := getGuildNotesBucket(i.GuildID)

	var targetUser *discordgo.User
	for _, opt := range sub.Options {
		if opt.Name == "user" {
			targetUser = opt.UserValue(s)
		}
	}

	if targetUser == nil {
		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{Content: "❌ Target user is required.", Flags: discordgo.MessageFlagsEphemeral},
		})
		return
	}

	invoker := i.Member.User

	switch sub.Name {
	case "add":
		var text string
		for _, opt := range sub.Options {
			if opt.Name == "text" {
				text = opt.StringValue()
			}
		}

		noteID := bucket.Meta.NextNoteID
		bucket.Meta.NextNoteID++

		rec := NoteRecord{
			NoteID:       noteID,
			ModeratorID:  invoker.ID,
			ModeratorTag: invoker.Username,
			Text:         text,
			Timestamp:    time.Now(),
		}

		bucket.Users[targetUser.ID] = append(bucket.Users[targetUser.ID], rec)
		saveNotesStore()

		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: fmt.Sprintf("✅ Added note **#%d** for **%s**.", noteID, targetUser.Username),
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})

	case "view":
		limit := 10
		for _, opt := range sub.Options {
			if opt.Name == "limit" {
				limit = int(opt.IntValue())
			}
		}

		userNotes := bucket.Users[targetUser.ID]
		if len(userNotes) == 0 {
			_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content: fmt.Sprintf("No notes on record for **%s**.", targetUser.Username),
					Flags:   discordgo.MessageFlagsEphemeral,
				},
			})
			return
		}

		start := 0
		if len(userNotes) > limit {
			start = len(userNotes) - limit
		}
		slice := userNotes[start:]

		embed := &discordgo.MessageEmbed{
			Title:       fmt.Sprintf("Staff Notes — %s", targetUser.Username),
			Description: fmt.Sprintf("Total notes: **%d** (showing latest %d)", len(userNotes), len(slice)),
			Color:       0x9B59B6,
		}

		for _, n := range slice {
			embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
				Name:   fmt.Sprintf("Note #%d • By %s (%s)", n.NoteID, n.ModeratorTag, n.Timestamp.Format("2006-01-02 15:04")),
				Value:  n.Text,
				Inline: false,
			})
		}

		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Embeds: []*discordgo.MessageEmbed{embed},
				Flags:  discordgo.MessageFlagsEphemeral,
			},
		})

	case "delete":
		noteID := 0
		for _, opt := range sub.Options {
			if opt.Name == "noteid" {
				noteID = int(opt.IntValue())
			}
		}

		userNotes := bucket.Users[targetUser.ID]
		idx := -1
		for index, n := range userNotes {
			if n.NoteID == noteID {
				idx = index
				break
			}
		}

		if idx == -1 {
			_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content: fmt.Sprintf("❌ Note #%d not found for %s.", noteID, targetUser.Username),
					Flags:   discordgo.MessageFlagsEphemeral,
				},
			})
			return
		}

		bucket.Users[targetUser.ID] = append(userNotes[:idx], userNotes[idx+1:]...)
		saveNotesStore()

		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: fmt.Sprintf("✅ Deleted note #%d for %s.", noteID, targetUser.Username),
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
	}
}
