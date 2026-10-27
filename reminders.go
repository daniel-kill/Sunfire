package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/bwmarrin/discordgo"
)

type Reminder struct {
	ID        string    `json:"id"`
	UserID    string    `json:"user_id"`
	ChannelID string    `json:"channel_id"`
	Message   string    `json:"message"`
	FireTime  time.Time `json:"fire_time"`
	Repeat    string    `json:"repeat,omitempty"`
}

const defaultReminderTimezone = "America/New_York"

var (
	reminders   []Reminder
	remindersMu sync.Mutex
)

func getRemindersFile() string {
	return dataPath("Reminders", "reminders.json")
}

func loadReminders() error {
	remindersMu.Lock()
	defer remindersMu.Unlock()

	data, err := os.ReadFile(getRemindersFile())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			reminders = nil
			return nil
		}
		return fmt.Errorf("reading reminders: %w", err)
	}
	var loaded []Reminder
	if err := json.Unmarshal(data, &loaded); err != nil {
		return fmt.Errorf("decoding reminders: %w", err)
	}
	for _, reminder := range loaded {
		if reminder.Repeat != "" {
			if _, err := parseRepeatInterval(reminder.Repeat); err != nil {
				return fmt.Errorf("reminder %s has an invalid repeat interval: %w", reminder.ID, err)
			}
		}
	}
	reminders = loaded
	return nil
}

func saveReminders() error {
	// Assumes caller already holds the mutex
	data, err := json.MarshalIndent(reminders, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding reminders: %w", err)
	}
	path := getRemindersFile()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("creating reminders directory: %w", err)
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".reminders-*.json")
	if err != nil {
		return fmt.Errorf("creating temporary reminders file: %w", err)
	}
	tempPath := file.Name()
	defer os.Remove(tempPath)
	if err := file.Chmod(0644); err != nil {
		file.Close()
		return fmt.Errorf("setting reminders file permissions: %w", err)
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return fmt.Errorf("writing temporary reminders file: %w", err)
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return fmt.Errorf("syncing temporary reminders file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("closing temporary reminders file: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("writing reminders: %w", err)
	}
	return nil
}

func handleRemind(s *discordgo.Session, i *discordgo.InteractionCreate) {
	options := i.ApplicationCommandData().Options
	if len(options) == 0 {
		respondRemind(s, i, "❌ Choose `add`, `list`, or `delete`.")
		return
	}

	userID := reminderUserID(i)
	if userID == "" {
		respondRemind(s, i, "❌ Could not identify the reminder owner.")
		return
	}

	subcommand := options[0]
	switch subcommand.Name {
	case "add":
		var timeStr, message, timezone, repeat string
		for _, opt := range subcommand.Options {
			switch opt.Name {
			case "when":
				timeStr = opt.StringValue()
			case "message":
				message = opt.StringValue()
			case "timezone":
				timezone = opt.StringValue()
			case "repeat":
				repeat = opt.StringValue()
			}
		}
		message = strings.TrimSpace(message)
		if message == "" {
			respondRemind(s, i, "❌ Reminder message cannot be empty.")
			return
		}
		if utf8.RuneCountInString(message) > 1800 {
			respondRemind(s, i, "❌ Reminder messages must be 1,800 characters or fewer.")
			return
		}
		fireTime, err := parseReminderTimeInLocation(timeStr, timezone)
		if err != nil {
			respondRemind(s, i, "❌ "+err.Error())
			return
		}
		if !fireTime.After(time.Now()) {
			respondRemind(s, i, "❌ You cannot set a reminder in the past!")
			return
		}
		repeat = strings.TrimSpace(repeat)
		if repeat != "" {
			if _, err := parseRepeatInterval(repeat); err != nil {
				respondRemind(s, i, "❌ "+err.Error())
				return
			}
		}

		reminder := Reminder{
			UserID:    userID,
			ChannelID: i.ChannelID,
			Message:   message,
			FireTime:  fireTime,
			Repeat:    repeat,
		}
		remindersMu.Lock()
		reminder.ID = newReminderID(reminders, time.Now())
		reminders = append(reminders, reminder)
		if err := saveReminders(); err != nil {
			reminders = reminders[:len(reminders)-1]
			remindersMu.Unlock()
			log.Printf("failed to save reminder %s for user %s: %v", reminder.ID, userID, err)
			respondRemind(s, i, "❌ Could not save your reminder; please try again later.")
			return
		}
		remindersMu.Unlock()

		repeatText := ""
		if reminder.Repeat != "" {
			repeatText = fmt.Sprintf(" (repeats every %s)", reminder.Repeat)
		}
		respondRemind(s, i, fmt.Sprintf("✅ Got it! I will remind you <t:%d:R>%s:\n> %s", fireTime.Unix(), repeatText, message))

	case "list":
		remindersMu.Lock()
		userReminders := make([]Reminder, 0)
		for _, reminder := range reminders {
			if reminder.UserID == userID {
				userReminders = append(userReminders, reminder)
			}
		}
		remindersMu.Unlock()
		sort.Slice(userReminders, func(i, j int) bool {
			return userReminders[i].FireTime.Before(userReminders[j].FireTime)
		})
		respondRemind(s, i, formatReminderList(userReminders))

	case "delete":
		reminderID := ""
		for _, opt := range subcommand.Options {
			if opt.Name == "id" {
				reminderID = opt.StringValue()
			}
		}
		remindersMu.Lock()
		index := -1
		for idx, reminder := range reminders {
			if reminder.ID == reminderID && reminder.UserID == userID {
				index = idx
				break
			}
		}
		if index == -1 {
			remindersMu.Unlock()
			respondRemind(s, i, "❌ No reminder with that ID belongs to you. Use `/remind list` to find an ID.")
			return
		}
		removed := reminders[index]
		reminders = append(reminders[:index], reminders[index+1:]...)
		if err := saveReminders(); err != nil {
			reminders = append(reminders, Reminder{})
			copy(reminders[index+1:], reminders[index:])
			reminders[index] = removed
			remindersMu.Unlock()
			log.Printf("failed to delete reminder %s for user %s: %v", removed.ID, userID, err)
			respondRemind(s, i, "❌ Could not delete your reminder; please try again later.")
			return
		}
		remindersMu.Unlock()
		respondRemind(s, i, fmt.Sprintf("✅ Deleted reminder `%s`.", reminderID))

	default:
		respondRemind(s, i, "❌ Choose `add`, `list`, or `delete`.")
	}
}

func parseReminderTime(timeStr string) (time.Time, error) {
	return parseReminderTimeInLocation(timeStr, defaultReminderTimezone)
}

func parseReminderTimeInLocation(timeStr, timezone string) (time.Time, error) {
	timeStr = strings.TrimSpace(timeStr)
	if timeStr == "" {
		return time.Time{}, fmt.Errorf("enter when to be reminded, such as `10m` or `09/29/2026 at 14:30`")
	}
	timezone = strings.TrimSpace(timezone)
	if timezone == "" {
		timezone = defaultReminderTimezone
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return time.Time{}, fmt.Errorf("unknown timezone %q; use an IANA timezone such as `America/Los_Angeles` or `UTC`", timezone)
	}
	return parseReminderTimeAt(timeStr, location, time.Now())
}

func parseReminderTimeAt(timeStr string, location *time.Location, now time.Time) (time.Time, error) {
	timeStr = strings.TrimSpace(timeStr)
	relative := strings.TrimSpace(timeStr)
	if strings.HasPrefix(strings.ToLower(relative), "in ") {
		relative = strings.TrimSpace(relative[3:])
	}
	if duration, err := time.ParseDuration(relative); err == nil {
		if duration <= 0 {
			return time.Time{}, fmt.Errorf("relative reminder times must be greater than zero")
		}
		return now.Add(duration), nil
	}
	if parsedTime, err := time.Parse(time.RFC3339, timeStr); err == nil {
		return parsedTime, nil
	}
	for _, layout := range []string{"01/02/2006 at 15:04", "2006-01-02 15:04"} {
		if parsedTime, err := time.ParseInLocation(layout, timeStr, location); err == nil {
			return parsedTime, nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid time format. Use a relative interval such as `10m` or `1h30m`, a local time like `MM/DD/YYYY at HH:MM`, or an ISO 8601 time such as `2026-09-29T14:30:00-04:00`")
}

func parseRepeatInterval(value string) (time.Duration, error) {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return 0, nil
	}
	if duration, err := time.ParseDuration(value); err == nil {
		if duration > 0 {
			return duration, nil
		}
		return 0, fmt.Errorf("repeat interval must be greater than zero, for example `1h`, `1d`, or `1w`")
	}
	for _, suffix := range []struct {
		suffix string
		unit   time.Duration
	}{{"d", 24 * time.Hour}, {"w", 7 * 24 * time.Hour}} {
		if strings.HasSuffix(value, suffix.suffix) {
			count, err := strconv.ParseInt(strings.TrimSuffix(value, suffix.suffix), 10, 64)
			if err != nil || count <= 0 || count > math.MaxInt64/int64(suffix.unit) {
				break
			}
			return time.Duration(count) * suffix.unit, nil
		}
	}
	return 0, fmt.Errorf("invalid repeat interval. Use a positive duration such as `30m`, `1h`, `1d`, or `1w`")
}

func newReminderID(existing []Reminder, now time.Time) string {
	id := now.Format("20060102150405.000000000")
	for _, reminder := range existing {
		if reminder.ID == id {
			return newReminderID(existing, now.Add(time.Nanosecond))
		}
	}
	return id
}

func reminderUserID(i *discordgo.InteractionCreate) string {
	if i.Member != nil && i.Member.User != nil {
		return i.Member.User.ID
	}
	if i.User != nil {
		return i.User.ID
	}
	return ""
}

func formatReminderList(userReminders []Reminder) string {
	if len(userReminders) == 0 {
		return "You have no scheduled reminders."
	}
	var content strings.Builder
	content.WriteString("**Your reminders**\n")
	for index, reminder := range userReminders {
		repeat := ""
		if reminder.Repeat != "" {
			repeat = fmt.Sprintf(" (every %s)", reminder.Repeat)
		}
		line := fmt.Sprintf("• `%s` · <t:%d:R>%s — %s\n", reminder.ID, reminder.FireTime.Unix(), repeat, reminder.Message)
		if content.Len()+len(line) > 1800 {
			fmt.Fprintf(&content, "…and %d more. Use `/remind delete` with an ID to remove one.\n", len(userReminders)-index)
			break
		}
		content.WriteString(line)
	}
	return content.String()
}

func respondRemind(s *discordgo.Session, i *discordgo.InteractionCreate, content string) {
	if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: content,
			Flags:   discordgo.MessageFlagsEphemeral,
		},
	}); err != nil {
		log.Printf("failed to respond to /remind interaction: %v", err)
	}
}

// startReminderDaemon runs forever in the background, checking for due reminders.
func startReminderDaemon(ctx context.Context, s *discordgo.Session) {
	if err := loadReminders(); err != nil {
		log.Printf("failed to load reminders: %v", err)
		return
	}
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		remindersMu.Lock()

		now := time.Now()
		pending := make([]Reminder, 0, len(reminders))
		changed := false

		for _, r := range reminders {
			if now.Before(r.FireTime) {
				pending = append(pending, r)
				continue
			}
			if err := deliverReminder(s, r); err != nil {
				log.Printf("failed to deliver reminder %s for user %s: %v", r.ID, r.UserID, err)
				pending = append(pending, r)
				continue
			}
			if r.Repeat == "" {
				changed = true
				continue
			}
			interval, err := parseRepeatInterval(r.Repeat)
			if err != nil {
				log.Printf("cannot repeat reminder %s: %v", r.ID, err)
				pending = append(pending, r)
				continue
			}
			r.FireTime = nextReminderTime(r.FireTime, interval, now)
			pending = append(pending, r)
			changed = true
		}

		if changed {
			previous := reminders
			reminders = pending
			if err := saveReminders(); err != nil {
				reminders = previous
				log.Printf("failed to save reminders after delivery: %v", err)
			}
		}

		remindersMu.Unlock()
	}
}

func deliverReminder(s *discordgo.Session, reminder Reminder) error {
	dmCh, err := s.UserChannelCreate(reminder.UserID)
	if err == nil {
		_, err = s.ChannelMessageSend(dmCh.ID, fmt.Sprintf("⏰ **REMINDER:** %s", reminder.Message))
	}
	if err == nil {
		return nil
	}

	_, fallbackErr := s.ChannelMessageSend(reminder.ChannelID, fmt.Sprintf("<@%s> ⏰ Your reminder is due, but I couldn't send it by DM.", reminder.UserID))
	if fallbackErr != nil {
		return fmt.Errorf("DM delivery failed (%v) and channel fallback failed: %w", err, fallbackErr)
	}
	return nil
}

func nextReminderTime(previous time.Time, interval time.Duration, now time.Time) time.Time {
	next := previous.Add(interval)
	if next.After(now) {
		return next
	}
	elapsed := now.Sub(previous)
	steps := elapsed / interval
	if steps == time.Duration(math.MaxInt64) {
		return now.Add(interval - elapsed%interval)
	}
	steps++
	if steps > time.Duration(math.MaxInt64)/interval {
		return now.Add(interval - elapsed%interval)
	}
	return previous.Add(steps * interval)
}
