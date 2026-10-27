// Package main — birthdays.go handles user birthday registration,
// viewing server birthdays, and a daily background daemon that announces
// member birthdays in the designated channel from config.json.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
)

var (
	birthdayFilePath = dataPath("birthdays.json")
	birthdayMu       sync.Mutex
)

// BirthdayRecord stores a user's birthday data.
type BirthdayRecord struct {
	Month             int    `json:"month"`
	Day               int    `json:"day"`
	CreatedAt         string `json:"createdAt,omitempty"`
	UpdatedAt         string `json:"updatedAt,omitempty"`
	LastAnnouncedYear int    `json:"lastAnnouncedYear,omitempty"`
}

// GuildBirthdayStore holds birthdays per guild.
type GuildBirthdayStore struct {
	Users map[string]BirthdayRecord `json:"users"`
}

func loadAllBirthdays() map[string]*GuildBirthdayStore {
	birthdayMu.Lock()
	defer birthdayMu.Unlock()

	data, err := os.ReadFile(birthdayFilePath)
	if err != nil {
		return make(map[string]*GuildBirthdayStore)
	}

	var raw map[string]*GuildBirthdayStore
	if err := json.Unmarshal(data, &raw); err != nil {
		return make(map[string]*GuildBirthdayStore)
	}
	if raw == nil {
		raw = make(map[string]*GuildBirthdayStore)
	}
	for _, g := range raw {
		if g.Users == nil {
			g.Users = make(map[string]BirthdayRecord)
		}
	}
	return raw
}

func saveAllBirthdays(data map[string]*GuildBirthdayStore) error {
	birthdayMu.Lock()
	defer birthdayMu.Unlock()

	_ = os.MkdirAll(filepath.Dir(birthdayFilePath), 0755)
	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(birthdayFilePath, b, 0644)
}

func getBirthday(guildID, userID string) *BirthdayRecord {
	all := loadAllBirthdays()
	g, ok := all[guildID]
	if !ok || g.Users == nil {
		return nil
	}
	rec, exists := g.Users[userID]
	if !exists {
		return nil
	}
	return &rec
}

func setBirthdayOnce(guildID, userID string, month, day int) bool {
	all := loadAllBirthdays()
	g, ok := all[guildID]
	if !ok {
		g = &GuildBirthdayStore{Users: make(map[string]BirthdayRecord)}
		all[guildID] = g
	}
	if g.Users == nil {
		g.Users = make(map[string]BirthdayRecord)
	}
	if _, exists := g.Users[userID]; exists {
		return false
	}

	now := time.Now().UTC().Format(time.RFC3339)
	g.Users[userID] = BirthdayRecord{
		Month:     month,
		Day:       day,
		CreatedAt: now,
		UpdatedAt: now,
	}
	_ = saveAllBirthdays(all)
	return true
}

type BirthdayUserEntry struct {
	UserID string
	Record BirthdayRecord
}

func getBirthdaysByMonth(guildID string, month int) []BirthdayUserEntry {
	all := loadAllBirthdays()
	g, ok := all[guildID]
	if !ok || g.Users == nil {
		return nil
	}

	var results []BirthdayUserEntry
	for uID, r := range g.Users {
		if r.Month == month {
			results = append(results, BirthdayUserEntry{UserID: uID, Record: r})
		}
	}
	sort.Slice(results, func(i, j int) bool {
		return results[i].Record.Day < results[j].Record.Day
	})
	return results
}

func getAllBirthdays(guildID string) []BirthdayUserEntry {
	all := loadAllBirthdays()
	g, ok := all[guildID]
	if !ok || g.Users == nil {
		return nil
	}

	var results []BirthdayUserEntry
	for uID, r := range g.Users {
		results = append(results, BirthdayUserEntry{UserID: uID, Record: r})
	}
	sort.Slice(results, func(i, j int) bool {
		if results[i].Record.Month != results[j].Record.Month {
			return results[i].Record.Month < results[j].Record.Month
		}
		return results[i].Record.Day < results[j].Record.Day
	})
	return results
}

func markBirthdayAnnounced(guildID, userID string, year int) {
	all := loadAllBirthdays()
	g, ok := all[guildID]
	if !ok || g.Users == nil {
		return
	}
	rec, exists := g.Users[userID]
	if !exists {
		return
	}
	rec.LastAnnouncedYear = year
	g.Users[userID] = rec
	_ = saveAllBirthdays(all)
}

func isValidBirthday(month, day int) bool {
	if month < 1 || month > 12 {
		return false
	}
	maxDays := 31
	switch month {
	case 2:
		maxDays = 29
	case 4, 6, 9, 11:
		maxDays = 30
	}
	return day >= 1 && day <= maxDays
}

func handleBirthday(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if i.GuildID == "" {
		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "❌ This command can only be used in a server.",
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
		return
	}

	options := i.ApplicationCommandData().Options
	if len(options) == 0 {
		return
	}

	sub := options[0]
	switch sub.Name {
	case "set":
		var month, day int
		for _, opt := range sub.Options {
			if opt.Name == "month" {
				month = int(opt.IntValue())
			} else if opt.Name == "day" {
				day = int(opt.IntValue())
			}
		}

		if !isValidBirthday(month, day) {
			_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content: "❌ That is not a valid date. Please check the month and day and try again.",
					Flags:   discordgo.MessageFlagsEphemeral,
				},
			})
			return
		}

		invokerID := i.Member.User.ID
		existing := getBirthday(i.GuildID, invokerID)
		if existing != nil {
			_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content: fmt.Sprintf("ℹ️ Your birthday is already set to %02d/%02d and cannot be edited.", existing.Month, existing.Day),
					Flags:   discordgo.MessageFlagsEphemeral,
				},
			})
			return
		}

		if !setBirthdayOnce(i.GuildID, invokerID, month, day) {
			_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content: "❌ Your birthday is already set and cannot be edited.",
					Flags:   discordgo.MessageFlagsEphemeral,
				},
			})
			return
		}

		cfg := getGuildSettings(i.GuildID)
		channelNote := ""
		if cfg.BirthdayAnnouncementChannelID != "" {
			channelNote = fmt.Sprintf(" Birthday announcements are enabled in <#%s>.", cfg.BirthdayAnnouncementChannelID)
		} else {
			channelNote = " An administrator can configure announcements in `config.json`."
		}

		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: fmt.Sprintf("🎉 Saved your birthday as **%02d/%02d**. This value is locked and cannot be changed.%s", month, day, channelNote),
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})

	case "view":
		var monthFilter int
		for _, opt := range sub.Options {
			if opt.Name == "month" {
				monthFilter = int(opt.IntValue())
			}
		}

		var entries []BirthdayUserEntry
		if monthFilter > 0 {
			entries = getBirthdaysByMonth(i.GuildID, monthFilter)
		} else {
			entries = getAllBirthdays(i.GuildID)
		}

		if len(entries) == 0 {
			emptyMsg := "No birthdays are registered in this server."
			if monthFilter > 0 {
				emptyMsg = fmt.Sprintf("No birthdays are registered for month %02d.", monthFilter)
			}
			_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content: emptyMsg,
					Flags:   discordgo.MessageFlagsEphemeral,
				},
			})
			return
		}

		header := fmt.Sprintf("📅 **Registered birthdays in this server (%d):**\n", len(entries))
		if monthFilter > 0 {
			header = fmt.Sprintf("📅 **Registered birthdays for month %02d (%d):**\n", monthFilter, len(entries))
		}

		var lines []string
		for idx, entry := range entries {
			lines = append(lines, fmt.Sprintf("%d. `%02d/%02d` — <@%s>", idx+1, entry.Record.Month, entry.Record.Day, entry.UserID))
		}

		msg := header
		for _, l := range lines {
			if len(msg)+len(l)+2 > 1900 {
				break
			}
			msg += l + "\n"
		}

		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: msg,
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
	}
}

// Background notifier daemon: checks daily for server birthdays and announces them
func startBirthdayNotifierDaemon(ctx context.Context, s *discordgo.Session) {
	log.Println("birthday notifier daemon started (interval 1h)")
	runPeriodic(ctx, 0, time.Hour, func() { runBirthdayCheck(s) })
}

func runBirthdayCheck(s *discordgo.Session) {
	now := time.Now().UTC()
	todayYear := now.Year()
	todayMonth := int(now.Month())
	todayDay := now.Day()

	cfg := getGuildSettings(SunfireServerID)
	if cfg.BirthdayAnnouncementChannelID != "" {
		channelID := cfg.BirthdayAnnouncementChannelID
		entries := getBirthdaysByMonth(SunfireServerID, todayMonth)

		var todayBirthdays []BirthdayUserEntry
		for _, entry := range entries {
			if entry.Record.Day == todayDay && entry.Record.LastAnnouncedYear != todayYear {
				todayBirthdays = append(todayBirthdays, entry)
			}
		}

		if len(todayBirthdays) == 0 {
			return
		}

		mentions := ""
		for idx, entry := range todayBirthdays {
			if idx > 0 {
				mentions += ", "
			}
			mentions += fmt.Sprintf("<@%s>", entry.UserID)
		}

		msg := fmt.Sprintf("🎂 Happy birthday %s! Have an amazing day! 🎉🥳", mentions)
		if len(todayBirthdays) > 1 {
			msg = fmt.Sprintf("🎂 Happy birthdays %s! Have an amazing day! 🎉🥳", mentions)
		}

		_, err := s.ChannelMessageSend(channelID, msg)
		if err == nil {
			for _, entry := range todayBirthdays {
				markBirthdayAnnounced(SunfireServerID, entry.UserID, todayYear)
			}
			log.Printf("[birthday] Announced birthdays in guild %s channel %s", SunfireServerID, channelID)
		} else {
			log.Printf("[birthday] Failed to announce in channel %s: %v", channelID, err)
		}
	}
}
