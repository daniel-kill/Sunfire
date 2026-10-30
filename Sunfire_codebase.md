# Sunfire Codebase

## `./reminders.go`

```go
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

```

## `./paths.go`

```go
package main

import (
	"os"
	"path/filepath"
)

// applicationDir is the deployment root, overridable for packaged installs.
func applicationDir() string {
	if dir := os.Getenv("SUNFIRE_HOME"); dir != "" {
		return dir
	}
	if cwd, err := os.Getwd(); err == nil {
		return cwd
	}
	return "."
}

// dataDir preserves the local Data/ default while allowing persistent state to
// live outside the release directory in service deployments.
func dataDir() string {
	if dir := os.Getenv("SUNFIRE_DATA_DIR"); dir != "" {
		return dir
	}
	return filepath.Join(applicationDir(), "Data")
}

func dataPath(parts ...string) string {
	return filepath.Join(append([]string{dataDir()}, parts...)...)
}

func applicationPath(parts ...string) string {
	return filepath.Join(append([]string{applicationDir()}, parts...)...)
}

```

## `./message_logger_test.go`

```go
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"
)

func TestMessageLoggerLifecycle(t *testing.T) {
	tmpDir := t.TempDir()
	messageLogsBaseDir = tmpDir
	defer func() {
		messageLogsBaseDir = ""
	}()

	guildID := "test_guild_1"
	authorID := "test_user_1"
	msgID1 := "msg_1001"
	msgID2 := "msg_1002"

	// 1. Test message create
	createMsg1 := &discordgo.MessageCreate{
		Message: &discordgo.Message{
			ID:        msgID1,
			ChannelID: "chan_general",
			GuildID:   guildID,
			Content:   "Hello, world!",
			Author: &discordgo.User{
				ID:       authorID,
				Username: "Tester",
				Bot:      false,
			},
		},
	}
	if err := logMessageToFile(createMsg1); err != nil {
		t.Fatalf("logMessageToFile failed: %v", err)
	}

	createMsg2 := &discordgo.MessageCreate{
		Message: &discordgo.Message{
			ID:        msgID2,
			ChannelID: "chan_general",
			GuildID:   guildID,
			Content:   "Second message",
			Author: &discordgo.User{
				ID:       authorID,
				Username: "Tester",
				Bot:      false,
			},
		},
	}
	if err := logMessageToFile(createMsg2); err != nil {
		t.Fatalf("logMessageToFile second message failed: %v", err)
	}

	// Verify file was written
	userLogPath := filepath.Join(tmpDir, guildID, authorID+".json")
	data, err := os.ReadFile(userLogPath)
	if err != nil {
		t.Fatalf("failed to read user log file: %v", err)
	}
	dirInfo, err := os.Stat(filepath.Dir(userLogPath))
	if err != nil {
		t.Fatalf("failed to stat user log directory: %v", err)
	}
	if got := dirInfo.Mode().Perm(); got != 0o700 {
		t.Errorf("expected private log directory permissions 0700, got %04o", got)
	}
	fileInfo, err := os.Stat(userLogPath)
	if err != nil {
		t.Fatalf("failed to stat user log file: %v", err)
	}
	if got := fileInfo.Mode().Perm(); got != 0o600 {
		t.Errorf("expected private log file permissions 0600, got %04o", got)
	}
	var records []MessageRecord
	if err := json.Unmarshal(data, &records); err != nil {
		t.Fatalf("failed to unmarshal records: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("expected 2 records, got %d", len(records))
	}
	if records[0].Content != "Hello, world!" {
		t.Errorf("expected 'Hello, world!', got %q", records[0].Content)
	}

	// 2. Test message update
	updatedRec, oldContent, err := logMessageUpdateToFile(guildID, msgID1, "Hello, world! (edited)")
	if err != nil {
		t.Fatalf("logMessageUpdateToFile failed: %v", err)
	}
	if updatedRec == nil {
		t.Fatal("expected updatedRec to be non-nil")
	}
	if oldContent != "Hello, world!" {
		t.Errorf("expected oldContent 'Hello, world!', got %q", oldContent)
	}
	if updatedRec.Content != "Hello, world! (edited)" {
		t.Errorf("expected updated content 'Hello, world! (edited)', got %q", updatedRec.Content)
	}
	if len(updatedRec.EditHistory) != 1 {
		t.Fatalf("expected 1 edit history item, got %d", len(updatedRec.EditHistory))
	}
	if updatedRec.EditHistory[0].OldContent != "Hello, world!" || updatedRec.EditHistory[0].NewContent != "Hello, world! (edited)" {
		t.Errorf("unexpected edit history record: %+v", updatedRec.EditHistory[0])
	}

	// Test identical update (no-op)
	sameRec, sameOld, err := logMessageUpdateToFile(guildID, msgID1, "Hello, world! (edited)")
	if err != nil {
		t.Fatalf("no-op update failed: %v", err)
	}
	if sameOld != "Hello, world! (edited)" {
		t.Errorf("expected same old content, got %q", sameOld)
	}
	if len(sameRec.EditHistory) != 1 {
		t.Errorf("expected edit history length to remain 1, got %d", len(sameRec.EditHistory))
	}

	// 3. Test message delete
	delRec, err := logMessageDeleteToFile(guildID, msgID1)
	if err != nil {
		t.Fatalf("logMessageDeleteToFile failed: %v", err)
	}
	if delRec == nil {
		t.Fatal("expected delRec to be non-nil")
	}
	if !delRec.Deleted {
		t.Errorf("expected Deleted to be true")
	}
	if delRec.DeletedTimestamp == "" {
		t.Errorf("expected DeletedTimestamp to be populated")
	}

	// 4. Test bulk delete
	if err := logMessageBulkDeleteToFile(guildID, []string{msgID2}); err != nil {
		t.Fatalf("logMessageBulkDeleteToFile failed: %v", err)
	}

	// Re-verify file state
	data, _ = os.ReadFile(userLogPath)
	records = nil
	_ = json.Unmarshal(data, &records)
	if len(records) != 2 {
		t.Fatalf("expected 2 records, got %d", len(records))
	}
	if !records[0].Deleted || !records[1].Deleted {
		t.Errorf("expected both messages to be marked deleted: %+v", records)
	}
}

func TestMessageLoggerDoesNotOverwriteCorruptExistingLog(t *testing.T) {
	tmpDir := t.TempDir()
	messageLogsBaseDir = tmpDir
	defer func() {
		messageLogsBaseDir = ""
	}()

	guildID := "test_guild"
	authorID := "test_user"
	logPath := filepath.Join(tmpDir, guildID, authorID+".json")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		t.Fatalf("failed to create log directory: %v", err)
	}
	const corruptContent = `{"incomplete":`
	if err := os.WriteFile(logPath, []byte(corruptContent), 0o600); err != nil {
		t.Fatalf("failed to write corrupt log: %v", err)
	}

	msg := &discordgo.MessageCreate{
		Message: &discordgo.Message{
			ID:        "message_id",
			GuildID:   guildID,
			ChannelID: "channel_id",
			Author:    &discordgo.User{ID: authorID, Username: "Tester"},
		},
	}
	if err := logMessageToFile(msg); err == nil {
		t.Fatal("expected corrupt existing log to return an error")
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("failed to read preserved log: %v", err)
	}
	if string(data) != corruptContent {
		t.Fatalf("corrupt log was unexpectedly overwritten: %q", data)
	}
}

func TestSecureExistingMessageLogs(t *testing.T) {
	root := t.TempDir()
	messageLogsBaseDir = root
	defer func() {
		messageLogsBaseDir = ""
	}()

	guildDir := filepath.Join(root, "guild")
	logPath := filepath.Join(guildDir, "user.json")
	if err := os.MkdirAll(guildDir, 0o755); err != nil {
		t.Fatalf("failed to create log directory: %v", err)
	}
	if err := os.WriteFile(logPath, []byte("[]"), 0o644); err != nil {
		t.Fatalf("failed to create log file: %v", err)
	}
	if err := os.Chmod(root, 0o755); err != nil {
		t.Fatalf("failed to set root permissions: %v", err)
	}

	if err := secureExistingMessageLogs(); err != nil {
		t.Fatalf("secureExistingMessageLogs failed: %v", err)
	}

	for path, want := range map[string]os.FileMode{
		root:     0o700,
		guildDir: 0o700,
		logPath:  0o600,
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("failed to stat %s: %v", path, err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Errorf("%s permissions = %04o, want %04o", path, got, want)
		}
	}
}

func TestFormatHelpers(t *testing.T) {
	// TruncateString
	if res := truncateString("short", 10); res != "short" {
		t.Errorf("expected 'short', got %q", res)
	}
	if res := truncateString("hello world", 5); res != "hello..." {
		t.Errorf("expected 'hello...', got %q", res)
	}
	// Multi-byte unicode UTF-8 safe truncation
	if res := truncateString("Kamusta mundo! 🌸✨🎉", 15); !strings.HasSuffix(res, "...") {
		t.Errorf("expected truncation with ellipsis, got %q", res)
	}

	// FormatBlockQuote
	singleLine := "Hello delete"
	if bq := formatBlockQuote(singleLine, 100); bq != "> Hello delete" {
		t.Errorf("expected '> Hello delete', got %q", bq)
	}
	multiLine := "Line 1\nLine 2"
	if bq := formatBlockQuote(multiLine, 100); bq != "> Line 1\n> Line 2" {
		t.Errorf("expected '> Line 1\\n> Line 2', got %q", bq)
	}

	// FormatContentPreview
	if preview := formatContentPreview("Single line text", 100); preview != "\"Single line text\"" {
		t.Errorf("expected quoted single line, got %q", preview)
	}
	if preview := formatContentPreview("Line 1\nLine 2", 100); preview != "\n> Line 1\n> Line 2" {
		t.Errorf("expected multi-line block quote preview, got %q", preview)
	}
}

```

## `./media_logger.go`

```go
// media_logger.go — downloads images/videos/audio posted to servers to Data/MediaLogs/<ServerID>/
// and periodically runs SHA-256 hashing across folders to prune duplicate content.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
)

var (
	mediaLogsBaseDir string                   // testing override
	downloadSem      = make(chan struct{}, 5) // max 5 concurrent downloads
	mediaHTTPClient  = &http.Client{Timeout: 60 * time.Second}
	mediaDedupMu     sync.Mutex
)

// mediaLogsDir returns the path to Data/MediaLogs/<ServerID>/
func mediaLogsDir(serverID string) string {
	if mediaLogsBaseDir != "" {
		return filepath.Join(mediaLogsBaseDir, serverID)
	}
	return dataPath("MediaLogs", serverID)
}

// mediaLogsRoot returns the base Data/MediaLogs directory.
func mediaLogsRoot() string {
	if mediaLogsBaseDir != "" {
		return mediaLogsBaseDir
	}
	return dataPath("MediaLogs")
}

// categorizeMedia identifies whether a file is an image, video, or audio file.
func categorizeMedia(filename, contentType string) string {
	ct := strings.ToLower(contentType)
	if strings.HasPrefix(ct, "image/") {
		return "images"
	}
	if strings.HasPrefix(ct, "video/") {
		return "videos"
	}
	if strings.HasPrefix(ct, "audio/") {
		return "audio"
	}

	ext := strings.ToLower(filepath.Ext(filename))
	switch ext {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp", ".tiff", ".svg", ".ico":
		return "images"
	case ".mp4", ".mov", ".avi", ".mkv", ".webm", ".flv", ".wmv", ".m4v", ".3gp":
		return "videos"
	case ".mp3", ".wav", ".ogg", ".m4a", ".flac", ".aac", ".opus", ".wma", ".aiff":
		return "audio"
	default:
		return ""
	}
}

// sanitizeFilename strips dangerous characters and keeps the filename safe and concise.
func sanitizeFilename(name string) string {
	base := filepath.Base(name)
	base = strings.ReplaceAll(base, "/", "_")
	base = strings.ReplaceAll(base, "\\", "_")
	base = strings.ReplaceAll(base, "..", "_")
	base = strings.ReplaceAll(base, " ", "_")

	var clean []rune
	for _, r := range base {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-' {
			clean = append(clean, r)
		} else {
			clean = append(clean, '_')
		}
	}
	result := string(clean)
	if len(result) > 100 {
		ext := filepath.Ext(result)
		result = result[:100-len(ext)] + ext
	}
	if result == "" || result == "." {
		return "media"
	}
	return result
}

// processMediaAttachments checks a message for media attachments and downloads them in the background.
func processMediaAttachments(m *discordgo.MessageCreate) {
	if m == nil || m.GuildID == "" {
		return
	}

	for _, att := range m.Attachments {
		category := categorizeMedia(att.Filename, att.ContentType)
		if category == "" {
			continue
		}

		go func(attachment *discordgo.MessageAttachment, cat string) {
			downloadSem <- struct{}{}
			defer func() { <-downloadSem }()

			if err := downloadAndSaveMedia(m.GuildID, m.ID, attachment, cat); err != nil {
				logErrorf("media_logger: download failed for %s (%s): %v", attachment.Filename, attachment.URL, err)
			}
		}(att, category)
	}
}

// downloadAndSaveMedia streams the attachment to a temporary file, computes its SHA-256 hash,
// drops duplicates if already present, and renames to the final destination.
func downloadAndSaveMedia(guildID, messageID string, att *discordgo.MessageAttachment, category string) error {
	folder := filepath.Join(mediaLogsDir(guildID), category)
	if err := os.MkdirAll(folder, 0755); err != nil {
		return err
	}

	cleanName := sanitizeFilename(att.Filename)
	timestamp := time.Now().Format("20060102_150405")
	finalName := fmt.Sprintf("%s_%s_%s_%s", timestamp, messageID, att.ID, cleanName)
	finalPath := filepath.Join(folder, finalName)
	tmpPath := finalPath + ".tmp"

	resp, err := mediaHTTPClient.Get(att.URL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("bad status: %s", resp.Status)
	}

	out, err := os.Create(tmpPath)
	if err != nil {
		return err
	}

	hasher := sha256.New()
	writer := io.MultiWriter(out, hasher)

	if _, err := io.Copy(writer, resp.Body); err != nil {
		out.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	out.Close()

	newHash := hex.EncodeToString(hasher.Sum(nil))

	// Check if this exact hash already exists in this guild's media logs
	existingFile, found := findFileByHash(mediaLogsDir(guildID), newHash)
	if found {
		_ = os.Remove(tmpPath)
		log.Printf("[MediaLogs] Skipped duplicate %s (identical to %s)", cleanName, existingFile)
		return nil
	}

	return os.Rename(tmpPath, finalPath)
}

// computeFileHash computes the SHA-256 hash of a file.
func computeFileHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	hasher := sha256.New()
	if _, err := io.Copy(hasher, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// findFileByHash searches rootDir recursively for an existing file with targetHash.
func findFileByHash(rootDir, targetHash string) (string, bool) {
	var foundPath string
	found := false

	_ = filepath.WalkDir(rootDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if strings.HasSuffix(path, ".tmp") {
			return nil
		}
		hash, err := computeFileHash(path)
		if err == nil && hash == targetHash {
			foundPath = path
			found = true
			return io.EOF // stop walking early
		}
		return nil
	})

	return foundPath, found
}

type fileInfoItem struct {
	path    string
	modTime time.Time
	size    int64
}

// DeduplicateMediaFolder scans folder recursively, hashes all files,
// retains the oldest version of each hash, and deletes any duplicate files.
func DeduplicateMediaFolder(folder string) (int, int64, error) {
	mediaDedupMu.Lock()
	defer mediaDedupMu.Unlock()

	if _, err := os.Stat(folder); os.IsNotExist(err) {
		return 0, 0, nil
	}

	hashMap := make(map[string][]fileInfoItem)

	err := filepath.WalkDir(folder, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if strings.HasSuffix(path, ".tmp") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		hash, err := computeFileHash(path)
		if err != nil {
			return nil
		}
		hashMap[hash] = append(hashMap[hash], fileInfoItem{
			path:    path,
			modTime: info.ModTime(),
			size:    info.Size(),
		})
		return nil
	})
	if err != nil {
		return 0, 0, err
	}

	deletedCount := 0
	var freedBytes int64

	for hash, items := range hashMap {
		if len(items) <= 1 {
			continue
		}
		// Sort by modTime ascending so the oldest file is kept as the original
		sort.Slice(items, func(i, j int) bool {
			if items[i].modTime.Equal(items[j].modTime) {
				return items[i].path < items[j].path
			}
			return items[i].modTime.Before(items[j].modTime)
		})

		keep := items[0]
		for _, dup := range items[1:] {
			if err := os.Remove(dup.path); err == nil {
				deletedCount++
				freedBytes += dup.size
				log.Printf("[MediaLogs] Deduplication: removed duplicate %s (matches %s, hash: %s)", dup.path, keep.path, hash[:8])
			} else {
				logErrorf("media_logger: failed to remove duplicate %s: %v", dup.path, err)
			}
		}
	}

	return deletedCount, freedBytes, nil
}

// DeduplicateAllMediaLogs runs deduplication across all server directories in Data/MediaLogs.
func DeduplicateAllMediaLogs() (int, int64, error) {
	root := mediaLogsRoot()
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, 0, nil
		}
		return 0, 0, err
	}

	totalDeleted := 0
	var totalFreed int64

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		serverDir := filepath.Join(root, entry.Name())
		deleted, freed, err := DeduplicateMediaFolder(serverDir)
		if err != nil {
			logErrorf("media_logger: dedup failed for %s: %v", serverDir, err)
			continue
		}
		totalDeleted += deleted
		totalFreed += freed
	}

	return totalDeleted, totalFreed, nil
}

// startPeriodicMediaDeduplication runs DeduplicateAllMediaLogs periodically in the background.
func startPeriodicMediaDeduplication(ctx context.Context, interval time.Duration, b *Bot) {
	log.Printf("media deduplication worker started (interval %s)", interval)
	runPeriodic(ctx, 10*time.Second, interval, func() {
		if deleted, freed, err := DeduplicateAllMediaLogs(); err == nil && deleted > 0 {
			msg := fmt.Sprintf("Media deduplication cleaned up %d duplicate file(s) (%.2f MB freed)", deleted, float64(freed)/(1024*1024))
			log.Println("[MediaLogs] " + msg)
			if b != nil && b.logger != nil && channelID() != "" {
				b.logger.log("MEDIA DEDUP", msg)
			}
		}
	})
}

```

## `./iahandlers.go`

```go
// iahandlers.go — slash-command handlers for the /ia command (subcommands).
package main

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/bwmarrin/discordgo"
)

// iaReply sends a channel-message reply.
func iaReply(s *discordgo.Session, i *discordgo.InteractionCreate, content string) {
	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: clipMessage(content),
		},
	})
}

// iaDefer acknowledges the interaction (for slow IA calls), then follows up.
func iaDefer(s *discordgo.Session, i *discordgo.InteractionCreate) {
	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	})
}

// iaFollowUp sends the result as a follow-up after a deferred reply.
func iaFollowUp(s *discordgo.Session, i *discordgo.InteractionCreate, content string, files []*discordgo.File) {
	params := &discordgo.WebhookParams{Content: clipMessage(content)}
	if len(files) > 0 {
		params.Files = files
	}
	_, _ = s.FollowupMessageCreate(i.Interaction, true, params)
}

// clipMessage keeps Discord messages under the 2000-char limit.
func clipMessage(s string) string {
	if len(s) <= 1900 {
		return s
	}
	return s[:1900] + "…"
}

// optionString reads a string option by name from an interaction.
func optionString(i *discordgo.InteractionCreate, name string) string {
	data := i.ApplicationCommandData()
	for _, opt := range data.Options {
		if opt.Name == name && opt.Value != nil {
			if v, ok := opt.Value.(string); ok {
				return v
			}
		}
	}
	return ""
}

// handleIA dispatches /ia subcommands.
func handleIA(s *discordgo.Session, i *discordgo.InteractionCreate) {
	data := i.ApplicationCommandData()
	if len(data.Options) == 0 {
		iaReply(s, i, iaHelpText())
		return
	}
	sub := data.Options[0]
	switch sub.Name {
	case "search":
		iaHandleSearch(s, i, sub)
	case "item":
		iaHandleItem(s, i, sub)
	case "download":
		iaHandleDownload(s, i, sub)
	case "wayback":
		iaHandleWayback(s, i, sub)
	case "stats":
		iaHandleStats(s, i, sub)
	case "fts":
		iaHandleFTS(s, i, sub)
	case "help":
		iaReply(s, i, iaHelpText())
	default:
		iaReply(s, i, "Unknown /ia subcommand. Try `/ia help`.")
	}
}

func iaHandleSearch(s *discordgo.Session, i *discordgo.InteractionCreate, sub *discordgo.ApplicationCommandInteractionDataOption) {
	query := optionStringFrom(sub, "query")
	if query == "" {
		iaReply(s, i, "Usage: `/ia search query:<query> [type:audio|texts|movies|image|software]`\nExample: `/ia search query:space audio`")
		return
	}
	// Optional mediatype filter.
	if mt := optionStringFrom(sub, "type"); mt != "" {
		query = query + " AND mediatype:" + mt
	}
	iaDefer(s, i)
	docs, numFound, err := iaAdvancedSearch(query, iaMaxResults)
	if err != nil {
		iaFollowUp(s, i, "❌ Search failed: "+err.Error(), nil)
		return
	}
	iaFollowUp(s, i, formatIASearch(query, docs, numFound), nil)
}

func iaHandleItem(s *discordgo.Session, i *discordgo.InteractionCreate, sub *discordgo.ApplicationCommandInteractionDataOption) {
	identifier := strings.TrimSpace(optionStringFrom(sub, "identifier"))
	if identifier == "" {
		iaReply(s, i, "Usage: `/ia item identifier:<identifier>`\nExample: `/ia item identifier:xfetch`")
		return
	}
	iaDefer(s, i)
	rec, err := iaFetchItem(identifier)
	if err != nil {
		iaFollowUp(s, i, "❌ Could not fetch item: "+err.Error(), nil)
		return
	}
	iaFollowUp(s, i, formatIAItem(rec), nil)
}

func iaHandleDownload(s *discordgo.Session, i *discordgo.InteractionCreate, sub *discordgo.ApplicationCommandInteractionDataOption) {
	identifier := strings.TrimSpace(optionStringFrom(sub, "identifier"))
	if identifier == "" {
		iaReply(s, i, "Usage: `/ia download identifier:<identifier> [file:<filename>]`\nIf no file is given, a small representative file (thumbnail/audio/pdf) is auto-picked.\nExample: `/ia download identifier:xfetch`")
		return
	}
	iaDefer(s, i)
	rec, err := iaFetchItem(identifier)
	if err != nil {
		iaFollowUp(s, i, "❌ Could not fetch item: "+err.Error(), nil)
		return
	}
	filename := strings.TrimSpace(optionStringFrom(sub, "file"))
	if filename == "" {
		filename = iaContentForReply(identifier, rec.Files)
		if filename == "" {
			iaFollowUp(s, i, "ℹ️ Item has no small auto-downloadable file. Use `/ia item identifier:"+identifier+"` to list its files, then `/ia download identifier:"+identifier+" file:<name>`.", nil)
			return
		}
	}
	// Verify the requested file exists.
	exists := false
	for _, f := range rec.Files {
		if f.Name == filename {
			exists = true
			break
		}
	}
	if !exists {
		iaFollowUp(s, i, "❌ File `"+filename+"` not found in item `"+identifier+"`. Use `/ia item identifier:"+identifier+"` to list files.", nil)
		return
	}
	body, err := iaGet(iaDownloadURL(identifier, filename))
	if err != nil {
		iaFollowUp(s, i, "❌ Download failed: "+err.Error(), nil)
		return
	}
	if len(body) > 8*1024*1024 {
		iaFollowUp(s, i, "⚠️ File is too large to post to Discord (>8 MB). Direct link: "+iaDownloadURL(identifier, filename), nil)
		return
	}
	file := &discordgo.File{
		Name:        filename,
		ContentType: contentTypeForName(filename),
		Reader:      bytes.NewReader(body),
	}
	iaFollowUp(s, i, fmt.Sprintf("⬇️ **%s** — `%s`\n🔗 <https://archive.org/download/%s/%s>",
		iaClip(identifier, 80), filename, urlPathEscape(identifier), urlPathEscape(filename)), []*discordgo.File{file})
}

func iaHandleWayback(s *discordgo.Session, i *discordgo.InteractionCreate, sub *discordgo.ApplicationCommandInteractionDataOption) {
	pageURL := strings.TrimSpace(optionStringFrom(sub, "url"))
	if pageURL == "" {
		iaReply(s, i, "Usage: `/ia wayback url:<url>`\nChecks whether the Wayback Machine has a snapshot of a URL.\nExample: `/ia wayback url:https://example.com`")
		return
	}
	if !strings.HasPrefix(pageURL, "http") {
		pageURL = "https://" + pageURL
	}
	iaDefer(s, i)
	res, err := iaWaybackAvailable(pageURL)
	if err != nil {
		iaFollowUp(s, i, "❌ Wayback check failed: "+err.Error(), nil)
		return
	}
	iaFollowUp(s, i, formatIAWayback(res), nil)
}

func iaHandleStats(s *discordgo.Session, i *discordgo.InteractionCreate, sub *discordgo.ApplicationCommandInteractionDataOption) {
	identifier := strings.TrimSpace(optionStringFrom(sub, "identifier"))
	if identifier == "" {
		iaReply(s, i, "Usage: `/ia stats identifier:<identifier>`\nShows views/downloads for an item.\nExample: `/ia stats identifier:xfetch`")
		return
	}
	iaDefer(s, i)
	st, err := iaFetchStats(identifier)
	if err != nil {
		iaFollowUp(s, i, "❌ Stats fetch failed: "+err.Error(), nil)
		return
	}
	iaFollowUp(s, i, formatIAStats(identifier, st), nil)
}

func iaHandleFTS(s *discordgo.Session, i *discordgo.InteractionCreate, sub *discordgo.ApplicationCommandInteractionDataOption) {
	query := strings.TrimSpace(optionStringFrom(sub, "query"))
	if query == "" {
		iaReply(s, i, "Usage: `/ia fts query:<query>`\nFull-text search across items.\nExample: `/ia fts query:\"war of the worlds\"`")
		return
	}
	iaDefer(s, i)
	items, total, err := iaFullText(query, iaMaxResults)
	if err != nil {
		iaFollowUp(s, i, "❌ Full-text search failed: "+err.Error(), nil)
		return
	}
	iaFollowUp(s, i, formatIAFTS(query, items, total), nil)
}

// optionStringFrom reads a string option from a subcommand's options.
func optionStringFrom(sub *discordgo.ApplicationCommandInteractionDataOption, name string) string {
	for _, o := range sub.Options {
		if o.Name == name && o.Value != nil {
			if v, ok := o.Value.(string); ok {
				return v
			}
		}
	}
	return ""
}

// urlPathEscape is a small wrapper so we don't import net/url in the handler.
func urlPathEscape(s string) string {
	esc := strings.NewReplacer(" ", "%20", "/", "%2F", "?", "%3F", "#", "%23", "&", "%26", "=", "%3D")
	return esc.Replace(s)
}

// contentTypeForName guesses a Discord content type from a filename.
func contentTypeForName(name string) string {
	n := strings.ToLower(name)
	switch {
	case strings.HasSuffix(n, ".jpg") || strings.HasSuffix(n, ".jpeg"):
		return "image/jpeg"
	case strings.HasSuffix(n, ".png"):
		return "image/png"
	case strings.HasSuffix(n, ".gif"):
		return "image/gif"
	case strings.HasSuffix(n, ".webp"):
		return "image/webp"
	case strings.HasSuffix(n, ".mp3"):
		return "audio/mpeg"
	case strings.HasSuffix(n, ".ogg"):
		return "audio/ogg"
	case strings.HasSuffix(n, ".wav"):
		return "audio/wav"
	case strings.HasSuffix(n, ".pdf"):
		return "application/pdf"
	case strings.HasSuffix(n, ".txt"):
		return "text/plain"
	case strings.HasSuffix(n, ".json"):
		return "application/json"
	default:
		return "application/octet-stream"
	}
}

// iaHelpText returns the /ia usage guide.
func iaHelpText() string {
	return "**🗂️ Internet Archive — `/ia`**\n\n" +
		"Search and explore the Internet Archive right from Discord. All data is public; no API key needed.\n\n" +
		"**`/ia search query:<q> [type:<type>]`** — metadata search. Optional type: `audio`, `texts`, `movies`, `image`, `software`, `etree`, `web`.\n" +
		"  _e.g. `/ia search query:space audio`_\n\n" +
		"**`/ia item identifier:<id>`** — full metadata for an item (title, creator, files, size).\n" +
		"  _e.g. `/ia item identifier:xfetch`_\n\n" +
		"**`/ia download identifier:<id> [file:<name>]`** — post a file from an item (auto-picks a small one if no file given).\n" +
		"  _e.g. `/ia download identifier:xfetch`_\n\n" +
		"**`/ia wayback url:<url>`** — check if the Wayback Machine archived a URL, with the snapshot date.\n" +
		"  _e.g. `/ia wayback url:https://example.com`_\n\n" +
		"**`/ia stats identifier:<id>`** — view + download counts for an item.\n\n" +
		"**`/ia fts query:<q>`** — full-text search across the archive.\n\n" +
		"**`/ia help`** — show this guide.\n\n" +
		"_Powered by archive.org — see `/home/daniel/Repos/shared/docs/InternetArchive.md`._"
}

```

## `./moderation.go`

```go
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

```

## `./health_checker.go`

```go
// health_checker.go — background goroutine that periodically checks configured
// URLs and posts outages/recoveries to the Discord logging channel (#logs).
//
// Enabled via config:  "healthCheck": { "enabled": true, "intervalMinutes": 10,
//
//	"targets": ["https://...", ...] }
//
// The checker keeps per-target state so it only posts on state CHANGES
// (down -> up, up -> down), not on every tick.
package main

import (
	"context"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
)

// healthState tracks the last-known state of one target.
type healthState struct {
	mu      sync.Mutex
	up      map[string]bool // url -> last known up
	started time.Time
}

// healthChecker runs the periodic loop.
type healthChecker struct {
	session *discordgo.Session
	state   *healthState
	cfg     HealthCheckConfig
}

// startHealthChecker launches the background loop if enabled in config.
func startHealthChecker(ctx context.Context, s *discordgo.Session) {
	cfg, err := loadConfig()
	if err != nil || !cfg.HealthCheck.Enabled {
		return
	}
	interval := cfg.HealthCheck.Interval
	if interval <= 0 {
		interval = 10
	}
	hc := &healthChecker{
		session: s,
		state:   &healthState{up: map[string]bool{}, started: time.Now()},
		cfg:     cfg.HealthCheck,
	}
	log.Printf("health checker started: %d target(s), every %d min", len(cfg.HealthCheck.Targets), interval)
	hc.loop(ctx, time.Duration(interval)*time.Minute)
}

// loop ticks every interval and runs a check pass.
func (hc *healthChecker) loop(ctx context.Context, interval time.Duration) {
	hc.checkOnce()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			hc.checkOnce()
		case <-ctx.Done():
			log.Println("health checker stopped")
			return
		}
	}
}

// checkOnce probes every target concurrently and posts state changes.
func (hc *healthChecker) checkOnce() {
	var wg sync.WaitGroup
	for _, url := range hc.cfg.Targets {
		url := url
		wg.Add(1)
		go func() {
			defer wg.Done()
			hc.checkTarget(url)
		}()
	}
	wg.Wait()
}

// targetChannel resolves the destination channel, checking for updated config and falling back to default logging.
func (hc *healthChecker) targetChannel() string {
	if cfg, err := loadConfig(); err == nil && cfg != nil {
		if cid := cfg.HealthCheck.TargetChannelID(); cid != "" {
			return cid
		}
	}
	return hc.cfg.TargetChannelID()
}

// checkTarget probes one URL and posts on a state change.
func (hc *healthChecker) checkTarget(url string) {
	up := probe(url)
	hc.state.mu.Lock()
	wasUp, known := hc.state.up[url]
	hc.state.up[url] = up
	hc.state.mu.Unlock()

	if !known || wasUp == up {
		return // no change (or first check — don't spam on startup)
	}

	cid := hc.targetChannel()
	if cid == "" {
		return
	}

	dl := newLogger(hc.session)
	if up {
		dl.logTo(cid, "INFO", "✅ **"+url+"** is back UP ("+formatTimestamp()+")")
	} else {
		dl.logTo(cid, "ERROR", "🛑 **"+url+"** is DOWN ("+formatTimestamp()+")")
	}
}

// probe does a GET with a short timeout and reports reachability.
func probe(url string) bool {
	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	// Treat 2xx/3xx as up.
	return resp.StatusCode >= 200 && resp.StatusCode < 400
}

```

## `./internetarchive.go`

```go
// internetarchive.go — /ia slash commands wrapping the Internet Archive public
// APIs (https://archive.org/developers/). All endpoints used here are public
// and need no authentication:
//
//	Metadata read   GET https://archive.org/metadata/{identifier}
//	Advanced search GET https://archive.org/advancedsearch.php?q=...&output=json
//	Scrape search   GET https://archive.org/services/search/v1/scrape?q=...
//	Full-text       GET https://archive.org/services/search/v1/scrape?... (ft via q)
//	Downloads       GET https://archive.org/download/{identifier}/{filename}
//	Thumbnails      GET https://archive.org/services/img/{identifier}
//	Wayback avail.  GET https://archive.org/wayback/available?url=...
//
// A descriptive User-Agent is sent on every request per the Archive's bots
// policy (https://archive.org/developers/bots.html).
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	iaBase       = "https://archive.org"
	iaTimeout    = 15 * time.Second
	iaUserAgent  = "ElyLightspeedBot/1.0 (Discord bot; https://github.com/daniel-kill/ElyLightspeed)"
	iaMaxResults = 5
)

// iaGet fetches a URL and returns the raw bytes, or an error on non-2xx.
func iaGet(rawURL string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", iaUserAgent)
	client := &http.Client{Timeout: iaTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return body, fmt.Errorf("HTTP %d from %s", resp.StatusCode, rawURL)
	}
	return body, nil
}

// iaGetJSON fetches and decodes a JSON body.
func iaGetJSON(rawURL string, out interface{}) error {
	body, err := iaGet(rawURL)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, out)
}

// ---- /ia search ------------------------------------------------------------

// iaAdvancedSearch runs a metadata search against the Advanced Search API.
func iaAdvancedSearch(query string, rows int) ([]map[string]interface{}, int64, error) {
	var out struct {
		Response struct {
			NumFound int64                    `json:"numFound"`
			Start    int                      `json:"start"`
			Docs     []map[string]interface{} `json:"docs"`
		} `json:"response"`
	}
	params := url.Values{}
	params.Set("q", query)
	params.Set("fl[]", "identifier,title,creator,year,mediatype,description")
	params.Set("rows", fmt.Sprintf("%d", rows))
	params.Set("output", "json")
	raw := iaBase + "/advancedsearch.php?" + params.Encode()
	if err := iaGetJSON(raw, &out); err != nil {
		return nil, 0, err
	}
	return out.Response.Docs, out.Response.NumFound, nil
}

// ---- /ia item ---------------------------------------------------------------

type iaItemMeta struct {
	Identifier  string      `json:"identifier"`
	Mediatype   string      `json:"mediatype"`
	Title       string      `json:"title"`
	Creator     interface{} `json:"creator"`
	Description interface{} `json:"description"`
	Date        string      `json:"date"`
	Year        string      `json:"year"`
	Collection  interface{} `json:"collection"`
	Subject     interface{} `json:"subject"`
	Uploader    string      `json:"uploader"`
}

type iaItemRecord struct {
	Created    int64        `json:"created"`
	ItemSize   int64        `json:"item_size"`
	FilesCount int          `json:"files_count"`
	Metadata   iaItemMeta   `json:"metadata"`
	Files      []iaItemFile `json:"files"`
}

type iaItemFile struct {
	Name   string `json:"name"`
	Size   string `json:"size"`
	Format string `json:"format"`
	Source string `json:"source"`
}

// iaFetchItem fetches the full metadata record for one item.
func iaFetchItem(identifier string) (*iaItemRecord, error) {
	var rec iaItemRecord
	raw := iaBase + "/metadata/" + url.PathEscape(identifier)
	if err := iaGetJSON(raw, &rec); err != nil {
		return nil, err
	}
	return &rec, nil
}

// ---- /ia wayback ------------------------------------------------------------

type iaWaybackResult struct {
	URL               string `json:"url"`
	ArchivedSnapshots struct {
		Closest struct {
			Available bool   `json:"available"`
			URL       string `json:"url"`
			Timestamp string `json:"timestamp"`
			Status    string `json:"status"`
		} `json:"closest"`
	} `json:"archived_snapshots"`
}

// iaWaybackAvailable checks if a URL has a Wayback snapshot.
func iaWaybackAvailable(pageURL string) (*iaWaybackResult, error) {
	var out iaWaybackResult
	params := url.Values{}
	params.Set("url", pageURL)
	raw := iaBase + "/wayback/available?" + params.Encode()
	if err := iaGetJSON(raw, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ---- /ia stats (views API) --------------------------------------------------

type iaStatsView struct {
	AllTime   int64 `json:"all_time"`
	Last30Day int64 `json:"last_30day"`
	Last7Day  int64 `json:"last_7day"`
	HaveData  bool  `json:"have_data"`
}

// iaFetchStats pulls view counts for an item (Views data service API).
// https://be-api.us.archive.org/views/v1/short/<identifier>
func iaFetchStats(identifier string) (*iaStatsView, error) {
	var out map[string]iaStatsView
	raw := "https://be-api.us.archive.org/views/v1/short/" + url.PathEscape(identifier)
	if err := iaGetJSON(raw, &out); err != nil {
		return nil, err
	}
	v, ok := out[identifier]
	if !ok {
		return &iaStatsView{}, nil
	}
	return &v, nil
}

// ---- /ia fts (full-text search via scrape API) ------------------------------

func iaFullText(query string, count int) ([]map[string]interface{}, int64, error) {
	var out struct {
		Items []map[string]interface{} `json:"items"`
		Total int64                    `json:"total"`
	}
	params := url.Values{}
	params.Set("q", query)
	params.Set("fields", "identifier,title,mediatype,creator")
	// The scraping API requires count >= 100; we only display iaMaxResults.
	if count < 100 {
		count = 100
	}
	params.Set("count", fmt.Sprintf("%d", count))
	raw := iaBase + "/services/search/v1/scrape?" + params.Encode()
	if err := iaGetJSON(raw, &out); err != nil {
		return nil, 0, err
	}
	if len(out.Items) > iaMaxResults {
		out.Items = out.Items[:iaMaxResults]
	}
	return out.Items, out.Total, nil
}

// ---- Discord rendering ------------------------------------------------------

// iaStrFrom is a small helper: interface{} -> trimmed string.
func iaStrFrom(v interface{}) string {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case []interface{}:
		var parts []string
		for _, e := range t {
			if s := iaStrFrom(e); s != "" {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, ", ")
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", t)
	}
}

// iaClip keeps a string under a max length, appending an ellipsis.
func iaClip(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return strings.TrimSpace(s[:max-1]) + "…"
}

// iaField renders "**Name:** value" when value is non-empty.
func iaField(name, value string) string {
	if value == "" {
		return ""
	}
	return fmt.Sprintf("**%s:** %s\n", name, value)
}

// iaThumbURL returns the thumbnail URL for an item.
func iaThumbURL(identifier string) string {
	return fmt.Sprintf("%s/services/img/%s", iaBase, url.PathEscape(identifier))
}

// iaDetailsURL returns the details (item) page URL.
func iaDetailsURL(identifier string) string {
	return fmt.Sprintf("%s/details/%s", iaBase, url.PathEscape(identifier))
}

// iaDownloadURL returns a file download URL.
func iaDownloadURL(identifier, filename string) string {
	return fmt.Sprintf("%s/download/%s/%s", iaBase,
		url.PathEscape(identifier), url.PathEscape(filename))
}

// formatIASearch renders the /ia search result list.
func formatIASearch(query string, docs []map[string]interface{}, numFound int64) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("🔎 **Internet Archive search — `%s`** (%d results)\n\n", iaClip(query, 60), numFound))
	if len(docs) == 0 {
		b.WriteString("No results found.")
		return b.String()
	}
	for i, d := range docs {
		id := iaStrFrom(d["identifier"])
		title := iaStrFrom(d["title"])
		if title == "" {
			title = id
		}
		creator := iaClip(iaStrFrom(d["creator"]), 80)
		year := iaStrFrom(d["year"])
		media := iaStrFrom(d["mediatype"])
		if media == "" {
			media = "item"
		}
		b.WriteString(fmt.Sprintf("**%d.** **%s**\n", i+1, iaClip(title, 120)))
		if creator != "" {
			b.WriteString(fmt.Sprintf("    _%s_", iaClip(creator, 80)))
			if year != "" {
				b.WriteString(fmt.Sprintf(" · %s", year))
			}
			b.WriteString("\n")
		}
		b.WriteString(fmt.Sprintf("    `%s` · %s · <https://archive.org/details/%s>\n",
			id, media, url.PathEscape(id)))
	}
	return b.String()
}

// formatIAItem renders the /ia item metadata record.
func formatIAItem(rec *iaItemRecord) string {
	md := rec.Metadata
	var b strings.Builder
	title := md.Title
	if title == "" {
		title = md.Identifier
	}
	b.WriteString(fmt.Sprintf("📦 **%s**\n", iaClip(title, 150)))
	b.WriteString(fmt.Sprintf("`%s` · %s\n", md.Identifier, iaFieldVal(md.Mediatype, "mediatype")))
	if c := iaClip(iaStrFrom(md.Creator), 100); c != "" {
		b.WriteString(fmt.Sprintf("_by %s_\n", c))
	}
	if d := iaStrFrom(md.Date); d == "" {
		if y := iaStrFrom(md.Year); y != "" {
			d = y
		}
	}
	if d := iaStrFrom(md.Date); d != "" {
		b.WriteString(fmt.Sprintf("**Date:** %s\n", d))
	}
	if s := iaClip(iaStrFrom(md.Subject), 200); s != "" {
		b.WriteString(fmt.Sprintf("**Subjects:** %s\n", s))
	}
	if u := iaStrFrom(md.Uploader); u != "" {
		b.WriteString(fmt.Sprintf("**Uploader:** %s\n", u))
	}
	b.WriteString(fmt.Sprintf("**Size:** %s\n", humanBytes(rec.ItemSize)))
	b.WriteString(fmt.Sprintf("**Files:** %d\n", rec.FilesCount))
	// List the first few files.
	if len(rec.Files) > 0 {
		b.WriteString("\n**Files:**\n")
		for i, f := range rec.Files {
			if i >= 8 {
				b.WriteString(fmt.Sprintf("_… and %d more_", len(rec.Files)-i))
				break
			}
			b.WriteString(fmt.Sprintf("  • `%s` (%s · %s)\n",
				iaClip(f.Name, 60), f.Format, humanBytesStr(f.Size)))
		}
	}
	b.WriteString(fmt.Sprintf("\n🔗 <https://archive.org/details/%s>", url.PathEscape(md.Identifier)))
	return b.String()
}

// iaFieldVal is a tiny helper: return "value (key)" when non-empty.
func iaFieldVal(v, key string) string {
	if v == "" || v == "null" {
		return ""
	}
	return v
}

// formatIAWayback renders the /ia wayback availability result.
func formatIAWayback(r *iaWaybackResult) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("🕰️ **Wayback Machine — `%s`**\n", iaClip(r.URL, 80)))
	closest := r.ArchivedSnapshots.Closest
	if !closest.Available || closest.URL == "" {
		b.WriteString("No snapshot found for this URL.")
		return b.String()
	}
	ts := closest.Timestamp
	date := "unknown"
	if len(ts) >= 8 {
		date = fmt.Sprintf("%s-%s-%s", ts[0:4], ts[4:6], ts[6:8])
	}
	b.WriteString(fmt.Sprintf("✅ Snapshot found: **%s** (status %s)\n", date, closest.Status))
	b.WriteString(fmt.Sprintf("🔗 <https://web.archive.org/web/%s/%s>", ts, strings.TrimPrefix(closest.URL, "http")))
	return b.String()
}

// formatIAStats renders the /ia stats result.
func formatIAStats(identifier string, st *iaStatsView) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("📊 **Internet Archive stats — `%s`**\n", identifier))
	b.WriteString(fmt.Sprintf("👁️ **All-time views:** %d\n", st.AllTime))
	b.WriteString(fmt.Sprintf("📅 **Last 30 days:** %d\n", st.Last30Day))
	b.WriteString(fmt.Sprintf("📅 **Last 7 days:** %d\n", st.Last7Day))
	if !st.HaveData {
		b.WriteString("_No view data available for this item._\n")
	}
	b.WriteString(fmt.Sprintf("🔗 <https://archive.org/details/%s>", url.PathEscape(identifier)))
	return b.String()
}

// formatIAFTS renders the /ia fts result.
func formatIAFTS(query string, items []map[string]interface{}, total int64) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("📖 **Full-text search — `%s`** (%d matches)\n\n", iaClip(query, 60), total))
	if len(items) == 0 {
		b.WriteString("No matches found.")
		return b.String()
	}
	for i, d := range items {
		id := iaStrFrom(d["identifier"])
		title := iaStrFrom(d["title"])
		if title == "" {
			title = id
		}
		media := iaStrFrom(d["mediatype"])
		if media == "" {
			media = "item"
		}
		b.WriteString(fmt.Sprintf("**%d.** **%s**\n", i+1, iaClip(title, 120)))
		b.WriteString(fmt.Sprintf("    `%s` · %s · <https://archive.org/details/%s>\n",
			id, media, url.PathEscape(id)))
	}
	return b.String()
}

// humanBytes formats a byte count.
func humanBytes(n int64) string {
	if n <= 0 {
		return "0 B"
	}
	units := []string{"B", "KB", "MB", "GB", "TB"}
	f := float64(n)
	i := 0
	for f >= 1024 && i < len(units)-1 {
		f /= 1024
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%d B", n)
	}
	return fmt.Sprintf("%.1f %s", f, units[i])
}

// humanBytesStr parses a numeric string (IA file sizes are strings) and formats it.
func humanBytesStr(s string) string {
	var n int64
	fmt.Sscanf(s, "%d", &n)
	return humanBytes(n)
}

// iaContentForReply picks a small file (image/pdf) from an item to attach,
// or "" if none is suitable.
func iaContentForReply(identifier string, files []iaItemFile) string {
	preferred := []string{"__ia_thumb.jpg", "thumb", ".jpg", ".png", ".gif", ".webp", ".mp3", ".ogg", ".pdf"}
	for _, p := range preferred {
		for _, f := range files {
			if strings.Contains(f.Name, p) {
				// Only auto-attach small files (< 4 MB) to keep replies snappy.
				var size int64
				fmt.Sscanf(f.Size, "%d", &size)
				if size > 0 && size > 4*1024*1024 {
					continue
				}
				return f.Name
			}
		}
	}
	return ""
}

```

## `./timequery.go`

```go
// timequery.go — answers "what time is it in <place>" questions via the free
// timeapi.io endpoint, with a small city→IANA timezone lookup.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
)

// timeZones maps common city/place names to IANA timezone ids.
var timeZones = map[string]string{
	"manila":        "Asia/Manila",
	"tokyo":         "Asia/Tokyo",
	"singapore":     "Asia/Singapore",
	"hong kong":     "Asia/Hong_Kong",
	"beijing":       "Asia/Shanghai",
	"shanghai":      "Asia/Shanghai",
	"seoul":         "Asia/Seoul",
	"mumbai":        "Asia/Kolkata",
	"new delhi":     "Asia/Kolkata",
	"bangkok":       "Asia/Bangkok",
	"jakarta":       "Asia/Jakarta",
	"kuala lumpur":  "Asia/Kuala_Lumpur",
	"sydney":        "Australia/Sydney",
	"melbourne":     "Australia/Melbourne",
	"auckland":      "Pacific/Auckland",
	"dubai":         "Asia/Dubai",
	"moscow":        "Europe/Moscow",
	"berlin":        "Europe/Berlin",
	"paris":         "Europe/Paris",
	"london":        "Europe/London",
	"madrid":        "Europe/Madrid",
	"rome":          "Europe/Rome",
	"amsterdam":     "Europe/Amsterdam",
	"stockholm":     "Europe/Stockholm",
	"oslo":          "Europe/Oslo",
	"istanbul":      "Europe/Istanbul",
	"new york":      "America/New_York",
	"nyc":           "America/New_York",
	"chicago":       "America/Chicago",
	"los angeles":   "America/Los_Angeles",
	"la":            "America/Los_Angeles",
	"san francisco": "America/Los_Angeles",
	"seattle":       "America/Los_Angeles",
	"toronto":       "America/Toronto",
	"vancouver":     "America/Vancouver",
	"mexico city":   "America/Mexico_City",
	"denver":        "America/Denver",
	"phoenix":       "America/Phoenix",
	"miami":         "America/New_York",
	"atlanta":       "America/New_York",
	"houston":       "America/Chicago",
	"dallas":        "America/Chicago",
	"sao paulo":     "America/Sao_Paulo",
	"buenos aires":  "America/Argentina/Buenos_Aires",
	"lisbon":        "Europe/Lisbon",
	"dublin":        "Europe/Dublin",
	"warsaw":        "Europe/Warsaw",
	"athens":        "Europe/Athens",
	"cairo":         "Africa/Cairo",
	"lagos":         "Africa/Lagos",
	"nairobi":       "Africa/Nairobi",
	"johannesburg":  "Africa/Johannesburg",
	"cape town":     "Africa/Johannesburg",
	"hawaii":        "Pacific/Honolulu",
	"honolulu":      "Pacific/Honolulu",
	"anchorage":     "America/Anchorage",
	"alaska":        "America/Anchorage",
}

// timeAPIZone is the endpoint for a fixed IANA zone.
const timeAPIZone = "https://timeapi.io/api/Time/current/zone?timeZone="

// timeAPIResult mirrors the JSON timeapi.io returns.
type timeAPIResult struct {
	DateTime  string `json:"dateTime"`
	TimeZone  string `json:"timeZone"`
	DayOfWeek string `json:"dayOfWeek"`
}

// isTimeQuery reports whether a query asks for the time in a place.
func isTimeQuery(query string) (string, bool) {
	q := strings.ToLower(strings.TrimSpace(query))
	place := ""
	// Match common phrasings: "time in X", "time is it in X", "time in X?" etc.
	for _, marker := range []string{"time is it in", "time it is in", "time in"} {
		if i := strings.Index(q, marker); i >= 0 {
			place = strings.TrimSpace(q[i+len(marker):])
			place = strings.TrimSuffix(place, "?")
			place = strings.TrimSpace(place)
			break
		}
	}
	if place == "" {
		return "", false
	}
	zone, ok := timeZones[place]
	if !ok {
		return "", false
	}
	return zone, true
}

// fetchTimeZoneTime hits timeapi.io and returns a formatted time string.
func fetchTimeZoneTime(zone string) (string, error) {
	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Get(timeAPIZone + zone)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
	if err != nil {
		return "", err
	}
	var t timeAPIResult
	if err := json.Unmarshal(body, &t); err != nil {
		return "", err
	}
	if t.DateTime == "" {
		return "", fmt.Errorf("empty response from timeapi.io")
	}
	return fmt.Sprintf("🕐 **%s** · %s (%s)", t.DateTime, t.DayOfWeek, t.TimeZone), nil
}

// handleTime answers a "what time is it in X" query directly.
func handleTime(s *discordgo.Session, i *discordgo.InteractionCreate, query string) bool {
	zone, ok := isTimeQuery(query)
	if !ok {
		return false // not a time query — let /any handle it
	}
	msg, err := fetchTimeZoneTime(zone)
	if err != nil {
		msg = "❌ Couldn't fetch the time for that location: " + err.Error()
	}
	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Content: msg},
	})
	return true
}

```

## `./command_access_test.go`

```go
package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCommandAccessPersistsAndDoesNotDuplicate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "commandAccess.json")

	if err := grantCommandAccess(path, "setup", "member-1"); err != nil {
		t.Fatalf("grantCommandAccess failed: %v", err)
	}
	if err := grantCommandAccess(path, "setup", "member-1"); err != nil {
		t.Fatalf("duplicate grantCommandAccess failed: %v", err)
	}

	granted, err := hasCommandAccess(path, "setup", "member-1")
	if err != nil {
		t.Fatalf("hasCommandAccess failed: %v", err)
	}
	if !granted {
		t.Fatal("expected grant to persist")
	}
	granted, err = hasCommandAccess(path, "setup", "member-2")
	if err != nil {
		t.Fatalf("hasCommandAccess failed: %v", err)
	}
	if granted {
		t.Fatal("unexpected grant for a different member")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading persisted grants failed: %v", err)
	}
	if got := string(data); got == "" {
		t.Fatal("expected persisted grant data")
	}

	removed, err := removeCommandAccess(path, "setup", "member-1")
	if err != nil {
		t.Fatalf("removeCommandAccess failed: %v", err)
	}
	if !removed {
		t.Fatal("expected grant to be removed")
	}
	granted, err = hasCommandAccess(path, "setup", "member-1")
	if err != nil {
		t.Fatalf("hasCommandAccess after removal failed: %v", err)
	}
	if granted {
		t.Fatal("grant remained after removal")
	}
	removed, err = removeCommandAccess(path, "setup", "member-1")
	if err != nil {
		t.Fatalf("removing an absent grant failed: %v", err)
	}
	if removed {
		t.Fatal("reported removing a grant that was absent")
	}
}

func TestIsGrantableCommand(t *testing.T) {
	buildCommandSpecs()

	for _, name := range []string{"setup", "/SETUP"} {
		if !isGrantableCommand(name) {
			t.Errorf("expected %q to be grantable", name)
		}
	}
	for _, name := range []string{"", "help", "grant-access", "keno-best", "not-a-command"} {
		if isGrantableCommand(name) {
			t.Errorf("expected %q not to be grantable", name)
		}
	}
}

```

## `./automod.go`

```go
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
)

var autoModMu sync.Mutex

type WarningRecord struct {
	Timestamp string `json:"timestamp"`
	WarnerID  string `json:"warner_id"`
	Reason    string `json:"reason"`
}

func getWarningsFile(guildID string) string {
	dir := dataPath("Warnings")
	return filepath.Join(dir, fmt.Sprintf("%s.json", guildID))
}

func addWarning(guildID, userID, warnerID, reason string) (int, error) {
	autoModMu.Lock()
	defer autoModMu.Unlock()

	filePath := getWarningsFile(guildID)
	warnings := make(map[string][]WarningRecord)
	data, err := os.ReadFile(filePath)
	if err != nil && !os.IsNotExist(err) {
		return 0, fmt.Errorf("reading warning records: %w", err)
	}
	if err == nil {
		if err := json.Unmarshal(data, &warnings); err != nil {
			return 0, fmt.Errorf("parsing warning records: %w", err)
		}
	}

	warnings[userID] = append(warnings[userID], WarningRecord{
		Timestamp: time.Now().Format(time.RFC3339),
		WarnerID:  warnerID,
		Reason:    reason,
	})

	out, err := json.MarshalIndent(warnings, "", "  ")
	if err != nil {
		return 0, fmt.Errorf("encoding warning records: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
		return 0, fmt.Errorf("creating warning records directory: %w", err)
	}
	if err := os.WriteFile(filePath, out, 0o600); err != nil {
		return 0, fmt.Errorf("writing warning records: %w", err)
	}
	return len(warnings[userID]), nil
}

func handleWarn(s *discordgo.Session, i *discordgo.InteractionCreate) {
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

	warnerID := ""
	if i.Member != nil {
		warnerID = i.Member.User.ID
	}

	strikeCount, err := addWarning(i.GuildID, user.ID, warnerID, reason)
	if err != nil {
		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{Content: "❌ Failed to save warning."},
		})
		return
	}

	msg := fmt.Sprintf("⚠️ **%s** has been warned. (Strike %d)\n**Reason:** %s", user.Username, strikeCount, reason)

	// --- AUTO ESCALATION ---
	if strikeCount == 3 {
		until := time.Now().Add(10 * time.Minute)
		_ = s.GuildMemberTimeout(i.GuildID, user.ID, &until)
		msg += "\n🛑 *Auto-Escalation:* User has been timed out for 10 minutes."
	} else if strikeCount >= 5 {
		_ = s.GuildMemberDeleteWithReason(i.GuildID, user.ID, "Auto-Escalation: 5 Strikes reached.")
		msg += "\n🔨 *Auto-Escalation:* User has been kicked from the server."
	}

	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Content: msg},
	})
}

type autoModConfig struct {
	Enabled     bool     `json:"enabled"`
	BannedWords []string `json:"banned_words"`
}

type autoModEvent struct {
	Timestamp      time.Time `json:"timestamp"`
	ChannelID      string    `json:"channel_id"`
	MessageID      string    `json:"message_id"`
	MatchedPhrase  string    `json:"matched_phrase"`
	MessageContent string    `json:"message_content"`
	MessageDeleted bool      `json:"message_deleted"`
}

func autoModEventsPath(guildID, userID string) (string, error) {
	if !isDiscordSnowflake(guildID) || !isDiscordSnowflake(userID) {
		return "", fmt.Errorf("invalid guild or user ID")
	}
	return dataPath("AutoModEvents", guildID, userID+".jsonl"), nil
}

func isDiscordSnowflake(id string) bool {
	if id == "" || len(id) > 20 {
		return false
	}
	for _, r := range id {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func truncateAutoModContent(content string, maxRunes int) string {
	runes := []rune(content)
	if len(runes) <= maxRunes {
		return content
	}
	return string(runes[:maxRunes]) + "…"
}

func recordAutoModEvent(guildID, userID string, event autoModEvent) error {
	path, err := autoModEventsPath(guildID, userID)
	if err != nil {
		return err
	}
	event.MessageContent = truncateAutoModContent(event.MessageContent, 500)
	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("encoding automod event: %w", err)
	}

	autoModMu.Lock()
	defer autoModMu.Unlock()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating automod events directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("opening automod events file: %w", err)
	}
	if _, err := file.Write(append(data, '\n')); err != nil {
		_ = file.Close()
		return fmt.Errorf("writing automod event: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("closing automod events file: %w", err)
	}
	return nil
}

func loadAutoModEvents(guildID, userID string, limit int) ([]autoModEvent, int, error) {
	path, err := autoModEventsPath(guildID, userID)
	if err != nil {
		return nil, 0, err
	}

	autoModMu.Lock()
	defer autoModMu.Unlock()
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return []autoModEvent{}, 0, nil
	}
	if err != nil {
		return nil, 0, fmt.Errorf("opening automod events file: %w", err)
	}
	defer file.Close()

	var events []autoModEvent
	total := 0
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var event autoModEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return nil, total, fmt.Errorf("parsing automod event %d: %w", total+1, err)
		}
		total++
		if limit <= 0 {
			continue
		}
		if len(events) < limit {
			events = append(events, event)
		} else {
			copy(events, events[1:])
			events[len(events)-1] = event
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, total, fmt.Errorf("reading automod events: %w", err)
	}
	return events, total, nil
}

func autoModConfigPath(guildID string) string {
	return dataPath("AutoMod", guildID+".json")
}

func defaultAutoModConfig() autoModConfig {
	return autoModConfig{
		Enabled:     true,
		BannedWords: []string{"discord.gg/", "http://scam-link"},
	}
}

func readAutoModConfig(path string) (autoModConfig, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return defaultAutoModConfig(), nil
	}
	if err != nil {
		return autoModConfig{}, fmt.Errorf("reading automod config: %w", err)
	}

	var config autoModConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return autoModConfig{}, fmt.Errorf("parsing automod config: %w", err)
	}
	if config.BannedWords == nil {
		config.BannedWords = []string{}
	}

	normalized := make([]string, 0, len(config.BannedWords))
	seen := make(map[string]struct{}, len(config.BannedWords))
	for _, word := range config.BannedWords {
		word = strings.ToLower(strings.TrimSpace(word))
		if word == "" {
			continue
		}
		if _, exists := seen[word]; exists {
			continue
		}
		seen[word] = struct{}{}
		normalized = append(normalized, word)
	}
	sort.Strings(normalized)
	config.BannedWords = normalized
	return config, nil
}

func writeAutoModConfig(path string, config autoModConfig) error {
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding automod config: %w", err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating automod config directory: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".automod-*.tmp")
	if err != nil {
		return fmt.Errorf("creating temporary automod config: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("securing temporary automod config: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing automod config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing automod config: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replacing automod config: %w", err)
	}
	return nil
}

func loadAutoModConfig(path string) (autoModConfig, error) {
	autoModMu.Lock()
	defer autoModMu.Unlock()
	return readAutoModConfig(path)
}

func updateAutoModConfig(path string, update func(*autoModConfig) error) error {
	autoModMu.Lock()
	defer autoModMu.Unlock()

	config, err := readAutoModConfig(path)
	if err != nil {
		return err
	}
	if err := update(&config); err != nil {
		return err
	}
	return writeAutoModConfig(path, config)
}

func matchedBannedWord(content string, words []string) string {
	content = strings.ToLower(content)
	for _, word := range words {
		if strings.Contains(content, word) {
			return word
		}
	}
	return ""
}

func handleAutoModCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if i.Member == nil || i.Member.User == nil {
		respondAutoMod(s, i, "❌ This command can only be used by a server member.")
		return
	}
	permissions, err := s.UserChannelPermissions(i.Member.User.ID, i.ChannelID)
	if err != nil {
		log.Printf("failed to check automod permissions for member %s: %v", i.Member.User.ID, err)
		respondAutoMod(s, i, "❌ Could not verify your permissions; please try again later.")
		return
	}
	if permissions&discordgo.PermissionAdministrator == 0 && permissions&discordgo.PermissionManageMessages == 0 {
		respondAutoMod(s, i, "❌ You need Manage Messages or Administrator permission to configure AutoMod.")
		return
	}

	options := i.ApplicationCommandData().Options
	if len(options) == 0 {
		respondAutoMod(s, i, "❌ Choose an AutoMod command.")
		return
	}
	subcommand := options[0]
	path := autoModConfigPath(i.GuildID)

	switch subcommand.Name {
	case "banned-word-add":
		word := autoModWordOption(subcommand)
		word = strings.ToLower(strings.TrimSpace(word))
		if word == "" {
			respondAutoMod(s, i, "❌ The banned word or phrase cannot be empty.")
			return
		}
		err := updateAutoModConfig(path, func(config *autoModConfig) error {
			for _, existing := range config.BannedWords {
				if existing == word {
					return fmt.Errorf("that word or phrase is already banned")
				}
			}
			config.BannedWords = append(config.BannedWords, word)
			sort.Strings(config.BannedWords)
			return nil
		})
		if err != nil {
			log.Printf("failed to add automod word for guild %s: %v", i.GuildID, err)
			respondAutoMod(s, i, "❌ Could not add that banned word or phrase: "+err.Error())
			return
		}
		respondAutoMod(s, i, fmt.Sprintf("✅ Added `%s` to the AutoMod banned-word list.", word))

	case "banned-word-remove":
		word := strings.ToLower(strings.TrimSpace(autoModWordOption(subcommand)))
		if word == "" {
			respondAutoMod(s, i, "❌ Provide the banned word or phrase to remove.")
			return
		}
		removed := false
		err := updateAutoModConfig(path, func(config *autoModConfig) error {
			words := config.BannedWords[:0]
			for _, existing := range config.BannedWords {
				if existing == word {
					removed = true
					continue
				}
				words = append(words, existing)
			}
			config.BannedWords = words
			if !removed {
				return fmt.Errorf("that word or phrase is not in the banned list")
			}
			return nil
		})
		if err != nil {
			log.Printf("failed to remove automod word for guild %s: %v", i.GuildID, err)
			respondAutoMod(s, i, "❌ Could not remove that banned word or phrase: "+err.Error())
			return
		}
		respondAutoMod(s, i, fmt.Sprintf("✅ Removed `%s` from the AutoMod banned-word list.", word))

	case "banned-word-list":
		config, err := loadAutoModConfig(path)
		if err != nil {
			log.Printf("failed to load automod config for guild %s: %v", i.GuildID, err)
			respondAutoMod(s, i, "❌ Could not load the AutoMod banned-word list.")
			return
		}
		if len(config.BannedWords) == 0 {
			respondAutoMod(s, i, "The AutoMod banned-word list is empty.")
			return
		}
		const maxListedWords = 15
		words := config.BannedWords
		message := strings.Join(words[:min(len(words), maxListedWords)], "\n")
		if len(words) > maxListedWords {
			message += fmt.Sprintf("\n…and %d more.", len(words)-maxListedWords)
		}
		respondAutoMod(s, i, fmt.Sprintf("**AutoMod banned words (%d):**\n%s", len(words), message))

	case "enable", "disable":
		enabled := subcommand.Name == "enable"
		err := updateAutoModConfig(path, func(config *autoModConfig) error {
			config.Enabled = enabled
			return nil
		})
		if err != nil {
			log.Printf("failed to update automod state for guild %s: %v", i.GuildID, err)
			respondAutoMod(s, i, "❌ Could not update AutoMod settings.")
			return
		}
		state := "disabled"
		if enabled {
			state = "enabled"
		}
		respondAutoMod(s, i, "✅ AutoMod is now "+state+".")

	case "status":
		config, err := loadAutoModConfig(path)
		if err != nil {
			log.Printf("failed to load automod config for guild %s: %v", i.GuildID, err)
			respondAutoMod(s, i, "❌ Could not load AutoMod settings.")
			return
		}
		state := "disabled"
		if config.Enabled {
			state = "enabled"
		}
		respondAutoMod(s, i, fmt.Sprintf("AutoMod is **%s** with **%d** banned words or phrases.", state, len(config.BannedWords)))

	case "view":
		var user *discordgo.User
		for _, option := range subcommand.Options {
			if option.Name == "user" {
				user = option.UserValue(s)
				break
			}
		}
		if user == nil {
			respondAutoMod(s, i, "❌ Select a user whose AutoMod events you want to view.")
			return
		}
		const maxEventsToShow = 5
		events, total, err := loadAutoModEvents(i.GuildID, user.ID, maxEventsToShow)
		if err != nil {
			log.Printf("failed to load automod events for member %s in guild %s: %v", user.ID, i.GuildID, err)
			respondAutoMod(s, i, "❌ Could not load AutoMod events for that user.")
			return
		}
		if total == 0 {
			respondAutoMod(s, i, fmt.Sprintf("No AutoMod events recorded for <@%s>.", user.ID))
			return
		}
		var output strings.Builder
		fmt.Fprintf(&output, "**AutoMod events for <@%s> (%d total; showing latest %d):**", user.ID, total, len(events))
		for index := len(events) - 1; index >= 0; index-- {
			event := events[index]
			deleted := "message not deleted"
			if event.MessageDeleted {
				deleted = "message deleted"
			}
			phrase := strings.ReplaceAll(event.MatchedPhrase, "`", "\\`")
			fmt.Fprintf(&output, "\n\n<t:%d:f> in <#%s> — matched `%s`; %s.",
				event.Timestamp.Unix(), event.ChannelID, phrase, deleted)
			if event.MessageContent != "" {
				fmt.Fprintf(&output, "\n> %s", truncateAutoModContent(event.MessageContent, 160))
			}
		}
		if total > len(events) {
			fmt.Fprintf(&output, "\n\n%d older event(s) not shown.", total-len(events))
		}
		respondAutoMod(s, i, output.String())

	default:
		respondAutoMod(s, i, "❌ Unknown AutoMod command.")
	}
}

func autoModWordOption(command *discordgo.ApplicationCommandInteractionDataOption) string {
	for _, option := range command.Options {
		if option.Name == "word" {
			return option.StringValue()
		}
	}
	return ""
}

func respondAutoMod(s *discordgo.Session, i *discordgo.InteractionCreate, content string) {
	if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content:         content,
			Flags:           discordgo.MessageFlagsEphemeral,
			AllowedMentions: &discordgo.MessageAllowedMentions{},
		},
	}); err != nil {
		log.Printf("failed to respond to /automod in guild %s: %v", i.GuildID, err)
	}
}

// checkAutoMod runs on every message to block configured banned words or phrases.
func checkAutoMod(s *discordgo.Session, m *discordgo.MessageCreate) {
	if m.GuildID == "" || m.Author == nil || m.Author.Bot {
		return
	}

	config, err := loadAutoModConfig(autoModConfigPath(m.GuildID))
	if err != nil {
		log.Printf("failed to load automod config for guild %s: %v", m.GuildID, err)
		return
	}
	matchedPhrase := matchedBannedWord(m.Content, config.BannedWords)
	if !config.Enabled || matchedPhrase == "" {
		return
	}

	deleteErr := s.ChannelMessageDelete(m.ChannelID, m.ID)
	if deleteErr != nil {
		log.Printf("failed to delete automod message %s in channel %s: %v", m.ID, m.ChannelID, deleteErr)
	}
	event := autoModEvent{
		Timestamp:      time.Now().UTC(),
		ChannelID:      m.ChannelID,
		MessageID:      m.ID,
		MatchedPhrase:  matchedPhrase,
		MessageContent: m.Content,
		MessageDeleted: deleteErr == nil,
	}
	if err := recordAutoModEvent(m.GuildID, m.Author.ID, event); err != nil {
		log.Printf("failed to record automod event for member %s in guild %s: %v", m.Author.ID, m.GuildID, err)
	}
	if deleteErr != nil {
		return
	}
	warnerID := ""
	if s.State != nil && s.State.User != nil {
		warnerID = s.State.User.ID
	}
	if _, err := addWarning(m.GuildID, m.Author.ID, warnerID, "AutoMod: Banned phrase detected."); err != nil {
		log.Printf("failed to record automod warning for member %s in guild %s: %v", m.Author.ID, m.GuildID, err)
	}

	ch, err := s.UserChannelCreate(m.Author.ID)
	if err != nil {
		log.Printf("failed to open automod notification DM for member %s: %v", m.Author.ID, err)
		return
	}
	if _, err := s.ChannelMessageSend(ch.ID, "⚠️ Your message was deleted for matching a banned word or phrase."); err != nil {
		log.Printf("failed to send automod notification DM to member %s: %v", m.Author.ID, err)
	}
}

```

## `./birthdays.go`

```go
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

```

## `./backup.go`

```go
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/bwmarrin/discordgo"
)

func handleBackup(s *discordgo.Session, i *discordgo.InteractionCreate) {
	// 1. Immediately defer the response so Discord doesn't time out
	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	})

	// 2. Skip the strict subcommand check!
	// If Discord's cache sends `/backup` without `create`, we just run it anyway.

	// 3. Proceed with the backup
	guild, err := s.Guild(i.GuildID)
	if err != nil {
		_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{Content: "❌ Failed to fetch guild info."})
		return
	}

	channels, _ := s.GuildChannels(i.GuildID)
	roles, _ := s.GuildRoles(i.GuildID)

	backupData := map[string]interface{}{
		"guild_name": guild.Name,
		"guild_id":   guild.ID,
		"timestamp":  time.Now().Format(time.RFC3339),
		"channels":   channels,
		"roles":      roles,
	}

	data, err := json.MarshalIndent(backupData, "", "  ")
	if err != nil {
		_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{Content: "❌ Serialization error: " + err.Error()})
		return
	}

	backupDir := dataPath("Backups", i.GuildID)
	_ = os.MkdirAll(backupDir, 0755)

	filename := fmt.Sprintf("backup_%s.json", time.Now().Format("20060102_150405"))
	filepath := filepath.Join(backupDir, filename)

	if err := os.WriteFile(filepath, data, 0644); err != nil {
		_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{Content: "❌ Failed to write backup: " + err.Error()})
		return
	}

	f, _ := os.Open(filepath)
	defer f.Close()

	_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
		Content: "✅ **Server Snapshot Created** and saved under the configured data directory's `Backups/` folder.",
		Files:   []*discordgo.File{{Name: filename, Reader: f}},
	})
}

func handleTranscript(s *discordgo.Session, i *discordgo.InteractionCreate) {
	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	})

	limit := 100
	for _, opt := range i.ApplicationCommandData().Options {
		if opt.Name == "limit" {
			limit = int(opt.IntValue())
		}
	}
	if limit > 5000 {
		limit = 5000
	}

	var allMessages []*discordgo.Message
	beforeID := ""

	for len(allMessages) < limit {
		fetch := 100
		if limit-len(allMessages) < 100 {
			fetch = limit - len(allMessages)
		}

		msgs, err := s.ChannelMessages(i.ChannelID, fetch, beforeID, "", "")
		if err != nil || len(msgs) == 0 {
			break
		}

		allMessages = append(allMessages, msgs...)
		beforeID = msgs[len(msgs)-1].ID
	}

	// Reverse to chronological order
	for x, y := 0, len(allMessages)-1; x < y; x, y = x+1, y-1 {
		allMessages[x], allMessages[y] = allMessages[y], allMessages[x]
	}

	transcript := fmt.Sprintf("# Transcript for channel %s\nGenerated: %s\n\n", i.ChannelID, time.Now().Format(time.RFC1123))
	for _, m := range allMessages {
		// m.Timestamp is already a time.Time object, so we just format it directly!
		transcript += fmt.Sprintf("[%s] **%s**: %s\n", m.Timestamp.Format("2006-01-02 15:04:05"), m.Author.Username, m.Content)
	}

	filename := fmt.Sprintf("transcript_%s.md", i.ChannelID)
	_ = os.WriteFile(filename, []byte(transcript), 0644)
	defer os.Remove(filename)

	f, _ := os.Open(filename)
	defer f.Close()

	_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
		Content: fmt.Sprintf("✅ Exported %d messages.", len(allMessages)),
		Files:   []*discordgo.File{{Name: filename, Reader: f}},
	})
}

```

## `./lyrics_test.go`

```go
package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSearchGeniusFiltersAndValidatesResults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("q"); got != "Radiohead OK Computer" {
			t.Errorf("query = %q, want searched text", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"response": {
				"sections": [
					{"type":"album","hits":[
						{"result":{"title":"OK Computer","primary_artist_names":"Radiohead","url":"https://genius.com/albums/Radiohead/Ok-computer"}},
						{"result":{"title":"Unsafe URL","url":"https://example.com/album"}}
					]},
					{"type":"song","hits":[
						{"result":{"title":"Paranoid Android","artist_names":"Radiohead","url":"https://genius.com/Radiohead-paranoid-android-lyrics"}}
					]}
				]
			}
		}`))
	}))
	defer server.Close()

	results, err := searchGeniusWithClient(server.Client(), server.URL, "album", " Radiohead OK Computer ")
	if err != nil {
		t.Fatalf("searchGeniusWithClient returned error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want one valid album result", len(results))
	}
	if results[0].Title != "OK Computer" || results[0].PrimaryArtistNames != "Radiohead" {
		t.Errorf("unexpected result metadata: %+v", results[0])
	}
	if !strings.HasPrefix(results[0].URL, "https://genius.com/") {
		t.Errorf("result URL was not retained as a Genius HTTPS URL: %q", results[0].URL)
	}
}

func TestSearchGeniusRejectsBadInputsAndHTTPFailures(t *testing.T) {
	if _, err := searchGeniusWithClient(http.DefaultClient, "://bad-url", "song", "title"); err == nil {
		t.Fatal("expected invalid endpoint error")
	}
	if _, err := searchGeniusWithClient(http.DefaultClient, "https://genius.com", "lyrics", "title"); err == nil {
		t.Fatal("expected unsupported search type error")
	}
	if _, err := searchGeniusWithClient(http.DefaultClient, "https://genius.com", "song", " "); err == nil {
		t.Fatal("expected empty query error")
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	if _, err := searchGeniusWithClient(server.Client(), server.URL, "song", "title"); err == nil {
		t.Fatal("expected HTTP status error")
	}
}

func TestFormatGeniusSearchResultsContainsMetadataAndNoLyrics(t *testing.T) {
	result := geniusSearchResult{
		Title:                 "Example Song",
		PrimaryArtistNames:    "Example Artist",
		ReleaseDateForDisplay: "January 1, 2020",
		URL:                   "https://genius.com/example-song-lyrics",
	}
	formatted := formatGeniusSearchResults("song", "Example Song", []geniusSearchResult{result})
	for _, want := range []string{"Example Song", "Example Artist", "January 1, 2020", result.URL, "metadata and links only"} {
		if !strings.Contains(formatted, want) {
			t.Errorf("formatted results %q do not contain %q", formatted, want)
		}
	}

	empty := formatGeniusSearchResults("album", "No Such Album", nil)
	if !strings.Contains(empty, "No albums found") {
		t.Errorf("empty result message = %q, want no-albums message", empty)
	}
}

func TestFormatGeniusSearchResultsFitsDiscordLimit(t *testing.T) {
	results := make([]geniusSearchResult, geniusResultLimit)
	for index := range results {
		results[index] = geniusSearchResult{
			Title:                 strings.Repeat("t", 300),
			PrimaryArtistNames:    strings.Repeat("a", 300),
			ReleaseDateForDisplay: strings.Repeat("d", 100),
			URL:                   "https://genius.com/" + strings.Repeat("x", 400),
		}
	}

	formatted := formatGeniusSearchResults("album", strings.Repeat("q", 300), results)
	if length := len([]rune(formatted)); length > geniusReplyLimit {
		t.Fatalf("formatted results have %d runes, maximum is %d", length, geniusReplyLimit)
	}
	if !strings.Contains(formatted, "Additional results omitted") {
		t.Fatal("expected oversized results to be clearly marked as omitted")
	}
}

func TestLyricsCommandIsPublicAndDefinesBothSearches(t *testing.T) {
	buildCommandSpecs()
	if !isPublicCommand("lyrics") {
		t.Fatal("/lyrics is not marked as a public command")
	}

	for _, spec := range commandSpecs {
		if spec.Command.Name != "lyrics" {
			continue
		}
		if len(spec.Command.Options) != 2 || spec.Command.Options[0].Name != "fetch-song" || spec.Command.Options[1].Name != "fetch-album" {
			t.Fatalf("/lyrics options do not define fetch-song and fetch-album: %+v", spec.Command.Options)
		}
		if !spec.Command.Options[0].Options[0].Required {
			t.Fatal("/lyrics fetch-song must require its search term")
		}
		albumOptions := spec.Command.Options[1].Options
		if len(albumOptions) != 2 || !albumOptions[0].Required || albumOptions[1].Required {
			t.Fatal("/lyrics fetch-album must require artist and make album optional")
		}
		return
	}
	t.Fatal("/lyrics was not registered")
}

func TestValidGeniusResultURL(t *testing.T) {
	for _, test := range []struct {
		url  string
		want bool
	}{
		{"https://genius.com/song", true},
		{"https://www.genius.com/song", true},
		{"http://genius.com/song", false},
		{"https://notgenius.com/song", false},
		{"https://genius.com.example.org/song", false},
	} {
		if got := validGeniusResultURL(test.url) != ""; got != test.want {
			t.Errorf("validGeniusResultURL(%q) accepted = %t, want %t", test.url, got, test.want)
		}
	}
}

```

## `./push.sh`

```bash
#!/usr/bin/env bash
set -euo pipefail

REPO_DIR="/home/daniel/Repos/Sunfire"
LOG_FILE="$REPO_DIR/push.sh.log"

# Prompt for passwords at the start
GPG_KEY="E453F9AEC380D33E4BA7FAC69B542AA270F867E5"
echo -n "Sudo password (daniel): "
read -s SUDO_PASS
echo ""
echo -n "GPG passphrase ($GPG_KEY): "
read -s GPG_PASS
echo ""

# Unlock GPG agent so signed commits work non-interactively
echo "$GPG_PASS" | gpg-connect-agent "UPDATESTARTUPTTY" /bye 2>/dev/null || true
echo "" | gpg --batch --pinentry-mode loopback --passphrase "$GPG_PASS" --sign /dev/null 2>/dev/null || true

# Initialize log
{
  echo "========================================"
  echo "Sunfire push started: $(date)"
  echo "========================================"
  echo ""
} > "$LOG_FILE"

cd "$REPO_DIR"

log_step() {
  echo "[$(date '+%Y-%m-%d %H:%M:%S')] $*" | tee -a "$LOG_FILE"
}

log_step "Running tests..."
{
  go test ./... 2>&1
  rc=$?
  if [ $rc -ne 0 ]; then
    log_step "Tests failed (exit $rc), aborting."
    exit $rc
  fi
} | tee -a "$LOG_FILE"
log_step "Tests passed."
echo "" >> "$LOG_FILE"

log_step "Building Sunfire..."
{
  go build -o Sunfire . 2>&1
  rc=$?
  if [ $rc -ne 0 ]; then
    log_step "Build failed (exit $rc), aborting."
    exit $rc
  fi
} | tee -a "$LOG_FILE"
log_step "Build succeeded. Binary: $(ls -lh "$REPO_DIR/Sunfire" | awk '{print $5}')"
echo "" >> "$LOG_FILE"

log_step "Restarting service..."
{
  echo "$SUDO_PASS" | sudo -S systemctl restart Sunfire 2>&1
  rc=$?
  if [ $rc -ne 0 ]; then
    log_step "Restart failed (exit $rc)."
    exit $rc
  fi
} | tee -a "$LOG_FILE"
log_step "Service restarted."
echo "" >> "$LOG_FILE"

log_step "Updating documentation..."
{
  repo2md 2>&1
  rc=$?
  if [ $rc -ne 0 ]; then
    log_step "repo2md failed (exit $rc), continuing..."
  fi
} | tee -a "$LOG_FILE"
echo "" >> "$LOG_FILE"

log_step "Pushing to remote..."
{
  push-to-remote 2>&1
  rc=$?
  if [ $rc -ne 0 ]; then
    log_step "Push failed (exit $rc)."
    exit $rc
  fi
} | tee -a "$LOG_FILE"
log_step "Push completed."
echo "" >> "$LOG_FILE"

log_step "Done: $(date)"

```

## `./notifier_freestuff.go`

```go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
)

type freeStuffState struct {
	Guilds map[string]struct {
		InitializedSources map[string]bool `json:"initializedSources"`
		SeenItems          struct {
			GGDeals     []string `json:"ggDeals"`
			ProductHunt []string `json:"productHunt"`
		} `json:"seenItems"`
	} `json:"guilds"`
}

type ggDealsResponse struct {
	Title string `json:"title"`
	URL   string `json:"url"`
}

var (
	freeStuffMu        sync.Mutex
	freeStuffCache     freeStuffState
	freeStuffCachePath string
)

func initFreeStuffCache() {
	freeStuffCachePath = dataPath("freeStuffNotifierCache.json")
	freeStuffCache.Guilds = make(map[string]struct {
		InitializedSources map[string]bool `json:"initializedSources"`
		SeenItems          struct {
			GGDeals     []string `json:"ggDeals"`
			ProductHunt []string `json:"productHunt"`
		} `json:"seenItems"`
	})

	data, err := os.ReadFile(freeStuffCachePath)
	if err == nil {
		if err := json.Unmarshal(data, &freeStuffCache); err != nil {
			log.Printf("FreeStuff notifier: parse cache %s: %v", freeStuffCachePath, err)
			freeStuffCache.Guilds = make(map[string]struct {
				InitializedSources map[string]bool `json:"initializedSources"`
				SeenItems          struct {
					GGDeals     []string `json:"ggDeals"`
					ProductHunt []string `json:"productHunt"`
				} `json:"seenItems"`
			})
		}
	} else if !os.IsNotExist(err) {
		log.Printf("FreeStuff notifier: read cache %s: %v", freeStuffCachePath, err)
	}
	if freeStuffCache.Guilds == nil {
		freeStuffCache.Guilds = make(map[string]struct {
			InitializedSources map[string]bool `json:"initializedSources"`
			SeenItems          struct {
				GGDeals     []string `json:"ggDeals"`
				ProductHunt []string `json:"productHunt"`
			} `json:"seenItems"`
		})
	}
}

func saveFreeStuffCache() error {
	if freeStuffCachePath == "" {
		return fmt.Errorf("FreeStuff cache path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(freeStuffCachePath), 0755); err != nil {
		return fmt.Errorf("creating FreeStuff cache directory: %w", err)
	}
	data, err := json.MarshalIndent(freeStuffCache, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding FreeStuff cache: %w", err)
	}
	if err := os.WriteFile(freeStuffCachePath, data, 0644); err != nil {
		return fmt.Errorf("writing FreeStuff cache %s: %w", freeStuffCachePath, err)
	}
	return nil
}

func checkFreeStuffUpdates(s *discordgo.Session, cfg *Config) {
	guilds := loadGuildSettings()
	freeStuffMu.Lock()
	defer freeStuffMu.Unlock()

	client := notifierClient(15 * time.Second)
	feedURL := "https://gg.deals/us/news/feed/"

	resp, err := client.Get(feedURL)
	if err != nil {
		log.Printf("FreeStuff notifier: fetch GG.deals feed: %v", err)
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("FreeStuff notifier: read GG.deals feed: %v", err)
		return
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Printf("FreeStuff notifier: GG.deals feed returned HTTP %d", resp.StatusCode)
		return
	}
	xmlContent := string(body)

	// Simple RSS item parser for <item><title>...</title><link>...</link></item>
	var items []struct {
		Title string
		Link  string
	}

	rawItems := strings.Split(xmlContent, "<item>")
	for _, raw := range rawItems[1:] {
		titleStart := strings.Index(raw, "<title>")
		titleEnd := strings.Index(raw, "</title>")
		linkStart := strings.Index(raw, "<link>")
		linkEnd := strings.Index(raw, "</link>")

		if titleStart != -1 && titleEnd != -1 && linkStart != -1 && linkEnd != -1 {
			title := raw[titleStart+7 : titleEnd]
			title = strings.TrimPrefix(title, "<![CDATA[")
			title = strings.TrimSuffix(title, "]]>")
			link := raw[linkStart+6 : linkEnd]
			link = strings.TrimPrefix(link, "<![CDATA[")
			link = strings.TrimSuffix(link, "]]>")

			// Only process freebies / deals
			if strings.Contains(strings.ToLower(title), "free") || strings.Contains(strings.ToLower(link), "freebie") {
				items = append(items, struct {
					Title string
					Link  string
				}{Title: strings.TrimSpace(title), Link: strings.TrimSpace(link)})
			}
		}
	}

	for guildID, gCfg := range guilds {
		if !gCfg.FreeStuffEnabled || gCfg.FreeStuffChannelID == "" {
			continue
		}

		gState, exists := freeStuffCache.Guilds[guildID]
		if !exists {
			gState.InitializedSources = map[string]bool{"ggDeals": true}
			gState.SeenItems.GGDeals = []string{}
			gState.SeenItems.ProductHunt = []string{}
		}

		seenMap := make(map[string]bool)
		for _, link := range gState.SeenItems.GGDeals {
			seenMap[link] = true
		}

		type freePost struct {
			link    string
			content string
		}
		var newPosts []freePost
		for _, item := range items {
			if !seenMap[item.Link] {
				newPosts = append(newPosts, freePost{
					link:    item.Link,
					content: fmt.Sprintf("🎁 **Freebie Deal Alert!**\n**%s**\n%s", item.Title, item.Link),
				})
				seenMap[item.Link] = true
				if len(newPosts) >= 3 {
					break
				}
			}
		}

		if len(newPosts) > 0 {
			header := ""
			if gCfg.FreeStuffRoleID != nil && *gCfg.FreeStuffRoleID != "" {
				header = fmt.Sprintf("<@&%s> ", *gCfg.FreeStuffRoleID)
			}
			for _, post := range newPosts {
				if _, err := sendNotifierMessage(s, gCfg.FreeStuffChannelID, header+post.content); err != nil {
					log.Printf("FreeStuff notifier: send deal %s to guild %s: %v", post.link, guildID, err)
					continue
				}
				previousState := gState
				previousState.SeenItems.GGDeals = append([]string(nil), gState.SeenItems.GGDeals...)
				gState.SeenItems.GGDeals = append(gState.SeenItems.GGDeals, post.link)
				freeStuffCache.Guilds[guildID] = gState
				if err := saveFreeStuffCache(); err != nil {
					gState = previousState
					if !exists {
						delete(freeStuffCache.Guilds, guildID)
					} else {
						freeStuffCache.Guilds[guildID] = previousState
					}
					log.Printf("FreeStuff notifier: persist delivered deal %s for guild %s: %v", post.link, guildID, err)
				} else {
					exists = true
				}
			}
		}
	}
}

// startFreeStuffNotifierDaemon runs periodic free game / software checks
func startFreeStuffNotifierDaemon(ctx context.Context, s *discordgo.Session) {
	initFreeStuffCache()
	cfg, err := loadConfig()
	if err != nil {
		log.Printf("FreeStuff notifier: load config: %v", err)
		return
	}

	log.Printf("FreeStuff notifier daemon started (interval 30m)")
	runPeriodic(ctx, time.Minute, 30*time.Minute, func() { checkFreeStuffUpdates(s, cfg) })
}

```

## `./notifier_kick.go`

```go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
)

type kickCacheEntry struct {
	IsLive        bool   `json:"isLive"`
	LastStreamID  *int   `json:"lastStreamId"`
	LastMessageID string `json:"lastMessageId"`
	LastTitle     string `json:"lastTitle"`
}

type kickTokenResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
	TokenType   string `json:"token_type"`
}

type kickChannelsResponse struct {
	Data []struct {
		BroadcasterUserID int    `json:"broadcaster_user_id"`
		Slug              string `json:"slug"`
		ChannelDesc       string `json:"channel_description"`
		BannerPicture     string `json:"banner_picture"`
		StreamTitle       string `json:"stream_title"`
		Stream            struct {
			IsLive      bool   `json:"is_live"`
			IsMature    bool   `json:"is_mature"`
			Language    string `json:"language"`
			StartTime   string `json:"start_time"`
			ViewerCount int    `json:"viewer_count"`
			Thumbnail   string `json:"thumbnail"`
			Key         string `json:"key"`
		} `json:"stream"`
		Category struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
		} `json:"category"`
	} `json:"data"`
	Message string `json:"message"`
}

var (
	kickMu          sync.Mutex
	kickCache       map[string]kickCacheEntry
	kickCachePath   string
	kickCachedToken string
	kickTokenExpiry time.Time
)

func initKickCache() {
	kickCachePath = dataPath("kickNotifier-cache.json")
	kickCache = make(map[string]kickCacheEntry)

	data, err := os.ReadFile(kickCachePath)
	if err == nil {
		if err := json.Unmarshal(data, &kickCache); err != nil {
			log.Printf("Kick notifier: parse cache %s: %v", kickCachePath, err)
			kickCache = make(map[string]kickCacheEntry)
		}
	} else if !os.IsNotExist(err) {
		log.Printf("Kick notifier: read cache %s: %v", kickCachePath, err)
	}
	if kickCache == nil {
		kickCache = make(map[string]kickCacheEntry)
	}
}

func saveKickCache() error {
	if kickCachePath == "" {
		return fmt.Errorf("Kick cache path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(kickCachePath), 0755); err != nil {
		return fmt.Errorf("creating Kick cache directory: %w", err)
	}
	data, err := json.MarshalIndent(kickCache, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding Kick cache: %w", err)
	}
	if err := os.WriteFile(kickCachePath, data, 0644); err != nil {
		return fmt.Errorf("writing Kick cache %s: %w", kickCachePath, err)
	}
	return nil
}

func getKickAccessToken(cfg *Config) (string, error) {
	if cfg.KickClientID == "" || cfg.KickClientSecret == "" {
		return "", fmt.Errorf("missing kick credentials")
	}

	if kickCachedToken != "" && time.Now().Before(kickTokenExpiry) {
		return kickCachedToken, nil
	}

	data := url.Values{}
	data.Set("grant_type", "client_credentials")
	data.Set("client_id", cfg.KickClientID)
	data.Set("client_secret", cfg.KickClientSecret)

	req, err := http.NewRequest("POST", "https://id.kick.com/oauth/token", strings.NewReader(data.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := notifierClient(10 * time.Second)
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading Kick token response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("Kick token endpoint returned HTTP %d", resp.StatusCode)
	}
	var tResp kickTokenResponse
	if err := json.Unmarshal(body, &tResp); err != nil {
		return "", fmt.Errorf("decoding Kick token response: %w", err)
	}
	if tResp.AccessToken == "" || tResp.ExpiresIn <= 0 {
		return "", fmt.Errorf("Kick token response did not include a valid token and expiry")
	}

	kickCachedToken = tResp.AccessToken
	kickTokenExpiry = time.Now().Add(time.Duration(tResp.ExpiresIn-60) * time.Second)
	return kickCachedToken, nil
}

func checkKickUpdates(s *discordgo.Session, cfg *Config) {
	if cfg.KickClientID == "" || cfg.KickClientSecret == "" {
		return
	}

	guilds := loadGuildSettings()
	kickMu.Lock()
	defer kickMu.Unlock()

	client := notifierClient(10 * time.Second)

	for guildID, gCfg := range guilds {
		if !gCfg.KickEnabled || gCfg.KickChannelID == "" || len(gCfg.KickStreamers) == 0 {
			continue
		}

		token, err := getKickAccessToken(cfg)
		if err != nil {
			log.Printf("Kick notifier token error: %v", err)
			return
		}

		for _, streamer := range gCfg.KickStreamers {
			slug := strings.ToLower(strings.TrimSpace(streamer))
			if slug == "" {
				continue
			}

			cacheKey := fmt.Sprintf("%s:%s", guildID, slug)
			cached := kickCache[cacheKey]

			apiURL := fmt.Sprintf("https://api.kick.com/public/v1/channels?slug=%s", url.QueryEscape(slug))
			req, err := http.NewRequest("GET", apiURL, nil)
			if err != nil {
				continue
			}
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("Accept", "application/json")

			resp, err := client.Do(req)
			if err != nil {
				log.Printf("Kick notifier: fetch channel %s: %v", slug, err)
				continue
			}

			body, readErr := io.ReadAll(resp.Body)
			resp.Body.Close()
			if readErr != nil {
				log.Printf("Kick notifier: read channel response for %s: %v", slug, readErr)
				continue
			}
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				log.Printf("Kick notifier: channel request for %s returned HTTP %d", slug, resp.StatusCode)
				continue
			}

			var kResp kickChannelsResponse
			if err := json.Unmarshal(body, &kResp); err != nil {
				log.Printf("Kick notifier: decode channel response for %s: %v", slug, err)
				continue
			}
			if len(kResp.Data) == 0 {
				log.Printf("Kick notifier: no channel data returned for %s", slug)
				continue
			}

			ch := kResp.Data[0]
			isLive := ch.Stream.IsLive
			if isLive && !cached.IsLive {
				streamTitle := "Streaming on Kick"
				if ch.StreamTitle != "" {
					streamTitle = ch.StreamTitle
				}

				kickURL := fmt.Sprintf("https://kick.com/%s", slug)
				embed := &discordgo.MessageEmbed{
					Title:       fmt.Sprintf("%s is LIVE on Kick!", streamer),
					URL:         kickURL,
					Description: streamTitle,
					Color:       0x53FC18, // Kick green
					Timestamp:   time.Now().Format(time.RFC3339),
				}

				if ch.Stream.Thumbnail != "" {
					embed.Image = &discordgo.MessageEmbedImage{
						URL: ch.Stream.Thumbnail,
					}
				}

				var mention string
				if gCfg.KickStreamingRoleID != nil && *gCfg.KickStreamingRoleID != "" {
					mention = fmt.Sprintf("<@&%s>", *gCfg.KickStreamingRoleID)
				}

				msg, err := sendNotifierComplex(s, gCfg.KickChannelID, &discordgo.MessageSend{
					Content: mention,
					Embed:   embed,
				})
				if err != nil {
					log.Printf("Kick notifier: send live announcement for %s: %v", slug, err)
					continue
				}
				if msg == nil || msg.ID == "" {
					log.Printf("Kick notifier: live announcement for %s returned no message ID", slug)
					continue
				}
				kickCache[cacheKey] = kickCacheEntry{
					IsLive:        true,
					LastStreamID:  nil,
					LastMessageID: msg.ID,
					LastTitle:     streamTitle,
				}
				if err := saveKickCache(); err != nil {
					kickCache[cacheKey] = cached
					log.Printf("Kick notifier: persist live state for %s after delivery: %v", slug, err)
				}
			} else if !isLive && cached.IsLive {
				// Update Discord message to indicate stream ended if we have the message ID
				if cached.LastMessageID != "" && gCfg.KickChannelID != "" {
					kickURL := fmt.Sprintf("https://kick.com/%s", slug)
					title := "Streaming on Kick"
					if cached.LastTitle != "" {
						title = cached.LastTitle
					}
					offlineEmbed := &discordgo.MessageEmbed{
						Title:       fmt.Sprintf("%s was LIVE on Kick (Offline)", streamer),
						URL:         kickURL,
						Description: fmt.Sprintf("~~%s~~\n\n🔴 *Stream has ended.*", title),
						Color:       0x747F8D, // Discord muted gray
						Timestamp:   time.Now().Format(time.RFC3339),
					}
					emptyContent := ""
					if _, err := editNotifierComplex(s, &discordgo.MessageEdit{
						Channel: gCfg.KickChannelID,
						ID:      cached.LastMessageID,
						Content: &emptyContent,
						Embed:   offlineEmbed,
					}); err != nil {
						log.Printf("Kick notifier: update offline announcement for %s: %v", slug, err)
						continue
					}
				} else {
					log.Printf("Kick notifier: stream %s is offline but no announcement message ID is cached", slug)
				}

				kickCache[cacheKey] = kickCacheEntry{
					IsLive:        false,
					LastStreamID:  cached.LastStreamID,
					LastMessageID: cached.LastMessageID,
					LastTitle:     cached.LastTitle,
				}
				if err := saveKickCache(); err != nil {
					kickCache[cacheKey] = cached
					log.Printf("Kick notifier: persist offline state for %s after delivery: %v", slug, err)
				}
			}
		}
	}
}

// startKickNotifierDaemon runs periodic Kick livestream checks
func startKickNotifierDaemon(ctx context.Context, s *discordgo.Session) {
	initKickCache()
	cfg, err := loadConfig()
	if err != nil {
		log.Printf("Kick notifier: load config: %v", err)
		return
	}
	if cfg.KickClientID == "" || cfg.KickClientSecret == "" {
		log.Printf("Kick notifier not started: Kick credentials are not configured")
		return
	}

	interval := 5 * time.Minute
	if cfg.KickPollIntervalMs > 0 {
		interval = time.Duration(cfg.KickPollIntervalMs) * time.Millisecond
	}

	log.Printf("Kick notifier daemon started (interval %v)", interval)
	runPeriodic(ctx, 15*time.Second, interval, func() { checkKickUpdates(s, cfg) })
}

```

## `./message_log_test.go`

```go
package main

import (
	"net/url"
	"strings"
	"testing"
)

func TestPostgresConnectionStringConfigAndEnvironment(t *testing.T) {
	t.Setenv("POSTGRES_CONNECTION_STRING", " postgres://environment ")

	got, err := postgresConnectionString(&Config{PostgresConnectionString: " postgres://config "})
	if err != nil {
		t.Fatalf("postgresConnectionString(config) error = %v", err)
	}
	parsed, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parsing config connection string: %v", err)
	}
	if parsed.Query().Get("sslmode") != "disable" {
		t.Fatalf("config connection string sslmode = %q; want disable", parsed.Query().Get("sslmode"))
	}

	got, err = postgresConnectionString(&Config{})
	if err != nil {
		t.Fatalf("postgresConnectionString(empty config) error = %v", err)
	}
	if !strings.HasPrefix(got, "postgres://environment?") {
		t.Fatalf("postgresConnectionString(empty config) = %q; want environment URL", got)
	}

	got, err = postgresConnectionString(&Config{PostgresConnectionString: "host=db sslmode=require"})
	if err != nil {
		t.Fatalf("postgresConnectionString(keyword DSN) error = %v", err)
	}
	if !strings.HasSuffix(got, "sslmode=require sslmode=disable") {
		t.Fatalf("keyword DSN = %q; want sslmode=disable to override the configured mode", got)
	}
}

func TestParseBigIntRequiresFullDecimalValue(t *testing.T) {
	tests := []struct {
		input   string
		want    int64
		wantErr bool
	}{
		{input: "1557930395868209162", want: 1557930395868209162},
		{input: "123abc", wantErr: true},
		{input: "9223372036854775808", wantErr: true},
	}

	for _, tt := range tests {
		got, err := parseBigInt(tt.input)
		if (err != nil) != tt.wantErr {
			t.Errorf("parseBigInt(%q) error = %v, wantErr %t", tt.input, err, tt.wantErr)
			continue
		}
		if err == nil && got != tt.want {
			t.Errorf("parseBigInt(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

```

## `./command_access.go`

```go
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/bwmarrin/discordgo"
)

var commandAccessMu sync.Mutex

func loadCommandAccess(path string) (map[string][]string, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string][]string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading command grants: %w", err)
	}

	var grants map[string][]string
	if err := json.Unmarshal(data, &grants); err != nil {
		return nil, fmt.Errorf("parsing command grants: %w", err)
	}
	if grants == nil {
		grants = map[string][]string{}
	}
	return grants, nil
}

func saveCommandAccess(path string, grants map[string][]string) error {
	data, err := json.MarshalIndent(grants, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding command grants: %w", err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating command grants directory: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".command-access-*.tmp")
	if err != nil {
		return fmt.Errorf("creating temporary command grants file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("securing temporary command grants file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing command grants: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing command grants file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replacing command grants file: %w", err)
	}
	return nil
}

func grantCommandAccess(path, commandName, memberID string) error {
	commandAccessMu.Lock()
	defer commandAccessMu.Unlock()

	grants, err := loadCommandAccess(path)
	if err != nil {
		return err
	}
	for _, id := range grants[commandName] {
		if id == memberID {
			return nil
		}
	}
	grants[commandName] = append(grants[commandName], memberID)
	sort.Strings(grants[commandName])
	return saveCommandAccess(path, grants)
}

func removeCommandAccess(path, commandName, memberID string) (bool, error) {
	commandAccessMu.Lock()
	defer commandAccessMu.Unlock()

	grants, err := loadCommandAccess(path)
	if err != nil {
		return false, err
	}
	ids := grants[commandName]
	for i, id := range ids {
		if id != memberID {
			continue
		}
		grants[commandName] = append(ids[:i], ids[i+1:]...)
		if len(grants[commandName]) == 0 {
			delete(grants, commandName)
		}
		if err := saveCommandAccess(path, grants); err != nil {
			return false, err
		}
		return true, nil
	}
	return false, nil
}

func hasCommandAccess(path, commandName, memberID string) (bool, error) {
	commandAccessMu.Lock()
	defer commandAccessMu.Unlock()

	grants, err := loadCommandAccess(path)
	if err != nil {
		return false, err
	}
	for _, id := range grants[commandName] {
		if id == memberID {
			return true, nil
		}
	}
	return false, nil
}

func isGrantableCommand(name string) bool {
	name = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(name)), "/")
	if name == "" || name == "grant-access" || isPublicCommand(name) {
		return false
	}
	for _, spec := range commandSpecs {
		if spec.Command.Name == name {
			return true
		}
	}
	return false
}

func handleGrantAccess(s *discordgo.Session, i *discordgo.InteractionCreate) {
	options := i.ApplicationCommandData().Options
	if len(options) == 0 {
		respondGrantAccess(s, i, "❌ Choose `grant` or `remove`.")
		return
	}
	subcommand := options[0]
	commandName := ""
	var member *discordgo.User
	for _, opt := range subcommand.Options {
		switch opt.Name {
		case "command":
			commandName = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(opt.StringValue())), "/")
		case "member":
			member = opt.UserValue(s)
		}
	}

	if !isGrantableCommand(commandName) {
		respondGrantAccess(s, i, "❌ Choose an existing restricted command; public commands and `/grant-access` cannot be delegated.")
		return
	}
	if member == nil || member.ID == "" {
		respondGrantAccess(s, i, "❌ A valid member is required.")
		return
	}

	switch subcommand.Name {
	case "grant":
		if err := grantCommandAccess(dataPath("commandAccess.json"), commandName, member.ID); err != nil {
			log.Printf("failed to grant /%s access to member %s: %v", commandName, member.ID, err)
			respondGrantAccess(s, i, "❌ Could not save command access; please try again later.")
			return
		}
		respondGrantAccess(s, i, fmt.Sprintf("✅ Granted <@%s> access to `/%s`.", member.ID, commandName))
	case "remove":
		removed, err := removeCommandAccess(dataPath("commandAccess.json"), commandName, member.ID)
		if err != nil {
			log.Printf("failed to remove /%s access from member %s: %v", commandName, member.ID, err)
			respondGrantAccess(s, i, "❌ Could not save command access; please try again later.")
			return
		}
		if !removed {
			respondGrantAccess(s, i, fmt.Sprintf("ℹ️ <@%s> did not have access to `/%s`.", member.ID, commandName))
			return
		}
		respondGrantAccess(s, i, fmt.Sprintf("✅ Removed <@%s>'s access to `/%s`.", member.ID, commandName))
	default:
		respondGrantAccess(s, i, "❌ Choose `grant` or `remove`.")
	}
}

func respondGrantAccess(s *discordgo.Session, i *discordgo.InteractionCreate, content string) {
	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: content,
			Flags:   discordgo.MessageFlagsEphemeral,
		},
	})
}

```

## `./message_logger.go`

```go
// message_logger.go — mirrors all server messages to per-user JSON files and tracks edits and deletions.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
)

var (
	messageMu          sync.Mutex
	messageLogsBaseDir string // for testing overrides
)

// EditRecord captures a single edit event for a message.
type EditRecord struct {
	Timestamp  string `json:"timestamp"`
	OldContent string `json:"old_content"`
	NewContent string `json:"new_content"`
}

// MessageRecord is the JSON shape saved per message.
type MessageRecord struct {
	Timestamp        string       `json:"timestamp"`
	MessageID        string       `json:"message_id"`
	ChannelID        string       `json:"channel_id"`
	AuthorID         string       `json:"author_id"`
	AuthorUsername   string       `json:"author_username"`
	Content          string       `json:"content"`
	Bot              bool         `json:"bot"`
	Deleted          bool         `json:"deleted,omitempty"`
	DeletedTimestamp string       `json:"deleted_timestamp,omitempty"`
	EditedTimestamp  string       `json:"edited_timestamp,omitempty"`
	EditHistory      []EditRecord `json:"edit_history,omitempty"`
}

func messageLogsRoot() string {
	if messageLogsBaseDir != "" {
		return messageLogsBaseDir
	}
	return dataPath("MessageLogs")
}

func messageLogsDir(guildID string) string {
	return filepath.Join(messageLogsRoot(), guildID)
}

func secureExistingMessageLogs() error {
	root := messageLogsRoot()
	guildDirs, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("reading message log directory: %w", err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		return fmt.Errorf("protecting message log directory: %w", err)
	}

	for _, guildDir := range guildDirs {
		if !guildDir.IsDir() {
			continue
		}
		guildPath := filepath.Join(root, guildDir.Name())
		if err := os.Chmod(guildPath, 0o700); err != nil {
			return fmt.Errorf("protecting message log directory %s: %w", guildPath, err)
		}
		files, err := os.ReadDir(guildPath)
		if err != nil {
			return fmt.Errorf("reading message log directory %s: %w", guildPath, err)
		}
		for _, file := range files {
			if file.IsDir() || filepath.Ext(file.Name()) != ".json" {
				continue
			}
			filePath := filepath.Join(guildPath, file.Name())
			if err := os.Chmod(filePath, 0o600); err != nil {
				return fmt.Errorf("protecting message log file %s: %w", filePath, err)
			}
		}
	}
	return nil
}

func writePrivateMessageLog(filePath string, data []byte) error {
	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating message log directory: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("protecting message log directory: %w", err)
	}
	if err := os.WriteFile(filePath, data, 0o600); err != nil {
		return err
	}
	if err := os.Chmod(filePath, 0o600); err != nil {
		return fmt.Errorf("protecting message log file: %w", err)
	}
	return nil
}

// logMessageToFile writes a message record to Data/MessageLogs/<GuildID>/<AuthorID>.json
func logMessageToFile(msg *discordgo.MessageCreate) error {
	if msg == nil {
		return nil
	}
	guildID := msg.GuildID
	if guildID == "" || msg.Author == nil {
		return nil // no server context
	}
	authorID := msg.Author.ID
	dir := messageLogsDir(guildID)
	filePath := filepath.Join(dir, fmt.Sprintf("%s.json", authorID))

	messageMu.Lock()
	defer messageMu.Unlock()

	record := MessageRecord{
		Timestamp:      time.Now().Format(time.RFC3339),
		MessageID:      msg.ID,
		ChannelID:      msg.ChannelID,
		AuthorID:       msg.Author.ID,
		AuthorUsername: msg.Author.Username,
		Content:        msg.Content,
		Bot:            msg.Author.Bot,
	}

	var records []MessageRecord
	if data, err := os.ReadFile(filePath); err == nil {
		if len(data) > 0 {
			if err := json.Unmarshal(data, &records); err != nil {
				return fmt.Errorf("parsing existing message log %s: %w", filePath, err)
			}
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("reading existing message log %s: %w", filePath, err)
	}
	records = append(records, record)

	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return err
	}
	return writePrivateMessageLog(filePath, data)
}

// logMessageUpdateToFile records an edit to the message in Data/MessageLogs.
// It returns the updated record, the previous content, and any error.
func logMessageUpdateToFile(guildID, messageID, newContent string) (*MessageRecord, string, error) {
	if guildID == "" || messageID == "" {
		return nil, "", nil
	}

	messageMu.Lock()
	defer messageMu.Unlock()

	dir := messageLogsDir(guildID)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, "", err
	}

	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		filePath := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(filePath)
		if err != nil {
			continue
		}
		var records []MessageRecord
		if err := json.Unmarshal(data, &records); err != nil {
			continue
		}

		modified := false
		var oldContent string
		var updatedRec MessageRecord

		for i := len(records) - 1; i >= 0; i-- {
			if records[i].MessageID == messageID {
				oldContent = records[i].Content
				if oldContent == newContent {
					recCopy := records[i]
					return &recCopy, oldContent, nil
				}
				now := time.Now().Format(time.RFC3339)
				records[i].EditHistory = append(records[i].EditHistory, EditRecord{
					Timestamp:  now,
					OldContent: oldContent,
					NewContent: newContent,
				})
				records[i].Content = newContent
				records[i].EditedTimestamp = now
				updatedRec = records[i]
				modified = true
				break
			}
		}

		if modified {
			data, err := json.MarshalIndent(records, "", "  ")
			if err != nil {
				return nil, oldContent, err
			}
			return &updatedRec, oldContent, writePrivateMessageLog(filePath, data)
		}
	}

	return nil, "", nil
}

// logMessageDeleteToFile marks a message as deleted in Data/MessageLogs and returns the record.
func logMessageDeleteToFile(guildID, messageID string) (*MessageRecord, error) {
	if guildID == "" || messageID == "" {
		return nil, nil
	}

	messageMu.Lock()
	defer messageMu.Unlock()

	dir := messageLogsDir(guildID)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		filePath := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(filePath)
		if err != nil {
			continue
		}
		var records []MessageRecord
		if err := json.Unmarshal(data, &records); err != nil {
			continue
		}

		modified := false
		var deletedRec MessageRecord

		for i := len(records) - 1; i >= 0; i-- {
			if records[i].MessageID == messageID {
				records[i].Deleted = true
				records[i].DeletedTimestamp = time.Now().Format(time.RFC3339)
				deletedRec = records[i]
				modified = true
				break
			}
		}

		if modified {
			data, err := json.MarshalIndent(records, "", "  ")
			if err != nil {
				return nil, err
			}
			return &deletedRec, writePrivateMessageLog(filePath, data)
		}
	}

	return nil, nil
}

// logMessageBulkDeleteToFile marks multiple messages as deleted in Data/MessageLogs.
func logMessageBulkDeleteToFile(guildID string, messageIDs []string) error {
	if guildID == "" || len(messageIDs) == 0 {
		return nil
	}
	idMap := make(map[string]bool, len(messageIDs))
	for _, id := range messageIDs {
		idMap[id] = true
	}

	messageMu.Lock()
	defer messageMu.Unlock()

	dir := messageLogsDir(guildID)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}

	now := time.Now().Format(time.RFC3339)
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		filePath := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(filePath)
		if err != nil {
			continue
		}
		var records []MessageRecord
		if err := json.Unmarshal(data, &records); err != nil {
			continue
		}

		modified := false
		for i := range records {
			if idMap[records[i].MessageID] {
				records[i].Deleted = true
				records[i].DeletedTimestamp = now
				modified = true
			}
		}

		if modified {
			data, err := json.MarshalIndent(records, "", "  ")
			if err != nil {
				return err
			}
			if err := writePrivateMessageLog(filePath, data); err != nil {
				return err
			}
		}
	}

	return nil
}

```

## `./commands_test.go`

```go
package main

import (
	"testing"

	"github.com/bwmarrin/discordgo"
)

func TestCommandConsolidations(t *testing.T) {
	buildCommandSpecs()

	commands := make(map[string]*discordgo.ApplicationCommand, len(commandSpecs))
	for _, spec := range commandSpecs {
		commands[spec.Command.Name] = spec.Command
	}
	for _, removed := range []string{"any", "keno-best", "keno", "lock", "unlock"} {
		if _, exists := commands[removed]; exists {
			t.Errorf("command /%s should not be registered", removed)
		}
	}

	for commandName, wantSubcommands := range map[string][]string{
		"ban":          {"add", "remove", "view"},
		"channel":      {"lock", "unlock"},
		"grant-access": {"grant", "remove"},
		"remind":       {"add", "list", "delete"},
		"emojigg":      {"search", "list", "random", "preview", "add"},
	} {
		command, exists := commands[commandName]
		if !exists {
			t.Errorf("command /%s is not registered", commandName)
			continue
		}
		got := make(map[string]bool, len(command.Options))
		for _, option := range command.Options {
			if option.Type == discordgo.ApplicationCommandOptionSubCommand {
				got[option.Name] = true
			}
		}
		for _, subcommand := range wantSubcommands {
			if !got[subcommand] {
				t.Errorf("/%s is missing subcommand %q", commandName, subcommand)
			}
		}
	}

	if commands["emojigg"] == nil {
		t.Fatal("/emojigg is not registered")
	}
	var emojiGGAdd *discordgo.ApplicationCommandOption
	for _, option := range commands["emojigg"].Options {
		if option.Name == "add" {
			emojiGGAdd = option
			break
		}
	}
	if emojiGGAdd == nil {
		t.Fatal("/emojigg is missing the add subcommand")
	}
	emojiGGAddOptions := make(map[string]*discordgo.ApplicationCommandOption, len(emojiGGAdd.Options))
	for _, option := range emojiGGAdd.Options {
		emojiGGAddOptions[option.Name] = option
	}
	for _, optionName := range []string{"asset", "name"} {
		if emojiGGAddOptions[optionName] == nil {
			t.Errorf("/emojigg add is missing option %q", optionName)
		}
	}
	if emojiGGAddOptions["asset"] != nil && !emojiGGAddOptions["asset"].Required {
		t.Error("/emojigg add asset option must be required")
	}
	if emojiGGAddOptions["name"] != nil && emojiGGAddOptions["name"].Required {
		t.Error("/emojigg add name option must remain optional")
	}
	var emojiGGSearch *discordgo.ApplicationCommandOption
	for _, option := range commands["emojigg"].Options {
		if option.Name == "search" {
			emojiGGSearch = option
			break
		}
	}
	if emojiGGSearch == nil {
		t.Fatal("/emojigg is missing the search subcommand")
	}
	for _, option := range emojiGGSearch.Options {
		if option.Name == "page" && option.Required {
			t.Error("/emojigg search page option must remain optional")
		}
	}
	var emojiGGPreview *discordgo.ApplicationCommandOption
	for _, option := range commands["emojigg"].Options {
		if option.Name == "preview" {
			emojiGGPreview = option
			break
		}
	}
	if emojiGGPreview == nil {
		t.Fatal("/emojigg is missing the preview subcommand")
	}
	if len(emojiGGPreview.Options) != 1 || emojiGGPreview.Options[0].Name != "asset" || !emojiGGPreview.Options[0].Required {
		t.Error("/emojigg preview must require one asset option")
	}

	var remindAdd *discordgo.ApplicationCommandOption
	for _, option := range commands["remind"].Options {
		if option.Name == "add" {
			remindAdd = option
			break
		}
	}
	if remindAdd == nil {
		t.Fatal("/remind is missing the add subcommand")
	}
	addOptions := make(map[string]*discordgo.ApplicationCommandOption, len(remindAdd.Options))
	for _, option := range remindAdd.Options {
		addOptions[option.Name] = option
	}
	for _, optionName := range []string{"when", "message", "repeat", "timezone"} {
		if addOptions[optionName] == nil {
			t.Errorf("/remind add is missing option %q", optionName)
		}
	}
	for _, optionName := range []string{"when", "message"} {
		if addOptions[optionName] != nil && !addOptions[optionName].Required {
			t.Errorf("/remind add %s option must remain required", optionName)
		}
	}
	if addOptions["repeat"] != nil && addOptions["repeat"].Required {
		t.Error("/remind add repeat option must remain optional for backward compatibility")
	}
	if addOptions["timezone"] != nil && addOptions["timezone"].Required {
		t.Error("/remind add timezone option must remain optional for backward compatibility")
	}
}

```

## `./notes.go`

```go
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

```

## `./bot.go`

```go
// Package main — bot.go holds the Bot type: session lifecycle, command
// registration, and event-handler wiring.
package main

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
)

// SunfireServerID is the primary server Sunfire runs in.
const SunfireServerID = "1557930395868209162"
const SecondaryServerID = "1557930395868209162"
const SunfireAppID = "1553668018641436702"

var allowedGuildIDs = []string{
	SunfireServerID,
	SecondaryServerID,
}

// isAllowedGuild returns true if the guild ID is one the bot operates in.
func isAllowedGuild(guildID string) bool {
	for _, id := range allowedGuildIDs {
		if id == guildID {
			return true
		}
	}
	return false
}

// Bot wraps a discordgo.Session with the commands/handlers it serves.
type Bot struct {
	Session     *discordgo.Session
	logger      *logger
	registered  bool
	workers     workerLifecycle
	closeOnce   sync.Once
	lifecycleMu sync.Mutex
	closed      bool
}

// NewBot creates a Bot from a token. It wires up the event handlers
// and registers the slash commands.
func NewBot(token string) (*Bot, error) {
	if err := secureExistingMessageLogs(); err != nil {
		return nil, fmt.Errorf("securing existing message logs: %w", err)
	}

	session, err := discordgo.New("Bot " + token)
	if err != nil {
		return nil, err
	}

	// Cache up to 1000 messages per channel so BeforeDelete and BeforeUpdate are populated.
	session.State.MaxMessageCount = 1000

	bot := &Bot{Session: session, logger: newLogger(session)}
	session.AddHandler(bot.onReady)
	session.AddHandler(bot.onInteractionCreate)
	session.AddHandler(bot.onGuildMemberAdd)
	session.AddHandler(bot.onGuildMemberRemove)
	session.AddHandler(bot.onGuildMemberUpdate)
	session.AddHandler(bot.onGuildBanAdd)
	session.AddHandler(bot.onGuildBanRemove)
	session.AddHandler(bot.onGuildChannelCreate)
	session.AddHandler(bot.onGuildChannelUpdate)
	session.AddHandler(bot.onGuildChannelDelete)
	session.AddHandler(bot.onUserUpdate)
	session.AddHandler(bot.onMessageCreate)
	session.AddHandler(bot.onMessageUpdate)
	session.AddHandler(bot.onMessageDelete)
	session.AddHandler(bot.onMessageDeleteBulk)

	return bot, nil
}

// Open establishes the Discord websocket connection.
func (b *Bot) Open() error {
	if err := b.Session.Open(); err != nil {
		return err
	}
	log.Println("Bot is now running. Press Ctrl-C to exit.")
	return nil
}

// Close gracefully closes the session.
func (b *Bot) Close() {
	b.closeOnce.Do(func() {
		b.lifecycleMu.Lock()
		b.closed = true
		b.lifecycleMu.Unlock()
		b.workers.cancel()
		if b.Session != nil {
			if err := b.Session.Close(); err != nil {
				log.Printf("closing Discord session: %v", err)
			}
		}
		b.workers.wait(10 * time.Second)
	})
}

// onReady runs when the bot first connects: registers slash commands once,
// logs startup info to the logging channel, and posts server list.
func (b *Bot) onReady(s *discordgo.Session, r *discordgo.Ready) {
	log.Printf("Logged in as %v (ID %v)", s.State.User.Username, s.State.User.ID)

	// Register slash commands once.
	if !b.registered {
		if err := registerCommands(s); err != nil {
			log.Printf("warning: failed to register slash commands: %v", err)
		}
		b.registered = true
	}

	// Startup log to Discord logging channel.
	if b.logger != nil && channelID() != "" {
		guilds := s.State.Guilds
		var guildInfo string
		for _, g := range guilds {
			guildInfo += fmt.Sprintf("%s (%s) ", g.Name, g.ID)
		}
		b.logger.log("INFO", fmt.Sprintf("Sunfire online — %s | Servers (%d): %s", time.Now().Format("2006-01-02 15:04:05"), len(guilds), guildInfo))
	}

	log.Println("Sunfire online — logged in as " + s.State.User.Username)

	b.startWorkers(s)
}

func (b *Bot) startWorkers(s *discordgo.Session) {
	b.lifecycleMu.Lock()
	defer b.lifecycleMu.Unlock()
	if b.closed {
		return
	}
	b.workers.start(context.Background(),
		func(ctx context.Context) { startPeriodicMediaDeduplication(ctx, time.Hour, b) },
		func(ctx context.Context) { startReminderDaemon(ctx, s) },
		func(ctx context.Context) { startHealthChecker(ctx, s) },
		func(ctx context.Context) { startYouTubeNotifierDaemon(ctx, s) },
		func(ctx context.Context) { startKickNotifierDaemon(ctx, s) },
		func(ctx context.Context) { startSpotifyNotifierDaemon(ctx, s) },
		func(ctx context.Context) { startMoviesNotifierDaemon(ctx, s) },
		func(ctx context.Context) { startFreeStuffNotifierDaemon(ctx, s) },
		func(ctx context.Context) { startScrapeImagesDaemon(ctx, s) },
		func(ctx context.Context) { startBirthdayNotifierDaemon(ctx, s) },
		func(ctx context.Context) { startHermesTipsDaemon(ctx, s) },
	)
}

// isPublicCommand returns true if any server member can use the command.
func isPublicCommand(name string) bool {
	switch name {
	case "analytics", "automod", "battle", "birthday", "close-ticket", "cloudflare", "cohere", "dice", "emojigg", "fallen-star", "groq", "help", "ia", "lyrics", "media", "mistral", "nvidia", "openrouter", "pick", "remind", "riddle", "serverstats", "sysinfo", "transcript", "translate", "userinfo", "video":
		return true
	default:
		return false
	}
}

// onInteractionCreate dispatches a slash-command interaction to its handler.
func (b *Bot) onInteractionCreate(s *discordgo.Session, i *discordgo.InteractionCreate) {
	// Restrict bot strictly to designated servers.
	if !isAllowedGuild(i.GuildID) {
		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "❌ Sunfire only operates in its designated server.",
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
		return
	}

	// Handle Modal Submissions
	if i.Type == discordgo.InteractionModalSubmit {
		data := i.ModalSubmitData()
		if strings.HasPrefix(data.CustomID, "media:resize:") {
			handleMediaResizeModal(s, i)
			return
		}
		return
	}

	// Handle Component Interactions (Buttons, Select Menus)
	if i.Type == discordgo.InteractionMessageComponent {
		data := i.MessageComponentData()
		if strings.HasPrefix(data.CustomID, "help:") {
			handleHelpComponent(s, i)
			return
		}
		if strings.HasPrefix(data.CustomID, "or:") {
			orHandleComponent(s, i)
			return
		}
		if strings.HasPrefix(data.CustomID, "ban:view:") {
			handleBanPageComponent(s, i)
			return
		}
		if strings.HasPrefix(data.CustomID, "emojigg:page:") {
			handleEmojiGGPageComponent(s, i)
			return
		}

		if data.CustomID == "ticket:create" {
			// Create a private support thread for the user
			title := fmt.Sprintf("ticket-%s", i.Member.User.Username)
			thread, err := s.ThreadStart(i.ChannelID, title, discordgo.ChannelTypeGuildPrivateThread, 1440)
			if err != nil {
				_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionResponseData{
						Content: "❌ Failed to create ticket thread: " + err.Error(),
						Flags:   discordgo.MessageFlagsEphemeral,
					},
				})
				return
			}

			// Add member to thread
			_ = s.ThreadMemberAdd(thread.ID, i.Member.User.ID)
			cfg := getGuildSettings(i.GuildID)
			staffPing := ""
			if cfg.TicketStaffRoleID != "" {
				staffPing = fmt.Sprintf("<@&%s> ", cfg.TicketStaffRoleID)
			}

			_, _ = s.ChannelMessageSend(thread.ID, fmt.Sprintf("%s👋 Welcome <@%s>! Staff will assist you shortly. Use `/close-ticket` when resolved.", staffPing, i.Member.User.ID))

			_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content: fmt.Sprintf("✅ Your ticket has been opened: <#%s>", thread.ID),
					Flags:   discordgo.MessageFlagsEphemeral,
				},
			})
			return
		}

		if data.CustomID == "rolemenu:select" {
			// Toggle selected roles for user
			selectedRoles := data.Values
			var added, removed []string

			member, err := s.GuildMember(i.GuildID, i.Member.User.ID)
			if err == nil {
				memberRoleSet := make(map[string]bool)
				for _, rID := range member.Roles {
					memberRoleSet[rID] = true
				}

				for _, rID := range selectedRoles {
					if memberRoleSet[rID] {
						_ = s.GuildMemberRoleRemove(i.GuildID, i.Member.User.ID, rID)
						removed = append(removed, fmt.Sprintf("<@&%s>", rID))
					} else {
						_ = s.GuildMemberRoleAdd(i.GuildID, i.Member.User.ID, rID)
						added = append(added, fmt.Sprintf("<@&%s>", rID))
					}
				}
			}

			respText := "Roles updated!"
			if len(added) > 0 {
				respText += fmt.Sprintf("\n• Added: %s", strings.Join(added, ", "))
			}
			if len(removed) > 0 {
				respText += fmt.Sprintf("\n• Removed: %s", strings.Join(removed, ", "))
			}

			_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content: respText,
					Flags:   discordgo.MessageFlagsEphemeral,
				},
			})
			return
		}

		if data.CustomID == "battle:join" {
			handleJoinBattleComponent(s, i)
			return
		}

		if strings.HasPrefix(data.CustomID, "fs:") {
			handleFallenStarComponent(s, i)
			return
		}
		if strings.HasPrefix(data.CustomID, "media:") {
			handleMediaComponent(s, i)
			return
		}

		return
	}

	if i.Type != discordgo.InteractionApplicationCommand {
		return
	}

	// Owner-only gate: public commands can be used by anyone; others require bot owner.
	cmdName := i.ApplicationCommandData().Name
	if !isPublicCommand(cmdName) {
		cfg, err := loadConfig()
		if err != nil || cfg == nil {
			log.Printf("cannot authorize command %q: failed to load config: %v", cmdName, err)
			_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content: "❌ Bot configuration is unavailable; please try again later.",
					Flags:   discordgo.MessageFlagsEphemeral,
				},
			})
			return
		}
		invokerID := ""
		if i.Member != nil && i.Member.User != nil {
			invokerID = i.Member.User.ID
		} else if i.User != nil {
			invokerID = i.User.ID
		}
		if !isBotAdmin(cfg, invokerID) {
			if cmdName == "grant-access" {
				respondOwnerOnly(s, i)
				return
			}

			ownerConfigured := cfg.OwnerID != "" && cfg.OwnerID != "PASTE_YOUR_DISCORD_USER_ID_HERE"
			if !ownerConfigured && len(cfg.AdminIDs) == 0 {
				respondOwnerOnly(s, i)
				return
			}

			granted, grantErr := hasCommandAccess(dataPath("commandAccess.json"), cmdName, invokerID)
			if grantErr != nil {
				log.Printf("cannot authorize command %q: failed to load command grants: %v", cmdName, grantErr)
				_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionResponseData{
						Content: "❌ Command access data is unavailable; please try again later.",
						Flags:   discordgo.MessageFlagsEphemeral,
					},
				})
				return
			}
			if !granted {
				respondOwnerOnly(s, i)
				return
			}
		}
	}

	// Look up the command by name and dispatch to its handler.
	for _, spec := range commandSpecs {
		if spec.Command.Name == i.ApplicationCommandData().Name {
			spec.Handler(s, i)
			return
		}
	}
}

func respondOwnerOnly(s *discordgo.Session, i *discordgo.InteractionCreate) {
	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: "❌ Bot owner only.",
			Flags:   discordgo.MessageFlagsEphemeral,
		},
	})
}

// onGuildMemberAdd logs when a member joins a guild and assigns autorole if enabled.
func (b *Bot) onGuildMemberAdd(s *discordgo.Session, m *discordgo.GuildMemberAdd) {
	if m.GuildID != SunfireServerID {
		return
	}
	if b.logger != nil && channelID() != "" {
		b.logger.log("MEMBER JOIN", fmt.Sprintf("%s joined %s (%s)", m.User.Username, m.GuildID, m.User.ID))
	}

	// AutoRole assignment
	cfg := getGuildSettings(m.GuildID)
	if cfg.AutoRoleID != nil && *cfg.AutoRoleID != "" {
		_ = s.GuildMemberRoleAdd(m.GuildID, m.User.ID, *cfg.AutoRoleID)
	}
}

// onGuildMemberRemove logs when a member leaves a guild.
func (b *Bot) onGuildMemberRemove(s *discordgo.Session, m *discordgo.GuildMemberRemove) {
	if m.GuildID != SunfireServerID {
		return
	}
	if b.logger != nil && channelID() != "" {
		b.logger.log("MEMBER LEAVE", fmt.Sprintf("%s left %s (%s)", m.User.Username, m.GuildID, m.User.ID))
	}
}

// onGuildMemberUpdate logs mute/deaf/nick changes without spamming unchanged state updates.
func (b *Bot) onGuildMemberUpdate(s *discordgo.Session, m *discordgo.GuildMemberUpdate) {
	if m.GuildID != SunfireServerID {
		return
	}
	if b.logger == nil || channelID() == "" {
		return
	}

	// Only log changes if we have the previous state to compare against
	if m.BeforeUpdate != nil {
		if m.Mute != m.BeforeUpdate.Mute {
			state := "unmuted"
			if m.Mute {
				state = "muted"
			}
			b.logger.log("MEMBER MUTE", fmt.Sprintf("%s %s in %s (%s)", m.User.Username, state, m.GuildID, m.User.ID))
		}

		if m.Deaf != m.BeforeUpdate.Deaf {
			state := "undeafened"
			if m.Deaf {
				state = "deafened"
			}
			b.logger.log("MEMBER DEAF", fmt.Sprintf("%s %s in %s (%s)", m.User.Username, state, m.GuildID, m.User.ID))
		}

		if m.Nick != m.BeforeUpdate.Nick {
			if m.Nick == "" {
				b.logger.log("NICK CHANGE", fmt.Sprintf("%s cleared their nickname in %s (%s)", m.User.Username, m.GuildID, m.User.ID))
			} else {
				b.logger.log("NICK CHANGE", fmt.Sprintf("%s nickname -> %q in %s (%s)", m.User.Username, m.Nick, m.GuildID, m.User.ID))
			}
		}
	}
}

// onGuildBanAdd logs bans.
func (b *Bot) onGuildBanAdd(s *discordgo.Session, u *discordgo.GuildBanAdd) {
	if u.GuildID != SunfireServerID {
		return
	}
	if b.logger != nil && channelID() != "" {
		b.logger.log("BAN", fmt.Sprintf("%s banned in %s (%s)", u.User.Username, u.GuildID, u.User.ID))
	}
}

// onGuildBanRemove logs unbans.
func (b *Bot) onGuildBanRemove(s *discordgo.Session, u *discordgo.GuildBanRemove) {
	if u.GuildID != SunfireServerID {
		return
	}
	if b.logger != nil && channelID() != "" {
		b.logger.log("UNBAN", fmt.Sprintf("%s unbanned in %s (%s)", u.User.Username, u.GuildID, u.User.ID))
	}
}

// callHermes invokes the Hermes CLI directly. It attempts to continue the latest conversational memory thread.
// If no previous session exists, it auto-heals and starts a fresh one to establish the baseline.
func callHermes(ctx context.Context, content string) (string, error) {
	return runHermes(ctx, content, false)
}

func callHermesNewSession(ctx context.Context, content string) (string, error) {
	return runHermes(ctx, content, true)
}

func runHermes(ctx context.Context, content string, newSession bool) (string, error) {
	hermesBinary, err := resolveHermesBinary()
	if err != nil {
		return "", err
	}
	args := hermesChatArgs(content, newSession)
	cmd := exec.CommandContext(ctx, hermesBinary, args...)
	cmd.Dir = applicationDir()

	var outBuf bytes.Buffer
	var errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	err = cmd.Run()
	errStr := strings.TrimSpace(errBuf.String())

	// Attempt 2: If Hermes fails because there is no previous session to continue,
	// run it normally to establish the very first session.
	if !newSession && err != nil && (strings.Contains(errStr, "not found") || strings.Contains(errStr, "No session") || strings.Contains(errStr, "unrecognized")) {
		cmdFallback := exec.CommandContext(ctx, hermesBinary, hermesChatArgs(content, true)...)
		cmdFallback.Dir = applicationDir()

		outBuf.Reset()
		errBuf.Reset()
		cmdFallback.Stdout = &outBuf
		cmdFallback.Stderr = &errBuf

		err = cmdFallback.Run()
		errStr = strings.TrimSpace(errBuf.String())
	}

	if ctx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("Hermes took too long and was terminated")
	}
	if err != nil {
		if errStr == "" {
			errStr = err.Error()
		}
		return "", fmt.Errorf("Hermes execution failed: %s", errStr)
	}

	response := strings.TrimSpace(outBuf.String())
	if response == "" {
		return "", fmt.Errorf("Hermes returned an empty response")
	}

	return response, nil
}

func hermesChatArgs(content string, newSession bool) []string {
	args := []string{"--yolo", "chat", "-q", content, "-Q", "--max-turns", "20"}
	if !newSession {
		args = append([]string{"--continue"}, args...)
	}
	return args
}

func hermesFailureMessage(err error) string {
	message := "Failed to contact Hermes: " + err.Error()
	errText := err.Error()
	if strings.Contains(errText, "Resume retains gateway session policy") ||
		strings.Contains(errText, "--max-turns differs from this session's launch") {
		message += "\n- To start a new session enter exactly: \"start-new-session\""
	}
	return message
}

func resolveHermesBinary() (string, error) {
	if hermesBinary := os.Getenv("SUNFIRE_HERMES_BIN"); hermesBinary != "" {
		return hermesBinary, nil
	}
	if hermesBinary, err := exec.LookPath("hermes"); err == nil {
		return hermesBinary, nil
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("Hermes executable not found on PATH, and could not determine service user's home directory: %w", err)
	}
	hermesBinary := filepath.Join(homeDir, ".local", "bin", "hermes")
	if path, err := exec.LookPath(hermesBinary); err == nil {
		return path, nil
	}

	return "", fmt.Errorf("Hermes executable not found on PATH or at %s; set SUNFIRE_HERMES_BIN to its absolute path", hermesBinary)
}

// sendSmartChunks splits a long response cleanly at newlines and preserves open Markdown code blocks.
func sendSmartChunks(s *discordgo.Session, channelID, text string) {
	const maxLen = 1950
	inCodeBlock := false

	lines := strings.Split(text, "\n")
	var currentChunk strings.Builder

	for _, line := range lines {
		// Toggle code block state if we see ```
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inCodeBlock = !inCodeBlock
		}

		// If adding this line exceeds the chunk limit, send the chunk and start a new one
		if currentChunk.Len()+len(line)+1 > maxLen {
			chunkStr := currentChunk.String()

			// Close the block temporarily if we are inside one so Discord formats it right
			if inCodeBlock {
				chunkStr += "\n```"
			}

			_, _ = s.ChannelMessageSend(channelID, chunkStr)
			currentChunk.Reset()

			// Re-open the block for the new chunk
			if inCodeBlock {
				currentChunk.WriteString("```\n")
			}
		}

		currentChunk.WriteString(line + "\n")
	}

	if currentChunk.Len() > 0 {
		_, _ = s.ChannelMessageSend(channelID, currentChunk.String())
	}
}

// onMessageCreate mirrors every server message to per-user JSON files
// and processes any media attachments into Data/MediaLogs/<GuildID>/.
func (b *Bot) onMessageCreate(s *discordgo.Session, m *discordgo.MessageCreate) {
	if m.GuildID != "" && m.GuildID != SunfireServerID {
		return
	}
	processMessageLogging(m)

	// Skip bot messages for games and automod
	if m.Author == nil || m.Author.Bot {
		_ = logMessageToFile(m)
		processMediaAttachments(m)
		return
	}

	// Check if this message solves an active riddle
	if checkRiddleAnswer(s, m) {
		_ = logMessageToFile(m)
		return
	}

	// AutoMod Check (deletes message and warns if blocked word is found)
	checkAutoMod(s, m)
	_ = logMessageToFile(m)
	processMediaAttachments(m)
}

// onMessageDelete logs when a message is deleted.
func (b *Bot) onMessageDelete(s *discordgo.Session, m *discordgo.MessageDelete) {
	if m == nil {
		return
	}
	guildID := m.GuildID
	if guildID == "" {
		if ch, err := s.State.Channel(m.ChannelID); err == nil && ch != nil {
			guildID = ch.GuildID
		}
	}
	if guildID != SunfireServerID || m.ChannelID == channelID() {
		return
	}

	// Update local JSON log and check for stored message details
	fileRec, _ := logMessageDeleteToFile(guildID, m.ID)

	var authorStr string
	var content string

	if m.BeforeDelete != nil {
		if m.BeforeDelete.Author != nil {
			authorStr = fmt.Sprintf("%s (%s)", m.BeforeDelete.Author.Username, m.BeforeDelete.Author.ID)
		}
		content = m.BeforeDelete.Content
	}

	if authorStr == "" && fileRec != nil {
		if fileRec.AuthorUsername != "" {
			authorStr = fmt.Sprintf("%s (%s)", fileRec.AuthorUsername, fileRec.AuthorID)
		}
		if content == "" {
			content = fileRec.Content
		}
	}

	if b.logger != nil && channelID() != "" {
		if authorStr != "" {
			if content != "" {
				b.logger.log("MESSAGE DELETE", fmt.Sprintf("Message by %s in <#%s> deleted:\n%s", authorStr, m.ChannelID, formatBlockQuote(content, 400)))
			} else {
				b.logger.log("MESSAGE DELETE", fmt.Sprintf("Message by %s in <#%s> deleted (ID: %s)", authorStr, m.ChannelID, m.ID))
			}
		} else {
			b.logger.log("MESSAGE DELETE", fmt.Sprintf("Message (%s) in <#%s> deleted", m.ID, m.ChannelID))
		}
	}
}

// onMessageDeleteBulk logs when multiple messages are deleted at once (e.g. purge).
func (b *Bot) onMessageDeleteBulk(s *discordgo.Session, m *discordgo.MessageDeleteBulk) {
	if m == nil {
		return
	}
	guildID := m.GuildID
	if guildID == "" {
		if ch, err := s.State.Channel(m.ChannelID); err == nil && ch != nil {
			guildID = ch.GuildID
		}
	}
	if guildID != SunfireServerID || m.ChannelID == channelID() {
		return
	}

	_ = logMessageBulkDeleteToFile(guildID, m.Messages)

	if b.logger != nil && channelID() != "" {
		b.logger.log("MESSAGE BULK DELETE", fmt.Sprintf("%d messages deleted in <#%s>", len(m.Messages), m.ChannelID))
	}
}

// onMessageUpdate logs when a message is edited.
func (b *Bot) onMessageUpdate(s *discordgo.Session, m *discordgo.MessageUpdate) {
	if m == nil {
		return
	}
	guildID := m.GuildID
	if guildID == "" {
		if ch, err := s.State.Channel(m.ChannelID); err == nil && ch != nil {
			guildID = ch.GuildID
		}
	}
	if guildID != SunfireServerID || m.ChannelID == channelID() {
		return
	}

	// Discord fires MessageUpdate when link unfurls/embeds load or pins change.
	// If m.Content is empty, it's not a text edit event.
	if m.Content == "" {
		return
	}

	var oldContent string
	var authorStr string

	if m.BeforeUpdate != nil {
		oldContent = m.BeforeUpdate.Content
		if m.BeforeUpdate.Author != nil {
			authorStr = fmt.Sprintf("%s (%s)", m.BeforeUpdate.Author.Username, m.BeforeUpdate.Author.ID)
		}
	}

	// Skip if before and after content are identical (e.g. embed preview was added)
	if m.BeforeUpdate != nil && oldContent == m.Content {
		return
	}

	fileRec, fileOldContent, _ := logMessageUpdateToFile(guildID, m.ID, m.Content)
	if oldContent == "" {
		oldContent = fileOldContent
	}

	// If oldContent was known and matches m.Content, skip
	if oldContent != "" && oldContent == m.Content {
		return
	}

	if authorStr == "" && m.Author != nil {
		authorStr = fmt.Sprintf("%s (%s)", m.Author.Username, m.Author.ID)
	}
	if authorStr == "" && fileRec != nil && fileRec.AuthorUsername != "" {
		authorStr = fmt.Sprintf("%s (%s)", fileRec.AuthorUsername, fileRec.AuthorID)
	}

	if b.logger != nil && channelID() != "" {
		jumpURL := fmt.Sprintf("https://discord.com/channels/%s/%s/%s", guildID, m.ChannelID, m.ID)
		if authorStr != "" {
			if oldContent != "" {
				b.logger.log("MESSAGE UPDATE", fmt.Sprintf("Message by %s in <#%s> edited ([Jump](<%s>)):\n• **Before:** %s\n• **After:** %s", authorStr, m.ChannelID, jumpURL, formatContentPreview(oldContent, 400), formatContentPreview(m.Content, 400)))
			} else {
				b.logger.log("MESSAGE UPDATE", fmt.Sprintf("Message by %s in <#%s> edited ([Jump](<%s>)):\n• **Now:** %s", authorStr, m.ChannelID, jumpURL, formatContentPreview(m.Content, 500)))
			}
		} else {
			b.logger.log("MESSAGE UPDATE", fmt.Sprintf("Message (%s) in <#%s> edited ([Jump](<%s>)):\n• **Now:** %s", m.ID, m.ChannelID, jumpURL, formatContentPreview(m.Content, 500)))
		}
	}
}

func (b *Bot) onGuildChannelCreate(s *discordgo.Session, ch *discordgo.ChannelCreate) {
	if ch.GuildID != SunfireServerID {
		return
	}
	if b.logger != nil && channelID() != "" {
		b.logger.log("CHAN CREATE", fmt.Sprintf("#%s (%s) created in %s", ch.Channel.Name, ch.Channel.ID, ch.GuildID))
	}
}

func (b *Bot) onGuildChannelUpdate(s *discordgo.Session, ch *discordgo.ChannelUpdate) {
	if ch.GuildID != SunfireServerID {
		return
	}
	if b.logger != nil && channelID() != "" {
		b.logger.log("CHAN UPDATE", fmt.Sprintf("#%s (%s) updated in %s", ch.Channel.Name, ch.Channel.ID, ch.GuildID))
	}
}

func (b *Bot) onGuildChannelDelete(s *discordgo.Session, ch *discordgo.ChannelDelete) {
	if ch.GuildID != SunfireServerID {
		return
	}
	if b.logger != nil && channelID() != "" {
		b.logger.log("CHAN DELETE", fmt.Sprintf("#%s (%s) deleted in %s", ch.Channel.Name, ch.Channel.ID, ch.GuildID))
	}
}

func (b *Bot) onUserUpdate(s *discordgo.Session, u *discordgo.UserUpdate) {
	if b.logger != nil && channelID() != "" {
		b.logger.log("USER UPDATE", fmt.Sprintf("%s (%s) username/avatar updated", u.User.Username, u.User.ID))
	}
}

func truncateString(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "..."
}

func formatBlockQuote(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) > maxLen {
		s = string(runes[:maxLen]) + "..."
	}
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = "> " + l
	}
	return strings.Join(lines, "\n")
}

func formatContentPreview(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) > maxLen {
		s = string(runes[:maxLen]) + "..."
	}
	if strings.Contains(s, "\n") {
		lines := strings.Split(s, "\n")
		for i, l := range lines {
			lines[i] = "> " + l
		}
		return "\n" + strings.Join(lines, "\n")
	}
	return fmt.Sprintf("%q", s)
}

```

## `./service_test.go`

```go
package main

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestRuntimePathsCanBeConfigured(t *testing.T) {
	home := filepath.Join(t.TempDir(), "app")
	data := filepath.Join(t.TempDir(), "state")
	config := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("SUNFIRE_HOME", home)
	t.Setenv("SUNFIRE_DATA_DIR", data)
	t.Setenv("SUNFIRE_CONFIG_FILE", config)

	if got, want := applicationPath("Pictures"), filepath.Join(home, "Pictures"); got != want {
		t.Fatalf("applicationPath = %q; want %q", got, want)
	}
	if got, want := dataPath("Reminders", "reminders.json"), filepath.Join(data, "Reminders", "reminders.json"); got != want {
		t.Fatalf("dataPath = %q; want %q", got, want)
	}
	if got, want := configPath(), config; got != want {
		t.Fatalf("configPath = %q; want %q", got, want)
	}
}

func TestWorkerLifecycleStartsOnceAndStops(t *testing.T) {
	var lifecycle workerLifecycle
	ctx := context.Background()
	var starts atomic.Int32
	stopped := make(chan struct{})
	worker := func(ctx context.Context) {
		starts.Add(1)
		<-ctx.Done()
		close(stopped)
	}

	lifecycle.start(ctx, worker)
	lifecycle.start(ctx, worker)
	lifecycle.stop(time.Second)

	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("worker did not observe cancellation")
	}
	if got := starts.Load(); got != 1 {
		t.Fatalf("worker starts = %d; want exactly one", got)
	}
}

func TestRunPeriodicCanBeCancelledDuringInitialDelay(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var runs atomic.Int32
	done := make(chan struct{})
	go func() {
		defer close(done)
		runPeriodic(ctx, time.Hour, time.Hour, func() { runs.Add(1) })
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("periodic worker did not stop during initial delay")
	}
	if got := runs.Load(); got != 0 {
		t.Fatalf("task runs = %d; want 0 after cancellation", got)
	}
}

```

## `./config.go`

```go
// config.go — loads secrets from the gitignored .private/config.json.
//
// Priority: .private/config.json ("botToken") → BOT_TOKEN env var (fallback).
// The env var remains a fallback so the bot also runs in environments that
// prefer environment-injected secrets (e.g. CI, systemd).
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Config is the shape of .private/config.json.
type Config struct {
	Project             string            `json:"project"`
	Notes               string            `json:"notes"`
	BotToken            string            `json:"botToken"`
	ApplicationID       string            `json:"applicationId"`
	PublicKey           string            `json:"publicKey"`
	BotInviteURL        string            `json:"botInviteUrl"`
	LoggingChannel      string            `json:"loggingChannelId"`
	OwnerID             string            `json:"ownerId"`
	AdminIDs            []string          `json:"adminIds,omitempty"`
	InfoCategoryID      string            `json:"infoCategoryId,omitempty"`
	RulesChannelID      string            `json:"rulesChannelId,omitempty"`
	VoiceChannelID      string            `json:"voiceChannelId,omitempty"`
	HermesTipsChannelID string            `json:"hermesTipsChannelId,omitempty"`
	HealthCheck         HealthCheckConfig `json:"healthCheck"`
	AnyAPI              []AnyAPIProvider  `json:"anyApi"`
	VeniceAPIKey        string            `json:"veniceApiKey"`
	VeniceBaseURL       string            `json:"veniceBaseUrl"`
	DefaultChatModel    string            `json:"defaultChatModel"`
	DefaultImageModel   string            `json:"defaultImageModel"`
	DefaultTTSModel     string            `json:"defaultTtsModel"`
	DefaultTTSVoice     string            `json:"defaultTtsVoice"`
	DefaultMusicModel   string            `json:"defaultMusicModel"`
	A2EAPIKey           string            `json:"A2E_API_KEY"`
	A2EBaseURL          string            `json:"a2eBaseUrl"`
	DefaultVideoModel   string            `json:"defaultVideoModel"`

	Cohere     ProviderConfig `json:"cohere"`
	Cloudflare ProviderConfig `json:"cloudflare"`
	Groq       ProviderConfig `json:"groq"`
	Mistral    ProviderConfig `json:"mistral"`
	Nvidia     ProviderConfig `json:"nvidia"`
	OpenRouter ProviderConfig `json:"openrouter"`

	// ElyAdmin Merged Integrations & Secrets
	YouTubeAPIKey                 string        `json:"YOUTUBE_API_KEY"`
	YouTubePollIntervalMs         int           `json:"YOUTUBE_POLL_INTERVAL"`
	KickClientID                  string        `json:"KICK_CLIENT_ID"`
	KickClientSecret              string        `json:"KICK_CLIENT_SECRET"`
	KickPollIntervalMs            int           `json:"KICK_POLL_INTERVAL"`
	TMDBAPIKey                    string        `json:"TMDB_API_KEY"`
	SpotifyClientID               string        `json:"SPOTIFY_CLIENT_ID"`
	SpotifyClientSecret           string        `json:"SPOTIFY_CLIENT_SECRET"`
	ProductHuntAPIKey             string        `json:"PRODUCTHUNT_API_KEY"`
	ProductHuntAPISecret          string        `json:"PRODUCTHUNT_API_SECRET"`
	GGDealsAPIKey                 string        `json:"GGDEALS_API_KEY"`
	PexelsAPIKey                  string        `json:"PEXELS_API_KEY"`
	GeminiAPIKey                  string        `json:"GEMINI_API_KEY"`
	AudiusAPIKey                  string        `json:"AUDIUS_API_KEY"`
	AudiusAPIBearerToken          string        `json:"AUDIUS_API_BEARER_TOKEN"`
	TarotAPIBaseURL               string        `json:"TAROT_API_BASE_URL"`
	PostgresConnectionString      string        `json:"POSTGRES_CONNECTION_STRING"`
	MessageLoggingEnabled         bool          `json:"MESSAGE_LOGGING_ENABLED"`
	Scrape                        ScrapeConfig  `json:"scrape"`
	Guild                         GuildSettings `json:"guild"`
}

type ScrapeConfig struct {
	URLs       []string `json:"urls"`
	ChannelIDs []string `json:"channelIds"`
}

// UnmarshalJSON allows ChannelIDs to be parsed from either strings or JSON numbers (Discord snowflake IDs).
func (sc *ScrapeConfig) UnmarshalJSON(data []byte) error {
	var raw struct {
		URLs       []string      `json:"urls"`
		ChannelIDs []interface{} `json:"channelIds"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&raw); err != nil {
		return err
	}
	sc.URLs = raw.URLs
	sc.ChannelIDs = make([]string, 0, len(raw.ChannelIDs))
	for _, item := range raw.ChannelIDs {
		switch v := item.(type) {
		case string:
			if s := strings.TrimSpace(v); s != "" {
				sc.ChannelIDs = append(sc.ChannelIDs, s)
			}
		case json.Number:
			sc.ChannelIDs = append(sc.ChannelIDs, v.String())
		case float64:
			sc.ChannelIDs = append(sc.ChannelIDs, fmt.Sprintf("%.0f", v))
		}
	}
	return nil
}

type HealthCheckConfig struct {
	Enabled   bool     `json:"enabled"`
	Interval  int      `json:"intervalMinutes"`
	Targets   []string `json:"targets"`
	ChannelID string   `json:"channelId,omitempty"`
}

// TargetChannelID returns the configured health check channel or falls back to the default LoggingChannel.
func (hc HealthCheckConfig) TargetChannelID() string {
	if hc.ChannelID != "" {
		return hc.ChannelID
	}
	return channelID()
}

// UnmarshalJSON parses HealthCheckConfig and normalizes string or numeric channelId/loggingChannelId.
func (hc *HealthCheckConfig) UnmarshalJSON(data []byte) error {
	type Alias HealthCheckConfig
	var raw struct {
		Alias
		RawChannelID        interface{} `json:"channelId"`
		RawLoggingChannelID interface{} `json:"loggingChannelId"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*hc = HealthCheckConfig(raw.Alias)

	parseID := func(v interface{}) string {
		switch val := v.(type) {
		case string:
			return strings.TrimSpace(val)
		case json.Number:
			return val.String()
		case float64:
			return fmt.Sprintf("%.0f", val)
		default:
			return ""
		}
	}

	if id := parseID(raw.RawChannelID); id != "" {
		hc.ChannelID = id
	} else if id := parseID(raw.RawLoggingChannelID); id != "" {
		hc.ChannelID = id
	}
	return nil
}

type AnyAPIProvider struct {
	Name    string `json:"name"`
	URL     string `json:"url"`
	Auth    string `json:"auth"`
	Timeout int    `json:"timeout"`
}

type ProviderConfig struct {
	APIKey           string `json:"apiKey"`
	APIManagementKey string `json:"apiManagementKey,omitempty"`
	AccountID        string `json:"accountId"`
}

// configPath honors an explicit deployment path, then the application root,
// with a source-tree fallback for local development.
func configPath() string {
	if path := os.Getenv("SUNFIRE_CONFIG_FILE"); path != "" {
		return path
	}
	legacyPath := filepath.Join(applicationDir(), ".private", "config.json")
	if _, err := os.Stat(legacyPath); err == nil {
		return legacyPath
	}
	if _, explicitlyConfigured := os.LookupEnv("SUNFIRE_HOME"); !explicitlyConfigured {
		if _, file, _, ok := runtime.Caller(0); ok {
			return filepath.Join(filepath.Dir(file), ".private", "config.json")
		}
	}
	return filepath.Join(applicationDir(), ".private", "config.json")
}

// loadConfig reads .private/config.json. Returns an empty Config if the file
// is missing (callers then fall back to env).
func loadConfig() (*Config, error) {
	return loadConfigFromPath(configPath())
}

// loadConfigFromPath reads a config file from an explicit path. A missing file
// yields an empty Config (not an error).
func loadConfigFromPath(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Config{}, nil
		}
		return nil, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err == nil {
		if cfg.InfoCategoryID == "" {
			if v, ok := raw["infoCategoryId"].(string); ok && v != "" {
				cfg.InfoCategoryID = v
			} else if v, ok := raw["infoChannelId"].(string); ok && v != "" {
				cfg.InfoCategoryID = v
			}
		}
		if cfg.RulesChannelID == "" {
			if v, ok := raw["rulesChannelId"].(string); ok && v != "" {
				cfg.RulesChannelID = v
			} else if v, ok := raw["rulesChannel"].(string); ok && v != "" {
				cfg.RulesChannelID = v
			}
		}
	}
	return &cfg, nil
}

// saveConfig writes a Config back to path as pretty JSON.
func saveConfig(path string, cfg *Config) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding config: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

// botToken resolves the token: config file first, then BOT_TOKEN env var.
func botToken() (string, error) {
	cfg, err := loadConfig()
	if err != nil {
		return "", err
	}
	return botTokenFromConfig(cfg), nil
}

// botTokenFromConfig is the pure, testable form of botToken precedence:
// config "botToken" wins; otherwise the BOT_TOKEN env var; else an error.
func botTokenFromConfig(cfg *Config) string {
	if cfg.BotToken != "" && cfg.BotToken != "PASTE_YOUR_BOT_TOKEN_HERE" {
		return cfg.BotToken
	}
	return os.Getenv("BOT_TOKEN")
}

// isBotAdmin returns true if the given Discord user ID is the owner or
// listed in the admin allowlist.
func isBotAdmin(cfg *Config, userID string) bool {
	if cfg.OwnerID != "" && cfg.OwnerID != "PASTE_YOUR_DISCORD_USER_ID_HERE" && cfg.OwnerID == userID {
		return true
	}
	for _, aid := range cfg.AdminIDs {
		if aid == userID {
			return true
		}
	}
	return false
}

```

## `./media_logger_test.go`

```go
package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCategorizeMedia(t *testing.T) {
	tests := []struct {
		filename    string
		contentType string
		expected    string
	}{
		{"image.png", "image/png", "images"},
		{"photo.jpg", "", "images"},
		{"clip.mp4", "video/mp4", "videos"},
		{"movie.mkv", "", "videos"},
		{"song.mp3", "audio/mpeg", "audio"},
		{"voice.ogg", "audio/ogg", "audio"},
		{"track.m4a", "", "audio"},
		{"document.pdf", "application/pdf", ""},
		{"script.sh", "text/x-shellscript", ""},
	}

	for _, tt := range tests {
		got := categorizeMedia(tt.filename, tt.contentType)
		if got != tt.expected {
			t.Errorf("categorizeMedia(%q, %q) = %q; want %q", tt.filename, tt.contentType, got, tt.expected)
		}
	}
}

func TestSanitizeFilename(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"normal.png", "normal.png"},
		{"path/to/file.jpg", "file.jpg"},
		{"../../evil.mp4", "evil.mp4"},
		{"my video (1).mp4", "my_video__1_.mp4"},
		{"hello@world!.png", "hello_world_.png"},
	}

	for _, tt := range tests {
		got := sanitizeFilename(tt.input)
		if got != tt.expected {
			t.Errorf("sanitizeFilename(%q) = %q; want %q", tt.input, got, tt.expected)
		}
	}
}

func TestMediaDeduplication(t *testing.T) {
	tmpDir := t.TempDir()
	mediaLogsBaseDir = tmpDir
	defer func() {
		mediaLogsBaseDir = ""
	}()

	serverID := "1557930395868209162"
	serverDir := filepath.Join(tmpDir, serverID)
	imagesDir := filepath.Join(serverDir, "images")
	videosDir := filepath.Join(serverDir, "videos")

	if err := os.MkdirAll(imagesDir, 0755); err != nil {
		t.Fatalf("failed to create images dir: %v", err)
	}
	if err := os.MkdirAll(videosDir, 0755); err != nil {
		t.Fatalf("failed to create videos dir: %v", err)
	}

	// 1. Create duplicate images
	content1 := []byte("identical image content for testing hash deduplication")
	fileA := filepath.Join(imagesDir, "img1.png")
	fileB := filepath.Join(imagesDir, "img2.png")
	fileC := filepath.Join(imagesDir, "img3.png")

	if err := os.WriteFile(fileA, content1, 0644); err != nil {
		t.Fatalf("failed to write fileA: %v", err)
	}
	// Give fileA older timestamp
	oldTime := time.Now().Add(-10 * time.Minute)
	_ = os.Chtimes(fileA, oldTime, oldTime)

	if err := os.WriteFile(fileB, content1, 0644); err != nil {
		t.Fatalf("failed to write fileB: %v", err)
	}
	if err := os.WriteFile(fileC, content1, 0644); err != nil {
		t.Fatalf("failed to write fileC: %v", err)
	}

	// 2. Create unique video
	content2 := []byte("different video content")
	fileD := filepath.Join(videosDir, "vid1.mp4")
	if err := os.WriteFile(fileD, content2, 0644); err != nil {
		t.Fatalf("failed to write fileD: %v", err)
	}

	// Test findFileByHash before deduplication
	hash1, err := computeFileHash(fileA)
	if err != nil {
		t.Fatalf("computeFileHash failed: %v", err)
	}
	foundPath, found := findFileByHash(serverDir, hash1)
	if !found {
		t.Errorf("expected findFileByHash to find file with hash %s", hash1)
	}
	if foundPath == "" {
		t.Errorf("expected non-empty foundPath")
	}

	// 3. Run DeduplicateMediaFolder
	deleted, freed, err := DeduplicateMediaFolder(serverDir)
	if err != nil {
		t.Fatalf("DeduplicateMediaFolder failed: %v", err)
	}

	if deleted != 2 {
		t.Errorf("expected 2 deleted duplicates, got %d", deleted)
	}
	expectedFreed := int64(len(content1) * 2)
	if freed != expectedFreed {
		t.Errorf("expected freed %d bytes, got %d", expectedFreed, freed)
	}

	// Verify fileA (oldest) still exists
	if _, err := os.Stat(fileA); os.IsNotExist(err) {
		t.Errorf("expected original fileA to exist, but was deleted")
	}

	// Verify fileB and fileC were deleted
	if _, err := os.Stat(fileB); !os.IsNotExist(err) {
		t.Errorf("expected duplicate fileB to be deleted")
	}
	if _, err := os.Stat(fileC); !os.IsNotExist(err) {
		t.Errorf("expected duplicate fileC to be deleted")
	}

	// Verify fileD (unique) still exists
	if _, err := os.Stat(fileD); os.IsNotExist(err) {
		t.Errorf("expected unique fileD to exist, but was deleted")
	}

	// 4. Test DeduplicateAllMediaLogs when no duplicates remain
	deleted2, freed2, err := DeduplicateAllMediaLogs()
	if err != nil {
		t.Fatalf("DeduplicateAllMediaLogs failed: %v", err)
	}
	if deleted2 != 0 || freed2 != 0 {
		t.Errorf("expected 0 duplicates on second run, got deleted=%d, freed=%d", deleted2, freed2)
	}
}

```

## `./media.go`

```go
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
)

const (
	maxMediaInputBytes   = 25 << 20
	maxMediaOutputBytes  = 25 << 20
	maxMediaSessionBytes = 100 << 20
	maxMediaSessions     = 20
	mediaSessionTTL      = 15 * time.Minute
)

type mediaKind int

const (
	mediaImage mediaKind = iota
	mediaAudio
	mediaVideo
)

type mediaEffect struct {
	value         string
	label         string
	videoFilter   string
	audioFilter   string
	audioChannels int
	videoSpeed    float64
	audioSpeed    float64
	resizeWidth   int
	resizeHeight  int
	optimize      bool
	reverse       bool
	mute          bool
}

type mediaSession struct {
	ownerID     string
	directory   string
	inputPath   string
	filename    string
	contentType string
	inputSize   int64
	kind        mediaKind
	frameCount  int
	expiresAt   time.Time
	timer       *time.Timer
}

var mediaSessionStore = struct {
	sync.Mutex
	entries map[string]*mediaSession
	bytes   int64
}{entries: make(map[string]*mediaSession)}

var mediaDownloadSemaphore = make(chan struct{}, 3)
var mediaProcessSemaphore = make(chan struct{}, 2)

var mediaTransformHTTPClient = &http.Client{
	Timeout: 60 * time.Second,
	CheckRedirect: func(request *http.Request, via []*http.Request) error {
		if request.URL.Scheme != "https" || request.URL.User != nil || !isDiscordMediaHost(request.URL.Hostname()) {
			return errors.New("attachment redirected to a non-Discord media host")
		}
		return nil
	},
}

var imageMediaEffects = []mediaEffect{
	{value: "grayscale", label: "Grayscale", videoFilter: "hue=s=0"},
	{value: "sepia", label: "Sepia", videoFilter: "colorchannelmixer=.393:.769:.189:0:.349:.686:.168:0:.272:.534:.131"},
	{value: "invert", label: "Invert colors", videoFilter: "negate"},
	{value: "mirror", label: "Mirror horizontally", videoFilter: "hflip"},
	{value: "flip", label: "Flip vertically", videoFilter: "vflip"},
	{value: "rotate-right", label: "Rotate right", videoFilter: "transpose=clock"},
	{value: "rotate-left", label: "Rotate left", videoFilter: "transpose=cclock"},
	{value: "blur", label: "Blur", videoFilter: "boxblur=10:1"},
	{value: "sharpen", label: "Sharpen", videoFilter: "unsharp=5:5:1.5:5:5:0"},
	{value: "pixelate", label: "Pixelate", videoFilter: "scale=iw/12:ih/12:flags=neighbor,scale=iw*12:ih*12:flags=neighbor"},
	{value: "brighten", label: "Brighten", videoFilter: "eq=brightness=0.15"},
	{value: "darken", label: "Darken", videoFilter: "eq=brightness=-0.15"},
	{value: "contrast-up", label: "Increase contrast", videoFilter: "eq=contrast=1.5"},
	{value: "contrast-down", label: "Decrease contrast", videoFilter: "eq=contrast=0.7"},
	{value: "saturate", label: "Boost color", videoFilter: "eq=saturation=1.8"},
	{value: "desaturate", label: "Soften color", videoFilter: "eq=saturation=0.3"},
	{value: "hue-shift", label: "Shift hue", videoFilter: "hue=h=45"},
	{value: "vignette", label: "Vignette", videoFilter: "vignette"},
	{value: "edges", label: "Edge detection", videoFilter: "edgedetect=low=0.1:high=0.4"},
	{value: "film-grain", label: "Film grain", videoFilter: "noise=alls=20:allf=t+u"},
	{value: "warm", label: "Warm colors", videoFilter: "colorbalance=rs=.2:bs=-.2"},
	{value: "cool", label: "Cool colors", videoFilter: "colorbalance=rs=-.2:bs=.2"},
	{value: "gamma-up", label: "Raise gamma", videoFilter: "eq=gamma=1.25"},
	{value: "resize", label: "Resize..."},
	{value: "optimize", label: "Optimize (lossless)", optimize: true},
}

var audioMediaEffects = []mediaEffect{
	{value: "volume-up", label: "Increase volume", audioFilter: "volume=1.5"},
	{value: "volume-down", label: "Lower volume", audioFilter: "volume=0.65"},
	{value: "silence", label: "Silence audio", audioFilter: "volume=0"},
	{value: "mono", label: "Convert to mono", audioChannels: 1},
	{value: "stereo", label: "Convert to stereo", audioChannels: 2},
	{value: "faster", label: "Speed up", audioSpeed: 1.25},
	{value: "slower", label: "Slow down", audioSpeed: 0.8},
	{value: "reverse", label: "Reverse audio", reverse: true},
	{value: "bass-boost", label: "Boost bass", audioFilter: "bass=g=8"},
	{value: "bass-cut", label: "Reduce bass", audioFilter: "bass=g=-8"},
	{value: "treble-boost", label: "Boost treble", audioFilter: "treble=g=6"},
	{value: "treble-cut", label: "Reduce treble", audioFilter: "treble=g=-6"},
	{value: "low-pass", label: "Low-pass filter", audioFilter: "lowpass=f=3000"},
	{value: "high-pass", label: "High-pass filter", audioFilter: "highpass=f=200"},
	{value: "telephone", label: "Telephone effect", audioFilter: "highpass=f=500,lowpass=f=3000"},
	{value: "echo", label: "Echo", audioFilter: "aecho=0.8:0.9:500:0.3"},
	{value: "chorus", label: "Chorus", audioFilter: "chorus=0.5:0.9:50|60:0.4|0.32:0.25|0.4:2|2.3"},
	{value: "flanger", label: "Flanger", audioFilter: "flanger"},
	{value: "tremolo", label: "Tremolo", audioFilter: "tremolo"},
	{value: "vibrato", label: "Vibrato", audioFilter: "vibrato"},
	{value: "compressor", label: "Audio compressor", audioFilter: "acompressor=threshold=0.125:ratio=4:attack=20:release=250"},
	{value: "normalize", label: "Normalize loudness", audioFilter: "loudnorm"},
	{value: "fade-in", label: "Fade in", audioFilter: "afade=t=in:st=0:d=2"},
	{value: "reverb", label: "Reverb", audioFilter: "aecho=0.8:0.88:60:0.4"},
	{value: "radio", label: "Noisy radio", audioFilter: "highpass=f=500,lowpass=f=3000,acrusher=bits=8:mix=0.3"},
}

var videoMediaEffects = []mediaEffect{
	{value: "grayscale", label: "Grayscale", videoFilter: "hue=s=0"},
	{value: "sepia", label: "Sepia", videoFilter: "colorchannelmixer=.393:.769:.189:0:.349:.686:.168:0:.272:.534:.131"},
	{value: "invert", label: "Invert colors", videoFilter: "negate"},
	{value: "mirror", label: "Mirror horizontally", videoFilter: "hflip"},
	{value: "flip", label: "Flip vertically", videoFilter: "vflip"},
	{value: "rotate-right", label: "Rotate right", videoFilter: "transpose=clock"},
	{value: "rotate-left", label: "Rotate left", videoFilter: "transpose=cclock"},
	{value: "blur", label: "Blur", videoFilter: "boxblur=10:1"},
	{value: "sharpen", label: "Sharpen", videoFilter: "unsharp=5:5:1.5:5:5:0"},
	{value: "pixelate", label: "Pixelate", videoFilter: "scale=iw/12:ih/12:flags=neighbor,scale=iw*12:ih*12:flags=neighbor"},
	{value: "brighten", label: "Brighten", videoFilter: "eq=brightness=0.15"},
	{value: "darken", label: "Darken", videoFilter: "eq=brightness=-0.15"},
	{value: "contrast-up", label: "Increase contrast", videoFilter: "eq=contrast=1.5"},
	{value: "contrast-down", label: "Decrease contrast", videoFilter: "eq=contrast=0.7"},
	{value: "saturate", label: "Boost color", videoFilter: "eq=saturation=1.8"},
	{value: "desaturate", label: "Soften color", videoFilter: "eq=saturation=0.3"},
	{value: "hue-shift", label: "Shift hue", videoFilter: "hue=h=45"},
	{value: "vignette", label: "Vignette", videoFilter: "vignette"},
	{value: "warm", label: "Warm colors", videoFilter: "colorbalance=rs=.2:bs=-.2"},
	{value: "cool", label: "Cool colors", videoFilter: "colorbalance=rs=-.2:bs=.2"},
	{value: "slow-motion", label: "Slow motion", videoSpeed: 2, audioSpeed: 0.5},
	{value: "fast-motion", label: "Speed up", videoSpeed: 0.8, audioSpeed: 1.25},
	{value: "mute", label: "Remove audio", mute: true},
	{value: "resize", label: "Resize..."},
	{value: "optimize", label: "Optimize (lossless)", optimize: true},
}

func mediaEffects(kind mediaKind) []mediaEffect {
	switch kind {
	case mediaImage:
		return imageMediaEffects
	case mediaAudio:
		return audioMediaEffects
	default:
		return videoMediaEffects
	}
}

func handleMedia(s *discordgo.Session, i *discordgo.InteractionCreate) {
	app := i.ApplicationCommandData()
	attachmentID := ""
	for _, option := range app.Options {
		if option.Name == "file" {
			attachmentID, _ = option.Value.(string)
			break
		}
	}
	var attachment *discordgo.MessageAttachment
	if app.Resolved != nil {
		attachment = app.Resolved.Attachments[attachmentID]
	}
	if attachmentID == "" || attachment == nil {
		respondMedia(s, i, "❌ Please attach a media file.")
		return
	}
	if attachment.Size > maxMediaInputBytes {
		respondMedia(s, i, fmt.Sprintf("❌ That file is too large. The maximum input size is %d MiB.", maxMediaInputBytes>>20))
		return
	}
	if attachment.URL == "" {
		respondMedia(s, i, "❌ Discord did not provide a downloadable URL for that attachment.")
		return
	}

	ownerID := mediaInteractionUserID(i)
	if ownerID == "" {
		respondMedia(s, i, "❌ Could not identify the user who uploaded this media.")
		return
	}
	if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Flags: discordgo.MessageFlagsEphemeral},
	}); err != nil {
		log.Printf("[media] failed to acknowledge /media: %v", err)
		return
	}
	select {
	case mediaDownloadSemaphore <- struct{}{}:
		defer func() { <-mediaDownloadSemaphore }()
	default:
		mediaEditMessage(s, i, "❌ Media uploads are busy right now. Please try again shortly.", &[]discordgo.MessageComponent{}, nil)
		return
	}

	directory, inputPath, size, err := downloadMediaAttachment(attachment)
	if err != nil {
		mediaEditMessage(s, i, "❌ "+err.Error(), &[]discordgo.MessageComponent{}, nil)
		return
	}
	kind, frameCount, err := probeMediaKind(inputPath, attachment.Filename, attachment.ContentType)
	if err != nil {
		removeMediaDirectory(directory)
		log.Printf("[media] ffprobe failed for attachment %q: %v", attachment.Filename, err)
		mediaEditMessage(s, i, "❌ This file could not be read as supported audio, video, or image media.", &[]discordgo.MessageComponent{}, nil)
		return
	}

	session := &mediaSession{
		ownerID: ownerID, directory: directory, inputPath: inputPath,
		filename: attachment.Filename, contentType: attachment.ContentType,
		inputSize: size, kind: kind, frameCount: frameCount, expiresAt: time.Now().Add(mediaSessionTTL),
	}
	sessionID, err := storeMediaSession(session)
	if err != nil {
		removeMediaDirectory(directory)
		mediaEditMessage(s, i, "❌ "+err.Error(), &[]discordgo.MessageComponent{}, nil)
		return
	}

	options := make([]discordgo.SelectMenuOption, 0, len(mediaEffects(kind)))
	for _, effect := range mediaEffects(kind) {
		options = append(options, discordgo.SelectMenuOption{
			Label: effect.label,
			Value: effect.value,
		})
	}
	components := []discordgo.MessageComponent{
		discordgo.ActionsRow{Components: []discordgo.MessageComponent{
			discordgo.SelectMenu{
				MenuType:    discordgo.StringSelectMenu,
				CustomID:    "media:" + sessionID,
				Placeholder: "Choose one of the available transformations",
				Options:     options,
				MaxValues:   1,
			},
		}},
	}
	kindLabel := map[mediaKind]string{mediaImage: "image", mediaAudio: "audio", mediaVideo: "video"}[kind]
	message := fmt.Sprintf("Choose a transformation for this %s. This private menu expires in 15 minutes.", kindLabel)
	mediaEditMessage(s, i, message, &components, nil)
}

func handleMediaComponent(s *discordgo.Session, i *discordgo.InteractionCreate) {
	data := i.MessageComponentData()
	sessionID := strings.TrimPrefix(data.CustomID, "media:")
	if len(data.Values) != 1 {
		respondMedia(s, i, "❌ Choose exactly one media transformation.")
		return
	}
	if data.Values[0] == "resize" {
		showMediaResizeModal(s, i, sessionID)
		return
	}
	ownerID := mediaInteractionUserID(i)
	session, ok := lookupMediaSession(sessionID, ownerID)
	if !ok {
		respondMedia(s, i, "❌ This media menu has expired or belongs to another user. Run `/media` again.")
		return
	}
	effect, ok := findMediaEffect(session.kind, data.Values[0])
	if !ok {
		respondMedia(s, i, "❌ That transformation is not available for this media.")
		return
	}
	session, ok = takeMediaSession(sessionID, ownerID)
	if !ok {
		respondMedia(s, i, "❌ This media menu has expired or belongs to another user. Run `/media` again.")
		return
	}
	defer removeMediaDirectory(session.directory)
	if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredMessageUpdate,
	}); err != nil {
		log.Printf("[media] failed to acknowledge transformation: %v", err)
		return
	}
	select {
	case mediaProcessSemaphore <- struct{}{}:
		defer func() { <-mediaProcessSemaphore }()
	default:
		mediaEditMessage(s, i, "❌ Media processing is busy right now. Please try again shortly.", &[]discordgo.MessageComponent{}, nil)
		return
	}

	outputPath, outputName, err := transformMedia(session, effect)
	if err != nil {
		log.Printf("[media] transform %q failed for %q: %v", effect.value, session.filename, err)
		mediaEditMessage(s, i, "❌ The transformation failed. Run `/media` again or try a smaller file.", &[]discordgo.MessageComponent{}, nil)
		return
	}
	file, err := os.Open(outputPath)
	if err != nil {
		log.Printf("[media] could not open transformed file: %v", err)
		mediaEditMessage(s, i, "❌ The transformed file could not be opened for upload.", &[]discordgo.MessageComponent{}, nil)
		return
	}
	defer file.Close()

	content := fmt.Sprintf("✅ Applied **%s**.", effect.label)
	mediaEditMessage(s, i, content, &[]discordgo.MessageComponent{}, []*discordgo.File{{Name: outputName, Reader: file}})
}

func showMediaResizeModal(s *discordgo.Session, i *discordgo.InteractionCreate, sessionID string) {
	session, ok := lookupMediaSession(sessionID, mediaInteractionUserID(i))
	if !ok {
		respondMedia(s, i, "❌ This media menu has expired or belongs to another user. Run `/media` again.")
		return
	}
	if session.kind != mediaImage && session.kind != mediaVideo {
		respondMedia(s, i, "❌ Resize is only available for images and videos.")
		return
	}
	response := &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseModal,
		Data: &discordgo.InteractionResponseData{
			CustomID: "media:resize:" + sessionID,
			Title:    "Resize media",
			Components: []discordgo.MessageComponent{
				discordgo.ActionsRow{Components: []discordgo.MessageComponent{
					discordgo.TextInput{
						CustomID:    "width",
						Label:       "Width in pixels",
						Style:       discordgo.TextInputShort,
						Placeholder: "Required, 1-8192",
						Required:    true,
						MinLength:   1,
						MaxLength:   4,
					},
				}},
				discordgo.ActionsRow{Components: []discordgo.MessageComponent{
					discordgo.TextInput{
						CustomID:    "height",
						Label:       "Height in pixels (optional)",
						Style:       discordgo.TextInputShort,
						Placeholder: "Leave blank to preserve aspect ratio",
						Required:    false,
						MaxLength:   4,
					},
				}},
			},
		},
	}
	if err := s.InteractionRespond(i.Interaction, response); err != nil {
		log.Printf("[media] failed to show resize modal: %v", err)
	}
}

func handleMediaResizeModal(s *discordgo.Session, i *discordgo.InteractionCreate) {
	data := i.ModalSubmitData()
	parts := strings.Split(data.CustomID, ":")
	if len(parts) != 3 || parts[0] != "media" || parts[1] != "resize" {
		respondMedia(s, i, "❌ Invalid resize request. Run `/media` again.")
		return
	}
	widthText, heightText, ok := mediaResizeModalValues(data.Components)
	if !ok {
		respondMedia(s, i, "❌ Enter a valid width and optional height in pixels.")
		return
	}
	width, height, err := parseMediaResizeDimensions(widthText, heightText)
	if err != nil {
		respondMedia(s, i, "❌ "+err.Error())
		return
	}
	ownerID := mediaInteractionUserID(i)
	session, ok := lookupMediaSession(parts[2], ownerID)
	if !ok {
		respondMedia(s, i, "❌ This media menu has expired or belongs to another user. Run `/media` again.")
		return
	}
	if err := validateMediaResizeDimensions(session.kind, width, height); err != nil {
		respondMedia(s, i, "❌ "+err.Error())
		return
	}
	session, ok = takeMediaSession(parts[2], ownerID)
	if !ok {
		respondMedia(s, i, "❌ This media menu has expired or belongs to another user. Run `/media` again.")
		return
	}
	sessionEffect := mediaEffect{
		value: "resize", label: fmt.Sprintf("Resized to %s", mediaResizeLabel(width, height)),
		resizeWidth: width, resizeHeight: height,
	}
	processMediaModalResult(s, i, session, sessionEffect)
}

func mediaResizeModalValues(components []discordgo.MessageComponent) (string, string, bool) {
	var width, height string
	for _, component := range components {
		row, ok := component.(*discordgo.ActionsRow)
		if !ok {
			continue
		}
		for _, nested := range row.Components {
			input, ok := nested.(*discordgo.TextInput)
			if !ok {
				continue
			}
			switch input.CustomID {
			case "width":
				width = strings.TrimSpace(input.Value)
			case "height":
				height = strings.TrimSpace(input.Value)
			}
		}
	}
	return width, height, width != ""
}

func parseMediaResizeDimensions(widthText, heightText string) (int, int, error) {
	width, err := strconv.Atoi(widthText)
	if err != nil || width < 1 || width > 8192 {
		return 0, 0, errors.New("width must be a whole number from 1 to 8192.")
	}
	height := 0
	if heightText != "" {
		height, err = strconv.Atoi(heightText)
		if err != nil || height < 1 || height > 8192 {
			return 0, 0, errors.New("height must be blank or a whole number from 1 to 8192.")
		}
	}
	return width, height, nil
}

func validateMediaResizeDimensions(kind mediaKind, width, height int) error {
	if kind == mediaVideo && (width%2 != 0 || (height != 0 && height%2 != 0)) {
		return errors.New("video width and height must be even numbers for MP4 compatibility.")
	}
	return nil
}

func mediaResizeLabel(width, height int) string {
	if height == 0 {
		return fmt.Sprintf("%d px wide (aspect ratio preserved)", width)
	}
	return fmt.Sprintf("%d x %d px", width, height)
}

func processMediaModalResult(s *discordgo.Session, i *discordgo.InteractionCreate, session *mediaSession, effect mediaEffect) {
	defer removeMediaDirectory(session.directory)
	if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Flags: discordgo.MessageFlagsEphemeral},
	}); err != nil {
		log.Printf("[media] failed to acknowledge modal transformation: %v", err)
		return
	}
	select {
	case mediaProcessSemaphore <- struct{}{}:
		defer func() { <-mediaProcessSemaphore }()
	default:
		mediaEditMessage(s, i, "❌ Media processing is busy right now. Please try again shortly.", &[]discordgo.MessageComponent{}, nil)
		return
	}
	outputPath, outputName, err := transformMedia(session, effect)
	if err != nil {
		log.Printf("[media] transform %q failed for %q: %v", effect.value, session.filename, err)
		mediaEditMessage(s, i, "❌ The transformation failed. Run `/media` again or try a smaller file.", &[]discordgo.MessageComponent{}, nil)
		return
	}
	file, err := os.Open(outputPath)
	if err != nil {
		log.Printf("[media] could not open transformed file: %v", err)
		mediaEditMessage(s, i, "❌ The transformed file could not be opened for upload.", &[]discordgo.MessageComponent{}, nil)
		return
	}
	defer file.Close()
	mediaEditMessage(s, i, "✅ Applied **"+effect.label+"**.", &[]discordgo.MessageComponent{}, []*discordgo.File{{Name: outputName, Reader: file}})
}

func respondMedia(s *discordgo.Session, i *discordgo.InteractionCreate, content string) {
	if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Content: content, Flags: discordgo.MessageFlagsEphemeral},
	}); err != nil {
		log.Printf("[media] failed to send response: %v", err)
	}
}

func mediaEditMessage(s *discordgo.Session, i *discordgo.InteractionCreate, content string, components *[]discordgo.MessageComponent, files []*discordgo.File) {
	if _, err := s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
		Content: &content, Components: components, Files: files,
	}); err != nil {
		log.Printf("[media] failed to update interaction response: %v", err)
	}
}

func mediaInteractionUserID(i *discordgo.InteractionCreate) string {
	if i == nil || i.Interaction == nil {
		return ""
	}
	if i.Member != nil && i.Member.User != nil {
		return i.Member.User.ID
	}
	if i.User != nil {
		return i.User.ID
	}
	return ""
}

func downloadMediaAttachment(attachment *discordgo.MessageAttachment) (string, string, int64, error) {
	parsedURL, err := url.Parse(attachment.URL)
	if err != nil || parsedURL.Scheme != "https" || parsedURL.User != nil || !isDiscordMediaHost(parsedURL.Hostname()) {
		return "", "", 0, errors.New("the attachment URL is not a valid Discord media URL")
	}
	if attachment.Size > maxMediaInputBytes {
		return "", "", 0, fmt.Errorf("the maximum input size is %d MiB", maxMediaInputBytes>>20)
	}
	request, err := http.NewRequest(http.MethodGet, parsedURL.String(), nil)
	if err != nil {
		return "", "", 0, fmt.Errorf("could not create media download request: %w", err)
	}
	response, err := mediaTransformHTTPClient.Do(request)
	if err != nil {
		return "", "", 0, fmt.Errorf("could not download the media attachment: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", "", 0, fmt.Errorf("Discord returned HTTP %d while downloading the attachment", response.StatusCode)
	}
	if response.ContentLength > maxMediaInputBytes {
		return "", "", 0, fmt.Errorf("the maximum input size is %d MiB", maxMediaInputBytes>>20)
	}

	directory, err := os.MkdirTemp("", "sunfire-media-")
	if err != nil {
		return "", "", 0, fmt.Errorf("could not create temporary media storage: %w", err)
	}
	inputPath := filepath.Join(directory, "input")
	output, err := os.OpenFile(inputPath, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		removeMediaDirectory(directory)
		return "", "", 0, fmt.Errorf("could not create temporary media file: %w", err)
	}
	written, copyErr := io.Copy(output, io.LimitReader(response.Body, maxMediaInputBytes+1))
	closeErr := output.Close()
	if copyErr != nil {
		removeMediaDirectory(directory)
		return "", "", 0, fmt.Errorf("could not save the downloaded media: %w", copyErr)
	}
	if closeErr != nil {
		removeMediaDirectory(directory)
		return "", "", 0, fmt.Errorf("could not finish saving the downloaded media: %w", closeErr)
	}
	if written == 0 {
		removeMediaDirectory(directory)
		return "", "", 0, errors.New("the attached file is empty")
	}
	if written > maxMediaInputBytes {
		removeMediaDirectory(directory)
		return "", "", 0, fmt.Errorf("the maximum input size is %d MiB", maxMediaInputBytes>>20)
	}
	return directory, inputPath, written, nil
}

func isDiscordMediaHost(host string) bool {
	switch strings.ToLower(host) {
	case "cdn.discordapp.com", "media.discordapp.net", "cdn.discordapp.net":
		return true
	default:
		return false
	}
}

func probeMediaKind(inputPath, filename, contentType string) (mediaKind, int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-show_entries", "stream=codec_type", "-of", "json", inputPath)
	var stderr limitedBuffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		if ctx.Err() != nil {
			return 0, 0, fmt.Errorf("ffprobe timed out: %w", ctx.Err())
		}
		return 0, 0, fmt.Errorf("ffprobe could not inspect the media: %w (%s)", err, strings.TrimSpace(stderr.String()))
	}
	var result struct {
		Streams []struct {
			CodecType   string `json:"codec_type"`
			Disposition struct {
				AttachedPicture int `json:"attached_pic"`
			} `json:"disposition"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		return 0, 0, fmt.Errorf("ffprobe returned invalid stream information: %w", err)
	}
	hasAudio, hasVideo := false, false
	for _, stream := range result.Streams {
		hasAudio = hasAudio || stream.CodecType == "audio"
		hasVideo = hasVideo || (stream.CodecType == "video" && stream.Disposition.AttachedPicture == 0)
	}
	if hasAudio && !hasVideo {
		return mediaAudio, 0, nil
	}
	if !hasVideo {
		return 0, 0, errors.New("the file contains no supported audio or video stream")
	}
	if isImageAttachment(filename, contentType) {
		frameCount, err := probeImageFrameCount(inputPath)
		if err != nil {
			return 0, 0, err
		}
		return mediaImage, frameCount, nil
	}
	return mediaVideo, 0, nil
}

func probeImageFrameCount(inputPath string) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-count_frames", "-select_streams", "v:0", "-show_entries", "stream=nb_read_frames", "-of", "default=nokey=1:noprint_wrappers=1", inputPath)
	var stderr limitedBuffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		if ctx.Err() != nil {
			return 0, fmt.Errorf("ffprobe timed out while checking image frames: %w", ctx.Err())
		}
		return 0, fmt.Errorf("ffprobe could not count image frames: %w (%s)", err, strings.TrimSpace(stderr.String()))
	}
	frameCount, err := strconv.Atoi(strings.TrimSpace(string(output)))
	if err != nil || frameCount < 1 {
		return 0, fmt.Errorf("ffprobe returned an invalid image frame count %q", strings.TrimSpace(string(output)))
	}
	return frameCount, nil
}

func isImageAttachment(filename, contentType string) bool {
	contentType = strings.ToLower(contentType)
	if strings.HasPrefix(contentType, "image/") {
		return true
	}
	if strings.HasPrefix(contentType, "audio/") || strings.HasPrefix(contentType, "video/") {
		return false
	}
	extension := strings.ToLower(filepath.Ext(filename))
	if contentType := mime.TypeByExtension(extension); strings.HasPrefix(contentType, "image/") {
		return true
	}
	switch extension {
	case ".avif", ".bmp", ".gif", ".heic", ".heif", ".ico", ".jxl", ".tif", ".tiff":
		return true
	default:
		return false
	}
}

func storeMediaSession(session *mediaSession) (string, error) {
	idBytes := make([]byte, 12)
	if _, err := rand.Read(idBytes); err != nil {
		return "", fmt.Errorf("could not create a private media session: %w", err)
	}
	id := hex.EncodeToString(idBytes)
	mediaSessionStore.Lock()
	defer mediaSessionStore.Unlock()
	if len(mediaSessionStore.entries) >= maxMediaSessions || mediaSessionStore.bytes+session.inputSize > maxMediaSessionBytes {
		return "", errors.New("media processing is busy right now; try again after an existing menu expires")
	}
	session.timer = time.AfterFunc(mediaSessionTTL, func() {
		removeMediaSession(id)
	})
	mediaSessionStore.entries[id] = session
	mediaSessionStore.bytes += session.inputSize
	return id, nil
}

func takeMediaSession(id, ownerID string) (*mediaSession, bool) {
	mediaSessionStore.Lock()
	session, exists := mediaSessionStore.entries[id]
	if !exists || ownerID == "" || session.ownerID != ownerID || time.Now().After(session.expiresAt) {
		mediaSessionStore.Unlock()
		return nil, false
	}
	delete(mediaSessionStore.entries, id)
	mediaSessionStore.bytes -= session.inputSize
	session.timer.Stop()
	mediaSessionStore.Unlock()
	return session, true
}

func lookupMediaSession(id, ownerID string) (*mediaSession, bool) {
	mediaSessionStore.Lock()
	defer mediaSessionStore.Unlock()
	session, exists := mediaSessionStore.entries[id]
	if !exists || ownerID == "" || session.ownerID != ownerID || time.Now().After(session.expiresAt) {
		return nil, false
	}
	return session, true
}

func removeMediaSession(id string) {
	mediaSessionStore.Lock()
	session, exists := mediaSessionStore.entries[id]
	if exists {
		delete(mediaSessionStore.entries, id)
		mediaSessionStore.bytes -= session.inputSize
	}
	mediaSessionStore.Unlock()
	if exists {
		removeMediaDirectory(session.directory)
	}
}

func removeMediaDirectory(directory string) {
	if err := os.RemoveAll(directory); err != nil {
		log.Printf("[media] failed to remove temporary directory %q: %v", directory, err)
	}
}

func findMediaEffect(kind mediaKind, value string) (mediaEffect, bool) {
	for _, effect := range mediaEffects(kind) {
		if effect.value == value {
			return effect, true
		}
	}
	return mediaEffect{}, false
}

func transformMedia(session *mediaSession, effect mediaEffect) (string, string, error) {
	if effect.optimize && session.kind == mediaImage && session.frameCount > 1 {
		return session.inputPath, filepath.Base(session.filename), nil
	}
	outputExtension := ".mp4"
	switch session.kind {
	case mediaImage:
		outputExtension = ".png"
		if strings.EqualFold(filepath.Ext(session.filename), ".gif") || strings.EqualFold(session.contentType, "image/gif") {
			outputExtension = ".gif"
		}
	case mediaAudio:
		outputExtension = ".mp3"
	}
	if effect.optimize {
		switch session.kind {
		case mediaImage:
			if outputExtension != ".gif" {
				outputExtension = ".png"
			}
		case mediaVideo:
			outputExtension = ".mkv"
		}
	}
	outputName := "transformed" + outputExtension
	outputPath := filepath.Join(session.directory, outputName)

	args := []string{"-nostdin", "-hide_banner", "-loglevel", "error", "-y", "-filter_threads", "2", "-i", session.inputPath}
	switch session.kind {
	case mediaImage:
		if effect.optimize {
			if outputExtension == ".gif" {
				args = appendImageTransformArgs(args, effect, outputExtension)
			} else {
				args = append(args, "-map", "0:v:0", "-an", "-frames:v", "1", "-c:v", "png", "-compression_level", "9", "-pix_fmt", "+")
			}
		} else {
			args = appendImageTransformArgs(args, effect, outputExtension)
		}
	case mediaAudio:
		args = append(args, "-map", "0:a:0", "-vn")
		audioFilters := make([]string, 0, 3)
		if effect.audioFilter != "" {
			audioFilters = append(audioFilters, effect.audioFilter)
		}
		if effect.reverse {
			audioFilters = append(audioFilters, "areverse")
		}
		if effect.audioSpeed != 0 {
			audioFilters = append(audioFilters, fmt.Sprintf("atempo=%.3f", effect.audioSpeed))
		}
		if len(audioFilters) > 0 {
			args = append(args, "-af", strings.Join(audioFilters, ","))
		}
		if effect.audioChannels != 0 {
			args = append(args, "-ac", fmt.Sprint(effect.audioChannels))
		}
		args = append(args, "-c:a", "libmp3lame", "-q:a", "3")
	case mediaVideo:
		args = append(args, "-map", "0:v:0", "-map", "0:a?")
		if effect.optimize {
			args = append(args, "-c:v", "libx264", "-preset", "veryslow", "-qp", "0", "-pix_fmt", "+", "-c:a", "copy")
		} else if effect.mute {
			args = append(args, "-an")
		} else {
			videoFilters := make([]string, 0, 4)
			if effect.videoSpeed != 0 {
				videoFilters = append(videoFilters, fmt.Sprintf("setpts=%.3f*PTS", effect.videoSpeed))
			}
			if effect.reverse {
				videoFilters = append(videoFilters, "reverse")
			}
			if effect.videoFilter != "" {
				videoFilters = append(videoFilters, effect.videoFilter)
			}
			if effect.resizeWidth > 0 {
				videoFilters = append(videoFilters, mediaScaleFilter(effect.resizeWidth, effect.resizeHeight, true))
			} else {
				videoFilters = append(videoFilters, "scale=trunc(iw/2)*2:trunc(ih/2)*2")
			}
			args = append(args, "-vf", strings.Join(videoFilters, ","))
			audioFilters := make([]string, 0, 2)
			if effect.reverse {
				audioFilters = append(audioFilters, "areverse")
			}
			if effect.audioSpeed != 0 {
				audioFilters = append(audioFilters, fmt.Sprintf("atempo=%.3f", effect.audioSpeed))
			}
			if effect.audioFilter != "" {
				audioFilters = append(audioFilters, effect.audioFilter)
			}
			if len(audioFilters) > 0 {
				args = append(args, "-af", strings.Join(audioFilters, ","))
			}
			args = append(args, "-c:v", "libx264", "-preset", "ultrafast", "-crf", "24", "-pix_fmt", "yuv420p", "-c:a", "aac", "-b:a", "128k", "-movflags", "+faststart")
		}
	}
	args = append(args, "-fs", fmt.Sprint(maxMediaOutputBytes), outputPath)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, "ffmpeg", args...)
	var stderr limitedBuffer
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return "", "", fmt.Errorf("ffmpeg timed out: %w", ctx.Err())
		}
		return "", "", fmt.Errorf("ffmpeg exited with an error: %w (%s)", err, strings.TrimSpace(stderr.String()))
	}
	info, err := os.Stat(outputPath)
	if err != nil {
		return "", "", fmt.Errorf("ffmpeg did not create an output file: %w", err)
	}
	if info.Size() == 0 {
		return "", "", errors.New("ffmpeg produced an empty output file")
	}
	if effect.optimize {
		inputInfo, err := os.Stat(session.inputPath)
		if err != nil {
			return "", "", fmt.Errorf("could not inspect the original media: %w", err)
		}
		if info.Size() >= inputInfo.Size() {
			return session.inputPath, filepath.Base(session.filename), nil
		}
	}
	if info.Size() >= maxMediaOutputBytes {
		return "", "", fmt.Errorf("the transformed file exceeds the %d MiB upload limit", maxMediaOutputBytes>>20)
	}
	return outputPath, outputName, nil
}

func appendImageTransformArgs(args []string, effect mediaEffect, outputExtension string) []string {
	filter := effect.videoFilter
	if effect.resizeWidth > 0 {
		resizeFilter := mediaScaleFilter(effect.resizeWidth, effect.resizeHeight, false)
		if filter == "" {
			filter = resizeFilter
		} else {
			filter += "," + resizeFilter
		}
	}
	if filter == "" {
		filter = "null"
	}
	if outputExtension == ".gif" {
		graph := fmt.Sprintf("[0:v:0]%s,split[v1][v2];[v1]palettegen[p];[v2][p]paletteuse[outv]", filter)
		args = append(args, "-an", "-filter_complex", graph, "-map", "[outv]", "-loop", "0")
	} else {
		args = append(args, "-map", "0:v:0", "-an", "-vf", filter, "-frames:v", "1")
	}
	codec := "png"
	if outputExtension == ".gif" {
		codec = "gif"
	}
	return append(args, "-c:v", codec)
}

func mediaScaleFilter(width, height int, evenDimensions bool) string {
	heightValue := "-1"
	if evenDimensions {
		heightValue = "-2"
	}
	if height > 0 {
		heightValue = strconv.Itoa(height)
	}
	return fmt.Sprintf("scale=%d:%s:flags=lanczos", width, heightValue)
}

type limitedBuffer struct {
	buffer bytes.Buffer
}

func (b *limitedBuffer) Write(data []byte) (int, error) {
	const maxLoggedOutput = 8 << 10
	inputLength := len(data)
	remaining := maxLoggedOutput - b.buffer.Len()
	if remaining > 0 {
		if len(data) > remaining {
			data = data[:remaining]
		}
		_, _ = b.buffer.Write(data)
	}
	return inputLength, nil
}

func (b *limitedBuffer) String() string {
	return b.buffer.String()
}

```

## `./discord_logger.go`

```go
// discord_logger.go — sends bot events to the configured logging channel.
package main

import (
	"fmt"
	"log"
	"time"

	"github.com/bwmarrin/discordgo"
)

type logger struct {
	session *discordgo.Session
}

func newLogger(s *discordgo.Session) *logger {
	return &logger{session: s}
}

func (l *logger) log(level, msg string) {
	l.logTo(channelID(), level, msg)
}

func (l *logger) logTo(cid, level, msg string) {
	if cid == "" {
		return
	}
	content := fmt.Sprintf("**[%s] %s** %s", time.Now().Format("15:04:05"), level, msg)
	if len(content) > 2000 {
		content = content[:1996] + "..."
	}
	if _, err := l.session.ChannelMessageSend(cid, content); err != nil {
		logErrorf("logger: send to %s: %v", cid, err)
	}
}

func channelID() string {
	cfg, err := loadConfig()
	if err != nil || cfg == nil {
		log.Printf("logger: failed to load config: %v", err)
		return ""
	}
	return cfg.LoggingChannel
}

func formatTimestamp() string {
	return time.Now().Format("2006-01-02 15:04:05")
}

```

## `./riddles.go`

```go
package main

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
)

type RiddleEntry struct {
	ID            int      `json:"id"`
	Question      string   `json:"question"`
	Answers       []string `json:"answers"`
	DisplayAnswer string   `json:"displayAnswer"`
}

type RiddleSession struct {
	ChannelID   string
	GuildID     string
	StartedByID string
	RoundIndex  int
	TotalRounds int
	Current     RiddleEntry
	Scores      map[string]int // userID -> points
	Active      bool
	Timer       *time.Timer
}

var (
	riddlesMu     sync.Mutex
	allRiddles    []RiddleEntry
	activeRiddles map[string]*RiddleSession // channelID -> session
)

func initRiddles() {
	if len(allRiddles) > 0 {
		return
	}
	path := dataPath("riddles.json")
	data, err := os.ReadFile(path)
	if err == nil {
		_ = json.Unmarshal(data, &allRiddles)
	}
	activeRiddles = make(map[string]*RiddleSession)
}

func handleRiddleCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	initRiddles()
	riddlesMu.Lock()
	defer riddlesMu.Unlock()

	options := i.ApplicationCommandData().Options
	if len(options) == 0 {
		return
	}

	sub := options[0]
	chID := i.ChannelID

	switch sub.Name {
	case "start":
		if session, ok := activeRiddles[chID]; ok && session.Active {
			_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content: fmt.Sprintf("⚠️ A riddle game is already active here! Current round: %d/%d", session.RoundIndex+1, session.TotalRounds),
					Flags:   discordgo.MessageFlagsEphemeral,
				},
			})
			return
		}

		if len(allRiddles) == 0 {
			_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{Content: "❌ No riddles available in Data/riddles.json."},
			})
			return
		}

		session := &RiddleSession{
			ChannelID:   chID,
			GuildID:     i.GuildID,
			StartedByID: i.Member.User.ID,
			RoundIndex:  0,
			TotalRounds: 10,
			Scores:      make(map[string]int),
			Active:      true,
		}
		activeRiddles[chID] = session

		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "🧩 **10-Round Riddle Game Started!** Type your answers in this channel. First to guess correctly scores a point!",
			},
		})

		go startRiddleRound(s, session)

	case "stop":
		session, ok := activeRiddles[chID]
		if !ok || !session.Active {
			_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{Content: "There is no active riddle game in this channel.", Flags: discordgo.MessageFlagsEphemeral},
			})
			return
		}

		session.Active = false
		if session.Timer != nil {
			session.Timer.Stop()
		}
		delete(activeRiddles, chID)

		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{Content: "🛑 Riddle game has been stopped."},
		})

	case "status":
		session, ok := activeRiddles[chID]
		if !ok || !session.Active {
			_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{Content: "No active riddle game in this channel.", Flags: discordgo.MessageFlagsEphemeral},
			})
			return
		}

		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: fmt.Sprintf("🧩 **Riddle Game:** Round %d/%d\n**Current Question:** %s", session.RoundIndex+1, session.TotalRounds, session.Current.Question),
			},
		})
	}
}

func startRiddleRound(s *discordgo.Session, session *RiddleSession) {
	riddlesMu.Lock()
	if !session.Active || session.RoundIndex >= session.TotalRounds {
		riddlesMu.Unlock()
		endRiddleGame(s, session)
		return
	}

	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	session.Current = allRiddles[rng.Intn(len(allRiddles))]
	currQ := session.Current
	roundNum := session.RoundIndex + 1
	total := session.TotalRounds
	riddlesMu.Unlock()

	embed := &discordgo.MessageEmbed{
		Title:       fmt.Sprintf("🧩 Riddle Round %d of %d", roundNum, total),
		Description: fmt.Sprintf("**%s**\n\n*(You have 45 seconds to solve it!)*", currQ.Question),
		Color:       0xF1C40F,
	}

	_, _ = s.ChannelMessageSendEmbed(session.ChannelID, embed)

	time.Sleep(45 * time.Second)

	riddlesMu.Lock()
	if !session.Active || session.RoundIndex+1 != roundNum {
		riddlesMu.Unlock()
		return
	}

	// Time expired for this round without correct answer
	_, _ = s.ChannelMessageSend(session.ChannelID, fmt.Sprintf("⏰ Time's up! The answer was: **%s**", currQ.DisplayAnswer))
	session.RoundIndex++
	riddlesMu.Unlock()

	time.Sleep(3 * time.Second)
	startRiddleRound(s, session)
}

func checkRiddleAnswer(s *discordgo.Session, m *discordgo.MessageCreate) bool {
	riddlesMu.Lock()
	session, ok := activeRiddles[m.ChannelID]
	if !ok || !session.Active || session.Current.Question == "" {
		riddlesMu.Unlock()
		return false
	}

	ans := strings.ToLower(strings.TrimSpace(m.Content))
	matched := false
	for _, valid := range session.Current.Answers {
		if strings.EqualFold(ans, strings.TrimSpace(valid)) {
			matched = true
			break
		}
	}

	if matched {
		session.Scores[m.Author.ID]++
		displayAns := session.Current.DisplayAnswer
		session.RoundIndex++
		riddlesMu.Unlock()

		_, _ = s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("🎉 Correct, <@%s>! The answer was **%s** (+1 pt)!", m.Author.ID, displayAns))
		time.Sleep(3 * time.Second)
		go startRiddleRound(s, session)
		return true
	}

	riddlesMu.Unlock()
	return false
}

func endRiddleGame(s *discordgo.Session, session *RiddleSession) {
	riddlesMu.Lock()
	session.Active = false
	delete(activeRiddles, session.ChannelID)

	var scoreLines []string
	for uID, pts := range session.Scores {
		scoreLines = append(scoreLines, fmt.Sprintf("• <@%s>: **%d** pt(s)", uID, pts))
	}
	riddlesMu.Unlock()

	scoreText := "No one scored any points!"
	if len(scoreLines) > 0 {
		scoreText = strings.Join(scoreLines, "\n")
	}

	embed := &discordgo.MessageEmbed{
		Title:       "🏆 Riddle Game Finished!",
		Description: fmt.Sprintf("**Final Standings:**\n%s", scoreText),
		Color:       0x2ECC71,
	}
	_, _ = s.ChannelMessageSendEmbed(session.ChannelID, embed)
}

```

## `./notifier_scrape.go`

```go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
)

type scrapeState struct {
	Posted       map[string][]string `json:"posted"`
	Attempted    map[string][]string `json:"attempted"`
	LastDailyRun string              `json:"lastDailyRun"`
}

var (
	scrapeMu        sync.Mutex
	scrapeCache     scrapeState
	scrapeCachePath string
	imgRegex        = regexp.MustCompile(`https?://[^\s"'<>]+\.(?:jpg|jpeg|png|webp)`)
)

func initScrapeCache() {
	scrapeCachePath = dataPath("scrapeImagesCache.json")
	scrapeCache = scrapeState{
		Posted:    make(map[string][]string),
		Attempted: make(map[string][]string),
	}

	data, err := os.ReadFile(scrapeCachePath)
	if err == nil {
		if err := json.Unmarshal(data, &scrapeCache); err != nil {
			log.Printf("Image scrape: parse cache %s: %v", scrapeCachePath, err)
			scrapeCache = scrapeState{
				Posted:    make(map[string][]string),
				Attempted: make(map[string][]string),
			}
		}
	} else if !os.IsNotExist(err) {
		log.Printf("Image scrape: read cache %s: %v", scrapeCachePath, err)
	}
	if scrapeCache.Posted == nil {
		scrapeCache.Posted = make(map[string][]string)
	}
	if scrapeCache.Attempted == nil {
		scrapeCache.Attempted = make(map[string][]string)
	}
}

func saveScrapeCache() error {
	if scrapeCachePath == "" {
		return fmt.Errorf("scrape cache path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(scrapeCachePath), 0755); err != nil {
		return fmt.Errorf("creating scrape cache directory: %w", err)
	}
	data, err := json.MarshalIndent(scrapeCache, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding scrape cache: %w", err)
	}
	if err := os.WriteFile(scrapeCachePath, data, 0644); err != nil {
		return fmt.Errorf("writing scrape cache %s: %w", scrapeCachePath, err)
	}
	return nil
}

func runDailyImageScrape(s *discordgo.Session, cfg *Config) {
	if len(cfg.Scrape.URLs) == 0 || len(cfg.Scrape.ChannelIDs) == 0 {
		return
	}

	scrapeMu.Lock()
	defer scrapeMu.Unlock()

	client := notifierClient(20 * time.Second)

	for _, targetURL := range cfg.Scrape.URLs {
		req, err := http.NewRequest("GET", targetURL, nil)
		if err != nil {
			log.Printf("Image scrape: create request for %s: %v", targetURL, err)
			continue
		}
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")

		resp, err := client.Do(req)
		if err != nil {
			log.Printf("Image scrape: fetch %s: %v", targetURL, err)
			continue
		}
		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			log.Printf("Image scrape: read %s: %v", targetURL, readErr)
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			log.Printf("Image scrape: %s returned HTTP %d", targetURL, resp.StatusCode)
			continue
		}

		matches := imgRegex.FindAllString(string(body), -1)
		for _, chID := range cfg.Scrape.ChannelIDs {
			cacheKey := targetURL + "\x00" + chID
			postedMap := make(map[string]bool)
			for _, u := range scrapeCache.Posted[targetURL] {
				postedMap[u] = true
			}
			for _, u := range scrapeCache.Posted[cacheKey] {
				postedMap[u] = true
			}

			var freshImage string
			for _, imgURL := range matches {
				lowerURL := strings.ToLower(imgURL)
				if !postedMap[imgURL] && !strings.Contains(lowerURL, "logo") && !strings.Contains(lowerURL, "icon") {
					freshImage = imgURL
					break
				}
			}
			if freshImage == "" {
				continue
			}

			embed := &discordgo.MessageEmbed{
				Title: "📸 Daily Scraped Gallery Image",
				Image: &discordgo.MessageEmbedImage{
					URL: freshImage,
				},
				Color:     0xF47FFF,
				Timestamp: time.Now().Format(time.RFC3339),
			}
			if _, err := sendNotifierEmbed(s, chID, embed); err != nil {
				log.Printf("Image scrape: send image from %s to channel %s: %v", targetURL, chID, err)
				continue
			}

			previousPosted := append([]string(nil), scrapeCache.Posted[cacheKey]...)
			previousLastRun := scrapeCache.LastDailyRun
			scrapeCache.Posted[cacheKey] = append(scrapeCache.Posted[cacheKey], freshImage)
			scrapeCache.LastDailyRun = time.Now().Format(time.RFC3339)
			if err := saveScrapeCache(); err != nil {
				scrapeCache.Posted[cacheKey] = previousPosted
				scrapeCache.LastDailyRun = previousLastRun
				log.Printf("Image scrape: persist delivered image from %s to channel %s: %v", targetURL, chID, err)
			}
		}
	}
}

// startScrapeImagesDaemon runs daily image scrape from configured galleries
func startScrapeImagesDaemon(ctx context.Context, s *discordgo.Session) {
	initScrapeCache()
	cfg, err := loadConfig()
	if err != nil {
		log.Printf("Image scrape: load config: %v", err)
		return
	}
	if len(cfg.Scrape.URLs) == 0 || len(cfg.Scrape.ChannelIDs) == 0 {
		log.Printf("Image scrape not started: no scrape URLs or target channels configured")
		return
	}

	log.Printf("Scrape images daemon started (interval 24h)")
	runPeriodic(ctx, 90*time.Second, 24*time.Hour, func() { runDailyImageScrape(s, cfg) })
}

```

## `./reminders_test.go`

```go
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseReminderTimeRelativeAndExact(t *testing.T) {
	now := time.Date(2026, time.September, 29, 12, 0, 0, 0, time.UTC)
	location, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatal(err)
	}

	relative, err := parseReminderTimeAt(" in 90m ", location, now)
	if err != nil {
		t.Fatalf("parse relative time: %v", err)
	}
	if want := now.Add(90 * time.Minute); !relative.Equal(want) {
		t.Fatalf("relative time = %s; want %s", relative, want)
	}

	exact, err := parseReminderTimeAt("09/29/2026 at 14:30", location, now)
	if err != nil {
		t.Fatalf("parse local exact time: %v", err)
	}
	if got, want := exact.Format(time.RFC3339), "2026-09-29T14:30:00-07:00"; got != want {
		t.Fatalf("local exact time = %s; want %s", got, want)
	}

	iso, err := parseReminderTimeAt("2026-09-29T14:30:00-04:00", location, now)
	if err != nil {
		t.Fatalf("parse ISO 8601 time: %v", err)
	}
	if _, offset := iso.Zone(); offset != -4*60*60 {
		t.Fatalf("ISO 8601 offset = %d seconds; want -14400", offset)
	}
}

func TestParseReminderTimeRejectsInvalidInputs(t *testing.T) {
	for _, value := range []string{"", "0s", "-5m", "next Tuesday"} {
		t.Run(value, func(t *testing.T) {
			_, err := parseReminderTimeAt(value, time.UTC, time.Now())
			if err == nil {
				t.Fatalf("parseReminderTimeAt(%q) succeeded; want error", value)
			}
		})
	}
	if _, err := parseReminderTimeInLocation("10m", "Not/A_Timezone"); err == nil {
		t.Fatal("invalid timezone succeeded; want error")
	}
}

func TestParseRepeatInterval(t *testing.T) {
	for _, test := range []struct {
		value string
		want  time.Duration
	}{
		{"30m", 30 * time.Minute},
		{"1h30m", 90 * time.Minute},
		{"1d", 24 * time.Hour},
		{"2w", 14 * 24 * time.Hour},
	} {
		t.Run(test.value, func(t *testing.T) {
			got, err := parseRepeatInterval(test.value)
			if err != nil {
				t.Fatalf("parseRepeatInterval(%q): %v", test.value, err)
			}
			if got != test.want {
				t.Fatalf("parseRepeatInterval(%q) = %s; want %s", test.value, got, test.want)
			}
		})
	}

	for _, value := range []string{"0s", "-1h", "tomorrow", "999999999999999999999w"} {
		if _, err := parseRepeatInterval(value); err == nil {
			t.Errorf("parseRepeatInterval(%q) succeeded; want error", value)
		}
	}
}

func TestNextReminderTimeSkipsMissedIntervals(t *testing.T) {
	previous := time.Date(2026, time.October, 1, 10, 0, 0, 0, time.UTC)
	now := previous.Add(35 * time.Minute)
	got := nextReminderTime(previous, 10*time.Minute, now)
	if want := previous.Add(40 * time.Minute); !got.Equal(want) {
		t.Fatalf("nextReminderTime = %s; want %s", got, want)
	}

	now = previous.Add(30 * time.Minute)
	got = nextReminderTime(previous, 10*time.Minute, now)
	if want := previous.Add(40 * time.Minute); !got.Equal(want) {
		t.Fatalf("nextReminderTime at interval boundary = %s; want %s", got, want)
	}

	ancient := time.Date(1, time.January, 1, 0, 0, 0, 0, time.UTC)
	farFuture := time.Date(9999, time.January, 1, 0, 0, 0, 0, time.UTC)
	got = nextReminderTime(ancient, time.Nanosecond, farFuture)
	if !got.After(farFuture) {
		t.Fatalf("nextReminderTime = %s; want a time after %s", got, farFuture)
	}
}

func TestReminderPersistenceIsBackwardCompatibleAndKeepsRepeat(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("SUNFIRE_DATA_DIR", dataDir)

	original := reminders
	defer func() {
		remindersMu.Lock()
		reminders = original
		remindersMu.Unlock()
	}()

	path := getRemindersFile()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`[{"id":"legacy","user_id":"u","channel_id":"c","message":"old","fire_time":"2026-10-01T10:00:00Z"}]`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := loadReminders(); err != nil {
		t.Fatalf("load legacy reminder: %v", err)
	}
	if len(reminders) != 1 || reminders[0].Repeat != "" {
		t.Fatalf("legacy reminder did not load as one-shot: %+v", reminders)
	}

	remindersMu.Lock()
	reminders = []Reminder{{
		ID: "repeat", UserID: "u", ChannelID: "c", Message: "daily",
		FireTime: time.Date(2026, time.October, 2, 10, 0, 0, 0, time.UTC), Repeat: "1d",
	}}
	err := saveReminders()
	remindersMu.Unlock()
	if err != nil {
		t.Fatalf("save recurring reminder: %v", err)
	}
	if err := loadReminders(); err != nil {
		t.Fatalf("reload recurring reminder: %v", err)
	}
	if len(reminders) != 1 || reminders[0].Repeat != "1d" {
		t.Fatalf("repeat interval was not persisted: %+v", reminders)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var persisted []map[string]json.RawMessage
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatalf("persisted reminder JSON is invalid: %v", err)
	}
	if _, exists := persisted[0]["repeat"]; !exists || !strings.Contains(string(data), `"repeat": "1d"`) {
		t.Fatalf("persisted JSON is missing the repeat interval: %s", data)
	}
}

func TestFormatReminderListShowsRepeat(t *testing.T) {
	got := formatReminderList([]Reminder{{
		ID: "id", Message: "check in", FireTime: time.Unix(1_800_000_000, 0), Repeat: "1w",
	}})
	if !strings.Contains(got, "(every 1w)") || !strings.Contains(got, "check in") {
		t.Fatalf("reminder list does not show recurring schedule: %q", got)
	}
}

```

## `./tickets.go`

```go
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

```

## `./dice.go`

```go
// dice.go — the /dice command: simulates a classic dice game where the user
// picks a win chance and we roll 0.00-100.00 millions of times to measure how
// often they'd win (roll < chance = win).
package main

import (
	"fmt"
	"math/rand"
	"strconv"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
)

const (
	diceMinChance = 2.0  // minimum win chance %
	diceMaxChance = 98.0 // maximum win chance %
	diceRollMax   = 100.0
	diceDefaultIter = 10_000_000
)

// parseDiceChance parses and validates the user's win chance (2.00-98.00).
func parseDiceChance(raw string) (float64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, fmt.Errorf("no win chance given")
	}
	chance, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, fmt.Errorf("%q is not a valid number", raw)
	}
	if chance < diceMinChance || chance > diceMaxChance {
		return 0, fmt.Errorf("win chance must be between %.2f and %.2f", diceMinChance, diceMaxChance)
	}
	return chance, nil
}

// rollDice returns a random roll in [0, 100).
func rollDice(rng *rand.Rand) float64 {
	return rng.Float64() * diceRollMax
}

// simulateDice runs iterations rolls and returns how many were wins
// (roll < chance).
func simulateDice(chance float64, iterations int) int {
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	wins := 0
	for i := 0; i < iterations; i++ {
		if rollDice(rng) < chance {
			wins++
		}
	}
	return wins
}

// formatDiceResult renders the simulation summary.
func formatDiceResult(chance float64, iterations, wins int) string {
	pct := float64(wins) * 100 / float64(iterations)
	var b strings.Builder
	b.WriteString(fmt.Sprintf("🎲 **Dice Simulation — %d rolls**\n", iterations))
	b.WriteString(fmt.Sprintf("Win chance: **%.2f%%** · roll 0.00-%.0f · win when roll < %.2f\n\n",
		chance, diceRollMax, chance))
	b.WriteString(fmt.Sprintf("• **Wins:** %d times (%.2f%%)\n", wins, pct))
	b.WriteString(fmt.Sprintf("• **Losses:** %d times (%.2f%%)\n", iterations-wins, 100-pct))
	b.WriteString(fmt.Sprintf("\n🎯 **Winning odds: 1 in %s rolls**", oneInX(wins, iterations)))
	return b.String()
}

// handleDice is the /dice slash-command handler.
func handleDice(s *discordgo.Session, i *discordgo.InteractionCreate) {
	raw := ""
	if opts := i.ApplicationCommandData().Options; len(opts) > 0 {
		raw = opts[0].StringValue()
	}
	chance, err := parseDiceChance(raw)
	if err != nil {
		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "❌ " + err.Error() + "\nUsage: `/dice chance:50` (win chance 2.00-98.00)",
			},
		})
		return
	}
	// Acknowledge immediately — the 10M-roll simulation takes a few seconds,
	// longer than Discord's 3s interaction window. Defer, then follow up.
	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	})
	wins := simulateDice(chance, diceDefaultIter)
	reply := formatDiceResult(chance, diceDefaultIter, wins)
	if len(reply) > 1900 {
		reply = reply[:1900] + "…"
	}
	_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{Content: reply})
}

func oneInX(wins, total int) string {
	if wins <= 0 {
		return "N/A"
	}
	ratio := float64(total) / float64(wins)
	if ratio < 10 {
		return fmt.Sprintf("%.2f", ratio)
	}
	return fmt.Sprintf("%.1f", ratio)
}

```

## `./notifier_test_seams.go`

```go
package main

import (
	"net/http"
	"time"

	"github.com/bwmarrin/discordgo"
)

var (
	notifierHTTPClient *http.Client

	sendNotifierMessage = func(s *discordgo.Session, channelID, content string) (*discordgo.Message, error) {
		return s.ChannelMessageSend(channelID, content)
	}
	sendNotifierEmbed = func(s *discordgo.Session, channelID string, embed *discordgo.MessageEmbed) (*discordgo.Message, error) {
		return s.ChannelMessageSendEmbed(channelID, embed)
	}
	sendNotifierComplex = func(s *discordgo.Session, channelID string, message *discordgo.MessageSend) (*discordgo.Message, error) {
		return s.ChannelMessageSendComplex(channelID, message)
	}
	editNotifierComplex = func(s *discordgo.Session, edit *discordgo.MessageEdit) (*discordgo.Message, error) {
		return s.ChannelMessageEditComplex(edit)
	}
)

func notifierClient(timeout time.Duration) *http.Client {
	if notifierHTTPClient != nil {
		return notifierHTTPClient
	}
	return &http.Client{Timeout: timeout}
}

```

## `./help_test.go`

```go
package main

import (
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"
)

func TestHelpCategoriesCoverRegisteredCommands(t *testing.T) {
	buildCommandSpecs()

	choiceValues := make(map[string]bool)
	choiceLabels := make(map[string]bool)
	for _, choice := range helpCategoryChoices() {
		value, ok := choice.Value.(string)
		if !ok {
			t.Fatalf("help category value has type %T, want string", choice.Value)
		}
		choiceValues[value] = true
		choiceLabels[choice.Name] = true
	}
	if len(choiceValues) != len(helpCategories) {
		t.Fatalf("got %d unique category choices, want %d", len(choiceValues), len(helpCategories))
	}
	if !choiceLabels[helpCategoryLabel("core")] {
		t.Fatal("core category is missing from the command choices")
	}

	for _, spec := range commandSpecs {
		category := helpCommandCategory(spec.Command.Name)
		if !choiceValues[category] {
			t.Errorf("command /%s maps to unselectable category %q", spec.Command.Name, category)
		}
	}
}

func TestHelpCommandEntryIncludesOptionTypesChoicesAndNestedPaths(t *testing.T) {
	minLength := 1
	entry := helpCommandEntry(commandSpec{
		Command: &discordgo.ApplicationCommand{
			Name:        "sample",
			Description: "A sample command.",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionSubCommandGroup,
					Name:        "group",
					Description: "A grouped action.",
					Options: []*discordgo.ApplicationCommandOption{
						{
							Type:        discordgo.ApplicationCommandOptionSubCommand,
							Name:        "run",
							Description: "Run the action.",
							Options: []*discordgo.ApplicationCommandOption{
								{
									Type:        discordgo.ApplicationCommandOptionString,
									Name:        "mode",
									Description: "Execution mode.",
									Required:    true,
									MinLength:   &minLength,
									MaxLength:   20,
									Choices: []*discordgo.ApplicationCommandOptionChoice{
										{Name: "Fast", Value: "fast"},
										{Name: "Safe", Value: "safe"},
									},
								},
							},
						},
					},
				},
			},
		},
	})

	for _, want := range []string{
		"/sample group",
		"/sample group run",
		"`/sample group run mode` (string, required)",
		"Choices: Fast (fast), Safe (safe)",
		"Length: 1 to 20 characters",
	} {
		if !strings.Contains(entry, want) {
			t.Errorf("help entry %q does not contain %q", entry, want)
		}
	}
}

func TestHelpSearchAndCategoryFiltering(t *testing.T) {
	buildCommandSpecs()

	entries := helpEntries("", "openrouter")
	if len(entries) != 1 || !strings.Contains(entries[0], "/openrouter") {
		t.Fatalf("help search returned %d entries, want only /openrouter", len(entries))
	}
	entries = helpEntries("moderation", "setup")
	if len(entries) != 1 || !strings.Contains(entries[0], "/setup") {
		t.Fatalf("moderation search returned %d entries, want only /setup", len(entries))
	}
	if validHelpSearch("bad query") || validHelpSearch("name/with/slash") || validHelpSearch(strings.Repeat("x", 33)) {
		t.Fatal("expected malformed or overlong search terms to be rejected")
	}
}

func TestHelpContentPagesStayWithinDiscordLimit(t *testing.T) {
	originalSpecs := commandSpecs
	defer func() { commandSpecs = originalSpecs }()

	commandSpecs = make([]commandSpec, 20)
	for n := range commandSpecs {
		commandSpecs[n] = commandSpec{
			Command: &discordgo.ApplicationCommand{
				Name:        "sample-" + strings.Repeat("x", n%10+1),
				Description: strings.Repeat("description ", 35),
				Options: []*discordgo.ApplicationCommandOption{
					{
						Type:        discordgo.ApplicationCommandOptionString,
						Name:        "parameter",
						Description: strings.Repeat("parameter details ", 25),
					},
				},
			},
		}
	}

	pages := helpContentPages("core", "")
	if len(pages) < 2 {
		t.Fatalf("got %d help pages, want pagination for large category", len(pages))
	}
	for page, content := range pages {
		if length := len([]rune(content)); length > 2000 {
			t.Errorf("help page %d has %d characters, exceeding Discord's 2000-character limit", page+1, length)
		}
	}

	customID := helpPageCustomID("core", 2, "sample")
	parts := strings.Split(customID, ":")
	if len(parts) != 5 || parts[2] != "core" || parts[3] != "2" || parts[4] != "sample" {
		t.Fatalf("help page custom ID %q cannot restore its category, page, and search", customID)
	}
	if len(customID) > 100 {
		t.Fatalf("help page custom ID has %d characters, exceeding Discord's limit", len(customID))
	}
}

func TestHelpOverviewListsAvailableCategories(t *testing.T) {
	buildCommandSpecs()

	pages := helpContentPages("", "")
	if len(pages) != 1 {
		t.Fatalf("overview returned %d pages, want 1", len(pages))
	}
	for _, want := range []string{"Core (System, Moderation, Media)", "LLM Providers", "/help search:"} {
		if !strings.Contains(pages[0], want) {
			t.Errorf("help overview %q does not contain %q", pages[0], want)
		}
	}
}

```

## `./hermes_exec_test.go`

```go
package main

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestHermesChatArgsChooseResumeOrFreshSession(t *testing.T) {
	content := "hello"
	wantResume := []string{"--continue", "--yolo", "chat", "-q", content, "-Q", "--max-turns", "20"}
	if got := hermesChatArgs(content, false); !reflect.DeepEqual(got, wantResume) {
		t.Fatalf("hermesChatArgs(resume) = %#v; want %#v", got, wantResume)
	}

	wantFresh := []string{"--yolo", "chat", "-q", content, "-Q", "--max-turns", "20"}
	if got := hermesChatArgs(content, true); !reflect.DeepEqual(got, wantFresh) {
		t.Fatalf("hermesChatArgs(new session) = %#v; want %#v", got, wantFresh)
	}
}

func TestHermesFailureMessageIncludesNewSessionInstructionForResumePolicyError(t *testing.T) {
	err := errors.New("Hermes execution failed: Error: Resume retains gateway session policy; creation overrides are unsupported on resume. --max-turns differs from this session's launch")
	want := "Failed to contact Hermes: " + err.Error() + "\n- To start a new session enter exactly: \"start-new-session\""
	if got := hermesFailureMessage(err); got != want {
		t.Fatalf("hermesFailureMessage() = %q; want %q", got, want)
	}
}

func TestHermesFailureMessageOmitsNewSessionInstructionForOtherErrors(t *testing.T) {
	err := errors.New("Hermes executable not found")
	want := "Failed to contact Hermes: " + err.Error()
	if got := hermesFailureMessage(err); got != want {
		t.Fatalf("hermesFailureMessage() = %q; want %q", got, want)
	}
}

func TestResolveHermesBinaryUsesConfiguredPath(t *testing.T) {
	configuredPath := filepath.Join(t.TempDir(), "configured-hermes")
	t.Setenv("SUNFIRE_HERMES_BIN", configuredPath)

	got, err := resolveHermesBinary()
	if err != nil {
		t.Fatal(err)
	}
	if got != configuredPath {
		t.Fatalf("resolveHermesBinary() = %q; want %q", got, configuredPath)
	}
}

func TestResolveHermesBinarySearchesPath(t *testing.T) {
	binDir := t.TempDir()
	binaryPath := filepath.Join(binDir, "hermes")
	if err := os.WriteFile(binaryPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SUNFIRE_HERMES_BIN", "")
	t.Setenv("PATH", binDir)

	got, err := resolveHermesBinary()
	if err != nil {
		t.Fatal(err)
	}
	if got != binaryPath {
		t.Fatalf("resolveHermesBinary() = %q; want %q", got, binaryPath)
	}
}

func TestResolveHermesBinarySearchesUserLocalBin(t *testing.T) {
	homeDir := t.TempDir()
	binDir := filepath.Join(homeDir, ".local", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	binaryPath := filepath.Join(binDir, "hermes")
	if err := os.WriteFile(binaryPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SUNFIRE_HERMES_BIN", "")
	t.Setenv("PATH", "")
	t.Setenv("HOME", homeDir)

	got, err := resolveHermesBinary()
	if err != nil {
		t.Fatal(err)
	}
	if got != binaryPath {
		t.Fatalf("resolveHermesBinary() = %q; want %q", got, binaryPath)
	}
}

func TestResolveHermesBinaryReportsMissingExecutable(t *testing.T) {
	t.Setenv("SUNFIRE_HERMES_BIN", "")
	t.Setenv("PATH", "")
	t.Setenv("HOME", t.TempDir())

	if _, err := resolveHermesBinary(); err == nil {
		t.Fatal("resolveHermesBinary() succeeded without an installed Hermes executable")
	}
}

```

## `./bot_event_test.go`

```go
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/bwmarrin/discordgo"
)

func TestMessageEventHandlersPersistCreateEditDeleteAndBulkDelete(t *testing.T) {
	oldBase := messageLogsBaseDir
	messageLogsBaseDir = t.TempDir()
	t.Cleanup(func() { messageLogsBaseDir = oldBase })
	bot := &Bot{}

	create := &discordgo.MessageCreate{Message: &discordgo.Message{
		ID: "message-one", GuildID: SunfireServerID, ChannelID: "channel",
		Author:  &discordgo.User{ID: "user", Username: "tester", Bot: true},
		Content: "original",
	}}
	bot.onMessageCreate(nil, create)

	bot.onMessageUpdate(nil, &discordgo.MessageUpdate{Message: &discordgo.Message{
		ID: "message-one", GuildID: SunfireServerID, ChannelID: "channel", Content: "edited",
	}})
	bot.onMessageDelete(nil, &discordgo.MessageDelete{Message: &discordgo.Message{
		ID: "message-one", GuildID: SunfireServerID, ChannelID: "channel",
	}})

	create.Message.ID = "message-two"
	create.Message.Content = "bulk delete"
	bot.onMessageCreate(nil, create)
	bot.onMessageDeleteBulk(nil, &discordgo.MessageDeleteBulk{
		GuildID: SunfireServerID, ChannelID: "channel", Messages: []string{"message-two"},
	})

	data, err := os.ReadFile(filepath.Join(messageLogsDir(SunfireServerID), "user.json"))
	if err != nil {
		t.Fatal(err)
	}
	var records []MessageRecord
	if err := json.Unmarshal(data, &records); err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 {
		t.Fatalf("records = %d; want 2", len(records))
	}
	if records[0].Content != "edited" || !records[0].Deleted || len(records[0].EditHistory) != 1 {
		t.Fatalf("single-message lifecycle not persisted: %#v", records[0])
	}
	if !records[1].Deleted {
		t.Fatalf("bulk deletion not persisted: %#v", records[1])
	}
}

func TestMessageCreateIgnoresMessagesFromOtherGuilds(t *testing.T) {
	oldBase := messageLogsBaseDir
	messageLogsBaseDir = t.TempDir()
	t.Cleanup(func() { messageLogsBaseDir = oldBase })

	bot := &Bot{}
	bot.onMessageCreate(nil, &discordgo.MessageCreate{Message: &discordgo.Message{
		ID: "other-message", GuildID: "other-guild", ChannelID: "channel",
		Author:  &discordgo.User{ID: "user", Username: "tester", Bot: true},
		Content: "should be ignored",
	}})
	if _, err := os.Stat(filepath.Join(messageLogsBaseDir, "other-guild")); !os.IsNotExist(err) {
		t.Fatalf("other guild message was persisted (stat error %v)", err)
	}
}

func TestMessageCreateQueuesMessagesFromLoggedChannels(t *testing.T) {
	resetMessageLogQueue(t)

	oldChannelsPath := msgLogChannelsFilePath
	oldBase := messageLogsBaseDir
	msgLogChannelsFilePath = filepath.Join(t.TempDir(), "messageLogChannels.json")
	messageLogsBaseDir = t.TempDir()
	t.Cleanup(func() {
		msgLogChannelsFilePath = oldChannelsPath
		messageLogsBaseDir = oldBase
	})
	if err := os.WriteFile(msgLogChannelsFilePath, []byte(`{"1557930395868209162":["1555309257065496678"]}`), 0o600); err != nil {
		t.Fatal(err)
	}

	msgQueueMu.Lock()
	isFlushing = true
	msgQueueMu.Unlock()

	message := &discordgo.MessageCreate{Message: &discordgo.Message{
		ID: "1558198498145607742", GuildID: SunfireServerID, ChannelID: "1555309257065496678",
		Author:  &discordgo.User{ID: "1553668018641436702", Username: "tester", Bot: true},
		Content: "test message",
	}}
	(&Bot{}).onMessageCreate(nil, message)

	msgQueueMu.Lock()
	defer msgQueueMu.Unlock()
	if len(msgQueue) != 1 {
		t.Fatalf("queued messages = %d; want 1", len(msgQueue))
	}
	if msgQueue[0].ID != 1558198498145607742 || msgQueue[0].Content != "test message" {
		t.Fatalf("queued message = %#v; want the message from the logged channel", msgQueue[0])
	}
}

```

## `./emojigg_test.go`

```go
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"
)

func withEmojiAssets(t *testing.T, files map[string][]byte) {
	t.Helper()
	dir := t.TempDir()
	oldDirectory := emojiAssetDirectory
	emojiAssetDirectory = func() string { return dir }
	t.Cleanup(func() { emojiAssetDirectory = oldDirectory })
	for name, contents := range files {
		if err := os.WriteFile(filepath.Join(dir, name), contents, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLoadEmojiGGAssetsAndFindDuplicateNames(t *testing.T) {
	withEmojiAssets(t, map[string][]byte{
		"123-blue-heart.gif": []byte("gif"),
		"456-blue-heart.gif": []byte("gif"),
		"789-party-time.png": []byte("png"),
		"manifest.json":      []byte("{}"),
		"ignored.webp":       []byte("webp"),
	})
	assets, err := loadEmojiGGAssets()
	if err != nil {
		t.Fatal(err)
	}
	if len(assets) != 3 {
		t.Fatalf("assets = %d; want 3 supported image assets", len(assets))
	}
	if assets[0].Name != "blue_heart" || assets[0].ID != "123" {
		t.Fatalf("first sorted asset = %#v; want blue_heart asset 123", assets[0])
	}
	if _, err := findEmojiGGAsset(assets, "blue_heart"); err == nil || !strings.Contains(err.Error(), "use an asset ID") {
		t.Fatalf("duplicate asset name error = %v; want disambiguation hint", err)
	}
	asset, err := findEmojiGGAsset(assets, "456")
	if err != nil {
		t.Fatal(err)
	}
	if asset.FileName != "456-blue-heart.gif" {
		t.Fatalf("asset by ID = %#v; want second blue-heart file", asset)
	}
}

func TestEmojiGGCreateParamsValidatesNameAndFileSize(t *testing.T) {
	withEmojiAssets(t, map[string][]byte{
		"123-party-time.gif": []byte("GIF89a"),
		"456-large.gif":      make([]byte, 200<<10),
	})
	assets, err := loadEmojiGGAssets()
	if err != nil {
		t.Fatal(err)
	}
	asset, err := findEmojiGGAsset(assets, "123")
	if err != nil {
		t.Fatal(err)
	}
	params, err := emojiGGCreateParams(asset, "")
	if err != nil {
		t.Fatal(err)
	}
	if params.Name != "party_time" || !strings.HasPrefix(params.Image, "data:image/gif;base64,") {
		t.Fatalf("create params = %#v; want normalized name and GIF data URI", params)
	}
	if _, err := emojiGGCreateParams(asset, "bad name!"); err == nil {
		t.Fatal("invalid emoji name was accepted")
	}
	if _, err := emojiGGCreateParams(asset, "custom_name"); err != nil {
		t.Fatalf("valid override name rejected: %v", err)
	}
	large, err := findEmojiGGAsset(assets, "456")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := emojiGGCreateParams(large, "large"); err == nil || !strings.Contains(err.Error(), "smaller than") {
		t.Fatalf("oversized asset error = %v; want size limit error", err)
	}
}

func TestEmojiGGPermissionsRequireManageExpressionsOrAdministrator(t *testing.T) {
	for _, tt := range []struct {
		name        string
		permissions int64
		want        bool
	}{
		{name: "no permission"},
		{name: "manage expressions", permissions: int64(discordgo.PermissionManageGuildExpressions), want: true},
		{name: "administrator", permissions: int64(discordgo.PermissionAdministrator), want: true},
		{name: "unrelated permission", permissions: int64(discordgo.PermissionManageMessages)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			interaction := &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{
				Member: &discordgo.Member{Permissions: tt.permissions},
			}}
			if got := canManageGuildEmojis(interaction); got != tt.want {
				t.Fatalf("canManageGuildEmojis() = %t; want %t", got, tt.want)
			}
		})
	}
	if canManageGuildEmojis(nil) {
		t.Fatal("nil interaction unexpectedly has emoji management permission")
	}
}

func TestFormatEmojiGGAssetPage(t *testing.T) {
	assets := make([]emojiGGAsset, emojiGGPageSize+1)
	for index := range assets {
		assets[index] = emojiGGAsset{Name: "emoji", ID: "123"}
	}
	page := formatEmojiGGAssetPage(assets, 2, len(assets))
	if !strings.Contains(page, "page 2/2") || strings.Count(page, "• `emoji`") != 1 {
		t.Fatalf("second page output = %q; want last asset only and page 2/2", page)
	}
	if got := formatEmojiGGAssetPage(assets, 0, len(assets)); !strings.Contains(got, "between 1 and 2") {
		t.Fatalf("invalid page response = %q; want valid page range", got)
	}
}

func TestEmojiGGPageComponentsKeepSearchAndPageState(t *testing.T) {
	query := "happy heart"
	components := emojiGGPageComponents(2, 5, query)
	if len(components) != 1 {
		t.Fatalf("component rows = %d; want one row", len(components))
	}
	buttons := components[0].(discordgo.ActionsRow).Components
	if len(buttons) != 3 {
		t.Fatalf("page buttons = %d; want previous, page indicator, next", len(buttons))
	}
	previous := buttons[0].(discordgo.Button)
	next := buttons[2].(discordgo.Button)
	if previous.Disabled || next.Disabled {
		t.Fatal("middle-page navigation buttons unexpectedly disabled")
	}
	for _, button := range []discordgo.Button{previous, next} {
		parts := strings.Split(button.CustomID, ":")
		if len(parts) != 4 {
			t.Fatalf("button custom ID %q is malformed", button.CustomID)
		}
		if parts[3] != "aGFwcHkgaGVhcnQ" {
			t.Fatalf("button custom ID query = %q; want encoded search query", parts[3])
		}
	}
	firstPage := emojiGGPageComponents(1, 5, "")
	if firstPage[0].(discordgo.ActionsRow).Components[0].(discordgo.Button).Disabled != true {
		t.Fatal("previous button should be disabled on first page")
	}
	lastPage := emojiGGPageComponents(5, 5, "")
	if lastPage[0].(discordgo.ActionsRow).Components[2].(discordgo.Button).Disabled != true {
		t.Fatal("next button should be disabled on last page")
	}
	if got := emojiGGPageComponents(1, 1, ""); got != nil {
		t.Fatalf("single-page controls = %#v; want no navigation buttons", got)
	}
}

```

## `./jokequery.go`

```go
// jokequery.go — answers "/any chuck" and "/any dad" with a random joke from
// the dedicated joke APIs. These short-circuit before the general fan-out.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
)

// randomChuckNorris fetches a random Chuck Norris joke.
func randomChuckNorris() (string, error) {
	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Get("https://api.chucknorris.io/jokes/random")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
	if err != nil {
		return "", err
	}
	var d struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal(body, &d); err != nil {
		return "", err
	}
	if strings.TrimSpace(d.Value) == "" {
		return "", fmt.Errorf("empty Chuck Norris response")
	}
	return d.Value, nil
}

// randomDadJoke fetches a random dad joke. icanhazdadjoke requires the
// Accept: application/json header (otherwise it returns HTML).
func randomDadJoke() (string, error) {
	client := &http.Client{Timeout: 8 * time.Second}
	req, err := http.NewRequest("GET", "https://icanhazdadjoke.com/", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
	if err != nil {
		return "", err
	}
	var d struct {
		Joke string `json:"joke"`
	}
	if err := json.Unmarshal(body, &d); err != nil {
		return "", err
	}
	if strings.TrimSpace(d.Joke) == "" {
		return "", fmt.Errorf("empty dad joke response")
	}
	return d.Joke, nil
}

// handleJokeQuery answers "/any chuck" and "/any dad" with a random joke.
// Returns true if it handled the query.
func handleJokeQuery(s *discordgo.Session, i *discordgo.InteractionCreate, query string) bool {
	q := strings.ToLower(strings.TrimSpace(query))
	var (
		msg string
		err error
	)
	switch q {
	case "chuck", "chuck norris":
		msg, err = randomChuckNorris()
	case "dad", "dad joke", "dadjoke":
		msg, err = randomDadJoke()
	default:
		return false
	}
	if err != nil {
		msg = "❌ Couldn't fetch a joke: " + err.Error()
	} else if q == "dad" || q == "dad joke" || q == "dadjoke" {
		msg = "👨‍👦 **Dad Joke:** " + msg
	} else {
		msg = "🤠 **Chuck Norris:** " + msg
	}
	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Content: msg},
	})
	return true
}

```

## `./openrouter.go`

```go
// openrouter.go — Tier 1 /openrouter subcommands for Sunfire.
//
// Subcommands: models, deals, model, compare, endpoints, providers,
// price, key, gen, usage, ask, help.
//
// Auth: reads cfg.OpenRouter.APIKey from .private/config.json.
//   - Catalog reads (models, deals, model, compare, endpoints, providers,
//     price) work with or without a key.
//   - ask / key / gen / usage require a key.
//
// Docs:   https://openrouter.ai/docs
// Catalog: https://openrouter.ai/models
package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
)

const (
	orBaseURL        = "https://openrouter.ai/api/v1"
	orAppURL         = "https://github.com/daniel-kill/Sunfire"
	orAppTitle       = "Sunfire"
	orHTTPTimeout    = 45 * time.Second
	orMaxModelsShown = 20
	orDefaultAsk     = "openrouter/auto"
	orEmbedColor     = 0x6467F2
)

// ---------- flexible JSON number (OpenRouter returns prices as strings) ----------

type flexFloat float64

func (f *flexFloat) UnmarshalJSON(b []byte) error {
	s := strings.Trim(strings.TrimSpace(string(b)), `"`)
	if s == "" || s == "null" {
		*f = 0
		return nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		*f = 0
		return nil
	}
	*f = flexFloat(v)
	return nil
}

// ---------- API types ----------

type orPricing struct {
	Prompt     flexFloat `json:"prompt"`
	Completion flexFloat `json:"completion"`
	Request    flexFloat `json:"request"`
	Image      flexFloat `json:"image"`
	Discount   flexFloat `json:"discount"`
}

type orArchitecture struct {
	Modality         string   `json:"modality"`
	InputModalities  []string `json:"input_modalities"`
	OutputModalities []string `json:"output_modalities"`
	Tokenizer        string   `json:"tokenizer"`
}

type orTopProvider struct {
	ContextLength       int  `json:"context_length"`
	MaxCompletionTokens int  `json:"max_completion_tokens"`
	IsModerated         bool `json:"is_moderated"`
}

type orModel struct {
	ID                  string         `json:"id"`
	CanonicalSlug       string         `json:"canonical_slug"`
	Name                string         `json:"name"`
	ContextLength       int            `json:"context_length"`
	Pricing             orPricing      `json:"pricing"`
	Architecture        orArchitecture `json:"architecture"`
	SupportedParameters []string       `json:"supported_parameters"`
	Description         string         `json:"description"`
	TopProvider         orTopProvider  `json:"top_provider"`
}

type orModelsResponse struct {
	Data       []orModel `json:"data"`
	TotalCount int       `json:"total_count"`
}

type orEndpoint struct {
	Name                    string    `json:"name"`
	ContextLength           int       `json:"context_length"`
	Pricing                 orPricing `json:"pricing"`
	ProviderName            string    `json:"provider_name"`
	Quantization            string    `json:"quantization"`
	MaxCompletionTokens     int       `json:"max_completion_tokens"`
	Status                  int       `json:"status"`
	UptimeLast30m           flexFloat `json:"uptime_last_30m"`
	SupportsImplicitCaching bool      `json:"supports_implicit_caching"`
	Latency                 flexFloat `json:"latency"`
}

type orProvider struct {
	Name             string `json:"name"`
	Slug             string `json:"slug"`
	PrivacyPolicyURL string `json:"privacy_policy_url"`
	StatusPageURL    string `json:"status_page_url"`
}

type orKeyInfo struct {
	Label                  string   `json:"label"`
	Limit                  *float64 `json:"limit"`
	LimitRemaining         *float64 `json:"limit_remaining"`
	LimitReset             *string  `json:"limit_reset"`
	Usage                  float64  `json:"usage"`
	UsageDaily             float64  `json:"usage_daily"`
	UsageWeekly            float64  `json:"usage_weekly"`
	UsageMonthly           float64  `json:"usage_monthly"`
	ByokUsage              float64  `json:"byok_usage"`
	ByokUsageDaily         float64  `json:"byok_usage_daily"`
	ByokUsageWeekly        float64  `json:"byok_usage_weekly"`
	ByokUsageMonthly       float64  `json:"byok_usage_monthly"`
	IsFreeTier             bool     `json:"is_free_tier"`
	IsManagementKey        bool     `json:"is_management_key"`
	FreeModelDailyRequests struct {
		Used      int `json:"used"`
		Limit     int `json:"limit"`
		Remaining int `json:"remaining"`
	} `json:"free_model_daily_requests"`
}

type orGeneration struct {
	ID                    string    `json:"id"`
	Model                 string    `json:"model"`
	ProviderName          string    `json:"provider_name"`
	Router                string    `json:"router"`
	TokensPrompt          int       `json:"tokens_prompt"`
	TokensCompletion      int       `json:"tokens_completion"`
	NativeTokensReasoning int       `json:"native_tokens_reasoning"`
	TotalCost             flexFloat `json:"total_cost"`
	UpstreamInferenceCost flexFloat `json:"upstream_inference_cost"`
	CacheDiscount         flexFloat `json:"cache_discount"`
	Latency               flexFloat `json:"latency"`
	GenerationTime        flexFloat `json:"generation_time"`
	FinishReason          string    `json:"finish_reason"`
	NativeFinishReason    string    `json:"native_finish_reason"`
	CreatedAt             string    `json:"created_at"`
	Streamed              bool      `json:"streamed"`
	Cancelled             bool      `json:"cancelled"`
	IsByok                bool      `json:"is_byok"`
	NumSearchResults      int       `json:"num_search_results"`
}

// ---------- HTTP plumbing ----------

func orAPIKey() string {
	cfg, err := loadConfig()
	if err != nil || cfg == nil {
		return ""
	}
	return cfg.OpenRouter.APIKey
}

func orMgmtAPIKey() string {
	cfg, err := loadConfig()
	if err != nil || cfg == nil {
		return ""
	}
	return cfg.OpenRouter.APIManagementKey
}

func orRequest(method, path string, query url.Values, body []byte) (*http.Request, error) {
	u := orBaseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, u, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("HTTP-Referer", orAppURL)
	req.Header.Set("X-OpenRouter-Title", orAppTitle)
	if k := orAPIKey(); k != "" {
		req.Header.Set("Authorization", "Bearer "+k)
	}
	return req, nil
}

func orRequestMgmt(method, path string, query url.Values, body []byte) (*http.Request, error) {
	u := orBaseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, u, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("HTTP-Referer", orAppURL)
	req.Header.Set("X-OpenRouter-Title", orAppTitle)
	if k := orMgmtAPIKey(); k != "" {
		req.Header.Set("Authorization", "Bearer "+k)
	}
	return req, nil
}

func orDo(req *http.Request) ([]byte, int, error) {
	client := &http.Client{Timeout: orHTTPTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return b, resp.StatusCode, nil
}

func orAPIError(body []byte, status int) string {
	var e struct {
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &e) == nil && e.Error.Message != "" {
		return fmt.Sprintf("OpenRouter: %s", e.Error.Message)
	}
	return fmt.Sprintf("OpenRouter returned HTTP %d", status)
}

// ---------- Discord reply helpers ----------

func orReply(s *discordgo.Session, i *discordgo.InteractionCreate, content string) {
	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Content: clipMessage(content)},
	})
}

func orDefer(s *discordgo.Session, i *discordgo.InteractionCreate) {
	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	})
}

func orFollowUp(s *discordgo.Session, i *discordgo.InteractionCreate, content string) {
	_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
		Content: clipMessage(content),
	})
}

func orFollowUpEmbed(s *discordgo.Session, i *discordgo.InteractionCreate, embeds ...*discordgo.MessageEmbed) {
	_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{Embeds: embeds})
}

// ---------- option helpers ----------

func orOptStr(sub *discordgo.ApplicationCommandInteractionDataOption, name string) string {
	for _, o := range sub.Options {
		if o.Name == name && o.Type == discordgo.ApplicationCommandOptionString {
			return o.StringValue()
		}
	}
	return ""
}

func orOptInt(sub *discordgo.ApplicationCommandInteractionDataOption, name string) (int64, bool) {
	for _, o := range sub.Options {
		if o.Name == name && o.Type == discordgo.ApplicationCommandOptionInteger {
			return o.IntValue(), true
		}
	}
	return 0, false
}

func orOptNum(sub *discordgo.ApplicationCommandInteractionDataOption, name string) (float64, bool) {
	for _, o := range sub.Options {
		if o.Name == name && o.Type == discordgo.ApplicationCommandOptionNumber {
			return o.FloatValue(), true
		}
	}
	return 0, false
}

// ---------- formatting ----------

func orFmtCtx(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.2fM", float64(n)/1_000_000)
	case n >= 1000:
		return fmt.Sprintf("%dK", n/1000)
	default:
		return strconv.Itoa(n)
	}
}

func orPerM(v flexFloat) string {
	p := float64(v) * 1_000_000
	if p <= 0 {
		return "free"
	}
	if p < 0.01 {
		return fmt.Sprintf("$%.4f", p)
	}
	return fmt.Sprintf("$%.2f", p)
}

func orDiscount(v flexFloat) string {
	d := float64(v)
	if d <= 0 {
		return ""
	}
	if d <= 1 {
		d *= 100
	}
	return fmt.Sprintf("%.0f%% off", d)
}

func orPricePair(p orPricing) string {
	return fmt.Sprintf("%s / %s per M", orPerM(p.Prompt), orPerM(p.Completion))
}

func orModelLine(m orModel) string {
	line := fmt.Sprintf("`%s` — %s · %s ctx · %s",
		m.ID, m.Name, orFmtCtx(m.ContextLength), orPricePair(m.Pricing))
	if d := orDiscount(m.Pricing.Discount); d != "" {
		line += " · " + d
	}
	return line
}

// ---------- data fetchers ----------

func orFetchModels(query url.Values) ([]orModel, int, error) {
	req, err := orRequest(http.MethodGet, "/models", query, nil)
	if err != nil {
		return nil, 0, err
	}
	body, status, err := orDo(req)
	if err != nil {
		return nil, 0, err
	}
	if status != http.StatusOK {
		return nil, 0, fmt.Errorf("%s", orAPIError(body, status))
	}
	var resp orModelsResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, 0, err
	}
	return resp.Data, resp.TotalCount, nil
}

type orVideoModel struct {
	ID                    string   `json:"id"`
	Name                  string   `json:"name"`
	SupportedDurations    []int    `json:"supported_durations"`
	SupportedResolutions  []string `json:"supported_resolutions"`
	SupportedAspectRatios []string `json:"supported_aspect_ratios"`
}

func orFetchVideoModels() ([]orVideoModel, error) {
	req, err := orRequest(http.MethodGet, "/videos/models", nil, nil)
	if err != nil {
		return nil, err
	}
	body, status, err := orDo(req)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("%s", orAPIError(body, status))
	}
	var resp struct {
		Data []orVideoModel `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("parsing video model catalog: %w", err)
	}
	return resp.Data, nil
}

func orFetchModel(slug string) (*orModel, error) {
	slug = strings.TrimPrefix(strings.TrimSpace(slug), "/")
	if slug == "" {
		return nil, fmt.Errorf("no model id given")
	}
	req, err := orRequest(http.MethodGet, "/models/"+slug, nil, nil)
	if err != nil {
		return nil, err
	}
	body, status, err := orDo(req)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("%s", orAPIError(body, status))
	}
	var resp struct {
		Data orModel `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	if resp.Data.ID == "" {
		return nil, fmt.Errorf("model `%s` not found", slug)
	}
	return &resp.Data, nil
}

func orFetchKey() (*orKeyInfo, error) {
	req, err := orRequest(http.MethodGet, "/key", nil, nil)
	if err != nil {
		return nil, err
	}
	body, status, err := orDo(req)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("%s", orAPIError(body, status))
	}
	var resp struct {
		Data orKeyInfo `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	return &resp.Data, nil
}

// ---------- embedding types ----------

type orEmbeddingData struct {
	Object    string    `json:"object"`
	Embedding []float64 `json:"embedding"`
	Index     int       `json:"index"`
}

type orEmbeddingResponse struct {
	Data  []orEmbeddingData `json:"data"`
	Model string            `json:"model"`
	Usage struct {
		PromptTokens int `json:"prompt_tokens"`
		TotalTokens  int `json:"total_tokens"`
	} `json:"usage"`
}

// ---------- rerank types ----------

type orRerankResult struct {
	Document struct {
		Text string `json:"text"`
	} `json:"document"`
	Index          int     `json:"index"`
	RelevanceScore float64 `json:"relevance_score"`
}

type orRerankResponse struct {
	ID      string           `json:"id"`
	Model   string           `json:"model"`
	Results []orRerankResult `json:"results"`
	Usage   struct {
		SearchUnits int `json:"search_units"`
		TotalTokens int `json:"total_tokens"`
	} `json:"usage"`
}

// ---------- TTS / STT types ----------

type orTTSResponse struct {
	AudioData string `json:"audio_data"`
	MediaType string `json:"media_type"`
	Usage     struct {
		Cost       int `json:"cost"`
		InputChars int `json:"input_chars"`
		Seconds    int `json:"seconds"`
	} `json:"usage"`
}

type orSTTResponse struct {
	Text  string `json:"text"`
	Usage struct {
		Cost        float64 `json:"cost"`
		InputTokens int     `json:"input_tokens"`
		Seconds     float64 `json:"seconds"`
	} `json:"usage"`
}

// ---------- embedding / rerank / TTS / STT fetchers ----------

func orFetchEmbeddings(model string, input interface{}, dimensions int) (*orEmbeddingResponse, error) {
	payload := map[string]any{
		"model": model,
		"input": input,
	}
	if dimensions > 0 {
		payload["dimensions"] = dimensions
	}
	bodyBytes, _ := json.Marshal(payload)
	req, err := orRequest(http.MethodPost, "/embeddings", nil, bodyBytes)
	if err != nil {
		return nil, err
	}
	body, status, err := orDo(req)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("%s", orAPIError(body, status))
	}
	var resp orEmbeddingResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func orFetchRerank(model, query string, documents []string, topN int) (*orRerankResponse, error) {
	payload := map[string]any{
		"model":     model,
		"query":     query,
		"documents": documents,
		"top_n":     topN,
	}
	bodyBytes, _ := json.Marshal(payload)
	req, err := orRequest(http.MethodPost, "/rerank", nil, bodyBytes)
	if err != nil {
		return nil, err
	}
	body, status, err := orDo(req)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("%s", orAPIError(body, status))
	}
	var resp orRerankResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

type orRawAudioResponse struct {
	Data        []byte
	ContentType string
}

func orFetchTTS(model, input, voice, format string, speed float64) (*orRawAudioResponse, error) {
	if format == "" {
		format = "mp3"
	}
	payload := map[string]any{
		"model":           model,
		"input":           input,
		"response_format": format,
	}
	if voice != "" {
		payload["voice"] = voice
	}
	if speed > 0 {
		payload["speed"] = speed
	}
	bodyBytes, _ := json.Marshal(payload)
	req, err := orRequest(http.MethodPost, "/audio/speech", nil, bodyBytes)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: orHTTPTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s", orAPIError(body, resp.StatusCode))
	}

	ct := resp.Header.Get("Content-Type")
	return &orRawAudioResponse{
		Data:        body,
		ContentType: ct,
	}, nil
}

func orFetchSTT(model string, audioData string, audioFormat string, language string) (*orSTTResponse, error) {
	if audioFormat == "" {
		audioFormat = "mp3"
	}
	payload := map[string]any{
		"model": model,
		"input_audio": map[string]string{
			"data":   audioData,
			"format": audioFormat,
		},
	}
	if language != "" {
		payload["language"] = language
	}
	bodyBytes, _ := json.Marshal(payload)
	req, err := orRequest(http.MethodPost, "/audio/transcriptions", nil, bodyBytes)
	if err != nil {
		return nil, err
	}
	body, status, err := orDo(req)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("%s", orAPIError(body, status))
	}
	var resp orSTTResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// ---------- benchmark types ----------

type orBenchmarkEntry struct {
	Source            string    `json:"source"`
	ModelPermaslug    string    `json:"model_permaslug"`
	DisplayName       string    `json:"display_name"`
	IntelligenceIndex *float64  `json:"intelligence_index"`
	CodingIndex       *float64  `json:"coding_index"`
	AgenticIndex      *float64  `json:"agentic_index"`
	Pricing           orPricing `json:"pricing"`
}

type orBenchmarksResponse struct {
	Data []orBenchmarkEntry `json:"data"`
}

func orFetchBenchmarks() ([]orBenchmarkEntry, error) {
	req, err := orRequest(http.MethodGet, "/benchmarks", nil, nil)
	if err != nil {
		return nil, err
	}
	body, status, err := orDo(req)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("%s", orAPIError(body, status))
	}
	var resp orBenchmarksResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	return resp.Data, nil
}

// ---------- Tier 3 types (management key endpoints) ----------

type orCreditsInfo struct {
	TotalCredits float64 `json:"total_credits"`
	TotalUsage   float64 `json:"total_usage"`
}

type orAPIKeyItem struct {
	Label          string  `json:"label"`
	Name           string  `json:"name"`
	Hash           string  `json:"hash"`
	Disabled       bool    `json:"disabled"`
	Limit          float64 `json:"limit"`
	LimitRemaining float64 `json:"limit_remaining"`
	LimitReset     string  `json:"limit_reset"`
	Usage          float64 `json:"usage"`
	UsageDaily     float64 `json:"usage_daily"`
	UsageMonthly   float64 `json:"usage_monthly"`
	UsageWeekly    float64 `json:"usage_weekly"`
	ByokUsage      float64 `json:"byok_usage"`
	CreatedAt      string  `json:"created_at"`
	ExpiresAt      string  `json:"expires_at,omitempty"`
	WorkspaceID    string  `json:"workspace_id"`
}

type orGuardrailItem struct {
	ID                   string   `json:"id"`
	Name                 string   `json:"name"`
	Description          string   `json:"description"`
	LimitUsd             float64  `json:"limit_usd"`
	ResetInterval        string   `json:"reset_interval"`
	AllowedProviders     []string `json:"allowed_providers"`
	AllowedModels        []string `json:"allowed_models"`
	EnforceZdr           bool     `json:"enforce_zdr"`
	IncludeByokInBudgets bool     `json:"include_byok_in_budgets"`
	WorkspaceID          string   `json:"workspace_id"`
	CreatedAt            string   `json:"created_at"`
	UpdatedAt            string   `json:"updated_at"`
}

type orBatchItem struct {
	ID            string `json:"id"`
	Object        string `json:"object"`
	Endpoint      string `json:"endpoint"`
	Model         string `json:"model"`
	Status        string `json:"status"`
	CreatedAt     int64  `json:"created_at"`
	RequestCounts struct {
		Total     int `json:"total"`
		Completed int `json:"completed"`
		Failed    int `json:"failed"`
	} `json:"request_counts"`
}

// ---------- Tier 3 fetchers (management key) ----------

func orFetchCredits() (*orCreditsInfo, error) {
	req, err := orRequestMgmt(http.MethodGet, "/credits", nil, nil)
	if err != nil {
		return nil, err
	}
	body, status, err := orDo(req)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("%s", orAPIError(body, status))
	}
	var wrap struct {
		Data orCreditsInfo `json:"data"`
	}
	if json.Unmarshal(body, &wrap) != nil {
		return nil, fmt.Errorf("could not parse credits response")
	}
	return &wrap.Data, nil
}

func orFetchKeys() ([]orAPIKeyItem, error) {
	req, err := orRequestMgmt(http.MethodGet, "/keys", nil, nil)
	if err != nil {
		return nil, err
	}
	body, status, err := orDo(req)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("%s", orAPIError(body, status))
	}
	var wrap struct {
		Data []orAPIKeyItem `json:"data"`
	}
	if json.Unmarshal(body, &wrap) != nil {
		return nil, fmt.Errorf("could not parse keys response")
	}
	return wrap.Data, nil
}

func orFetchGuardrails() ([]orGuardrailItem, error) {
	req, err := orRequestMgmt(http.MethodGet, "/guardrails?limit=50", nil, nil)
	if err != nil {
		return nil, err
	}
	body, status, err := orDo(req)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("%s", orAPIError(body, status))
	}
	var wrap struct {
		Data []orGuardrailItem `json:"data"`
	}
	if json.Unmarshal(body, &wrap) != nil {
		return nil, fmt.Errorf("could not parse guardrails response")
	}
	return wrap.Data, nil
}

type orAnalyticsQuery struct {
	Dimensions  []string `json:"dimensions"`
	Metrics     []string `json:"metrics"`
	Granularity string   `json:"granularity"`
	TimeRange   struct {
		Start string `json:"start"`
		End   string `json:"end"`
	} `json:"time_range"`
	Limit int `json:"limit"`
}

func newORAnalyticsQuery(now time.Time, days int) orAnalyticsQuery {
	end := now.UTC()
	start := end.AddDate(0, 0, -days)
	query := orAnalyticsQuery{
		Dimensions:  []string{"model"},
		Metrics:     []string{"request_count"},
		Granularity: "day",
		Limit:       100,
	}
	query.TimeRange.Start = start.Format(time.RFC3339)
	query.TimeRange.End = end.Format(time.RFC3339)
	return query
}

func orFetchAnalytics(query orAnalyticsQuery) (interface{}, error) {
	bodyBytes, err := json.Marshal(query)
	if err != nil {
		return nil, fmt.Errorf("encoding analytics query: %w", err)
	}
	req, err := orRequestMgmt(http.MethodPost, "/analytics/query", nil, bodyBytes)
	if err != nil {
		return nil, err
	}
	body, status, err := orDo(req)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("%s", orAPIError(body, status))
	}
	var wrap struct {
		Data interface{} `json:"data"`
	}
	if json.Unmarshal(body, &wrap) != nil {
		return nil, fmt.Errorf("could not parse analytics response")
	}
	return wrap.Data, nil
}

func orFetchBatches(statusFilter string) ([]orBatchItem, error) {
	u := "/batches"
	if statusFilter != "" {
		u += "?status=" + statusFilter
	}
	req, err := orRequestMgmt(http.MethodGet, u, nil, nil)
	if err != nil {
		return nil, err
	}
	body, status, err := orDo(req)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("%s", orAPIError(body, status))
	}
	var wrap struct {
		Data []orBatchItem `json:"data"`
	}
	if json.Unmarshal(body, &wrap) != nil {
		return nil, fmt.Errorf("could not parse batches response")
	}
	return wrap.Data, nil
}

// ---------- subcommand handlers ----------

func orHandleModels(s *discordgo.Session, i *discordgo.InteractionCreate, sub *discordgo.ApplicationCommandInteractionDataOption) {
	orDefer(s, i)

	q := url.Values{}
	if v := strings.TrimSpace(orOptStr(sub, "query")); v != "" {
		q.Set("q", v)
	}
	if v := orOptStr(sub, "sort"); v != "" {
		q.Set("sort", v)
	}
	if v := orOptStr(sub, "modality"); v != "" {
		q.Set("output_modalities", v)
	}
	if v, ok := orOptNum(sub, "max_price"); ok {
		q.Set("max_price", strconv.FormatFloat(v, 'f', -1, 64))
	}
	limit := 15
	if v, ok := orOptInt(sub, "limit"); ok && v > 0 {
		limit = int(v)
	}
	if limit > orMaxModelsShown {
		limit = orMaxModelsShown
	}
	q.Set("limit", strconv.Itoa(limit))

	models, total, err := orFetchModels(q)
	if err != nil {
		orFollowUp(s, i, "❌ "+err.Error())
		return
	}
	if len(models) == 0 {
		orFollowUp(s, i, "No models matched that filter.")
		return
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("**OpenRouter models** — showing %d of %d\n\n", len(models), total))
	for _, m := range models {
		b.WriteString("• " + orModelLine(m) + "\n")
	}
	orFollowUp(s, i, b.String())
}

func orHandleDeals(s *discordgo.Session, i *discordgo.InteractionCreate, sub *discordgo.ApplicationCommandInteractionDataOption) {
	orDefer(s, i)

	limit := 15
	if v, ok := orOptInt(sub, "limit"); ok && v > 0 {
		limit = int(v)
	}
	if limit > orMaxModelsShown {
		limit = orMaxModelsShown
	}

	models, _, err := orFetchModels(url.Values{"limit": {"1000"}})
	if err != nil {
		orFollowUp(s, i, "❌ "+err.Error())
		return
	}

	var deals []orModel
	for _, m := range models {
		if float64(m.Pricing.Discount) > 0 {
			deals = append(deals, m)
		}
	}

	header := "**OpenRouter — discounted models**\n\n"
	if len(deals) == 0 {
		// No discount metadata surfaced; fall back to the cheapest text models.
		sort.Slice(models, func(a, b int) bool {
			return float64(models[a].Pricing.Prompt) < float64(models[b].Pricing.Prompt)
		})
		if len(models) > limit {
			models = models[:limit]
		}
		var b strings.Builder
		b.WriteString("No active discounts are published right now. Cheapest models instead:\n\n")
		for _, m := range models {
			b.WriteString("• " + orModelLine(m) + "\n")
		}
		orFollowUp(s, i, b.String())
		return
	}

	sort.Slice(deals, func(a, b int) bool {
		return float64(deals[a].Pricing.Discount) > float64(deals[b].Pricing.Discount)
	})
	if len(deals) > limit {
		deals = deals[:limit]
	}

	var b strings.Builder
	b.WriteString(header)
	for _, m := range deals {
		b.WriteString("• " + orModelLine(m) + "\n")
	}
	b.WriteString("\n_Batch variants (`:batch`) stack an extra discount on top._")
	orFollowUp(s, i, b.String())
}

func orHandleModel(s *discordgo.Session, i *discordgo.InteractionCreate, sub *discordgo.ApplicationCommandInteractionDataOption) {
	orDefer(s, i)

	slug := orOptStr(sub, "slug")
	m, err := orFetchModel(slug)
	if err != nil {
		orFollowUp(s, i, "❌ "+err.Error())
		return
	}

	embed := &discordgo.MessageEmbed{
		Title:       truncateString(m.Name, 250),
		Description: truncateString(m.Description, 1200),
		Color:       orEmbedColor,
		Footer:      &discordgo.MessageEmbedFooter{Text: m.ID},
		Fields: []*discordgo.MessageEmbedField{
			{Name: "Context", Value: orFmtCtx(m.ContextLength), Inline: true},
			{Name: "Modality", Value: orOrDash(m.Architecture.Modality), Inline: true},
			{Name: "Tokenizer", Value: orOrDash(m.Architecture.Tokenizer), Inline: true},
			{Name: "Input", Value: orOrDash(strings.Join(m.Architecture.InputModalities, ", ")), Inline: true},
			{Name: "Output", Value: orOrDash(strings.Join(m.Architecture.OutputModalities, ", ")), Inline: true},
			{Name: "Max output", Value: orFmtCtx(m.TopProvider.MaxCompletionTokens), Inline: true},
			{Name: "Price", Value: orPricePair(m.Pricing), Inline: false},
		},
	}
	if d := orDiscount(m.Pricing.Discount); d != "" {
		embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{Name: "Discount", Value: d, Inline: true})
	}
	if len(m.SupportedParameters) > 0 {
		embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
			Name:  fmt.Sprintf("Parameters (%d)", len(m.SupportedParameters)),
			Value: truncateString(strings.Join(m.SupportedParameters, ", "), 1000),
		})
	}
	orFollowUpEmbed(s, i, embed)
}

func orHandleCompare(s *discordgo.Session, i *discordgo.InteractionCreate, sub *discordgo.ApplicationCommandInteractionDataOption) {
	orDefer(s, i)

	aSlug := orOptStr(sub, "model_a")
	bSlug := orOptStr(sub, "model_b")
	a, err := orFetchModel(aSlug)
	if err != nil {
		orFollowUp(s, i, "❌ Model A: "+err.Error())
		return
	}
	b, err := orFetchModel(bSlug)
	if err != nil {
		orFollowUp(s, i, "❌ Model B: "+err.Error())
		return
	}

	row := func(label, av, bv string) *discordgo.MessageEmbedField {
		return &discordgo.MessageEmbedField{
			Name:  label,
			Value: fmt.Sprintf("**A:** %s\n**B:** %s", av, bv),
		}
	}

	embed := &discordgo.MessageEmbed{
		Title: "Model comparison",
		Color: orEmbedColor,
		Fields: []*discordgo.MessageEmbedField{
			{Name: "A", Value: "`" + a.ID + "`", Inline: true},
			{Name: "B", Value: "`" + b.ID + "`", Inline: true},
			{Name: "\u200b", Value: "\u200b", Inline: true},
			row("Context", orFmtCtx(a.ContextLength), orFmtCtx(b.ContextLength)),
			row("Price (in / out per M)", orPerM(a.Pricing.Prompt)+" / "+orPerM(a.Pricing.Completion), orPerM(b.Pricing.Prompt)+" / "+orPerM(b.Pricing.Completion)),
			row("Modality", orOrDash(a.Architecture.Modality), orOrDash(b.Architecture.Modality)),
			row("Max output", orFmtCtx(a.TopProvider.MaxCompletionTokens), orFmtCtx(b.TopProvider.MaxCompletionTokens)),
			row("Params", strconv.Itoa(len(a.SupportedParameters)), strconv.Itoa(len(b.SupportedParameters))),
		},
	}
	orFollowUpEmbed(s, i, embed)
}

func orHandleEndpoints(s *discordgo.Session, i *discordgo.InteractionCreate, sub *discordgo.ApplicationCommandInteractionDataOption) {
	orDefer(s, i)

	slug := strings.TrimPrefix(strings.TrimSpace(orOptStr(sub, "slug")), "/")
	if slug == "" {
		orFollowUp(s, i, "❌ Give a model id, e.g. `openai/gpt-5.6-sol`.")
		return
	}
	req, err := orRequest(http.MethodGet, "/models/"+slug+"/endpoints", nil, nil)
	if err != nil {
		orFollowUp(s, i, "❌ "+err.Error())
		return
	}
	body, status, err := orDo(req)
	if err != nil {
		orFollowUp(s, i, "❌ "+err.Error())
		return
	}
	if status != http.StatusOK {
		orFollowUp(s, i, "❌ "+orAPIError(body, status))
		return
	}

	var wrap struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &wrap); err != nil {
		orFollowUp(s, i, "❌ Could not parse endpoints response.")
		return
	}
	var eps []orEndpoint
	var obj struct {
		Endpoints []orEndpoint `json:"endpoints"`
	}
	if json.Unmarshal(wrap.Data, &obj) == nil && len(obj.Endpoints) > 0 {
		eps = obj.Endpoints
	} else {
		_ = json.Unmarshal(wrap.Data, &eps)
	}
	if len(eps) == 0 {
		orFollowUp(s, i, "No endpoints published for `"+slug+"`.")
		return
	}

	sort.Slice(eps, func(a, b int) bool {
		return float64(eps[a].Pricing.Prompt) < float64(eps[b].Pricing.Prompt)
	})

	var b strings.Builder
	b.WriteString(fmt.Sprintf("**Endpoints for `%s`** (%d providers, cheapest first)\n\n", slug, len(eps)))
	for _, e := range eps {
		up := ""
		if float64(e.UptimeLast30m) > 0 {
			up = fmt.Sprintf(" · %.1f%% up", float64(e.UptimeLast30m))
		}
		quant := ""
		if e.Quantization != "" {
			quant = " · " + e.Quantization
		}
		b.WriteString(fmt.Sprintf("• **%s** — %s · %s ctx%s%s\n",
			orOrDash(e.ProviderName), orPricePair(e.Pricing), orFmtCtx(e.ContextLength), quant, up))
	}
	orFollowUp(s, i, b.String())
}
func orHandleProviders(s *discordgo.Session, i *discordgo.InteractionCreate, _ *discordgo.ApplicationCommandInteractionDataOption) {
	orShowProvidersPage(s, i, 0)
}
func orHandlePrice(s *discordgo.Session, i *discordgo.InteractionCreate, sub *discordgo.ApplicationCommandInteractionDataOption) {
	orDefer(s, i)

	m, err := orFetchModel(orOptStr(sub, "slug"))
	if err != nil {
		orFollowUp(s, i, "❌ "+err.Error())
		return
	}

	inTokens := int64(1000)
	outTokens := int64(500)
	if v, ok := orOptInt(sub, "in_tokens"); ok && v > 0 {
		inTokens = v
	}
	if v, ok := orOptInt(sub, "out_tokens"); ok && v > 0 {
		outTokens = v
	}

	inCost := float64(m.Pricing.Prompt) * float64(inTokens)
	outCost := float64(m.Pricing.Completion) * float64(outTokens)
	total := inCost + outCost

	var b strings.Builder
	b.WriteString(fmt.Sprintf("**`%s`** — %s\n\n", m.ID, m.Name))
	b.WriteString(fmt.Sprintf("• Input:  **%s** per M\n", orPerM(m.Pricing.Prompt)))
	b.WriteString(fmt.Sprintf("• Output: **%s** per M\n", orPerM(m.Pricing.Completion)))
	if d := orDiscount(m.Pricing.Discount); d != "" {
		b.WriteString(fmt.Sprintf("• Discount: **%s**\n", d))
	}
	b.WriteString(fmt.Sprintf("\n**Estimate** for %s in + %s out tokens: **$%.6f**",
		orThousands(inTokens), orThousands(outTokens), total))
	if total < 0.01 && total > 0 {
		b.WriteString(fmt.Sprintf("  _(%.4f cents)_", total*100))
	}
	orFollowUp(s, i, b.String())
}

func orHandleKey(s *discordgo.Session, i *discordgo.InteractionCreate, _ *discordgo.ApplicationCommandInteractionDataOption) {
	orDefer(s, i)

	if orAPIKey() == "" {
		orFollowUp(s, i, "❌ No OpenRouter API key configured (`openrouter.apiKey` in `.private/config.json`).")
		return
	}
	k, err := orFetchKey()
	if err != nil {
		orFollowUp(s, i, "❌ "+err.Error())
		return
	}

	remaining := "unlimited"
	if k.LimitRemaining != nil {
		remaining = fmt.Sprintf("$%.4f", *k.LimitRemaining)
	}
	limit := "none"
	if k.Limit != nil {
		limit = fmt.Sprintf("$%.2f", *k.Limit)
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("**OpenRouter key** — %s\n\n", orOrDash(k.Label)))
	b.WriteString(fmt.Sprintf("• Spend today:   **$%.4f**\n", k.UsageDaily))
	b.WriteString(fmt.Sprintf("• This week:     **$%.4f**\n", k.UsageWeekly))
	b.WriteString(fmt.Sprintf("• This month:    **$%.4f**\n", k.UsageMonthly))
	b.WriteString(fmt.Sprintf("• All time:      **$%.4f**\n", k.Usage))
	if k.ByokUsage > 0 {
		b.WriteString(fmt.Sprintf("• BYOK all time: **$%.4f**\n", k.ByokUsage))
	}
	b.WriteString(fmt.Sprintf("\n• Limit: **%s** · Remaining: **%s**\n", limit, remaining))
	if k.FreeModelDailyRequests.Limit > 0 {
		b.WriteString(fmt.Sprintf("• Free-model requests left today: **%d / %d**\n",
			k.FreeModelDailyRequests.Remaining, k.FreeModelDailyRequests.Limit))
	}
	b.WriteString(fmt.Sprintf("• Free tier: **%v** · Management key: **%v**", k.IsFreeTier, k.IsManagementKey))
	orFollowUp(s, i, b.String())
}

func orHandleCredits(s *discordgo.Session, i *discordgo.InteractionCreate, sub *discordgo.ApplicationCommandInteractionDataOption) {
	orDefer(s, i)

	if orAPIKey() == "" {
		orFollowUp(s, i, "\u274c No OpenRouter API key configured.")
		return
	}

	// Always show key info (regular endpoint)
	k, err := orFetchKey()
	if err != nil {
		orFollowUp(s, i, "\u274c "+err.Error())
		return
	}

	remaining := "unlimited"
	if k.LimitRemaining != nil {
		remaining = fmt.Sprintf("$%.4f", *k.LimitRemaining)
	}
	limit := "none"
	if k.Limit != nil {
		limit = fmt.Sprintf("$%.2f", *k.Limit)
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("**OpenRouter \u2014 Credits & Usage**\n\n"))
	b.WriteString(fmt.Sprintf("**Key** \u2014 %s\n", orOrDash(k.Label)))
	b.WriteString(fmt.Sprintf("\u2022 Spend today:   **$%.4f**\n", k.UsageDaily))
	b.WriteString(fmt.Sprintf("\u2022 This week:     **$%.4f**\n", k.UsageWeekly))
	b.WriteString(fmt.Sprintf("\u2022 This month:    **$%.4f**\n", k.UsageMonthly))
	b.WriteString(fmt.Sprintf("\u2022 All time:      **$%.4f**\n", k.Usage))
	if k.ByokUsage > 0 {
		b.WriteString(fmt.Sprintf("\u2022 BYOK all time: **$%.4f**\n", k.ByokUsage))
	}
	b.WriteString(fmt.Sprintf("\n\u2022 Limit: **%s** \u00b7 Remaining: **%s**\n", limit, remaining))
	if k.FreeModelDailyRequests.Limit > 0 {
		b.WriteString(fmt.Sprintf("\u2022 Free-model requests left today: **%d / %d**\n",
			k.FreeModelDailyRequests.Remaining, k.FreeModelDailyRequests.Limit))
	}
	b.WriteString(fmt.Sprintf("\u2022 Free tier: **%v** \u00b7 Management key: **%v**\n", k.IsFreeTier, k.IsManagementKey))

	// Also try to fetch credits (management key endpoint)
	if orMgmtAPIKey() != "" {
		credits, credErr := orFetchCredits()
		if credErr == nil {
			total := float64(credits.TotalCredits)
			used := float64(credits.TotalUsage)
			pct := 0.0
			if total > 0 {
				pct = (used / total) * 100
			}
			b.WriteString(fmt.Sprintf("\n**Account credits**\n"))
			b.WriteString(fmt.Sprintf("\u2022 Purchased: **$%.2f**\n", total))
			b.WriteString(fmt.Sprintf("\u2022 Used:      **$%.2f** (%.1f%%)\n", used, pct))
			b.WriteString(fmt.Sprintf("\u2022 Remaining: **$%.2f**\n", total-used))
		}
	}

	orFollowUp(s, i, b.String())
}

func orHandleGen(s *discordgo.Session, i *discordgo.InteractionCreate, sub *discordgo.ApplicationCommandInteractionDataOption) {
	orDefer(s, i)

	if orAPIKey() == "" {
		orFollowUp(s, i, "❌ No OpenRouter API key configured.")
		return
	}
	id := strings.TrimSpace(orOptStr(sub, "id"))
	if id == "" {
		orFollowUp(s, i, "❌ Give a generation id, e.g. `gen-1234567890`.")
		return
	}

	req, err := orRequest(http.MethodGet, "/generation", url.Values{"id": {id}}, nil)
	if err != nil {
		orFollowUp(s, i, "❌ "+err.Error())
		return
	}
	body, status, err := orDo(req)
	if err != nil {
		orFollowUp(s, i, "❌ "+err.Error())
		return
	}
	if status != http.StatusOK {
		orFollowUp(s, i, "❌ "+orAPIError(body, status))
		return
	}
	var resp struct {
		Data orGeneration `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		orFollowUp(s, i, "❌ Could not parse generation response.")
		return
	}
	g := resp.Data

	var b strings.Builder
	b.WriteString(fmt.Sprintf("**Generation `%s`**\n\n", g.ID))
	b.WriteString(fmt.Sprintf("• Model: **%s** via **%s**\n", orOrDash(g.Model), orOrDash(g.ProviderName)))
	if g.Router != "" {
		b.WriteString(fmt.Sprintf("• Router: `%s`\n", g.Router))
	}
	b.WriteString(fmt.Sprintf("• Tokens: **%d** in / **%d** out", g.TokensPrompt, g.TokensCompletion))
	if g.NativeTokensReasoning > 0 {
		b.WriteString(fmt.Sprintf(" (%d reasoning)", g.NativeTokensReasoning))
	}
	b.WriteString("\n")
	b.WriteString(fmt.Sprintf("• Cost: **$%.6f**", float64(g.TotalCost)))
	if float64(g.UpstreamInferenceCost) > 0 {
		b.WriteString(fmt.Sprintf(" (upstream $%.6f)", float64(g.UpstreamInferenceCost)))
	}
	b.WriteString("\n")
	if float64(g.CacheDiscount) != 0 {
		b.WriteString(fmt.Sprintf("• Cache discount: **$%.6f**\n", float64(g.CacheDiscount)))
	}
	b.WriteString(fmt.Sprintf("• Latency: **%.0f ms** · generation %.0f ms\n", float64(g.Latency), float64(g.GenerationTime)))
	b.WriteString(fmt.Sprintf("• Finish: **%s**", orOrDash(g.FinishReason)))
	if g.NativeFinishReason != "" {
		b.WriteString(fmt.Sprintf(" (native: %s)", g.NativeFinishReason))
	}
	if g.NumSearchResults > 0 {
		b.WriteString(fmt.Sprintf("\n• Web search results: %d", g.NumSearchResults))
	}
	orFollowUp(s, i, b.String())
}

func orHandleHistory(s *discordgo.Session, i *discordgo.InteractionCreate, sub *discordgo.ApplicationCommandInteractionDataOption) {
	orDefer(s, i)
	if orAPIKey() == "" {
		orFollowUp(s, i, "❌ No OpenRouter API key configured.")
		return
	}

	limit := 10
	if v, ok := orOptInt(sub, "limit"); ok && v > 0 && v <= 50 {
		limit = int(v)
	}

	req, err := orRequest(http.MethodGet, "/generations?limit="+strconv.Itoa(limit), nil, nil)
	if err != nil {
		orFollowUp(s, i, "❌ "+err.Error())
		return
	}
	body, status, err := orDo(req)
	if err != nil {
		orFollowUp(s, i, "❌ "+err.Error())
		return
	}
	if status != http.StatusOK {
		orFollowUp(s, i, "❌ "+orAPIError(body, status))
		return
	}

	var wrap struct {
		Data []orGeneration `json:"data"`
	}
	if json.Unmarshal(body, &wrap) != nil || len(wrap.Data) == 0 {
		orFollowUp(s, i, "No generations found.")
		return
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("**OpenRouter — Recent generations** (last %d)\n\n", len(wrap.Data)))
	for _, g := range wrap.Data {
		cost := float64(g.TotalCost)
		b.WriteString(fmt.Sprintf("• `%s` — **%s** · $%.4f · %d+%d tok\n",
			g.ID[:24], orOrDash(g.Model), cost, g.TokensPrompt, g.TokensCompletion))
	}
	b.WriteString("\n_Use `/openrouter gen id:<id>` to inspect one generation._")
	orFollowUp(s, i, b.String())
}

func orHandleUsage(s *discordgo.Session, i *discordgo.InteractionCreate, _ *discordgo.ApplicationCommandInteractionDataOption) {
	orDefer(s, i)

	if orAPIKey() == "" {
		orFollowUp(s, i, "❌ No OpenRouter API key configured.")
		return
	}
	k, err := orFetchKey()
	if err != nil {
		orFollowUp(s, i, "❌ "+err.Error())
		return
	}

	pctLine := ""
	if k.Limit != nil && *k.Limit > 0 {
		pct := (k.Usage / *k.Limit) * 100
		pctLine = fmt.Sprintf("\n\n**%.1f%%** of a **$%.2f** lifetime limit used.", pct, *k.Limit)
	}

	var b strings.Builder
	b.WriteString("**OpenRouter spend dashboard**\n\n")
	b.WriteString(fmt.Sprintf("Today:   **$%.4f**\n", k.UsageDaily))
	b.WriteString(fmt.Sprintf("Week:    **$%.4f**\n", k.UsageWeekly))
	b.WriteString(fmt.Sprintf("Month:   **$%.4f**\n", k.UsageMonthly))
	b.WriteString(fmt.Sprintf("Lifetime: **$%.4f**\n", k.Usage))
	if k.ByokUsageDaily > 0 || k.ByokUsageMonthly > 0 {
		b.WriteString(fmt.Sprintf("\nBYOK — today **$%.4f** · month **$%.4f** · lifetime **$%.4f**\n",
			k.ByokUsageDaily, k.ByokUsageMonthly, k.ByokUsage))
	}
	b.WriteString(pctLine)
	b.WriteString("\n\n_Use `/openrouter gen id:<gen-...>` to audit a single request's real cost._")
	orFollowUp(s, i, b.String())
}

func orHandleAsk(s *discordgo.Session, i *discordgo.InteractionCreate, sub *discordgo.ApplicationCommandInteractionDataOption) {
	orDefer(s, i)

	if orAPIKey() == "" {
		orFollowUp(s, i, "❌ No OpenRouter API key configured (`openrouter.apiKey`).")
		return
	}

	prompt := strings.TrimSpace(orOptStr(sub, "prompt"))
	if prompt == "" {
		orFollowUp(s, i, "❌ Give a prompt.")
		return
	}
	model := strings.TrimSpace(orOptStr(sub, "model"))
	if model == "" {
		model = orDefaultAsk
	}
	maxTokens := 1024
	if v, ok := orOptInt(sub, "max_tokens"); ok && v > 0 {
		maxTokens = int(v)
	}

	payload := map[string]any{
		"model":      model,
		"max_tokens": maxTokens,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
	}
	if t, ok := orOptNum(sub, "temperature"); ok {
		payload["temperature"] = t
	}
	bodyBytes, _ := json.Marshal(payload)

	req, err := orRequest(http.MethodPost, "/chat/completions", nil, bodyBytes)
	if err != nil {
		orFollowUp(s, i, "❌ "+err.Error())
		return
	}
	body, status, err := orDo(req)
	if err != nil {
		orFollowUp(s, i, "❌ "+err.Error())
		return
	}
	if status != http.StatusOK {
		orFollowUp(s, i, "❌ "+orAPIError(body, status))
		return
	}

	var resp struct {
		ID      string `json:"id"`
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int       `json:"prompt_tokens"`
			CompletionTokens int       `json:"completion_tokens"`
			TotalTokens      int       `json:"total_tokens"`
			Cost             flexFloat `json:"cost"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		orFollowUp(s, i, "❌ Could not parse completion response.")
		return
	}
	if len(resp.Choices) == 0 {
		orFollowUp(s, i, "OpenRouter returned no choices.")
		return
	}

	answer := strings.TrimSpace(resp.Choices[0].Message.Content)
	if answer == "" {
		answer = "_(empty response)_"
	}
	usedModel := resp.Model
	if usedModel == "" {
		usedModel = model
	}

	footer := fmt.Sprintf("%s · %d in / %d out tokens · $%.6f",
		usedModel, resp.Usage.PromptTokens, resp.Usage.CompletionTokens, float64(resp.Usage.Cost))
	if resp.Choices[0].FinishReason != "" && resp.Choices[0].FinishReason != "stop" {
		footer += " · " + resp.Choices[0].FinishReason
	}

	embed := &discordgo.MessageEmbed{
		Description: truncateString(answer, 4000),
		Color:       orEmbedColor,
		Footer:      &discordgo.MessageEmbedFooter{Text: footer},
	}
	if resp.ID != "" {
		embed.Title = "`" + resp.ID + "`"
	}
	orFollowUpEmbed(s, i, embed)
}

// ---------- Tier 2: routing wrappers (free, online, think, route, fallback, fusion, tools) ----------

func orHandleFree(s *discordgo.Session, i *discordgo.InteractionCreate, sub *discordgo.ApplicationCommandInteractionDataOption) {
	// Alias: free is ask with model=openrouter/free
	prompt := strings.TrimSpace(orOptStr(sub, "prompt"))
	if prompt == "" {
		orFollowUp(s, i, "❌ Give a prompt. Usage: `/openrouter free prompt:<text>`")
		return
	}
	if orAPIKey() == "" {
		orFollowUp(s, i, "❌ No OpenRouter API key configured.")
		return
	}
	// Build a minimal ask payload with model=openrouter/free
	payload := map[string]any{
		"model":      "openrouter/free",
		"max_tokens": 1024,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
	}
	if v, ok := orOptInt(sub, "max_tokens"); ok && v > 0 {
		payload["max_tokens"] = v
	}
	bodyBytes, _ := json.Marshal(payload)
	req, err := orRequest(http.MethodPost, "/chat/completions", nil, bodyBytes)
	if err != nil {
		orFollowUp(s, i, "❌ "+err.Error())
		return
	}
	body, status, err := orDo(req)
	if err != nil {
		orFollowUp(s, i, "❌ "+err.Error())
		return
	}
	if status != http.StatusOK {
		orFollowUp(s, i, "❌ "+orAPIError(body, status))
		return
	}

	var resp struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int       `json:"prompt_tokens"`
			CompletionTokens int       `json:"completion_tokens"`
			Cost             flexFloat `json:"cost"`
		} `json:"usage"`
	}
	if json.Unmarshal(body, &resp) != nil || len(resp.Choices) == 0 {
		orFollowUp(s, i, "❌ Could not parse free-model response.")
		return
	}
	answer := strings.TrimSpace(resp.Choices[0].Message.Content)
	if answer == "" {
		answer = "_(empty response)_"
	}
	footer := fmt.Sprintf("%s · %d in / %d out · $%.6f", resp.Model, resp.Usage.PromptTokens, resp.Usage.CompletionTokens, float64(resp.Usage.Cost))
	embed := &discordgo.MessageEmbed{
		Description: truncateString(answer, 4000),
		Color:       orEmbedColor,
		Footer:      &discordgo.MessageEmbedFooter{Text: footer},
	}
	orFollowUpEmbed(s, i, embed)
}

func orHandleOnline(s *discordgo.Session, i *discordgo.InteractionCreate, sub *discordgo.ApplicationCommandInteractionDataOption) {
	orDefer(s, i)
	prompt := strings.TrimSpace(orOptStr(sub, "prompt"))
	if prompt == "" {
		orFollowUp(s, i, "❌ Give a prompt. Usage: `/openrouter online prompt:<text>`")
		return
	}
	if orAPIKey() == "" {
		orFollowUp(s, i, "❌ No OpenRouter API key configured.")
		return
	}
	model := strings.TrimSpace(orOptStr(sub, "model"))
	if model == "" {
		model = "openrouter/auto"
	}
	if !strings.HasSuffix(model, ":online") {
		model += ":online"
	}

	payload := map[string]any{
		"model":      model,
		"max_tokens": 1024,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
	}
	if v, ok := orOptInt(sub, "max_tokens"); ok && v > 0 {
		payload["max_tokens"] = v
	}
	bodyBytes, _ := json.Marshal(payload)
	req, err := orRequest(http.MethodPost, "/chat/completions", nil, bodyBytes)
	if err != nil {
		orFollowUp(s, i, "❌ "+err.Error())
		return
	}
	body, status, err := orDo(req)
	if err != nil {
		orFollowUp(s, i, "❌ "+err.Error())
		return
	}
	if status != http.StatusOK {
		orFollowUp(s, i, "❌ "+orAPIError(body, status))
		return
	}

	var resp struct {
		ID      string `json:"id"`
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int       `json:"prompt_tokens"`
			CompletionTokens int       `json:"completion_tokens"`
			Cost             flexFloat `json:"cost"`
		} `json:"usage"`
	}
	if json.Unmarshal(body, &resp) != nil || len(resp.Choices) == 0 {
		orFollowUp(s, i, "❌ Could not parse online response.")
		return
	}
	answer := strings.TrimSpace(resp.Choices[0].Message.Content)
	if answer == "" {
		answer = "_(empty response)_"
	}
	footer := fmt.Sprintf("%s · %d in / %d out · $%.6f", resp.Model, resp.Usage.PromptTokens, resp.Usage.CompletionTokens, float64(resp.Usage.Cost))
	if resp.ID != "" {
		footer += " · `" + resp.ID + "`"
	}
	embed := &discordgo.MessageEmbed{
		Description: truncateString(answer, 4000),
		Color:       orEmbedColor,
		Footer:      &discordgo.MessageEmbedFooter{Text: footer},
	}
	orFollowUpEmbed(s, i, embed)
}

func orHandleThink(s *discordgo.Session, i *discordgo.InteractionCreate, sub *discordgo.ApplicationCommandInteractionDataOption) {
	orDefer(s, i)
	prompt := strings.TrimSpace(orOptStr(sub, "prompt"))
	if prompt == "" {
		orFollowUp(s, i, "❌ Give a prompt. Usage: `/openrouter think prompt:<text>`")
		return
	}
	if orAPIKey() == "" {
		orFollowUp(s, i, "❌ No OpenRouter API key configured.")
		return
	}
	model := strings.TrimSpace(orOptStr(sub, "model"))
	if model == "" {
		model = "openrouter/auto"
	}

	payload := map[string]any{
		"model":      model,
		"max_tokens": 2048,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
	}
	if v, ok := orOptInt(sub, "max_tokens"); ok && v > 0 {
		payload["max_tokens"] = v
	}
	reasoningMax := 2048
	if v, ok := orOptInt(sub, "reasoning_tokens"); ok && v > 0 {
		reasoningMax = int(v)
	}
	payload["reasoning"] = map[string]any{"max_tokens": reasoningMax}

	bodyBytes, _ := json.Marshal(payload)
	req, err := orRequest(http.MethodPost, "/chat/completions", nil, bodyBytes)
	if err != nil {
		orFollowUp(s, i, "❌ "+err.Error())
		return
	}
	body, status, err := orDo(req)
	if err != nil {
		orFollowUp(s, i, "❌ "+err.Error())
		return
	}
	if status != http.StatusOK {
		orFollowUp(s, i, "❌ "+orAPIError(body, status))
		return
	}

	var resp struct {
		ID      string `json:"id"`
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content   string `json:"content"`
				Reasoning string `json:"reasoning,omitempty"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int       `json:"prompt_tokens"`
			CompletionTokens int       `json:"completion_tokens"`
			Cost             flexFloat `json:"cost"`
		} `json:"usage"`
	}
	if json.Unmarshal(body, &resp) != nil || len(resp.Choices) == 0 {
		orFollowUp(s, i, "❌ Could not parse thinking response.")
		return
	}
	answer := strings.TrimSpace(resp.Choices[0].Message.Content)
	if answer == "" {
		answer = "_(empty response)_"
	}
	footer := fmt.Sprintf("%s · %d in / %d out · $%.6f", resp.Model, resp.Usage.PromptTokens, resp.Usage.CompletionTokens, float64(resp.Usage.Cost))
	embed := &discordgo.MessageEmbed{
		Description: truncateString(answer, 4000),
		Color:       orEmbedColor,
		Footer:      &discordgo.MessageEmbedFooter{Text: footer},
	}
	orFollowUpEmbed(s, i, embed)
}

func orHandleRoute(s *discordgo.Session, i *discordgo.InteractionCreate, sub *discordgo.ApplicationCommandInteractionDataOption) {
	orDefer(s, i)
	prompt := strings.TrimSpace(orOptStr(sub, "prompt"))
	if prompt == "" {
		orFollowUp(s, i, "**OpenRouter — Routing & Provider Preferences**\n\n"+
			"Route requests across upstream providers dynamically:\n\n"+
			"• `/openrouter route prompt:<text> sort:<price|latency|throughput>` — Query with provider sort\n"+
			"• `/openrouter free prompt:<text>` — Free models router (`openrouter/free`)\n"+
			"• `/openrouter online prompt:<text>` — Live web search routing (`:online`)\n"+
			"• `/openrouter think prompt:<text>` — Reasoning/thinking models\n"+
			"• `/openrouter fallback prompt:<text> models:<model1,model2>` — Explicit failover list\n"+
			"• `/openrouter fusion prompt:<text>` — Multi-model deliberation (`openrouter/fusion`)\n"+
			"• `/openrouter endpoints slug:<id>` — Inspect upstream endpoints and uptime\n\n"+
			"_Routing engine honors `sort: price`, `latency`, or `throughput`, fallback chains, and data privacy policies._")
		return
	}
	if orAPIKey() == "" {
		orFollowUp(s, i, "❌ No OpenRouter API key configured.")
		return
	}

	model := strings.TrimSpace(orOptStr(sub, "model"))
	if model == "" {
		model = "openrouter/auto"
	}
	sortPref := strings.TrimSpace(orOptStr(sub, "sort"))
	if sortPref == "" {
		sortPref = "price"
	}

	providerMap := map[string]any{
		"sort":            sortPref,
		"allow_fallbacks": true,
	}
	payload := map[string]any{
		"model":      model,
		"provider":   providerMap,
		"max_tokens": 1024,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
	}
	bodyBytes, _ := json.Marshal(payload)
	req, err := orRequest(http.MethodPost, "/chat/completions", nil, bodyBytes)
	if err != nil {
		orFollowUp(s, i, "❌ "+err.Error())
		return
	}
	body, status, err := orDo(req)
	if err != nil {
		orFollowUp(s, i, "❌ "+err.Error())
		return
	}
	if status != http.StatusOK {
		orFollowUp(s, i, "❌ "+orAPIError(body, status))
		return
	}

	var resp struct {
		ID      string `json:"id"`
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int       `json:"prompt_tokens"`
			CompletionTokens int       `json:"completion_tokens"`
			Cost             flexFloat `json:"cost"`
		} `json:"usage"`
	}
	if json.Unmarshal(body, &resp) != nil || len(resp.Choices) == 0 {
		orFollowUp(s, i, "❌ Could not parse completion response.")
		return
	}
	answer := strings.TrimSpace(resp.Choices[0].Message.Content)
	footer := fmt.Sprintf("%s · routed by %s · %d in / %d out · $%.6f",
		resp.Model, sortPref, resp.Usage.PromptTokens, resp.Usage.CompletionTokens, float64(resp.Usage.Cost))
	embed := &discordgo.MessageEmbed{
		Description: truncateString(answer, 4000),
		Color:       orEmbedColor,
		Footer:      &discordgo.MessageEmbedFooter{Text: footer},
	}
	orFollowUpEmbed(s, i, embed)
}

func orHandleFallback(s *discordgo.Session, i *discordgo.InteractionCreate, sub *discordgo.ApplicationCommandInteractionDataOption) {
	orDefer(s, i)
	prompt := strings.TrimSpace(orOptStr(sub, "prompt"))
	if prompt == "" {
		orFollowUp(s, i, "**OpenRouter — Model Fallback Array**\n\n"+
			"If a primary model fails or is rate-limited, OpenRouter automatically falls back down the list.\n\n"+
			"Usage: `/openrouter fallback prompt:<text> models:<model1,model2,...>`\n\n"+
			"Example: `/openrouter fallback prompt:Hello models:anthropic/claude-3.5-haiku,openai/gpt-4o-mini`")
		return
	}
	if orAPIKey() == "" {
		orFollowUp(s, i, "❌ No OpenRouter API key configured.")
		return
	}

	modelsStr := strings.TrimSpace(orOptStr(sub, "models"))
	var modelsList []string
	if modelsStr != "" {
		for _, m := range strings.Split(modelsStr, ",") {
			m = strings.TrimSpace(m)
			if m != "" {
				modelsList = append(modelsList, m)
			}
		}
	}
	if len(modelsList) == 0 {
		modelsList = []string{"anthropic/claude-3.5-haiku", "openai/gpt-4o-mini", "openrouter/auto"}
	}

	payload := map[string]any{
		"models":     modelsList,
		"max_tokens": 1024,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
	}
	bodyBytes, _ := json.Marshal(payload)
	req, err := orRequest(http.MethodPost, "/chat/completions", nil, bodyBytes)
	if err != nil {
		orFollowUp(s, i, "❌ "+err.Error())
		return
	}
	body, status, err := orDo(req)
	if err != nil {
		orFollowUp(s, i, "❌ "+err.Error())
		return
	}
	if status != http.StatusOK {
		orFollowUp(s, i, "❌ "+orAPIError(body, status))
		return
	}

	var resp struct {
		ID      string `json:"id"`
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int       `json:"prompt_tokens"`
			CompletionTokens int       `json:"completion_tokens"`
			Cost             flexFloat `json:"cost"`
		} `json:"usage"`
	}
	if json.Unmarshal(body, &resp) != nil || len(resp.Choices) == 0 {
		orFollowUp(s, i, "❌ Could not parse fallback response.")
		return
	}
	answer := strings.TrimSpace(resp.Choices[0].Message.Content)
	footer := fmt.Sprintf("%s · fallback list: [%s] · $%.6f", resp.Model, strings.Join(modelsList, ", "), float64(resp.Usage.Cost))
	embed := &discordgo.MessageEmbed{
		Description: truncateString(answer, 4000),
		Color:       orEmbedColor,
		Footer:      &discordgo.MessageEmbedFooter{Text: footer},
	}
	orFollowUpEmbed(s, i, embed)
}

func orHandleFusion(s *discordgo.Session, i *discordgo.InteractionCreate, sub *discordgo.ApplicationCommandInteractionDataOption) {
	orDefer(s, i)
	prompt := strings.TrimSpace(orOptStr(sub, "prompt"))
	if prompt == "" {
		orFollowUp(s, i, "❌ Give a prompt. Usage: `/openrouter fusion prompt:<text>`\n\n"+
			"Model Fusion routes your prompt through `openrouter/fusion`, multi-model deliberation that merges diverse responses.")
		return
	}
	if orAPIKey() == "" {
		orFollowUp(s, i, "❌ No OpenRouter API key configured.")
		return
	}

	payload := map[string]any{
		"model":      "openrouter/fusion",
		"max_tokens": 1500,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
	}
	bodyBytes, _ := json.Marshal(payload)
	req, err := orRequest(http.MethodPost, "/chat/completions", nil, bodyBytes)
	if err != nil {
		orFollowUp(s, i, "❌ "+err.Error())
		return
	}
	body, status, err := orDo(req)
	if err != nil {
		orFollowUp(s, i, "❌ "+err.Error())
		return
	}
	if status != http.StatusOK {
		orFollowUp(s, i, "❌ "+orAPIError(body, status))
		return
	}

	var resp struct {
		ID      string `json:"id"`
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int       `json:"prompt_tokens"`
			CompletionTokens int       `json:"completion_tokens"`
			Cost             flexFloat `json:"cost"`
		} `json:"usage"`
	}
	if json.Unmarshal(body, &resp) != nil || len(resp.Choices) == 0 {
		orFollowUp(s, i, "❌ Could not parse fusion response.")
		return
	}
	answer := strings.TrimSpace(resp.Choices[0].Message.Content)
	footer := fmt.Sprintf("%s · %d in / %d out · $%.6f", resp.Model, resp.Usage.PromptTokens, resp.Usage.CompletionTokens, float64(resp.Usage.Cost))
	embed := &discordgo.MessageEmbed{
		Title:       "⚡ OpenRouter Model Fusion",
		Description: truncateString(answer, 4000),
		Color:       orEmbedColor,
		Footer:      &discordgo.MessageEmbedFooter{Text: footer},
	}
	orFollowUpEmbed(s, i, embed)
}

func orHandleTools(s *discordgo.Session, i *discordgo.InteractionCreate, sub *discordgo.ApplicationCommandInteractionDataOption) {
	orDefer(s, i)
	q := url.Values{}
	q.Set("supported_parameters", "tools")
	q.Set("limit", "15")
	models, total, err := orFetchModels(q)
	if err != nil || len(models) == 0 {
		q.Set("supported_parameters", "tool_calls")
		models, total, err = orFetchModels(q)
	}
	if err != nil {
		orFollowUp(s, i, "❌ "+err.Error())
		return
	}
	if len(models) == 0 {
		orFollowUp(s, i, "No models with tool-calling support found.")
		return
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("**OpenRouter — models with tool calling** (showing %d of %d)\n\n", len(models), total))
	for _, m := range models {
		b.WriteString(fmt.Sprintf("• `%s` — %s · %s ctx\n", m.ID, m.Name, orFmtCtx(m.ContextLength)))
	}
	orFollowUp(s, i, b.String())
}

// ---------- Tier 2: retrieval (embed, similar, rerank) ----------

func orHandleEmbed(s *discordgo.Session, i *discordgo.InteractionCreate, sub *discordgo.ApplicationCommandInteractionDataOption) {
	orDefer(s, i)
	if orAPIKey() == "" {
		orFollowUp(s, i, "❌ No OpenRouter API key configured.")
		return
	}

	input := strings.TrimSpace(orOptStr(sub, "input"))
	if input == "" {
		orFollowUp(s, i, "❌ Give text to embed. Usage: `/openrouter embed input:<text>`")
		return
	}
	model := strings.TrimSpace(orOptStr(sub, "model"))
	if model == "" {
		model = "openai/text-embedding-3-small"
	}
	dimensions := 0
	if v, ok := orOptInt(sub, "dimensions"); ok && v > 0 {
		dimensions = int(v)
	}

	resp, err := orFetchEmbeddings(model, input, dimensions)
	if err != nil {
		orFollowUp(s, i, "❌ "+err.Error())
		return
	}
	if len(resp.Data) == 0 {
		orFollowUp(s, i, "No embeddings returned.")
		return
	}

	embedding := resp.Data[0].Embedding
	var vals []string
	for idx, v := range embedding {
		if idx >= 5 {
			break
		}
		vals = append(vals, fmt.Sprintf("%.6f", v))
	}
	preview := strings.Join(vals, ", ")
	if len(embedding) > 5 {
		preview += ", ..."
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("**Embedding** — `%s`\n\n", resp.Model))
	b.WriteString(fmt.Sprintf("• Dimensions: **%d**\n", len(embedding)))
	b.WriteString(fmt.Sprintf("• Preview: `[%s]`\n", preview))
	b.WriteString(fmt.Sprintf("• Tokens used: **%d**", resp.Usage.TotalTokens))
	orFollowUp(s, i, b.String())
}

func orHandleSimilar(s *discordgo.Session, i *discordgo.InteractionCreate, sub *discordgo.ApplicationCommandInteractionDataOption) {
	orDefer(s, i)
	if orAPIKey() == "" {
		orFollowUp(s, i, "❌ No OpenRouter API key configured.")
		return
	}

	textA := strings.TrimSpace(orOptStr(sub, "text_a"))
	textB := strings.TrimSpace(orOptStr(sub, "text_b"))
	if textA == "" || textB == "" {
		orFollowUp(s, i, "❌ Give two texts to compare. Usage: `/openrouter similar text_a:<text1> text_b:<text2>`")
		return
	}
	model := strings.TrimSpace(orOptStr(sub, "model"))
	if model == "" {
		model = "openai/text-embedding-3-small"
	}

	resp, err := orFetchEmbeddings(model, []string{textA, textB}, 0)
	if err != nil {
		orFollowUp(s, i, "❌ "+err.Error())
		return
	}
	if len(resp.Data) < 2 {
		orFollowUp(s, i, "❌ Failed to generate embeddings for both texts.")
		return
	}

	vecA := resp.Data[0].Embedding
	vecB := resp.Data[1].Embedding
	if len(vecA) != len(vecB) || len(vecA) == 0 {
		orFollowUp(s, i, "❌ Vector dimension mismatch.")
		return
	}

	var dot, magA, magB float64
	for idx := range vecA {
		dot += vecA[idx] * vecB[idx]
		magA += vecA[idx] * vecA[idx]
		magB += vecB[idx] * vecB[idx]
	}
	similarity := 0.0
	if magA > 0 && magB > 0 {
		similarity = dot / (math.Sqrt(magA) * math.Sqrt(magB))
	}

	pct := similarity * 100
	var b strings.Builder
	b.WriteString(fmt.Sprintf("**Semantic Similarity** — `%s`\n\n", resp.Model))
	b.WriteString(fmt.Sprintf("• **Cosine Similarity:** **%.2f%%** (score: `%.4f`)\n\n", pct, similarity))
	b.WriteString(fmt.Sprintf("**Text A:** %s\n", truncateString(textA, 150)))
	b.WriteString(fmt.Sprintf("**Text B:** %s\n\n", truncateString(textB, 150)))
	b.WriteString(fmt.Sprintf("_Dimensions: %d · Tokens used: %d_", len(vecA), resp.Usage.TotalTokens))
	orFollowUp(s, i, b.String())
}

func orHandleRerank(s *discordgo.Session, i *discordgo.InteractionCreate, sub *discordgo.ApplicationCommandInteractionDataOption) {
	orDefer(s, i)
	if orAPIKey() == "" {
		orFollowUp(s, i, "❌ No OpenRouter API key configured.")
		return
	}

	query := strings.TrimSpace(orOptStr(sub, "query"))
	if query == "" {
		orFollowUp(s, i, "❌ Give a query. Usage: `/openrouter rerank query:<text> documents:<doc1|doc2|...>`")
		return
	}
	docsStr := strings.TrimSpace(orOptStr(sub, "documents"))
	if docsStr == "" {
		orFollowUp(s, i, "❌ Give documents separated by `|`. Usage: `/openrouter rerank query:<text> documents:<doc1|doc2>`")
		return
	}
	documents := strings.Split(docsStr, "|")
	model := strings.TrimSpace(orOptStr(sub, "model"))
	if model == "" {
		model = "cohere/rerank-v3.5"
	}
	topN := 5
	if v, ok := orOptInt(sub, "top_n"); ok && v > 0 {
		topN = int(v)
	}

	resp, err := orFetchRerank(model, query, documents, topN)
	if err != nil {
		orFollowUp(s, i, "❌ "+err.Error())
		return
	}
	if len(resp.Results) == 0 {
		orFollowUp(s, i, "No rerank results.")
		return
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("**Rerank results** — `%s`\n\n", resp.Model))
	for _, r := range resp.Results {
		score := r.RelevanceScore * 100
		b.WriteString(fmt.Sprintf("• **%.1f%%** — %s\n", score, truncateString(r.Document.Text, 200)))
	}
	b.WriteString(fmt.Sprintf("\n_%d search units · total tokens %d_", resp.Usage.SearchUnits, resp.Usage.TotalTokens))
	orFollowUp(s, i, b.String())
}

// ---------- Tier 2: media (image, video, tts, stt) ----------

func orHandleImage(s *discordgo.Session, i *discordgo.InteractionCreate, sub *discordgo.ApplicationCommandInteractionDataOption) {
	orDefer(s, i)
	if orAPIKey() == "" {
		orFollowUp(s, i, "❌ No OpenRouter API key configured.")
		return
	}
	prompt := strings.TrimSpace(orOptStr(sub, "prompt"))
	if prompt == "" {
		orFollowUp(s, i, "❌ Give a prompt. Usage: `/openrouter image prompt:<text>`")
		return
	}
	model := strings.TrimSpace(orOptStr(sub, "model"))
	if model == "" {
		model = "bytedance-seed/seedream-5-0-flash"
	}

	payload := map[string]any{
		"model":  model,
		"prompt": prompt,
		"n":      1,
	}
	bodyBytes, _ := json.Marshal(payload)
	req, err := orRequest(http.MethodPost, "/images/generations", nil, bodyBytes)
	if err != nil {
		orFollowUp(s, i, "❌ "+err.Error())
		return
	}
	body, status, err := orDo(req)
	if err != nil {
		orFollowUp(s, i, "❌ "+err.Error())
		return
	}
	if status != http.StatusOK {
		orFollowUp(s, i, "❌ "+orAPIError(body, status))
		return
	}

	var resp struct {
		Created int64 `json:"created"`
		Data    []struct {
			B64JSON   string `json:"b64_json"`
			URL       string `json:"url"`
			MediaType string `json:"media_type,omitempty"`
		} `json:"data"`
		Usage struct {
			Cost flexFloat `json:"cost"`
		} `json:"usage"`
	}
	if json.Unmarshal(body, &resp) != nil || len(resp.Data) == 0 {
		orFollowUp(s, i, "❌ Could not parse image response.")
		return
	}

	costLine := ""
	if float64(resp.Usage.Cost) > 0 {
		costLine = fmt.Sprintf(" · $%.4f", float64(resp.Usage.Cost))
	}

	if resp.Data[0].B64JSON != "" {
		imgBytes, decErr := base64.StdEncoding.DecodeString(resp.Data[0].B64JSON)
		if decErr != nil {
			orFollowUp(s, i, "❌ Failed to decode image: "+decErr.Error())
			return
		}
		ext := "png"
		if resp.Data[0].MediaType == "image/jpeg" {
			ext = "jpg"
		} else if resp.Data[0].MediaType == "image/webp" {
			ext = "webp"
		}
		_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
			Content: fmt.Sprintf("**`%s`** generated%s", model, costLine),
			Files: []*discordgo.File{
				{Name: fmt.Sprintf("or_image_%d.%s", resp.Created, ext), Reader: bytes.NewReader(imgBytes)},
			},
		})
		return
	}

	if resp.Data[0].URL != "" {
		embed := &discordgo.MessageEmbed{
			Title:       fmt.Sprintf("`%s` generated%s", model, costLine),
			Description: truncateString(prompt, 200),
			Image:       &discordgo.MessageEmbedImage{URL: resp.Data[0].URL},
			Color:       orEmbedColor,
		}
		orFollowUpEmbed(s, i, embed)
		return
	}

	orFollowUp(s, i, "❌ No image data or URL returned.")
}

func orHandleVideo(s *discordgo.Session, i *discordgo.InteractionCreate, sub *discordgo.ApplicationCommandInteractionDataOption) {
	orDefer(s, i)
	prompt := strings.TrimSpace(orOptStr(sub, "prompt"))
	if prompt == "" {
		models, err := orFetchVideoModels()
		if err != nil {
			orFollowUp(s, i, "❌ Could not load video model catalog: "+err.Error())
			return
		}
		var b strings.Builder
		b.WriteString("**OpenRouter — Video Generation**\n\n")
		b.WriteString("Available video generation models:\n")
		for _, m := range models {
			name := m.Name
			if name == "" {
				name = m.ID
			}
			b.WriteString(fmt.Sprintf("• `%s` — %s", m.ID, name))
			if len(m.SupportedDurations) > 0 {
				durations := make([]string, len(m.SupportedDurations))
				for idx, duration := range m.SupportedDurations {
					durations[idx] = strconv.Itoa(duration)
				}
				b.WriteString(" · " + strings.Join(durations, ", ") + "s")
			}
			b.WriteByte('\n')
		}
		if len(models) == 0 {
			b.WriteString("No video models are currently listed.\n")
		}
		b.WriteString("\nUsage: `/openrouter video prompt:<description> [model]`\n")
		b.WriteString("_Video generation runs asynchronously and may take several minutes._")
		orFollowUp(s, i, b.String())
		return
	}
	if orAPIKey() == "" {
		orFollowUp(s, i, "❌ No OpenRouter API key configured.")
		return
	}

	model := strings.TrimSpace(orOptStr(sub, "model"))
	if model == "" {
		model = "google/veo-3.1"
	}

	payload := map[string]any{
		"model":  model,
		"prompt": prompt,
	}
	bodyBytes, _ := json.Marshal(payload)
	req, err := orRequest(http.MethodPost, "/videos", nil, bodyBytes)
	if err != nil {
		orFollowUp(s, i, "❌ "+err.Error())
		return
	}
	body, status, err := orDo(req)
	if err != nil {
		orFollowUp(s, i, "❌ "+err.Error())
		return
	}
	if status != http.StatusAccepted && status != http.StatusOK {
		orFollowUp(s, i, "❌ "+orAPIError(body, status))
		return
	}

	var job struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		Error  string `json:"error"`
	}
	if err := json.Unmarshal(body, &job); err != nil {
		orFollowUp(s, i, "❌ Could not parse OpenRouter video job response.")
		return
	}
	if job.ID == "" {
		orFollowUp(s, i, "❌ OpenRouter accepted the request but did not return a video job ID.")
		return
	}

	orFollowUp(s, i, fmt.Sprintf("🎬 Video generation queued with `%s` (job `%s`). I’ll post the video when it’s ready.", model, job.ID))
	deadline := time.Now().Add(8 * time.Minute)
	for time.Now().Before(deadline) {
		time.Sleep(30 * time.Second)
		pollReq, err := orRequest(http.MethodGet, "/videos/"+url.PathEscape(job.ID), nil, nil)
		if err != nil {
			orFollowUp(s, i, "❌ Could not create video status request: "+err.Error())
			return
		}
		pollBody, pollStatus, err := orDo(pollReq)
		if err != nil {
			orFollowUp(s, i, "❌ Could not check video status: "+err.Error())
			return
		}
		if pollStatus != http.StatusOK {
			orFollowUp(s, i, "❌ Could not check video status: "+orAPIError(pollBody, pollStatus))
			return
		}

		var status struct {
			Status       string   `json:"status"`
			Error        string   `json:"error"`
			UnsignedURLs []string `json:"unsigned_urls"`
			Usage        struct {
				Cost flexFloat `json:"cost"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(pollBody, &status); err != nil {
			orFollowUp(s, i, "❌ Could not parse OpenRouter video status response.")
			return
		}
		switch status.Status {
		case "failed":
			reason := strings.TrimSpace(status.Error)
			if reason == "" {
				reason = "OpenRouter reported that video generation failed."
			}
			orFollowUp(s, i, "❌ "+reason)
			return
		case "completed":
			if len(status.UnsignedURLs) == 0 {
				orFollowUp(s, i, "❌ Video generation completed without a downloadable video.")
				return
			}
			video, err := orDownloadVideo(job.ID)
			if err != nil {
				orFollowUp(s, i, "❌ Video is ready, but I couldn’t download it: "+err.Error())
				return
			}
			costLine := ""
			if float64(status.Usage.Cost) > 0 {
				costLine = fmt.Sprintf(" · $%.4f", float64(status.Usage.Cost))
			}
			_, err = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
				Content: fmt.Sprintf("✅ **`%s`** video generated%s", model, costLine),
				Files: []*discordgo.File{{
					Name:   "openrouter_video.mp4",
					Reader: bytes.NewReader(video),
				}},
			})
			if err != nil {
				log.Printf("openrouter video: failed to send generated video: %v", err)
			}
			return
		}
	}

	orFollowUp(s, i, fmt.Sprintf("⏳ Video job `%s` is still processing. Check back later; generation can take several minutes.", job.ID))
}

func orDownloadVideo(jobID string) ([]byte, error) {
	req, err := orRequest(http.MethodGet, "/videos/"+url.PathEscape(jobID)+"/content?index=0", nil, nil)
	if err != nil {
		return nil, err
	}
	resp, err := (&http.Client{Timeout: orHTTPTimeout}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if readErr != nil {
			return nil, readErr
		}
		return nil, fmt.Errorf("%s", orAPIError(body, resp.StatusCode))
	}

	const maxVideoBytes = 24 << 20
	video, err := io.ReadAll(io.LimitReader(resp.Body, maxVideoBytes+1))
	if err != nil {
		return nil, err
	}
	if len(video) > maxVideoBytes {
		return nil, fmt.Errorf("download exceeds the bot’s 24 MiB attachment limit")
	}
	return video, nil
}

func orHandleTTS(s *discordgo.Session, i *discordgo.InteractionCreate, sub *discordgo.ApplicationCommandInteractionDataOption) {
	orDefer(s, i)
	if orAPIKey() == "" {
		orFollowUp(s, i, "❌ No OpenRouter API key configured.")
		return
	}
	text := strings.TrimSpace(orOptStr(sub, "text"))
	if text == "" {
		orFollowUp(s, i, "❌ Give text. Usage: `/openrouter tts text:<words> [model] [voice]`")
		return
	}
	model := strings.TrimSpace(orOptStr(sub, "model"))
	if model == "" {
		model = "hexgrad/kokoro-82m"
	}
	voice := strings.TrimSpace(orOptStr(sub, "voice"))
	if voice == "" {
		voice = "af_bella"
	}

	resp, err := orFetchTTS(model, text, voice, "mp3", 0)
	if err != nil {
		orFollowUp(s, i, "❌ "+err.Error())
		return
	}
	if len(resp.Data) == 0 {
		orFollowUp(s, i, "❌ Empty audio response from OpenRouter.")
		return
	}

	fileName := "speech.mp3"
	if strings.Contains(resp.ContentType, "wav") {
		fileName = "speech.wav"
	}

	_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
		Content: fmt.Sprintf("**TTS** — `%s` (voice: `%s`)", model, voice),
		Files: []*discordgo.File{
			{Name: fileName, Reader: bytes.NewReader(resp.Data)},
		},
	})
}

func orHandleSTT(s *discordgo.Session, i *discordgo.InteractionCreate, sub *discordgo.ApplicationCommandInteractionDataOption) {
	orDefer(s, i)
	if orAPIKey() == "" {
		orFollowUp(s, i, "❌ No OpenRouter API key configured.")
		return
	}
	model := strings.TrimSpace(orOptStr(sub, "model"))
	if model == "" {
		model = "openai/whisper-large-v3"
	}
	language := strings.TrimSpace(orOptStr(sub, "language"))

	var audioBytes []byte
	var audioFmt string = "mp3"

	// 1. Check if user provided audio_url
	audioURL := strings.TrimSpace(orOptStr(sub, "audio_url"))

	// 2. Check if user passed an attachment
	if audioURL == "" {
		for _, o := range sub.Options {
			if o.Type == discordgo.ApplicationCommandOptionAttachment {
				if attID, ok := o.Value.(string); ok && i.ApplicationCommandData().Resolved != nil {
					if att, exists := i.ApplicationCommandData().Resolved.Attachments[attID]; exists {
						audioURL = att.URL
					}
				}
			}
		}
	}

	if audioURL == "" {
		orFollowUp(s, i, "**OpenRouter — Speech-to-Text (STT)**\n\n"+
			"Transcribe audio using Whisper:\n"+
			"• `/openrouter stt audio:<file> [model]` — upload an audio attachment directly\n"+
			"• `/openrouter stt audio_url:<url> [model]` — transcribe audio from a direct URL\n\n"+
			fmt.Sprintf("_Default model: `%s`_", model))
		return
	}

	// Download audio bytes
	httpClient := &http.Client{Timeout: 30 * time.Second}
	dlResp, dlErr := httpClient.Get(audioURL)
	if dlErr != nil {
		orFollowUp(s, i, "❌ Failed to download audio: "+dlErr.Error())
		return
	}
	defer dlResp.Body.Close()
	data, rErr := io.ReadAll(io.LimitReader(dlResp.Body, 20<<20))
	if rErr != nil || len(data) == 0 {
		orFollowUp(s, i, "❌ Could not read audio content.")
		return
	}
	audioBytes = data
	if strings.Contains(strings.ToLower(audioURL), ".wav") {
		audioFmt = "wav"
	} else if strings.Contains(strings.ToLower(audioURL), ".m4a") {
		audioFmt = "m4a"
	}

	b64Data := base64.StdEncoding.EncodeToString(audioBytes)
	sttResp, err := orFetchSTT(model, b64Data, audioFmt, language)
	if err != nil {
		orFollowUp(s, i, "❌ "+err.Error())
		return
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("**Transcription** — `%s`\n\n", model))
	b.WriteString(sttResp.Text + "\n\n")
	if sttResp.Usage.Cost > 0 || sttResp.Usage.Seconds > 0 {
		b.WriteString(fmt.Sprintf("_Audio duration: %.1fs · Cost: $%.6f_", sttResp.Usage.Seconds, sttResp.Usage.Cost))
	}
	orFollowUp(s, i, b.String())
}

// ---------- Tier 2: bench ----------

func orHandleBench(s *discordgo.Session, i *discordgo.InteractionCreate, sub *discordgo.ApplicationCommandInteractionDataOption) {
	orDefer(s, i)
	benchmarks, err := orFetchBenchmarks()
	if err != nil {
		orFollowUp(s, i, "❌ "+err.Error())
		return
	}
	if len(benchmarks) == 0 {
		orFollowUp(s, i, "No benchmarks available.")
		return
	}

	filterSlug := strings.ToLower(strings.TrimSpace(orOptStr(sub, "model")))

	var b strings.Builder
	if filterSlug != "" {
		b.WriteString(fmt.Sprintf("**OpenRouter Benchmarks for `%s`**\n\n", filterSlug))
		found := 0
		for _, bm := range benchmarks {
			if strings.Contains(strings.ToLower(bm.ModelPermaslug), filterSlug) || strings.Contains(strings.ToLower(bm.DisplayName), filterSlug) {
				found++
				intel := "—"
				if bm.IntelligenceIndex != nil {
					intel = fmt.Sprintf("%.1f", *bm.IntelligenceIndex)
				}
				coding := "—"
				if bm.CodingIndex != nil {
					coding = fmt.Sprintf("%.1f", *bm.CodingIndex)
				}
				agentic := "—"
				if bm.AgenticIndex != nil {
					agentic = fmt.Sprintf("%.1f", *bm.AgenticIndex)
				}
				b.WriteString(fmt.Sprintf("• **%s** (`%s`)\n  Intelligence: **%s** · Coding: **%s** · Agentic: **%s** · Price: %s\n",
					bm.DisplayName, bm.Source, intel, coding, agentic, orPricePair(bm.Pricing)))
				if found >= 10 {
					break
				}
			}
		}
		if found == 0 {
			b.WriteString("No benchmark entries found for that model slug.\n")
		}
		orFollowUp(s, i, b.String())
		return
	}

	// Sort by Intelligence Index descending
	var validIntel []orBenchmarkEntry
	for _, bm := range benchmarks {
		if bm.IntelligenceIndex != nil {
			validIntel = append(validIntel, bm)
		}
	}
	sort.Slice(validIntel, func(idxA, idxB int) bool {
		return *validIntel[idxA].IntelligenceIndex > *validIntel[idxB].IntelligenceIndex
	})

	if len(validIntel) > 10 {
		validIntel = validIntel[:10]
	}

	b.WriteString("**OpenRouter — Top Models by Intelligence Benchmark**\n\n")
	for idx, bm := range validIntel {
		intel := fmt.Sprintf("%.1f", *bm.IntelligenceIndex)
		coding := "—"
		if bm.CodingIndex != nil {
			coding = fmt.Sprintf("%.1f", *bm.CodingIndex)
		}
		agentic := "—"
		if bm.AgenticIndex != nil {
			agentic = fmt.Sprintf("%.1f", *bm.AgenticIndex)
		}
		b.WriteString(fmt.Sprintf("**%d. %s** (`%s`)\n   Intelligence: **%s** · Coding: **%s** · Agentic: **%s** · %s\n",
			idx+1, bm.DisplayName, bm.ModelPermaslug, intel, coding, agentic, orPricePair(bm.Pricing)))
	}
	b.WriteString("\n_Use `/openrouter bench model:<slug>` to query specific model benchmark scores._")
	orFollowUp(s, i, b.String())
}

// ---------- Tier 3: management-key commands (budget, analytics, guardrails, keys, batch) ----------

func orHandleBudget(s *discordgo.Session, i *discordgo.InteractionCreate, _ *discordgo.ApplicationCommandInteractionDataOption) {
	orDefer(s, i)
	if orAPIKey() == "" {
		orFollowUp(s, i, "\u274c No OpenRouter API key configured.")
		return
	}
	credits, err := orFetchCredits()
	if err != nil {
		orFollowUp(s, i, "\u274c "+err.Error()+"\n\n_This may need a management key. Use `/openrouter credits` for the basic key endpoint._")
		return
	}
	total := float64(credits.TotalCredits)
	used := float64(credits.TotalUsage)
	pct := 0.0
	if total > 0 {
		pct = (used / total) * 100
	}
	var b strings.Builder
	b.WriteString("**OpenRouter \u2014 Budget & Credits**\n\n")
	b.WriteString(fmt.Sprintf("Total purchased:   **$%.2f**\n", total))
	b.WriteString(fmt.Sprintf("Total used:       **$%.2f** (%.1f%%)\n", used, pct))
	b.WriteString(fmt.Sprintf("Remaining:        **$%.2f**\n", total-used))
	b.WriteString("\n_Use `/openrouter keys` to list API keys and their individual limits._")
	orFollowUp(s, i, b.String())
}

func orHandleAnalytics(s *discordgo.Session, i *discordgo.InteractionCreate, sub *discordgo.ApplicationCommandInteractionDataOption) {
	orDefer(s, i)
	if orMgmtAPIKey() == "" {
		orFollowUp(s, i, "\u274c Analytics requires an OpenRouter management key configured in `.private/config.json` (`openrouter.apiManagementKey`).")
		return
	}
	days := 3
	if v, ok := orOptInt(sub, "days"); ok && v > 0 && v <= 90 {
		days = int(v)
	}
	data, err := orFetchAnalytics(newORAnalyticsQuery(time.Now(), days))
	if err != nil {
		orFollowUp(s, i, "\u274c "+err.Error())
		return
	}
	formatted, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		orFollowUp(s, i, "\u274c Could not format analytics results: "+err.Error())
		return
	}
	if len(formatted) == 0 || string(formatted) == "null" {
		orFollowUp(s, i, fmt.Sprintf("No analytics data returned for the last %d days.", days))
		return
	}
	orFollowUp(s, i, fmt.Sprintf("**OpenRouter \u2014 Analytics** (last %d days)\n```json\n%s\n```",
		days, truncateString(string(formatted), 1700)))
}

func orHandleGuardrails(s *discordgo.Session, i *discordgo.InteractionCreate, _ *discordgo.ApplicationCommandInteractionDataOption) {
	orDefer(s, i)
	if orAPIKey() == "" {
		orFollowUp(s, i, "\u274c No OpenRouter API key configured.")
		return
	}
	rails, err := orFetchGuardrails()
	if err != nil {
		orFollowUp(s, i, "\u274c "+err.Error()+"\n\n_Requires a management key. See https://openrouter.ai/settings/api-keys_")
		return
	}
	if len(rails) == 0 {
		orFollowUp(s, i, "No guardrails configured in your workspace.\n\nCreate them at https://openrouter.ai/workspaces")
		return
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("**OpenRouter \u2014 Guardrails** (%d)\n\n", len(rails)))
	for _, g := range rails {
		providers := orOrDash(strings.Join(g.AllowedProviders, ", "))
		limitLine := ""
		if g.LimitUsd > 0 {
			limitLine = fmt.Sprintf(" \u2022 $%.0f/%s", g.LimitUsd, orOrDash(g.ResetInterval))
		}
		zdr := ""
		if g.EnforceZdr {
			zdr = " \u2022 ZDR"
		}
		b.WriteString(fmt.Sprintf("\u2022 **%s**%s%s\n  Providers: %s\n", g.Name, limitLine, zdr, providers))
	}
	orFollowUp(s, i, b.String())
}

func orHandleKeys(s *discordgo.Session, i *discordgo.InteractionCreate, _ *discordgo.ApplicationCommandInteractionDataOption) {
	orDefer(s, i)
	if orAPIKey() == "" {
		orFollowUp(s, i, "\u274c No OpenRouter API key configured.")
		return
	}
	items, err := orFetchKeys()
	if err != nil {
		orFollowUp(s, i, "\u274c "+err.Error()+"\n\n_Requires a management key. See https://openrouter.ai/settings/api-keys_")
		return
	}
	if len(items) == 0 {
		orFollowUp(s, i, "No API keys found in your workspace.")
		return
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("**OpenRouter \u2014 API Keys** (%d)\n\n", len(items)))
	for _, k := range items {
		status := "\u2705"
		if k.Disabled {
			status = "\u274c"
		}
		limitStr := "unlimited"
		if k.Limit > 0 {
			limitStr = fmt.Sprintf("$%.2f/%s", k.Limit, orOrDash(k.LimitReset))
		}
		remaining := ""
		if k.LimitRemaining > 0 {
			remaining = fmt.Sprintf(" \u2022 $%.2f remaining", k.LimitRemaining)
		}
		b.WriteString(fmt.Sprintf("%s **%s** \u2014 %s%s\n  Usage: $%.2f/mo%s\n", status, orOrDash(k.Label), limitStr, remaining, k.UsageMonthly, orOrDash(k.Name)))
	}
	b.WriteString("\n_Only the key hash is stored; full keys are shown once on creation._")
	orFollowUp(s, i, b.String())
}

func orHandleBatch(s *discordgo.Session, i *discordgo.InteractionCreate, sub *discordgo.ApplicationCommandInteractionDataOption) {
	orDefer(s, i)
	if orAPIKey() == "" {
		orFollowUp(s, i, "\u274c No OpenRouter API key configured.")
		return
	}

	statusFilter := strings.TrimSpace(orOptStr(sub, "status"))
	batches, err := orFetchBatches(statusFilter)
	if err != nil {
		orFollowUp(s, i, "\u274c "+err.Error()+"\n\n_Batch API may need a management key._")
		return
	}
	if len(batches) == 0 {
		var suffix string
		if statusFilter != "" {
			suffix = fmt.Sprintf(" (status: %s)", statusFilter)
		}
		orFollowUp(s, i, "No batches found."+suffix)
		return
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("**OpenRouter \u2014 Batches** (%d)\n\n", len(batches)))
	for _, bt := range batches {
		emoji := ""
		switch bt.Status {
		case "completed":
			emoji = "\u2705"
		case "failed":
			emoji = "\u274c"
		case "validating":
			emoji = "\u23f3"
		case "in_progress":
			emoji = "\u0031\ufe0f\u20e3"
		default:
			emoji = "\u2753"
		}
		createdStr := ""
		if bt.CreatedAt > 0 {
			t := time.Unix(bt.CreatedAt, 0)
			createdStr = fmt.Sprintf(" \u2022 <t:%d:R>", t.Unix())
		}
		b.WriteString(fmt.Sprintf("%s `%s` \u2014 %s%s\n  %s \u2022 %d/%d/%d\n",
			emoji, bt.ID, bt.Status, createdStr,
			orOrDash(bt.Model), bt.RequestCounts.Completed, bt.RequestCounts.Failed, bt.RequestCounts.Total))
	}
	b.WriteString("\n_Use `/openrouter batch status:completed` to filter by status._")
	orFollowUp(s, i, b.String())
}

// ---------- pagination helpers ----------

// orPaginationButtons returns prev / next buttons for a paginated list.
// The custom_id format is "or:page:<type>:<page>" for prev and "or:npage:<type>:<page>" for next.
func orPaginationButtons(page, totalPages int, pageType string) []discordgo.MessageComponent {
	var buttons []discordgo.MessageComponent
	if page > 0 {
		buttons = append(buttons, discordgo.Button{
			CustomID: fmt.Sprintf("or:page:%s:%d", pageType, page),
			Label:    "\u25c0 Prev",
			Style:    discordgo.SecondaryButton,
		})
	}
	if page+1 < totalPages {
		label := "Next \u25b6"
		if page == 0 && totalPages == 1 {
			label = ""
		}
		if label != "" {
			buttons = append(buttons, discordgo.Button{
				CustomID: fmt.Sprintf("or:npage:%s:%d", pageType, page),
				Label:    label,
				Style:    discordgo.SecondaryButton,
			})
		}
	}
	if len(buttons) > 0 {
		return []discordgo.MessageComponent{
			discordgo.ActionsRow{Components: buttons},
		}
	}
	return nil
}

// orHandleComponent handles button interactions with "or:" custom IDs.
func orHandleComponent(s *discordgo.Session, i *discordgo.InteractionCreate) {
	data := i.MessageComponentData()
	parts := strings.Split(data.CustomID, ":")
	if len(parts) < 4 {
		return
	}
	dir := parts[1]      // "page" for prev, "npage" for next
	pageType := parts[2] // "help", "providers", etc.
	curPage, err := strconv.ParseInt(parts[3], 10, 64)
	if err != nil {
		return
	}

	newPage := curPage
	if dir == "page" {
		newPage = curPage - 1
	} else if dir == "npage" {
		newPage = curPage + 1
	}
	if newPage < 0 {
		newPage = 0
	}

	switch pageType {
	case "help":
		orShowHelpPage(s, i, int(newPage))
	case "providers":
		orShowProvidersPage(s, i, int(newPage))
	}
}

// orShowHelpPage sends one page of help text.
func orShowHelpPage(s *discordgo.Session, i *discordgo.InteractionCreate, page int) {
	pages := orHelpPages()
	if page < 0 || page >= len(pages) {
		return
	}
	content := pages[page]
	buttons := orPaginationButtons(page, len(pages), "help")

	// If we can update in-place (button click), use UpdateMessage
	if i.Type == discordgo.InteractionMessageComponent {
		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseUpdateMessage,
			Data: &discordgo.InteractionResponseData{
				Content:    content,
				Components: buttons,
			},
		})
	} else {
		// First call from help command
		if buttons != nil {
			_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content:    content,
					Components: buttons,
				},
			})
		} else {
			orReply(s, i, content)
		}
	}
}

// orHelpPages returns the help text split into pages.
func orHelpPages() []string {
	return []string{
		"**OpenRouter — `/openrouter`**\n" +
			"Browse the catalog, check live prices, audit spend, and route across models.\n" +
			"_Powered by https://openrouter.ai_\n\n" +
			"**Catalog & Discovery**\n" +
			"• `/openrouter models [query] [sort] [modality] [max_price] [limit]`\n" +
			"  Search models with sorting, modalities, price caps.\n" +
			"• `/openrouter model slug:<id>`\n" +
			"  Full specs for one model (context, pricing, parameters, modalities).\n" +
			"• `/openrouter compare model_a:<id> model_b:<id>`\n" +
			"  Side-by-side model comparison.\n" +
			"• `/openrouter deals [limit]`\n" +
			"  Discounted models, ranked by discount percentage.\n" +
			"• `/openrouter endpoints slug:<id>`\n" +
			"  Upstream providers serving a model, with pricing, uptime, latency.\n" +
			"• `/openrouter price slug:<id> [in_tokens] [out_tokens]`\n" +
			"  Price lookup + cost estimator.\n" +
			"• `/openrouter bench [model]`\n" +
			"  Leaderboard & scores (intelligence, coding, agentic) from Artificial Analysis.",

		"**Chat & Intelligent Routing**\n" +
			"• `/openrouter ask prompt:<text> [model] [max_tokens] [temperature]`\n" +
			"  Query any model with real cost/token tracking.\n" +
			"• `/openrouter free prompt:<text>`\n" +
			"  Chat via the free models router (`openrouter/free`).\n" +
			"• `/openrouter online prompt:<text> [model]`\n" +
			"  Live web search–augmented answers (`:online`).\n" +
			"• `/openrouter think prompt:<text> [model] [reasoning_tokens]`\n" +
			"  Reasoning/thinking models with reasoning token budget.\n" +
			"• `/openrouter route [prompt] [sort] [model]`\n" +
			"  Route by provider preference (`price`, `latency`, `throughput`).\n" +
			"• `/openrouter fallback [prompt] [models]`\n" +
			"  Failover array across multiple backup models.\n" +
			"• `/openrouter fusion prompt:<text>`\n" +
			"  Multi-model deliberation (`openrouter/fusion`).\n" +
			"• `/openrouter tools`\n" +
			"  Catalog models supporting function/tool calling.",

		"**Retrieval & Embeddings**\n" +
			"• `/openrouter embed input:<text> [model] [dimensions]`\n" +
			"  Generate vector embeddings with preview.\n" +
			"• `/openrouter similar text_a:<text> text_b:<text> [model]`\n" +
			"  Compute cosine semantic similarity between two texts.\n" +
			"• `/openrouter rerank query:<text> documents:<doc1|doc2|...> [model]`\n" +
			"  Rerank documents by semantic relevance score.",

		"**Multimodal Media & Audio**\n" +
			"• `/openrouter image prompt:<text> [model]`\n" +
			"  Generate images (ByteDance Seedream, Flux, etc.).\n" +
			"• `/openrouter video [prompt] [model]`\n" +
			"  Video generation and catalog models (Luma Ray 2, etc.).\n" +
			"• `/openrouter tts text:<text> [model] [voice]`\n" +
			"  Text-to-speech audio (.mp3 attachment via Kokoro-82M).\n" +
			"• `/openrouter stt [audio] [audio_url] [model]`\n" +
			"  Transcribe voice/audio files via Whisper-large-v3.",

		"**Account, Keys & Spend**\n" +
			"• `/openrouter key`\n" +
			"  Spend limits, free model quota, and key details.\n" +
			"• `/openrouter analytics [days]`\n" +
			"  Request counts by model (requires a management key).\n" +
			"• `/openrouter usage`\n" +
			"  Spend dashboard: today, week, month, lifetime, BYOK.\n" +
			"• `/openrouter gen id:<gen-...>`\n" +
			"  Audit a single request's real cost, latency, tokens, cache discounts.\n" +
			"• `/openrouter help`\n" +
			"  Interactive guide for all /openrouter commands.\n\n" +
			"_Router slugs: `openrouter/auto`, `openrouter/free`, `openrouter/fusion`_\n" +
			"_Docs: https://openrouter.ai/docs_",
	}
}

func orShowProvidersPage(s *discordgo.Session, i *discordgo.InteractionCreate, page int) {
	req, err := orRequest(http.MethodGet, "/providers", nil, nil)
	if err != nil {
		orFollowUp(s, i, "\u274c "+err.Error())
		return
	}
	body, status, err := orDo(req)
	if err != nil {
		orFollowUp(s, i, "\u274c "+err.Error())
		return
	}
	if status != http.StatusOK {
		orFollowUp(s, i, "\u274c "+orAPIError(body, status))
		return
	}
	var resp struct {
		Data []orProvider `json:"data"`
	}
	if json.Unmarshal(body, &resp) != nil || len(resp.Data) == 0 {
		orFollowUp(s, i, "No providers returned.")
		return
	}

	names := make([]string, 0, len(resp.Data))
	for _, p := range resp.Data {
		label := p.Name
		if p.Slug != "" {
			label += " (`" + p.Slug + "`)"
		}
		names = append(names, label)
	}
	sort.Strings(names)
	perPage := 20
	totalPages := (len(names) + perPage - 1) / perPage
	if page >= totalPages {
		page = totalPages - 1
	}
	if page < 0 {
		page = 0
	}

	start := page * perPage
	end := start + perPage
	if end > len(names) {
		end = len(names)
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("**OpenRouter upstream providers** (page %d/%d, showing %d-%d of %d)\n\n", page+1, totalPages, start+1, end, len(names)))
	for _, n := range names[start:end] {
		b.WriteString("\u2022 " + n + "\n")
	}
	buttons := orPaginationButtons(page, totalPages, "providers")

	if i.Type == discordgo.InteractionMessageComponent {
		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseUpdateMessage,
			Data: &discordgo.InteractionResponseData{
				Content:    clipMessage(b.String()),
				Components: buttons,
			},
		})
	} else {
		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content:    clipMessage(b.String()),
				Components: buttons,
			},
		})
	}
}

func openRouterHelpText() string {
	return "**OpenRouter — `/openrouter`**\n" +
		"Use the arrows below to flip through help pages, or use specific subcommand help.\n" +
		"_Powered by https://openrouter.ai/docs_"
}

// ---------- small local helpers ----------

func orOrDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}

func orThousands(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	var b strings.Builder
	for idx, c := range s {
		if idx > 0 && (len(s)-idx)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

// ---------- dispatcher + command spec ----------

func handleOpenRouterCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	data := i.ApplicationCommandData()
	if len(data.Options) == 0 {
		orShowHelpPage(s, i, 0)
		return
	}
	sub := data.Options[0]
	switch sub.Name {
	case "models":
		orHandleModels(s, i, sub)
	case "deals":
		orHandleDeals(s, i, sub)
	case "model":
		orHandleModel(s, i, sub)
	case "compare":
		orHandleCompare(s, i, sub)
	case "endpoints":
		orHandleEndpoints(s, i, sub)
	case "providers":
		orShowProvidersPage(s, i, 0)
	case "price":
		orHandlePrice(s, i, sub)
	case "key", "credits":
		orHandleCredits(s, i, sub)
	case "gen":
		orHandleGen(s, i, sub)
	case "history":
		orHandleHistory(s, i, sub)
	case "usage":
		orHandleUsage(s, i, sub)
	case "ask", "chat":
		orHandleAsk(s, i, sub)
	case "help":
		orShowHelpPage(s, i, 0)
	// Routing: route · fallback · free · online · think · fusion · tools
	case "route":
		orHandleRoute(s, i, sub)
	case "fallback":
		orHandleFallback(s, i, sub)
	case "free":
		orHandleFree(s, i, sub)
	case "online":
		orHandleOnline(s, i, sub)
	case "think":
		orHandleThink(s, i, sub)
	case "fusion":
		orHandleFusion(s, i, sub)
	case "tools":
		orHandleTools(s, i, sub)
	// Retrieval: embed · similar · rerank
	case "embed":
		orHandleEmbed(s, i, sub)
	case "similar":
		orHandleSimilar(s, i, sub)
	case "rerank":
		orHandleRerank(s, i, sub)
	// Media: image · video · tts · stt
	case "image":
		orHandleImage(s, i, sub)
	case "video":
		orHandleVideo(s, i, sub)
	case "tts":
		orHandleTTS(s, i, sub)
	case "stt", "transcribe":
		orHandleSTT(s, i, sub)
	// Benchmark: bench
	case "bench":
		orHandleBench(s, i, sub)
	// Tier 3 — management (needs management key)
	case "budget":
		orHandleBudget(s, i, sub)
	case "analytics":
		orHandleAnalytics(s, i, sub)
	case "guardrails":
		orHandleGuardrails(s, i, sub)
	case "keys":
		orHandleKeys(s, i, sub)
	case "batch":
		orHandleBatch(s, i, sub)
	default:
		orReply(s, i, "Unknown /openrouter subcommand. Try `/openrouter help`.")
	}
}

// openRouterCommandSpec returns the /openrouter command definition + handler,
// ready to drop into commandSpecs in commands.go.
// Note: Discord allows a maximum of 25 subcommands/options at the root level.
func openRouterCommandSpec() commandSpec {
	return commandSpec{
		Command: &discordgo.ApplicationCommand{
			Name:        "openrouter",
			Description: "OpenRouter: browse models, prices, routing, retrieval, media, and benchmarks.",
			Options: []*discordgo.ApplicationCommandOption{
				// Core Catalog (6)
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "models",
					Description: "Search the OpenRouter model catalog.",
					Options: []*discordgo.ApplicationCommandOption{
						{Type: discordgo.ApplicationCommandOptionString, Name: "query", Description: "Keyword search (e.g. 'gemini', 'deepseek')", Required: false},
						{
							Type:        discordgo.ApplicationCommandOptionString,
							Name:        "sort",
							Description: "Sort order",
							Required:    false,
							Choices: []*discordgo.ApplicationCommandOptionChoice{
								{Name: "Most popular", Value: "most-popular"},
								{Name: "Newest", Value: "newest"},
								{Name: "Price: low to high", Value: "pricing-low-to-high"},
								{Name: "Price: high to low", Value: "pricing-high-to-low"},
								{Name: "Context: high to low", Value: "context-high-to-low"},
							},
						},
						{
							Type:        discordgo.ApplicationCommandOptionString,
							Name:        "modality",
							Description: "Filter by output modality",
							Required:    false,
							Choices: []*discordgo.ApplicationCommandOptionChoice{
								{Name: "Text", Value: "text"},
								{Name: "Image", Value: "image"},
								{Name: "Audio", Value: "audio"},
								{Name: "Video", Value: "video"},
								{Name: "Embeddings", Value: "embeddings"},
							},
						},
						{Type: discordgo.ApplicationCommandOptionNumber, Name: "max_price", Description: "Max prompt price in USD per million tokens", Required: false},
						{Type: discordgo.ApplicationCommandOptionInteger, Name: "limit", Description: "How many models to show (max 20)", Required: false},
					},
				},
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "deals",
					Description: "Show currently discounted models, ranked by discount.",
					Options: []*discordgo.ApplicationCommandOption{
						{Type: discordgo.ApplicationCommandOptionInteger, Name: "limit", Description: "How many to show (max 20)", Required: false},
					},
				},
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "model",
					Description: "Full details for one model.",
					Options: []*discordgo.ApplicationCommandOption{
						{Type: discordgo.ApplicationCommandOptionString, Name: "slug", Description: "Model id, e.g. openai/gpt-5.6-sol", Required: true},
					},
				},
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "compare",
					Description: "Compare two models side by side.",
					Options: []*discordgo.ApplicationCommandOption{
						{Type: discordgo.ApplicationCommandOptionString, Name: "model_a", Description: "First model id", Required: true},
						{Type: discordgo.ApplicationCommandOptionString, Name: "model_b", Description: "Second model id", Required: true},
					},
				},
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "endpoints",
					Description: "List providers serving a model, with per-endpoint pricing and latency.",
					Options: []*discordgo.ApplicationCommandOption{
						{Type: discordgo.ApplicationCommandOptionString, Name: "slug", Description: "Model id", Required: true},
					},
				},
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "providers",
					Description: "List all upstream OpenRouter providers.",
				},
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "price",
					Description: "Show a model's price and estimate a cost.",
					Options: []*discordgo.ApplicationCommandOption{
						{Type: discordgo.ApplicationCommandOptionString, Name: "slug", Description: "Model id", Required: true},
						{Type: discordgo.ApplicationCommandOptionInteger, Name: "in_tokens", Description: "Input tokens to estimate (default 1000)", Required: false},
						{Type: discordgo.ApplicationCommandOptionInteger, Name: "out_tokens", Description: "Output tokens to estimate (default 500)", Required: false},
					},
				},

				// Account & Tracking (5)
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "key",
					Description: "Show live usage, limits, and free-model quota for your API key.",
				},
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "gen",
					Description: "Audit one generation's cost and latency by id.",
					Options: []*discordgo.ApplicationCommandOption{
						{Type: discordgo.ApplicationCommandOptionString, Name: "id", Description: "Generation id, e.g. gen-1234567890", Required: true},
					},
				},
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "analytics",
					Description: "Show request counts by model for recent days.",
					Options: []*discordgo.ApplicationCommandOption{
						{Type: discordgo.ApplicationCommandOptionInteger, Name: "days", Description: "Number of previous days to include (1-90, default 3)", Required: false},
					},
				},
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "usage",
					Description: "Spend dashboard: today, week, month, lifetime.",
				},
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "ask",
					Description: "Ask any OpenRouter model; the reply shows the real cost.",
					Options: []*discordgo.ApplicationCommandOption{
						{Type: discordgo.ApplicationCommandOptionString, Name: "prompt", Description: "Your prompt", Required: true},
						{Type: discordgo.ApplicationCommandOptionString, Name: "model", Description: "Model id (default: openrouter/auto)", Required: false},
						{Type: discordgo.ApplicationCommandOptionInteger, Name: "max_tokens", Description: "Max output tokens (default 1024)", Required: false},
						{Type: discordgo.ApplicationCommandOptionNumber, Name: "temperature", Description: "Sampling temperature (0-2)", Required: false},
					},
				},
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "help",
					Description: "Show the /openrouter interactive usage guide.",
				},

				// Routing (7): route · fallback · free · online · think · fusion · tools
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "route",
					Description: "Route prompt across providers with sort preferences (price/latency/throughput).",
					Options: []*discordgo.ApplicationCommandOption{
						{Type: discordgo.ApplicationCommandOptionString, Name: "prompt", Description: "Your prompt", Required: false},
						{
							Type:        discordgo.ApplicationCommandOptionString,
							Name:        "sort",
							Description: "Provider priority sort",
							Required:    false,
							Choices: []*discordgo.ApplicationCommandOptionChoice{
								{Name: "Lowest price", Value: "price"},
								{Name: "Lowest latency", Value: "latency"},
								{Name: "Highest throughput", Value: "throughput"},
							},
						},
						{Type: discordgo.ApplicationCommandOptionString, Name: "model", Description: "Model id (default: openrouter/auto)", Required: false},
					},
				},
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "fallback",
					Description: "Failover prompt across an ordered list of backup models.",
					Options: []*discordgo.ApplicationCommandOption{
						{Type: discordgo.ApplicationCommandOptionString, Name: "prompt", Description: "Your prompt", Required: false},
						{Type: discordgo.ApplicationCommandOptionString, Name: "models", Description: "Comma-separated model IDs in order of priority", Required: false},
					},
				},
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "free",
					Description: "Chat via the zero-cost models router (openrouter/free).",
					Options: []*discordgo.ApplicationCommandOption{
						{Type: discordgo.ApplicationCommandOptionString, Name: "prompt", Description: "Your prompt", Required: true},
						{Type: discordgo.ApplicationCommandOptionInteger, Name: "max_tokens", Description: "Max output tokens (default 1024)", Required: false},
					},
				},
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "online",
					Description: "Chat with web search-augmented answers (:online).",
					Options: []*discordgo.ApplicationCommandOption{
						{Type: discordgo.ApplicationCommandOptionString, Name: "prompt", Description: "Your query", Required: true},
						{Type: discordgo.ApplicationCommandOptionString, Name: "model", Description: "Model id (default: openrouter/auto)", Required: false},
						{Type: discordgo.ApplicationCommandOptionInteger, Name: "max_tokens", Description: "Max output tokens (default 1024)", Required: false},
					},
				},
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "think",
					Description: "Chat with reasoning/thinking models with explicit reasoning budget.",
					Options: []*discordgo.ApplicationCommandOption{
						{Type: discordgo.ApplicationCommandOptionString, Name: "prompt", Description: "Your reasoning prompt", Required: true},
						{Type: discordgo.ApplicationCommandOptionString, Name: "model", Description: "Model id (default: openrouter/auto)", Required: false},
						{Type: discordgo.ApplicationCommandOptionInteger, Name: "reasoning_tokens", Description: "Max reasoning tokens (default 2048)", Required: false},
						{Type: discordgo.ApplicationCommandOptionInteger, Name: "max_tokens", Description: "Max output tokens (default 2048)", Required: false},
					},
				}, {
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "tools",
					Description: "List models that support native function and tool calling.",
				},

				// Retrieval (3): embed · similar · rerank
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "embed",
					Description: "Generate vector embeddings for input text.",
					Options: []*discordgo.ApplicationCommandOption{
						{Type: discordgo.ApplicationCommandOptionString, Name: "input", Description: "Text to generate embedding for", Required: true},
						{Type: discordgo.ApplicationCommandOptionString, Name: "model", Description: "Embedding model (default: openai/text-embedding-3-small)", Required: false},
						{Type: discordgo.ApplicationCommandOptionInteger, Name: "dimensions", Description: "Target dimensions (optional)", Required: false},
					},
				}, {
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "rerank",
					Description: "Rerank candidate documents against a query by relevance score.",
					Options: []*discordgo.ApplicationCommandOption{
						{Type: discordgo.ApplicationCommandOptionString, Name: "query", Description: "Search query", Required: true},
						{Type: discordgo.ApplicationCommandOptionString, Name: "documents", Description: "Candidate documents separated by |", Required: true},
						{Type: discordgo.ApplicationCommandOptionString, Name: "model", Description: "Rerank model (default: cohere/rerank-v3.5)", Required: false},
						{Type: discordgo.ApplicationCommandOptionInteger, Name: "top_n", Description: "Number of top results to return (default 5)", Required: false},
					},
				},

				// Media (4): image · video · tts · stt
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "image",
					Description: "Generate an image via OpenRouter image generation models.",
					Options: []*discordgo.ApplicationCommandOption{
						{Type: discordgo.ApplicationCommandOptionString, Name: "prompt", Description: "Image description prompt", Required: true},
						{Type: discordgo.ApplicationCommandOptionString, Name: "model", Description: "Image model (default: bytedance-seed/seedream-5-0-flash)", Required: false},
					},
				},
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "video",
					Description: "Generate video or browse video models in the catalog.",
					Options: []*discordgo.ApplicationCommandOption{
						{Type: discordgo.ApplicationCommandOptionString, Name: "prompt", Description: "Video description prompt", Required: false},
						{Type: discordgo.ApplicationCommandOptionString, Name: "model", Description: "Video model (default: luma/ray-2)", Required: false},
					},
				},
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "tts",
					Description: "Convert text to speech audio (.mp3 attachment).",
					Options: []*discordgo.ApplicationCommandOption{
						{Type: discordgo.ApplicationCommandOptionString, Name: "text", Description: "Text to speak", Required: true},
						{Type: discordgo.ApplicationCommandOptionString, Name: "model", Description: "TTS model (default: hexgrad/kokoro-82m)", Required: false},
						{Type: discordgo.ApplicationCommandOptionString, Name: "voice", Description: "Voice ID (default: af_bella)", Required: false},
					},
				},
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "stt",
					Description: "Transcribe speech audio file or URL to text.",
					Options: []*discordgo.ApplicationCommandOption{
						{Type: discordgo.ApplicationCommandOptionAttachment, Name: "audio", Description: "Upload audio file attachment", Required: false},
						{Type: discordgo.ApplicationCommandOptionString, Name: "audio_url", Description: "Direct audio URL to transcribe", Required: false},
						{Type: discordgo.ApplicationCommandOptionString, Name: "model", Description: "STT model (default: openai/whisper-large-v3)", Required: false},
						{Type: discordgo.ApplicationCommandOptionString, Name: "language", Description: "Spoken language code (e.g. en, es, fr)", Required: false},
					},
				},
			},
		},
		Handler: handleOpenRouterCommand,
	}
}

```

## `./emojigg.go`

```go
package main

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log"
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/bwmarrin/discordgo"
)

const (
	emojiGGPageSize       = 15
	maxGuildEmojiFileSize = 256 << 10
)

var (
	emojiAssetDirectory = func() string { return applicationPath("emojis") }
	emojiAssetNameRE    = regexp.MustCompile(`^[a-zA-Z0-9_]{2,32}$`)
	emojiSourceIDRE     = regexp.MustCompile(`^(\d+)-`)
)

type emojiGGAsset struct {
	ID       string
	Name     string
	FileName string
	Path     string
	Size     int64
}

func loadEmojiGGAssets() ([]emojiGGAsset, error) {
	dir := emojiAssetDirectory()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading emoji asset directory %s: %w", dir, err)
	}

	var assets []emojiGGAsset
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if ext != ".gif" && ext != ".png" {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, fmt.Errorf("reading emoji asset metadata for %s: %w", entry.Name(), err)
		}
		if !info.Mode().IsRegular() {
			continue
		}

		baseName := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
		id := ""
		if match := emojiSourceIDRE.FindStringSubmatch(baseName); match != nil {
			id = match[1]
			baseName = strings.TrimPrefix(baseName, id+"-")
		}
		name := discordEmojiName(baseName)
		assets = append(assets, emojiGGAsset{
			ID:       id,
			Name:     name,
			FileName: entry.Name(),
			Path:     filepath.Join(dir, entry.Name()),
			Size:     info.Size(),
		})
	}
	sort.Slice(assets, func(i, j int) bool {
		if assets[i].Name == assets[j].Name {
			return assets[i].FileName < assets[j].FileName
		}
		return assets[i].Name < assets[j].Name
	})
	return assets, nil
}

func discordEmojiName(name string) string {
	name = strings.ToLower(name)
	var out strings.Builder
	lastUnderscore := false
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_':
			out.WriteRune(r)
			lastUnderscore = r == '_'
		case r == '-' || r == ' ':
			if out.Len() > 0 && !lastUnderscore {
				out.WriteByte('_')
				lastUnderscore = true
			}
		}
	}
	return strings.Trim(out.String(), "_")
}

func searchEmojiGGAssets(assets []emojiGGAsset, query string) []emojiGGAsset {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return nil
	}
	var matches []emojiGGAsset
	for _, asset := range assets {
		if strings.Contains(asset.Name, query) || (asset.ID != "" && strings.Contains(asset.ID, query)) {
			matches = append(matches, asset)
		}
	}
	return matches
}

func findEmojiGGAsset(assets []emojiGGAsset, selector string) (emojiGGAsset, error) {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return emojiGGAsset{}, fmt.Errorf("provide an emoji name or emoji.gg asset ID")
	}
	var matches []emojiGGAsset
	for _, asset := range assets {
		if asset.ID == selector || strings.EqualFold(asset.Name, selector) || strings.EqualFold(asset.FileName, selector) {
			matches = append(matches, asset)
		}
	}
	if len(matches) == 0 {
		return emojiGGAsset{}, fmt.Errorf("no emoji asset found for %q; use `/emojigg search` to find an asset", selector)
	}
	if len(matches) > 1 {
		var ids []string
		for _, asset := range matches {
			ids = append(ids, asset.ID)
		}
		return emojiGGAsset{}, fmt.Errorf("more than one asset is named `%s`; use an asset ID instead: %s", selector, strings.Join(ids, ", "))
	}
	return matches[0], nil
}

func emojiGGCreateParams(asset emojiGGAsset, requestedName string) (*discordgo.EmojiParams, error) {
	name := asset.Name
	if requestedName != "" {
		name = strings.TrimSpace(requestedName)
	}
	if !emojiAssetNameRE.MatchString(name) {
		return nil, fmt.Errorf("emoji name must contain 2–32 letters, numbers, or underscores; provide a valid `name` option")
	}
	if emojiGGAssetTooLarge(asset) {
		return nil, fmt.Errorf("this asset is too large; Discord requires the encoded emoji image to be smaller than %d bytes", maxGuildEmojiFileSize)
	}
	data, err := os.ReadFile(asset.Path)
	if err != nil {
		return nil, fmt.Errorf("reading emoji asset: %w", err)
	}
	if len(data) != int(asset.Size) {
		return nil, fmt.Errorf("emoji asset changed while being read; try again")
	}
	contentType := "image/gif"
	if strings.EqualFold(filepath.Ext(asset.FileName), ".png") {
		contentType = "image/png"
	}
	image := "data:" + contentType + ";base64," + base64.StdEncoding.EncodeToString(data)
	if len(image) >= maxGuildEmojiFileSize {
		return nil, fmt.Errorf("this asset is too large; Discord requires the encoded emoji image to be smaller than %d bytes", maxGuildEmojiFileSize)
	}
	return &discordgo.EmojiParams{
		Name:  name,
		Image: image,
	}, nil
}

func emojiGGAssetTooLarge(asset emojiGGAsset) bool {
	if asset.Size < 0 || asset.Size >= maxGuildEmojiFileSize {
		return true
	}
	contentType := "image/gif"
	if strings.EqualFold(filepath.Ext(asset.FileName), ".png") {
		contentType = "image/png"
	}
	prefixBytes := int64(len("data:" + contentType + ";base64,"))
	encodedBytes := ((asset.Size + 2) / 3 * 4) + prefixBytes
	return encodedBytes >= maxGuildEmojiFileSize
}

func canManageGuildEmojis(i *discordgo.InteractionCreate) bool {
	if i == nil || i.Member == nil {
		return false
	}
	required := int64(discordgo.PermissionManageGuildExpressions | discordgo.PermissionAdministrator)
	return i.Member.Permissions&required != 0
}

func handleEmojiGG(s *discordgo.Session, i *discordgo.InteractionCreate) {
	options := i.ApplicationCommandData().Options
	if len(options) == 0 {
		respondEmojiGG(s, i, "Choose `search`, `list`, `random`, `preview`, or `add`.")
		return
	}
	subcommand := options[0]

	assets, err := loadEmojiGGAssets()
	if err != nil {
		log.Printf("[emojigg] %v", err)
		respondEmojiGG(s, i, "❌ Emoji assets are unavailable. Check the bot's `emojis` folder.")
		return
	}
	if len(assets) == 0 {
		respondEmojiGG(s, i, "❌ No supported GIF or PNG emoji assets were found.")
		return
	}

	switch subcommand.Name {
	case "search":
		query := stringOptionValue(subcommand.Options, "query")
		matches := searchEmojiGGAssets(assets, query)
		if len(matches) == 0 {
			respondEmojiGG(s, i, fmt.Sprintf("No emoji assets found matching %q.", query))
			return
		}
		page := intOptionValue(subcommand.Options, "page", 1)
		respondEmojiGGAssetPage(s, i, matches, page, query, false)
	case "list":
		page := intOptionValue(subcommand.Options, "page", 1)
		respondEmojiGGAssetPage(s, i, assets, page, "", false)
	case "random":
		index, err := rand.Int(rand.Reader, big.NewInt(int64(len(assets))))
		if err != nil {
			log.Printf("[emojigg] selecting random emoji asset: %v", err)
			respondEmojiGG(s, i, "❌ Could not select a random emoji. Please try again.")
			return
		}
		previewEmojiGGAsset(s, i, assets[index.Int64()], true)
	case "preview":
		asset, err := findEmojiGGAsset(assets, stringOptionValue(subcommand.Options, "asset"))
		if err != nil {
			respondEmojiGG(s, i, "❌ "+err.Error())
			return
		}
		previewEmojiGGAsset(s, i, asset, false)
	case "add":
		if !canManageGuildEmojis(i) {
			respondEmojiGG(s, i, "❌ You need **Manage Expressions** or **Administrator** permission to add server emojis.")
			return
		}
		asset, err := findEmojiGGAsset(assets, stringOptionValue(subcommand.Options, "asset"))
		if err != nil {
			respondEmojiGG(s, i, "❌ "+err.Error())
			return
		}
		requestedName := stringOptionValue(subcommand.Options, "name")
		params, err := emojiGGCreateParams(asset, requestedName)
		if err != nil {
			respondEmojiGG(s, i, "❌ "+err.Error())
			return
		}

		if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{Flags: discordgo.MessageFlagsEphemeral},
		}); err != nil {
			log.Printf("[emojigg] deferring add response: %v", err)
			return
		}
		existing, err := s.GuildEmojis(i.GuildID)
		if err != nil {
			log.Printf("[emojigg] checking existing guild emojis: %v", err)
			followupEmojiGG(s, i, "❌ Could not check the server's existing emojis. Please try again.")
			return
		}
		for _, guildEmoji := range existing {
			if strings.EqualFold(guildEmoji.Name, params.Name) {
				followupEmojiGG(s, i, fmt.Sprintf("ℹ️ The server already has an emoji named `%s`.", params.Name))
				return
			}
		}
		emoji, err := s.GuildEmojiCreate(i.GuildID, params)
		if err != nil {
			log.Printf("[emojigg] adding asset %s as %s in guild %s: %v", asset.FileName, params.Name, i.GuildID, err)
			followupEmojiGG(s, i, fmt.Sprintf("❌ Discord could not add `%s`. Check the bot's Manage Expressions permission, emoji slot availability, and whether the file meets server limits. Error: %v", params.Name, err))
			return
		}
		followupEmojiGG(s, i, fmt.Sprintf("✅ Added %s (`%s`) to the server.", emoji.MessageFormat(), emoji.Name))
	default:
		respondEmojiGG(s, i, "Choose `search`, `list`, `random`, `preview`, or `add`.")
	}
}

func previewEmojiGGAsset(s *discordgo.Session, i *discordgo.InteractionCreate, asset emojiGGAsset, random bool) {
	file, err := os.Open(asset.Path)
	if err != nil {
		log.Printf("[emojigg] opening emoji asset %s: %v", asset.FileName, err)
		respondEmojiGG(s, i, "❌ Could not read that emoji asset. Please try again.")
		return
	}
	defer file.Close()

	selector := asset.ID
	if selector == "" {
		selector = asset.FileName
	}
	prefix := "👀"
	if random {
		prefix = "🎲"
	}
	if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: fmt.Sprintf("%s **%s** · asset `%s`\nUse `/emojigg add asset:%s` to add it to the server.", prefix, asset.Name, selector, selector),
			Files:   []*discordgo.File{{Name: asset.FileName, Reader: file}},
			Flags:   discordgo.MessageFlagsEphemeral,
		},
	}); err != nil {
		log.Printf("[emojigg] responding with emoji asset preview %s: %v", asset.FileName, err)
	}
}

func formatEmojiGGAssetPage(assets []emojiGGAsset, page, total int) string {
	pageCount := (total + emojiGGPageSize - 1) / emojiGGPageSize
	if page < 1 || page > pageCount {
		return fmt.Sprintf("Page must be between 1 and %d.", pageCount)
	}
	start := (page - 1) * emojiGGPageSize
	end := start + emojiGGPageSize
	if end > total {
		end = total
	}
	var lines []string
	for _, asset := range assets[start:end] {
		sizeNote := ""
		if emojiGGAssetTooLarge(asset) {
			sizeNote = " · too large to add"
		}
		id := asset.ID
		if id == "" {
			id = asset.FileName
		}
		lines = append(lines, fmt.Sprintf("• `%s` · asset `%s`%s", asset.Name, id, sizeNote))
	}
	return fmt.Sprintf("**Emoji.gg assets (%d total) — page %d/%d**\n%s\n\nUse `/emojigg add asset:<name-or-ID>` to add one.", total, page, pageCount, strings.Join(lines, "\n"))
}

func emojiGGPageCustomID(page int, query string) string {
	encodedQuery := base64.RawURLEncoding.EncodeToString([]byte(query))
	return fmt.Sprintf("emojigg:page:%d:%s", page, encodedQuery)
}

func emojiGGPageComponents(page, pageCount int, query string) []discordgo.MessageComponent {
	if pageCount <= 1 {
		return nil
	}
	return []discordgo.MessageComponent{
		discordgo.ActionsRow{Components: []discordgo.MessageComponent{
			discordgo.Button{Label: "Previous", Style: discordgo.PrimaryButton, CustomID: emojiGGPageCustomID(page-1, query), Disabled: page <= 1},
			discordgo.Button{Label: fmt.Sprintf("%d / %d", page, pageCount), Style: discordgo.SecondaryButton, CustomID: "emojigg:page:noop:", Disabled: true},
			discordgo.Button{Label: "Next", Style: discordgo.PrimaryButton, CustomID: emojiGGPageCustomID(page+1, query), Disabled: page >= pageCount},
		}},
	}
}

func respondEmojiGGAssetPage(s *discordgo.Session, i *discordgo.InteractionCreate, assets []emojiGGAsset, page int, query string, update bool) {
	pageCount := (len(assets) + emojiGGPageSize - 1) / emojiGGPageSize
	content := formatEmojiGGAssetPage(assets, page, len(assets))
	data := &discordgo.InteractionResponseData{
		Content:    content,
		Components: emojiGGPageComponents(page, pageCount, query),
	}
	responseType := discordgo.InteractionResponseChannelMessageWithSource
	if update {
		responseType = discordgo.InteractionResponseUpdateMessage
	} else {
		data.Flags = discordgo.MessageFlagsEphemeral
	}
	if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: responseType,
		Data: data,
	}); err != nil {
		log.Printf("[emojigg] rendering asset page %d: %v", page, err)
	}
}

func handleEmojiGGPageComponent(s *discordgo.Session, i *discordgo.InteractionCreate) {
	parts := strings.Split(i.MessageComponentData().CustomID, ":")
	if len(parts) != 4 || parts[0] != "emojigg" || parts[1] != "page" {
		respondEmojiGG(s, i, "❌ This emoji page control is invalid. Run `/emojigg list` again.")
		return
	}
	page, err := strconv.Atoi(parts[2])
	if err != nil || page < 1 {
		respondEmojiGG(s, i, "❌ This emoji page control is invalid. Run `/emojigg list` again.")
		return
	}
	queryBytes, err := base64.RawURLEncoding.DecodeString(parts[3])
	if err != nil {
		respondEmojiGG(s, i, "❌ This emoji page control is invalid. Run `/emojigg list` again.")
		return
	}
	query := string(queryBytes)
	assets, err := loadEmojiGGAssets()
	if err != nil {
		log.Printf("[emojigg] loading assets for page interaction: %v", err)
		respondEmojiGG(s, i, "❌ Emoji assets are unavailable. Check the bot's `emojis` folder.")
		return
	}
	if query != "" {
		assets = searchEmojiGGAssets(assets, query)
	}
	if len(assets) == 0 {
		respondEmojiGG(s, i, "No emoji assets were found for this page. Run `/emojigg list` again.")
		return
	}
	pageCount := (len(assets) + emojiGGPageSize - 1) / emojiGGPageSize
	if page > pageCount {
		page = pageCount
	}
	respondEmojiGGAssetPage(s, i, assets, page, query, true)
}

func stringOptionValue(options []*discordgo.ApplicationCommandInteractionDataOption, name string) string {
	for _, option := range options {
		if option.Name == name {
			return option.StringValue()
		}
	}
	return ""
}

func intOptionValue(options []*discordgo.ApplicationCommandInteractionDataOption, name string, fallback int) int {
	for _, option := range options {
		if option.Name == name {
			return int(option.IntValue())
		}
	}
	return fallback
}

func respondEmojiGG(s *discordgo.Session, i *discordgo.InteractionCreate, content string) {
	if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: content,
			Flags:   discordgo.MessageFlagsEphemeral,
		},
	}); err != nil {
		log.Printf("[emojigg] responding to command: %v", err)
	}
}

func followupEmojiGG(s *discordgo.Session, i *discordgo.InteractionCreate, content string) {
	if _, err := s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
		Content: content,
		Flags:   discordgo.MessageFlagsEphemeral,
	}); err != nil {
		log.Printf("[emojigg] sending follow-up: %v", err)
	}
}

```

## `./searchlogs.go`

```go
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
```

## `./sysinfo_windows.go`

```go
//go:build windows

package main

import (
	"fmt"
	"syscall"
	"unsafe"
)

var (
	kernel32            = syscall.NewLazyDLL("kernel32.dll")
	procGetDiskFreeSpace = kernel32.NewProc("GetDiskFreeSpaceExW")
)

// getDiskInfo checks the disk space of the current system drive on Windows
func getDiskInfo() string {
	var freeBytesAvailable, totalNumberOfBytes, totalNumberOfFreeBytes int64

	rootPath, err := syscall.UTF16PtrFromString("C:\\")
	if err != nil {
		return "Unknown"
	}

	r1, _, _ := procGetDiskFreeSpace.Call(
		uintptr(unsafe.Pointer(rootPath)),
		uintptr(unsafe.Pointer(&freeBytesAvailable)),
		uintptr(unsafe.Pointer(&totalNumberOfBytes)),
		uintptr(unsafe.Pointer(&totalNumberOfFreeBytes)),
	)
	if r1 == 0 {
		return "Unknown"
	}

	total := float64(totalNumberOfBytes) / (1024 * 1024 * 1024)
	free := float64(freeBytesAvailable) / (1024 * 1024 * 1024)
	used := total - free
	var percent float64
	if total > 0 {
		percent = (used / total) * 100
	}

	return fmt.Sprintf("Total: %.2f GB\nFree: %.2f GB\n*(%.1f%% Used)*", total, free, percent)
}

```

## `./.gitignore`

```text
# Custom
/.private/
/.logs/
*.asc

# Local runtime data & caches
/Data/

# Compiled binaries
/Sunfire
/push.sh.log

```

## `./sysinfo_unix.go`

```go
//go:build !windows

package main

import (
	"fmt"
	"syscall"
)

// getDiskInfo uses syscall to check the root file system usage on unix
func getDiskInfo() string {
	var stat syscall.Statfs_t
	if err := syscall.Statfs("/", &stat); err != nil {
		return "Unknown"
	}

	total := float64(stat.Blocks) * float64(stat.Bsize) / (1024 * 1024 * 1024)
	free := float64(stat.Bavail) * float64(stat.Bsize) / (1024 * 1024 * 1024)
	used := total - free
	var percent float64
	if total > 0 {
		percent = (used / total) * 100
	}

	return fmt.Sprintf("Total: %.2f GB\nFree: %.2f GB\n*(%.1f%% Used)*", total, free, percent)
}

```

## `./fallen_star.go`

```go
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

type StoryChoice struct {
	Text     string                 `json:"text"`
	NextNode string                 `json:"nextNode"`
	Effects  map[string]interface{} `json:"effects"`
}

type StoryNode struct {
	ID       string        `json:"id"`
	Title    string        `json:"title"`
	Content  string        `json:"content"`
	Image    string        `json:"image"`
	Choices  []StoryChoice `json:"choices"`
	IsEnding bool          `json:"isEnding"`
}

type FallenStarGame struct {
	UserID      string                 `json:"userId"`
	GuildID     string                 `json:"guildId"`
	ChannelID   string                 `json:"channelId"`
	CurrentNode string                 `json:"currentNode"`
	Status      string                 `json:"status"` // active, won, lost
	StartedAt   int64                  `json:"startedAt"`
	EndedAt     int64                  `json:"endedAt,omitempty"`
	Stats       map[string]int         `json:"stats"`
	Inventory   []string               `json:"inventory"`
	Metadata    map[string]interface{} `json:"metadata"`
}

var (
	fsMu          sync.Mutex
	fallenStory   map[string]StoryNode
	activeFSGames map[string]*FallenStarGame // userID -> game
	fsGamesPath   string
)

func initFallenStar() {
	if len(fallenStory) > 0 {
		return
	}
	storyPath := dataPath("fallenStarStory.json")
	sData, err := os.ReadFile(storyPath)
	if err == nil {
		_ = json.Unmarshal(sData, &fallenStory)
	}

	fsGamesPath = dataPath("fallenStarGames.json")
	activeFSGames = make(map[string]*FallenStarGame)
	gData, err := os.ReadFile(fsGamesPath)
	if err == nil {
		_ = json.Unmarshal(gData, &activeFSGames)
	}
}

func saveFallenStarGames() {
	if fsGamesPath == "" {
		return
	}
	data, err := json.MarshalIndent(activeFSGames, "", "  ")
	if err == nil {
		_ = os.WriteFile(fsGamesPath, data, 0644)
	}
}

func handleFallenStarCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	initFallenStar()
	fsMu.Lock()
	defer fsMu.Unlock()

	options := i.ApplicationCommandData().Options
	if len(options) == 0 {
		return
	}

	sub := options[0]
	uID := i.Member.User.ID

	switch sub.Name {
	case "start":
		if g, ok := activeFSGames[uID]; ok && g.Status == "active" {
			_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content: fmt.Sprintf("⚠️ You already have an active adventure in node `%s`! Use `/fallen-star status` to continue.", g.CurrentNode),
					Flags:   discordgo.MessageFlagsEphemeral,
				},
			})
			return
		}

		game := &FallenStarGame{
			UserID:      uID,
			GuildID:     i.GuildID,
			ChannelID:   i.ChannelID,
			CurrentNode: "intro",
			Status:      "active",
			StartedAt:   time.Now().UnixMilli(),
			Stats: map[string]int{
				"sanity":        100,
				"corruption":    0,
				"magicAffinity": 0,
			},
			Inventory: []string{},
			Metadata: map[string]interface{}{
				"characterName": i.Member.User.Username,
			},
		}
		activeFSGames[uID] = game
		saveFallenStarGames()

		node, exists := fallenStory["intro"]
		if !exists {
			_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{Content: "❌ Story introductory node missing from Data/fallenStarStory.json."},
			})
			return
		}

		sendStoryNodeInteraction(s, i, node, game)

	case "status":
		game, ok := activeFSGames[uID]
		if !ok || game.Status != "active" {
			_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content: "You don't have an active adventure right now. Use `/fallen-star start` to begin!",
					Flags:   discordgo.MessageFlagsEphemeral,
				},
			})
			return
		}

		node, ok := fallenStory[game.CurrentNode]
		if !ok {
			node = fallenStory["intro"]
		}
		sendStoryNodeInteraction(s, i, node, game)
	}
}

func sendStoryNodeInteraction(s *discordgo.Session, i *discordgo.InteractionCreate, node StoryNode, game *FallenStarGame) {
	embed := &discordgo.MessageEmbed{
		Title:       node.Title,
		Description: node.Content,
		Color:       0x4A0080, // Dark purple
		Footer: &discordgo.MessageEmbedFooter{
			Text: fmt.Sprintf("Sanity: %d/100 | Corruption: %d/100 | Magic: %d/100", game.Stats["sanity"], game.Stats["corruption"], game.Stats["magicAffinity"]),
		},
	}

	var rows []discordgo.MessageComponent
	if len(node.Choices) > 0 {
		var buttons []discordgo.MessageComponent
		for cIdx, ch := range node.Choices {
			label := ch.Text
			if len(label) > 80 {
				label = label[:77] + "..."
			}

			btn := discordgo.Button{
				CustomID: fmt.Sprintf("fs:%s:%s", game.UserID, ch.NextNode),
				Label:    label,
				Style:    discordgo.SecondaryButton,
			}
			buttons = append(buttons, btn)
			if len(buttons) >= 5 || cIdx == len(node.Choices)-1 {
				rows = append(rows, discordgo.ActionsRow{Components: buttons})
				buttons = nil
			}
		}
	}

	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Embeds:     []*discordgo.MessageEmbed{embed},
			Components: rows,
		},
	})
}

func handleFallenStarComponent(s *discordgo.Session, i *discordgo.InteractionCreate) {
	initFallenStar()
	fsMu.Lock()
	defer fsMu.Unlock()

	parts := strings.Split(i.MessageComponentData().CustomID, ":")
	if len(parts) != 3 {
		return
	}

	ownerID := parts[1]
	targetNodeID := parts[2]

	if i.Member.User.ID != ownerID {
		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "❌ This is not your adventure!",
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
		return
	}

	game, ok := activeFSGames[ownerID]
	if !ok || game.Status != "active" {
		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "No active adventure found. Use `/fallen-star start`!",
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
		return
	}

	node, ok := fallenStory[targetNodeID]
	if !ok {
		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{Content: "❌ Path leads into the unknown void (node missing)."},
		})
		return
	}

	game.CurrentNode = targetNodeID
	if node.IsEnding {
		game.Status = "ended"
		game.EndedAt = time.Now().UnixMilli()
	}
	saveFallenStarGames()

	embed := &discordgo.MessageEmbed{
		Title:       node.Title,
		Description: node.Content,
		Color:       0x4A0080,
		Footer: &discordgo.MessageEmbedFooter{
			Text: fmt.Sprintf("Sanity: %d/100 | Corruption: %d/100 | Magic: %d/100", game.Stats["sanity"], game.Stats["corruption"], game.Stats["magicAffinity"]),
		},
	}

	var rows []discordgo.MessageComponent
	if len(node.Choices) > 0 {
		var buttons []discordgo.MessageComponent
		for cIdx, ch := range node.Choices {
			label := ch.Text
			if len(label) > 80 {
				label = label[:77] + "..."
			}

			btn := discordgo.Button{
				CustomID: fmt.Sprintf("fs:%s:%s", game.UserID, ch.NextNode),
				Label:    label,
				Style:    discordgo.SecondaryButton,
			}
			buttons = append(buttons, btn)
			if len(buttons) >= 5 || cIdx == len(node.Choices)-1 {
				rows = append(rows, discordgo.ActionsRow{Components: buttons})
				buttons = nil
			}
		}
	}

	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseUpdateMessage,
		Data: &discordgo.InteractionResponseData{
			Embeds:     []*discordgo.MessageEmbed{embed},
			Components: rows,
		},
	})
}

```

## `./notifier_hermes.go`

```go
// notifier_hermes.go — Hermes tips fetching, storage, deduplication, and scheduled posting.
// Fetches tips from docs/llms.txt and GitHub releases every 24 hours, updates Data/hermes_tips.json,
// and posts any newly discovered tips to the Hermes Tips channel (1555337100273123408)
// rate-limited to 3 tips every 3 seconds to avoid overflowing the Discord API.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
)

// HermesTipsChannelID is the fallback or test-overridden Discord channel for Hermes tips.
var HermesTipsChannelID string

// getHermesTipsChannelID returns the configured Discord channel for Hermes tips from config, or HermesTipsChannelID.
func getHermesTipsChannelID() string {
	cfg, err := loadConfig()
	if err == nil && cfg != nil && strings.TrimSpace(cfg.HermesTipsChannelID) != "" {
		return strings.TrimSpace(cfg.HermesTipsChannelID)
	}
	return strings.TrimSpace(HermesTipsChannelID)
}

// HermesTip represents the structure of each tip in hermes_tips.json.
type HermesTip struct {
	Source string `json:"source"`
	Text   string `json:"tip"`
}

// HermesTipState stores the known tips and history of posted hashes to guarantee no duplicates.
type HermesTipState struct {
	Tips       []HermesTip `json:"tips"`
	PostedKeys []string    `json:"postedKeys"`
}

// HermesGithubRelease represents the GitHub API release payload.
type HermesGithubRelease struct {
	Name string `json:"name"`
	Body string `json:"body"`
}

var (
	hermesMu             sync.Mutex
	hermesState          HermesTipState
	hermesJSONPath       string
	hermesRateLimitDelay = 3 * time.Second
	hermesHTTPTimeout    = 15 * time.Minute
)

func hermesTipsFilePath() string {
	if hermesJSONPath != "" {
		return hermesJSONPath
	}
	return dataPath("hermes_tips.json")
}

func initHermesTipsState() error {
	path := hermesTipsFilePath()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("creating Hermes state directory: %w", err)
	}

	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("reading Hermes state %s: %w", path, err)
	}
	if err == nil {
		var st HermesTipState
		if stateErr := json.Unmarshal(data, &st); stateErr == nil {
			hermesState = st
			return nil
		}
		var tips []HermesTip
		if legacyErr := json.Unmarshal(data, &tips); legacyErr != nil {
			return fmt.Errorf("parsing Hermes state %s: %w", path, legacyErr)
		}
		hermesState = HermesTipState{Tips: tips}
		for _, t := range tips {
			hermesState.PostedKeys = append(hermesState.PostedKeys, hashHermesTip(t))
		}
		return nil
	}

	hermesState = HermesTipState{
		Tips:       []HermesTip{},
		PostedKeys: []string{},
	}
	return nil
}

func saveHermesTipsState() error {
	path := hermesTipsFilePath()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("creating Hermes state directory: %w", err)
	}
	data, err := json.MarshalIndent(hermesState, "", "    ")
	if err != nil {
		return fmt.Errorf("encoding Hermes state: %w", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("writing Hermes state %s: %w", path, err)
	}
	return nil
}

func hashHermesTip(t HermesTip) string {
	norm := strings.TrimSpace(t.Source) + "|" + strings.TrimSpace(t.Text)
	h := sha256.Sum256([]byte(norm))
	return hex.EncodeToString(h[:])
}

func fetchHermesDocsTips() []HermesTip {
	var tips []HermesTip
	client := notifierClient(hermesHTTPTimeout)

	resp, err := client.Get("https://hermes-agent.nousresearch.com/docs/llms.txt")
	if err != nil {
		log.Printf("[Hermes] Error fetching docs: %v\n", err)
		return tips
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		if err != nil {
			log.Printf("[Hermes] Error reading docs response: %v", err)
			return tips
		}
		lines := strings.Split(string(bodyBytes), "\n")
		for _, line := range lines {
			// Grab markdown bullet points that look like "- [Topic](link): Description"
			if strings.HasPrefix(line, "- [") && strings.Contains(line, ":") {
				tips = append(tips, HermesTip{
					Source: "Hermes Docs",
					Text:   strings.TrimSpace(line),
				})
			}
		}
	} else {
		log.Printf("[Hermes] Docs returned HTTP %d", resp.StatusCode)
	}
	return tips
}

func fetchHermesGithubTips() []HermesTip {
	var tips []HermesTip
	req, err := http.NewRequest("GET", "https://api.github.com/repos/NousResearch/hermes-agent/releases", nil)
	if err != nil {
		log.Printf("[Hermes] Error creating GitHub request: %v\n", err)
		return tips
	}
	req.Header.Set("User-Agent", "Sunfire-Hermes-Tips-Scraper/1.0")

	client := notifierClient(hermesHTTPTimeout)
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("[Hermes] Error fetching GitHub releases: %v\n", err)
		return tips
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		var releases []HermesGithubRelease
		if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
			log.Printf("[Hermes] Error decoding GitHub releases JSON: %v\n", err)
			return tips
		}

		reUser := regexp.MustCompile(`(?i) by @[\w-]+`)
		rePR1 := regexp.MustCompile(`(?i) in #\d+`)
		rePR2 := regexp.MustCompile(`(?i)\(#\d+\)`)

		limit := 10
		if len(releases) < limit {
			limit = len(releases)
		}

		for _, release := range releases[:limit] {
			version := release.Name
			if version == "" {
				version = "New Release"
			}

			lines := strings.Split(release.Body, "\n")
			for _, line := range lines {
				line = strings.TrimSpace(line)

				// Find lines that are bullet points
				if strings.HasPrefix(line, "* ") || strings.HasPrefix(line, "- ") {
					cleanLine := strings.TrimSpace(strings.TrimLeft(line, "*- "))
					cleanLine = reUser.ReplaceAllString(cleanLine, "")
					cleanLine = rePR1.ReplaceAllString(cleanLine, "")
					cleanLine = rePR2.ReplaceAllString(cleanLine, "")

					if len(cleanLine) > 20 && !strings.Contains(cleanLine, "Full Changelog") {
						tips = append(tips, HermesTip{
							Source: fmt.Sprintf("GitHub (%s)", version),
							Text:   strings.TrimSpace(cleanLine),
						})
					}
				}
			}
		}
	} else {
		log.Printf("[Hermes] GitHub releases returned HTTP %d", resp.StatusCode)
	}

	if len(tips) > 20 {
		return tips[:20]
	}
	return tips
}

func collectAllHermesTips() []HermesTip {
	var allTips []HermesTip

	// 1. Manual baseline custom workflow tip
	customWorkflowTip := `# everyday ops
hermes config set model.default deepseek/deepseek-v4-flash
hermes config set agent.reasoning_effort low
hermes gateway restart
- close the terminal

# heavy session
hermes config set model.default deepseek/deepseek-v4-pro
hermes config set agent.reasoning_effort medium
hermes gateway restart
- close the terminal`

	allTips = append(allTips, HermesTip{
		Source: "My Workflows",
		Text:   customWorkflowTip,
	})

	// 2. Dynamic tips from Docs and GitHub releases
	allTips = append(allTips, fetchHermesDocsTips()...)
	allTips = append(allTips, fetchHermesGithubTips()...)
	return allTips
}

// checkHermesTipsUpdates fetches the latest tips, updates the JSON file, and posts newly
// discovered tips in batches of 3 every 3 seconds to avoid Discord API rate-limiting.
func checkHermesTipsUpdates(s *discordgo.Session) (newTipsCount int, err error) {
	hermesMu.Lock()
	defer hermesMu.Unlock()

	if err := initHermesTipsState(); err != nil {
		return 0, err
	}

	fetched := collectAllHermesTips()
	if len(fetched) == 0 {
		return 0, nil
	}

	postedSet := make(map[string]bool)
	for _, k := range hermesState.PostedKeys {
		postedSet[k] = true
	}

	var newTips []HermesTip
	for _, tip := range fetched {
		key := hashHermesTip(tip)
		if !postedSet[key] {
			newTips = append(newTips, tip)
		}
	}

	// Update stored tips with all current tips (merging unique items)
	existingMap := make(map[string]bool)
	for _, t := range hermesState.Tips {
		existingMap[hashHermesTip(t)] = true
	}
	for _, t := range fetched {
		k := hashHermesTip(t)
		if !existingMap[k] {
			hermesState.Tips = append(hermesState.Tips, t)
			existingMap[k] = true
		}
	}
	if err := saveHermesTipsState(); err != nil {
		return 0, err
	}

	if len(newTips) == 0 {
		return 0, nil
	}

	channelID := getHermesTipsChannelID()
	if s != nil && channelID == "" {
		log.Printf("[Hermes] Discovered %d new tips, but hermesTipsChannelId is not configured; gracefully pausing Discord posting", len(newTips))
		return 0, nil
	}
	if channelID != "" {
		log.Printf("[Hermes] Discovered %d new tips to post to channel %s", len(newTips), channelID)
	}

	// Post newly found tips: 3 tips every 3 seconds
	batchSize := 3
	for i := 0; i < len(newTips); i += batchSize {
		end := i + batchSize
		if end > len(newTips) {
			end = len(newTips)
		}

		for j := i; j < end; j++ {
			tip := newTips[j]
			key := hashHermesTip(tip)

			var content string
			if strings.Contains(tip.Text, "\n") {
				content = fmt.Sprintf("💁‍♀️ **Hermes Tip** • *%s*\n```bash\n%s\n```", tip.Source, tip.Text)
			} else {
				content = fmt.Sprintf("💁‍♀️ **Hermes Tip** • *%s*\n> %s", tip.Source, tip.Text)
			}

			if s != nil {
				_, sendErr := sendNotifierMessage(s, channelID, content)
				if sendErr != nil {
					log.Printf("[Hermes] Failed to post tip to Discord: %v", sendErr)
					return newTipsCount, nil
				} else {
					hermesState.PostedKeys = append(hermesState.PostedKeys, key)
					postedSet[key] = true
					if err := saveHermesTipsState(); err != nil {
						hermesState.PostedKeys = hermesState.PostedKeys[:len(hermesState.PostedKeys)-1]
						delete(postedSet, key)
						return newTipsCount, err
					}
					newTipsCount++
				}
			} else {
				hermesState.PostedKeys = append(hermesState.PostedKeys, key)
				postedSet[key] = true
				newTipsCount++
			}
		}

		if s == nil {
			if err := saveHermesTipsState(); err != nil {
				return newTipsCount, err
			}
		}

		// If there are more batches to send, pause 3 seconds before sending next batch
		if end < len(newTips) {
			time.Sleep(hermesRateLimitDelay)
		}
	}

	return newTipsCount, nil
}

// startHermesTipsDaemon runs every 24 hours to fetch and post new tips.
func startHermesTipsDaemon(ctx context.Context, s *discordgo.Session) {
	log.Println("[Hermes] Starting 24h tips daemon...")
	runPeriodic(ctx, 10*time.Second, 24*time.Hour, func() {
		if _, err := checkHermesTipsUpdates(s); err != nil {
			log.Printf("[Hermes] poll pass error: %v", err)
		}
	})
}

```

## `./sysinfo.go`

```go
package main

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
)

func handleSysInfo(s *discordgo.Session, i *discordgo.InteractionCreate) {
	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	})

	// 1. OS Uptime
	uptimeStr := "Unknown"
	if upData, err := os.ReadFile("/proc/uptime"); err == nil {
		parts := strings.Fields(string(upData))
		if len(parts) > 0 {
			if secs, err := strconv.ParseFloat(parts[0], 64); err == nil {
				uptimeStr = (time.Duration(secs) * time.Second).String()
			}
		}
	}

	// 2. Host Memory
	memTotal, memAvail := "Unknown", "Unknown"
	if memData, err := os.ReadFile("/proc/meminfo"); err == nil {
		lines := strings.Split(string(memData), "\n")
		for _, line := range lines {
			if strings.HasPrefix(line, "MemTotal:") {
				memTotal = parseMemLine(line)
			}
			if strings.HasPrefix(line, "MemAvailable:") {
				memAvail = parseMemLine(line)
			}
		}
	}

	// 3. CPU Load Averages (Cleaned up)
	loadAvg := "Unknown"
	if loadData, err := os.ReadFile("/proc/loadavg"); err == nil {
		parts := strings.Fields(string(loadData))
		if len(parts) >= 3 {
			loadAvg = fmt.Sprintf("%s, %s, %s", parts[0], parts[1], parts[2])
		}
	}

	// 4. Bot Process Memory & Goroutines
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)
	botMem := fmt.Sprintf("%.2f MB", float64(memStats.Alloc)/1024/1024)
	goroutines := fmt.Sprintf("%d", runtime.NumGoroutine())

	// Build the Embed
	embed := &discordgo.MessageEmbed{
		Title: "🖥️ Host System Information",
		Color: 0xFF6B35,
		Fields: []*discordgo.MessageEmbedField{
			{Name: "🐧 Operating System", Value: getOSVersion(), Inline: true},
			{Name: "⏱️ OS Uptime", Value: uptimeStr, Inline: true},
			{Name: "⚙️ CPU Info", Value: getCPUInfo(), Inline: true},
			{Name: "🧠 Host Memory", Value: fmt.Sprintf("Total: %s\nAvail: %s", memTotal, memAvail), Inline: true},
			{Name: "💾 Disk Space (Root)", Value: getDiskInfo(), Inline: true},
			{Name: "📈 CPU Load (1, 5, 15m)", Value: loadAvg, Inline: true},
			{Name: "🤖 Bot Process", Value: fmt.Sprintf("RAM: %s\nGoroutines: %s", botMem, goroutines), Inline: true},
			{Name: "🐹 Go Version", Value: runtime.Version(), Inline: true},
			{Name: "🏛️ Architecture", Value: fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH), Inline: true},
		},
	}

	_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
		Embeds: []*discordgo.MessageEmbed{embed},
	})
}

// parseMemLine converts kilobytes from /proc/meminfo into GB
func parseMemLine(line string) string {
	parts := strings.Fields(line)
	if len(parts) >= 2 {
		if kb, err := strconv.ParseFloat(parts[1], 64); err == nil {
			return fmt.Sprintf("%.2f GB", kb/1024/1024)
		}
	}
	return "Unknown"
}

// getOSVersion parses /etc/os-release to find the pretty name (e.g., Ubuntu 24.04 LTS)
func getOSVersion() string {
	data, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return runtime.GOOS
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "PRETTY_NAME=") {
			return strings.Trim(strings.TrimPrefix(line, "PRETTY_NAME="), `"'`)
		}
	}
	return runtime.GOOS
}

// getCPUInfo counts processors and grabs the model name from /proc/cpuinfo
func getCPUInfo() string {
	data, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		return "Unknown CPU"
	}
	model := "Unknown CPU"
	cores := 0
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "model name") && model == "Unknown CPU" {
			parts := strings.Split(line, ":")
			if len(parts) == 2 {
				model = strings.TrimSpace(parts[1])
			}
		}
		if strings.HasPrefix(line, "processor") {
			cores++
		}
	}
	
	// Truncate CPU model if it's absurdly long so it fits neatly in the embed
	if len(model) > 30 {
		model = strings.ReplaceAll(model, "(R)", "")
		model = strings.ReplaceAll(model, "(TM)", "")
		model = strings.ReplaceAll(model, "CPU", "")
		model = strings.TrimSpace(model)
	}
	
	return fmt.Sprintf("%s\n**%d Cores**", model, cores)
}

```

## `./automod_test.go`

```go
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
)

func TestAutoModConfigDefaultsAndPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "automod.json")

	config, err := loadAutoModConfig(path)
	if err != nil {
		t.Fatalf("loadAutoModConfig failed: %v", err)
	}
	if !config.Enabled {
		t.Fatal("AutoMod should be enabled by default")
	}
	if !reflect.DeepEqual(config.BannedWords, []string{"discord.gg/", "http://scam-link"}) {
		t.Fatalf("default banned words = %#v", config.BannedWords)
	}

	if err := updateAutoModConfig(path, func(config *autoModConfig) error {
		config.BannedWords = append(config.BannedWords, "  BAd phrase  ")
		config.Enabled = false
		return nil
	}); err != nil {
		t.Fatalf("updateAutoModConfig failed: %v", err)
	}

	config, err = loadAutoModConfig(path)
	if err != nil {
		t.Fatalf("loading persisted config failed: %v", err)
	}
	if config.Enabled {
		t.Fatal("disabled state was not persisted")
	}
	wantWords := []string{"bad phrase", "discord.gg/", "http://scam-link"}
	if !reflect.DeepEqual(config.BannedWords, wantWords) {
		t.Fatalf("persisted banned words = %#v; want %#v", config.BannedWords, wantWords)
	}
}

func TestAutoModConfigErrorsOnMalformedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "automod.json")
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadAutoModConfig(path); err == nil {
		t.Fatal("expected malformed config to return an error")
	}
}

func TestMatchedBannedWordIsCaseInsensitive(t *testing.T) {
	if got := matchedBannedWord("This contains BAD phrase.", []string{"bad phrase"}); got != "bad phrase" {
		t.Fatalf("matched banned word = %q; want %q", got, "bad phrase")
	}
	if got := matchedBannedWord("This is fine.", []string{"bad phrase"}); got != "" {
		t.Fatal("unexpected match")
	}
}

func TestAutoModEventsPersistAndLoadLatestFirst(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("SUNFIRE_DATA_DIR", dataDir)

	for index := 1; index <= 7; index++ {
		err := recordAutoModEvent("123", "456", autoModEvent{
			Timestamp:      time.Date(2026, time.January, index, 12, 0, 0, 0, time.UTC),
			ChannelID:      "789",
			MessageID:      fmt.Sprintf("message-%d", index),
			MatchedPhrase:  "bad phrase",
			MessageContent: "This is bad phrase.",
			MessageDeleted: index%2 == 0,
		})
		if err != nil {
			t.Fatalf("recordAutoModEvent(%d) failed: %v", index, err)
		}
	}

	events, total, err := loadAutoModEvents("123", "456", 5)
	if err != nil {
		t.Fatalf("loadAutoModEvents failed: %v", err)
	}
	if total != 7 || len(events) != 5 {
		t.Fatalf("loaded %d events (total %d); want 5 of 7", len(events), total)
	}
	if events[0].MessageID != "message-3" || events[4].MessageID != "message-7" {
		t.Fatalf("events are not the latest in chronological order: %#v", events)
	}

	path, err := autoModEventsPath("123", "../456")
	if err == nil || path != "" {
		t.Fatalf("expected invalid user ID to be rejected, got path %q and error %v", path, err)
	}
}

func TestAddWarningPersistsAndRejectsMalformedData(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("SUNFIRE_DATA_DIR", dataDir)

	count, err := addWarning("guild", "member", "moderator", "test warning")
	if err != nil {
		t.Fatalf("addWarning failed: %v", err)
	}
	if count != 1 {
		t.Fatalf("warning count = %d; want 1", count)
	}

	path := filepath.Join(dataDir, "Warnings", "guild.json")
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := addWarning("guild", "member", "moderator", "another warning"); err == nil {
		t.Fatal("expected malformed warning data to return an error")
	}
}

func TestAutoModCommandRegistration(t *testing.T) {
	buildCommandSpecs()
	for _, spec := range commandSpecs {
		if spec.Command.Name != "automod" {
			continue
		}
		if spec.Handler == nil {
			t.Fatal("/automod has no handler")
		}
		subcommands := make(map[string]bool)
		for _, option := range spec.Command.Options {
			subcommands[option.Name] = true
		}
		for _, name := range []string{
			"banned-word-add", "banned-word-remove", "banned-word-list",
			"enable", "disable", "status", "view",
		} {
			if !subcommands[name] {
				t.Errorf("/automod is missing subcommand %q", name)
			}
		}
		if spec.Command.Options[0].Type != discordgo.ApplicationCommandOptionSubCommand {
			t.Fatal("/automod options must be subcommands")
		}
		return
	}
	t.Fatal("/automod is not registered")
}

```

## `./userinfo_test.go`

```go
package main

import (
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"
)

func TestFormatUserPresenceIncludesClientsAndActivity(t *testing.T) {
	got := formatUserPresence(&discordgo.Presence{
		Status: discordgo.StatusOnline,
		ClientStatus: discordgo.ClientStatus{
			Desktop: discordgo.StatusOnline,
			Mobile:  discordgo.StatusIdle,
		},
		Activities: []*discordgo.Activity{
			{
				Type:    discordgo.ActivityTypeGame,
				Name:    "Example Game",
				Details: "In a match",
				State:   "Ranked",
			},
			{
				Type:  discordgo.ActivityTypeCustom,
				State: "At work",
			},
		},
	}, nil)
	for _, want := range []string{"Online", "Desktop: online", "Mobile: idle", "Example Game", "In a match", "Ranked", "At work"} {
		if !strings.Contains(got, want) {
			t.Errorf("formatted presence %q does not contain %q", got, want)
		}
	}
}

func TestFormatUserVoiceStateIncludesVoiceFlags(t *testing.T) {
	got := formatUserVoiceState(&discordgo.Guild{
		VoiceStates: []*discordgo.VoiceState{{
			UserID:     "user-id",
			ChannelID:  "channel-id",
			SelfMute:   true,
			Deaf:       true,
			SelfStream: true,
			SelfVideo:  true,
		}},
	}, "user-id")
	for _, want := range []string{"<#channel-id>", "self-muted", "server-deafened", "streaming", "camera on"} {
		if !strings.Contains(got, want) {
			t.Errorf("formatted voice state %q does not contain %q", got, want)
		}
	}
}

func TestGuildMemberRoleNamesAreOrderedByHierarchy(t *testing.T) {
	member := &discordgo.Member{Roles: []string{"lower", "higher"}}
	guild := &discordgo.Guild{Roles: []*discordgo.Role{
		{ID: "lower", Name: "Lower", Position: 1},
		{ID: "higher", Name: "Higher", Position: 5, Managed: true},
	}}

	got, highest := guildMemberRoleNames(member, guild)
	if highest != "Higher" {
		t.Fatalf("highest role = %q, want Higher", highest)
	}
	if strings.Index(got, "Higher") > strings.Index(got, "Lower") {
		t.Fatalf("roles are not ordered highest-first: %q", got)
	}
	if !strings.Contains(got, "managed") {
		t.Fatalf("managed role marker missing from %q", got)
	}
}

func TestEffectivePermissionNames(t *testing.T) {
	got := effectivePermissionNames(discordgo.PermissionAdministrator | discordgo.PermissionBanMembers)
	if len(got) != 1 || got[0] != "Administrator (all permissions)" {
		t.Fatalf("administrator permissions = %#v", got)
	}

	got = effectivePermissionNames(discordgo.PermissionBanMembers | discordgo.PermissionManageMessages)
	if len(got) != 2 || got[0] != "Manage Messages" || got[1] != "Ban Members" {
		t.Fatalf("selected permissions = %#v", got)
	}
}

func TestGetUserBadgesIncludesAllKnownPublicFlags(t *testing.T) {
	flags := discordgo.UserFlagDiscordEmployee |
		discordgo.UserFlagDiscordPartner |
		discordgo.UserFlagHypeSquadEvents |
		discordgo.UserFlagBugHunterLevel1 |
		discordgo.UserFlagHouseBravery |
		discordgo.UserFlagHouseBrilliance |
		discordgo.UserFlagHouseBalance |
		discordgo.UserFlagEarlySupporter |
		discordgo.UserFlagTeamUser |
		discordgo.UserFlagBugHunterLevel2 |
		discordgo.UserFlagVerifiedBot |
		discordgo.UserFlagVerifiedBotDeveloper |
		discordgo.UserFlagDiscordCertifiedModerator |
		discordgo.UserFlagBotHTTPInteractions |
		discordgo.UserFlagActiveBotDeveloper
	got := getUserBadges(int(flags))
	if len(got) != 15 {
		t.Fatalf("got %d public flag labels, want 15: %#v", len(got), got)
	}
	if !strings.Contains(strings.Join(got, "\n"), "HTTP interactions") {
		t.Fatalf("public bot interaction flag was omitted: %#v", got)
	}
}

func TestUserInfoTruncationPreservesUTF8(t *testing.T) {
	got := truncateUserInfo(strings.Repeat("é", 20), 10)
	if len([]rune(got)) != 10 || !strings.HasSuffix(got, "…") {
		t.Fatalf("truncated text = %q", got)
	}
}

```

## `./media_test.go`

```go
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
)

func TestMediaCommandAndTransformMenus(t *testing.T) {
	buildCommandSpecs()
	var command *discordgo.ApplicationCommand
	for _, spec := range commandSpecs {
		if spec.Command.Name == "media" {
			command = spec.Command
			break
		}
	}
	if command == nil {
		t.Fatal("/media is not registered")
	}
	if len(command.Options) != 1 || command.Options[0].Type != discordgo.ApplicationCommandOptionAttachment || !command.Options[0].Required {
		t.Fatalf("/media options = %#v, want one required attachment option", command.Options)
	}

	for _, kind := range []mediaKind{mediaImage, mediaAudio, mediaVideo} {
		effects := mediaEffects(kind)
		if len(effects) == 0 || len(effects) > 25 {
			t.Errorf("media kind %d has %d effects; Discord select menus allow 1-25", kind, len(effects))
		}
		seen := make(map[string]bool, len(effects))
		for _, effect := range effects {
			if effect.value == "" || effect.label == "" || seen[effect.value] {
				t.Errorf("media kind %d has an empty or duplicate effect: %#v", kind, effect)
			}
			seen[effect.value] = true
		}
		if kind != mediaAudio && (!seen["resize"] || !seen["optimize"]) {
			t.Errorf("visual media kind %d is missing resize or optimize", kind)
		}
		if kind == mediaAudio && (seen["resize"] || seen["optimize"]) {
			t.Error("audio menu should not contain image/video-only transformations")
		}
		if kind == mediaVideo {
			if err := validateMediaResizeDimensions(kind, 31, 20); err == nil {
				t.Error("video resize should reject odd width")
			}
			if err := validateMediaResizeDimensions(kind, 32, 21); err == nil {
				t.Error("video resize should reject odd height")
			}
		}
	}
}

func TestParseMediaResizeDimensions(t *testing.T) {
	tests := []struct {
		name       string
		width      string
		height     string
		wantWidth  int
		wantHeight int
		wantErr    bool
	}{
		{name: "preserve aspect ratio", width: "640", wantWidth: 640},
		{name: "explicit dimensions", width: "1920", height: "1080", wantWidth: 1920, wantHeight: 1080},
		{name: "invalid width", width: "0", wantErr: true},
		{name: "invalid height", width: "800", height: "9000", wantErr: true},
		{name: "non-integer", width: "640.5", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			width, height, err := parseMediaResizeDimensions(test.width, test.height)
			if (err != nil) != test.wantErr {
				t.Fatalf("parseMediaResizeDimensions() error = %v, wantErr %v", err, test.wantErr)
			}
			if err == nil && (width != test.wantWidth || height != test.wantHeight) {
				t.Errorf("got dimensions %dx%d, want %dx%d", width, height, test.wantWidth, test.wantHeight)
			}
		})
	}
}

func TestMediaHostAndImageClassification(t *testing.T) {
	for _, host := range []string{"cdn.discordapp.com", "media.discordapp.net", "cdn.discordapp.net"} {
		if !isDiscordMediaHost(host) {
			t.Errorf("expected Discord media host %q to be allowed", host)
		}
	}
	for _, host := range []string{"discordapp.com.evil.example", "example.com", ""} {
		if isDiscordMediaHost(host) {
			t.Errorf("unexpectedly allowed media host %q", host)
		}
	}
	if !isImageAttachment("photo.webp", "application/octet-stream") {
		t.Fatal("expected WebP attachment to be treated as an image")
	}
	if isImageAttachment("clip.mp4", "video/mp4") {
		t.Fatal("expected MP4 attachment not to be treated as an image")
	}
}

func TestMediaEffectsRunThroughFFmpeg(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is not installed")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe is not installed")
	}
	directory := t.TempDir()
	audioPath := filepath.Join(directory, "tone.wav")
	videoPath := filepath.Join(directory, "clip.mp4")
	silentVideoPath := filepath.Join(directory, "silent.mp4")
	rawVideoPath := filepath.Join(directory, "raw.avi")
	imagePath := filepath.Join(directory, "still.png")
	unoptimizedImagePath := filepath.Join(directory, "unoptimized.png")
	gifPath := filepath.Join(directory, "animation.gif")

	runMediaFixtureCommand(t, "-f", "lavfi", "-i", "sine=frequency=1000:duration=0.25", "-c:a", "pcm_s16le", audioPath)
	runMediaFixtureCommand(t,
		"-f", "lavfi", "-i", "testsrc=size=64x64:rate=8:duration=0.25",
		"-f", "lavfi", "-i", "sine=frequency=1000:duration=0.25", "-shortest",
		"-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", videoPath)
	runMediaFixtureCommand(t, "-f", "lavfi", "-i", "testsrc=size=64x64:rate=8:duration=0.25", "-c:v", "libx264", "-pix_fmt", "yuv420p", silentVideoPath)
	runMediaFixtureCommand(t, "-f", "lavfi", "-i", "testsrc=size=64x64:rate=8:duration=0.5", "-c:v", "rawvideo", "-pix_fmt", "yuv420p", rawVideoPath)
	runMediaFixtureCommand(t, "-f", "lavfi", "-i", "color=c=red:s=64x64", "-frames:v", "1", imagePath)
	runMediaFixtureCommand(t, "-f", "lavfi", "-i", "testsrc=size=256x256", "-frames:v", "1", "-compression_level", "0", unoptimizedImagePath)
	runMediaFixtureCommand(t, "-f", "lavfi", "-i", "testsrc=size=32x32:rate=5:duration=0.5", "-loop", "0", gifPath)

	cases := []struct {
		kind               mediaKind
		path               string
		filename           string
		content            string
		effects            []mediaEffect
		expectOptimization bool
	}{
		{kind: mediaAudio, path: audioPath, filename: "tone.wav", content: "audio/wav"},
		{kind: mediaVideo, path: videoPath, filename: "clip.mp4", content: "video/mp4"},
		{kind: mediaVideo, path: silentVideoPath, filename: "silent.mp4", content: "video/mp4", effects: videoMediaEffects[22:]},
		{kind: mediaVideo, path: rawVideoPath, filename: "raw.avi", content: "video/x-msvideo", effects: []mediaEffect{{value: "optimize", optimize: true}}, expectOptimization: true},
		{kind: mediaImage, path: imagePath, filename: "still.png", content: "image/png"},
		{kind: mediaImage, path: unoptimizedImagePath, filename: "unoptimized.png", content: "image/png", effects: []mediaEffect{{value: "optimize", optimize: true}}, expectOptimization: true},
		{kind: mediaImage, path: gifPath, filename: "animation.gif", content: "image/gif"},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(fmt.Sprintf("kind-%d-%s", testCase.kind, testCase.filename), func(t *testing.T) {
			inputInfo, err := os.Stat(testCase.path)
			if err != nil {
				t.Fatal(err)
			}
			frameCount := 0
			if testCase.kind == mediaImage {
				frameCount, err = probeImageFrameCount(testCase.path)
				if err != nil {
					t.Fatalf("probe image frame count: %v", err)
				}
			}
			session := &mediaSession{
				ownerID: "test", directory: directory, inputPath: testCase.path,
				filename: testCase.filename, contentType: testCase.content,
				inputSize: inputInfo.Size(), kind: testCase.kind, frameCount: frameCount, expiresAt: time.Now().Add(time.Minute),
			}
			effects := mediaEffects(testCase.kind)
			if testCase.effects != nil {
				effects = testCase.effects
			}
			for _, effect := range effects {
				if effect.value == "resize" {
					effect = mediaEffect{value: "resize", label: "resize", resizeWidth: 32, resizeHeight: 24}
				}
				outputPath, outputName, err := transformMedia(session, effect)
				if err != nil {
					t.Errorf("effect %q failed: %v", effect.value, err)
					continue
				}
				if effect.optimize {
					outputInfo, err := os.Stat(outputPath)
					if err != nil {
						t.Errorf("stat optimized media: %v", err)
					} else if outputInfo.Size() > inputInfo.Size() {
						t.Errorf("optimize returned %d bytes, larger than original %d bytes", outputInfo.Size(), inputInfo.Size())
					}
					if testCase.expectOptimization && outputName == filepath.Base(testCase.filename) {
						t.Errorf("expected optimization to produce a smaller file than the %d-byte original", inputInfo.Size())
					}
					if testCase.kind == mediaImage && outputName != filepath.Base(testCase.filename) {
						originalHashes := mediaFrameHashes(t, testCase.path)
						optimizedHashes := mediaFrameHashes(t, outputPath)
						if strings.Join(originalHashes, ",") != strings.Join(optimizedHashes, ",") {
							t.Errorf("lossless image optimization changed decoded pixels: %v != %v", originalHashes, optimizedHashes)
						}
					}
					if testCase.kind == mediaVideo && outputName == "transformed.mkv" {
						originalHashes := mediaFrameHashes(t, testCase.path)
						optimizedHashes := mediaFrameHashes(t, outputPath)
						if strings.Join(originalHashes, ",") != strings.Join(optimizedHashes, ",") {
							t.Errorf("lossless video optimization changed decoded frames: %v != %v", originalHashes, optimizedHashes)
						}
					}
				}
				if effect.value == "resize" {
					dimensions := probeMediaDimensions(t, outputPath)
					if dimensions.width != 32 || dimensions.height != 24 {
						t.Errorf("explicit resize dimensions = %dx%d, want 32x24", dimensions.width, dimensions.height)
					}
				}
			}
			if testCase.kind != mediaAudio {
				resize := mediaEffect{value: "resize", label: "resize", resizeWidth: 32}
				resizedPath, _, err := transformMedia(session, resize)
				if err != nil {
					t.Fatalf("aspect-preserving resize failed: %v", err)
				}
				dimensions := probeMediaDimensions(t, resizedPath)
				if dimensions.width != 32 || dimensions.height != 32 {
					t.Errorf("resized dimensions = %dx%d, want 32x32", dimensions.width, dimensions.height)
				}
			}
		})
	}
}

func mediaFrameHashes(t *testing.T, path string) []string {
	t.Helper()
	command := exec.Command("ffmpeg", "-v", "error", "-i", path, "-map", "0:v:0", "-vf", "format=rgba", "-f", "framemd5", "-")
	output, err := command.Output()
	if err != nil {
		t.Fatalf("frame hash: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) == 0 {
		t.Fatal("frame hash output was empty")
	}
	var hashes []string
	for _, line := range lines {
		if strings.HasPrefix(line, "#") || strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Split(line, ",")
		hashes = append(hashes, strings.TrimSpace(fields[len(fields)-1]))
	}
	return hashes
}

func probeMediaDimensions(t *testing.T, path string) struct{ width, height int } {
	t.Helper()
	command := exec.Command("ffprobe", "-v", "error", "-select_streams", "v:0", "-show_entries", "stream=width,height", "-of", "csv=s=x:p=0", path)
	output, err := command.Output()
	if err != nil {
		t.Fatalf("ffprobe dimensions: %v", err)
	}
	var dimensions struct{ width, height int }
	if _, err := fmt.Sscanf(string(output), "%dx%d", &dimensions.width, &dimensions.height); err != nil {
		t.Fatalf("parse ffprobe dimensions %q: %v", output, err)
	}
	return dimensions
}

func runMediaFixtureCommand(t *testing.T, args ...string) {
	t.Helper()
	command := exec.Command("ffmpeg", append([]string{"-nostdin", "-hide_banner", "-loglevel", "error", "-y"}, args...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Skipf("could not create FFmpeg test fixtures: %v (%s)", err, output)
	}
}

```

## `./notifier_delivery_test.go`

```go
package main

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
)

type notifierRoundTripFunc func(*http.Request) (*http.Response, error)

func (f notifierRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func notifierResponse(req *http.Request, status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}
}

func setNotifierTestState(t *testing.T, settings *GuildSettings, transport notifierRoundTripFunc) {
	t.Helper()
	oldClient := notifierHTTPClient
	oldMessage := sendNotifierMessage
	oldEmbed := sendNotifierEmbed
	oldComplex := sendNotifierComplex
	oldEdit := editNotifierComplex
	guildSettingsMu.Lock()
	oldGuildSettings := guildSettings
	guildSettings = map[string]*GuildSettings{SunfireServerID: settings}
	guildSettingsMu.Unlock()

	notifierHTTPClient = &http.Client{Transport: transport}
	sendNotifierMessage = func(_ *discordgo.Session, _, _ string) (*discordgo.Message, error) {
		return &discordgo.Message{ID: "sent-message"}, nil
	}
	sendNotifierEmbed = func(_ *discordgo.Session, _ string, _ *discordgo.MessageEmbed) (*discordgo.Message, error) {
		return &discordgo.Message{ID: "sent-message"}, nil
	}
	sendNotifierComplex = func(_ *discordgo.Session, _ string, _ *discordgo.MessageSend) (*discordgo.Message, error) {
		return &discordgo.Message{ID: "sent-message"}, nil
	}
	editNotifierComplex = func(_ *discordgo.Session, _ *discordgo.MessageEdit) (*discordgo.Message, error) {
		return &discordgo.Message{ID: "sent-message"}, nil
	}
	t.Cleanup(func() {
		notifierHTTPClient = oldClient
		sendNotifierMessage = oldMessage
		sendNotifierEmbed = oldEmbed
		sendNotifierComplex = oldComplex
		editNotifierComplex = oldEdit
		guildSettingsMu.Lock()
		guildSettings = oldGuildSettings
		guildSettingsMu.Unlock()
	})
}

func TestYouTubeNotifierCachesOnlyAfterSuccessfulDelivery(t *testing.T) {
	dir := t.TempDir()
	oldCache, oldPath := ytCache, ytCachePath
	ytCache = map[string]youtubeMonitorCacheEntry{SunfireServerID + ":UC12345678901234567890": {LastVideoID: "old"}}
	ytCachePath = filepath.Join(dir, "youtube.json")
	t.Cleanup(func() { ytCache, ytCachePath = oldCache, oldPath })
	setNotifierTestState(t, &GuildSettings{
		YouTubeEnabled: true, YouTubeChannelID: "channel", YouTubers: []string{"UC12345678901234567890"},
	}, func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/youtube/v3/playlistItems" {
			t.Fatalf("unexpected request URL: %s", req.URL)
		}
		return notifierResponse(req, http.StatusOK, `{"items":[{"snippet":{"title":"Latest","channelTitle":"Creator","resourceId":{"videoId":"new"}}}]}`), nil
	})

	sends := 0
	sendNotifierMessage = func(_ *discordgo.Session, channelID, _ string) (*discordgo.Message, error) {
		sends++
		if channelID != "channel" {
			t.Fatalf("sent to channel %q", channelID)
		}
		if sends == 1 {
			return nil, io.ErrUnexpectedEOF
		}
		return &discordgo.Message{ID: "sent"}, nil
	}
	cfg := &Config{YouTubeAPIKey: "test-key"}

	checkYouTubeUpdates(nil, cfg)
	if got := ytCache[SunfireServerID+":UC12345678901234567890"].LastVideoID; got != "old" {
		t.Fatalf("cache advanced after failed send: %q", got)
	}
	checkYouTubeUpdates(nil, cfg)
	if got := ytCache[SunfireServerID+":UC12345678901234567890"].LastVideoID; got != "new" {
		t.Fatalf("cache not advanced after successful send: %q", got)
	}
}

func TestMoviesNotifierDoesNotCacheFailedDeliveryAndRollsBackFailedPersistence(t *testing.T) {
	dir := t.TempDir()
	oldCache, oldPath := moviesCache, moviesCachePath
	moviesCache = make(map[string]map[string][]string)
	moviesCachePath = filepath.Join(dir, "movies.json")
	t.Cleanup(func() { moviesCache, moviesCachePath = oldCache, oldPath })
	setNotifierTestState(t, &GuildSettings{MoviesThisWeekChannelID: "channel"}, func(req *http.Request) (*http.Response, error) {
		return notifierResponse(req, http.StatusOK, `{"results":[{"id":42,"title":"Test Movie","release_date":"2026-10-05"}]}`), nil
	})

	failSend := true
	sendNotifierComplex = func(_ *discordgo.Session, _ string, _ *discordgo.MessageSend) (*discordgo.Message, error) {
		if failSend {
			return nil, io.ErrUnexpectedEOF
		}
		return &discordgo.Message{ID: "sent"}, nil
	}
	cfg := &Config{TMDBAPIKey: "test-key"}
	checkMoviesUpdates(nil, cfg)
	if len(moviesCache[SunfireServerID]) != 0 {
		t.Fatalf("cache changed after failed send: %#v", moviesCache)
	}

	failSend = false
	blockedDir := filepath.Join(dir, "blocked")
	if err := os.WriteFile(blockedDir, []byte("block"), 0o600); err != nil {
		t.Fatal(err)
	}
	moviesCachePath = filepath.Join(blockedDir, "movies.json")
	checkMoviesUpdates(nil, cfg)
	if len(moviesCache[SunfireServerID]["seenMovieIds"]) != 0 {
		t.Fatalf("cache not rolled back after persistence error: %#v", moviesCache)
	}

	moviesCachePath = filepath.Join(dir, "movies.json")
	checkMoviesUpdates(nil, cfg)
	if got := moviesCache[SunfireServerID]["seenMovieIds"]; len(got) != 1 || got[0] != "42" {
		t.Fatalf("successful delivery was not persisted: %#v", got)
	}
}

func TestSpotifyNotifierCachesOnlyAfterSuccessfulDelivery(t *testing.T) {
	dir := t.TempDir()
	oldCache, oldPath, oldToken, oldExpiry := spotCache, spotCachePath, spotCachedToken, spotTokenExpiry
	spotCache = make(map[string]spotifyCacheEntry)
	spotCachePath = filepath.Join(dir, "spotify.json")
	spotCachedToken = "cached"
	spotTokenExpiry = time.Now().Add(time.Hour)
	t.Cleanup(func() {
		spotCache, spotCachePath, spotCachedToken, spotTokenExpiry = oldCache, oldPath, oldToken, oldExpiry
	})
	setNotifierTestState(t, &GuildSettings{SpotifyEnabled: true, SpotifyChannelID: "channel"}, func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/v1/search" {
			t.Fatalf("unexpected request URL: %s", req.URL)
		}
		return notifierResponse(req, http.StatusOK, `{"albums":{"items":[{"id":"release","name":"Album","artists":[{"name":"Artist"}],"external_urls":{"spotify":"https://open.spotify.com/album/release"}}]}}`), nil
	})
	sends := 0
	sendNotifierMessage = func(_ *discordgo.Session, _, _ string) (*discordgo.Message, error) {
		sends++
		if sends == 1 {
			return nil, io.ErrUnexpectedEOF
		}
		return &discordgo.Message{ID: "sent"}, nil
	}
	cfg := &Config{SpotifyClientID: "id", SpotifyClientSecret: "secret"}

	checkSpotifyUpdates(nil, cfg)
	if len(spotCache[SunfireServerID].SeenReleaseIDs) != 0 {
		t.Fatalf("cache changed after failed send: %#v", spotCache)
	}
	checkSpotifyUpdates(nil, cfg)
	if got := spotCache[SunfireServerID].SeenReleaseIDs; len(got) != 1 || got[0] != "release" {
		t.Fatalf("cache not advanced after successful send: %#v", got)
	}
}

func TestKickNotifierCachesOnlyAfterSuccessfulDelivery(t *testing.T) {
	dir := t.TempDir()
	oldCache, oldPath, oldToken, oldExpiry := kickCache, kickCachePath, kickCachedToken, kickTokenExpiry
	kickCache = make(map[string]kickCacheEntry)
	kickCachePath = filepath.Join(dir, "kick.json")
	kickCachedToken = "cached"
	kickTokenExpiry = time.Now().Add(time.Hour)
	t.Cleanup(func() {
		kickCache, kickCachePath, kickCachedToken, kickTokenExpiry = oldCache, oldPath, oldToken, oldExpiry
	})
	setNotifierTestState(t, &GuildSettings{KickEnabled: true, KickChannelID: "channel", KickStreamers: []string{"Streamer"}}, func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/public/v1/channels" {
			t.Fatalf("unexpected request URL: %s", req.URL)
		}
		return notifierResponse(req, http.StatusOK, `{"data":[{"slug":"streamer","stream_title":"Live now","stream":{"is_live":true}}]}`), nil
	})
	sends := 0
	sendNotifierComplex = func(_ *discordgo.Session, _ string, _ *discordgo.MessageSend) (*discordgo.Message, error) {
		sends++
		if sends == 1 {
			return nil, io.ErrUnexpectedEOF
		}
		return &discordgo.Message{ID: "sent"}, nil
	}
	cfg := &Config{KickClientID: "id", KickClientSecret: "secret"}

	checkKickUpdates(nil, cfg)
	if kickCache[SunfireServerID+":streamer"].IsLive {
		t.Fatal("cache marked live after failed send")
	}
	checkKickUpdates(nil, cfg)
	if !kickCache[SunfireServerID+":streamer"].IsLive || kickCache[SunfireServerID+":streamer"].LastMessageID != "sent" {
		t.Fatalf("cache not updated after successful send: %#v", kickCache[SunfireServerID+":streamer"])
	}
}

func TestFreeStuffNotifierCachesOnlyAfterSuccessfulDelivery(t *testing.T) {
	dir := t.TempDir()
	oldCache, oldPath := freeStuffCache, freeStuffCachePath
	freeStuffCache = freeStuffState{Guilds: make(map[string]struct {
		InitializedSources map[string]bool `json:"initializedSources"`
		SeenItems          struct {
			GGDeals     []string `json:"ggDeals"`
			ProductHunt []string `json:"productHunt"`
		} `json:"seenItems"`
	})}
	freeStuffCachePath = filepath.Join(dir, "free.json")
	t.Cleanup(func() { freeStuffCache, freeStuffCachePath = oldCache, oldPath })
	setNotifierTestState(t, &GuildSettings{FreeStuffEnabled: true, FreeStuffChannelID: "channel"}, func(req *http.Request) (*http.Response, error) {
		return notifierResponse(req, http.StatusOK, `<rss><channel><item><title>Free game</title><link>https://example.test/freebie</link></item></channel></rss>`), nil
	})
	sends := 0
	sendNotifierMessage = func(_ *discordgo.Session, _, _ string) (*discordgo.Message, error) {
		sends++
		if sends == 1 {
			return nil, io.ErrUnexpectedEOF
		}
		return &discordgo.Message{ID: "sent"}, nil
	}

	checkFreeStuffUpdates(nil, &Config{})
	if len(freeStuffCache.Guilds[SunfireServerID].SeenItems.GGDeals) != 0 {
		t.Fatal("cache changed after failed send")
	}
	checkFreeStuffUpdates(nil, &Config{})
	if got := freeStuffCache.Guilds[SunfireServerID].SeenItems.GGDeals; len(got) != 1 || got[0] != "https://example.test/freebie" {
		t.Fatalf("cache not advanced after successful send: %#v", got)
	}
}

func TestScrapeNotifierCachesOnlyAfterSuccessfulDelivery(t *testing.T) {
	dir := t.TempDir()
	oldCache, oldPath := scrapeCache, scrapeCachePath
	scrapeCache = scrapeState{Posted: make(map[string][]string), Attempted: make(map[string][]string)}
	scrapeCachePath = filepath.Join(dir, "scrape.json")
	t.Cleanup(func() { scrapeCache, scrapeCachePath = oldCache, oldPath })
	setNotifierTestState(t, &GuildSettings{}, func(req *http.Request) (*http.Response, error) {
		return notifierResponse(req, http.StatusOK, `<html>https://example.test/image.jpg</html>`), nil
	})
	failSend := true
	sendNotifierEmbed = func(_ *discordgo.Session, _ string, _ *discordgo.MessageEmbed) (*discordgo.Message, error) {
		if failSend {
			return nil, io.ErrUnexpectedEOF
		}
		return &discordgo.Message{ID: "sent"}, nil
	}
	cfg := &Config{Scrape: ScrapeConfig{
		URLs:       []string{"https://example.test/gallery"},
		ChannelIDs: []string{"channel"},
	}}

	runDailyImageScrape(nil, cfg)
	if len(scrapeCache.Posted["https://example.test/gallery\x00channel"]) != 0 {
		t.Fatal("cache changed after failed send")
	}
	failSend = false
	runDailyImageScrape(nil, cfg)
	if got := scrapeCache.Posted["https://example.test/gallery\x00channel"]; len(got) != 1 || got[0] != "https://example.test/image.jpg" {
		t.Fatalf("cache not advanced after successful send: %#v", got)
	}
}

```

## `./guild_config.go`

```go
package main

import (
	"sync"
)

// GuildSettings defines per-guild configuration for ElyAdmin features ported into Sunfire.
type GuildSettings struct {
	LockdownExemptRoleIDs        []string               `json:"lockdownExemptRoleIds"`
	DefaultSlowmodeSeconds       int                    `json:"defaultSlowmodeSeconds"`
	ModLogChannelID              string                 `json:"modLogChannelId"`
	CommandRoleAllowlist         map[string]interface{} `json:"commandRoleAllowlist"`
	BirthdayAnnouncementChannelID string                `json:"birthdayAnnouncementChannelId"`
	TicketChannelID              string                 `json:"ticketChannelId"`
	TicketStaffRoleID            string                 `json:"ticketStaffRoleId"`
	AutoRoleID                   *string                `json:"autoRoleId"`
	WelcomeChannelID             *string                `json:"welcomeChannelId"`
	FarewellChannelID            *string                `json:"farewellChannelId"`
	WelcomeTemplate              string                 `json:"welcomeTemplate"`
	FarewellTemplate             string                 `json:"farewellTemplate"`
	RaidProtectionEnabled        bool                   `json:"raidProtectionEnabled"`
	StarboardChannelID           *string                `json:"starboardChannelId"`
	StarboardThreshold           int                    `json:"starboardThreshold"`
	KickEnabled                  bool                   `json:"kickEnabled"`
	KickChannelID                string                 `json:"kickChannelId"`
	KickStreamers                []string               `json:"kickStreamers"`
	KickStreamingRoleID          *string                `json:"kickStreamingRoleId"`
	MoviesThisWeekChannelID      string                 `json:"moviesThisWeekChannelId"`
	MoviesThisWeekRoleID         *string                `json:"moviesThisWeekRoleId"`
	StreamingThisWeekChannelID   string                 `json:"streamingThisWeekChannelId"`
	StreamingThisWeekRoleID      *string                `json:"streamingThisWeekRoleId"`
	MinimumAccountAgeDays        *int                   `json:"minimumAccountAgeDays"`
	YouTubeEnabled               bool                   `json:"youtubeEnabled"`
	YouTubeChannelID             string                 `json:"youtubeChannelId"`
	YouTubeRoleID                *string                `json:"youtubeRoleId"`
	YouTubers                    []string               `json:"youtubers"`
	SpotifyChannelID             string                 `json:"spotifyChannelId"`
	SpotifyRoleID                *string                `json:"spotifyRoleId"`
	SpotifyEnabled               bool                   `json:"spotifyEnabled"`
	FreeStuffEnabled             bool                   `json:"freeStuffEnabled"`
	FreeStuffChannelID           string                 `json:"freeStuffChannelId"`
	FreeStuffRoleID              *string                `json:"freeStuffRoleId"`
}

var (
	guildSettingsMu sync.RWMutex
	guildSettings   map[string]*GuildSettings
)

// loadGuildSettings loads the guild settings from .private/config.json into memory.
func loadGuildSettings() map[string]*GuildSettings {
	guildSettingsMu.Lock()
	defer guildSettingsMu.Unlock()

	if guildSettings != nil {
		return guildSettings
	}

	guildSettings = make(map[string]*GuildSettings)
	cfg, err := loadConfig()
	if err == nil && cfg != nil {
		guildSettings[SunfireServerID] = &cfg.Guild
	} else {
		guildSettings[SunfireServerID] = &GuildSettings{}
	}

	return guildSettings
}

// getGuildSettings retrieves or creates settings for a given guild ID (Sunfire single-server).
func getGuildSettings(guildID string) *GuildSettings {
	all := loadGuildSettings()
	guildSettingsMu.Lock()
	defer guildSettingsMu.Unlock()

	if g, ok := all[guildID]; ok && g != nil {
		return g
	}

	// For single-server bot, default to the main guild settings
	if g, ok := all[SunfireServerID]; ok && g != nil {
		all[guildID] = g
		return g
	}

	g := &GuildSettings{}
	all[guildID] = g
	return g
}

// saveGuildSettings persists the current guild settings to .private/config.json.
func saveGuildSettings() error {
	guildSettingsMu.Lock()
	defer guildSettingsMu.Unlock()

	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	if g, ok := guildSettings[SunfireServerID]; ok && g != nil {
		cfg.Guild = *g
	}

	return saveConfig(configPath(), cfg)
}

```

## `./config_test.go`

```go
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestConfigLoadAndSave(t *testing.T) {
	tmpDir := t.TempDir()
	cfgFile := filepath.Join(tmpDir, "config.json")

	initialJSON := `{
  "project": "Sunfire",
  "notes": "Test notes",
  "botToken": "dummy_token",
  "applicationId": "123456",
  "publicKey": "pubkey",
  "botInviteUrl": "https://discord.com",
  "loggingChannelId": "1001",
  "ownerId": "2002",
  "infoCategoryId": "3003",
  "rulesChannelId": "4004"
}`

	if err := os.WriteFile(cfgFile, []byte(initialJSON), 0o600); err != nil {
		t.Fatalf("failed to write test config: %v", err)
	}

	cfg, err := loadConfigFromPath(cfgFile)
	if err != nil {
		t.Fatalf("loadConfigFromPath failed: %v", err)
	}

	if cfg.LoggingChannel != "1001" {
		t.Errorf("expected LoggingChannel 1001, got %s", cfg.LoggingChannel)
	}
	if cfg.InfoCategoryID != "3003" {
		t.Errorf("expected InfoCategoryID 3003, got %s", cfg.InfoCategoryID)
	}
	if cfg.RulesChannelID != "4004" {
		t.Errorf("expected RulesChannelID 4004, got %s", cfg.RulesChannelID)
	}

	// Test modifying and saving
	cfg.InfoCategoryID = "5005"
	cfg.RulesChannelID = "6006"
	if err := saveConfig(cfgFile, cfg); err != nil {
		t.Fatalf("saveConfig failed: %v", err)
	}

	reloaded, err := loadConfigFromPath(cfgFile)
	if err != nil {
		t.Fatalf("reloading config failed: %v", err)
	}
	if reloaded.InfoCategoryID != "5005" {
		t.Errorf("expected reloaded InfoCategoryID 5005, got %s", reloaded.InfoCategoryID)
	}
	if reloaded.RulesChannelID != "6006" {
		t.Errorf("expected reloaded RulesChannelID 6006, got %s", reloaded.RulesChannelID)
	}
}

func TestConfigAliases(t *testing.T) {
	tmpDir := t.TempDir()
	cfgFile := filepath.Join(tmpDir, "config_alias.json")

	aliasJSON := `{
  "infoChannelId": "7007",
  "rulesChannel": "8008"
}`
	if err := os.WriteFile(cfgFile, []byte(aliasJSON), 0o600); err != nil {
		t.Fatalf("failed to write test config: %v", err)
	}

	cfg, err := loadConfigFromPath(cfgFile)
	if err != nil {
		t.Fatalf("loadConfigFromPath failed: %v", err)
	}
	if cfg.InfoCategoryID != "7007" {
		t.Errorf("expected InfoCategoryID 7007 from alias, got %s", cfg.InfoCategoryID)
	}
	if cfg.RulesChannelID != "8008" {
		t.Errorf("expected RulesChannelID 8008 from alias, got %s", cfg.RulesChannelID)
	}
}

func TestPublicCommands(t *testing.T) {
	publicCmds := []string{
		"help", "userinfo", "media", "emojigg",
		"analytics", "automod", "dice", "remind", "serverstats", "sysinfo", "transcript",
		"ia", "lyrics", "cloudflare", "cohere", "groq", "mistral", "nvidia",
	}
	for _, cmd := range publicCmds {
		if !isPublicCommand(cmd) {
			t.Errorf("expected command %q to be public", cmd)
		}
	}

	ownerOnlyCmds := []string{"purge", "setup", "dedup", "unknown"}
	for _, cmd := range ownerOnlyCmds {
		if isPublicCommand(cmd) {
			t.Errorf("expected command %q to be owner-only", cmd)
		}
	}
}

func TestScrapeConfigUnmarshal(t *testing.T) {
	jsonData := `{
		"urls": ["https://example.com/gallery"],
		"channelIds": [123456789012345678, "987654321098765432"]
	}`

	var sc ScrapeConfig
	if err := json.Unmarshal([]byte(jsonData), &sc); err != nil {
		t.Fatalf("failed to unmarshal ScrapeConfig: %v", err)
	}

	if len(sc.ChannelIDs) != 2 {
		t.Fatalf("expected 2 channel IDs, got %d", len(sc.ChannelIDs))
	}
	if sc.ChannelIDs[0] != "123456789012345678" {
		t.Errorf("expected channel ID '123456789012345678', got %q", sc.ChannelIDs[0])
	}
	if sc.ChannelIDs[1] != "987654321098765432" {
		t.Errorf("expected channel ID '987654321098765432', got %q", sc.ChannelIDs[1])
	}
}

func TestHealthCheckConfigUnmarshal(t *testing.T) {
	jsonData := `{
		"enabled": true,
		"intervalMinutes": 5,
		"channelId": 1564465554864209920,
		"targets": ["https://alienentity.xyz"]
	}`

	var hc HealthCheckConfig
	if err := json.Unmarshal([]byte(jsonData), &hc); err != nil {
		t.Fatalf("failed to unmarshal HealthCheckConfig: %v", err)
	}

	if hc.ChannelID != "1564465554864209920" {
		t.Errorf("expected ChannelID '1564465554864209920', got %q", hc.ChannelID)
	}
	if hc.TargetChannelID() != "1564465554864209920" {
		t.Errorf("expected TargetChannelID '1564465554864209920', got %q", hc.TargetChannelID())
	}
}

```

## `./serverstats.go`

```go
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
```

## `./notifier_youtube.go`

```go
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
)

type youtubeMonitorCacheEntry struct {
	LastVideoID string `json:"lastVideoId"`
}

type youtubePlaylistItemsResponse struct {
	Items []struct {
		Snippet struct {
			Title        string    `json:"title"`
			PublishedAt  time.Time `json:"publishedAt"`
			ChannelTitle string    `json:"channelTitle"`
			ResourceID   struct {
				VideoID string `json:"videoId"`
			} `json:"resourceId"`
		} `json:"snippet"`
	} `json:"items"`
}

type youtubeChannelResponse struct {
	Items []struct {
		ID string `json:"id"`
	} `json:"items"`
}

var (
	ytMu             sync.Mutex
	ytCache          map[string]youtubeMonitorCacheEntry
	ytChannelIDCache = make(map[string]string)
	ytCachePath      string
)

func initYTCache() {
	ytCachePath = dataPath("youtubeMonitorCache.json")
	ytCache = make(map[string]youtubeMonitorCacheEntry)

	data, err := os.ReadFile(ytCachePath)
	if err == nil {
		if err := json.Unmarshal(data, &ytCache); err != nil {
			log.Printf("YouTube notifier: parse cache %s: %v", ytCachePath, err)
			ytCache = make(map[string]youtubeMonitorCacheEntry)
		}
	} else if !os.IsNotExist(err) {
		log.Printf("YouTube notifier: read cache %s: %v", ytCachePath, err)
	}
	if ytCache == nil {
		ytCache = make(map[string]youtubeMonitorCacheEntry)
	}
}

func saveYTCache() error {
	if ytCachePath == "" {
		return fmt.Errorf("YouTube cache path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(ytCachePath), 0755); err != nil {
		return fmt.Errorf("creating YouTube cache directory: %w", err)
	}
	data, err := json.MarshalIndent(ytCache, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding YouTube cache: %w", err)
	}
	if err := os.WriteFile(ytCachePath, data, 0644); err != nil {
		return fmt.Errorf("writing YouTube cache %s: %w", ytCachePath, err)
	}
	return nil
}

// uploadsPlaylistID derives the uploads playlist ID from a channel ID (UC... -> UU...).
func uploadsPlaylistID(chanID string) string {
	if strings.HasPrefix(chanID, "UC") && len(chanID) > 2 {
		return "UU" + chanID[2:]
	}
	return chanID
}

// resolveYouTubeChannelID resolves an @handle or returns channel ID directly
func resolveYouTubeChannelID(source, apiKey string) (string, error) {
	s := strings.TrimSpace(source)
	if strings.HasPrefix(s, "https://www.youtube.com/") || strings.HasPrefix(s, "https://youtube.com/") {
		parts := strings.Split(strings.TrimRight(s, "/"), "/")
		if len(parts) > 0 {
			s = parts[len(parts)-1]
		}
	}

	if strings.HasPrefix(s, "UC") && len(s) >= 20 {
		return s, nil
	}

	ytMu.Lock()
	if cached, ok := ytChannelIDCache[source]; ok {
		ytMu.Unlock()
		return cached, nil
	}
	ytMu.Unlock()

	if strings.HasPrefix(s, "@") {
		handle := strings.TrimPrefix(s, "@")
		query := url.Values{
			"part":      {"id"},
			"forHandle": {handle},
			"key":       {apiKey},
		}
		apiURL := "https://www.googleapis.com/youtube/v3/channels?" + query.Encode()
		client := notifierClient(15 * time.Second)
		resp, err := client.Get(apiURL)
		if err != nil {
			var urlErr *url.Error
			if errors.As(err, &urlErr) {
				return "", fmt.Errorf("YouTube channel request failed: %w", urlErr.Err)
			}
			return "", err
		}
		defer resp.Body.Close()

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return "", fmt.Errorf("reading YouTube channel response: %w", err)
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return "", fmt.Errorf("YouTube channel lookup returned HTTP %d", resp.StatusCode)
		}
		var cResp youtubeChannelResponse
		if err := json.Unmarshal(body, &cResp); err != nil {
			return "", fmt.Errorf("decoding YouTube channel response: %w", err)
		}
		if len(cResp.Items) > 0 {
			id := cResp.Items[0].ID
			ytMu.Lock()
			ytChannelIDCache[source] = id
			ytMu.Unlock()
			return id, nil
		}
	}

	return "", fmt.Errorf("unable to resolve channel ID for %s", source)
}

// checkYouTubeUpdates polls configured channels for new videos
func checkYouTubeUpdates(s *discordgo.Session, cfg *Config) {
	if cfg.YouTubeAPIKey == "" {
		return
	}

	guilds := loadGuildSettings()
	ytMu.Lock()
	defer ytMu.Unlock()

	for guildID, gCfg := range guilds {
		if !gCfg.YouTubeEnabled || gCfg.YouTubeChannelID == "" || len(gCfg.YouTubers) == 0 {
			continue
		}

		for _, source := range gCfg.YouTubers {
			chanID, err := resolveYouTubeChannelID(source, cfg.YouTubeAPIKey)
			if err != nil {
				log.Printf("YouTube notifier: resolve channel %q: %v", source, err)
				continue
			}

			cacheKey := fmt.Sprintf("%s:%s", guildID, chanID)
			cached := ytCache[cacheKey]

			uploadsID := uploadsPlaylistID(chanID)
			query := url.Values{
				"part":       {"snippet"},
				"playlistId": {uploadsID},
				"maxResults": {"1"},
				"key":        {cfg.YouTubeAPIKey},
			}
			apiURL := "https://www.googleapis.com/youtube/v3/playlistItems?" + query.Encode()
			client := notifierClient(15 * time.Second)
			resp, err := client.Get(apiURL)
			if err != nil {
				var urlErr *url.Error
				if errors.As(err, &urlErr) {
					err = urlErr.Err
				}
				log.Printf("YouTube notifier: fetch latest video for channel %s: %v", chanID, err)
				continue
			}

			body, readErr := io.ReadAll(resp.Body)
			resp.Body.Close()
			if readErr != nil {
				log.Printf("YouTube notifier: read latest video response for channel %s: %v", chanID, readErr)
				continue
			}
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				log.Printf("YouTube notifier: latest video request for channel %s returned HTTP %d", chanID, resp.StatusCode)
				continue
			}

			var sResp youtubePlaylistItemsResponse
			if err := json.Unmarshal(body, &sResp); err != nil {
				log.Printf("YouTube notifier: decode latest video response for channel %s: %v", chanID, err)
				continue
			}
			if len(sResp.Items) == 0 {
				log.Printf("YouTube notifier: no videos returned for channel %s", chanID)
				continue
			}

			latest := sResp.Items[0]
			videoID := latest.Snippet.ResourceID.VideoID
			if videoID == "" {
				continue
			}

			if cached.LastVideoID == "" {
				// Initial seed
				ytCache[cacheKey] = youtubeMonitorCacheEntry{LastVideoID: videoID}
				if err := saveYTCache(); err != nil {
					delete(ytCache, cacheKey)
					log.Printf("YouTube notifier: persist initial video state for channel %s: %v", chanID, err)
				}
				continue
			}

			if cached.LastVideoID != videoID {
				videoURL := fmt.Sprintf("https://www.youtube.com/watch?v=%s", videoID)
				content := fmt.Sprintf("🎬 **%s** uploaded a new video!\n**%s**\n%s", latest.Snippet.ChannelTitle, latest.Snippet.Title, videoURL)
				if gCfg.YouTubeRoleID != nil && *gCfg.YouTubeRoleID != "" {
					content = fmt.Sprintf("<@&%s> %s", *gCfg.YouTubeRoleID, content)
				}

				if _, err := sendNotifierMessage(s, gCfg.YouTubeChannelID, content); err != nil {
					log.Printf("YouTube notifier: send upload announcement for channel %s: %v", chanID, err)
					continue
				}
				ytCache[cacheKey] = youtubeMonitorCacheEntry{LastVideoID: videoID}
				if err := saveYTCache(); err != nil {
					ytCache[cacheKey] = cached
					log.Printf("YouTube notifier: persist delivered video state for channel %s: %v", chanID, err)
				}
			}
		}
	}
}

// startYouTubeNotifierDaemon runs periodic YouTube video checks
func startYouTubeNotifierDaemon(ctx context.Context, s *discordgo.Session) {
	initYTCache()
	cfg, err := loadConfig()
	if err != nil {
		log.Printf("YouTube notifier: load config: %v", err)
		return
	}
	if cfg.YouTubeAPIKey == "" {
		log.Printf("YouTube notifier not started: YouTube API key is not configured")
		return
	}

	interval := 20 * time.Minute
	if cfg.YouTubePollIntervalMs > 0 {
		interval = time.Duration(cfg.YouTubePollIntervalMs) * time.Millisecond
	}

	log.Printf("YouTube notifier daemon started (interval %v)", interval)
	runPeriodic(ctx, 10*time.Second, interval, func() { checkYouTubeUpdates(s, cfg) })
}

```

## `./roles.go`

```go
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

```

## `./translate.go`

```go
// Package main — translate.go provides multi-language translation using
// LibreTranslate and MyMemory fallback.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
)

var langCodeRegex = regexp.MustCompile(`^[a-zA-Z]{2,5}(?:-[a-zA-Z]{2,5})?$`)

func normalizeTargetLanguage(input string) string {
	raw := strings.ToLower(strings.TrimSpace(input))
	switch raw {
	case "ph", "filipino":
		return "tl"
	case "jp":
		return "ja"
	case "kr":
		return "ko"
	default:
		return raw
	}
}

func translateWithLibreTranslate(text, target string) (string, error) {
	reqBody, _ := json.Marshal(map[string]string{
		"q":      text,
		"source": "auto",
		"target": target,
		"format": "text",
	})

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post("https://libretranslate.de/translate", "application/json", bytes.NewReader(reqBody))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("libretranslate status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	var data struct {
		TranslatedText string `json:"translatedText"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return "", err
	}

	res := strings.TrimSpace(data.TranslatedText)
	if res == "" {
		return "", fmt.Errorf("empty translation from libretranslate")
	}
	return res, nil
}

func translateWithMyMemory(text, target string) (string, error) {
	queryEndpoint := func(source string) (string, error) {
		reqURL := fmt.Sprintf("https://api.mymemory.translated.net/get?q=%s&langpair=%s|%s",
			url.QueryEscape(text), url.QueryEscape(source), url.QueryEscape(target))

		client := &http.Client{Timeout: 10 * time.Second}
		resp, err := client.Get(reqURL)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("mymemory status %d", resp.StatusCode)
		}

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return "", err
		}

		var data struct {
			ResponseData struct {
				TranslatedText string `json:"translatedText"`
			} `json:"responseData"`
			ResponseDetails string `json:"responseDetails"`
		}
		if err := json.Unmarshal(body, &data); err != nil {
			return "", err
		}

		translated := strings.TrimSpace(data.ResponseData.TranslatedText)
		details := strings.TrimSpace(data.ResponseDetails)

		if strings.Contains(strings.ToLower(translated), "invalid source language") ||
			strings.Contains(strings.ToLower(details), "invalid source language") {
			return "", fmt.Errorf("invalid source language")
		}

		if translated == "" {
			return "", fmt.Errorf("%s", details)
		}
		return translated, nil
	}

	// Try auto source first
	if res, err := queryEndpoint("auto"); err == nil && res != "" {
		return res, nil
	}

	// Fallback to explicit English source
	if res, err := queryEndpoint("en"); err == nil && res != "" {
		return res, nil
	}

	return "", fmt.Errorf("mymemory could not translate text")
}

func translateText(text, target string) (string, error) {
	if res, err := translateWithLibreTranslate(text, target); err == nil && res != "" {
		return res, nil
	}
	return translateWithMyMemory(text, target)
}

func buildLanguageHelpMessage(code string) string {
	return fmt.Sprintf("❌ Invalid or unsupported target language code: `%s`.\n"+
		"Use ISO language codes like: `en`, `es`, `fr`, `de`, `ja`, `ko`, `tl`, `zh-CN`.\n"+
		"Note: Filipino/Tagalog is `tl` (not `ph`).\n"+
		"Reference: <https://en.wikipedia.org/wiki/List_of_ISO_639-1_codes>", code)
}

// Slash command: /translate text: <text> to: <lang>
func handleTranslate(s *discordgo.Session, i *discordgo.InteractionCreate) {
	options := i.ApplicationCommandData().Options
	var text, rawTo string
	for _, opt := range options {
		if opt.Name == "text" {
			text = opt.StringValue()
		} else if opt.Name == "to" {
			rawTo = opt.StringValue()
		}
	}

	to := normalizeTargetLanguage(rawTo)
	if !langCodeRegex.MatchString(to) {
		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: buildLanguageHelpMessage(rawTo),
			},
		})
		return
	}

	// Defer reply while translating
	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	})

	translated, err := translateText(text, to)
	if err != nil {
		_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
			Content: fmt.Sprintf("❌ Translation failed: %v", err),
		})
		return
	}

	respContent := fmt.Sprintf("**Original:** %s\n\n**Translated (%s):** %s", text, to, translated)
	_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
		Content: respContent,
	})
}

```

## `./notifier_spotify.go`

```go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
)

type spotifyCacheEntry struct {
	SeenReleaseIDs []string `json:"seenReleaseIds"`
}

type spotifyTokenResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
}

type spotifyReleasesResponse struct {
	Albums struct {
		Items []struct {
			ID           string `json:"id"`
			Name         string `json:"name"`
			ReleaseDate  string `json:"release_date"`
			ExternalURLs struct {
				Spotify string `json:"spotify"`
			} `json:"external_urls"`
			Artists []struct {
				Name string `json:"name"`
			} `json:"artists"`
			Images []struct {
				URL string `json:"url"`
			} `json:"images"`
		} `json:"items"`
	} `json:"albums"`
}

var (
	spotMu          sync.Mutex
	spotCache       map[string]spotifyCacheEntry
	spotCachePath   string
	spotCachedToken string
	spotTokenExpiry time.Time
)

func initSpotifyCache() {
	spotCachePath = dataPath("spotifyReleasesCache.json")
	spotCache = make(map[string]spotifyCacheEntry)

	data, err := os.ReadFile(spotCachePath)
	if err == nil {
		if err := json.Unmarshal(data, &spotCache); err != nil {
			log.Printf("Spotify notifier: parse cache %s: %v", spotCachePath, err)
			spotCache = make(map[string]spotifyCacheEntry)
		}
	} else if !os.IsNotExist(err) {
		log.Printf("Spotify notifier: read cache %s: %v", spotCachePath, err)
	}
	if spotCache == nil {
		spotCache = make(map[string]spotifyCacheEntry)
	}
}

func saveSpotifyCache() error {
	if spotCachePath == "" {
		return fmt.Errorf("Spotify cache path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(spotCachePath), 0755); err != nil {
		return fmt.Errorf("creating Spotify cache directory: %w", err)
	}
	data, err := json.MarshalIndent(spotCache, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding Spotify cache: %w", err)
	}
	if err := os.WriteFile(spotCachePath, data, 0644); err != nil {
		return fmt.Errorf("writing Spotify cache %s: %w", spotCachePath, err)
	}
	return nil
}

func getSpotifyAccessToken(cfg *Config) (string, error) {
	if cfg.SpotifyClientID == "" || cfg.SpotifyClientSecret == "" {
		return "", fmt.Errorf("missing spotify credentials")
	}

	if spotCachedToken != "" && time.Now().Before(spotTokenExpiry) {
		return spotCachedToken, nil
	}

	data := url.Values{}
	data.Set("grant_type", "client_credentials")

	req, err := http.NewRequest("POST", "https://accounts.spotify.com/api/token", strings.NewReader(data.Encode()))
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(cfg.SpotifyClientID, cfg.SpotifyClientSecret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := notifierClient(10 * time.Second)
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading Spotify token response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("Spotify token endpoint returned HTTP %d", resp.StatusCode)
	}
	var tResp spotifyTokenResponse
	if err := json.Unmarshal(body, &tResp); err != nil {
		return "", fmt.Errorf("decoding Spotify token response: %w", err)
	}
	if tResp.AccessToken == "" || tResp.ExpiresIn <= 0 {
		return "", fmt.Errorf("Spotify token response did not include a valid token and expiry")
	}

	spotCachedToken = tResp.AccessToken
	spotTokenExpiry = time.Now().Add(time.Duration(tResp.ExpiresIn-60) * time.Second)
	return spotCachedToken, nil
}

func checkSpotifyUpdates(s *discordgo.Session, cfg *Config) {
	if cfg.SpotifyClientID == "" || cfg.SpotifyClientSecret == "" {
		return
	}

	guilds := loadGuildSettings()
	spotMu.Lock()
	defer spotMu.Unlock()

	client := notifierClient(15 * time.Second)

	for guildID, gCfg := range guilds {
		if !gCfg.SpotifyEnabled || gCfg.SpotifyChannelID == "" {
			continue
		}

		token, err := getSpotifyAccessToken(cfg)
		if err != nil {
			log.Printf("Spotify token error: %v", err)
			return
		}

		req, err := http.NewRequest("GET", "https://api.spotify.com/v1/search?q=tag:new&type=album&limit=10&market=US", nil)
		if err != nil {
			log.Printf("Spotify notifier: create release request: %v", err)
			continue
		}
		req.Header.Set("Authorization", "Bearer "+token)

		resp, err := client.Do(req)
		if err != nil {
			log.Printf("Spotify notifier: fetch releases: %v", err)
			continue
		}

		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			log.Printf("Spotify notifier: read release response: %v", readErr)
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			log.Printf("Spotify notifier: release request returned HTTP %d", resp.StatusCode)
			continue
		}

		var relResp spotifyReleasesResponse
		if err := json.Unmarshal(body, &relResp); err != nil {
			log.Printf("Spotify notifier: decode release response: %v", err)
			continue
		}

		// Retrieve seen IDs for this guild
		previousEntry, hadPreviousEntry := spotCache[guildID]
		seen := append([]string(nil), previousEntry.SeenReleaseIDs...)

		seenSet := make(map[string]bool)
		for _, id := range seen {
			seenSet[id] = true
		}

		type releaseAnnouncement struct {
			id      string
			message string
		}
		var newAlbums []releaseAnnouncement
		for _, item := range relResp.Albums.Items {
			if item.ID == "" || seenSet[item.ID] {
				continue
			}
			var artistNames []string
			for _, a := range item.Artists {
				artistNames = append(artistNames, a.Name)
			}
			artists := strings.Join(artistNames, ", ")
			newAlbums = append(newAlbums, releaseAnnouncement{
				id:      item.ID,
				message: fmt.Sprintf("🎵 **%s** by **%s**\n%s", item.Name, artists, item.ExternalURLs.Spotify),
			})
		}

		if len(newAlbums) > 0 {
			header := "**🔥 New Spotify Releases Today:**\n"
			if gCfg.SpotifyRoleID != nil && *gCfg.SpotifyRoleID != "" {
				header = fmt.Sprintf("<@&%s> %s", *gCfg.SpotifyRoleID, header)
			}

			for _, album := range newAlbums {
				if _, err := sendNotifierMessage(s, gCfg.SpotifyChannelID, header+album.message); err != nil {
					log.Printf("Spotify notifier: send release %s to guild %s: %v", album.id, guildID, err)
					continue
				}

				seen = append(seen, album.id)
				seenSet[album.id] = true
				spotCache[guildID] = spotifyCacheEntry{SeenReleaseIDs: append([]string(nil), seen...)}
				if err := saveSpotifyCache(); err != nil {
					seen = seen[:len(seen)-1]
					delete(seenSet, album.id)
					if hadPreviousEntry {
						spotCache[guildID] = previousEntry
					} else {
						delete(spotCache, guildID)
					}
					log.Printf("Spotify notifier: persist delivered release %s for guild %s: %v", album.id, guildID, err)
				} else {
					previousEntry = spotifyCacheEntry{SeenReleaseIDs: append([]string(nil), seen...)}
					hadPreviousEntry = true
				}
			}
		}
	}
}

// startSpotifyNotifierDaemon runs daily Spotify release checks
func startSpotifyNotifierDaemon(ctx context.Context, s *discordgo.Session) {
	initSpotifyCache()
	cfg, err := loadConfig()
	if err != nil {
		log.Printf("Spotify notifier: load config: %v", err)
		return
	}
	if cfg.SpotifyClientID == "" || cfg.SpotifyClientSecret == "" {
		log.Printf("Spotify notifier not started: Spotify credentials are not configured")
		return
	}

	log.Printf("Spotify notifier daemon started (interval 24h)")
	runPeriodic(ctx, 30*time.Second, 24*time.Hour, func() { checkSpotifyUpdates(s, cfg) })
}

```

## `./battle.go`

```go
package main

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
)

type BattleSession struct {
	ChannelID string
	StarterID string
	Players   map[string]bool
	Started   bool
	CreatedAt time.Time
}

var (
	battleMu           sync.Mutex
	activeBattles      map[string]*BattleSession // channelID -> battle
	eliminationPhrases []string
)

func initBattle() {
	if len(eliminationPhrases) > 0 {
		return
	}
	path := dataPath("elimination-events.json")
	data, err := os.ReadFile(path)
	if err == nil {
		_ = json.Unmarshal(data, &eliminationPhrases)
	}
	if len(eliminationPhrases) == 0 {
		eliminationPhrases = []string{
			"had a nightmare and woke up suddenly! 🍬😰",
			"tripped over a moonbeam and tumbled out of the dream! 🌙💔",
			"got tangled in a blanket of stars and drifted away! ✨🌀",
			"was tickled awake by a swarm of giggling pixies! 🧚‍♀️😂",
		}
	}
	activeBattles = make(map[string]*BattleSession)
}

func handleBattleCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	initBattle()
	battleMu.Lock()
	defer battleMu.Unlock()

	options := i.ApplicationCommandData().Options
	if len(options) == 0 {
		return
	}

	sub := options[0]
	chID := i.ChannelID

	switch sub.Name {
	case "start":
		if b, ok := activeBattles[chID]; ok && b != nil {
			_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content: "🌙 A Sweet Dreams battle is already taking place or gathering dreamers in this channel!",
					Flags:   discordgo.MessageFlagsEphemeral,
				},
			})
			return
		}

		battle := &BattleSession{
			ChannelID: chID,
			StarterID: i.Member.User.ID,
			Players:   map[string]bool{i.Member.User.ID: true},
			Started:   false,
			CreatedAt: time.Now(),
		}
		activeBattles[chID] = battle

		startUnix := time.Now().Add(40 * time.Second).Unix()
		msg := fmt.Sprintf("🌙 **<@%s>** has started a Sweet Dreams battle!\nClick the button or use `/battle join` to enter the dreamscape...\n⏳ The dream will begin <t:%d:R>!", i.Member.User.ID, startUnix)

		joinBtn := discordgo.Button{
			CustomID: "battle:join",
			Label:    "Join Dream Battle",
			Style:    discordgo.SuccessButton,
			Emoji:    &discordgo.ComponentEmoji{Name: "💤"},
		}
		row := discordgo.ActionsRow{
			Components: []discordgo.MessageComponent{joinBtn},
		}

		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content:    msg,
				Components: []discordgo.MessageComponent{row},
			},
		})

		go runBattleCountdown(s, chID)

	case "join":
		b, ok := activeBattles[chID]
		if !ok || b == nil || b.Started {
			_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{Content: "😴 There is no open dream battle to join right now.", Flags: discordgo.MessageFlagsEphemeral},
			})
			return
		}

		uID := i.Member.User.ID
		if b.Players[uID] {
			_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{Content: "✨ You are already drifting in this dream!", Flags: discordgo.MessageFlagsEphemeral},
			})
			return
		}

		b.Players[uID] = true
		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: fmt.Sprintf("💤 **<@%s>** has joined the dream battle! (%d dreamers)", uID, len(b.Players)),
			},
		})
	}
}

func handleJoinBattleComponent(s *discordgo.Session, i *discordgo.InteractionCreate) {
	initBattle()
	battleMu.Lock()
	defer battleMu.Unlock()

	chID := i.ChannelID
	b, ok := activeBattles[chID]
	if !ok || b == nil || b.Started {
		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{Content: "😴 The dream has already begun or faded away.", Flags: discordgo.MessageFlagsEphemeral},
		})
		return
	}

	uID := i.Member.User.ID
	if b.Players[uID] {
		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{Content: "✨ You are already in this dream battle!", Flags: discordgo.MessageFlagsEphemeral},
		})
		return
	}

	b.Players[uID] = true
	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: fmt.Sprintf("💤 **<@%s>** entered the dream! (%d dreamers ready)", uID, len(b.Players)),
		},
	})
}

func runBattleCountdown(s *discordgo.Session, chID string) {
	time.Sleep(40 * time.Second)

	battleMu.Lock()
	b, ok := activeBattles[chID]
	if !ok || b == nil || b.Started {
		battleMu.Unlock()
		return
	}
	b.Started = true

	var players []string
	for p := range b.Players {
		players = append(players, p)
	}
	battleMu.Unlock()

	if len(players) < 2 {
		_, _ = s.ChannelMessageSend(chID, "Not enough dreamers joined. The dream fades away... 🌫️")
		battleMu.Lock()
		delete(activeBattles, chID)
		battleMu.Unlock()
		return
	}

	_, _ = s.ChannelMessageSend(chID, fmt.Sprintf("🌌 **The Sweet Dreams battle begins!** %d dreamers enter the dreamscape...", len(players)))

	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	for len(players) > 1 {
		time.Sleep(6 * time.Second)

		// Pick victim
		victimIdx := r.Intn(len(players))
		victim := players[victimIdx]
		players = append(players[:victimIdx], players[victimIdx+1:]...)

		phrase := eliminationPhrases[r.Intn(len(eliminationPhrases))]
		_, _ = s.ChannelMessageSend(chID, fmt.Sprintf("💤 <@%s> %s", victim, phrase))
	}

	time.Sleep(4 * time.Second)
	winner := players[0]
	embed := &discordgo.MessageEmbed{
		Title:       "🌟 Sweet Dreams Champion!",
		Description: fmt.Sprintf("Congratulations to <@%s> for having the sweetest dream and winning the battle! 🏆✨", winner),
		Color:       0x9B59B6,
	}
	_, _ = s.ChannelMessageSendEmbed(chID, embed)

	battleMu.Lock()
	delete(activeBattles, chID)
	battleMu.Unlock()
}

```

## `./notifier_hermes_test.go`

```go
package main

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
)

func TestHermesTipsParsingAndDeduplication(t *testing.T) {
	tmpDir := t.TempDir()
	origPath := hermesJSONPath
	origDelay := hermesRateLimitDelay
	oldHTTPClient := notifierHTTPClient
	notifierHTTPClient = &http.Client{Transport: notifierRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		response := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Request: req}
		switch req.URL.Host {
		case "hermes-agent.nousresearch.com":
			response.Body = io.NopCloser(strings.NewReader("- [Deterministic test](https://example.test): fixture tip\n"))
		case "api.github.com":
			response.Body = io.NopCloser(strings.NewReader(`[{"name":"v1","body":"- A deterministic release note with enough characters"}]`))
		default:
			t.Fatalf("unexpected Hermes test request: %s", req.URL)
		}
		return response, nil
	})}
	defer func() {
		hermesJSONPath = origPath
		hermesRateLimitDelay = origDelay
		notifierHTTPClient = oldHTTPClient
	}()

	hermesJSONPath = filepath.Join(tmpDir, "hermes_tips.json")
	hermesRateLimitDelay = 10 * time.Millisecond

	initHermesTipsState()
	if len(hermesState.Tips) != 0 || len(hermesState.PostedKeys) != 0 {
		t.Fatalf("expected empty initial state, got %+v", hermesState)
	}

	count1, err := checkHermesTipsUpdates(nil)
	if err != nil {
		t.Fatalf("checkHermesTipsUpdates failed: %v", err)
	}
	if count1 == 0 {
		t.Fatalf("expected new tips to be collected, got %d", count1)
	}
	if _, err := os.Stat(hermesJSONPath); err != nil {
		t.Fatalf("expected hermes_tips.json to exist at %s", hermesJSONPath)
	}

	count2, err := checkHermesTipsUpdates(nil)
	if err != nil {
		t.Fatalf("checkHermesTipsUpdates second pass failed: %v", err)
	}
	if count2 != 0 {
		t.Fatalf("expected 0 new tips on second pass, got %d (deduplication failed)", count2)
	}

	initHermesTipsState()
	if len(hermesState.PostedKeys) != count1 {
		t.Errorf("expected %d posted keys loaded from file, got %d", count1, len(hermesState.PostedKeys))
	}
}

func TestHermesNotifierMarksTipsPostedOnlyAfterSuccessfulDelivery(t *testing.T) {
	tmpDir := t.TempDir()
	oldPath, oldState, oldDelay, oldChan := hermesJSONPath, hermesState, hermesRateLimitDelay, HermesTipsChannelID
	oldClient, oldSend := notifierHTTPClient, sendNotifierMessage
	defer func() {
		hermesJSONPath = oldPath
		hermesState = oldState
		hermesRateLimitDelay = oldDelay
		HermesTipsChannelID = oldChan
		notifierHTTPClient = oldClient
		sendNotifierMessage = oldSend
	}()

	HermesTipsChannelID = "test-channel"

	hermesJSONPath = filepath.Join(tmpDir, "hermes_tips.json")
	hermesRateLimitDelay = time.Millisecond
	notifierHTTPClient = &http.Client{Transport: notifierRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		response := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Request: req}
		switch req.URL.Host {
		case "hermes-agent.nousresearch.com":
			response.Body = io.NopCloser(strings.NewReader("- [Delivery sequence](https://example.test): deterministic fixture\n"))
		case "api.github.com":
			response.Body = io.NopCloser(strings.NewReader("[]"))
		default:
			t.Fatalf("unexpected Hermes test request: %s", req.URL)
		}
		return response, nil
	})}
	if err := initHermesTipsState(); err != nil {
		t.Fatal(err)
	}
	failSend := true
	sendNotifierMessage = func(_ *discordgo.Session, _, _ string) (*discordgo.Message, error) {
		if failSend {
			return nil, io.ErrUnexpectedEOF
		}
		return &discordgo.Message{ID: "sent"}, nil
	}
	session := &discordgo.Session{}

	if _, err := checkHermesTipsUpdates(session); err != nil {
		t.Fatal(err)
	}
	if len(hermesState.PostedKeys) != 0 {
		t.Fatalf("failed Discord send was recorded as delivered: %#v", hermesState.PostedKeys)
	}

	failSend = false
	count, err := checkHermesTipsUpdates(session)
	if err != nil {
		t.Fatal(err)
	}
	if count == 0 || len(hermesState.PostedKeys) == 0 {
		t.Fatalf("successful Discord send was not recorded: count=%d state=%#v", count, hermesState)
	}
}


func TestHermesNotifierPausesWhenChannelNotConfigured(t *testing.T) {
	tmpDir := t.TempDir()
	oldPath, oldState, oldDelay, oldChan := hermesJSONPath, hermesState, hermesRateLimitDelay, HermesTipsChannelID
	defer func() {
		hermesJSONPath = oldPath
		hermesState = oldState
		hermesRateLimitDelay = oldDelay
		HermesTipsChannelID = oldChan
	}()

	hermesJSONPath = filepath.Join(tmpDir, "hermes_tips.json")
	hermesRateLimitDelay = time.Millisecond
	HermesTipsChannelID = ""

	session := &discordgo.Session{}
	count, err := checkHermesTipsUpdates(session)
	if err != nil {
		t.Fatalf("expected no error when channel not configured, got: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected 0 delivered tips when channel not configured, got: %d", count)
	}
}
```

## `./pick.go`

```go
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

```

## `./lyrics.go`

```go
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
)

const (
	geniusSearchURL     = "https://genius.com/api/search/multi"
	geniusSearchTimeout = 15 * time.Second
	geniusResultLimit   = 5
	geniusReplyLimit    = 1900
)

type geniusSearchResult struct {
	Title                 string `json:"title"`
	FullTitle             string `json:"full_title"`
	ArtistNames           string `json:"artist_names"`
	PrimaryArtistNames    string `json:"primary_artist_names"`
	ReleaseDateForDisplay string `json:"release_date_for_display"`
	URL                   string `json:"url"`
}

type geniusSearchResponse struct {
	Response struct {
		Sections []struct {
			Type string `json:"type"`
			Hits []struct {
				Result geniusSearchResult `json:"result"`
			} `json:"hits"`
		} `json:"sections"`
	} `json:"response"`
}

func searchGenius(kind, query string) ([]geniusSearchResult, error) {
	return searchGeniusWithClient(&http.Client{Timeout: geniusSearchTimeout}, geniusSearchURL, kind, query)
}

func searchGeniusWithClient(client *http.Client, endpoint, kind, query string) ([]geniusSearchResult, error) {
	if kind != "song" && kind != "album" {
		return nil, fmt.Errorf("unsupported Genius search type %q", kind)
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("search query cannot be empty")
	}

	parsedEndpoint, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("invalid Genius search endpoint: %w", err)
	}
	params := parsedEndpoint.Query()
	params.Set("q", query)
	parsedEndpoint.RawQuery = params.Encode()

	req, err := http.NewRequest(http.MethodGet, parsedEndpoint.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("creating Genius search request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Sunfire/1.0 (Discord lyrics metadata search)")

	if client == nil {
		client = &http.Client{Timeout: geniusSearchTimeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("requesting Genius search: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Genius search returned HTTP %d", resp.StatusCode)
	}

	var payload geniusSearchResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decoding Genius search response: %w", err)
	}

	results := make([]geniusSearchResult, 0, geniusResultLimit)
	seenURLs := make(map[string]struct{})
	for _, section := range payload.Response.Sections {
		if section.Type != kind {
			continue
		}
		for _, hit := range section.Hits {
			result := hit.Result
			result.URL = validGeniusResultURL(result.URL)
			if result.URL == "" {
				continue
			}
			if _, exists := seenURLs[result.URL]; exists {
				continue
			}
			seenURLs[result.URL] = struct{}{}
			results = append(results, result)
			if len(results) == geniusResultLimit {
				return results, nil
			}
		}
	}
	return results, nil
}

func validGeniusResultURL(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" {
		return ""
	}
	host := strings.ToLower(parsed.Hostname())
	if (host != "genius.com" && !strings.HasSuffix(host, ".genius.com")) || parsed.User != nil || len(rawURL) > 512 {
		return ""
	}
	return parsed.String()
}

func handleLyrics(s *discordgo.Session, i *discordgo.InteractionCreate) {
	data := i.ApplicationCommandData()
	if len(data.Options) == 0 {
		lyricsReply(s, i, "Choose `/lyrics fetch-song` or `/lyrics fetch-album`.")
		return
	}

	sub := data.Options[0]
	var kind, query string
	switch sub.Name {
	case "fetch-song":
		kind = "song"
		query = optionStringFrom(sub, "song")
	case "fetch-album":
		kind = "album"
		artist := optionStringFrom(sub, "artist")
		album := optionStringFrom(sub, "album")
		query = strings.TrimSpace(strings.Join([]string{artist, album}, " "))
	default:
		lyricsReply(s, i, "Unknown `/lyrics` action. Try `/lyrics fetch-song` or `/lyrics fetch-album`.")
		return
	}
	if strings.TrimSpace(query) == "" {
		lyricsReply(s, i, "❌ Enter a search term.")
		return
	}

	if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	}); err != nil {
		log.Printf("lyrics: acknowledging interaction failed: %v", err)
		return
	}

	results, err := searchGenius(kind, query)
	if err != nil {
		log.Printf("lyrics: Genius %s search failed for %q: %v", kind, query, err)
		lyricsFollowUp(s, i, "❌ Genius search failed. Please try again later.")
		return
	}
	lyricsFollowUp(s, i, formatGeniusSearchResults(kind, query, results))
}

func formatGeniusSearchResults(kind, query string, results []geniusSearchResult) string {
	label := "song"
	if kind == "album" {
		label = "album"
	}
	if len(results) == 0 {
		return fmt.Sprintf("No %ss found on Genius for **%s**.", label, discordSafeText(query))
	}

	var out strings.Builder
	fmt.Fprintf(&out, "Genius %s search results for **%s** (metadata and links only):\n", label, boundedGeniusText(query, 250))
	omitted := "\n… Additional results omitted to fit Discord's message limit."
	for index, result := range results {
		title := result.Title
		if title == "" {
			title = result.FullTitle
		}
		if title == "" {
			title = "Untitled"
		}
		artist := result.PrimaryArtistNames
		if artist == "" {
			artist = result.ArtistNames
		}
		var item strings.Builder
		fmt.Fprintf(&item, "\n• **%s**", boundedGeniusText(title, 120))
		if artist != "" {
			fmt.Fprintf(&item, " — %s", boundedGeniusText(artist, 100))
		}
		if result.ReleaseDateForDisplay != "" {
			fmt.Fprintf(&item, " · %s", boundedGeniusText(result.ReleaseDateForDisplay, 48))
		}
		fmt.Fprintf(&item, "\n  <%s>\n", result.URL)
		nextLength := len([]rune(out.String())) + len([]rune(item.String()))
		if nextLength > geniusReplyLimit || (index < len(results)-1 && nextLength+len([]rune(omitted)) > geniusReplyLimit) {
			out.WriteString(omitted)
			break
		}
		out.WriteString(item.String())
	}
	return out.String()
}

func discordSafeText(value string) string {
	value = strings.ReplaceAll(value, "@", "@\u200b")
	value = strings.ReplaceAll(value, "`", "'")
	return strings.Join(strings.Fields(value), " ")
}

func boundedGeniusText(value string, limit int) string {
	runes := []rune(discordSafeText(value))
	if len(runes) <= limit {
		return string(runes)
	}
	return string(runes[:limit-1]) + "…"
}

func lyricsReply(s *discordgo.Session, i *discordgo.InteractionCreate, content string) {
	if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Content: content},
	}); err != nil {
		log.Printf("lyrics: replying to interaction failed: %v", err)
	}
}

func lyricsFollowUp(s *discordgo.Session, i *discordgo.InteractionCreate, content string) {
	if _, err := s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{Content: content}); err != nil {
		log.Printf("lyrics: sending search result failed: %v", err)
	}
}

```

## `./main.go`

```go
// Sunfire — Daniel Kill's personal Discord bot (Go / discordgo).
// Primary server: Sunfire (1557930395868209162)
// Bot owner: Daniel Kill (daniel@danielkill.online)
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"sunfire/a2e"
	"sunfire/providers"
	"sunfire/venice"
)

func main() {
	token, err := botToken()
	if err != nil {
		log.Fatalf("failed to get bot token: %v", err)
	}
	if token == "" || token == "PASTE_YOUR_BOT_TOKEN_HERE" {
		log.Fatal("bot token not configured")
	}

	if err := a2e.Init(); err != nil {
		log.Printf("warning: a2e subpackage init: %v", err)
	}
	if err := venice.Init(); err != nil {
		log.Printf("warning: venice subpackage init: %v", err)
	}
	if err := providers.Init(); err != nil {
		log.Printf("warning: providers subpackage init: %v", err)
	}

	// Populate command specs (including /openrouter) before creating the bot.
	buildCommandSpecs()

	bot, err := NewBot(token)
	if err != nil {
		log.Fatalf("failed to create bot: %v", err)
	}
	defer bot.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := bot.Open(); err != nil {
		log.Fatalf("cannot open session: %v", err)
	}
	<-ctx.Done()
	log.Println("shutdown signal received")
}

```

## `./anyapi.go`

```go
// anyapi.go — the /any command: fan out the user's query to multiple API
// providers concurrently and return the first successful response.
//
// Providers are configured in .private/config.json:
//
//	"anyApi": [
//	  {"name": "wikipedia", "url": "https://en.wikipedia.org/api/rest_v1/page/summary/{query}", ...},
//	  ...
//	]
//
// The {query} placeholder in each URL is URL-escaped. All providers are hit in
// parallel with goroutines; the first 2xx response wins, so latency is bounded
// by the fastest provider, not the slowest — the "lightspeed" payoff.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
)

const anyAPITimeout = 8 * time.Second

// anyAPIResult is the outcome of one provider call.
type anyAPIResult struct {
	provider string
	body     string
	status   int
	err      error
	query    string // the (normalized) search term, for no-results messages
}

// anyAPIResponse builds the content for a /any response from a result.
// It parses the body according to which provider produced it, so the reply is
// a clean summary rather than a raw JSON dump.
func anyAPIResponse(res anyAPIResult) string {
	if res.err != nil {
		return fmt.Sprintf("`%s` error: %v", res.provider, res.err)
	}
	switch res.provider {
	case "Wikipedia":
		if s := formatWikipedia(res.body); s != "" {
			return s
		}
	case "DuckDuckGo Instant Answer", "DuckDuckGo":
		if s := formatDuckDuckGo(res.body); s != "" {
			return s
		}
	case "SWAPI (Star Wars)", "Rick and Morty", "Dad Jokes", "Chuck Norris":
		if s := formatGenericList(res.provider, res.body); s != "" {
			return s
		}
		// Known list provider returned a 2xx but no parseable results — give a
		// clean "no results" line (with the search term) instead of raw JSON.
		term := res.query
		if term == "" {
			term = "that query"
		}
		return fmt.Sprintf("**%s** — no results found for `%s`.", res.provider, term)
	}
	// Fallback: unknown provider or unparseable body — show a short raw preview.
	body := res.body
	if len(body) > 800 {
		body = body[:800] + "…"
	}
	return fmt.Sprintf("**%s** (HTTP %d)\n```\n%s\n```", res.provider, res.status, body)
}

// formatWikipedia renders a Wikipedia REST summary JSON into a clean reply:
// the title as a bold header, a link, and the extract as body text.
func formatWikipedia(body string) string {
	var d struct {
		Title       string `json:"title"`
		Extract     string `json:"extract"`
		Description string `json:"description"`
		ContentURLs struct {
			Desktop struct {
				Page string `json:"page"`
			} `json:"desktop"`
		} `json:"content_urls"`
	}
	if err := json.Unmarshal([]byte(body), &d); err != nil || d.Extract == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString("**" + d.Title + "**")
	if d.Description != "" {
		b.WriteString(" — " + d.Description)
	}
	b.WriteString("\n\n")
	b.WriteString(d.Extract)
	if d.ContentURLs.Desktop.Page != "" {
		b.WriteString("\n\n" + d.ContentURLs.Desktop.Page)
	}
	return truncateContent(b.String())
}

// formatDuckDuckGo renders a DDG Instant Answer JSON into a clean reply:
// the Heading, any Answer/Abstract, then the top RelatedTopics as bullets.
func formatDuckDuckGo(body string) string {
	var d struct {
		Heading       string            `json:"Heading"`
		Answer        string            `json:"Answer"`
		AbstractText  string            `json:"AbstractText"`
		Definition    string            `json:"Definition"`
		RelatedTopics []json.RawMessage `json:"RelatedTopics"`
		AbstractURL   string            `json:"AbstractURL"`
	}
	if err := json.Unmarshal([]byte(body), &d); err != nil {
		return ""
	}
	// If DDG had no answer fields, don't format it (caller falls back to raw).
	if d.Heading == "" && d.Answer == "" && d.AbstractText == "" && len(d.RelatedTopics) == 0 {
		return ""
	}
	var b strings.Builder
	if d.Heading != "" {
		b.WriteString("**" + d.Heading + "**\n\n")
	}
	if d.Answer != "" {
		b.WriteString(d.Answer + "\n")
	}
	if d.AbstractText != "" {
		b.WriteString(d.AbstractText + "\n")
	}
	if d.Definition != "" {
		b.WriteString("*" + d.Definition + "*\n")
	}
	// RelatedTopics can contain both flat objects and nested {Name, Topics}.
	var topics []string
	appendTopic := func(tm json.RawMessage) {
		var t struct {
			Text string `json:"Text"`
		}
		if err := json.Unmarshal(tm, &t); err == nil && t.Text != "" {
			topics = append(topics, t.Text)
		}
	}
	for _, rm := range d.RelatedTopics {
		// Distinguish a flat topic from a nested group.
		var asMap map[string]json.RawMessage
		_ = json.Unmarshal(rm, &asMap)
		if nested, ok := asMap["Topics"]; ok {
			var inner []json.RawMessage
			if err := json.Unmarshal(nested, &inner); err == nil {
				for _, it := range inner {
					appendTopic(it)
				}
			}
			continue
		}
		appendTopic(rm)
	}
	if len(topics) > 0 {
		b.WriteString("\n")
		limit := len(topics)
		if limit > 5 {
			limit = 5
		}
		for _, t := range topics[:limit] {
			b.WriteString("• " + t + "\n")
		}
		if len(topics) > limit {
			b.WriteString("… and " + itoa(len(topics)-limit) + " more\n")
		}
	}
	return truncateContent(b.String())
}

// formatGenericList renders a "list of results" API (SWAPI, Rick and Morty,
// Dad Jokes, Chuck Norris) into a clean bulleted reply. These all return an
// array under a "results"/"result" key, where each object has a "name" and/or
// "joke"/"value" field. We pick the most descriptive field per provider.
func formatGenericList(provider, body string) string {
	var d struct {
		Results []map[string]any `json:"results"`
		Result  []map[string]any `json:"result"`
	}
	if err := json.Unmarshal([]byte(body), &d); err != nil {
		return ""
	}
	items := d.Results
	if len(items) == 0 {
		items = d.Result
	}
	if len(items) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("**" + provider + "**\n")
	limit := len(items)
	if limit > 5 {
		limit = 5
	}
	for _, it := range items[:limit] {
		line := ""
		switch provider {
		case "Dad Jokes", "Chuck Norris":
			line, _ = it["joke"].(string)
			if line == "" {
				line, _ = it["value"].(string)
			}
		default: // SWAPI, Rick and Morty -> "name"
			line, _ = it["name"].(string)
		}
		if line == "" {
			continue
		}
		b.WriteString("• " + line + "\n")
	}
	if len(items) > limit {
		b.WriteString("… and " + itoa(len(items)-limit) + " more\n")
	}
	return truncateContent(b.String())
}

// truncateContent cuts a formatted reply to fit Discord's 2000-char limit.
func truncateContent(s string) string {
	const max = 1900
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

// itoa is a tiny helper to avoid importing strconv in the hot path.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	if neg {
		digits = append([]byte{'-'}, digits...)
	}
	return string(digits)
}

// fanOutAny hits every provider concurrently, returns the first success.
func fanOutAny(query string, providers []AnyAPIProvider) (anyAPIResult, bool) {
	if len(providers) == 0 {
		return anyAPIResult{provider: "none", err: fmt.Errorf("no providers configured")}, false
	}
	results := make(chan anyAPIResult, len(providers))
	var wg sync.WaitGroup
	for _, p := range providers {
		p := p
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- callProvider(query, p)
		}()
	}
	// Close the channel once all providers finish.
	go func() {
		wg.Wait()
		close(results)
	}()
	// Return the first USEFUL success. A 2xx with an empty/trivial body is not a
	// win (e.g. DuckDuckGo's empty JSON for unanswered queries) — keep looking.
	var firstErr anyAPIResult
	gotErr := false
	for r := range results {
		if r.err == nil && r.status >= 200 && r.status < 300 && useful(r.body) {
			return r, true
		}
		// Only remember genuine errors. A useless 2xx (r.err == nil) must NOT
		// count as an error — otherwise a single useless-200 provider would set
		// gotErr=true and we'd return it with a nil err (panic in the handler).
		if r.err != nil && !gotErr {
			firstErr = r
			gotErr = true
		}
	}
	// No useful result. Report a real error if we saw one; otherwise say all
	// providers returned empty/useless responses (never return a nil err).
	if !gotErr {
		firstErr = anyAPIResult{provider: "all", err: fmt.Errorf("all providers returned empty/useless responses")}
	}
	return firstErr, false
}

// useful reports whether a response body is worth surfacing. Empty or
// near-empty bodies (and bodies that are just an empty JSON object / array or
// whitespace) are treated as useless so the /any command doesn't return
// garbage just because some API answered 200 with nothing.
func useful(body string) bool {
	b := strings.TrimSpace(body)
	if b == "" {
		return false
	}
	// Empty JSON object or array:  {}  []
	if b == "{}" || b == "[]" || b == "null" {
		return false
	}
	// JSON object: useful only if it has at least one content-bearing value.
	if strings.HasPrefix(b, "{") {
		if !json.Valid([]byte(b)) {
			return false
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(b), &m); err == nil && len(m) > 0 {
			// Semantic check: DuckDuckGo's Instant Answer API returns this
			// exact "no answer" shape when it has nothing — all the answer
			// fields empty, only boilerplate meta. Treat it as useless.
			if isDDGNoAnswer(m) {
				return false
			}
			// List-style APIs (Dad Jokes, Chuck Norris, SWAPI, Rick & Morty)
			// return a "results"/"result" array. If that array is empty, the
			// response has no real content even if pagination metadata
			// (current_page, limit, total) is present — treat it as useless so
			// it doesn't win over a provider that DID return results.
			if isEmptyContentArray(m) {
				return false
			}
			return hasContent(m)
		}
	}
	// JSON array: useful if it has a non-empty element.
	if strings.HasPrefix(b, "[") && json.Valid([]byte(b)) {
		var arr []any
		if err := json.Unmarshal([]byte(b), &arr); err == nil {
			return hasContent(arr)
		}
	}
	// Anything else with real content is fine.
	return len([]rune(b)) >= 1
}

// isDDGNoAnswer detects DuckDuckGo's Instant Answer "no result" response:
// the answer-bearing fields (Abstract/Answer/Definition/Heading) are all
// empty AND the topic lists are empty. The `meta` block is boilerplate.
//
// Guard: only applies when the object is actually a DDG Instant Answer
// response (has the "meta" object). Without this guard, ANY JSON object that
// happens to lack these keys (e.g. {"ok":true,"value":"fast"}) would be
// wrongly classified as a "no answer".
func isDDGNoAnswer(m map[string]any) bool {
	if _, hasMeta := m["meta"]; !hasMeta {
		return false
	}
	str := func(k string) string {
		s, _ := m[k].(string)
		return s
	}
	empty := func(k string) bool { return strings.TrimSpace(str(k)) == "" }
	emptyArr := func(k string) bool {
		a, _ := m[k].([]any)
		return len(a) == 0
	}
	return empty("Abstract") && empty("Answer") && empty("Definition") &&
		empty("Heading") && emptyArr("RelatedTopics") && emptyArr("Results")
}

// isEmptyContentArray reports whether a JSON object has a "results" or
// "result" key that is an empty array. Used to reject list-style API responses
// that return no actual content (only pagination/boilerplate metadata).
func isEmptyContentArray(m map[string]any) bool {
	for _, key := range []string{"results", "result", "docs"} {
		if arr, ok := m[key].([]any); ok {
			return len(arr) == 0
		}
	}
	return false
}

// hasContent recursively reports whether a decoded JSON value contains any
// meaningful content (non-empty strings, non-empty arrays/maps of content,
// non-zero numbers, or booleans).
func hasContent(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case string:
		return strings.TrimSpace(t) != ""
	case bool:
		return true
	case float64:
		return t != 0
	case []any:
		for _, e := range t {
			if hasContent(e) {
				return true
			}
		}
		return false
	case map[string]any:
		for _, val := range t {
			if hasContent(val) {
				return true
			}
		}
		return false
	}
	return false
}

// callProvider requests one provider and returns the result.
func callProvider(query string, p AnyAPIProvider) anyAPIResult {
	u := strings.ReplaceAll(p.URL, "{query}", url.QueryEscape(query))
	timeout := p.Timeout
	if timeout <= 0 {
		timeout = 8
	}
	client := &http.Client{Timeout: time.Duration(timeout) * time.Second}
	// All our providers return JSON; explicitly request JSON so we never get a
	// default HTML page (e.g. icanhazdadjoke serves HTML unless Accept is set).
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return anyAPIResult{provider: p.Name, err: err}
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "ElyLightspeed/1.0 (+https://github.com/daniel-kill/ElyLightspeed)")
	resp, err := client.Do(req)
	if err != nil {
		return anyAPIResult{provider: p.Name, err: err}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return anyAPIResult{provider: p.Name, status: resp.StatusCode, err: err}
	}
	res := anyAPIResult{provider: p.Name, body: string(body), status: resp.StatusCode, query: query}

	// Wikipedia enhancement: the REST summary endpoint needs a page TITLE, so
	// question-phrases ("what is a zebra") 404. If we got a 404 (or useless
	// content), search for the best-matching page title and re-fetch its
	// summary. This makes `/any <question>` return real content.
	if p.Name == "Wikipedia" && (res.status == 404 || !useful(res.body)) {
		if summary := wikipediaSearchSummary(query, client); summary != "" {
			res.body = summary
			res.status = 200
		}
	}
	return res
}

// wikipediaSearchSummary uses the Wikipedia search API to find the best page
// title for a natural-language query, then fetches that page's REST summary.
// Returns "" if nothing usable is found.
func wikipediaSearchSummary(query string, client *http.Client) string {
	searchURL := "https://en.wikipedia.org/w/api.php?action=query&list=search&srsearch=" +
		url.QueryEscape(query) + "&srlimit=1&format=json&formatversion=2"
	resp, err := client.Get(searchURL)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return ""
	}
	var sr struct {
		Query struct {
			Search []struct {
				Title string `json:"title"`
			} `json:"search"`
		} `json:"query"`
	}
	if err := json.Unmarshal(body, &sr); err != nil {
		return ""
	}
	if len(sr.Query.Search) == 0 {
		return ""
	}
	title := sr.Query.Search[0].Title
	summaryURL := "https://en.wikipedia.org/api/rest_v1/page/summary/" + url.PathEscape(title)
	resp2, err := client.Get(summaryURL)
	if err != nil {
		return ""
	}
	defer resp2.Body.Close()
	body2, err := io.ReadAll(io.LimitReader(resp2.Body, 64<<10))
	if err != nil {
		return ""
	}
	if resp2.StatusCode != 200 || !useful(string(body2)) {
		return ""
	}
	return string(body2)
}

// anyAPIConfigProviders returns the configured providers (empty if none).
func anyAPIConfigProviders() []AnyAPIProvider {
	cfg, err := loadConfig()
	if err != nil {
		return nil
	}
	return cfg.AnyAPI
}

// normalizeQuery strips leading question words / stopwords and trailing
// punctuation so the underlying APIs get a clean subject to search for.
// Examples: "what is a zebra" -> "zebra", "tell me about Go" -> "Go",
// "define photosynthesis" -> "photosynthesis".
func normalizeQuery(q string) string {
	q = strings.TrimSpace(q)
	q = strings.TrimSuffix(q, "?")
	q = strings.TrimSpace(q)
	// Repeatedly strip leading stopwords. Check multi-word openers first
	// (e.g. "tell me about", "how does") so they're consumed as one unit.
	for {
		fields := strings.Fields(q)
		if len(fields) == 0 {
			return ""
		}
		// Three-word opener: "tell me about X".
		if len(fields) >= 3 {
			three := strings.ToLower(fields[0] + " " + fields[1] + " " + fields[2])
			if isStopword(three) {
				q = strings.TrimSpace(strings.TrimPrefix(q, fields[0]+" "+fields[1]+" "+fields[2]))
				continue
			}
		}
		// Two-word opener like "what is", "how do", "tell me".
		if len(fields) >= 2 {
			two := strings.ToLower(fields[0] + " " + fields[1])
			if isStopword(two) {
				q = strings.TrimSpace(strings.TrimPrefix(q, fields[0]+" "+fields[1]))
				continue
			}
		}
		first := strings.ToLower(fields[0])
		if isStopword(first) {
			q = strings.TrimSpace(strings.TrimPrefix(q, fields[0]))
			continue
		}
		break
	}
	return q
}

// isStopword reports whether w is a common English question/stop word.
func isStopword(w string) bool {
	switch w {
	case "what", "whats", "what is", "what are", "what's",
		"who", "whos", "who is", "who's",
		"where", "wheres", "where is", "where's",
		"when", "whens", "when is", "when's",
		"why", "whys", "why is", "why's",
		"how", "hows", "how is", "how's", "how do", "how does", "how to",
		"is", "are", "am", "was", "were", "be", "being", "been",
		"a", "an", "the", "of", "and", "or", "for", "in", "on", "at", "to", "with",
		"tell", "tell me", "tell me about", "explain", "explain about",
		"define", "describe", "about", "please", "do", "does", "did",
		"can", "could", "would", "should", "may", "might", "will", "shall":
		return true
	}
	return false
}

// helpAny returns the usage guide shown by `/any help`.
func helpAny() string {
	return "**/any — query multiple APIs at once**\n\n" +
		"`/any <query>` sends your query to all configured APIs **in parallel** and returns the best result.\n\n" +
		"**Tips for good results:**\n" +
		"• Use a **subject, not a full question**: `/any zebra` works better than `/any what is a zebra` (the bot strips filler words, but a clean subject is best).\n" +
		"• **Time**: `/any what time is it in Manila` → live time for 55+ cities.\n" +
		"• **Definitions**: `/any <word>` → Wikipedia/DuckDuckGo summary.\n" +
		"• **Random jokes**: `/any dad` and `/any chuck` always return a random joke from the dedicated joke APIs.\n\n" +
		"**What each provider answers:**\n" +
		"• **Wikipedia** — encyclopedia summaries: `/any zebra`\n" +
		"• **DuckDuckGo** — instant answers + related topics: `/any Germany`\n" +
		"• **SWAPI (Star Wars)** — characters: `/any luke`\n" +
		"• **Rick and Morty** — characters: `/any rick`\n" +
		"• **Dad Jokes** — random dad joke: `/any dad`\n" +
		"• **Chuck Norris** — random Chuck Norris joke: `/any chuck`\n\n" +
		"**Examples:**\n" +
		"`/any zebra` · `/any what time is it in Tokyo` · `/any luke` · `/any rick`\n" +
		"`/any Germany` · `/any python` · `/any dad` · `/any chuck`\n\n" +
		"_The bot returns the first useful result across all providers._"
}

// handleAny is the /any slash-command handler.
func handleAny(s *discordgo.Session, i *discordgo.InteractionCreate) {
	// The command takes a "query" string option.
	query := ""
	if opts := i.ApplicationCommandData().Options; len(opts) > 0 {
		query = opts[0].StringValue()
	}
	query = strings.TrimSpace(query)
	// Empty query or explicit "help" -> show usage guide.
	lower := strings.ToLower(query)
	if query == "" || lower == "help" || lower == "help me" || lower == "?" {
		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{Content: helpAny()},
		})
		return
	}

	// Answer "what time is it in <place>" directly (the time API doesn't use a
	// {query} placeholder, so it's handled specially before the fan-out).
	if handleTime(s, i, query) {
		return
	}

	// "/any dad" and "/any chuck" always return a random joke from the
	// dedicated joke APIs.
	if handleJokeQuery(s, i, query) {
		return
	}

	// Strip leading question words/stopwords ("what is a zebra" -> "zebra")
	// so Wikipedia and DDG get a clean subject to search for.
	query = normalizeQuery(query)

	providers := anyAPIConfigProviders()
	res, ok := fanOutAny(query, providers)
	if !ok {
		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "❌ No API results — " + res.err.Error(),
			},
		})
		return
	}
	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Content: anyAPIResponse(res)},
	})
}

// makeAnyAPIJSON is a small helper to demonstrate the config shape (used by
// tests/docs); not required at runtime.
func makeAnyAPIJSON(providers []AnyAPIProvider) string {
	b, _ := json.MarshalIndent(providers, "", "  ")
	return string(b)
}

```

## `./service.go`

```go
package main

import (
	"context"
	"sync"
	"time"
)

func runPeriodic(ctx context.Context, initialDelay, interval time.Duration, task func()) {
	if ctx.Err() != nil {
		return
	}
	if initialDelay > 0 {
		timer := time.NewTimer(initialDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
	if ctx.Err() != nil {
		return
	}

	task()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			task()
		}
	}
}

type workerLifecycle struct {
	mu         sync.Mutex
	started    bool
	cancelFunc context.CancelFunc
	wg         sync.WaitGroup
}

func (w *workerLifecycle) start(parent context.Context, workers ...func(context.Context)) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.started {
		return
	}
	ctx, cancel := context.WithCancel(parent)
	w.cancelFunc = cancel
	w.started = true
	for _, worker := range workers {
		worker := worker
		w.wg.Add(1)
		go func() {
			defer w.wg.Done()
			worker(ctx)
		}()
	}
}

func (w *workerLifecycle) stop(timeout time.Duration) {
	w.cancel()
	w.wait(timeout)
}

func (w *workerLifecycle) cancel() {
	w.mu.Lock()
	cancel := w.cancelFunc
	w.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (w *workerLifecycle) wait(timeout time.Duration) {
	done := make(chan struct{})
	go func() {
		w.wg.Wait()
		close(done)
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
	}
}

```

## `./commands.go`

```go
// commands.go — slash command definitions, registration, and handling.
package main

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"

	"sunfire/a2e"
	"sunfire/providers"
	"sunfire/venice"
)

// commandSpec couples a command definition with its handler.
type commandSpec struct {
	Command *discordgo.ApplicationCommand
	Handler func(s *discordgo.Session, i *discordgo.InteractionCreate)
}

// buildCommandSpecs populates the commandSpecs list.
// Split from init() because "init" is a reserved auto-call name in this Go variant.
var commandSpecs []commandSpec

func buildCommandSpecs() {
	commandSpecs = []commandSpec{
		{
			Command: &discordgo.ApplicationCommand{
				Name:        "help",
				Description: "Browse commands by category or find one by name.",
				Options: []*discordgo.ApplicationCommandOption{
					{
						Type:        discordgo.ApplicationCommandOptionString,
						Name:        "category",
						Description: "Browse a command category",
						Required:    false,
						Choices:     helpCategoryChoices(),
					},
					{
						Type:        discordgo.ApplicationCommandOptionString,
						Name:        "search",
						Description: "Find commands whose names contain this text (letters, numbers, _ or -)",
						Required:    false,
						MaxLength:   32,
					},
				},
			},
			Handler: handleHelp,
		},
		{
			Command: &discordgo.ApplicationCommand{
				Name:        "userinfo",
				Description: "Show available Discord profile, server, activity, and voice information.",
				Options: []*discordgo.ApplicationCommandOption{
					{Type: discordgo.ApplicationCommandOptionUser, Name: "user", Description: "The user to get info for", Required: false},
				},
			},
			Handler: handleUserInfo,
		},
		{
			Command: &discordgo.ApplicationCommand{
				Name:        "media",
				Description: "Apply an FFmpeg effect to an uploaded image, audio, or video file.",
				Options: []*discordgo.ApplicationCommandOption{
					{Type: discordgo.ApplicationCommandOptionAttachment, Name: "file", Description: "Media file to transform", Required: true},
				},
			},
			Handler: handleMedia,
		},
		{
			Command: &discordgo.ApplicationCommand{
				Name:        "emojigg",
				Description: "Browse downloaded Emoji.gg assets and add them to the server.",
				Options: []*discordgo.ApplicationCommandOption{
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "search",
						Description: "Find emoji assets by name or asset ID.",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionString, Name: "query", Description: "Name or Emoji.gg asset ID to search for", Required: true, MaxLength: 32},
							{Type: discordgo.ApplicationCommandOptionInteger, Name: "page", Description: "Page number when there are more than 15 matches", Required: false},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "list",
						Description: "Browse downloaded emoji assets by page.",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionInteger, Name: "page", Description: "Page number (15 assets per page)", Required: false},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "random",
						Description: "Preview a random downloaded emoji.",
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "preview",
						Description: "Preview a downloaded emoji by name or asset ID.",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionString, Name: "asset", Description: "Emoji name or Emoji.gg asset ID from search/list", Required: true, MaxLength: 64},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "add",
						Description: "Add a downloaded emoji to the server (Manage Expressions required).",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionString, Name: "asset", Description: "Emoji name or Emoji.gg asset ID from search/list", Required: true, MaxLength: 64},
							{Type: discordgo.ApplicationCommandOptionString, Name: "name", Description: "Optional server emoji name (letters, numbers, underscores)", Required: false, MaxLength: 32},
						},
					},
				},
			},
			Handler: handleEmojiGG,
		},
		{
			Command: &discordgo.ApplicationCommand{
				Name:        "purge",
				Description: "Purge messages (amount: number or 'all', max 10000).",
				Options: []*discordgo.ApplicationCommandOption{
					{Type: discordgo.ApplicationCommandOptionString, Name: "amount", Description: "Number of messages to purge, or 'all'", Required: true},
				},
			},
			Handler: handlePurge,
		},
		{Command: &discordgo.ApplicationCommand{Name: "setup", Description: "Create private Sunfire category/logs and read-only INFO/rules channels."}, Handler: handleSetup},
		{Command: &discordgo.ApplicationCommand{Name: "dedup", Description: "Run SHA-256 duplicate media cleanup on server media logs."}, Handler: handleDedup},
		{
			Command: &discordgo.ApplicationCommand{
				Name:        "grant-access",
				Description: "Grant or remove a member's access to one restricted command.",
				Options: []*discordgo.ApplicationCommandOption{
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "grant",
						Description: "Grant a member access to a restricted command.",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionString, Name: "command", Description: "Restricted command name (without /)", Required: true},
							{Type: discordgo.ApplicationCommandOptionUser, Name: "member", Description: "Member to grant access to", Required: true},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "remove",
						Description: "Remove a member's access to a restricted command.",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionString, Name: "command", Description: "Restricted command name (without /)", Required: true},
							{Type: discordgo.ApplicationCommandOptionUser, Name: "member", Description: "Member to remove access from", Required: true},
						},
					},
				},
			},
			Handler: handleGrantAccess,
		},

		// --- SYSTEM & LOGGING COMMANDS ---
		{Command: &discordgo.ApplicationCommand{Name: "sysinfo", Description: "Show live host system resources and uptime."}, Handler: handleSysInfo},
		{
			Command: &discordgo.ApplicationCommand{
				Name:        "remind",
				Description: "Add, list, and delete your reminders.",
				Options: []*discordgo.ApplicationCommandOption{
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "add",
						Description: "Schedule a reminder.",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionString, Name: "when", Description: "e.g. '10m', '1h', or '09/29/2026 at 14:30'", Required: true},
							{Type: discordgo.ApplicationCommandOptionString, Name: "message", Description: "What to remind you about", Required: true, MaxLength: 1800},
							{Type: discordgo.ApplicationCommandOptionString, Name: "repeat", Description: "Optional interval, e.g. '1d' or '1w'"},
							{Type: discordgo.ApplicationCommandOptionString, Name: "timezone", Description: "IANA timezone for local date/time (default: America/New_York)"},
						},
					},
					{Type: discordgo.ApplicationCommandOptionSubCommand, Name: "list", Description: "List your scheduled reminders."},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "delete",
						Description: "Delete one of your reminders.",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionString, Name: "id", Description: "Reminder ID shown by /remind list", Required: true},
						},
					},
				},
			},
			Handler: handleRemind,
		},
		{
			Command: &discordgo.ApplicationCommand{
				Name:        "searchlogs",
				Description: "Search a user's logged chat and edit history.",
				Options: []*discordgo.ApplicationCommandOption{
					{Type: discordgo.ApplicationCommandOptionUser, Name: "user", Description: "User to search", Required: true},
					{Type: discordgo.ApplicationCommandOptionString, Name: "keyword", Description: "Keyword to filter by", Required: false},
				},
			},
			Handler: handleSearchLogs,
		},
		{
			Command: &discordgo.ApplicationCommand{
				Name:        "warn",
				Description: "Issue a warning strike to a user.",
				Options: []*discordgo.ApplicationCommandOption{
					{Type: discordgo.ApplicationCommandOptionUser, Name: "user", Description: "User to warn", Required: true},
					{Type: discordgo.ApplicationCommandOptionString, Name: "reason", Description: "Reason for warning", Required: true},
				},
			},
			Handler: handleWarn,
		},
		{
			Command: &discordgo.ApplicationCommand{
				Name:        "automod",
				Description: "Configure automatic moderation for banned words and phrases.",
				Options: []*discordgo.ApplicationCommandOption{
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "banned-word-add",
						Description: "Add a word or phrase to the banned list.",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionString, Name: "word", Description: "Word or phrase to block", Required: true, MaxLength: 100},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "banned-word-remove",
						Description: "Remove a word or phrase from the banned list.",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionString, Name: "word", Description: "Word or phrase to unblock", Required: true, MaxLength: 100},
						},
					},
					{Type: discordgo.ApplicationCommandOptionSubCommand, Name: "banned-word-list", Description: "List the banned words and phrases."},
					{Type: discordgo.ApplicationCommandOptionSubCommand, Name: "enable", Description: "Enable automatic moderation."},
					{Type: discordgo.ApplicationCommandOptionSubCommand, Name: "disable", Description: "Disable automatic moderation."},
					{Type: discordgo.ApplicationCommandOptionSubCommand, Name: "status", Description: "Show AutoMod's status and banned-word count."},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "view",
						Description: "View a user's recent AutoMod trigger events.",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionUser, Name: "user", Description: "User whose AutoMod events to view", Required: true},
						},
					},
				},
			},
			Handler: handleAutoModCommand,
		},

		// --- ANALYTICS & MEDIA COMMANDS ---
		{
			Command: &discordgo.ApplicationCommand{
				Name:        "analytics",
				Description: "Generate a chat activity heatmap and statistics.",
			},
			Handler: handleAnalytics,
		},

		
		// --- CHANNEL MANAGEMENT COMMAND ---
		{
			Command: &discordgo.ApplicationCommand{
				Name:        "channel",
				Description: "Manage server channels.",
				Options: []*discordgo.ApplicationCommandOption{
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "create",
						Description: "Create a new channel.",
						Options: []*discordgo.ApplicationCommandOption{
							{
								Type:        discordgo.ApplicationCommandOptionString,
								Name:        "type",
								Description: "The type of channel",
								Required:    true,
								Choices: []*discordgo.ApplicationCommandOptionChoice{
									{Name: "Text", Value: "text"},
									{Name: "Voice", Value: "voice"},
									{Name: "Category", Value: "category"},
								},
							},
							{
								Type:        discordgo.ApplicationCommandOptionString,
								Name:        "name",
								Description: "The channel name",
								Required:    true,
							},
							{
								Type:         discordgo.ApplicationCommandOptionChannel,
								Name:         "in",
								Description:  "The parent category (ignored for category types)",
								Required:     false,
								ChannelTypes: []discordgo.ChannelType{discordgo.ChannelTypeGuildCategory},
							},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "delete",
						Description: "Delete a channel.",
						Options: []*discordgo.ApplicationCommandOption{
							{
								Type:        discordgo.ApplicationCommandOptionChannel,
								Name:        "target",
								Description: "The channel to delete",
								Required:    true,
							},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "rename",
						Description: "Rename a channel.",
						Options: []*discordgo.ApplicationCommandOption{
							{
								Type:        discordgo.ApplicationCommandOptionChannel,
								Name:        "target",
								Description: "The channel to rename",
								Required:    true,
							},
							{
								Type:        discordgo.ApplicationCommandOptionString,
								Name:        "name",
								Description: "The new channel name",
								Required:    true,
							},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "lock",
						Description: "Prevent @everyone from sending messages in a channel.",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionChannel, Name: "channel", Description: "The channel to lock", Required: true},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "unlock",
						Description: "Allow @everyone to send messages in a channel.",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionChannel, Name: "channel", Description: "The channel to unlock", Required: true},
						},
					},
				},
			},
			Handler: handleChannel,
		},

		// --- MODERATION COMMANDS ---
		{
			Command: &discordgo.ApplicationCommand{
				Name:        "timeout",
				Description: "Timeout a user.",
				Options: []*discordgo.ApplicationCommandOption{
					{Type: discordgo.ApplicationCommandOptionUser, Name: "user", Description: "User to timeout", Required: true},
					{Type: discordgo.ApplicationCommandOptionInteger, Name: "minutes", Description: "Duration in minutes", Required: true},
					{Type: discordgo.ApplicationCommandOptionString, Name: "reason", Description: "Reason for timeout", Required: false},
				},
			},
			Handler: handleTimeout,
		},
		{
			Command: &discordgo.ApplicationCommand{
				Name:        "kick",
				Description: "Kick a user from the server.",
				Options: []*discordgo.ApplicationCommandOption{
					{Type: discordgo.ApplicationCommandOptionUser, Name: "user", Description: "User to kick", Required: true},
					{Type: discordgo.ApplicationCommandOptionString, Name: "reason", Description: "Reason for kick", Required: false},
				},
			},
			Handler: handleKick,
		},
		{
			Command: &discordgo.ApplicationCommand{
				Name:        "ban",
				Description: "Add, remove, and view server bans.",
				Options: []*discordgo.ApplicationCommandOption{
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "add",
						Description: "Ban a user from the server.",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionUser, Name: "user", Description: "User to ban", Required: true},
							{Type: discordgo.ApplicationCommandOptionInteger, Name: "days", Description: "Days of message history to delete (0-7)", Required: false},
							{Type: discordgo.ApplicationCommandOptionString, Name: "reason", Description: "Reason for ban", Required: false},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "remove",
						Description: "Remove a user's ban.",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionString, Name: "user", Description: "Banned user's Discord ID (shown by /ban view)", Required: true},
						},
					},
					{Type: discordgo.ApplicationCommandOptionSubCommand, Name: "view", Description: "List banned users."},
				},
			},
			Handler: handleBan,
		},
		{
			Command: &discordgo.ApplicationCommand{
				Name:        "slowmode",
				Description: "Set channel slowmode.",
				Options: []*discordgo.ApplicationCommandOption{
					{Type: discordgo.ApplicationCommandOptionInteger, Name: "seconds", Description: "Slowmode delay in seconds (0 to disable)", Required: true},
				},
			},
			Handler: handleSlowmode,
		},

		// --- BACKUP & EXPORT COMMANDS ---
		{Command: &discordgo.ApplicationCommand{Name: "backup", Description: "Export full server configuration to a JSON file."}, Handler: handleBackup},
		{
			Command: &discordgo.ApplicationCommand{
				Name:        "transcript",
				Description: "Export chat history to a file.",
				Options: []*discordgo.ApplicationCommandOption{
					{Type: discordgo.ApplicationCommandOptionInteger, Name: "limit", Description: "Number of messages to export (max 5000)", Required: true},
				},
			},
			Handler: handleTranscript,
		},

		// --- DUAL CLOCK & WEATHER COMMAND ---

		// --- STATS COMMAND ---
		{Command: &discordgo.ApplicationCommand{Name: "serverstats", Description: "Display detailed server, channel, and media storage statistics."}, Handler: handleServerStats},

		// --- INTERNET ARCHIVE ---
		{
			Command: &discordgo.ApplicationCommand{
				Name:        "lyrics",
				Description: "Search Genius for song or album metadata and links.",
				Options: []*discordgo.ApplicationCommandOption{
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "fetch-song",
						Description: "Search Genius for a song; returns metadata and a link, not lyrics.",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionString, Name: "song", Description: "Song title, optionally with artist", Required: true, MaxLength: 200},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "fetch-album",
						Description: "Search Genius for albums by an artist, optionally narrowed to an album.",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionString, Name: "artist", Description: "Artist name", Required: true, MaxLength: 100},
							{Type: discordgo.ApplicationCommandOptionString, Name: "album", Description: "Optional album title", Required: false, MaxLength: 100},
						},
					},
				},
			},
			Handler: handleLyrics,
		},
		{
			Command: &discordgo.ApplicationCommand{
				Name:        "ia",
				Description: "Search and explore the Internet Archive (search, item, download, wayback, stats, fts).",
				Options: []*discordgo.ApplicationCommandOption{
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "search",
						Description: "Metadata search across the archive.",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionString, Name: "query", Description: "Search query (e.g. space, creator:Heinlein).", Required: true},
							{Type: discordgo.ApplicationCommandOptionString, Name: "type", Description: "Optional mediatype filter: audio, texts, movies, image, software, etree, web.", Required: false},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "item",
						Description: "Full metadata for an item by identifier.",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionString, Name: "identifier", Description: "Item identifier (e.g. xfetch).", Required: true},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "download",
						Description: "Post a file from an item (auto-picks a small one if no file given).",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionString, Name: "identifier", Description: "Item identifier.", Required: true},
							{Type: discordgo.ApplicationCommandOptionString, Name: "file", Description: "Optional filename within the item (use /ia item to list files).", Required: false},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "wayback",
						Description: "Check if the Wayback Machine archived a URL.",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionString, Name: "url", Description: "The URL to check (e.g. https://example.com).", Required: true},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "stats",
						Description: "View/download counts for an item.",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionString, Name: "identifier", Description: "Item identifier.", Required: true},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "fts",
						Description: "Full-text search across the archive.",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionString, Name: "query", Description: "Full-text search query.", Required: true},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "help",
						Description: "Show the /ia usage guide.",
					},
				},
			},
			Handler: handleIA,
		},

		// --- GAMES & SIMULATIONS ---
		{
			Command: &discordgo.ApplicationCommand{
				Name:        "dice",
				Description: "Simulate dice odds: your win chance vs roll 0-100, 10,000,000 rolls.",
				Options: []*discordgo.ApplicationCommandOption{
					{Type: discordgo.ApplicationCommandOptionString, Name: "chance", Description: "Your win chance (2.00 to 98.00). Win when roll < chance.", Required: true},
				},
			},
			Handler: handleDice,
		},
		{
			Command: &discordgo.ApplicationCommand{
				Name:        "notifiers",
				Description: "Manage media & stream notification feeds.",
				Options: []*discordgo.ApplicationCommandOption{
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "status",
						Description: "Show status of YouTube, Kick, Spotify, Movies, FreeStuff, & Scrape notifiers.",
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "trigger",
						Description: "Force an immediate poll pass for a notifier.",
						Options: []*discordgo.ApplicationCommandOption{
							{
								Type:        discordgo.ApplicationCommandOptionString,
								Name:        "service",
								Description: "The service to trigger",
								Required:    true,
								Choices: []*discordgo.ApplicationCommandOptionChoice{
									{Name: "YouTube", Value: "youtube"},
									{Name: "Kick", Value: "kick"},
									{Name: "Spotify", Value: "spotify"},
									{Name: "Movies (TMDB)", Value: "movies"},
									{Name: "FreeStuff (GG.deals)", Value: "freestuff"},
									{Name: "Hermes Tips", Value: "hermes"},
									{Name: "Image Scrape", Value: "scrape"},
								},
							},
						},
					},
				},
			},
			Handler: handleNotifiersCommand,
		},
		{
			Command: &discordgo.ApplicationCommand{
				Name:        "note",
				Description: "Manage private staff notes for users.",
				Options: []*discordgo.ApplicationCommandOption{
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "add",
						Description: "Add a note for a user",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionUser, Name: "user", Description: "Target user", Required: true},
							{Type: discordgo.ApplicationCommandOptionString, Name: "text", Description: "Note content", Required: true},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "view",
						Description: "View recent notes for a user",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionUser, Name: "user", Description: "Target user", Required: true},
							{Type: discordgo.ApplicationCommandOptionInteger, Name: "limit", Description: "How many notes to show (default 10)", Required: false},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "delete",
						Description: "Delete a note by ID",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionUser, Name: "user", Description: "Target user", Required: true},
							{Type: discordgo.ApplicationCommandOptionInteger, Name: "noteid", Description: "Note ID to delete", Required: true},
						},
					},
				},
			},
			Handler: handleNoteCommand,
		},
		{
			Command: &discordgo.ApplicationCommand{
				Name:        "role",
				Description: "Add or remove roles from server members.",
				Options: []*discordgo.ApplicationCommandOption{
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "add",
						Description: "Add a role to a member",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionUser, Name: "user", Description: "Target user", Required: true},
							{Type: discordgo.ApplicationCommandOptionRole, Name: "role", Description: "Role to add", Required: true},
							{Type: discordgo.ApplicationCommandOptionString, Name: "reason", Description: "Reason", Required: false},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "remove",
						Description: "Remove a role from a member",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionUser, Name: "user", Description: "Target user", Required: true},
							{Type: discordgo.ApplicationCommandOptionRole, Name: "role", Description: "Role to remove", Required: true},
							{Type: discordgo.ApplicationCommandOptionString, Name: "reason", Description: "Reason", Required: false},
						},
					},
				},
			},
			Handler: handleRoleCommand,
		},
		{
			Command: &discordgo.ApplicationCommand{
				Name:        "rolemenu",
				Description: "Create a dropdown select menu for self-assigned roles.",
				Options: []*discordgo.ApplicationCommandOption{
					{Type: discordgo.ApplicationCommandOptionString, Name: "title", Description: "Header text for the role menu", Required: true},
					{Type: discordgo.ApplicationCommandOptionString, Name: "pairs", Description: "Semicolon separated list (Label:RoleName; Label:@Role)", Required: true},
				},
			},
			Handler: handleRoleMenuCommand,
		},
		{
			Command: &discordgo.ApplicationCommand{
				Name:        "autorole",
				Description: "Configure automatic role assignment for new server members.",
				Options: []*discordgo.ApplicationCommandOption{
					{Type: discordgo.ApplicationCommandOptionSubCommand, Name: "view", Description: "View current auto-role"},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "set",
						Description: "Set auto-role for new members",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionRole, Name: "role", Description: "Role to assign", Required: true},
						},
					},
					{Type: discordgo.ApplicationCommandOptionSubCommand, Name: "clear", Description: "Disable auto-role"},
				},
			},
			Handler: handleAutoRoleCommand,
		},
		{
			Command: &discordgo.ApplicationCommand{
				Name:        "ticket-panel",
				Description: "Post a button panel allowing members to create private support tickets.",
			},
			Handler: handleTicketPanelCommand,
		},
		{
			Command: &discordgo.ApplicationCommand{
				Name:        "close-ticket",
				Description: "Close and archive the current support ticket thread.",
			},
			Handler: handleCloseTicketCommand,
		},
		{
			Command: &discordgo.ApplicationCommand{
				Name:        "pick",
				Description: "Pick random results from server data.",
				Options: []*discordgo.ApplicationCommandOption{
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "random-member",
						Description: "Pick one or more random members from this server.",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionInteger, Name: "count", Description: "How many members to pick (default: 1)", Required: false},
							{Type: discordgo.ApplicationCommandOptionBoolean, Name: "include-bots", Description: "Include bot accounts", Required: false},
							{Type: discordgo.ApplicationCommandOptionRole, Name: "role", Description: "Only pick members with this role", Required: false},
							{Type: discordgo.ApplicationCommandOptionString, Name: "exclude", Description: "Comma-separated user IDs or mentions to exclude", Required: false},
							{Type: discordgo.ApplicationCommandOptionInteger, Name: "countdown-minutes", Description: "Optional countdown timestamp in minutes", Required: false},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "random-role",
						Description: "Pick one or more random roles from this server.",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionInteger, Name: "count", Description: "How many roles to pick (default: 1)", Required: false},
							{Type: discordgo.ApplicationCommandOptionInteger, Name: "countdown-minutes", Description: "Optional countdown timestamp in minutes", Required: false},
						},
					},
				},
			},
			Handler: handlePickCommand,
		},
		{
			Command: &discordgo.ApplicationCommand{
				Name:        "riddle",
				Description: "Start or manage a multi-round riddle game in this channel.",
				Options: []*discordgo.ApplicationCommandOption{
					{Type: discordgo.ApplicationCommandOptionSubCommand, Name: "start", Description: "Start a new 10-round riddle game in this channel"},
					{Type: discordgo.ApplicationCommandOptionSubCommand, Name: "stop", Description: "Stop the active riddle game in this channel"},
					{Type: discordgo.ApplicationCommandOptionSubCommand, Name: "status", Description: "Show status of current riddle round"},
				},
			},
			Handler: handleRiddleCommand,
		},
		{
			Command: &discordgo.ApplicationCommand{
				Name:        "battle",
				Description: "Manage Sweet Dreams multiplayer battles.",
				Options: []*discordgo.ApplicationCommandOption{
					{Type: discordgo.ApplicationCommandOptionSubCommand, Name: "start", Description: "Start a Sweet Dreams battle!"},
					{Type: discordgo.ApplicationCommandOptionSubCommand, Name: "join", Description: "Join an open Sweet Dreams battle!"},
				},
			},
			Handler: handleBattleCommand,
		},
		{
			Command: &discordgo.ApplicationCommand{
				Name:        "fallen-star",
				Description: "Play the Fallen Star dark fantasy CYOA text adventure game.",
				Options: []*discordgo.ApplicationCommandOption{
					{Type: discordgo.ApplicationCommandOptionSubCommand, Name: "start", Description: "Start a new Fallen Star adventure"},
					{Type: discordgo.ApplicationCommandOptionSubCommand, Name: "status", Description: "Resume or view current adventure status"},
				},
			},
			Handler: handleFallenStarCommand,
		},

		// --- COMMUNITY & UTILITY (Ported from ElyAdmin) ---
		{
			Command: &discordgo.ApplicationCommand{
				Name:        "birthday",
				Description: "Register your birthday and let staff view server birthdays.",
				Options: []*discordgo.ApplicationCommandOption{
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "set",
						Description: "Set your birthday one time. This cannot be edited later.",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionInteger, Name: "month", Description: "Birth month (1-12)", Required: true},
							{Type: discordgo.ApplicationCommandOptionInteger, Name: "day", Description: "Birth day (1-31)", Required: true},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "view",
						Description: "View birthdays registered in this server.",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionInteger, Name: "month", Description: "Optional month filter (1-12)", Required: false},
						},
					},
				},
			},
			Handler: handleBirthday,
		},
		{
			Command: &discordgo.ApplicationCommand{
				Name:        "translate",
				Description: "Translate text to a target language.",
				Options: []*discordgo.ApplicationCommandOption{
					{Type: discordgo.ApplicationCommandOptionString, Name: "text", Description: "Text to translate", Required: true},
					{Type: discordgo.ApplicationCommandOptionString, Name: "to", Description: "Target language code, e.g. en, es, fr, de, tl, ja", Required: true},
				},
			},
			Handler: handleTranslate,
		},
		{
			Command: &discordgo.ApplicationCommand{
				Name:        "message-log",
				Description: "Manage which channels are logged to Postgres, and search logged messages.",
				Options: []*discordgo.ApplicationCommandOption{
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "add",
						Description: "Start logging a text channel",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionChannel, Name: "channel", Description: "Text channel to add to message logging", Required: true},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "remove",
						Description: "Stop logging a text channel",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionChannel, Name: "channel", Description: "Text channel to remove from message logging", Required: true},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "list",
						Description: "Show channels currently being logged",
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "search",
						Description: "Search logged messages by text",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionString, Name: "query", Description: "Text to search for", Required: true},
							{Type: discordgo.ApplicationCommandOptionChannel, Name: "channel", Description: "Restrict search to a channel", Required: false},
							{Type: discordgo.ApplicationCommandOptionUser, Name: "user", Description: "Restrict search to a user", Required: false},
							{Type: discordgo.ApplicationCommandOptionInteger, Name: "limit", Description: "Max results (1-25)", Required: false},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "stats",
						Description: "Show message logging stats (total messages, servers, channels)",
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "fetch",
						Description: "Return the full logged message as a .md file attachment",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionString, Name: "messageid", Description: "Message ID to fetch (from a search result)", Required: true},
						},
					},
				},
			},
			Handler: handleMessageLog,
		},
	}

	commandSpecs = append(commandSpecs, openRouterCommandSpec())

	for _, spec := range a2e.CommandSpecs() {
		commandSpecs = append(commandSpecs, commandSpec{Command: spec.Command, Handler: spec.Handler})
	}
	for _, spec := range venice.CommandSpecs() {
		commandSpecs = append(commandSpecs, commandSpec{Command: spec.Command, Handler: spec.Handler})
	}
	for _, spec := range providers.CommandSpecs() {
		// Skip the providers package's openrouter command — openrouter.go's
		// /openrouter (models, deals, model, compare, endpoints, etc.)
		// replaces it with more features.
		if spec.Command.Name == "openrouter" {
			continue
		}
		commandSpecs = append(commandSpecs, commandSpec{Command: spec.Command, Handler: spec.Handler})
	}
}

// registerCommands syncs the slash commands directly to Sunfire's server.
// It also clears any global slash commands previously registered so commands do not duplicate.
func registerCommands(s *discordgo.Session) error {
	// 1. Wipe any previously registered global commands (prevents duplicates in the server)
	if _, err := s.ApplicationCommandBulkOverwrite(s.State.User.ID, "", []*discordgo.ApplicationCommand{}); err != nil {
		logErrorf("warning: failed to clear global commands: %v", err)
	}

	var cmds []*discordgo.ApplicationCommand
	// Extract just the Discord command structures from our specs
	for _, spec := range commandSpecs {
		cmds = append(cmds, spec.Command)
	}

	// 2. Register/overwrite commands specifically for SunfireServerID
	_, err := s.ApplicationCommandBulkOverwrite(s.State.User.ID, SunfireServerID, cmds)
	if err != nil {
		logErrorf("failed to bulk overwrite guild commands: %v", err)
		return err
	}

	return nil
}

func handleHelp(s *discordgo.Session, i *discordgo.InteractionCreate) {
	category, search := "", ""
	for _, option := range i.ApplicationCommandData().Options {
		switch option.Name {
		case "category":
			category = strings.ToLower(option.StringValue())
		case "search":
			search = strings.ToLower(strings.TrimSpace(option.StringValue()))
		}
	}
	if !validHelpSearch(search) {
		respondHelpError(s, i, "Search must contain only letters, numbers, underscores, or hyphens.")
		return
	}
	if category != "" && !validHelpCategory(category) {
		respondHelpError(s, i, "That help category is not available. Use `/help` to browse categories.")
		return
	}
	respondHelpPage(s, i, category, 0, search, false)
}

type helpCategory struct {
	Value              string
	Label              string
	Commands           []string
	IncludedCategories []string
}

var helpCategories = []helpCategory{
	{Value: "a2e", Label: "A2E AI", Commands: []string{"a2e"}},
	{Value: "archive", Label: "Internet Archive", Commands: []string{"ia"}},
	{Value: "cloudflare", Label: "Cloudflare", Commands: []string{"cloudflare"}},
	{Value: "cohere", Label: "Cohere", Commands: []string{"cohere"}},
	{Value: "core", Label: "Core (System, Moderation, Media)"},
	{Value: "groq", Label: "Groq", Commands: []string{"groq"}},
	{Value: "moderation", Label: "Moderation & Staff", Commands: []string{
		"automod", "warn", "timeout", "kick", "ban", "unban", "mute", "unmute", "purge", "note",
		"role", "rolemenu", "autorole", "ticket-panel", "close-ticket", "message-log", "backup", "channel",
		"dedup", "grant-access", "searchlogs", "setup",
		"slowmode",
	}},
	{Value: "mistral", Label: "Mistral", Commands: []string{"mistral"}},
	{Value: "notifiers", Label: "Media & Stream Notifiers", Commands: []string{"notifiers"}},
	{Value: "nvidia", Label: "NVIDIA", Commands: []string{"nvidia"}},
	{Value: "openrouter", Label: "OpenRouter", Commands: []string{"openrouter"}},
	{Value: "providers", Label: "LLM Providers", IncludedCategories: []string{
		"cloudflare", "cohere", "groq", "mistral", "nvidia", "openrouter",
	}},
	{Value: "venice", Label: "Venice AI", Commands: []string{"venice"}},
}

func helpCategoryChoices() []*discordgo.ApplicationCommandOptionChoice {
	choices := make([]*discordgo.ApplicationCommandOptionChoice, 0, len(helpCategories))
	for _, category := range helpCategories {
		choices = append(choices, &discordgo.ApplicationCommandOptionChoice{Name: category.Label, Value: category.Value})
	}
	return choices
}

func helpCategoryLabel(value string) string {
	for _, category := range helpCategories {
		if category.Value == value {
			return category.Label
		}
	}
	return "Commands"
}

func validHelpCategory(value string) bool {
	for _, category := range helpCategories {
		if category.Value == value {
			return true
		}
	}
	return false
}

func validHelpSearch(search string) bool {
	if len(search) > 32 {
		return false
	}
	for _, r := range search {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' && r != '-' {
			return false
		}
	}
	return true
}

func helpCommandCategory(name string) string {
	for _, category := range helpCategories {
		for _, command := range category.Commands {
			if command == name {
				return category.Value
			}
		}
	}
	return "core"
}

func helpCategoryMatches(category, commandCategory string) bool {
	for _, definition := range helpCategories {
		if definition.Value != category {
			continue
		}
		if category == commandCategory {
			return true
		}
		for _, included := range definition.IncludedCategories {
			if included == commandCategory {
				return true
			}
		}
	}
	return false
}

func helpOptionTypeName(optionType discordgo.ApplicationCommandOptionType) string {
	switch optionType {
	case discordgo.ApplicationCommandOptionSubCommand:
		return "subcommand"
	case discordgo.ApplicationCommandOptionSubCommandGroup:
		return "subcommand group"
	case discordgo.ApplicationCommandOptionString:
		return "string"
	case discordgo.ApplicationCommandOptionInteger:
		return "integer"
	case discordgo.ApplicationCommandOptionBoolean:
		return "boolean"
	case discordgo.ApplicationCommandOptionUser:
		return "user"
	case discordgo.ApplicationCommandOptionChannel:
		return "channel"
	case discordgo.ApplicationCommandOptionRole:
		return "role"
	case discordgo.ApplicationCommandOptionMentionable:
		return "mentionable"
	case discordgo.ApplicationCommandOptionNumber:
		return "number"
	case discordgo.ApplicationCommandOptionAttachment:
		return "attachment"
	default:
		return "option"
	}
}

func helpCommandEntry(spec commandSpec) string {
	cmd := spec.Command
	description := cmd.Description
	if cmd.Name == "grant-access" {
		description += " *(Owner only)*"
	} else if !isPublicCommand(cmd.Name) {
		description += " *(Owner only unless access is granted)*"
	}
	var lines []string
	lines = append(lines, fmt.Sprintf("• **/%s** — %s", cmd.Name, description))
	lines = append(lines, helpOptionLines("/"+cmd.Name, cmd.Options, "  ")...)
	return strings.Join(lines, "\n")
}

func helpOptionLines(commandPath string, options []*discordgo.ApplicationCommandOption, indent string) []string {
	var lines []string
	for _, option := range options {
		path := commandPath + " " + option.Name
		switch option.Type {
		case discordgo.ApplicationCommandOptionSubCommand, discordgo.ApplicationCommandOptionSubCommandGroup:
			lines = append(lines, fmt.Sprintf("%s↳ **`%s`** — %s", indent, path, option.Description))
			lines = append(lines, helpOptionLines(path, option.Options, indent+"  ")...)
		default:
			required := "optional"
			if option.Required {
				required = "required"
			}
			line := fmt.Sprintf("%s• `%s` (%s, %s): %s", indent, path, helpOptionTypeName(option.Type), required, option.Description)
			var constraints []string
			if option.MinValue != nil || option.MaxValue != 0 {
				min, max := "any", "any"
				if option.MinValue != nil {
					min = fmt.Sprintf("%g", *option.MinValue)
				}
				if option.MaxValue != 0 {
					max = fmt.Sprintf("%g", option.MaxValue)
				}
				constraints = append(constraints, fmt.Sprintf("Range: %s to %s", min, max))
			}
			if option.MinLength != nil || option.MaxLength > 0 {
				min, max := "any", "any"
				if option.MinLength != nil {
					min = fmt.Sprint(*option.MinLength)
				}
				if option.MaxLength > 0 {
					max = fmt.Sprint(option.MaxLength)
				}
				constraints = append(constraints, fmt.Sprintf("Length: %s to %s characters", min, max))
			}
			if option.Autocomplete {
				constraints = append(constraints, "autocomplete")
			}
			if len(option.Choices) > 0 {
				choices := make([]string, 0, len(option.Choices))
				for _, choice := range option.Choices {
					label := choice.Name
					if fmt.Sprint(choice.Value) != label {
						label = fmt.Sprintf("%s (%v)", label, choice.Value)
					}
					choices = append(choices, label)
				}
				line += " Choices: " + strings.Join(choices, ", ")
			}
			if len(constraints) > 0 {
				line += " (" + strings.Join(constraints, "; ") + ")"
			}
			lines = append(lines, line)
		}
	}
	return lines
}

func helpEntries(category, search string) []string {
	var entries []string
	for _, spec := range commandSpecs {
		name := strings.ToLower(spec.Command.Name)
		commandCategory := helpCommandCategory(name)
		if category != "" && !helpCategoryMatches(category, commandCategory) {
			continue
		}
		if search != "" && !strings.Contains(name, search) {
			continue
		}
		entries = append(entries, helpCommandEntry(spec))
	}
	sort.Strings(entries)
	return entries
}

const helpPageLimit = 1800

func splitHelpLine(line string, limit int) []string {
	runes := []rune(line)
	if len(runes) <= limit {
		return []string{line}
	}
	parts := make([]string, 0, (len(runes)+limit-1)/limit)
	for len(runes) > 0 {
		n := limit
		if len(runes) < n {
			n = len(runes)
		}
		parts = append(parts, string(runes[:n]))
		runes = runes[n:]
	}
	return parts
}

func helpContentPages(category, search string) []string {
	if category == "" && search == "" {
		var lines []string
		counts := make(map[string]int)
		for _, spec := range commandSpecs {
			counts[helpCommandCategory(spec.Command.Name)]++
		}
		for _, categoryInfo := range helpCategories {
			count := counts[categoryInfo.Value]
			for _, provider := range categoryInfo.IncludedCategories {
				count += counts[provider]
			}
			if count > 0 {
				lines = append(lines, fmt.Sprintf("• **%s** — %d commands", categoryInfo.Label, count))
			}
		}
		return []string{strings.Join(lines, "\n") + "\n\nChoose a category below, or use `/help search:<name>` to find a command."}
	}

	entries := helpEntries(category, search)
	if len(entries) == 0 {
		return []string{"No commands found. Try a different category or search term."}
	}
	title := helpCategoryLabel(category)
	if search != "" {
		title = "Search: " + search
	}
	header := fmt.Sprintf("**Sunfire Commands — %s**\n", title)
	bodyLimit := helpPageLimit - len([]rune(header)) - 48
	var pages []string
	current := ""
	flush := func() {
		if current != "" {
			pages = append(pages, header+current)
			current = ""
		}
	}
	for _, entry := range entries {
		for _, line := range strings.Split(entry, "\n") {
			for _, part := range splitHelpLine(line, bodyLimit) {
				part += "\n"
				if len([]rune(current))+len([]rune(part)) > bodyLimit {
					flush()
				}
				current += part
			}
		}
		current += "\n"
	}
	flush()
	if len(pages) == 0 {
		return []string{header}
	}
	return pages
}

func helpComponents(category string, page, pageCount int, search string) []discordgo.MessageComponent {
	options := make([]discordgo.SelectMenuOption, 0, len(helpCategories))
	for _, item := range helpCategories {
		options = append(options, discordgo.SelectMenuOption{Label: item.Label, Value: item.Value, Default: item.Value == category})
	}
	rows := []discordgo.MessageComponent{
		discordgo.ActionsRow{Components: []discordgo.MessageComponent{
			discordgo.SelectMenu{CustomID: "help:category", Placeholder: "Browse command categories", Options: options},
		}},
	}
	if pageCount > 1 {
		rows = append(rows, discordgo.ActionsRow{Components: []discordgo.MessageComponent{
			discordgo.Button{Label: "Previous", Style: discordgo.PrimaryButton, CustomID: helpPageCustomID(category, page-1, search), Disabled: page == 0},
			discordgo.Button{Label: fmt.Sprintf("%d / %d", page+1, pageCount), Style: discordgo.SecondaryButton, CustomID: "help:page:noop", Disabled: true},
			discordgo.Button{Label: "Next", Style: discordgo.PrimaryButton, CustomID: helpPageCustomID(category, page+1, search), Disabled: page+1 >= pageCount},
		}})
	}
	return rows
}

func helpPageCustomID(category string, page int, search string) string {
	if category == "" {
		category = "all"
	}
	return fmt.Sprintf("help:page:%s:%d:%s", category, page, search)
}

func respondHelpError(s *discordgo.Session, i *discordgo.InteractionCreate, message string) {
	if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Content: message, Flags: discordgo.MessageFlagsEphemeral},
	}); err != nil {
		logErrorf("failed to respond to /help: %v", err)
	}
}

func respondHelpPage(s *discordgo.Session, i *discordgo.InteractionCreate, category string, page int, search string, update bool) {
	pages := helpContentPages(category, search)
	if page < 0 {
		page = 0
	}
	if page >= len(pages) {
		page = len(pages) - 1
	}
	content := pages[page]
	if len(pages) > 1 {
		content += fmt.Sprintf("\n*Page %d of %d*", page+1, len(pages))
	}
	data := &discordgo.InteractionResponseData{Content: content, Components: helpComponents(category, page, len(pages), search)}
	responseType := discordgo.InteractionResponseChannelMessageWithSource
	if update {
		responseType = discordgo.InteractionResponseUpdateMessage
	}
	if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{Type: responseType, Data: data}); err != nil {
		logErrorf("failed to render /help category=%q page=%d: %v", category, page, err)
	}
}

func handleHelpComponent(s *discordgo.Session, i *discordgo.InteractionCreate) {
	data := i.MessageComponentData()
	if data.CustomID == "help:category" {
		if len(data.Values) != 1 || !validHelpCategory(data.Values[0]) {
			respondHelpError(s, i, "That help category is not available. Use `/help` to browse categories.")
			return
		}
		respondHelpPage(s, i, data.Values[0], 0, "", true)
		return
	}
	parts := strings.Split(data.CustomID, ":")
	if len(parts) != 5 || parts[0] != "help" || parts[1] != "page" {
		respondHelpError(s, i, "This help control is invalid. Run `/help` again.")
		return
	}
	category := parts[2]
	if category == "all" {
		category = ""
	} else if !validHelpCategory(category) {
		respondHelpError(s, i, "This help category is no longer available. Run `/help` again.")
		return
	}
	page, err := strconv.Atoi(parts[3])
	search := parts[4]
	if err != nil || !validHelpSearch(search) {
		respondHelpError(s, i, "This help control is invalid. Run `/help` again.")
		return
	}
	respondHelpPage(s, i, category, page, search, true)
}

// handleUserInfo displays the profile and guild information Discord makes
// available to this bot for the selected user.
func handleUserInfo(s *discordgo.Session, i *discordgo.InteractionCreate) {
	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	})

	var targetUser *discordgo.User
	if options := i.ApplicationCommandData().Options; len(options) > 0 {
		targetUser = options[0].UserValue(s)
	} else if i.Member != nil {
		targetUser = i.Member.User
	} else {
		targetUser = i.User
	}
	if targetUser == nil || targetUser.ID == "" {
		_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{Content: "❌ Discord did not provide a user for this request."})
		return
	}

	// The user endpoint may add profile fields omitted from the interaction's
	// resolved user object; retain the resolved object if Discord rejects it.
	if fullUser, err := s.User(targetUser.ID); err == nil && fullUser != nil {
		targetUser = fullUser
	} else if err != nil {
		logErrorf("could not fetch full Discord profile for user %s: %v", targetUser.ID, err)
	}

	var member *discordgo.Member
	var guild *discordgo.Guild
	if i.GuildID != "" {
		var err error
		member, err = s.GuildMember(i.GuildID, targetUser.ID)
		if err != nil && i.Member != nil && i.Member.User != nil && i.Member.User.ID == targetUser.ID {
			member = i.Member
		}
		if err != nil && member == nil {
			logErrorf("could not fetch guild membership for user %s in guild %s: %v", targetUser.ID, i.GuildID, err)
		}
		guild, _ = s.State.Guild(i.GuildID)
		if guild == nil {
			var err error
			guild, err = s.Guild(i.GuildID)
			if err != nil {
				logErrorf("could not fetch guild %s for /userinfo: %v", i.GuildID, err)
			}
		}
	}

	embedColor := targetUser.AccentColor
	if embedColor == 0 {
		embedColor = 0xFF6B35
	}
	embed := &discordgo.MessageEmbed{
		Color: embedColor,
		Author: &discordgo.MessageEmbedAuthor{
			Name:    fmt.Sprintf("%s's Profile", targetUser.DisplayName()),
			IconURL: targetUser.AvatarURL("1024"),
		},
		Thumbnail: &discordgo.MessageEmbedThumbnail{URL: targetUser.AvatarURL("1024")},
		Fields:    []*discordgo.MessageEmbedField{},
	}
	if member != nil && member.Avatar != "" {
		embed.Thumbnail.URL = member.AvatarURL("1024")
	}
	if targetUser.Banner != "" {
		embed.Image = &discordgo.MessageEmbedImage{URL: targetUser.BannerURL("1024")}
	}

	accountType := "User 👤"
	if targetUser.Bot {
		accountType = "Bot 🤖"
	}
	if targetUser.System {
		accountType = "Discord System ⚙️"
	}
	created, err := discordgo.SnowflakeTimestamp(targetUser.ID)
	desc := fmt.Sprintf(
		"**Display name:** %s\n**Username:** `%s`\n**User ID:** `%s`\n**Account type:** %s\n",
		targetUser.DisplayName(), targetUser.Username, targetUser.ID, accountType,
	)
	if err == nil {
		desc += fmt.Sprintf("**Created:** <t:%d:f> (<t:%d:R>)\n", created.Unix(), created.Unix())
	}
	if targetUser.Discriminator != "" && targetUser.Discriminator != "0" {
		desc += fmt.Sprintf("**Legacy discriminator:** `%s`\n", targetUser.Discriminator)
	}
	if targetUser.Locale != "" {
		desc += fmt.Sprintf("**Locale:** `%s`\n", targetUser.Locale)
	}
	if targetUser.AccentColor != 0 {
		desc += fmt.Sprintf("**Profile accent:** `#%06X`\n", targetUser.AccentColor)
	}
	if targetUser.PremiumType != discordgo.UserPremiumTypeNone {
		desc += fmt.Sprintf("**Discord premium:** %s\n", userPremiumTypeName(targetUser.PremiumType))
	}
	desc += fmt.Sprintf("**Avatar:** [open profile image](%s)", targetUser.AvatarURL("1024"))
	if member != nil && member.Avatar != "" {
		desc += fmt.Sprintf("\n**Server avatar:** [open server image](%s)", member.AvatarURL("1024"))
	}
	if targetUser.Banner != "" {
		desc += fmt.Sprintf("\n**Banner:** [open banner image](%s)", targetUser.BannerURL("1024"))
	}
	embed.Description = truncateUserInfo(desc, 4000)

	if i.GuildID != "" {
		presence, presenceErr := s.State.Presence(i.GuildID, targetUser.ID)
		activityValue := formatUserPresence(presence, presenceErr)
		voiceValue := formatUserVoiceState(guild, targetUser.ID)
		embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
			Name:   "📡 Presence & Voice",
			Value:  truncateUserInfo("**Presence:** "+activityValue+"\n**Voice:** "+voiceValue, 1024),
			Inline: false,
		})
	}

	if member != nil {
		embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
			Name:   "📥 Server Profile",
			Value:  truncateUserInfo(formatGuildMemberInfo(s, i, targetUser, member, guild), 1024),
			Inline: false,
		})
	}

	badges := getUserBadges(int(targetUser.PublicFlags))
	badgeValue := "No public badges or flags."
	if len(badges) > 0 {
		badgeValue = strings.Join(badges, "\n")
	}
	embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
		Name:   "🎖️ Public Badges & Flags",
		Value:  truncateUserInfo(badgeValue, 1024),
		Inline: false,
	})

	_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{Embeds: []*discordgo.MessageEmbed{embed}})
}

func userPremiumTypeName(premiumType discordgo.UserPremiumType) string {
	switch premiumType {
	case discordgo.UserPremiumTypeNitroClassic:
		return "Nitro Classic"
	case discordgo.UserPremiumTypeNitro:
		return "Nitro"
	case discordgo.UserPremiumTypeNitroBasic:
		return "Nitro Basic"
	default:
		return "Unknown"
	}
}

func formatUserPresence(presence *discordgo.Presence, err error) string {
	if err != nil || presence == nil {
		return "Offline or unavailable (presence intent/data may be unavailable)."
	}
	status := "Unknown"
	switch presence.Status {
	case discordgo.StatusOnline:
		status = "🟢 Online"
	case discordgo.StatusIdle:
		status = "🌙 Idle"
	case discordgo.StatusDoNotDisturb:
		status = "🔴 Do Not Disturb"
	case discordgo.StatusInvisible:
		status = "⚫ Invisible"
	case discordgo.StatusOffline:
		status = "⚫ Offline"
	}
	var details []string
	devices := []struct {
		name   string
		status discordgo.Status
	}{
		{"Desktop", presence.ClientStatus.Desktop},
		{"Mobile", presence.ClientStatus.Mobile},
		{"Web", presence.ClientStatus.Web},
	}
	var activeDevices []string
	for _, device := range devices {
		if device.status != "" && device.status != discordgo.StatusOffline {
			activeDevices = append(activeDevices, device.name+": "+statusLabel(device.status))
		}
	}
	if len(activeDevices) > 0 {
		details = append(details, "Clients: "+strings.Join(activeDevices, ", "))
	}
	for _, activity := range presence.Activities {
		if activity == nil {
			continue
		}
		details = append(details, formatUserActivity(activity))
	}
	if len(details) == 0 {
		return status
	}
	return status + "\n" + strings.Join(details, "\n")
}

func statusLabel(status discordgo.Status) string {
	switch status {
	case discordgo.StatusOnline:
		return "online"
	case discordgo.StatusIdle:
		return "idle"
	case discordgo.StatusDoNotDisturb:
		return "DND"
	case discordgo.StatusInvisible:
		return "invisible"
	case discordgo.StatusOffline:
		return "offline"
	default:
		return string(status)
	}
}

func formatUserActivity(activity *discordgo.Activity) string {
	prefix := "•"
	switch activity.Type {
	case discordgo.ActivityTypeGame:
		prefix = "🎮 Playing"
	case discordgo.ActivityTypeStreaming:
		prefix = "📡 Streaming"
	case discordgo.ActivityTypeListening:
		prefix = "🎵 Listening"
	case discordgo.ActivityTypeWatching:
		prefix = "📺 Watching"
	case discordgo.ActivityTypeCustom:
		prefix = "💬 Status"
	case discordgo.ActivityTypeCompeting:
		prefix = "🏆 Competing"
	}
	label := activity.Name
	if activity.Type == discordgo.ActivityTypeCustom && activity.State != "" {
		label = activity.State
	}
	value := prefix
	if label != "" {
		value += " **" + label + "**"
	}
	if activity.Details != "" {
		value += "\n  " + activity.Details
	}
	if activity.State != "" && !(activity.Type == discordgo.ActivityTypeCustom && activity.State == label) {
		value += "\n  " + activity.State
	}
	if activity.URL != "" && activity.Type == discordgo.ActivityTypeStreaming {
		value += "\n  " + activity.URL
	}
	if activity.Assets.LargeText != "" {
		value += "\n  " + activity.Assets.LargeText
	}
	if activity.Assets.SmallText != "" {
		value += " · " + activity.Assets.SmallText
	}
	if activity.Timestamps.StartTimestamp > 0 {
		value += fmt.Sprintf("\n  Started <t:%d:R>", activity.Timestamps.StartTimestamp/1000)
	}
	return value
}

func formatUserVoiceState(guild *discordgo.Guild, userID string) string {
	if guild == nil {
		return "Unavailable."
	}
	for _, voice := range guild.VoiceStates {
		if voice.UserID != userID {
			continue
		}
		value := fmt.Sprintf("🔊 <#%s>", voice.ChannelID)
		var states []string
		if voice.SelfMute {
			states = append(states, "self-muted")
		}
		if voice.Mute {
			states = append(states, "server-muted")
		}
		if voice.SelfDeaf {
			states = append(states, "self-deafened")
		}
		if voice.Deaf {
			states = append(states, "server-deafened")
		}
		if voice.SelfStream {
			states = append(states, "streaming")
		}
		if voice.SelfVideo {
			states = append(states, "camera on")
		}
		if voice.Suppress {
			states = append(states, "suppressed")
		}
		if voice.RequestToSpeakTimestamp != nil {
			states = append(states, fmt.Sprintf("requested to speak <t:%d:R>", voice.RequestToSpeakTimestamp.Unix()))
		}
		if len(states) > 0 {
			value += " (" + strings.Join(states, ", ") + ")"
		}
		return value
	}
	return "Not connected to voice."
}

func formatGuildMemberInfo(s *discordgo.Session, i *discordgo.InteractionCreate, user *discordgo.User, member *discordgo.Member, guild *discordgo.Guild) string {
	var lines []string
	if guild != nil && guild.OwnerID == user.ID {
		lines = append(lines, "👑 **Server owner**")
	}
	if member.Nick != "" {
		lines = append(lines, "**Server nickname:** "+member.Nick)
	}
	if !member.JoinedAt.IsZero() {
		lines = append(lines, fmt.Sprintf("**Joined:** <t:%d:f> (<t:%d:R>)", member.JoinedAt.Unix(), member.JoinedAt.Unix()))
	}
	if member.Pending {
		lines = append(lines, "**Membership screening:** Pending")
	}
	if member.Flags&discordgo.MemberFlagCompletedOnboarding != 0 {
		lines = append(lines, "**Onboarding:** Completed")
	} else if member.Flags&discordgo.MemberFlagStartedOnboarding != 0 {
		lines = append(lines, "**Onboarding:** Started")
	}
	if member.Flags&discordgo.MemberFlagDidRejoin != 0 {
		lines = append(lines, "**Membership:** Has left and rejoined")
	}
	if member.Flags&discordgo.MemberFlagBypassesVerification != 0 {
		lines = append(lines, "**Verification:** Bypasses guild verification")
	}
	if member.PremiumSince != nil {
		lines = append(lines, fmt.Sprintf("**Boosting since:** <t:%d:f> (<t:%d:R>) 💎", member.PremiumSince.Unix(), member.PremiumSince.Unix()))
	}
	if member.CommunicationDisabledUntil != nil && member.CommunicationDisabledUntil.After(time.Now()) {
		lines = append(lines, fmt.Sprintf("**Timed out until:** <t:%d:f> (<t:%d:R>) 🛑", member.CommunicationDisabledUntil.Unix(), member.CommunicationDisabledUntil.Unix()))
	}
	if member.Deaf || member.Mute {
		lines = append(lines, fmt.Sprintf("**Server voice state:** Muted: %t · Deafened: %t", member.Mute, member.Deaf))
	}

	if permissions, err := s.UserChannelPermissions(user.ID, i.ChannelID); err == nil {
		names := effectivePermissionNames(permissions)
		if len(names) == 0 {
			lines = append(lines, fmt.Sprintf("**Permissions in <#%s>:** No elevated permissions", i.ChannelID))
		} else {
			lines = append(lines, fmt.Sprintf("**Permissions in <#%s>:** %s", i.ChannelID, strings.Join(names, ", ")))
		}
	} else {
		logErrorf("could not calculate channel permissions for user %s in channel %s: %v", user.ID, i.ChannelID, err)
	}

	roleNames, highestRole := guildMemberRoleNames(member, guild)
	lines = append(lines, fmt.Sprintf("**Roles (%d):** %s", len(member.Roles), roleNames))
	if highestRole != "" {
		lines = append(lines, "**Highest role:** "+highestRole)
	}
	if member.Avatar != "" {
		lines = append(lines, fmt.Sprintf("[Server avatar](%s)", member.AvatarURL("1024")))
	}
	if member.Banner != "" {
		lines = append(lines, fmt.Sprintf("[Server profile banner](%s)", member.BannerURL("1024")))
	}
	return strings.Join(lines, "\n")
}

func effectivePermissionNames(permissions int64) []string {
	if permissions&discordgo.PermissionAdministrator != 0 {
		return []string{"Administrator (all permissions)"}
	}
	permissionsToShow := []struct {
		flag int64
		name string
	}{
		{discordgo.PermissionManageGuild, "Manage Server"},
		{discordgo.PermissionManageRoles, "Manage Roles"},
		{discordgo.PermissionManageChannels, "Manage Channels"},
		{discordgo.PermissionManageMessages, "Manage Messages"},
		{discordgo.PermissionManageThreads, "Manage Threads"},
		{discordgo.PermissionKickMembers, "Kick Members"},
		{discordgo.PermissionBanMembers, "Ban Members"},
		{discordgo.PermissionModerateMembers, "Timeout Members"},
		{discordgo.PermissionViewAuditLogs, "View Audit Log"},
		{discordgo.PermissionManageWebhooks, "Manage Webhooks"},
		{discordgo.PermissionManageNicknames, "Manage Nicknames"},
		{discordgo.PermissionMentionEveryone, "Mention Everyone"},
		{discordgo.PermissionUseApplicationCommands, "Use Application Commands"},
		{discordgo.PermissionManageEvents, "Manage Events"},
		{discordgo.PermissionVoiceMoveMembers, "Move Members"},
		{discordgo.PermissionVoiceMuteMembers, "Mute Members"},
		{discordgo.PermissionVoiceDeafenMembers, "Deafen Members"},
		{discordgo.PermissionSendMessages, "Send Messages"},
		{discordgo.PermissionAttachFiles, "Attach Files"},
		{discordgo.PermissionVoiceConnect, "Connect to Voice"},
		{discordgo.PermissionVoiceSpeak, "Speak"},
	}
	var names []string
	for _, permission := range permissionsToShow {
		if permissions&permission.flag != 0 {
			names = append(names, permission.name)
		}
	}
	return names
}

func guildMemberRoleNames(member *discordgo.Member, guild *discordgo.Guild) (string, string) {
	if len(member.Roles) == 0 {
		return "None", ""
	}
	rolesByID := make(map[string]*discordgo.Role)
	if guild != nil {
		for _, role := range guild.Roles {
			if role != nil {
				rolesByID[role.ID] = role
			}
		}
	}
	roles := make([]*discordgo.Role, 0, len(member.Roles))
	for _, roleID := range member.Roles {
		if role := rolesByID[roleID]; role != nil {
			roles = append(roles, role)
		}
	}
	sort.Slice(roles, func(a, b int) bool { return roles[a].Position > roles[b].Position })

	var lines []string
	highest := ""
	for _, role := range roles {
		if highest == "" {
			highest = role.Name
		}
		line := "• " + role.Name
		if role.Managed {
			line += " *(managed)*"
		}
		if role.Hoist {
			line += " *(displayed separately)*"
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return fmt.Sprintf("%d role(s; names unavailable)", len(member.Roles)), ""
	}
	return truncateUserInfo(strings.Join(lines, "\n"), 850), highest
}

func truncateUserInfo(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit-1]) + "…"
}

// getUserBadges uses bitwise operations to detect official Discord badges/flags on the account.
func getUserBadges(flags int) []string {
	var badges []string
	if flags&(1<<0) != 0 {
		badges = append(badges, "• Discord Employee")
	}
	if flags&(1<<1) != 0 {
		badges = append(badges, "• Partnered Server Owner")
	}
	if flags&(1<<2) != 0 {
		badges = append(badges, "• HypeSquad Events Member")
	}
	if flags&(1<<3) != 0 {
		badges = append(badges, "• Bug Hunter Level 1")
	}
	if flags&(1<<6) != 0 {
		badges = append(badges, "• House Bravery Member")
	}
	if flags&(1<<7) != 0 {
		badges = append(badges, "• House Brilliance Member")
	}
	if flags&(1<<8) != 0 {
		badges = append(badges, "• House Balance Member")
	}
	if flags&(1<<9) != 0 {
		badges = append(badges, "• Early Nitro Supporter")
	}
	if flags&(1<<10) != 0 {
		badges = append(badges, "• Team User")
	}
	if flags&(1<<14) != 0 {
		badges = append(badges, "• Bug Hunter Level 2")
	}
	if flags&(1<<16) != 0 {
		badges = append(badges, "• Verified Bot")
	}
	if flags&(1<<17) != 0 {
		badges = append(badges, "• Early Verified Bot Developer")
	}
	if flags&(1<<18) != 0 {
		badges = append(badges, "• Discord Certified Moderator")
	}
	if flags&(1<<19) != 0 {
		badges = append(badges, "• Bot uses HTTP interactions")
	}
	if flags&(1<<22) != 0 {
		badges = append(badges, "• Active Developer")
	}
	return badges
}

func logErrorf(format string, args ...interface{}) {
	fmt.Printf("ERROR: "+format+"\n", args...)
}

func coalesce(s ...string) string {
	for _, str := range s {
		if str != "" {
			return str
		}
	}
	return ""
}

func accentColor(c int) int {
	if c == 0 {
		return 0xFF6B35 // fallback orange
	}
	return c
}

func snowflakeToTimestamp(id string) int64 {
	var snow int64
	fmt.Sscanf(id, "%d", &snow)
	return ((snow >> 22) + 1420070400000) / 1000
}

func handlePurge(s *discordgo.Session, i *discordgo.InteractionCreate) {
	amountStr := ""
	channelID := i.ChannelID
	if i.ApplicationCommandData().Options != nil {
		for _, opt := range i.ApplicationCommandData().Options {
			if opt.Name == "amount" {
				amountStr = opt.StringValue()
			}
		}
	}

	// Confirm the purge before proceeding (optional safety)
	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	})

	var toDelete int
	if amountStr == "all" || amountStr == "" {
		// Purge all messages in the channel
		toDelete = 1000000 // sentinel for "all"
	} else {
		_, err := fmt.Sscanf(amountStr, "%d", &toDelete)
		if err != nil || toDelete < 1 || toDelete > 10000 {
			s.ChannelMessageSend(i.ChannelID, "❌ Amount must be a number between 1 and 10000, or 'all'.")
			return
		}
	}

	var messages []string // message IDs to delete
	beforeID := ""
	maxFetch := 100

	loopCount := 0
	for {
		if toDelete != 1000000 && len(messages) >= toDelete {
			break
		}
		if loopCount > 500 { // safety break
			break
		}
		loopCount++
		limit := maxFetch
		if toDelete != 1000000 && toDelete-len(messages) < limit {
			limit = toDelete - len(messages)
		}
		if limit <= 0 {
			break
		}

		msgs, err := s.ChannelMessages(channelID, limit, beforeID, "", "")
		if err != nil || len(msgs) == 0 {
			break
		}
		for _, msg := range msgs {
			messages = append(messages, msg.ID)
			beforeID = msg.ID
		}
	}

	deletedCount := 0
	// Bulk delete in chunks of 99 (Discord bulk delete max = 100, but safe at 99)
	idx := 0
	for idx < len(messages) {
		end := idx + 99
		if end > len(messages) {
			end = len(messages)
		}
		chunk := messages[idx:end]

		// Try bulk delete
		err := s.ChannelMessagesBulkDelete(channelID, chunk)
		if err != nil {
			// Bulk failed (likely >14 days for some), fall back to single deletes for this chunk
			for _, id := range chunk {
				err2 := s.ChannelMessageDelete(channelID, id)
				if err2 == nil {
					deletedCount++
				}
			}
		} else {
			deletedCount += len(chunk)
		}
		idx = end
	}

	s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
		Content: fmt.Sprintf("✅ Purged %d messages.", deletedCount),
	})
}

func handleSetup(s *discordgo.Session, i *discordgo.InteractionCreate) {
	guildID := i.GuildID
	if guildID == "" {
		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{Content: "❌ /setup must be run in a server."},
		})
		return
	}

	// Defer response to avoid Discord's 3-second interaction timeout during multiple API calls.
	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	})

	respondMsg := func(content string) {
		_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
			Content: content,
		})
	}

	guild, err := s.Guild(guildID)
	if err != nil {
		respondMsg("❌ Could not fetch guild info: " + err.Error())
		return
	}

	var everyoneRoleID string
	for _, r := range guild.Roles {
		if r.Name == "@everyone" {
			everyoneRoleID = r.ID
			break
		}
	}
	if everyoneRoleID == "" {
		everyoneRoleID = guild.ID
	}

	cfg, err := loadConfig()
	if err != nil {
		cfg = &Config{}
	}

	cfgModified := false
	var statusMessages []string

	// 1. Private Sunfire category + chats/logs channels.
	// If loggingChannelId already exists in config.json, do not recreate them.
	if cfg.LoggingChannel != "" {
		statusMessages = append(statusMessages, fmt.Sprintf("• **Logs channel**: <#%s> (already exists in config, skipped creation)", cfg.LoggingChannel))
	} else {
		categoryName := "Sunfire"
		category, err := s.GuildChannelCreateComplex(guildID, discordgo.GuildChannelCreateData{
			Name: categoryName,
			Type: discordgo.ChannelTypeGuildCategory,
			PermissionOverwrites: []*discordgo.PermissionOverwrite{
				{
					Type:  discordgo.PermissionOverwriteTypeRole,
					ID:    everyoneRoleID,
					Allow: 0,
					Deny:  1024 | 2048,
				},
			},
		})
		if err != nil {
			respondMsg("❌ Failed to create Sunfire category: " + err.Error())
			return
		}

		_, err = s.GuildChannelCreateComplex(guildID, discordgo.GuildChannelCreateData{
			Name:                 "chats",
			Type:                 discordgo.ChannelTypeGuildText,
			ParentID:             category.ID,
			PermissionOverwrites: category.PermissionOverwrites,
		})
		if err != nil {
			respondMsg("❌ Failed to create chats channel: " + err.Error())
			return
		}

		logsChannel, err := s.GuildChannelCreateComplex(guildID, discordgo.GuildChannelCreateData{
			Name:                 "logs",
			Type:                 discordgo.ChannelTypeGuildText,
			ParentID:             category.ID,
			PermissionOverwrites: category.PermissionOverwrites,
		})
		if err != nil {
			respondMsg("❌ Failed to create logs channel: " + err.Error())
			return
		}

		cfg.LoggingChannel = logsChannel.ID
		cfgModified = true
		statusMessages = append(statusMessages, fmt.Sprintf("• **Sunfire category**: created private category with #chats and #logs (<#%s>)", logsChannel.ID))
	}

	// Prepare permission overwrites for read-only INFO category & rules channel
	botUserID := ""
	if s.State != nil && s.State.User != nil {
		botUserID = s.State.User.ID
	}
	if botUserID == "" {
		if me, err := s.User("@me"); err == nil && me != nil {
			botUserID = me.ID
		}
	}

	infoOverwrites := []*discordgo.PermissionOverwrite{
		{
			Type:  discordgo.PermissionOverwriteTypeRole,
			ID:    everyoneRoleID,
			Allow: discordgo.PermissionViewChannel | discordgo.PermissionReadMessageHistory,
			Deny:  discordgo.PermissionSendMessages | discordgo.PermissionSendMessagesInThreads | discordgo.PermissionCreatePublicThreads | discordgo.PermissionCreatePrivateThreads | discordgo.PermissionAddReactions,
		},
	}
	if botUserID != "" {
		infoOverwrites = append(infoOverwrites, &discordgo.PermissionOverwrite{
			Type:  discordgo.PermissionOverwriteTypeMember,
			ID:    botUserID,
			Allow: discordgo.PermissionViewChannel | discordgo.PermissionSendMessages | discordgo.PermissionReadMessageHistory | discordgo.PermissionEmbedLinks,
			Deny:  0,
		})
	}

	// 2. Read-only INFO category.
	// If infoCategoryId already exists in config.json, do not recreate it.
	infoCategoryID := cfg.InfoCategoryID
	if infoCategoryID != "" {
		statusMessages = append(statusMessages, fmt.Sprintf("• **INFO category**: <#%s> (already exists in config, skipped creation)", infoCategoryID))
	} else {
		infoCategory, err := s.GuildChannelCreateComplex(guildID, discordgo.GuildChannelCreateData{
			Name:                 "INFO",
			Type:                 discordgo.ChannelTypeGuildCategory,
			PermissionOverwrites: infoOverwrites,
		})
		if err != nil {
			respondMsg("❌ Failed to create INFO category: " + err.Error())
			return
		}
		infoCategoryID = infoCategory.ID
		cfg.InfoCategoryID = infoCategory.ID
		cfgModified = true
		statusMessages = append(statusMessages, fmt.Sprintf("• **INFO category**: created read-only category (<#%s>)", infoCategory.ID))
	}

	// 3. Rules channel under INFO category and 1-time rules message.
	// If rulesChannelId already exists in config.json, do not recreate it or repost rules message.
	if cfg.RulesChannelID != "" {
		statusMessages = append(statusMessages, fmt.Sprintf("• **Rules channel**: <#%s> (already exists in config, rules message not reposted)", cfg.RulesChannelID))
	} else {
		rulesChannel, err := s.GuildChannelCreateComplex(guildID, discordgo.GuildChannelCreateData{
			Name:                 "rules",
			Type:                 discordgo.ChannelTypeGuildText,
			ParentID:             infoCategoryID,
			PermissionOverwrites: infoOverwrites,
		})
		if err != nil {
			respondMsg("❌ Failed to create rules channel: " + err.Error())
			return
		}

		cfg.RulesChannelID = rulesChannel.ID
		cfgModified = true

		// Post 1-time rules message in the rules channel
		rulesEmbed := &discordgo.MessageEmbed{
			Title: "📜 Server Rules & Guidelines",
			Description: "Welcome to **Sunfire**! To ensure a safe, welcoming, and enjoyable community for everyone, all members are required to follow these rules.\n\n" +
				"By participating in this server, you also agree to adhere to Discord's official policies:\n" +
				"• **Discord Terms of Service**: https://discord.com/terms\n" +
				"• **Discord Community Guidelines**: https://discord.com/guidelines",
			Color: 0xFF6B35,
			Fields: []*discordgo.MessageEmbedField{
				{
					Name:   "1. Be Respectful & Civil",
					Value:  "Treat all members with courtesy and respect. Harassment, hate speech, discrimination, personal attacks, slurs, or toxic behavior will not be tolerated.",
					Inline: false,
				},
				{
					Name:   "2. Discord Terms of Service & Community Guidelines",
					Value:  "You must comply with the [Discord Terms of Service](https://discord.com/terms) and [Discord Community Guidelines](https://discord.com/guidelines) at all times.",
					Inline: false,
				},
				{
					Name:   "3. No Spam or Flooding",
					Value:  "Do not spam messages, emojis, reactions, mentions, or post unsolicited self-promotion, advertisements, or invite links.",
					Inline: false,
				},
				{
					Name:   "4. Appropriate Content",
					Value:  "Keep content safe and appropriate. No NSFW, sexually explicit, gory, graphic, or illegal content.",
					Inline: false,
				},
				{
					Name:   "5. Privacy & Safety",
					Value:  "Respect personal privacy. Do not share personal identifying information (doxxing) or suspicious links.",
					Inline: false,
				},
				{
					Name:   "6. Follow Staff Directions",
					Value:  "Moderators and administrators have final discretion on server enforcement. Please cooperate with server staff.",
					Inline: false,
				},
			},
			Footer: &discordgo.MessageEmbedFooter{
				Text: "Sunfire • Please abide by these rules to keep our community safe and fun.",
			},
		}

		_, err = s.ChannelMessageSendComplex(rulesChannel.ID, &discordgo.MessageSend{
			Content: "**Welcome to the server!** Please read and follow our community rules:\n" +
				"• **Discord Terms of Service**: https://discord.com/terms\n" +
				"• **Discord Community Guidelines**: https://discord.com/guidelines",
			Embeds: []*discordgo.MessageEmbed{rulesEmbed},
		})
		if err != nil {
			logErrorf("failed to send rules message to channel %s: %v", rulesChannel.ID, err)
		}

		statusMessages = append(statusMessages, fmt.Sprintf("• **Rules channel**: created <#%s> and posted rules message", rulesChannel.ID))
	}

	// 4. Feeds & Media Category and Channels (YouTube, Spotify, Movies, FreeStuff, Kick).
	gCfg := getGuildSettings(guildID)
	gCfgModified := false

	feedsCategoryName := "Feeds & Media"
	var feedsCategoryID string

	// Check if an existing feeds category already exists in the guild
	guildChannels, err := s.GuildChannels(guildID)
	if err == nil {
		for _, ch := range guildChannels {
			if ch.Type == discordgo.ChannelTypeGuildCategory && strings.EqualFold(ch.Name, feedsCategoryName) {
				feedsCategoryID = ch.ID
				break
			}
		}
	}

	if feedsCategoryID == "" {
		feedsCat, err := s.GuildChannelCreateComplex(guildID, discordgo.GuildChannelCreateData{
			Name:                 feedsCategoryName,
			Type:                 discordgo.ChannelTypeGuildCategory,
			PermissionOverwrites: infoOverwrites,
		})
		if err == nil && feedsCat != nil {
			feedsCategoryID = feedsCat.ID
			statusMessages = append(statusMessages, fmt.Sprintf("• **Feeds category**: created read-only category (<#%s>)", feedsCat.ID))
		}
	}

	createFeedChannel := func(channelName string, currentID string) (string, bool) {
		if currentID != "" {
			// Check if channel still exists
			if ch, err := s.Channel(currentID); err == nil && ch != nil {
				return currentID, false
			}
		}
		newCh, err := s.GuildChannelCreateComplex(guildID, discordgo.GuildChannelCreateData{
			Name:                 channelName,
			Type:                 discordgo.ChannelTypeGuildText,
			ParentID:             feedsCategoryID,
			PermissionOverwrites: infoOverwrites,
		})
		if err != nil || newCh == nil {
			return currentID, false
		}
		return newCh.ID, true
	}

	// YouTube Channel
	if newID, created := createFeedChannel("youtube", gCfg.YouTubeChannelID); created {
		gCfg.YouTubeChannelID = newID
		gCfg.YouTubeEnabled = true
		gCfgModified = true
		statusMessages = append(statusMessages, fmt.Sprintf("• **YouTube channel**: created <#%s>", newID))
	} else if gCfg.YouTubeChannelID != "" {
		statusMessages = append(statusMessages, fmt.Sprintf("• **YouTube channel**: <#%s> (exists)", gCfg.YouTubeChannelID))
	}

	// Spotify Channel
	if newID, created := createFeedChannel("spotify", gCfg.SpotifyChannelID); created {
		gCfg.SpotifyChannelID = newID
		gCfg.SpotifyEnabled = true
		gCfgModified = true
		statusMessages = append(statusMessages, fmt.Sprintf("• **Spotify channel**: created <#%s>", newID))
	} else if gCfg.SpotifyChannelID != "" {
		statusMessages = append(statusMessages, fmt.Sprintf("• **Spotify channel**: <#%s> (exists)", gCfg.SpotifyChannelID))
	}

	// Movies Channel
	if newID, created := createFeedChannel("movies", gCfg.MoviesThisWeekChannelID); created {
		gCfg.MoviesThisWeekChannelID = newID
		gCfg.StreamingThisWeekChannelID = newID
		gCfgModified = true
		statusMessages = append(statusMessages, fmt.Sprintf("• **Movies channel**: created <#%s>", newID))
	} else if gCfg.MoviesThisWeekChannelID != "" {
		statusMessages = append(statusMessages, fmt.Sprintf("• **Movies channel**: <#%s> (exists)", gCfg.MoviesThisWeekChannelID))
	}

	// FreeStuff Channel
	if newID, created := createFeedChannel("free-stuff", gCfg.FreeStuffChannelID); created {
		gCfg.FreeStuffChannelID = newID
		gCfg.FreeStuffEnabled = true
		gCfgModified = true
		statusMessages = append(statusMessages, fmt.Sprintf("• **FreeStuff channel**: created <#%s>", newID))
	} else if gCfg.FreeStuffChannelID != "" {
		statusMessages = append(statusMessages, fmt.Sprintf("• **FreeStuff channel**: <#%s> (exists)", gCfg.FreeStuffChannelID))
	}

	// Kick Streams Channel
	if newID, created := createFeedChannel("kick-streams", gCfg.KickChannelID); created {
		gCfg.KickChannelID = newID
		gCfg.KickEnabled = true
		gCfgModified = true
		statusMessages = append(statusMessages, fmt.Sprintf("• **Kick streams channel**: created <#%s>", newID))
	} else if gCfg.KickChannelID != "" {
		statusMessages = append(statusMessages, fmt.Sprintf("• **Kick streams channel**: <#%s> (exists)", gCfg.KickChannelID))
	}

	if gCfgModified {
		_ = saveGuildSettings()
	}

	if cfgModified {
		if err := saveConfig(configPath(), cfg); err != nil {
			logErrorf("failed to save config: %v", err)
			respondMsg("⚠️️ Channels created, but failed to save config: " + err.Error())
			return
		}
	}

	resultContent := "✅ **Setup Summary:**\n"
	for _, msg := range statusMessages {
		resultContent += msg + "\n"
	}
	respondMsg(resultContent)
}

func handleDedup(s *discordgo.Session, i *discordgo.InteractionCreate) {
	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	})

	deleted, freed, err := DeduplicateAllMediaLogs()
	if err != nil {
		_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
			Content: fmt.Sprintf("❌ Deduplication error: %v", err),
		})
		return
	}

	var content string
	if deleted == 0 {
		content = "✅ **Media Deduplication:** No duplicate files found across server media folders."
	} else {
		content = fmt.Sprintf("✅ **Media Deduplication Complete:** Removed **%d** duplicate file(s) (freed **%.2f MB**).", deleted, float64(freed)/(1024*1024))
	}

	_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
		Content: content,
	})
}

func handleNotifiersCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	data := i.ApplicationCommandData()
	if len(data.Options) == 0 {
		return
	}

	sub := data.Options[0]
	cfg, err := loadConfig()
	if err != nil {
		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{Content: "❌ Failed to load config."},
		})
		return
	}

	switch sub.Name {
	case "status":
		guilds := loadGuildSettings()
		var gInfo string
		for gID, g := range guilds {
			gInfo += fmt.Sprintf("• **Guild %s**:\n  - YouTube: %t (Chan: %s, YouTubers: %d)\n  - Kick: %t (Chan: %s, Streamers: %d)\n  - Spotify: %t (Chan: %s)\n  - Movies: Chan: %s\n  - FreeStuff: %t (Chan: %s)\n",
				gID, g.YouTubeEnabled, g.YouTubeChannelID, len(g.YouTubers),
				g.KickEnabled, g.KickChannelID, len(g.KickStreamers),
				g.SpotifyEnabled, g.SpotifyChannelID,
				g.MoviesThisWeekChannelID,
				g.FreeStuffEnabled, g.FreeStuffChannelID,
			)
		}
		if gInfo == "" {
			gInfo = "No per-guild notifier channels configured."
		}

		embed := &discordgo.MessageEmbed{
			Title: "🔔 Media & Stream Notifiers Status",
			Description: fmt.Sprintf("**Global Credentials:**\n• YouTube API: %t\n• Kick API: %t\n• TMDB API: %t\n• Spotify API: %t\n• Scrape Targets: %d URLs -> %d Channels\n\n**Guild Configurations:**\n%s",
				cfg.YouTubeAPIKey != "",
				cfg.KickClientID != "",
				cfg.TMDBAPIKey != "",
				cfg.SpotifyClientID != "",
				len(cfg.Scrape.URLs), len(cfg.Scrape.ChannelIDs),
				gInfo,
			),
			Color: 0x3498DB,
		}

		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Embeds: []*discordgo.MessageEmbed{embed},
			},
		})

	case "trigger":
		service := ""
		for _, o := range sub.Options {
			if o.Name == "service" {
				service = o.StringValue()
			}
		}

		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: fmt.Sprintf("⚡ Triggering poll pass for **%s**...", service),
			},
		})

		go func() {
			switch service {
			case "youtube":
				checkYouTubeUpdates(s, cfg)
			case "kick":
				checkKickUpdates(s, cfg)
			case "spotify":
				checkSpotifyUpdates(s, cfg)
			case "movies":
				checkMoviesUpdates(s, cfg)
			case "freestuff":
				checkFreeStuffUpdates(s, cfg)
			case "hermes":
				_, _ = checkHermesTipsUpdates(s)
			case "scrape":
				runDailyImageScrape(s, cfg)
			}
		}()
	}
}

```

## `./message_log.go`

```go
// Package main — message_log.go handles PostgreSQL message archiving,
// channel tracking via Data/messageLogChannels.json, and full-text search.
package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	_ "github.com/lib/pq"
)

var (
	msgLogChannelsFilePath = dataPath("messageLogChannels.json")
	msgLogStoreMu          sync.Mutex

	pgPool   *sql.DB
	pgPoolMu sync.Mutex

	msgQueue             []*loggedMessageRow
	msgQueueMu           sync.Mutex
	isFlushing           bool
	messageLogRetryDelay = 10 * time.Second
	messageLogDBProvider = getPostgresPool

	channelIDRegex = regexp.MustCompile(`^\d{10,25}$`)
)

type loggedMessageRow struct {
	ID        int64
	ServerID  int64
	ChannelID int64
	UserID    int64
	Content   string
	CreatedAt time.Time
}

type LoggedMessageResult struct {
	ID        string
	ServerID  string
	ChannelID string
	UserID    string
	Content   string
	CreatedAt time.Time
}

func loadLoggedChannelIDs(guildID string) []string {
	msgLogStoreMu.Lock()
	defer msgLogStoreMu.Unlock()

	data, err := os.ReadFile(msgLogChannelsFilePath)
	if err != nil {
		return nil
	}

	var root map[string][]string
	if err := json.Unmarshal(data, &root); err != nil {
		return nil
	}

	return root[guildID]
}

func addLoggedChannelID(guildID, channelID string) (ok bool, reason string) {
	if !channelIDRegex.MatchString(channelID) {
		return false, "invalid-id"
	}

	msgLogStoreMu.Lock()
	defer msgLogStoreMu.Unlock()

	_ = os.MkdirAll(filepath.Dir(msgLogChannelsFilePath), 0755)
	root := make(map[string][]string)
	data, err := os.ReadFile(msgLogChannelsFilePath)
	if err == nil {
		_ = json.Unmarshal(data, &root)
	}

	list := root[guildID]
	for _, id := range list {
		if id == channelID {
			return false, "already-added"
		}
	}

	list = append(list, channelID)
	root[guildID] = list

	b, _ := json.MarshalIndent(root, "", "  ")
	_ = os.WriteFile(msgLogChannelsFilePath, b, 0644)
	return true, ""
}

func removeLoggedChannelID(guildID, channelID string) (ok bool, reason string) {
	if !channelIDRegex.MatchString(channelID) {
		return false, "invalid-id"
	}

	msgLogStoreMu.Lock()
	defer msgLogStoreMu.Unlock()

	data, err := os.ReadFile(msgLogChannelsFilePath)
	if err != nil {
		return false, "not-found"
	}

	root := make(map[string][]string)
	if err := json.Unmarshal(data, &root); err != nil {
		return false, "not-found"
	}

	list := root[guildID]
	var next []string
	found := false
	for _, id := range list {
		if id == channelID {
			found = true
		} else {
			next = append(next, id)
		}
	}

	if !found {
		return false, "not-found"
	}

	root[guildID] = next
	b, _ := json.MarshalIndent(root, "", "  ")
	_ = os.WriteFile(msgLogChannelsFilePath, b, 0644)
	return true, ""
}

func isChannelLogged(guildID, channelID string) bool {
	ids := loadLoggedChannelIDs(guildID)
	for _, id := range ids {
		if id == channelID {
			return true
		}
	}
	return false
}

func getPostgresPool() *sql.DB {
	db, err := getPostgresPoolWithError()
	if err != nil {
		log.Printf("[message-log-db] %v", err)
	}
	return db
}

func getPostgresPoolWithError() (*sql.DB, error) {
	pgPoolMu.Lock()
	defer pgPoolMu.Unlock()

	if pgPool != nil {
		return pgPool, nil
	}

	cfg, err := loadConfig()
	if err != nil {
		return nil, fmt.Errorf("loading PostgreSQL configuration: %w", err)
	}
	connectionString, err := postgresConnectionString(cfg)
	if err != nil {
		return nil, fmt.Errorf("preparing PostgreSQL connection: %w", err)
	}
	if connectionString == "" {
		return nil, fmt.Errorf("PostgreSQL is not configured (set POSTGRES_CONNECTION_STRING in config or the environment)")
	}

	db, err := sql.Open("postgres", connectionString)
	if err != nil {
		return nil, fmt.Errorf("opening PostgreSQL connection: %w", err)
	}

	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(30 * time.Minute)

	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("connecting to PostgreSQL: %w", err)
	}

	pgPool = db
	return pgPool, nil
}

func postgresConnectionString(cfg *Config) (string, error) {
	connectionString := ""
	if cfg != nil && strings.TrimSpace(cfg.PostgresConnectionString) != "" {
		connectionString = strings.TrimSpace(cfg.PostgresConnectionString)
	} else {
		connectionString = strings.TrimSpace(os.Getenv("POSTGRES_CONNECTION_STRING"))
	}
	if connectionString == "" {
		return "", nil
	}

	if strings.HasPrefix(connectionString, "postgres://") || strings.HasPrefix(connectionString, "postgresql://") {
		parsed, err := url.Parse(connectionString)
		if err != nil {
			return "", err
		}
		query := parsed.Query()
		query.Set("sslmode", "disable")
		parsed.RawQuery = query.Encode()
		return parsed.String(), nil
	}

	return connectionString + " sslmode=disable", nil
}

func enqueueLoggedMessage(m *discordgo.MessageCreate) {
	if m == nil || m.ID == "" || m.GuildID == "" || m.ChannelID == "" || m.Author == nil {
		return
	}

	id, _ := parseBigInt(m.ID)
	serverID, _ := parseBigInt(m.GuildID)
	channelID, _ := parseBigInt(m.ChannelID)
	userID, _ := parseBigInt(m.Author.ID)

	if id == 0 || serverID == 0 || channelID == 0 || userID == 0 {
		return
	}

	createdAt := m.Timestamp.UTC()

	content := m.Content
	if len(content) > 4000 {
		content = content[:4000]
	}

	row := &loggedMessageRow{
		ID:        id,
		ServerID:  serverID,
		ChannelID: channelID,
		UserID:    userID,
		Content:   content,
		CreatedAt: createdAt,
	}

	msgQueueMu.Lock()
	msgQueue = append(msgQueue, row)
	shouldFlush := !isFlushing
	if shouldFlush {
		isFlushing = true
	}
	msgQueueMu.Unlock()

	if shouldFlush {
		go flushMessageLogQueue()
	}
}

func flushMessageLogQueue() {
	db := messageLogDBProvider()
	if db == nil {
		msgQueueMu.Lock()
		queueLength := len(msgQueue)
		msgQueueMu.Unlock()
		log.Printf("[message-log-db] PostgreSQL unavailable; retaining %d queued messages for retry", queueLength)
		requeueBatch(nil)
		return
	}

	msgQueueMu.Lock()
	batch := msgQueue
	msgQueue = nil
	msgQueueMu.Unlock()

	if len(batch) == 0 {
		msgQueueMu.Lock()
		isFlushing = false
		msgQueueMu.Unlock()
		return
	}

	tx, err := db.Begin()
	if err != nil {
		log.Printf("[message-log-db] begin tx failed: %v", err)
		requeueBatch(batch)
		return
	}

	stmt, err := tx.Prepare(`
		INSERT INTO messages (id, server_id, channel_id, user_id, content, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (id) DO NOTHING
	`)
	if err != nil {
		_ = tx.Rollback()
		log.Printf("[message-log-db] prepare statement failed: %v", err)
		requeueBatch(batch)
		return
	}
	defer stmt.Close()

	for _, r := range batch {
		_, err := stmt.Exec(r.ID, r.ServerID, r.ChannelID, r.UserID, r.Content, r.CreatedAt)
		if err != nil {
			_ = tx.Rollback()
			log.Printf("[message-log-db] exec insert failed: %v", err)
			requeueBatch(batch)
			return
		}
	}

	if err := tx.Commit(); err != nil {
		log.Printf("[message-log-db] commit failed: %v", err)
		requeueBatch(batch)
		return
	}

	msgQueueMu.Lock()
	if len(msgQueue) > 0 {
		go func() {
			time.Sleep(1 * time.Second)
			flushMessageLogQueue()
		}()
	} else {
		isFlushing = false
	}
	msgQueueMu.Unlock()
}

func requeueBatch(batch []*loggedMessageRow) {
	msgQueueMu.Lock()
	msgQueue = append(batch, msgQueue...)
	isFlushing = false
	msgQueueMu.Unlock()
	time.AfterFunc(messageLogRetryDelay, func() {
		msgQueueMu.Lock()
		if !isFlushing && len(msgQueue) > 0 {
			isFlushing = true
			msgQueueMu.Unlock()
			flushMessageLogQueue()
			return
		}
		msgQueueMu.Unlock()
	})
}

func parseBigInt(s string) (int64, error) {
	return strconv.ParseInt(s, 10, 64)
}

func searchPostgresMessages(serverID, channelID, userID, query string, limit int) ([]LoggedMessageResult, error) {
	db := getPostgresPool()
	if db == nil {
		return nil, fmt.Errorf("PostgreSQL is not configured (missing POSTGRES_CONNECTION_STRING)")
	}

	if limit < 1 {
		limit = 10
	} else if limit > 25 {
		limit = 25
	}

	var clauses []string
	var args []any
	n := 1

	if serverID != "" {
		sID, err := parseBigInt(serverID)
		if err != nil {
			return nil, fmt.Errorf("invalid server ID: %w", err)
		}
		clauses = append(clauses, fmt.Sprintf("server_id = $%d", n))
		args = append(args, sID)
		n++
	}
	if channelID != "" {
		cID, err := parseBigInt(channelID)
		if err != nil {
			return nil, fmt.Errorf("invalid channel ID: %w", err)
		}
		clauses = append(clauses, fmt.Sprintf("channel_id = $%d", n))
		args = append(args, cID)
		n++
	}
	if userID != "" {
		uID, err := parseBigInt(userID)
		if err != nil {
			return nil, fmt.Errorf("invalid user ID: %w", err)
		}
		clauses = append(clauses, fmt.Sprintf("user_id = $%d", n))
		args = append(args, uID)
		n++
	}
	if query != "" {
		clauses = append(clauses, fmt.Sprintf("content ILIKE $%d", n))
		args = append(args, "%"+query+"%")
		n++
	}

	whereClause := "TRUE"
	if len(clauses) > 0 {
		whereClause = strings.Join(clauses, " AND ")
	}

	sqlText := fmt.Sprintf(`
		SELECT id, server_id, channel_id, user_id, content, created_at
		FROM messages
		WHERE %s
		ORDER BY created_at DESC
		LIMIT $%d
	`, whereClause, n)
	args = append(args, limit)

	rows, err := db.Query(sqlText, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []LoggedMessageResult
	for rows.Next() {
		var id, sID, cID, uID int64
		var content sql.NullString
		var createdAt time.Time

		if err := rows.Scan(&id, &sID, &cID, &uID, &content, &createdAt); err != nil {
			return nil, fmt.Errorf("reading message search result: %w", err)
		}

		results = append(results, LoggedMessageResult{
			ID:        fmt.Sprintf("%d", id),
			ServerID:  fmt.Sprintf("%d", sID),
			ChannelID: fmt.Sprintf("%d", cID),
			UserID:    fmt.Sprintf("%d", uID),
			Content:   content.String,
			CreatedAt: createdAt,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating message search results: %w", err)
	}

	return results, nil
}

func getPostgresMessageByID(messageID string) (*LoggedMessageResult, error) {
	db := getPostgresPool()
	if db == nil {
		return nil, fmt.Errorf("PostgreSQL is not configured")
	}

	id, err := parseBigInt(messageID)
	if err != nil {
		return nil, fmt.Errorf("invalid message ID")
	}

	row := db.QueryRow(`
		SELECT id, server_id, channel_id, user_id, content, created_at
		FROM messages
		WHERE id = $1
	`, id)

	var mID, sID, cID, uID int64
	var content sql.NullString
	var createdAt time.Time

	if err := row.Scan(&mID, &sID, &cID, &uID, &content, &createdAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("message is not in the archive; fetch an ID from `/message-log search` (only messages captured while logging was enabled can be fetched)")
		}
		return nil, err
	}

	return &LoggedMessageResult{
		ID:        fmt.Sprintf("%d", mID),
		ServerID:  fmt.Sprintf("%d", sID),
		ChannelID: fmt.Sprintf("%d", cID),
		UserID:    fmt.Sprintf("%d", uID),
		Content:   content.String,
		CreatedAt: createdAt,
	}, nil
}

func getPostgresStats() (total int64, servers int64, channels int64, err error) {
	db, err := getPostgresPoolWithError()
	if err != nil {
		return 0, 0, 0, err
	}

	row := db.QueryRow(`
		SELECT count(*)::bigint AS total, count(DISTINCT server_id) AS servers, count(DISTINCT channel_id) AS channels
		FROM messages
	`)
	if err := row.Scan(&total, &servers, &channels); err != nil {
		return 0, 0, 0, err
	}
	return total, servers, channels, nil
}

// Hook called in onMessageCreate
func processMessageLogging(m *discordgo.MessageCreate) {
	if m.GuildID == "" || m.ChannelID == "" {
		return
	}
	if isChannelLogged(m.GuildID, m.ChannelID) {
		enqueueLoggedMessage(m)
	}
}

// Slash command: /message-log [add|remove|list|search|stats|fetch]
func handleMessageLog(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if i.GuildID == "" {
		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "❌ This command can only be used inside a server.",
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
	case "add":
		var channelID string
		for _, opt := range sub.Options {
			if opt.Name == "channel" {
				channelID = opt.ChannelValue(s).ID
			}
		}
		if channelID == "" {
			channelID = i.ChannelID
		}

		ok, reason := addLoggedChannelID(i.GuildID, channelID)
		if !ok {
			msg := "❌ That channel ID is not valid."
			if reason == "already-added" {
				msg = fmt.Sprintf("ℹ️ <#%s> is already being logged.", channelID)
			}
			_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content: msg,
					Flags:   discordgo.MessageFlagsEphemeral,
				},
			})
			return
		}

		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: fmt.Sprintf("✅ Now logging <#%s> (`%s`) to PostgreSQL.", channelID, channelID),
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})

	case "remove":
		var channelID string
		for _, opt := range sub.Options {
			if opt.Name == "channel" {
				channelID = opt.ChannelValue(s).ID
			}
		}
		if channelID == "" {
			channelID = i.ChannelID
		}

		ok, reason := removeLoggedChannelID(i.GuildID, channelID)
		if !ok {
			msg := "❌ That channel ID is not valid."
			if reason == "not-found" {
				msg = fmt.Sprintf("ℹ️ <#%s> is not currently being logged.", channelID)
			}
			_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content: msg,
					Flags:   discordgo.MessageFlagsEphemeral,
				},
			})
			return
		}

		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: fmt.Sprintf("✅ Stopped logging <#%s> (`%s`).", channelID, channelID),
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})

	case "list":
		ids := loadLoggedChannelIDs(i.GuildID)
		if len(ids) == 0 {
			_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content: "ℹ️ No channels are being logged. Use `/message-log add <channel>`.",
					Flags:   discordgo.MessageFlagsEphemeral,
				},
			})
			return
		}

		var lines []string
		for idx, id := range ids {
			lines = append(lines, fmt.Sprintf("%d. <#%s> (`%s`)", idx+1, id, id))
		}

		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: fmt.Sprintf("📋 **Channels being logged (%d):**\n%s", len(ids), strings.Join(lines, "\n")),
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})

	case "search":
		var query, channelID, userID string
		limit := 10
		for _, opt := range sub.Options {
			switch opt.Name {
			case "query":
				query = opt.StringValue()
			case "channel":
				channelID = opt.ChannelValue(s).ID
			case "user":
				userID = opt.UserValue(s).ID
			case "limit":
				limit = int(opt.IntValue())
			}
		}

		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Flags: discordgo.MessageFlagsEphemeral,
			},
		})

		results, err := searchPostgresMessages(i.GuildID, channelID, userID, query, limit)
		if err != nil {
			_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
				Content: fmt.Sprintf("❌ Search failed: %v", err),
				Flags:   discordgo.MessageFlagsEphemeral,
			})
			return
		}

		if len(results) == 0 {
			_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
				Content: fmt.Sprintf("No messages found matching %q.", query),
				Flags:   discordgo.MessageFlagsEphemeral,
			})
			return
		}

		content := fmt.Sprintf("🔎 **%d result(s)** for %q:\n", len(results), query)
		for _, r := range results {
			snippet := r.Content
			if len(snippet) > 120 {
				snippet = snippet[:120] + "..."
			}
			ts := fmt.Sprintf("<t:%d:R>", r.CreatedAt.Unix())
			content += fmt.Sprintf("• <#%s> · <@%s> · %s · ID `%s`\n  `%s`\n", r.ChannelID, r.UserID, ts, r.ID, snippet)
		}
		content += "\n_Use `/message-log fetch messageid:<id>` to download the full message as a markdown file._"

		_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
			Content: content,
			Flags:   discordgo.MessageFlagsEphemeral,
		})

	case "stats":
		total, servers, channels, err := getPostgresStats()
		if err != nil {
			_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content: fmt.Sprintf("❌ Stats unavailable: %v", err),
					Flags:   discordgo.MessageFlagsEphemeral,
				},
			})
			return
		}

		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: fmt.Sprintf("📊 **Message log stats**\n• Messages: %d\n• Servers: %d\n• Channels: %d", total, servers, channels),
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})

	case "fetch":
		var messageID string
		for _, opt := range sub.Options {
			if opt.Name == "messageid" {
				messageID = opt.StringValue()
			}
		}

		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Flags: discordgo.MessageFlagsEphemeral,
			},
		})

		msg, err := getPostgresMessageByID(messageID)
		if err != nil {
			_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
				Content: fmt.Sprintf("❌ Could not fetch message `%s`: %v", messageID, err),
				Flags:   discordgo.MessageFlagsEphemeral,
			})
			return
		}

		mdContent := fmt.Sprintf("# Message Log — %s\n\n"+
			"- **ID**: `%s`\n"+
			"- **Server**: `%s`\n"+
			"- **Channel**: <#%s> (`%s`)\n"+
			"- **Author**: <@%s> (`%s`)\n"+
			"- **Timestamp**: %s\n\n"+
			"---\n\n%s\n",
			msg.ID, msg.ID, msg.ServerID, msg.ChannelID, msg.ChannelID, msg.UserID, msg.UserID,
			msg.CreatedAt.Format(time.RFC3339), msg.Content)

		filename := fmt.Sprintf("message-%s.md", msg.ID)
		reader := strings.NewReader(mdContent)

		_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
			Content: fmt.Sprintf("Full message **`%s`** (%d chars):", msg.ID, len(msg.Content)),
			Files: []*discordgo.File{
				{
					Name:   filename,
					Reader: reader,
				},
			},
			Flags: discordgo.MessageFlagsEphemeral,
		})
	}
}

```

## `./openrouter_test.go`

```go
package main

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
)

func TestOpenRouterCommandRegistersAnalyticsAndVideoWithinDiscordLimit(t *testing.T) {
	spec := openRouterCommandSpec()
	if len(spec.Command.Options) > 25 {
		t.Fatalf("openrouter has %d options; Discord permits at most 25", len(spec.Command.Options))
	}

	options := make(map[string]*discordgo.ApplicationCommandOption, len(spec.Command.Options))
	for _, option := range spec.Command.Options {
		options[option.Name] = option
	}
	for _, name := range []string{"analytics", "video"} {
		if options[name] == nil {
			t.Errorf("/openrouter %s is not registered", name)
		}
	}
	analytics := options["analytics"]
	if analytics != nil {
		if len(analytics.Options) != 1 || analytics.Options[0].Name != "days" {
			t.Errorf("/openrouter analytics options = %+v, want optional days parameter", analytics.Options)
		}
	}
}

func TestOpenRouterAnalyticsBuildsSupportedQuery(t *testing.T) {
	now := time.Date(2026, time.October, 4, 5, 59, 32, 0, time.FixedZone("UTC+2", 2*60*60))
	query := newORAnalyticsQuery(now, 7)

	if len(query.Metrics) != 1 || query.Metrics[0] != "request_count" {
		t.Fatalf("analytics metrics = %v, want only documented request_count metric", query.Metrics)
	}
	if query.Granularity != "day" || len(query.Dimensions) != 1 || query.Dimensions[0] != "model" {
		t.Fatalf("unexpected analytics grouping: dimensions=%v granularity=%q", query.Dimensions, query.Granularity)
	}
	if query.TimeRange.Start != "2026-09-27T03:59:32Z" || query.TimeRange.End != "2026-10-04T03:59:32Z" {
		t.Errorf("unexpected UTC analytics range: start=%q end=%q", query.TimeRange.Start, query.TimeRange.End)
	}
	encoded, err := json.Marshal(query)
	if err != nil {
		t.Fatalf("failed to marshal analytics query: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatalf("failed to decode analytics query: %v", err)
	}
	if _, exists := payload["total_cost"]; exists {
		t.Error("analytics query includes unsupported top-level total_cost field")
	}
	if payload["limit"] != float64(100) {
		t.Errorf("analytics limit = %v, want 100", payload["limit"])
	}
}

```

## `./message_log_queue_test.go`

```go
package main

import (
	"database/sql"
	"database/sql/driver"
	"errors"
	"sync"
	"testing"
	"time"
)

type queueTestDriver struct{}

type queueTestState struct {
	mu           sync.Mutex
	failNextExec bool
	commits      int
}

var queueTestStateValue *queueTestState

func (queueTestDriver) Open(string) (driver.Conn, error) {
	return queueTestConn{}, nil
}

type queueTestConn struct{}

func (queueTestConn) Prepare(string) (driver.Stmt, error) {
	return queueTestStmt{}, nil
}
func (queueTestConn) Close() error { return nil }
func (queueTestConn) Begin() (driver.Tx, error) {
	return queueTestTx{}, nil
}

type queueTestStmt struct{}

func (queueTestStmt) Close() error  { return nil }
func (queueTestStmt) NumInput() int { return -1 }
func (queueTestStmt) Exec([]driver.Value) (driver.Result, error) {
	queueTestStateValue.mu.Lock()
	defer queueTestStateValue.mu.Unlock()
	if queueTestStateValue.failNextExec {
		queueTestStateValue.failNextExec = false
		return nil, errors.New("injected insert failure")
	}
	return driver.RowsAffected(1), nil
}
func (queueTestStmt) Query([]driver.Value) (driver.Rows, error) {
	return nil, errors.New("unexpected query")
}

type queueTestTx struct{}

func (queueTestTx) Commit() error {
	queueTestStateValue.mu.Lock()
	queueTestStateValue.commits++
	queueTestStateValue.mu.Unlock()
	return nil
}
func (queueTestTx) Rollback() error { return nil }

var registerQueueTestDriver sync.Once

func openQueueTestDB(t *testing.T, state *queueTestState) *sql.DB {
	t.Helper()
	registerQueueTestDriver.Do(func() { sql.Register("sunfire-queue-test", queueTestDriver{}) })
	queueTestStateValue = state
	db, err := sql.Open("sunfire-queue-test", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func resetMessageLogQueue(t *testing.T) {
	t.Helper()
	msgQueueMu.Lock()
	oldQueue, oldFlushing := msgQueue, isFlushing
	msgQueue = nil
	isFlushing = false
	msgQueueMu.Unlock()
	t.Cleanup(func() {
		msgQueueMu.Lock()
		msgQueue, isFlushing = oldQueue, oldFlushing
		msgQueueMu.Unlock()
	})
}

func TestFlushMessageLogQueueRetainsMessagesWhenPostgresUnavailable(t *testing.T) {
	resetMessageLogQueue(t)
	oldProvider, oldDelay := messageLogDBProvider, messageLogRetryDelay
	messageLogDBProvider = func() *sql.DB { return nil }
	messageLogRetryDelay = time.Hour
	t.Cleanup(func() {
		messageLogDBProvider = oldProvider
		messageLogRetryDelay = oldDelay
	})

	msgQueueMu.Lock()
	msgQueue = []*loggedMessageRow{{ID: 1}}
	isFlushing = true
	msgQueueMu.Unlock()
	flushMessageLogQueue()

	msgQueueMu.Lock()
	defer msgQueueMu.Unlock()
	if len(msgQueue) != 1 || msgQueue[0].ID != 1 {
		t.Fatalf("queue after unavailable DB = %#v; expected queued message to remain", msgQueue)
	}
	if isFlushing {
		t.Fatal("isFlushing remained true after unavailable DB")
	}
}

func TestFlushMessageLogQueueRequeuesFailureAndRetries(t *testing.T) {
	resetMessageLogQueue(t)
	oldProvider, oldDelay := messageLogDBProvider, messageLogRetryDelay
	messageLogRetryDelay = time.Hour
	state := &queueTestState{failNextExec: true}
	db := openQueueTestDB(t, state)
	messageLogDBProvider = func() *sql.DB { return db }
	t.Cleanup(func() {
		messageLogDBProvider = oldProvider
		messageLogRetryDelay = oldDelay
	})

	msgQueueMu.Lock()
	msgQueue = []*loggedMessageRow{{ID: 2}}
	isFlushing = true
	msgQueueMu.Unlock()
	flushMessageLogQueue()

	msgQueueMu.Lock()
	retained := len(msgQueue) == 1 && msgQueue[0].ID == 2
	isFlushing = true
	msgQueueMu.Unlock()
	if !retained {
		t.Fatal("transaction failure did not requeue the batch")
	}

	flushMessageLogQueue()
	msgQueueMu.Lock()
	defer msgQueueMu.Unlock()
	if len(msgQueue) != 0 {
		t.Fatalf("queue after successful retry = %#v", msgQueue)
	}
	if isFlushing {
		t.Fatal("isFlushing remained true after successful flush")
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.commits != 1 {
		t.Fatalf("commits = %d; want 1 after retry", state.commits)
	}
}

```

## `./notifier_movies.go`

```go
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
)

type tmdbNowPlayingResponse struct {
	Results []struct {
		ID          int     `json:"id"`
		Title       string  `json:"title"`
		Overview    string  `json:"overview"`
		ReleaseDate string  `json:"release_date"`
		VoteAverage float64 `json:"vote_average"`
		PosterPath  string  `json:"poster_path"`
	} `json:"results"`
}

var (
	moviesMu        sync.Mutex
	moviesCache     map[string]map[string][]string
	moviesCachePath string
)

func initMoviesCache() {
	moviesCachePath = dataPath("moviesThisWeekCache.json")
	moviesCache = make(map[string]map[string][]string)

	data, err := os.ReadFile(moviesCachePath)
	if err == nil {
		if err := json.Unmarshal(data, &moviesCache); err != nil {
			log.Printf("TMDB notifier: parse cache %s: %v", moviesCachePath, err)
			moviesCache = make(map[string]map[string][]string)
		}
	} else if !os.IsNotExist(err) {
		log.Printf("TMDB notifier: read cache %s: %v", moviesCachePath, err)
	}
	if moviesCache == nil {
		moviesCache = make(map[string]map[string][]string)
	}
}

func saveMoviesCache() error {
	if moviesCachePath == "" {
		return fmt.Errorf("movies cache path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(moviesCachePath), 0755); err != nil {
		return fmt.Errorf("creating movies cache directory: %w", err)
	}
	data, err := json.MarshalIndent(moviesCache, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding movies cache: %w", err)
	}
	if err := os.WriteFile(moviesCachePath, data, 0644); err != nil {
		return fmt.Errorf("writing movies cache %s: %w", moviesCachePath, err)
	}
	return nil
}

func checkMoviesUpdates(s *discordgo.Session, cfg *Config) {
	if cfg.TMDBAPIKey == "" {
		return
	}

	guilds := loadGuildSettings()
	moviesMu.Lock()
	defer moviesMu.Unlock()

	client := notifierClient(15 * time.Second)
	query := url.Values{
		"api_key":  {cfg.TMDBAPIKey},
		"language": {"en-US"},
		"page":     {"1"},
	}
	apiURL := "https://api.themoviedb.org/3/movie/now_playing?" + query.Encode()

	resp, err := client.Get(apiURL)
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		log.Printf("TMDB notifier: fetch now-playing request: %v", err)
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("TMDB notifier: read now-playing response: %v", err)
		return
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Printf("TMDB notifier: now-playing request returned HTTP %d", resp.StatusCode)
		return
	}
	var tmdbResp tmdbNowPlayingResponse
	if err := json.Unmarshal(body, &tmdbResp); err != nil {
		log.Printf("TMDB notifier: decode now-playing response: %v", err)
		return
	}

	for guildID, gCfg := range guilds {
		if gCfg.MoviesThisWeekChannelID == "" {
			continue
		}

		if moviesCache[guildID] == nil {
			moviesCache[guildID] = make(map[string][]string)
		}
		seenList := moviesCache[guildID]["seenMovieIds"]
		seenMap := make(map[string]bool)
		for _, id := range seenList {
			seenMap[id] = true
		}

		var newEmbeds []*discordgo.MessageEmbed
		var newMovieIDs []string
		for _, m := range tmdbResp.Results {
			idStr := fmt.Sprintf("%d", m.ID)
			if m.ID == 0 || seenMap[idStr] {
				continue
			}

			embed := &discordgo.MessageEmbed{
				Title:       fmt.Sprintf("🍿 %s (%s)", m.Title, m.ReleaseDate),
				Description: m.Overview,
				Color:       0xE50914,
				Fields: []*discordgo.MessageEmbedField{
					{Name: "Rating", Value: fmt.Sprintf("⭐ %.1f / 10", m.VoteAverage), Inline: true},
				},
			}
			if m.PosterPath != "" {
				embed.Thumbnail = &discordgo.MessageEmbedThumbnail{
					URL: fmt.Sprintf("https://image.tmdb.org/t/p/w500%s", m.PosterPath),
				}
			}
			newEmbeds = append(newEmbeds, embed)
			newMovieIDs = append(newMovieIDs, idStr)
			if len(newEmbeds) >= 5 {
				break
			}
		}

		if len(newEmbeds) > 0 {
			header := "**🎬 In Theaters This Week:**"
			if gCfg.MoviesThisWeekRoleID != nil && *gCfg.MoviesThisWeekRoleID != "" {
				header = fmt.Sprintf("<@&%s> %s", *gCfg.MoviesThisWeekRoleID, header)
			}
			if _, err := sendNotifierComplex(s, gCfg.MoviesThisWeekChannelID, &discordgo.MessageSend{
				Content: header,
				Embeds:  newEmbeds,
			}); err != nil {
				log.Printf("TMDB notifier: send movie announcements to guild %s: %v", guildID, err)
				continue
			}

			seenList = append(seenList, newMovieIDs...)
			moviesCache[guildID]["seenMovieIds"] = seenList
			if err := saveMoviesCache(); err != nil {
				moviesCache[guildID]["seenMovieIds"] = append([]string(nil), seenList[:len(seenList)-len(newMovieIDs)]...)
				log.Printf("TMDB notifier: persist delivered movies for guild %s: %v", guildID, err)
			}
		}
	}
}

// startMoviesNotifierDaemon runs daily TMDB theatrical/streaming movie checks
func startMoviesNotifierDaemon(ctx context.Context, s *discordgo.Session) {
	initMoviesCache()
	cfg, err := loadConfig()
	if err != nil {
		log.Printf("TMDB notifier: load config: %v", err)
		return
	}
	if cfg.TMDBAPIKey == "" {
		log.Printf("TMDB notifier not started: TMDB API key is not configured")
		return
	}

	log.Printf("TMDB movies notifier daemon started (interval 24h)")
	runPeriodic(ctx, 45*time.Second, 24*time.Hour, func() { checkMoviesUpdates(s, cfg) })
}

```

## `./media_logger_download_test.go`

```go
package main

import (
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"
)

type mediaReadErrorBody struct{}

func (mediaReadErrorBody) Read([]byte) (int, error) { return 0, errors.New("injected body read error") }
func (mediaReadErrorBody) Close() error             { return nil }

func TestDownloadAndSaveMediaReportsHTTPAndBodyFailures(t *testing.T) {
	tests := []struct {
		name      string
		transport func(*http.Request) (*http.Response, error)
		wantError bool
	}{
		{
			name: "http status",
			transport: func(req *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusServiceUnavailable, Status: "503 Service Unavailable", Body: io.NopCloser(strings.NewReader("down")), Request: req}, nil
			},
			wantError: true,
		},
		{
			name: "body read",
			transport: func(req *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Body: mediaReadErrorBody{}, Request: req}, nil
			},
			wantError: true,
		},
		{
			name: "transport",
			transport: func(*http.Request) (*http.Response, error) {
				return nil, errors.New("injected transport failure")
			},
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			oldBase, oldClient := mediaLogsBaseDir, mediaHTTPClient
			mediaLogsBaseDir = root
			mediaHTTPClient = &http.Client{Transport: notifierRoundTripFunc(tt.transport)}
			t.Cleanup(func() {
				mediaLogsBaseDir = oldBase
				mediaHTTPClient = oldClient
			})

			err := downloadAndSaveMedia("guild", "message", &discordgo.MessageAttachment{
				ID: "attachment", Filename: "photo.png", URL: "https://media.example/photo.png",
			}, "images")
			if (err != nil) != tt.wantError {
				t.Fatalf("downloadAndSaveMedia error = %v, wantError %t", err, tt.wantError)
			}
			files, readErr := filepath.Glob(filepath.Join(root, "guild", "images", "*"))
			if readErr != nil {
				t.Fatal(readErr)
			}
			if len(files) != 0 {
				t.Fatalf("failed download left partial files: %v", files)
			}
		})
	}
}

```

## `./analytics.go`

```go
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

```

## `./moderation_test.go`

```go
package main

import (
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"
)

func TestBanUserIDParsing(t *testing.T) {
	for input, want := range map[string]string{
		"123456789012345678":     "123456789012345678",
		"<@123456789012345678>":  "123456789012345678",
		"<@!123456789012345678>": "123456789012345678",
		"not-a-user":             "",
		"":                       "",
	} {
		if got := parseBanUserID(input); got != want {
			t.Errorf("parseBanUserID(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestBanPagePaginationControls(t *testing.T) {
	onePage := make([]*discordgo.GuildBan, 16)
	for i := range onePage {
		onePage[i] = &discordgo.GuildBan{
			User: &discordgo.User{ID: "123456789012345678", Username: "one"},
		}
	}
	content, components := banPage(onePage, 0)
	if len(components) != 0 {
		t.Fatalf("one-page ban list has %d component rows, want none", len(components))
	}
	if !strings.Contains(content, "one") {
		t.Fatalf("ban page omitted user: %q", content)
	}

	manyBans := make([]*discordgo.GuildBan, 50)
	for i := range manyBans {
		manyBans[i] = &discordgo.GuildBan{
			User: &discordgo.User{ID: "123456789012345678", Username: strings.Repeat("x", 32)},
		}
	}
	content, components = banPage(manyBans, 0)
	if len(content) > 2000 {
		t.Fatalf("ban page is %d characters, exceeds Discord's message limit", len(content))
	}
	if len(components) != 1 {
		t.Fatalf("multi-page ban list has %d component rows, want one", len(components))
	}
	row, ok := components[0].(discordgo.ActionsRow)
	if !ok || len(row.Components) != 2 {
		t.Fatalf("multi-page ban list should have previous/next buttons, got %#v", components[0])
	}
}

```

## `./venice/handlers2.go`

```go
// handlers2.go — handlers for the expanded ElyVenice command set, using the
// reusable modules library (enit-sunfire-bot/modules).
//
// Adds: /stt, /search, /scrape, /video, /embed, /characters, /upscale.
package venice

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"sunfire/modules"
	"sunfire/modules/audio"
	"sunfire/modules/augment"
	"sunfire/modules/characters"
	"sunfire/modules/embeddings"
	"sunfire/modules/image"
	"sunfire/modules/video"
	"github.com/bwmarrin/discordgo"
)

// ---- helpers -----------------------------------------------------------------

// attachmentBytes downloads the first Discord attachment into memory.
func attachmentBytes(s *discordgo.Session, i *discordgo.InteractionCreate) ([]byte, string, error) {
	att := i.ApplicationCommandData().Resolved
	if att == nil || len(att.Attachments) == 0 {
		return nil, "", fmt.Errorf("no attachment provided")
	}
	var a *discordgo.MessageAttachment
	for _, v := range att.Attachments {
		a = v
		break
	}
	if a == nil || a.URL == "" {
		return nil, "", fmt.Errorf("attachment has no URL")
	}
	req, err := http.NewRequest(http.MethodGet, a.URL, nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := s.Client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, "", fmt.Errorf("downloading attachment: HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 25<<20))
	if err != nil {
		return nil, "", err
	}
	return data, a.Filename, nil
}

// ---- /stt: transcribe an attached audio file ---------------------------------

func handleSTT(s *discordgo.Session, i *discordgo.InteractionCreate, get func(string) string) {
	data, name, err := attachmentBytes(s, i)
	if err != nil {
		respond(s, i, "Please attach an audio file to transcribe. "+err.Error())
		return
	}
	if len(data) == 0 {
		respond(s, i, "The attached file is empty.")
		return
	}
	respondDefer(s, i)

	model := get("model")
	if model == "" {
		model = "openai/whisper-large-v3"
	}
	req := &audio.TranscriptionRequest{
		Model:          model,
		File:           venice.MultipartFile{Name: name, Data: data},
		ResponseFormat: "json",
	}
	if lang := get("language"); lang != "" {
		req.Language = lang
	}
	out, err := mc.Audio.Transcribe(bg, req)
	if err != nil {
		editResponse(s, i, "Transcription error: "+err.Error())
		return
	}
	text := strings.TrimSpace(out.Text)
	if text == "" {
		editResponse(s, i, "(no speech detected)")
		return
	}
	editResponse(s, i, fmt.Sprintf("📝 **Transcription** (`%s`)\n%s", model, text))
}

// ---- /search: web search ------------------------------------------------------

func handleSearch(s *discordgo.Session, i *discordgo.InteractionCreate, get func(string) string) {
	query := strings.TrimSpace(get("query"))
	if query == "" {
		respond(s, i, "Please provide a search query.")
		return
	}
	respondDefer(s, i)

	req := &augment.SearchRequest{Query: query}
	if n := get("max_results"); n != "" {
		var v int
		fmt.Sscanf(n, "%d", &v)
		if v > 0 {
			req.MaxResults = &v
		}
	}
	if p := get("provider"); p != "" {
		req.Provider = p
	}
	res, err := mc.Augment.Search(bg, req)
	if err != nil {
		editResponse(s, i, "Search error: "+err.Error())
		return
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("🔎 **Web search** — `%s`\n\n", query))
	if len(res.Results) == 0 {
		sb.WriteString("No results.")
	} else {
		max := len(res.Results)
		if max > 8 {
			max = 8
		}
		for _, r := range res.Results[:max] {
			sb.WriteString(fmt.Sprintf("**%d.** [%s](%s)\n", r.Index+1, truncateForDiscord(r.Title), r.URL))
			if r.Description != "" {
				sb.WriteString(truncateForDiscord(r.Description) + "\n")
			} else if r.Snippet != "" {
				sb.WriteString(truncateForDiscord(r.Snippet) + "\n")
			}
			sb.WriteString("\n")
		}
	}
	editResponse(s, i, truncateForDiscord(sb.String()))
}

// ---- /scrape: scrape a URL to markdown ----------------------------------------

func handleScrape(s *discordgo.Session, i *discordgo.InteractionCreate, get func(string) string) {
	target := strings.TrimSpace(get("url"))
	if target == "" {
		respond(s, i, "Please provide a URL to scrape.")
		return
	}
	if !strings.HasPrefix(target, "http") {
		target = "https://" + target
	}
	respondDefer(s, i)

	text, err := mc.Augment.Scrape(bg, target)
	if err != nil {
		editResponse(s, i, "Scrape error: "+err.Error())
		return
	}
	if strings.TrimSpace(text) == "" {
		editResponse(s, i, "(page returned no extractable text)")
		return
	}
	editResponse(s, i, fmt.Sprintf("📄 **Scraped** %s\n\n%s", target, truncateForDiscord(text)))
}

// ---- /video: generate a video (async) ------------------------------------------

// videoResolutions is the set the API accepts for resolution.
var videoResolutions = map[string]bool{"1080p": true, "720p": true, "480p": true}

// videoAspectRatios is the set the API accepts for aspect_ratio.
var videoAspectRatios = map[string]bool{"21:9": true, "16:9": true, "4:3": true, "1:1": true, "3:4": true, "9:16": true}

// normalizeVideoParam clamps a user-supplied value to a known set; returns ""
// when empty/invalid so a default applies.
func normalizeVideoParam(v string, valid map[string]bool) string {
	v = strings.ToLower(strings.TrimSpace(v))
	if valid[v] {
		return v
	}
	return ""
}

func handleVideo(s *discordgo.Session, i *discordgo.InteractionCreate, get func(string) string) {
	prompt := strings.TrimSpace(get("prompt"))
	if prompt == "" {
		respond(s, i, "Please provide a video prompt.")
		return
	}
	model := get("model")
	if model == "" {
		model = "seedance-2-5-text-to-video-basic" // live catalog (verified 2026-09-21)
	}
	// aspect_ratio is REQUIRED by the API; default to 16:9 landscape.
	aspect := normalizeVideoParam(get("aspect_ratio"), videoAspectRatios)
	if aspect == "" {
		aspect = "16:9"
	}
	// resolution must be one of 1080p|720p|480p.
	resolution := normalizeVideoParam(get("resolution"), videoResolutions)
	if resolution == "" {
		resolution = "1080p"
	}

	// Strict Duration Sanitizer: Pull only the digits from the input
	durationRaw := strings.TrimSpace(get("duration"))
	var digits []rune
	for _, r := range durationRaw {
		if r >= '0' && r <= '9' {
			digits = append(digits, r)
		}
	}
	duration := "5s" // Default fallback
	if len(digits) > 0 {
		duration = string(digits) + "s" // The API strictly expects strings like "5s"
	}

	respondDefer(s, i)

	req := &video.QueueRequest{
		Model:       model,
		Prompt:      prompt,
		Duration:    duration,
		Resolution:  resolution,
		AspectRatio: aspect,
	}
	editResponse(s, i, fmt.Sprintf("🎬 Queued video generation (`%s`, %s, %s, %s). This takes a minute or more — I'll post it when ready.", model, aspect, resolution, duration))

	go func() {
		data, ct, err := mc.Video.GenerateVideo(bg, req, 5*time.Second)
		if err != nil {
			editResponse(s, i, "Video generation failed: "+err.Error())
			return
		}
		ext := extensionForContentType(ct)
		_, err = s.ChannelMessageSendComplex(i.ChannelID, &discordgo.MessageSend{
			Content: fmt.Sprintf("🎬 **Video** — `%s`", model),
			Files:   []*discordgo.File{{Name: fmt.Sprintf("venice_video_%s.%s", i.ID, ext), Reader: bytes.NewReader(data)}},
		})
		if err != nil {
			editResponse(s, i, "Error posting video: "+err.Error())
		}
	}()
}

// ---- /embed: generate embeddings ----------------------------------------------

func handleEmbed(s *discordgo.Session, i *discordgo.InteractionCreate, get func(string) string) {
	input := strings.TrimSpace(get("input"))
	if input == "" {
		respond(s, i, "Please provide text to embed.")
		return
	}
	model := get("model")
	if model == "" {
		model = "text-embedding-bge-m3"
	}
	respondDefer(s, i)

	out, err := mc.Embeddings.Generate(bg, &embeddings.Request{Model: model, Input: []string{input}})
	if err != nil {
		editResponse(s, i, "Embedding error: "+err.Error())
		return
	}
	if len(out.Data) == 0 {
		editResponse(s, i, "(no embedding data returned)")
		return
	}
	vec := out.Data[0].Embedding
	n := len(vec)
	if n > 8 {
		n = 8
	}
	var dims []string
	for _, v := range vec[:n] {
		dims = append(dims, fmt.Sprintf("%.4f", v))
	}
	msg := fmt.Sprintf("🧠 **Embedding** (`%s`)\nDimensions: **%d**\nPreview: `[%s]`\nTokens: %d",
		model, len(vec), strings.Join(dims, ", "), out.Usage.PromptTokens)
	editResponse(s, i, msg)
}

// ---- /characters: list/search Venice characters ----------------------------------

func handleCharacters(s *discordgo.Session, i *discordgo.InteractionCreate, get func(string) string) {
	search := strings.TrimSpace(get("search"))
	respondDefer(s, i)

	opts := &characters.ListOptions{PageSize: 10}
	if search != "" {
		opts.Search = search
	}
	res, err := mc.Characters.List(bg, opts)
	if err != nil {
		editResponse(s, i, "Characters error: "+err.Error())
		return
	}
	var sb strings.Builder
	if search != "" {
		sb.WriteString(fmt.Sprintf("🎭 **Characters** — search `%s`\n\n", search))
	} else {
		sb.WriteString("🎭 **Characters**\n\n")
	}
	if len(res.Data) == 0 {
		sb.WriteString("No characters found.")
	} else {
		for _, c := range res.Data {
			web := ""
			if c.WebEnabled {
				web = " 🌐"
			}
			sb.WriteString(fmt.Sprintf("**%s** (`%s`)%s\n", c.Name, c.Slug, web))
			if c.Description != "" {
				sb.WriteString(truncateForDiscord(c.Description) + "\n")
			}
			sb.WriteString("\n")
		}
	}
	editResponse(s, i, truncateForDiscord(sb.String()))
}

// ---- /upscale: upscale an attached image -----------------------------------------

func handleUpscale(s *discordgo.Session, i *discordgo.InteractionCreate, get func(string) string) {
	data, name, err := attachmentBytes(s, i)
	if err != nil {
		respond(s, i, "Please attach an image to upscale. "+err.Error())
		return
	}
	if len(data) == 0 {
		respond(s, i, "The attached file is empty.")
		return
	}
	model := get("model")
	if model == "" {
		model = "upscale-model"
	}
	respondDefer(s, i)

	req := &image.UpscaleRequest{
		Model: model,
		// The API expects RAW base64 (no data: prefix) — verified live
		// 2026-09-21: a data-URL causes 400 "image appears to be incomplete
		// or corrupted". This matches modules' LoadImageArg output.
		Image: base64.StdEncoding.EncodeToString(data),
	}
	if scale := get("scale"); scale != "" {
		var v int
		fmt.Sscanf(scale, "%d", &v)
		if v >= 2 && v <= 8 {
			req.Scale = &v
		}
	}
	up, err := mc.Image.Upscale(bg, req)
	if err != nil {
		editResponse(s, i, "Upscale error: "+err.Error())
		return
	}
	ext := detectImageExt(up)
	_, err = s.ChannelMessageSendComplex(i.ChannelID, &discordgo.MessageSend{
		Content: fmt.Sprintf("🔍 **Upscaled** — `%s` (%d → %d bytes)", name, len(data), len(up)),
		Files:   []*discordgo.File{{Name: fmt.Sprintf("venice_upscaled_%s.%s", i.ID, ext), Reader: bytes.NewReader(up)}},
	})
	if err != nil {
		editResponse(s, i, "Error posting upscaled image: "+err.Error())
		return
	}
	editResponse(s, i, "Image upscaled ✅")
}

// ---- small helpers ----------------------------------------------------------------

func extensionForContentType(ct string) string {
	switch {
	case strings.Contains(ct, "mp4"), strings.Contains(ct, "mpeg"), strings.Contains(ct, "quicktime"):
		return "mp4"
	case strings.Contains(ct, "webm"):
		return "webm"
	case strings.Contains(ct, "gif"):
		return "gif"
	case strings.Contains(ct, "png"):
		return "png"
	case strings.Contains(ct, "jpeg"):
		return "jpg"
	default:
		return "mp4"
	}
}

```

## `./venice/venice_sub_test.go`

```go
package venice

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"
)

func TestTTSFormatForModel(t *testing.T) {
	cases := []struct {
		model string
		want  string
	}{
		{"tts-chatterbox-hd", "wav"},
		{"tts-minimax-speech-02-hd", "wav"},
		{"tts-kokoro", "mp3"},
		{"", "mp3"},
	}
	for _, c := range cases {
		if got := ttsFormatForModel(c.model); got != c.want {
			t.Errorf("ttsFormatForModel(%q) = %q, want %q", c.model, got, c.want)
		}
	}
}

func TestMusicQueueMessageShowsDurationAndQuotedCost(t *testing.T) {
	got := musicQueueMessage("minimax-music-v26", 240, 0.18)
	if !strings.Contains(got, "4m") || !strings.Contains(got, "$0.1800") {
		t.Fatalf("musicQueueMessage() = %q, want duration and cost", got)
	}
}

func TestMusicCommandRegistersDurationBounds(t *testing.T) {
	for _, spec := range CommandSpecs() {
		if spec.Command.Name != "venice" {
			continue
		}
		for _, subcommand := range spec.Command.Options {
			if subcommand.Name != "music" {
				continue
			}
			for _, option := range subcommand.Options {
				if option.Name != "duration" {
					continue
				}
				if option.Type != discordgo.ApplicationCommandOptionInteger ||
					option.MinValue == nil || *option.MinValue != minMusicDurationSeconds ||
					option.MaxValue != maxMusicDurationSeconds || option.Required {
					t.Fatalf("duration option has unexpected constraints: %#v", option)
				}
				return
			}
		}
	}
	t.Fatal("music duration option was not registered")
}

func TestFormatDuration(t *testing.T) {
	if got := formatDuration(45); got != "45s" {
		t.Errorf("formatDuration(45) = %q, want 45s", got)
	}
	if got := formatDuration(105); got != "1m 45s" {
		t.Errorf("formatDuration(105) = %q, want 1m 45s", got)
	}
	if got := formatDuration(120); got != "2m" {
		t.Errorf("formatDuration(120) = %q, want 2m", got)
	}
}

func TestQuoteAudioAndQueueAudioSendRecommendedRequestFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("Authorization = %q, want bearer token", r.Header.Get("Authorization"))
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("reading request body: %v", err)
			return
		}
		var payload map[string]json.RawMessage
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Errorf("decoding request body: %v", err)
			return
		}

		switch r.URL.Path {
		case "/audio/quote":
			if got := string(payload["model"]); got != `"minimax-music-v26"` {
				t.Errorf("quote model = %s", got)
			}
			if got := string(payload["duration_seconds"]); got != "240" {
				t.Errorf("quote duration_seconds = %s, want 240", got)
			}
			_, _ = io.WriteString(w, `{"quote":0.18}`)
		case "/audio/queue":
			for key, want := range map[string]string{
				"model":            `"minimax-music-v26"`,
				"prompt":           `"dark synth pop"`,
				"lyrics_prompt":    `"[Verse]\nHello\n[Chorus]\nAgain"`,
				"duration_seconds": "240",
			} {
				if got := string(payload[key]); got != want {
					t.Errorf("queue %s = %s, want %s", key, got, want)
				}
			}
			if _, ok := payload["force_instrumental"]; ok {
				t.Error("force_instrumental should be omitted for vocal songs")
			}
			_, _ = io.WriteString(w, `{"queue_id":"queue-123"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := &veniceClient{BaseURL: server.URL, APIKey: "test-key", HTTP: server.Client()}
	quote, err := client.QuoteAudio("minimax-music-v26", 240)
	if err != nil {
		t.Fatalf("QuoteAudio() error = %v", err)
	}
	if quote != 0.18 {
		t.Fatalf("QuoteAudio() = %v, want 0.18", quote)
	}

	queueID, err := client.QueueAudio("minimax-music-v26", "dark synth pop", "[Verse]\nHello\n[Chorus]\nAgain", false, 240)
	if err != nil {
		t.Fatalf("QueueAudio() error = %v", err)
	}
	if queueID != "queue-123" {
		t.Fatalf("QueueAudio() = %q, want queue-123", queueID)
	}
}

func TestWaitAudioRetrievesAudioAndCompletesQueue(t *testing.T) {
	completed := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/audio/retrieve":
			w.Header().Set("Content-Type", "audio/mpeg")
			_, _ = io.WriteString(w, "audio-data")
		case "/audio/complete":
			completed = true
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := &veniceClient{BaseURL: server.URL, APIKey: "test-key", HTTP: server.Client()}
	data, ext, err := client.WaitAudio("minimax-music-v26", "queue-123", 1, 1)
	if err != nil {
		t.Fatalf("WaitAudio() error = %v", err)
	}
	if string(data) != "audio-data" || ext != "mp3" {
		t.Fatalf("WaitAudio() = (%q, %q), want audio bytes and mp3", data, ext)
	}
	if !completed {
		t.Fatal("WaitAudio() did not call /audio/complete")
	}
}

```

## `./venice/config.go`

```go
// config.go — Venice subpackage config: reads the shared repo .private/config.json.
package venice

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
)

// Config holds the Venice-relevant settings from the shared config file.
type Config struct {
	VeniceAPIKey      string `json:"veniceApiKey"`
	VeniceBaseURL     string `json:"veniceBaseUrl"`
	DefaultChatModel  string `json:"defaultChatModel"`
	DefaultImageModel string `json:"defaultImageModel"`
	DefaultTTSModel   string `json:"defaultTtsModel"`
	DefaultTTSVoice   string `json:"defaultTtsVoice"`
	DefaultMusicModel string `json:"defaultMusicModel"`
}

// cfg is the package-level config, initialized by Init.
var cfg *Config

// loadConfig reads the repo-root .private/config.json (relative to this file).
func loadConfig() (*Config, error) {
	_, file, _, ok := runtime.Caller(0)
	path := ""
	if ok {
		path = filepath.Join(filepath.Dir(file), "..", ".private", "config.json")
	} else {
		path = filepath.Join("..", ".private", "config.json")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var raw struct {
		VeniceAPIKey      string `json:"veniceApiKey"`
		VeniceBaseURL     string `json:"veniceBaseUrl"`
		DefaultChatModel  string `json:"defaultChatModel"`
		DefaultImageModel string `json:"defaultImageModel"`
		DefaultTTSModel   string `json:"defaultTtsModel"`
		DefaultTTSVoice   string `json:"defaultTtsVoice"`
		DefaultMusicModel string `json:"defaultMusicModel"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	// NOTE: we do NOT read the shared "defaultImageModel" config key, because
	// that key now holds the A2E default (seedream) which Venice doesn't
	// support. Venice's default image model is always flux-2-pro unless the
	// user passes model: explicitly.
	c := &Config{
		VeniceAPIKey:      raw.VeniceAPIKey,
		VeniceBaseURL:     raw.VeniceBaseURL,
		DefaultChatModel:  raw.DefaultChatModel,
		DefaultImageModel: "flux-2-pro",
		DefaultTTSModel:   raw.DefaultTTSModel,
		DefaultTTSVoice:   raw.DefaultTTSVoice,
		DefaultMusicModel: raw.DefaultMusicModel,
	}
	if c.VeniceBaseURL == "" {
		c.VeniceBaseURL = "https://api.venice.ai/api/v1"
	}
	if c.DefaultChatModel == "" {
		c.DefaultChatModel = "gemini-3-6-flash"
	}
	if c.DefaultTTSModel == "" {
		c.DefaultTTSModel = "tts-kokoro"
	}
	if c.DefaultTTSVoice == "" {
		c.DefaultTTSVoice = "af_bella"
	}
	if c.DefaultMusicModel == "" {
		c.DefaultMusicModel = "minimax-music-v26"
	}
	return c, nil
}

```

## `./venice/helpers.go`

```go
// helpers.go — Venice subpackage: init, response helpers, modules client.
package venice

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"sunfire/modules"
	"sunfire/modules/audio"
	"sunfire/modules/augment"
	"sunfire/modules/characters"
	"sunfire/modules/embeddings"
	"sunfire/modules/image"
	"sunfire/modules/video"
	"github.com/bwmarrin/discordgo"
)

// vc is the package-level inline Venice client.
var vc *veniceClient

// mc bundles the modules-library clients (for stt/search/scrape/video/embed/characters/upscale).
var mc *moduleClients

// bg is a shared background context for module calls.
var bg = context.Background()

// moduleClients bundles the modules library clients sharing one root client.
type moduleClients struct {
	Root       *venice.Client
	Audio      *audio.Client
	Augment    *augment.Client
	Characters *characters.Client
	Embeddings *embeddings.Client
	Image      *image.Client
	Video      *video.Client
}

// Init loads config and prepares the package-level clients. Call once at startup.
func Init() error {
	c, err := loadConfig()
	if err != nil {
		return err
	}
	cfg = c
	vc = newVeniceClient(c)

	// Build the modules root client from the same settings.
	base := c.VeniceBaseURL
	if base == "" {
		base = "https://api.venice.ai/api/v1"
	}
	root, err := venice.NewClient(
		venice.WithAPIKey(c.VeniceAPIKey),
		venice.WithBaseURL(base),
	)
	if err != nil {
		logErrorf("modules client init: %v", err)
		return err
	}
	mc = &moduleClients{
		Root:       root,
		Audio:      audio.NewClient(root),
		Augment:    augment.NewClient(root),
		Characters: characters.NewClient(root),
		Embeddings: embeddings.NewClient(root),
		Image:      image.NewClient(root),
		Video:      video.NewClient(root),
	}
	return nil
}

// respond sends an interaction response.
func respond(s *discordgo.Session, i *discordgo.InteractionCreate, content string) {
	content = truncateForDiscord(content)
	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Content: content},
	})
}

// respondDefer acknowledges first (for long operations).
func respondDefer(s *discordgo.Session, i *discordgo.InteractionCreate) {
	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	})
}

// editResponse edits a deferred response and logs errors centrally.
func editResponse(s *discordgo.Session, i *discordgo.InteractionCreate, content string) {
	c := truncateForDiscord(content)
	
	// Centralized Error Logging: If the bot warns the user about an error, persist it to disk
	lower := strings.ToLower(content)
	if strings.Contains(lower, "error") || strings.Contains(lower, "failed") {
		logErrorf("API Error sent to user /%s: %s", i.ApplicationCommandData().Name, content)
	}
	
	_, _ = s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{Content: &c})
}

// truncateForDiscord keeps replies under the Discord 2000-char limit.
func truncateForDiscord(s string) string {
	const max = 1990
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}

// logErrorf logs a real error.
func logErrorf(format string, v ...interface{}) {
	log.Printf("venice: "+format, v...)
}

// downloadHTTPClient for downloading Discord attachment bytes.
var downloadHTTPClient = &http.Client{Timeout: 90 * time.Second}

// downloadURL fetches the bytes at url (used to download a Discord CDN
// attachment for voice cloning).
func downloadURL(url string) ([]byte, error) {
	resp, err := downloadHTTPClient.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, resp.Body)
		return nil, &downloadError{status: resp.StatusCode}
	}
	return io.ReadAll(io.LimitReader(resp.Body, 25<<20))
}

type downloadError struct{ status int }

func (e *downloadError) Error() string {
	if e.status == 0 {
		return "download failed"
	}
	return "download HTTP " + strconv.Itoa(e.status)
}

// getAttachment returns the MessageAttachment referenced by an attachment
// option. The attachment option's Value is the attachment ID; it's resolved
// via the interaction's Resolved.Attachments map. Returns nil if none.
func getAttachment(i *discordgo.InteractionCreate, attachmentID string) *discordgo.MessageAttachment {
	if i == nil || i.Interaction == nil || i.Interaction.Data == nil {
		return nil
	}
	// Data is a value ApplicationCommandInteractionData (not a pointer) for
	// application command interactions.
	app, ok := i.Interaction.Data.(discordgo.ApplicationCommandInteractionData)
	if !ok || app.Resolved == nil {
		return nil
	}
	if attachmentID != "" {
		if a, ok := app.Resolved.Attachments[attachmentID]; ok && a != nil {
			return a
		}
	}
	for _, a := range app.Resolved.Attachments {
		if a != nil {
			return a
		}
	}
	return nil
}

// getOpt reads a command option by name.
func getOpt(opts []*discordgo.ApplicationCommandInteractionDataOption, key string) string {
	for _, o := range opts {
		if o.Name == key {
			return toString(o.Value)
		}
	}
	return ""
}

func toString(v interface{}) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}

```

## `./venice/lyricslog.go`

```go
// lyricslog.go — writes the normalized lyrics that are about to be sent to the
// Venice music API into a temp log file so the user can inspect exactly what the
// model receives. Files go under ~/Repos/TEMP/TempLogs_Delete-after-viewing/.
package venice

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// logDir is the directory where normalized-lyrics logs are written. The user
// reviews them and deletes the folder when done. It's a var (not const) so tests
// can point it at a temp dir.
var logDir = "/home/daniel/Repos/TEMP/TempLogs_Delete-after-viewing"

// safeNameRe strips characters that are invalid or awkward in filenames.
var safeNameRe = regexp.MustCompile(`[^A-Za-z0-9 _\-]`)

// sanitizeFilename turns an arbitrary string (e.g. the music prompt) into a
// filesystem-safe base name, collapsing whitespace and capping length.
func sanitizeFilename(s string) string {
	s = safeNameRe.ReplaceAllString(s, "")
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 60 {
		s = s[:60]
	}
	s = strings.TrimSpace(s)
	if s == "" {
		s = "song"
	}
	return s
}

// logNormalizedLyrics writes the normalized lyrics (the exact text that will be
// sent as lyrics_prompt) to a .md file in the temp log dir, which it creates if
// needed. It returns the file path, or an empty string + error on failure.
func logNormalizedLyrics(prompt, lyrics string) (string, error) {
	if lyrics == "" {
		return "", nil
	}
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return "", err
	}

	base := sanitizeFilename(prompt)
	ts := time.Now().Format("20060102-150405")
	name := fmt.Sprintf("%s_%s.md", base, ts)
	path := filepath.Join(logDir, name)

	content := "# Normalized lyrics (sent to Venice music API)\n\n" +
		"*Generated by ElyVenice /music — review and delete this folder when done.*\n\n" +
		"---\n\n" +
		lyrics + "\n"

	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

```

## `./venice/clonedvoices.go`

```go
package venice

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// clonedVoiceStore persists cloned-voice handles locally, because the Venice
// API has no list endpoint for them (a handle is only returned at creation).
// File lives in Data/cloned-voices.json (gitignored).
var clonedVoiceStorePathDefault = func() string {
	if dir := os.Getenv("SUNFIRE_DATA_DIR"); dir != "" {
		return filepath.Join(dir, "cloned-voices.json")
	}
	return filepath.Join("Data", "cloned-voices.json")
}()

// clonedVoiceStorePath is a var (not const) so tests can override it.
var clonedVoiceStorePath = clonedVoiceStorePathDefault

// clonedVoice is one stored cloned-voice handle.
type clonedVoice struct {
	ID        string `json:"id"`        // vv_... handle
	Model     string `json:"model"`     // tts-chatterbox-hd | tts-minimax-speech-02-hd
	Name      string `json:"name"`      // friendly name chosen by user
	CreatedAt string `json:"createdAt"` // RFC3339
}

type clonedVoiceStore struct {
	Voices []clonedVoice `json:"voices"`
}

func loadClonedVoiceStore() (*clonedVoiceStore, error) {
	s := &clonedVoiceStore{Voices: []clonedVoice{}}
	b, err := os.ReadFile(filepath.Clean(clonedVoiceStorePath))
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, err
	}
	if err := json.Unmarshal(b, s); err != nil {
		return nil, fmt.Errorf("parsing cloned-voices.json: %w", err)
	}
	if s.Voices == nil {
		s.Voices = []clonedVoice{}
	}
	return s, nil
}

func (s *clonedVoiceStore) save() error {
	dir := filepath.Dir(filepath.Clean(clonedVoiceStorePath))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Clean(clonedVoiceStorePath), b, 0o644)
}

// add appends (or replaces by ID) a cloned voice and persists it.
func (s *clonedVoiceStore) add(id, model, name string) error {
	for i := range s.Voices {
		if s.Voices[i].ID == id {
			s.Voices[i].Model = model
			s.Voices[i].Name = name
			s.Voices[i].CreatedAt = time.Now().UTC().Format(time.RFC3339)
			return s.save()
		}
	}
	s.Voices = append(s.Voices, clonedVoice{
		ID: id, Model: model, Name: name,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	})
	return s.save()
}

// remove deletes a handle by ID or by name.
func (s *clonedVoiceStore) remove(idOrName string) bool {
	idx := -1
	for i, v := range s.Voices {
		if v.ID == idOrName || strings.EqualFold(v.Name, idOrName) {
			idx = i
			break
		}
	}
	if idx < 0 {
		return false
	}
	s.Voices = append(s.Voices[:idx], s.Voices[idx+1:]...)
	_ = s.save()
	return true
}

// find returns the handle matching a name or exact id, plus its model.
func (s *clonedVoiceStore) find(idOrName string) (string, string, bool) {
	for _, v := range s.Voices {
		if v.ID == idOrName || strings.EqualFold(v.Name, idOrName) {
			return v.ID, v.Model, true
		}
	}
	return "", "", false
}

// format renders the stored cloned voices.
func (s *clonedVoiceStore) format() string {
	if len(s.Voices) == 0 {
		return "No cloned voices stored yet. Use `/voice-clone name:<name> mp3:<file>` to create one."
	}
	var b strings.Builder
	b.WriteString("🎙️ **Your Venice cloned voices**\n")
	for _, v := range s.Voices {
		name := v.Name
		if name == "" {
			name = "(unnamed)"
		}
		b.WriteString(fmt.Sprintf("• **%s**\n  `%s` · %s\n", name, v.ID, v.Model))
	}
	return b.String()
}

```

## `./venice/lyrics.go`

```go
// lyrics.go — normalization of song-lyric text before it's sent to the Venice
// music API. The repo's song .md files (Tala/Ordo style) embed stage-direction
// and voice-tag headers like:
//
//	[Chorus - **Samael's voice, both registers simultaneously, inhuman**]
//
// Per the official MiniMax documentation, the lyrics input must contain ONLY the
// words to be sung plus clean structural tags; tags are "structural instructions
// rather than lines to be sung", and "lyrics define the words to be sung". Any
// descriptive text placed on a tag line (after the "]") gets treated as lyric
// content and sung/spoken by the model. So this normalizer:
//
//   - converts "[Chorus - **description**]" to a clean "[Chorus]" tag
//
//   - strips ALL descriptions from the lyrics (they belong in the prompt field)
//
//   - keeps only the sung words
//
//     [Chorus - **Samael's voice, both registers simultaneously, inhuman**]
//     ->  [Chorus]
package venice

import (
	"regexp"
	"strings"
)

// supportedTags are the section tags MiniMax Music v2.6 understands.
var supportedTags = map[string]bool{
	"intro": true, "verse": true, "pre-chorus": true, "chorus": true,
	"bridge": true, "outro": true, "hook": true, "solo": true,
	"break": true, "interlude": true, "instrumental": true, "drop": true,
	"build": true, "post-chorus": true,
}

// sectionHeaderRe matches a bracketed section header, optionally with a
// "- description" tail, e.g.:
//
//	[Chorus]                              -> group1="Chorus",     group2=""
//	[Chorus - Samael's voice...]          -> group1="Chorus",     group2=" - Samael's voice..."
//	[Verse 1 - **Male voice...**]         -> group1="Verse 1",    group2=" - **Male voice...**"
var sectionHeaderRe = regexp.MustCompile(`^\s*\[\s*([^\]\-]+?)\s*(\s*-\s*[^\]]*)?\]\s*$`)

// normalizeLyrics turns raw song-file text into clean, model-ready lyrics.
// It is safe to call with already-clean lyrics (returns them essentially as-is).
func normalizeLyrics(raw string) string {
	if raw == "" {
		return ""
	}

	lines := strings.Split(raw, "\n")
	var out []string

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Drop horizontal-rule separators and empty runs between sections.
		if trimmed == "" || trimmed == "---" || trimmed == "***" || trimmed == "___" {
			continue
		}

		// Drop explicit end marker.
		if strings.EqualFold(trimmed, "[End]") || strings.EqualFold(trimmed, "[END]") {
			continue
		}

		// Drop markdown headings (# 09 - Samael, ## Lyrics) — not lyric text.
		if strings.HasPrefix(trimmed, "#") {
			continue
		}

		// Section header: emit ONLY the clean tag ("[Verse]"). Descriptions after
		// the tag are dropped from the lyrics — MiniMax sings them otherwise.
		if m := sectionHeaderRe.FindStringSubmatch(trimmed); m != nil {
			tagWord := normalizeTagWord(m[1])
			if tagWord != "" {
				// Blank line separates the tag from the following lyrics block.
				out = append(out, "", "["+titleCaseTag(tagWord)+"]", "")
			}
			continue
		}

		// Ordinary lyric line: strip markdown emphasis markers but keep the text.
		clean := stripMarkdown(trimmed)
		if clean != "" {
			out = append(out, clean)
		}
	}

	// Join with newlines, then normalize spacing: keep a single blank line between
	// a section tag and its lyrics, but collapse any accidental double blanks and
	// trim leading/trailing blank lines.
	joined := strings.Join(out, "\n")
	joined = collapseBlankLines(joined)
	return strings.Trim(joined, "\n")
}

// collapseBlankLines reduces runs of two or more consecutive blank lines down to
// a single blank line, so tags and lyric blocks stay cleanly separated.
func collapseBlankLines(s string) string {
	lines := strings.Split(s, "\n")
	var out []string
	for i := 0; i < len(lines); i++ {
		out = append(out, lines[i])
		if strings.TrimSpace(lines[i]) == "" {
			// Skip any following blank lines.
			for i+1 < len(lines) && strings.TrimSpace(lines[i+1]) == "" {
				i++
			}
		}
	}
	return strings.Join(out, "\n")
}

// normalizeTagWord maps a raw header word ("Chorus", "Verse 1", "Pre Chorus") to
// a canonical supported tag ("chorus", "verse", "pre-chorus"). Returns "" if the
// word isn't a recognized section (e.g. it was purely a description).
func normalizeTagWord(raw string) string {
	w := strings.ToLower(strings.TrimSpace(raw))
	// Strip trailing section numbers ("verse 1" -> "verse", "chorus 2" -> "chorus").
	w = strings.TrimRight(w, "0123456789 ")
	w = strings.TrimSpace(w)
	// Normalize spacing/hyphens for multi-word tags.
	replace := strings.NewReplacer("-", " ", "_", " ", "  ", " ")
	w = replace.Replace(w)
	w = strings.Join(strings.Fields(w), "-")

	// Accept "prechorus" / "postchorus" forms too.
	switch w {
	case "prechorus":
		w = "pre-chorus"
	case "postchorus":
		w = "post-chorus"
	}

	if supportedTags[w] {
		return w
	}
	return ""
}

// titleCaseTag capitalizes the first letter of each hyphen-separated word of a
// canonical tag so the output reads [Intro], [Pre-Chorus], [Outro] (the form the
// MiniMax models are documented with).
func titleCaseTag(tag string) string {
	parts := strings.Split(tag, "-")
	for i, p := range parts {
		if p == "" {
			continue
		}
		parts[i] = strings.ToUpper(p[:1]) + p[1:]
	}
	return strings.Join(parts, "-")
}

// stripMarkdown removes markdown emphasis ( * _ ` ) from a lyric line, keeping
// the words. Bold `**...**` and italic `*...*` both reduce to plain text.
var markdownRe = regexp.MustCompile(`[\\*_` + "`" + `]`)

func stripMarkdown(line string) string {
	s := markdownRe.ReplaceAllString(line, "")
	return strings.TrimSpace(s)
}

```

## `./venice/venice.go`

```go
package venice

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"strings"
	"time"
)

// veniceClient is a minimal HTTP client for the Venice API (base /api/v1).
type veniceClient struct {
	BaseURL string
	APIKey  string
	HTTP    *http.Client
}

func newVeniceClient(cfg *Config) *veniceClient {
	return &veniceClient{
		BaseURL: strings.TrimSuffix(cfg.VeniceBaseURL, "/"),
		APIKey:  cfg.VeniceAPIKey,
		HTTP:    &http.Client{Timeout: 180 * time.Second},
	}
}

func (v *veniceClient) authHeader() string { return "Bearer " + v.APIKey }

// doJSON sends a JSON request and decodes the response into out.
func (v *veniceClient) doJSON(method, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, v.BaseURL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", v.authHeader())
	req.Header.Set("Content-Type", "application/json")
	resp, err := v.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	bodyBytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("Venice API %s %s -> HTTP %d: %s", method, path, resp.StatusCode, truncate(string(bodyBytes), 500))
	}
	if out != nil && len(bodyBytes) > 0 {
		return json.Unmarshal(bodyBytes, out)
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// --- Chat ---

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model            string         `json:"model"`
	Messages         []chatMessage  `json:"messages"`
	VeniceParameters map[string]any `json:"venice_parameters,omitempty"`
}

type chatChoice struct {
	Message struct {
		Content string `json:"content"`
	} `json:"message"`
}

type chatResponse struct {
	Choices []chatChoice `json:"choices"`
}

// Chat sends a chat completion; returns the assistant text.
func (v *veniceClient) Chat(model, system, user string, web bool) (string, error) {
	messages := []chatMessage{}
	if system != "" {
		messages = append(messages, chatMessage{Role: "system", Content: system})
	}
	messages = append(messages, chatMessage{Role: "user", Content: user})
	req := chatRequest{Model: model, Messages: messages}
	if web {
		req.VeniceParameters = map[string]any{"enable_web_search": "on"}
	}
	var resp chatResponse
	if err := v.doJSON("POST", "/chat/completions", req, &resp); err != nil {
		return "", err
	}
	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("no choices in chat response")
	}
	return strings.TrimSpace(resp.Choices[0].Message.Content), nil
}

// --- Image ---

type imageRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
	Size   string `json:"size,omitempty"`
}

type imageResponse struct {
	ID     string   `json:"id"`
	Images []string `json:"images"`
}

// GenerateImage returns the decoded image bytes + detected extension.
func (v *veniceClient) GenerateImage(model, prompt, size string) ([]byte, string, error) {
	req := imageRequest{Model: model, Prompt: prompt, Size: size}
	var resp imageResponse
	if err := v.doJSON("POST", "/image/generate", req, &resp); err != nil {
		return nil, "", err
	}
	if len(resp.Images) == 0 {
		return nil, "", fmt.Errorf("no image in response")
	}
	b64 := resp.Images[0]
	// Strip possible data URL prefix.
	if i := strings.Index(b64, "base64,"); i >= 0 {
		b64 = b64[i+7:]
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, "", fmt.Errorf("decoding image: %w", err)
	}
	return raw, detectImageExt(raw), nil
}

func detectImageExt(b []byte) string {
	if len(b) >= 4 {
		switch {
		case b[0] == 0x89 && b[1] == 'P' && b[2] == 'N' && b[3] == 'G':
			return "png"
		case b[0] == 0xFF && b[1] == 0xD8:
			return "jpg"
		case b[0] == 'R' && b[1] == 'I' && b[2] == 'F' && b[3] == 'F':
			return "webp"
		}
	}
	return "png"
}

// --- TTS ---

type ttsRequest struct {
	Model          string `json:"model"`
	Input          string `json:"input"`
	Voice          string `json:"voice"`
	ResponseFormat string `json:"response_format"`
}

// TTS generates speech audio and returns the bytes + detected extension.
func (v *veniceClient) TTS(model, text, voice, format string) ([]byte, string, error) {
	req := ttsRequest{Model: model, Input: text, Voice: voice, ResponseFormat: format}
	payload, _ := json.Marshal(req)
	httpReq, err := http.NewRequest("POST", v.BaseURL+"/audio/speech", bytes.NewReader(payload))
	if err != nil {
		return nil, "", err
	}
	httpReq.Header.Set("Authorization", v.authHeader())
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := v.HTTP.Do(httpReq)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 50<<20))
	if err != nil {
		return nil, "", err
	}
	if resp.StatusCode != 200 {
		return nil, "", fmt.Errorf("TTS HTTP %d: %s", resp.StatusCode, truncate(string(b), 500))
	}
	ct := resp.Header.Get("Content-Type")
	ext := "mp3"
	switch {
	case strings.Contains(ct, "wav"):
		ext = "wav"
	case strings.Contains(ct, "flac"):
		ext = "flac"
	case strings.Contains(ct, "ogg"):
		ext = "ogg"
	}
	return b, ext, nil
}

// --- Voice Cloning ---

// clonedVoiceResponse is the response from POST /audio/voices.
type clonedVoiceResponse struct {
	ID    string `json:"id"`
	Model string `json:"model"`
}

// CloneVoice uploads an audio sample to clone a voice and returns the
// `vv_...` handle. model must be tts-chatterbox-hd (default) or
// tts-minimax-speech-02-hd. The handle is passed as `voice` to TTS with the
// same model.
func (v *veniceClient) CloneVoice(model string, sample []byte, filename string) (*clonedVoiceResponse, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)

	// model field
	if err := mw.WriteField("model", model); err != nil {
		return nil, err
	}
	// file field
	fw, err := mw.CreateFormFile("file", filename)
	if err != nil {
		return nil, err
	}
	if _, err := fw.Write(sample); err != nil {
		return nil, err
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequest("POST", v.BaseURL+"/audio/voices", &buf)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Authorization", v.authHeader())
	httpReq.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := v.HTTP.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("voice clone HTTP %d: %s", resp.StatusCode, truncate(string(b), 500))
	}
	var out clonedVoiceResponse
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("parsing clone response: %w", err)
	}
	if out.ID == "" {
		return nil, fmt.Errorf("clone response missing id: %s", truncate(string(b), 200))
	}
	return &out, nil
}

// ttsFormatForModel returns the response_format supported by a TTS model.
// tts-chatterbox-hd and tts-minimax-speech-02-hd support only WAV; other TTS
// models (e.g. tts-kokoro) accept mp3.
func ttsFormatForModel(model string) string {
	switch model {
	case "tts-chatterbox-hd", "tts-minimax-speech-02-hd":
		return "wav"
	default:
		return "mp3"
	}
}

// --- STT ---

// Transcribe sends an audio file to /audio/transcriptions (multipart).
func (v *veniceClient) Transcribe(model, filePath, format string) (string, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("model", model)
	mw.WriteField("response_format", format)
	fw, err := mw.CreateFormFile("file", filePath)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(fw, f); err != nil {
		return "", err
	}
	mw.Close()

	httpReq, err := http.NewRequest("POST", v.BaseURL+"/audio/transcriptions", &buf)
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Authorization", v.authHeader())
	httpReq.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := v.HTTP.Do(httpReq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("STT HTTP %d: %s", resp.StatusCode, truncate(string(b), 500))
	}
	// If response_format is text, return raw; otherwise try to extract text.
	if format == "text" {
		return strings.TrimSpace(string(b)), nil
	}
	var parsed map[string]any
	if err := json.Unmarshal(b, &parsed); err == nil {
		if t, ok := parsed["text"].(string); ok {
			return strings.TrimSpace(t), nil
		}
	}
	return strings.TrimSpace(string(b)), nil
}

// --- Balance ---

type balanceResponse struct {
	Balances struct {
		USD  *float64 `json:"usd"`
		DIEM *float64 `json:"diem"`
	} `json:"balances"`
	CanConsume          bool   `json:"canConsume"`
	ConsumptionCurrency string `json:"consumptionCurrency"`
}

// Balance returns a formatted balance string.
func (v *veniceClient) Balance() (string, error) {
	var resp balanceResponse
	if err := v.doJSON("GET", "/billing/balance", nil, &resp); err != nil {
		return "", err
	}
	var sb strings.Builder
	if resp.Balances.USD != nil {
		fmt.Fprintf(&sb, "USD: $%.2f\n", *resp.Balances.USD)
	}
	if resp.Balances.DIEM != nil {
		fmt.Fprintf(&sb, "DIEM: %.4f\n", *resp.Balances.DIEM)
	}
	sb.WriteString("canConsume: ")
	sb.WriteString(fmt.Sprintf("%v", resp.CanConsume))
	return strings.TrimSpace(sb.String()), nil
}

// --- Models ---

type modelInfo struct {
	ID        string `json:"id"`
	ModelSpec struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	} `json:"model_spec"`
}

// Models returns model IDs for a modality (text|image|video|music|embedding|asr).
func (v *veniceClient) Models(modality string) ([]modelInfo, error) {
	path := "/models"
	if modality != "" {
		path += "?type=" + modality
	}
	var resp struct {
		Data []modelInfo `json:"data"`
	}
	if err := v.doJSON("GET", path, nil, &resp); err != nil {
		return nil, err
	}
	return resp.Data, nil
}

// --- Async audio (music/SFX) ---

type audioQueueRequest struct {
	Model        string `json:"model"`
	Prompt       string `json:"prompt"`
	Lyrics       string `json:"lyrics_prompt,omitempty"`
	DurationSec  *int   `json:"duration_seconds,omitempty"`
	Instrumental bool   `json:"force_instrumental,omitempty"`
}

type audioQueueResponse struct {
	Model   string `json:"model"`
	QueueID string `json:"queue_id"`
	Status  string `json:"status"`
}

type audioQuoteRequest struct {
	Model       string `json:"model"`
	DurationSec int    `json:"duration_seconds"`
}

type audioQuoteResponse struct {
	Quote float64 `json:"quote"`
}

// QuoteAudio asks Venice for the estimated cost of a music generation.
func (v *veniceClient) QuoteAudio(model string, duration int) (float64, error) {
	var resp audioQuoteResponse
	if err := v.doJSON("POST", "/audio/quote", audioQuoteRequest{
		Model: model, DurationSec: duration,
	}, &resp); err != nil {
		return 0, err
	}
	return resp.Quote, nil
}

// QueueAudio starts an async music/audio generation job; returns the queue_id.
func (v *veniceClient) QueueAudio(model, prompt, lyrics string, instrumental bool, duration int) (string, error) {
	req := audioQueueRequest{Model: model, Prompt: prompt, Lyrics: lyrics, Instrumental: instrumental}
	if duration > 0 {
		req.DurationSec = &duration
	}
	var resp audioQueueResponse
	if err := v.doJSON("POST", "/audio/queue", req, &resp); err != nil {
		return "", err
	}
	if resp.QueueID == "" {
		return "", fmt.Errorf("no queue_id in audio/queue response")
	}
	return resp.QueueID, nil
}

type audioRetrieveRequest struct {
	Model                   string `json:"model"`
	QueueID                 string `json:"queue_id"`
	DeleteMediaOnCompletion bool   `json:"delete_media_on_completion,omitempty"`
}

// WaitAudio polls /audio/retrieve until the clip is ready (binary body), then
// calls /audio/complete to clean up. Returns audio bytes + extension.
func (v *veniceClient) WaitAudio(model, queueID string, pollSec, timeoutMin int) ([]byte, string, error) {
	deadline := time.Now().Add(time.Duration(timeoutMin) * time.Minute)
	for time.Now().Before(deadline) {
		req := audioRetrieveRequest{Model: model, QueueID: queueID, DeleteMediaOnCompletion: false}
		payload, _ := json.Marshal(req)
		httpReq, err := http.NewRequest("POST", v.BaseURL+"/audio/retrieve", bytes.NewReader(payload))
		if err != nil {
			return nil, "", err
		}
		httpReq.Header.Set("Authorization", v.authHeader())
		httpReq.Header.Set("Content-Type", "application/json")
		resp, err := v.HTTP.Do(httpReq)
		if err != nil {
			return nil, "", err
		}
		ct := resp.Header.Get("Content-Type")
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 60<<20))
		closeErr := resp.Body.Close()
		if readErr != nil {
			return nil, "", fmt.Errorf("reading audio retrieve response: %w", readErr)
		}
		if closeErr != nil {
			log.Printf("audio retrieve response close failed: %v", closeErr)
		}
		if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
			return nil, "", fmt.Errorf("Venice audio retrieve returned HTTP %d: %s", resp.StatusCode, truncate(string(body), 500))
		}

		// Still processing → JSON status body.
		if strings.Contains(ct, "application/json") {
			var st struct {
				Status string `json:"status"`
			}
			json.Unmarshal(body, &st)
			log.Printf("audio poll: status=%s queue=%s", st.Status, queueID)
			time.Sleep(time.Duration(pollSec) * time.Second)
			continue
		}
		if !strings.HasPrefix(strings.ToLower(ct), "audio/") {
			return nil, "", fmt.Errorf("unexpected audio retrieve content type %q: %s", ct, truncate(string(body), 500))
		}

		ext := "mp3"
		switch {
		case strings.Contains(ct, "wav"):
			ext = "wav"
		case strings.Contains(ct, "flac"):
			ext = "flac"
		case strings.Contains(ct, "ogg"):
			ext = "ogg"
		}
		// Clean up Venice's copy after receiving the audio.
		complete := audioRetrieveRequest{Model: model, QueueID: queueID}
		if err := v.doJSON("POST", "/audio/complete", complete, nil); err != nil {
			log.Printf("audio completion cleanup failed for queue %s: %v", queueID, err)
		}
		return body, ext, nil
	}
	return nil, "", fmt.Errorf("timed out waiting for audio generation")
}

```

## `./venice/handlers.go`

```go
package venice

import (
	"bytes"
	"fmt"
	"log"
	"strconv"
	"strings"

	"github.com/bwmarrin/discordgo"
)

// handleChat: /chat [prompt] [model] [web]
func handleChat(s *discordgo.Session, i *discordgo.InteractionCreate, get func(string) string) {
	prompt := get("prompt")
	if prompt == "" {
		respond(s, i, "Please provide a prompt.")
		return
	}
	model := get("model")
	if model == "" {
		model = cfg.DefaultChatModel
	}
	web := get("web") == "true"

	respondDefer(s, i)

	reply, err := vc.Chat(model, "", prompt, web)
	if err != nil {
		editResponse(s, i, "Error: "+err.Error())
		return
	}
	if reply == "" {
		editResponse(s, i, "(empty reply)")
		return
	}
	editResponse(s, i, reply)
}

// handleImage: /image [prompt] [model] [size] → uploads the generated image.
func handleImage(s *discordgo.Session, i *discordgo.InteractionCreate, get func(string) string) {
	prompt := get("prompt")
	if prompt == "" {
		respond(s, i, "Please provide a prompt.")
		return
	}
	model := get("model")
	if model == "" {
		model = cfg.DefaultImageModel
	}
	size := get("size")

	respondDefer(s, i)

	data, ext, err := vc.GenerateImage(model, prompt, size)
	if err != nil {
		editResponse(s, i, "Error: "+err.Error())
		return
	}
	// Quality guard: some models return a tiny ~8-10KB image (diffusion
	// steps:0) that comes out as a blur. Warn but still upload.
	if len(data) < 15000 {
		warn := fmt.Sprintf("⚠️ `%s` returned a very small image (%d bytes) — likely blurred (steps:0). Try `/image model:venice-sd35` for full quality. Uploading what came back:", model, len(data))
		if _, err := s.ChannelMessageSend(i.ChannelID, warn); err != nil {
			logErrorf("image guard message failed: %v", err)
		}
	}
	// Upload as attachment.
	file := &discordgo.File{
		Name:   fmt.Sprintf("venice_%s.%s", i.ID, ext),
		Reader: bytes.NewReader(data),
	}
	// Send as a follow-up message with the attachment.
	_, err = s.ChannelMessageSendComplex(i.ChannelID, &discordgo.MessageSend{
		Content: fmt.Sprintf("**%s** — generated with `%s`", truncateForDiscord(prompt), model),
		Files:   []*discordgo.File{file},
	})
	if err != nil {
		editResponse(s, i, "Error: "+err.Error())
		return
	}
	editResponse(s, i, "Image generated ✅")
}

// handleTTS: /tts [text] [voice] → uploads speech audio.
func handleTTS(s *discordgo.Session, i *discordgo.InteractionCreate, get func(string) string) {
	text := get("text")
	if text == "" {
		respond(s, i, "Please provide text to speak.")
		return
	}
	voice := get("voice")
	model := cfg.DefaultTTSModel
	if voice == "" {
		voice = cfg.DefaultTTSVoice
	} else {
		// If the voice matches a stored cloned-voice name, use its vv_ handle
		// and its model (cloned handles are model-specific).
		if store, err := loadClonedVoiceStore(); err == nil {
			if id, m, ok := store.find(voice); ok {
				voice = id
				model = m
			}
		}
	}

	respondDefer(s, i)

	data, ext, err := vc.TTS(model, text, voice, ttsFormatForModel(model))
	if err != nil {
		editResponse(s, i, "Error: "+err.Error())
		return
	}
	_, err = s.ChannelMessageSendComplex(i.ChannelID, &discordgo.MessageSend{
		Content: "**TTS** — `" + voice + "`",
		Files:   []*discordgo.File{{Name: fmt.Sprintf("venice_tts_%s.%s", i.ID, ext), Reader: bytes.NewReader(data)}},
	})
	if err != nil {
		editResponse(s, i, "Error: "+err.Error())
		return
	}
	editResponse(s, i, "Speech generated ✅")
}

// handleVoiceClone: /voice-clone <name> [mp3] [model] — clone a voice from an
// attached audio sample using Venice's /audio/voices, then store the handle.
func handleVoiceClone(s *discordgo.Session, i *discordgo.InteractionCreate, get func(string) string) {
	name := strings.TrimSpace(get("name"))
	if name == "" {
		respond(s, i, "Usage: `/voice-clone name:<name> mp3:<file>` (attach an audio sample).")
		return
	}
	model := strings.TrimSpace(get("model"))
	if model == "" {
		model = "tts-chatterbox-hd"
	}

	attach := getAttachment(i, get("mp3"))
	if attach == nil {
		respond(s, i, "Please attach an audio sample via the **mp3** option (`/voice-clone name:<name> mp3:<file>`). MP3/WAV/FLAC/M4A work.")
		return
	}
	if attach.ContentType != "" && !strings.HasPrefix(attach.ContentType, "audio/") {
		respond(s, i, fmt.Sprintf("❌ Attached file `%s` is `%s`, not audio.", attach.Filename, attach.ContentType))
		return
	}

	respondDefer(s, i)

	// Download the attachment bytes (Discord CDN).
	sample, err := downloadURL(attach.URL)
	if err != nil {
		editResponse(s, i, "❌ Could not download the attached audio: "+err.Error())
		return
	}

	out, err := vc.CloneVoice(model, sample, attach.Filename)
	if err != nil {
		editResponse(s, i, "❌ Voice cloning failed: "+err.Error())
		return
	}

	// Store the handle locally.
	store, err := loadClonedVoiceStore()
	if err != nil {
		log.Printf("[voice-clone] warning: could not load store: %v", err)
	} else if err := store.add(out.ID, out.Model, name); err != nil {
		log.Printf("[voice-clone] warning: could not save handle: %v", err)
	}

	editResponse(s, i, fmt.Sprintf("🎙️ **Voice cloned** — **%s**\n- Handle: `%s`\n- Model: `%s`\n\nUse it with `/tts text:... voice:%s`.\n_Handles expire after 7 days._", name, out.ID, out.Model, name))
}

// handleVoices: /voices — list locally stored cloned voice handles.
func handleVoices(s *discordgo.Session, i *discordgo.InteractionCreate, get func(string) string) {
	store, err := loadClonedVoiceStore()
	if err != nil {
		respond(s, i, "⚠️ Could not read cloned voices: "+err.Error())
		return
	}
	respond(s, i, store.format())
}

const (
	minMusicDurationSeconds     = 120
	defaultMusicDurationSeconds = 240
	maxMusicDurationSeconds     = 360
)

// musicQueueMessage includes the requested duration and Venice's price estimate.
func musicQueueMessage(model string, durationSeconds int, quote float64) string {
	msg := fmt.Sprintf("Queued music job (`%s`). Generating... this can take a minute or two. I'll post the result when ready.", model)
	msg += fmt.Sprintf("\n\nRequested duration: **%s** · Venice estimate: **$%.4f**", formatDuration(durationSeconds), quote)
	return msg
}

// formatDuration renders seconds as "1m 45s" for ≥60s or "45s" otherwise.
func formatDuration(sec int) string {
	if sec < 60 {
		return fmt.Sprintf("%ds", sec)
	}
	m := sec / 60
	s := sec % 60
	if s == 0 {
		return fmt.Sprintf("%dm", m)
	}
	return fmt.Sprintf("%dm %ds", m, s)
}

// handleMusic: /music [prompt] [lyrics] → async music generation.
func handleMusic(s *discordgo.Session, i *discordgo.InteractionCreate, get func(string) string) {
	prompt := get("prompt")
	if prompt == "" {
		respond(s, i, "Please provide a music prompt.")
		return
	}
	durationSeconds := defaultMusicDurationSeconds
	if rawDuration := get("duration"); rawDuration != "" {
		parsedDuration, err := strconv.Atoi(rawDuration)
		if err != nil || parsedDuration < minMusicDurationSeconds || parsedDuration > maxMusicDurationSeconds {
			respond(s, i, fmt.Sprintf(
				"Duration must be between %d and %d seconds.",
				minMusicDurationSeconds,
				maxMusicDurationSeconds,
			))
			return
		}
		durationSeconds = parsedDuration
	}
	lyrics := get("lyrics")

	// Normalize song-file style lyrics (Tala/Ordo [Section - Voice] headers,
	// markdown, --- separators) into clean MiniMax Music v2.6 structure tags so
	// the model doesn't sing the stage directions.
	if lyrics != "" {
		lyrics = normalizeLyrics(lyrics)
		// Log the exact lyrics about to be sent to the API for review.
		if path, err := logNormalizedLyrics(prompt, lyrics); err == nil {
			fmt.Printf("[music] normalized lyrics logged to %s\n", path)
		} else {
			fmt.Printf("[music] warning: could not log normalized lyrics: %v\n", err)
		}
	}

	respondDefer(s, i)

	model := cfg.DefaultMusicModel
	quote, err := vc.QuoteAudio(model, durationSeconds)
	if err != nil {
		editResponse(s, i, "Could not estimate music cost: "+err.Error())
		return
	}
	queueID, err := vc.QueueAudio(model, prompt, lyrics, false, durationSeconds)
	if err != nil {
		editResponse(s, i, "Error: "+err.Error())
		return
	}
	editResponse(s, i, musicQueueMessage(model, durationSeconds, quote))

	// Poll in a goroutine and post when done.
	go func() {
		data, ext, err := vc.WaitAudio(model, queueID, 15, 10)
		if err != nil {
			editResponse(s, i, "Music generation failed: "+err.Error())
			return
		}
		_, err = s.ChannelMessageSendComplex(i.ChannelID, &discordgo.MessageSend{
			Content: "**Music** — `" + model + "`",
			Files:   []*discordgo.File{{Name: fmt.Sprintf("venice_music_%s.%s", i.ID, ext), Reader: bytes.NewReader(data)}},
		})
		if err != nil {
			editResponse(s, i, "Error posting music: "+err.Error())
		}
	}()
}

// handleBalance: /balance
func handleBalance(s *discordgo.Session, i *discordgo.InteractionCreate) {
	bal, err := vc.Balance()
	if err != nil {
		respond(s, i, "Error: "+err.Error())
		return
	}
	respond(s, i, "**Venice Balance**\n"+bal)
}

// handleModels: /models [type]
func handleModels(s *discordgo.Session, i *discordgo.InteractionCreate, get func(string) string) {
	modality := get("type")
	models, err := vc.Models(modality)
	if err != nil {
		respond(s, i, "Error: "+err.Error())
		return
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("**Venice models** (`%s`):\n", modality))
	for _, m := range models {
		sb.WriteString("- ")
		sb.WriteString(m.ID)
		sb.WriteString("\n")
		if sb.Len() > 1500 {
			sb.WriteString("... and more")
			break
		}
	}
	respond(s, i, sb.String())
}

```

## `./venice/commands.go`

```go
// commands.go — Venice subpackage command definitions for the ElyLightspeed bot.
// All commands are registered with the "venice-" prefix (e.g. /venice-chat).
package venice

import (
	"fmt"
	"github.com/bwmarrin/discordgo"
)

// CommandSpec pairs a command definition with its handler, mirroring the
// main package's commandSpec so the main package can register these directly.
type CommandSpec struct {
	Command *discordgo.ApplicationCommand
	Handler func(s *discordgo.Session, i *discordgo.InteractionCreate)
}

// CommandSpecs returns the Venice slash commands for the main package to register.
func CommandSpecs() []CommandSpec {
	minMusicDuration := float64(minMusicDurationSeconds)
	return []CommandSpec{
		{
			Command: &discordgo.ApplicationCommand{
				Name:        "venice",
				Description: "Venice AI commands (chat, image, tts, music, search, etc.)",
				Options: []*discordgo.ApplicationCommandOption{
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "chat",
						Description: "Chat with a Venice text model",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionString, Name: "prompt", Description: "Your message", Required: true},
							{Type: discordgo.ApplicationCommandOptionString, Name: "model", Description: "Model id (default from config)"},
							{Type: discordgo.ApplicationCommandOptionBoolean, Name: "web", Description: "Enable web search"},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "image",
						Description: "Generate an image with Venice",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionString, Name: "prompt", Description: "Image prompt", Required: true},
							{Type: discordgo.ApplicationCommandOptionString, Name: "model", Description: "Image model (default flux-2-pro)"},
							{Type: discordgo.ApplicationCommandOptionString, Name: "size", Description: "Size e.g. 1024x1024"},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "tts",
						Description: "Generate speech audio (text-to-speech)",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionString, Name: "text", Description: "Text to speak", Required: true},
							{Type: discordgo.ApplicationCommandOptionString, Name: "voice", Description: "Voice id or cloned-voice name (default from config)"},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "voice-clone",
						Description: "Clone a voice from an attached audio sample (Chatterbox HD)",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionString, Name: "name", Description: "Name for the cloned voice", Required: true},
							{Type: discordgo.ApplicationCommandOptionAttachment, Name: "mp3", Description: "Audio sample to clone from (MP3/WAV/FLAC/M4A)"},
							{Type: discordgo.ApplicationCommandOptionString, Name: "model", Description: "tts-chatterbox-hd (default) | tts-minimax-speech-02-hd"},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "voices",
						Description: "List your cloned Venice voices",
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "balance",
						Description: "Show Venice account balance",
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "models",
						Description: "List Venice models (by modality)",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionString, Name: "type", Description: "text|image|video|music|embedding|asr", Required: true},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "music",
						Description: "Generate a song with Venice (120-360 sec, cost quoted first)",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionString, Name: "prompt", Description: "Style/genre prompt", Required: true},
							{Type: discordgo.ApplicationCommandOptionString, Name: "lyrics", Description: "Lyrics text"},
							{
								Type:        discordgo.ApplicationCommandOptionInteger,
								Name:        "duration",
								Description: "Song length in seconds (120-360, default 240)",
								MinValue:    &minMusicDuration,
								MaxValue:    maxMusicDurationSeconds,
							},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "stt",
						Description: "Transcribe an attached audio file (speech-to-text)",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionAttachment, Name: "audio", Description: "Audio file to transcribe", Required: true},
							{Type: discordgo.ApplicationCommandOptionString, Name: "model", Description: "ASR model (default openai/whisper-large-v3)"},
							{Type: discordgo.ApplicationCommandOptionString, Name: "language", Description: "Optional language code (e.g. en)"},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "search",
						Description: "Web search via Venice augment",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionString, Name: "query", Description: "Search query", Required: true},
							{Type: discordgo.ApplicationCommandOptionString, Name: "max_results", Description: "Max results (default 5)"},
							{Type: discordgo.ApplicationCommandOptionString, Name: "provider", Description: "brave (default) | google"},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "scrape",
						Description: "Scrape a URL to markdown via Venice augment",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionString, Name: "url", Description: "URL to scrape", Required: true},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "video",
						Description: "Generate a video via Venice (async)",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionString, Name: "prompt", Description: "Video prompt", Required: true},
							{Type: discordgo.ApplicationCommandOptionString, Name: "model", Description: "Video model (default seedance-2-5-text-to-video-basic)"},
							{Type: discordgo.ApplicationCommandOptionString, Name: "aspect_ratio", Description: "21:9 | 16:9 | 4:3 | 1:1 | 3:4 | 9:16 (default 16:9)"},
							{Type: discordgo.ApplicationCommandOptionString, Name: "resolution", Description: "1080p | 720p | 480p (default 1080p)"},
							{Type: discordgo.ApplicationCommandOptionString, Name: "duration", Description: "Duration e.g. 5s"},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "embed",
						Description: "Generate an embedding for text",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionString, Name: "input", Description: "Text to embed", Required: true},
							{Type: discordgo.ApplicationCommandOptionString, Name: "model", Description: "Embedding model (default text-embedding-bge-m3)"},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "characters",
						Description: "List Venice characters",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionString, Name: "search", Description: "Optional search term"},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "upscale",
						Description: "Upscale an image via Venice",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionAttachment, Name: "image", Description: "Image to upscale", Required: true},
							{Type: discordgo.ApplicationCommandOptionString, Name: "model", Description: "Upscale model"},
							{Type: discordgo.ApplicationCommandOptionString, Name: "scale", Description: "Scale factor 2-8"},
						},
					},
				},
			},
			Handler: handleVeniceCommand, // We will map the subcommands here
		},
	}
}

// handleVeniceCommand routes the root /venice command to the correct subcommand handler.
func handleVeniceCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	data := i.ApplicationCommandData()
	if len(data.Options) == 0 {
		return
	}
	subCommand := data.Options[0]

	// Create a dynamic 'get' function that reads options from the subcommand, not the root command
	get := func(key string) string {
		for _, o := range subCommand.Options {
			if o.Name == key {
				if s, ok := o.Value.(string); ok {
					return s
				}
				return fmt.Sprintf("%v", o.Value)
			}
		}
		return ""
	}

	switch subCommand.Name {
	case "chat":
		handleChat(s, i, get)
	case "image":
		handleImage(s, i, get)
	case "tts":
		handleTTS(s, i, get)
	case "voice-clone":
		handleVoiceClone(s, i, get)
	case "voices":
		handleVoices(s, i, get)
	case "balance":
		handleBalance(s, i) // balance doesn't use 'get'
	case "models":
		handleModels(s, i, get)
	case "music":
		handleMusic(s, i, get)
	case "stt":
		handleSTT(s, i, get)
	case "search":
		handleSearch(s, i, get)
	case "scrape":
		handleScrape(s, i, get)
	case "video":
		handleVideo(s, i, get)
	case "embed":
		handleEmbed(s, i, get)
	case "characters":
		handleCharacters(s, i, get)
	case "upscale":
		handleUpscale(s, i, get)
	}
}

```

## `./deploy/sunfire.service`

```ini
[Unit]
Description=Sunfire Discord Bot
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=/home/elysium/Repos/Sunfire
Environment=SUNFIRE_HOME=/home/elysium/Repos/Sunfire
Environment=SUNFIRE_CONFIG_FILE=/home/elysium/Repos/Sunfire/.private/config.json
ExecStart=/home/elysium/Repos/Sunfire/Sunfire
Restart=on-failure
RestartSec=5s
TimeoutStopSec=15s
KillSignal=SIGTERM
UMask=0077
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=default.target

```

## `./modules/venice.go`

```go
// Package venice is a small, stdlib-only Go client for the Venice AI API.
//
// It covers the native Venice endpoints: chat completions (OpenAI-compatible
// with Venice-specific parameters), image generation/editing, text-to-speech,
// speech-to-text, music & sound-effect generation, video generation, web
// search/scrape, models, billing balance/usage, and characters.
//
// The client resolves its API key from, in order of precedence:
//
//  1. an explicit WithAPIKey option,
//  2. the VENICE_ELYVENICE_API_KEY environment variable,
//  3. the "VENICE_ELYVENICE_API_KEY" field of ~/.private/config.json.
package venice

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DefaultBaseURL is the public Venice API root.
const DefaultBaseURL = "https://api.venice.ai/api/v1"

// ConfigKey is the JSON field name read from ~/.private/config.json.
const ConfigKey = "VENICE_ELYVENICE_API_KEY"

// EnvKey is the environment variable that overrides the config file.
const EnvKey = "VENICE_ELYVENICE_API_KEY"

// Option configures a Client.
type Option func(*Client)

// WithAPIKey sets the bearer token used for authorization.
func WithAPIKey(key string) Option {
	return func(c *Client) { c.APIKey = key }
}

// WithBaseURL overrides the API base URL (default DefaultBaseURL).
func WithBaseURL(base string) Option {
	return func(c *Client) { c.BaseURL = strings.TrimRight(base, "/") }
}

// WithHTTPClient sets the underlying *http.Client (custom timeouts, transport).
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) { c.HTTP = hc }
}

// Client talks to the Venice AI API. Zero-value is not usable; construct with
// NewClient.
type Client struct {
	APIKey  string
	BaseURL string
	HTTP    *http.Client
}

// NewClient builds a Client. If no API key is supplied via options, the key is
// resolved from the environment (VENICE_ELYVENICE_API_KEY) and then from
// ~/.private/config.json (key VENICE_ELYVENICE_API_KEY). An error is returned
// only if key resolution fails (no key found) — it never panics.
func NewClient(opts ...Option) (*Client, error) {
	c := &Client{
		BaseURL: DefaultBaseURL,
		HTTP:    &http.Client{Timeout: 300 * time.Second},
	}
	for _, o := range opts {
		o(c)
	}
	if c.APIKey == "" {
		if k := os.Getenv(EnvKey); k != "" {
			c.APIKey = k
		}
	}
	if c.APIKey == "" {
		k, err := keyFromConfig()
		if err != nil {
			return nil, fmt.Errorf("venice: %w", err)
		}
		c.APIKey = k
	}
	return c, nil
}

// keyFromConfig reads the VENICE_ELYVENICE_API_KEY field from
// $HOME/.private/config.json (or %USERPROFILE% on Windows).
func keyFromConfig() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot resolve home directory: %w", err)
	}
	path := filepath.Join(home, ".private", "config.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("no API key set: %s is unset and %s unreadable: %w", EnvKey, path, err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return "", fmt.Errorf("parse %s: %w", path, err)
	}
	k, _ := m[ConfigKey].(string)
	if k == "" {
		return "", fmt.Errorf("no API key set: %s is unset and %s has no %q field", EnvKey, path, ConfigKey)
	}
	return k, nil
}

// APIError is a non-2xx response from the Venice API.
type APIError struct {
	StatusCode int
	Status     string
	Type       string `json:"type"`
	Code       string `json:"code"`
	Message    string `json:"message"`
	Raw        []byte
}

func (e *APIError) Error() string {
	msg := strings.TrimSpace(e.Message)
	if msg == "" {
		msg = strings.TrimSpace(string(e.Raw))
	}
	if msg == "" {
		msg = e.Status
	}
	return fmt.Sprintf("venice: %s (%d): %s", e.Status, e.StatusCode, msg)
}

// Send performs an HTTP request with the client's auth and returns the raw
// response. It does not check the status code; callers decide how to interpret
// the body. On a non-2xx status, call DecodeErrorResponse to surface an
// *APIError.
func (c *Client) Send(ctx context.Context, method, path string, query url.Values, contentType string, body []byte) (*http.Response, error) {
	return c.send(ctx, method, path, query, contentType, body)
}

// send performs an HTTP request and returns the raw response. It does not
// check the status code; callers decide how to interpret the body (JSON or
// binary). On a non-2xx status the caller is expected to call
// decodeError to surface an *APIError.
func (c *Client) send(ctx context.Context, method, path string, query url.Values, contentType string, body []byte) (*http.Response, error) {
	if c.APIKey == "" {
		return nil, errors.New("venice: no API key configured")
	}
	u := c.BaseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	return c.HTTP.Do(req)
}

// DoJSON marshals in, sends the request, checks the status and unmarshals the
// JSON response into out. A non-2xx status produces an *APIError.
func (c *Client) DoJSON(ctx context.Context, method, path string, query url.Values, in, out any) error {
	return c.doJSON(ctx, method, path, query, in, out)
}

// doJSON marshals in, sends the request, checks the status and unmarshals the
// JSON response into out. A non-2xx status produces an *APIError.
func (c *Client) doJSON(ctx context.Context, method, path string, query url.Values, in, out any) error {
	var body []byte
	var err error
	if in != nil {
		body, err = json.Marshal(in)
		if err != nil {
			return fmt.Errorf("venice: marshal request: %w", err)
		}
	}
	contentType := "application/json"
	if body == nil {
		contentType = ""
	}
	resp, err := c.send(ctx, method, path, query, contentType, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return decodeError(resp)
	}
	if out == nil {
		io.Copy(io.Discard, resp.Body)
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("venice: decode response: %w", err)
	}
	return nil
}

// DoRaw sends the request and returns the raw response body bytes plus the
// Content-Type of the reply. Non-2xx statuses become *APIError.
func (c *Client) DoRaw(ctx context.Context, method, path string, query url.Values, in any) ([]byte, string, http.Header, error) {
	return c.doRaw(ctx, method, path, query, in)
}

// doRaw sends the request and returns the raw response body bytes plus the
// Content-Type of the reply. Non-2xx statuses become *APIError.
func (c *Client) doRaw(ctx context.Context, method, path string, query url.Values, in any) ([]byte, string, http.Header, error) {
	var body []byte
	var err error
	if in != nil {
		body, err = json.Marshal(in)
		if err != nil {
			return nil, "", nil, fmt.Errorf("venice: marshal request: %w", err)
		}
	}
	resp, err := c.send(ctx, method, path, query, "application/json", body)
	if err != nil {
		return nil, "", nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, "", nil, decodeError(resp)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", nil, err
	}
	return data, resp.Header.Get("Content-Type"), resp.Header, nil
}

func decodeError(resp *http.Response) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	apiErr := &APIError{StatusCode: resp.StatusCode, Status: resp.Status, Raw: raw}
	if err := json.Unmarshal(raw, apiErr); err != nil {
		// Not JSON; keep raw text in Message.
		apiErr.Message = strings.TrimSpace(string(raw))
	}
	if apiErr.Message == "" && apiErr.Code == "" {
		apiErr.Message = strings.TrimSpace(string(raw))
	}
	return apiErr
}

// DecodeErrorResponse builds an *APIError from an already-read error
// response, for packages that performed the request themselves.
func DecodeErrorResponse(resp *http.Response) error {
	return decodeError(resp)
}

// WriteFile writes data to path (0666, as os.WriteFile).
func WriteFile(path string, data []byte) error {
	return os.WriteFile(path, data, 0o666)
}

// BuildMultipart assembles a multipart/form-data body from string fields and
// file parts, returning the buffer and the boundary Content-Type header value.
func BuildMultipart(fields map[string]string, files map[string]MultipartFile) ([]byte, string, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for k, v := range fields {
		if err := w.WriteField(k, v); err != nil {
			return nil, "", err
		}
	}
	for k, f := range files {
		fw, err := w.CreateFormFile(k, f.Name)
		if err != nil {
			return nil, "", err
		}
		if _, err := fw.Write(f.Data); err != nil {
			return nil, "", err
		}
	}
	if err := w.Close(); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), w.FormDataContentType(), nil
}

// MultipartFile is a file part for multipart uploads (STT, text parser).
type MultipartFile struct {
	Name string // filename reported to the server
	MIME string // optional; used when set
	Data []byte
}

// LoadImageArg accepts a base64 image string, a data URL, an http(s) URL, or
// a local file path and returns a base64 data URL suitable for the API image
// fields (e.g. POST /image/edit "image", POST /image/upscale "image"). Binary
// input is returned as-is (assumed already base64). URLs are fetched with the
// client's HTTP client.
func (c *Client) LoadImageArg(arg string) (string, error) {
	return loadImageArg(arg, c.HTTP)
}

// LoadImageArg is a package-level convenience that fetches URLs with
// http.DefaultClient.
func LoadImageArg(arg string) (string, error) {
	return loadImageArg(arg, http.DefaultClient)
}

func loadImageArg(arg string, hc *http.Client) (string, error) {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return "", errors.New("empty image argument")
	}
	// data: URL -> extract the base64 payload
	if strings.HasPrefix(arg, "data:") {
		if i := strings.Index(arg, ","); i >= 0 {
			return arg[i+1:], nil
		}
		return "", errors.New("malformed data URL")
	}
	// http(s) URL -> fetch bytes
	if strings.HasPrefix(arg, "http://") || strings.HasPrefix(arg, "https://") {
		resp, err := hc.Get(arg)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("fetch %s: %s", arg, resp.Status)
		}
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			return "", err
		}
		return base64.StdEncoding.EncodeToString(data), nil
	}
	// local file path
	data, err := os.ReadFile(arg)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(data), nil
}

```

## `./modules/models/models.go`

```go
// Package models implements the Venice Models API: GET /models and
// GET /models/traits.
package models

import (
	"context"
	"net/http"
	"net/url"

	"sunfire/modules"
)

// ListOptions filter the model catalog. Type is a filter such as "text",
// "image", "tts", "asr", "audio", "video". All fields are optional.
type ListOptions struct {
	Type string
	// Extra filters passed through as query parameters.
	Extra map[string]string
}

// Model describes one model in the Venice catalog.
type Model struct {
	ID        string     `json:"id"`
	Type      string     `json:"type"`
	Object    string     `json:"object"`
	Created   int64      `json:"created"`
	OwnedBy   string     `json:"owned_by"`
	ModelSpec *ModelSpec `json:"model_spec,omitempty"`
	// Extra holds any unrecognized fields for forward compatibility.
	Extra map[string]any `json:"-"`
}

// ModelSpec carries detailed per-model metadata.
type ModelSpec struct {
	AvailableContextTokens int            `json:"availableContextTokens,omitempty"`
	Capabilities           *Capabilities  `json:"capabilities,omitempty"`
	DefaultFormat          string         `json:"default_format,omitempty"`
	SupportedFormats       []string       `json:"supported_formats,omitempty"`
	Voices                 []string       `json:"voices,omitempty"`
	VoiceChanger           bool           `json:"voice_changer,omitempty"`
	Pricing                *Pricing       `json:"pricing,omitempty"`
	Constraints            map[string]any `json:"constraints,omitempty"`
	Extra                  map[string]any `json:"-"`
}

// Capabilities lists model feature flags.
type Capabilities struct {
	SupportsFunctionCalling bool `json:"supportsFunctionCalling,omitempty"`
	SupportsResponseSchema  bool `json:"supportsResponseSchema,omitempty"`
	SupportsWebSearch       bool `json:"supportsWebSearch,omitempty"`
	SupportsE2EE            bool `json:"supportsE2EE,omitempty"`
	SupportsTeeAttestation  bool `json:"supportsTeeAttestation,omitempty"`
}

// Pricing is a flexible price schedule (resolutions, quality, durations).
type Pricing struct {
	Resolutions map[string]Price            `json:"resolutions,omitempty"`
	Quality     map[string]map[string]Price `json:"quality,omitempty"`
	Durations   map[string]Price            `json:"durations,omitempty"`
}

// Price is a USD/DIEM price pair.
type Price struct {
	USD  float64 `json:"usd"`
	DIEM float64 `json:"diem"`
}

// Response is the list-model response envelope.
type Response struct {
	Object string  `json:"object"`
	Data   []Model `json:"data"`
}

// Traits maps trait names to their current model IDs.
type Traits struct {
	Object string            `json:"object"`
	Data   map[string]string `json:"data"`
}

// Client wraps a *venice.Client for the models API.
type Client struct {
	*venice.Client
}

// NewClient wraps a *venice.Client.
func NewClient(c *venice.Client) *Client { return &Client{Client: c} }

// List returns models, optionally filtered by type.
func (c *Client) List(ctx context.Context, opts *ListOptions) (*Response, error) {
	q := url.Values{}
	if opts != nil {
		if opts.Type != "" {
			q.Set("type", opts.Type)
		}
		for k, v := range opts.Extra {
			q.Set(k, v)
		}
	}
	var out Response
	if err := c.DoJSON(ctx, http.MethodGet, "/models", q, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListRaw returns the raw JSON body of GET /models.
func (c *Client) ListRaw(ctx context.Context, opts *ListOptions) ([]byte, error) {
	q := url.Values{}
	if opts != nil {
		if opts.Type != "" {
			q.Set("type", opts.Type)
		}
		for k, v := range opts.Extra {
			q.Set(k, v)
		}
	}
	data, _, _, err := c.DoRaw(ctx, http.MethodGet, "/models", q, nil)
	return data, err
}

// Traits lists trait-to-model mappings.
func (c *Client) Traits(ctx context.Context) (*Traits, error) {
	var out Traits
	if err := c.DoJSON(ctx, http.MethodGet, "/models/traits", nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ByID finds a model by ID in a list response.
func (r *Response) ByID(id string) *Model {
	if r == nil {
		return nil
	}
	for i := range r.Data {
		if r.Data[i].ID == id {
			return &r.Data[i]
		}
	}
	return nil
}

// IDs returns the model IDs in a list response.
func (r *Response) IDs() []string {
	if r == nil {
		return nil
	}
	out := make([]string, 0, len(r.Data))
	for _, m := range r.Data {
		out = append(out, m.ID)
	}
	return out
}

```

## `./modules/image/image.go`

```go
// Package image implements Venice image endpoints: native generation
// (POST /image/generate), OpenAI-compatible generation (POST /images/generations),
// editing/inpainting, multi-image editing, background removal, upscaling, and
// style listing.
package image

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"

	"sunfire/modules"
)

// Request is a native image generation request (POST /image/generate).
type Request struct {
	Model           string         `json:"model"`
	Prompt          string         `json:"prompt"`
	NegativePrompt  string         `json:"negative_prompt,omitempty"`
	Width           *int           `json:"width,omitempty"`
	Height          *int           `json:"height,omitempty"`
	Format          string         `json:"format,omitempty"` // jpeg, png, webp
	Variants        *int           `json:"variants,omitempty"`
	ReturnBinary    *bool          `json:"return_binary,omitempty"`
	SafeMode        *bool          `json:"safe_mode,omitempty"`
	Seed            *int           `json:"seed,omitempty"`
	CFGScale        *float64       `json:"cfg_scale,omitempty"`
	StylePreset     string         `json:"style_preset,omitempty"`
	AspectRatio     string         `json:"aspect_ratio,omitempty"`
	Resolution      string         `json:"resolution,omitempty"`
	EnhancePrompt   *bool          `json:"enhance_prompt,omitempty"`
	Quality         string         `json:"quality,omitempty"`
	EnableWebSearch *bool          `json:"enable_web_search,omitempty"`
	Extra           map[string]any `json:"-"`
}

// MarshalJSON merges Extra fields into the request body.
func (r Request) MarshalJSON() ([]byte, error) {
	type alias Request
	b, err := json.Marshal(alias(r))
	if err != nil {
		return nil, err
	}
	if len(r.Extra) == 0 {
		return b, nil
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	for k, v := range r.Extra {
		m[k] = v
	}
	return json.Marshal(m)
}

// Response is a native image generation response. Images holds the generated
// images as base64-encoded strings (unless the request used return_binary).
type Response struct {
	Images []string `json:"images"`
}

// Decoded returns the generated images decoded from base64.
func (r *Response) Decoded() ([][]byte, error) {
	out := make([][]byte, 0, len(r.Images))
	for _, s := range r.Images {
		b, err := base64.StdEncoding.DecodeString(s)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, nil
}

// EditRequest is an image edit/inpaint request (POST /image/edit). The image
// is provided as base64 (Data), as an http(s) URL (URL), or as a local file
// path (Path) which is loaded at call time.
type EditRequest struct {
	Model          string         `json:"model"`
	Prompt         string         `json:"prompt"`
	Image          string         `json:"image,omitempty"` // base64
	ImageURL       string         `json:"image_url,omitempty"`
	Path           string         `json:"-"` // local file path, resolved to base64
	NegativePrompt string         `json:"negative_prompt,omitempty"`
	Mask           string         `json:"mask,omitempty"` // base64 (inpaint region)
	Width          *int           `json:"width,omitempty"`
	Height         *int           `json:"height,omitempty"`
	Format         string         `json:"format,omitempty"`
	Seed           *int           `json:"seed,omitempty"`
	CFGScale       *float64       `json:"cfg_scale,omitempty"`
	AspectRatio    string         `json:"aspect_ratio,omitempty"`
	Resolution     string         `json:"resolution,omitempty"`
	Quality        string         `json:"quality,omitempty"`
	EnhancePrompt  *bool          `json:"enhance_prompt,omitempty"`
	SafeMode       *bool          `json:"safe_mode,omitempty"`
	Extra          map[string]any `json:"-"`
}

// MarshalJSON merges Extra fields.
func (r EditRequest) MarshalJSON() ([]byte, error) {
	type alias EditRequest
	b, err := json.Marshal(alias(r))
	if err != nil {
		return nil, err
	}
	if len(r.Extra) == 0 {
		return b, nil
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	for k, v := range r.Extra {
		m[k] = v
	}
	return json.Marshal(m)
}

// MultiEditRequest is a multi-image edit (POST /image/multi-edit).
type MultiEditRequest struct {
	Model          string         `json:"model"`
	Prompt         string         `json:"prompt"`
	Images         []string       `json:"images,omitempty"` // base64
	ImageURLs      []string       `json:"image_urls,omitempty"`
	NegativePrompt string         `json:"negative_prompt,omitempty"`
	Format         string         `json:"format,omitempty"`
	Seed           *int           `json:"seed,omitempty"`
	Resolution     string         `json:"resolution,omitempty"`
	AspectRatio    string         `json:"aspect_ratio,omitempty"`
	Quality        string         `json:"quality,omitempty"`
	Extra          map[string]any `json:"-"`
}

// MarshalJSON merges Extra fields.
func (r MultiEditRequest) MarshalJSON() ([]byte, error) {
	type alias MultiEditRequest
	b, err := json.Marshal(alias(r))
	if err != nil {
		return nil, err
	}
	if len(r.Extra) == 0 {
		return b, nil
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	for k, v := range r.Extra {
		m[k] = v
	}
	return json.Marshal(m)
}

// UpscaleRequest upsizes an image (POST /image/upscale).
type UpscaleRequest struct {
	Model    string         `json:"model"`
	Image    string         `json:"image,omitempty"` // base64
	ImageURL string         `json:"image_url,omitempty"`
	Scale    *int           `json:"scale,omitempty"`
	Extra    map[string]any `json:"-"`
}

// MarshalJSON merges Extra fields.
func (r UpscaleRequest) MarshalJSON() ([]byte, error) {
	type alias UpscaleRequest
	b, err := json.Marshal(alias(r))
	if err != nil {
		return nil, err
	}
	if len(r.Extra) == 0 {
		return b, nil
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	for k, v := range r.Extra {
		m[k] = v
	}
	return json.Marshal(m)
}

// OpenAIRequest is an OpenAI-compatible generation request
// (POST /images/generations). Safe-mode style moderation uses the
// "moderation" field (auto|low).
type OpenAIRequest struct {
	Model          string         `json:"model"`
	Prompt         string         `json:"prompt"`
	Size           string         `json:"size,omitempty"`            // e.g. 1024x1024
	ResponseFormat string         `json:"response_format,omitempty"` // b64_json | url
	Moderation     string         `json:"moderation,omitempty"`
	Extra          map[string]any `json:"-"`
}

// MarshalJSON merges Extra fields.
func (r OpenAIRequest) MarshalJSON() ([]byte, error) {
	type alias OpenAIRequest
	b, err := json.Marshal(alias(r))
	if err != nil {
		return nil, err
	}
	if len(r.Extra) == 0 {
		return b, nil
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	for k, v := range r.Extra {
		m[k] = v
	}
	return json.Marshal(m)
}

// OpenAIResponse is the OpenAI-compatible generation response.
type OpenAIResponse struct {
	Created int64         `json:"created"`
	Data    []OpenAIImage `json:"data"`
}

// OpenAIImage is a single generated image (b64 or URL).
type OpenAIImage struct {
	URL           string `json:"url,omitempty"`
	B64JSON       string `json:"b64_json,omitempty"`
	RevisedPrompt string `json:"revised_prompt,omitempty"`
}

// Client wraps a *venice.Client for image endpoints.
type Client struct {
	*venice.Client
}

// NewClient wraps a *venice.Client.
func NewClient(c *venice.Client) *Client { return &Client{Client: c} }

// Generate runs a native image generation. Returns decoded bytes of the first
// image plus the full response (for multi-variant calls).
func (c *Client) Generate(ctx context.Context, req *Request) (*Response, error) {
	var out Response
	if err := c.DoJSON(ctx, http.MethodPost, "/image/generate", nil, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GenerateRaw returns the raw JSON body, in case the caller wants to
// decode fields not covered by Response.
func (c *Client) GenerateRaw(ctx context.Context, req *Request) ([]byte, error) {
	data, _, _, err := c.DoRaw(ctx, http.MethodPost, "/image/generate", nil, req)
	return data, err
}

// Edit runs an image edit. Path/URL are resolved into base64 before sending.
func (c *Client) Edit(ctx context.Context, req *EditRequest) (*Response, error) {
	if err := req.resolve(); err != nil {
		return nil, err
	}
	var out Response
	if err := c.DoJSON(ctx, http.MethodPost, "/image/edit", nil, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// MultiEdit runs a multi-image edit.
func (c *Client) MultiEdit(ctx context.Context, req *MultiEditRequest) (*Response, error) {
	var out Response
	if err := c.DoJSON(ctx, http.MethodPost, "/image/multi-edit", nil, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RemoveBackground strips the background from an image and returns the PNG
// bytes. The input can be base64, an http(s) URL, or a local file path.
func (c *Client) RemoveBackground(ctx context.Context, image string) ([]byte, error) {
	data, err := loadImageArg(image)
	if err != nil {
		return nil, err
	}
	req := map[string]string{"image": data}
	raw, _, _, err := c.DoRaw(ctx, http.MethodPost, "/image/background-remove", nil, req)
	return raw, err
}

// Upscale returns upscaled image bytes.
func (c *Client) Upscale(ctx context.Context, req *UpscaleRequest) ([]byte, error) {
	if req.Image == "" && req.ImageURL == "" {
		return nil, fmt.Errorf("venice/image: upscale requires Image or ImageURL")
	}
	raw, _, _, err := c.DoRaw(ctx, http.MethodPost, "/image/upscale", nil, req)
	return raw, err
}

// Styles lists the available style presets for image generation.
func (c *Client) Styles(ctx context.Context) ([]string, error) {
	var out struct {
		Object string   `json:"object"`
		Data   []string `json:"data"`
	}
	if err := c.DoJSON(ctx, http.MethodGet, "/image/styles", nil, nil, &out); err != nil {
		return nil, err
	}
	return out.Data, nil
}

// GenerateOpenAI runs the OpenAI-compatible generation endpoint.
func (c *Client) GenerateOpenAI(ctx context.Context, req *OpenAIRequest) (*OpenAIResponse, error) {
	var out OpenAIResponse
	if err := c.DoJSON(ctx, http.MethodPost, "/images/generations", nil, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// resolve converts a local path or URL into the base64 image field.
func (r *EditRequest) resolve() error {
	if r.Image != "" {
		return nil
	}
	if r.ImageURL != "" {
		data, err := loadImageArg(r.ImageURL)
		if err != nil {
			return err
		}
		r.Image = data
		r.ImageURL = ""
		return nil
	}
	if r.Path != "" {
		data, err := loadImageArg(r.Path)
		if err != nil {
			return err
		}
		r.Image = data
		r.Path = ""
	}
	return nil
}

// loadImageArg turns an http(s) URL, data URL, or local file path into a
// base64 data URL suitable for the API image fields.
func loadImageArg(arg string) (string, error) {
	data, err := venice.LoadImageArg(arg)
	if err != nil {
		return "", fmt.Errorf("venice/image: %w", err)
	}
	return data, nil
}

```

## `./modules/characters/characters.go`

```go
// Package characters implements the Venice Characters API: GET /characters,
// GET /characters/{slug} and GET /characters/{slug}/reviews.
package characters

import (
	"context"
	"fmt"
	"net/http"
	"net/url"

	"sunfire/modules"
)

// Character is a Venice public character.
type Character struct {
	Name        string `json:"name"`
	Slug        string `json:"slug"`
	ModelID     string `json:"modelId"`
	Description string `json:"description"`
	WebEnabled  bool   `json:"webEnabled"`
}

// ListOptions filter and page the characters list.
type ListOptions struct {
	Search   string
	Tags     string
	Category string
	Model    string
	Sort     string
	Page     int
	PageSize int
}

// Response is the characters list envelope.
type Response struct {
	Object string      `json:"object"`
	Data   []Character `json:"data"`
}

// ReviewsPage is a page of character reviews.
type ReviewsPage struct {
	Object     string   `json:"object"`
	Data       []Review `json:"data"`
	Total      int      `json:"total"`
	Page       int      `json:"page"`
	PageSize   int      `json:"pageSize"`
	TotalPages int      `json:"totalPages"`
}

// Review is a single public review of a character.
type Review struct {
	ID        string `json:"id"`
	Author    string `json:"author,omitempty"`
	Rating    int    `json:"rating,omitempty"`
	Title     string `json:"title,omitempty"`
	Content   string `json:"content,omitempty"`
	CreatedAt string `json:"createdAt,omitempty"`
}

// Client wraps a *venice.Client for the characters API.
type Client struct {
	*venice.Client
}

// NewClient wraps a *venice.Client.
func NewClient(c *venice.Client) *Client { return &Client{Client: c} }

// List returns characters, optionally filtered.
func (c *Client) List(ctx context.Context, opts *ListOptions) (*Response, error) {
	q := url.Values{}
	if opts != nil {
		if opts.Search != "" {
			q.Set("search", opts.Search)
		}
		if opts.Tags != "" {
			q.Set("tags", opts.Tags)
		}
		if opts.Category != "" {
			q.Set("category", opts.Category)
		}
		if opts.Model != "" {
			q.Set("model", opts.Model)
		}
		if opts.Sort != "" {
			q.Set("sort", opts.Sort)
		}
		if opts.Page > 0 {
			q.Set("page", fmt.Sprint(opts.Page))
		}
		if opts.PageSize > 0 {
			q.Set("pageSize", fmt.Sprint(opts.PageSize))
		}
	}
	var out Response
	if err := c.DoJSON(ctx, http.MethodGet, "/characters", q, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Get returns a single character by slug.
func (c *Client) Get(ctx context.Context, slug string) (*Character, error) {
	var out struct {
		Object string    `json:"object"`
		Data   Character `json:"data"`
	}
	if err := c.DoJSON(ctx, http.MethodGet, "/characters/"+slug, nil, nil, &out); err != nil {
		return nil, err
	}
	return &out.Data, nil
}

// Reviews returns paginated reviews for a character.
func (c *Client) Reviews(ctx context.Context, slug string, page, pageSize int) (*ReviewsPage, error) {
	q := url.Values{}
	if page > 0 {
		q.Set("page", fmt.Sprint(page))
	}
	if pageSize > 0 {
		q.Set("pageSize", fmt.Sprint(pageSize))
	}
	var out ReviewsPage
	if err := c.DoJSON(ctx, http.MethodGet, "/characters/"+slug+"/reviews", q, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

```

## `./modules/video/video.go`

```go
// Package video implements the asynchronous Venice video generation lifecycle:
// /video/quote, /video/queue, /video/retrieve and /video/complete.
package video

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"sunfire/modules"
)

// QuoteRequest estimates the USD cost of a video generation.
type QuoteRequest struct {
	Model         string         `json:"model"`
	Prompt        string         `json:"prompt,omitempty"`
	Duration      string         `json:"duration,omitempty"`     // e.g. "5s"
	Resolution    string         `json:"resolution,omitempty"`   // e.g. "720p"
	AspectRatio   string         `json:"aspect_ratio,omitempty"` // e.g. "16:9"
	Audio         *bool          `json:"audio,omitempty"`
	UpscaleFactor *int           `json:"upscale_factor,omitempty"` // topaz-video-upscale: 1, 2, 4
	Extra         map[string]any `json:"-"`
}

// MarshalJSON merges Extra fields.
func (r QuoteRequest) MarshalJSON() ([]byte, error) {
	type alias QuoteRequest
	b, err := json.Marshal(alias(r))
	if err != nil {
		return nil, err
	}
	if len(r.Extra) == 0 {
		return b, nil
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	for k, v := range r.Extra {
		m[k] = v
	}
	return json.Marshal(m)
}

// QueueRequest queues a video generation (POST /video/queue).
type QueueRequest struct {
	Model                       string         `json:"model"`
	Prompt                      string         `json:"prompt,omitempty"`
	Duration                    string         `json:"duration,omitempty"`
	Resolution                  string         `json:"resolution,omitempty"`
	AspectRatio                 string         `json:"aspect_ratio,omitempty"`
	ImageURL                    string         `json:"image_url,omitempty"`            // image-to-video
	VideoURL                    string         `json:"video_url,omitempty"`            // video-to-video / upscale
	ReferenceImageURLs          []string       `json:"reference_image_urls,omitempty"` // reference-to-video
	ReferenceVideoTotalDuration *float64       `json:"reference_video_total_duration,omitempty"`
	UpscaleFactor               *int           `json:"upscale_factor,omitempty"`
	BitrateMode                 string         `json:"bitrate_mode,omitempty"`
	Extra                       map[string]any `json:"-"`
}

// MarshalJSON merges Extra fields.
func (r QueueRequest) MarshalJSON() ([]byte, error) {
	type alias QueueRequest
	b, err := json.Marshal(alias(r))
	if err != nil {
		return nil, err
	}
	if len(r.Extra) == 0 {
		return b, nil
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	for k, v := range r.Extra {
		m[k] = v
	}
	return json.Marshal(m)
}

// QueueResponse is the result of POST /video/queue. Private models include a
// short-lived download_url that must be persisted alongside the queue_id.
type QueueResponse struct {
	Model       string `json:"model"`
	QueueID     string `json:"queue_id"`
	DownloadURL string `json:"download_url,omitempty"`
	Status      string `json:"status,omitempty"`
}

// JobRef identifies a queued video job.
type JobRef struct {
	Model   string `json:"model"`
	QueueID string `json:"queue_id"`
}

// Status is the JSON status returned while a job is processing.
type Status struct {
	Status               string `json:"status"` // PROCESSING
	AverageExecutionTime int    `json:"average_execution_time,omitempty"`
	ExecutionDuration    int    `json:"execution_duration,omitempty"`
	Error                string `json:"error,omitempty"`
}

// Result is the outcome of a retrieve call.
type Result struct {
	Data        []byte
	ContentType string
	Status      *Status
	JSON        []byte // raw body when the response was JSON
}

// Complete reports whether the retrieval yielded finished media.
func (r *Result) Complete() bool {
	return r != nil && r.Status == nil && r.Data != nil && r.ContentType != ""
}

// Client wraps a *venice.Client for video endpoints.
type Client struct {
	*venice.Client
}

// NewClient wraps a *venice.Client.
func NewClient(c *venice.Client) *Client { return &Client{Client: c} }

// Quote estimates the USD cost of a video generation.
func (c *Client) Quote(ctx context.Context, req *QuoteRequest) (float64, error) {
	var out struct {
		Quote float64 `json:"quote"`
	}
	if err := c.DoJSON(ctx, http.MethodPost, "/video/quote", nil, req, &out); err != nil {
		return 0, err
	}
	return out.Quote, nil
}

// Queue starts a video generation.
func (c *Client) Queue(ctx context.Context, req *QueueRequest) (*QueueResponse, error) {
	var out QueueResponse
	if err := c.DoJSON(ctx, http.MethodPost, "/video/queue", nil, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Retrieve polls a queued video job. Processing responses carry a Status;
// completed inline responses carry the video bytes. Models that returned a
// download_url at queue time always respond with JSON here — fetch the file
// from the download_url instead.
func (c *Client) Retrieve(ctx context.Context, ref *JobRef) (*Result, error) {
	data, ct, _, err := c.DoRaw(ctx, http.MethodPost, "/video/retrieve", nil, ref)
	if err != nil {
		return nil, err
	}
	base := strings.SplitN(ct, ";", 2)[0]
	if base != "application/json" {
		return &Result{Data: data, ContentType: ct}, nil
	}
	var st Status
	if err := json.Unmarshal(data, &st); err != nil {
		return &Result{Data: data, ContentType: ct, JSON: data}, nil
	}
	return &Result{ContentType: ct, Status: &st, JSON: data}, nil
}

// Wait polls Retrieve until the job completes or ctx is done.
func (c *Client) Wait(ctx context.Context, ref *JobRef, poll time.Duration) (*Result, error) {
	if poll <= 0 {
		poll = 5 * time.Second
	}
	for {
		res, err := c.Retrieve(ctx, ref)
		if err != nil {
			return nil, err
		}
		if res.Complete() {
			return res, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(poll):
		}
	}
}

// Complete releases provider-held media for a finished video job.
func (c *Client) Complete(ctx context.Context, ref *JobRef) error {
	return c.DoJSON(ctx, http.MethodPost, "/video/complete", nil, ref, nil)
}

// Download fetches a private download_url (pre-signed, no auth header).
func (c *Client) Download(ctx context.Context, downloadURL string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, "", venice.DecodeErrorResponse(resp)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", err
	}
	return data, resp.Header.Get("Content-Type"), nil
}

// GenerateVideo is a convenience that quotes, queues and waits for a video,
// returning the finished bytes.
func (c *Client) GenerateVideo(ctx context.Context, req *QueueRequest, poll time.Duration) ([]byte, string, error) {
	queued, err := c.Queue(ctx, req)
	if err != nil {
		return nil, "", err
	}
	ref := &JobRef{Model: queued.Model, QueueID: queued.QueueID}
	res, err := c.Wait(ctx, ref, poll)
	if err != nil {
		return nil, "", err
	}
	return res.Data, res.ContentType, nil
}

```

## `./modules/augment/augment.go`

```go
// Package augment implements Venice web-retrieval endpoints: web search
// (POST /augment/search), page scraping (POST /augment/scrape), and document
// text parsing (POST /augment/text-parser).
package augment

import (
	"context"
	"encoding/json"
	"net/http"

	"sunfire/modules"
)

// SearchRequest searches the web (POST /augment/search).
type SearchRequest struct {
	Query      string         `json:"query"`
	Provider   string         `json:"provider,omitempty"` // brave (default) | google
	MaxResults *int           `json:"max_results,omitempty"`
	Extra      map[string]any `json:"-"`
}

// MarshalJSON merges Extra fields.
func (r SearchRequest) MarshalJSON() ([]byte, error) {
	type alias SearchRequest
	b, err := json.Marshal(alias(r))
	if err != nil {
		return nil, err
	}
	if len(r.Extra) == 0 {
		return b, nil
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	for k, v := range r.Extra {
		m[k] = v
	}
	return json.Marshal(m)
}

// SearchResult is a single web search hit.
type SearchResult struct {
	Index       int    `json:"index"`
	Title       string `json:"title"`
	URL         string `json:"url"`
	Snippet     string `json:"snippet"`
	Description string `json:"description,omitempty"`
	Date        string `json:"date,omitempty"`
}

// SearchResponse is the structured result of a web search.
type SearchResponse struct {
	Object  string         `json:"object"`
	Results []SearchResult `json:"results"`
	Query   string         `json:"query,omitempty"`
	Total   int            `json:"total,omitempty"`
}

// ScrapeRequest scrapes a public URL to markdown (POST /augment/scrape).
type ScrapeRequest struct {
	URL   string         `json:"url"`
	Extra map[string]any `json:"-"`
}

// MarshalJSON merges Extra fields.
func (r ScrapeRequest) MarshalJSON() ([]byte, error) {
	type alias ScrapeRequest
	b, err := json.Marshal(alias(r))
	if err != nil {
		return nil, err
	}
	if len(r.Extra) == 0 {
		return b, nil
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	for k, v := range r.Extra {
		m[k] = v
	}
	return json.Marshal(m)
}

// TextParserRequest extracts text from an uploaded document (up to 25MB).
// Supported: PDF, EPUB, DOCX, PPTX, XLSX, XLS, plain text, markdown, CSV,
// JSON, YAML, TOML, XML, source code.
type TextParserRequest struct {
	File           venice.MultipartFile
	ResponseFormat string // json (default) or text
}

// TextParserResult is the structured text-parser response.
type TextParserResult struct {
	Text    string `json:"text"`
	Tokens  *int   `json:"token_count,omitempty"`
	RawText string `json:"raw_text,omitempty"`
	URL     string `json:"url,omitempty"`
}

// Client wraps a *venice.Client for web-retrieval endpoints.
type Client struct {
	*venice.Client
}

// NewClient wraps a *venice.Client.
func NewClient(c *venice.Client) *Client { return &Client{Client: c} }

// Search runs a web search and returns structured results.
func (c *Client) Search(ctx context.Context, req *SearchRequest) (*SearchResponse, error) {
	var out SearchResponse
	if err := c.DoJSON(ctx, http.MethodPost, "/augment/search", nil, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// SearchRaw returns the raw JSON body for schema evolution.
func (c *Client) SearchRaw(ctx context.Context, req *SearchRequest) ([]byte, error) {
	data, _, _, err := c.DoRaw(ctx, http.MethodPost, "/augment/search", nil, req)
	return data, err
}

// Scrape returns a web page's content as markdown.
func (c *Client) Scrape(ctx context.Context, url string) (string, error) {
	req := &ScrapeRequest{URL: url}
	var out struct {
		Content  string `json:"content"`
		Markdown string `json:"markdown"`
		Scrape   string `json:"scrape"`
	}
	if err := c.DoJSON(ctx, http.MethodPost, "/augment/scrape", nil, req, &out); err != nil {
		return "", err
	}
	switch {
	case out.Content != "":
		return out.Content, nil
	case out.Markdown != "":
		return out.Markdown, nil
	default:
		return out.Scrape, nil
	}
}

// TextParser extracts text from an uploaded document file.
func (c *Client) TextParser(ctx context.Context, req *TextParserRequest) (*TextParserResult, error) {
	if req.File.Name == "" {
		req.File.Name = "document.bin"
	}
	fields := map[string]string{}
	if req.ResponseFormat != "" {
		fields["response_format"] = req.ResponseFormat
	}
	body, ct, err := venice.BuildMultipart(fields, map[string]venice.MultipartFile{"file": req.File})
	if err != nil {
		return nil, err
	}
	resp, err := c.Send(ctx, http.MethodPost, "/augment/text-parser", nil, ct, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, venice.DecodeErrorResponse(resp)
	}
	var out TextParserResult
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

```

## `./modules/audio/audio.go`

```go
// Package audio implements Venice audio endpoints: text-to-speech
// (POST /audio/speech), speech-to-text (POST /audio/transcriptions), and the
// asynchronous music / sound-effect generation lifecycle
// (/audio/quote, /audio/queue, /audio/retrieve, /audio/complete).
package audio

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"sunfire/modules"
)

// SpeechRequest is a text-to-speech request (POST /audio/speech).
type SpeechRequest struct {
	Model          string         `json:"model"`
	Input          string         `json:"input"`
	Voice          string         `json:"voice,omitempty"`
	ResponseFormat string         `json:"response_format,omitempty"` // mp3, opus, aac, flac, wav, pcm
	Speed          *float64       `json:"speed,omitempty"`
	Extra          map[string]any `json:"-"`
}

// MarshalJSON merges Extra fields.
func (r SpeechRequest) MarshalJSON() ([]byte, error) {
	type alias SpeechRequest
	b, err := json.Marshal(alias(r))
	if err != nil {
		return nil, err
	}
	if len(r.Extra) == 0 {
		return b, nil
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	for k, v := range r.Extra {
		m[k] = v
	}
	return json.Marshal(m)
}

// TranscriptionRequest is a speech-to-text request (POST /audio/transcriptions).
// The audio is uploaded as multipart form data.
type TranscriptionRequest struct {
	Model          string
	File           venice.MultipartFile
	ResponseFormat string // json (default) or text
	Timestamps     bool
	Language       string
	Prompt         string
	Temperature    *float64
	Extra          map[string]string // extra form fields
}

// Transcription is the structured transcription result (response_format=json).
type Transcription struct {
	Text       string                `json:"text"`
	Duration   float64               `json:"duration,omitempty"`
	Timestamps *TranscriptionTimings `json:"timestamps,omitempty"`
}

// TranscriptionTimings holds word- or segment-level timing data.
type TranscriptionTimings struct {
	Word    []WordTiming    `json:"word,omitempty"`
	Segment []SegmentTiming `json:"segment,omitempty"`
}

// WordTiming is a word-level timestamp.
type WordTiming struct {
	Word  string  `json:"word"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}

// SegmentTiming is a segment-level timestamp.
type SegmentTiming struct {
	Text  string  `json:"text"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}

// QuoteRequest is a music / sound-effect pricing request (POST /audio/quote).
type QuoteRequest struct {
	Model           string         `json:"model"`
	DurationSeconds *int           `json:"duration_seconds,omitempty"`
	Extra           map[string]any `json:"-"`
}

// MarshalJSON merges Extra fields.
func (r QuoteRequest) MarshalJSON() ([]byte, error) {
	type alias QuoteRequest
	b, err := json.Marshal(alias(r))
	if err != nil {
		return nil, err
	}
	if len(r.Extra) == 0 {
		return b, nil
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	for k, v := range r.Extra {
		m[k] = v
	}
	return json.Marshal(m)
}

// QueueRequest queues a music or sound-effect generation (POST /audio/queue).
type QueueRequest struct {
	Model             string         `json:"model"`
	Prompt            string         `json:"prompt,omitempty"`
	DurationSeconds   *int           `json:"duration_seconds,omitempty"`
	ForceInstrumental *bool          `json:"force_instrumental,omitempty"`
	LyricsPrompt      string         `json:"lyrics_prompt,omitempty"`
	Loop              *bool          `json:"loop,omitempty"`
	Extra             map[string]any `json:"-"`
}

// MarshalJSON merges Extra fields.
func (r QueueRequest) MarshalJSON() ([]byte, error) {
	type alias QueueRequest
	b, err := json.Marshal(alias(r))
	if err != nil {
		return nil, err
	}
	if len(r.Extra) == 0 {
		return b, nil
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	for k, v := range r.Extra {
		m[k] = v
	}
	return json.Marshal(m)
}

// QueueResponse is the result of POST /audio/queue.
type QueueResponse struct {
	Model   string `json:"model"`
	QueueID string `json:"queue_id"`
	Status  string `json:"status"`
}

// JobRef identifies a queued job for retrieve/complete calls.
type JobRef struct {
	Model   string `json:"model"`
	QueueID string `json:"queue_id"`
}

// Status is the JSON status returned while a job is still processing.
type Status struct {
	Status               string `json:"status"` // PROCESSING
	AverageExecutionTime int    `json:"average_execution_time,omitempty"`
	ExecutionDuration    int    `json:"execution_duration,omitempty"`
	CreditsRefunded      *bool  `json:"credits_refunded,omitempty"`
	Error                string `json:"error,omitempty"`
}

// Result is the outcome of a retrieve call: either a completed binary payload
// or a Status. ContentType is the response Content-Type.
type Result struct {
	Data        []byte
	ContentType string
	Status      *Status
}

// Complete reports whether the retrieval yielded finished media (audio bytes
// rather than a PROCESSING status).
func (r *Result) Complete() bool {
	return r != nil && r.Data != nil && r.ContentType != "" && r.Status == nil
}

// Client wraps a *venice.Client for audio endpoints.
type Client struct {
	*venice.Client
}

// NewClient wraps a *venice.Client.
func NewClient(c *venice.Client) *Client { return &Client{Client: c} }

// Speech synthesizes text to audio and returns the raw bytes plus the response
// Content-Type (e.g. audio/mpeg).
func (c *Client) Speech(ctx context.Context, req *SpeechRequest) ([]byte, string, error) {
	data, ct, _, err := c.DoRaw(ctx, http.MethodPost, "/audio/speech", nil, req)
	return data, ct, err
}

// SpeechToFile synthesizes speech and writes the bytes to path. It infers a
// file extension from the response Content-Type when path has no extension.
// Returns the path actually written.
func (c *Client) SpeechToFile(ctx context.Context, req *SpeechRequest, path string) (string, error) {
	data, ct, err := c.Speech(ctx, req)
	if err != nil {
		return "", err
	}
	return writeMedia(path, ct, data)
}

// Transcribe uploads audio and returns the transcription. When
// req.ResponseFormat is "text" the returned Transcription.Text holds the raw
// text and the other fields are empty.
func (c *Client) Transcribe(ctx context.Context, req *TranscriptionRequest) (*Transcription, error) {
	if req.File.Name == "" {
		return nil, fmt.Errorf("venice/audio: transcription requires a file with a name")
	}
	fields := map[string]string{
		"model": req.Model,
	}
	if req.ResponseFormat != "" {
		fields["response_format"] = req.ResponseFormat
	}
	if req.Timestamps {
		fields["timestamps"] = "true"
	}
	if req.Language != "" {
		fields["language"] = req.Language
	}
	if req.Prompt != "" {
		fields["prompt"] = req.Prompt
	}
	if req.Temperature != nil {
		fields["temperature"] = fmt.Sprintf("%g", *req.Temperature)
	}
	for k, v := range req.Extra {
		fields[k] = v
	}
	body, ct, err := venice.BuildMultipart(fields, map[string]venice.MultipartFile{"file": req.File})
	if err != nil {
		return nil, err
	}
	resp, err := c.Send(ctx, http.MethodPost, "/audio/transcriptions", nil, ct, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, venice.DecodeErrorResponse(resp)
	}
	var out Transcription
	if req.ResponseFormat == "text" {
		raw, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, err
		}
		out.Text = strings.TrimSpace(string(raw))
		return &out, nil
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Quote estimates the USD cost of a music / sound-effect generation.
func (c *Client) Quote(ctx context.Context, req *QuoteRequest) (float64, error) {
	var out struct {
		Quote float64 `json:"quote"`
	}
	if err := c.DoJSON(ctx, http.MethodPost, "/audio/quote", nil, req, &out); err != nil {
		return 0, err
	}
	return out.Quote, nil
}

// Queue starts a music or sound-effect generation.
func (c *Client) Queue(ctx context.Context, req *QueueRequest) (*QueueResponse, error) {
	var out QueueResponse
	if err := c.DoJSON(ctx, http.MethodPost, "/audio/queue", nil, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Retrieve polls a queued audio job. When the job is still processing the
// returned Result carries a Status; when it is finished Result.Data holds the
// audio bytes.
func (c *Client) Retrieve(ctx context.Context, ref *JobRef) (*Result, error) {
	data, ct, _, err := c.DoRaw(ctx, http.MethodPost, "/audio/retrieve", nil, ref)
	if err != nil {
		return nil, err
	}
	base := strings.SplitN(ct, ";", 2)[0]
	if base != "application/json" {
		return &Result{Data: data, ContentType: ct}, nil
	}
	var st Status
	if err := json.Unmarshal(data, &st); err != nil {
		return &Result{Data: data, ContentType: ct}, nil
	}
	return &Result{ContentType: ct, Status: &st}, nil
}

// Wait polls Retrieve until the job completes or ctx is done. poll is the
// delay between polls (a non-positive value uses 5 seconds).
func (c *Client) Wait(ctx context.Context, ref *JobRef, poll time.Duration) (*Result, error) {
	if poll <= 0 {
		poll = 5 * time.Second
	}
	for {
		res, err := c.Retrieve(ctx, ref)
		if err != nil {
			return nil, err
		}
		if res.Complete() {
			return res, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(poll):
		}
	}
}

// Complete releases provider-held media for a finished audio job.
func (c *Client) Complete(ctx context.Context, ref *JobRef) error {
	return c.DoJSON(ctx, http.MethodPost, "/audio/complete", nil, ref, nil)
}

// GenerateMusic is a convenience that quotes, queues and waits for a music /
// sound-effect generation, returning the finished audio bytes.
func (c *Client) GenerateMusic(ctx context.Context, req *QueueRequest, poll time.Duration) ([]byte, string, error) {
	queued, err := c.Queue(ctx, req)
	if err != nil {
		return nil, "", err
	}
	ref := &JobRef{Model: queued.Model, QueueID: queued.QueueID}
	res, err := c.Wait(ctx, ref, poll)
	if err != nil {
		return nil, "", err
	}
	return res.Data, res.ContentType, nil
}

// writeMedia writes data to path, appending an extension derived from the
// Content-Type when path has none. Returns the final path.
func writeMedia(path, contentType string, data []byte) (string, error) {
	if filepath.Ext(path) == "" {
		ext := extensionFor(contentType)
		path += ext
	}
	if err := venice.WriteFile(path, data); err != nil {
		return "", err
	}
	return path, nil
}

func extensionFor(contentType string) string {
	switch strings.ToLower(strings.SplitN(contentType, ";", 2)[0]) {
	case "audio/mpeg", "audio/mp3":
		return ".mp3"
	case "audio/wav", "audio/x-wav":
		return ".wav"
	case "audio/flac":
		return ".flac"
	case "audio/ogg":
		return ".ogg"
	case "audio/aac":
		return ".aac"
	case "audio/mp4":
		return ".m4a"
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "video/mp4":
		return ".mp4"
	case "audio/pcm":
		return ".pcm"
	default:
		return ".bin"
	}
}

```

## `./modules/billing/billing.go`

```go
// Package billing implements Venice billing endpoints: GET /billing/balance,
// GET /billing/usage-analytics, and the cursor-paginated GET /billing/usage-history.
// (The legacy GET /billing/usage endpoint is sunset and returns 410.)
package billing

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"sunfire/modules"
)

// Balance is the account balance from GET /billing/balance.
type Balance struct {
	CanConsume          bool      `json:"canConsume"`
	ConsumptionCurrency string    `json:"consumptionCurrency"`
	Balances            *Balances `json:"balances,omitempty"`
	DiemEpochAllocation *float64  `json:"diemEpochAllocation,omitempty"`
	BalanceUSD          *float64  `json:"balanceUsd,omitempty"`
	BalanceDIEM         *float64  `json:"balanceDiem,omitempty"`
}

// Balances holds the per-currency balances.
type Balances struct {
	DIEM *float64 `json:"diem,omitempty"`
	USD  *float64 `json:"usd,omitempty"`
}

// UsageAnalytics is the aggregated usage from GET /billing/usage-analytics.
type UsageAnalytics struct {
	Lookback     string           `json:"lookback"`
	ByDate       []DailyUsage     `json:"byDate,omitempty"`
	ByModel      []ModelUsage     `json:"byModel,omitempty"`
	ByModelDaily []map[string]any `json:"byModelDaily,omitempty"`
	TopModels    []string         `json:"topModels,omitempty"`
	ByKey        []KeyUsage       `json:"byKey,omitempty"`
	ByKeyDaily   []map[string]any `json:"byKeyDaily,omitempty"`
	TopKeyNames  []string         `json:"topKeyNames,omitempty"`
}

// DailyUsage is one day's spend.
type DailyUsage struct {
	Date string  `json:"date"`
	USD  float64 `json:"USD"`
	DIEM float64 `json:"DIEM"`
}

// ModelUsage is per-model usage.
type ModelUsage struct {
	ModelName  string           `json:"modelName"`
	UnitType   string           `json:"unitType"`
	ModelType  string           `json:"modelType"`
	TotalUSD   float64          `json:"totalUsd"`
	TotalDIEM  float64          `json:"totalDiem"`
	TotalUnits float64          `json:"totalUnits"`
	Breakdown  []UsageBreakdown `json:"breakdown,omitempty"`
}

// UsageBreakdown is a per-token-type breakdown of model usage.
type UsageBreakdown struct {
	Type  string  `json:"type"`
	USD   float64 `json:"usd"`
	DIEM  float64 `json:"diem"`
	Units float64 `json:"units"`
}

// KeyUsage is per-API-key usage.
type KeyUsage struct {
	APIKeyID    string  `json:"apiKeyId"`
	Description string  `json:"description"`
	TotalUSD    float64 `json:"totalUsd"`
	TotalDIEM   float64 `json:"totalDiem"`
	TotalUnits  float64 `json:"totalUnits"`
}

// UsageHistoryOptions filter the usage-history walk.
type UsageHistoryOptions struct {
	StartTimestamp int64 // unix seconds
	EndTimestamp   int64
	PageSize       int
	// CSV requests a text/csv page instead of JSON.
	CSV bool
}

// UsageHistoryPage is one cursor-paginated page of usage entries.
type UsageHistoryPage struct {
	Entries    []UsageEntry `json:"entries"`
	NextCursor string       `json:"nextCursor"`
	HasMore    bool         `json:"hasMore"`
	CSV        string       `json:"-"` // populated when CSV was requested
}

// UsageEntry is a single billing ledger entry.
type UsageEntry struct {
	Timestamp        time.Time         `json:"timestamp"`
	SKU              string            `json:"sku"`
	PricePerUnitUSD  float64           `json:"pricePerUnitUsd"`
	Unit             float64           `json:"unit"`
	Amount           float64           `json:"amount"`
	Currency         string            `json:"currency"`
	Notes            string            `json:"notes,omitempty"`
	InferenceDetails *InferenceDetails `json:"inferenceDetails,omitempty"`
}

// InferenceDetails describes the inference behind a usage entry.
type InferenceDetails struct {
	RequestID              string `json:"requestId,omitempty"`
	InferenceExecutionTime int64  `json:"inferenceExecutionTime,omitempty"`
	PromptTokens           int    `json:"promptTokens,omitempty"`
	CompletionTokens       int    `json:"completionTokens,omitempty"`
}

// Client wraps a *venice.Client for billing endpoints.
type Client struct {
	*venice.Client
}

// NewClient wraps a *venice.Client.
func NewClient(c *venice.Client) *Client { return &Client{Client: c} }

// Balance returns the current account balance.
func (c *Client) Balance(ctx context.Context) (*Balance, error) {
	var out Balance
	if err := c.DoJSON(ctx, http.MethodGet, "/billing/balance", nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// UsageAnalytics returns aggregated usage. An empty opts uses the 7-day
// default lookback.
func (c *Client) UsageAnalytics(ctx context.Context, lookback string, startDate, endDate string) (*UsageAnalytics, error) {
	q := url.Values{}
	if lookback != "" {
		q.Set("lookback", lookback)
	}
	if startDate != "" {
		q.Set("startDate", startDate)
	}
	if endDate != "" {
		q.Set("endDate", endDate)
	}
	var out UsageAnalytics
	if err := c.DoJSON(ctx, http.MethodGet, "/billing/usage-analytics", q, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// UsageHistory fetches one page of the usage-history walk. Pass the
// nextCursor from a previous page to continue; filters travel inside the
// cursor and must be omitted on continuation requests.
func (c *Client) UsageHistory(ctx context.Context, opts *UsageHistoryOptions, nextCursor string) (*UsageHistoryPage, error) {
	q := url.Values{}
	if nextCursor != "" {
		q.Set("cursor", nextCursor)
	} else if opts != nil {
		if opts.StartTimestamp > 0 {
			q.Set("startTimestamp", strconv.FormatInt(opts.StartTimestamp, 10))
		}
		if opts.EndTimestamp > 0 {
			q.Set("endTimestamp", strconv.FormatInt(opts.EndTimestamp, 10))
		}
		if opts.PageSize > 0 {
			q.Set("pageSize", strconv.Itoa(opts.PageSize))
		}
	}
	accept := "application/json"
	if opts != nil && opts.CSV && nextCursor == "" {
		accept = "text/csv"
	}
	resp, err := c.Send(ctx, http.MethodGet, "/billing/usage-history", q, "", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, venice.DecodeErrorResponse(resp)
	}
	// When CSV was requested the body is CSV regardless of the Accept header
	// echoed back; detect by header Content-Type first, then by Accept.
	isCSV := false
	ct := resp.Header.Get("Content-Type")
	if len(ct) >= 9 && ct[:9] == "text/csv" {
		isCSV = true
	} else if accept == "text/csv" {
		isCSV = true
	}
	page := &UsageHistoryPage{}
	if isCSV {
		sb, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, err
		}
		page.CSV = string(sb)
		return page, nil
	}
	if err := json.NewDecoder(resp.Body).Decode(page); err != nil {
		return nil, err
	}
	return page, nil
}

// WalkUsageHistory iterates the entire usage history via its cursor.
// onPage is called for each page; return false to stop early.
func (c *Client) WalkUsageHistory(ctx context.Context, opts *UsageHistoryOptions, onPage func(*UsageHistoryPage) (bool, error)) error {
	cursor := ""
	for {
		page, err := c.UsageHistory(ctx, opts, cursor)
		if err != nil {
			return err
		}
		cont, err := onPage(page)
		if err != nil {
			return err
		}
		if !cont || !page.HasMore || page.NextCursor == "" {
			return nil
		}
		cursor = page.NextCursor
		opts = nil // filters travel inside the cursor on continuation
	}
}

```

## `./modules/chat/chat.go`

```go
// Package chat implements the Venice Chat Completions API
// (POST /chat/completions), an OpenAI-compatible endpoint extended with
// Venice-specific parameters: web search/citations, character personas,
// thinking controls, and streaming.
package chat

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"sunfire/modules"
)

// Message is a single chat message. Content is either a plain string or a
// []Part slice for multimodal content.
type Message struct {
	Role    string `json:"role"` // system, user, assistant, tool
	Content any    `json:"content"`
}

// Part is a multimodal content part (text, image_url, input_audio,
// video_url, or file).
type Part struct {
	Type       string         `json:"type"`
	Text       string         `json:"text,omitempty"`
	ImageURL   *ImageURL      `json:"image_url,omitempty"`
	InputAudio *InputAudio    `json:"input_audio,omitempty"`
	VideoURL   *VideoURL      `json:"video_url,omitempty"`
	File       *FileRef       `json:"file,omitempty"`
	Refusal    string         `json:"refusal,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
	ToolCalls  []ToolCall     `json:"tool_calls,omitempty"`
	Name       string         `json:"name,omitempty"`
	Extra      map[string]any `json:"-"`
}

// ImageURL references an image for multimodal input.
type ImageURL struct {
	URL string `json:"url,omitempty"` // http(s) URL or data: URL
}

// InputAudio references audio bytes for multimodal input.
type InputAudio struct {
	Data   string `json:"data,omitempty"`   // base64
	Format string `json:"format,omitempty"` // wav, mp3, ...
}

// VideoURL references a video for multimodal input.
type VideoURL struct {
	URL string `json:"url,omitempty"`
}

// FileRef references an uploaded file (PDF, DOCX, ...) for multimodal input.
type FileRef struct {
	URL string `json:"url,omitempty"` // file_id:... or data URL
}

// ToolCall is an assistant tool invocation.
type ToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function ToolCallFunction `json:"function"`
}

// ToolCallFunction is the name/arguments of a tool call.
type ToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// Tool is a function declaration for function calling.
type Tool struct {
	Type     string `json:"type"` // "function"
	Function any    `json:"function"`
}

// ResponseFormat requests structured JSON output (json_object or json_schema).
type ResponseFormat struct {
	Type       string `json:"type"`
	JSONSchema any    `json:"json_schema,omitempty"`
}

// VeniceParameters carries the Venice-specific chat options.
type VeniceParameters struct {
	CharacterSlug                  string `json:"character_slug,omitempty"`
	StripThinkingResponse          bool   `json:"strip_thinking_response,omitempty"`
	DisableThinking                bool   `json:"disable_thinking,omitempty"`
	EnableWebSearch                string `json:"enable_web_search,omitempty"` // off | on | auto
	EnableWebScraping              bool   `json:"enable_web_scraping,omitempty"`
	EnableXSearch                  bool   `json:"enable_x_search,omitempty"`
	EnableWebCitations             bool   `json:"enable_web_citations,omitempty"`
	IncludeSearchResultsInStream   bool   `json:"include_search_results_in_stream,omitempty"`
	ReturnSearchResultsAsDocuments bool   `json:"return_search_results_as_documents,omitempty"`
	IncludeVeniceSystemPrompt      *bool  `json:"include_venice_system_prompt,omitempty"`
}

// StreamOptions is the OpenAI-compatible streaming options object.
type StreamOptions struct {
	IncludeUsage bool `json:"include_usage,omitempty"`
}

// Request is a chat completion request.
type Request struct {
	Model               string            `json:"model"`
	Messages            []Message         `json:"messages"`
	MaxTokens           *int              `json:"max_tokens,omitempty"`
	MaxCompletionTokens *int              `json:"max_completion_tokens,omitempty"`
	Temperature         *float64          `json:"temperature,omitempty"`
	TopP                *float64          `json:"top_p,omitempty"`
	FrequencyPenalty    *float64          `json:"frequency_penalty,omitempty"`
	PresencePenalty     *float64          `json:"presence_penalty,omitempty"`
	Stop                []string          `json:"stop,omitempty"`
	Stream              bool              `json:"stream,omitempty"`
	StreamOptions       *StreamOptions    `json:"stream_options,omitempty"`
	Tools               []Tool            `json:"tools,omitempty"`
	ToolChoice          any               `json:"tool_choice,omitempty"`
	ResponseFormat      *ResponseFormat   `json:"response_format,omitempty"`
	Seed                *int              `json:"seed,omitempty"`
	User                string            `json:"user,omitempty"`
	PromptCacheKey      string            `json:"prompt_cache_key,omitempty"`
	VeniceParameters    *VeniceParameters `json:"venice_parameters,omitempty"`
	Extra               map[string]any    `json:"-"`
}

// MarshalJSON merges Extra fields into the standard request body.
func (r Request) MarshalJSON() ([]byte, error) {
	type alias Request
	b, err := json.Marshal(alias(r))
	if err != nil {
		return nil, err
	}
	if len(r.Extra) == 0 {
		return b, nil
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	for k, v := range r.Extra {
		m[k] = v
	}
	return json.Marshal(m)
}

// Usage reports token consumption.
type Usage struct {
	PromptTokens            int          `json:"prompt_tokens"`
	CompletionTokens        int          `json:"completion_tokens"`
	TotalTokens             int          `json:"total_tokens"`
	PromptTokensDetails     *UsageDetail `json:"prompt_tokens_details,omitempty"`
	CompletionTokensDetails *UsageDetail `json:"completion_tokens_details,omitempty"`
}

// UsageDetail breaks usage into cached/reasoning components.
type UsageDetail struct {
	CachedTokens    int `json:"cached_tokens,omitempty"`
	ReasoningTokens int `json:"reasoning_tokens,omitempty"`
}

// Citation is a web-search citation returned in the response.
type Citation struct {
	Index int    `json:"index"`
	URL   string `json:"url"`
}

// Choice is one generated completion.
type Choice struct {
	Index        int     `json:"index"`
	Message      Message `json:"message"`
	FinishReason string  `json:"finish_reason"`
	Delta        Message `json:"delta,omitempty"`
	Logprobs     any     `json:"logprobs,omitempty"`
}

// Response is a non-streaming chat completion.
type Response struct {
	ID      string          `json:"id"`
	Object  string          `json:"object"`
	Created int64           `json:"created"`
	Model   string          `json:"model"`
	Choices []Choice        `json:"choices"`
	Usage   *Usage          `json:"usage,omitempty"`
	Venice  *VeniceResponse `json:"venice,omitempty"`
}

// VeniceResponse carries Venice-specific response metadata.
type VeniceResponse struct {
	SearchResults []SearchResult `json:"search_results,omitempty"`
}

// SearchResult is a web search result surfaced by the chat endpoint.
type SearchResult struct {
	Index   int    `json:"index"`
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
}

// Client is a chat-completions client bound to a Venice API key.
type Client struct {
	*venice.Client
}

// NewClient wraps a *venice.Client for the chat API.
func NewClient(c *venice.Client) *Client { return &Client{Client: c} }

// Create sends a chat completion request and returns the decoded response.
func (c *Client) Create(ctx context.Context, req *Request) (*Response, error) {
	req.Stream = false
	var out Response
	if err := c.DoJSON(ctx, http.MethodPost, "/chat/completions", nil, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CreateRaw sends a request and returns the raw JSON body, for callers that
// want to decode an evolving schema themselves.
func (c *Client) CreateRaw(ctx context.Context, req *Request) ([]byte, error) {
	data, _, _, err := c.DoRaw(ctx, http.MethodPost, "/chat/completions", nil, req)
	return data, err
}

// Stream iterates an SSE stream of chat completion chunks. It stops on
// [io.EOF] after the final [DONE] marker. Each chunk is delivered via next.
// The iterator must be closed to release the underlying connection.
func (c *Client) Stream(ctx context.Context, req *Request) (*StreamIterator, error) {
	req.Stream = true
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	resp, err := c.Send(ctx, http.MethodPost, "/chat/completions", nil, "application/json", body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		defer resp.Body.Close()
		return nil, c.decodeErrorResponse(resp)
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	return &StreamIterator{resp: resp, sc: sc}, nil
}

// decodeErrorResponse reads an error body and returns an error.
func (c *Client) decodeErrorResponse(resp *http.Response) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return &venice.APIError{StatusCode: resp.StatusCode, Status: resp.Status, Raw: raw, Message: strings.TrimSpace(string(raw))}
}

// StreamIterator walks an SSE response from the chat completions endpoint.
type StreamIterator struct {
	resp *http.Response
	sc   *bufio.Scanner
	done bool
}

// Next decodes the next chunk. Returns io.EOF when the stream ends.
func (it *StreamIterator) Next() (*Response, error) {
	if it.done {
		return nil, io.EOF
	}
	for it.sc.Scan() {
		line := strings.TrimSpace(it.sc.Text())
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue // SSE comment / keepalive
		}
		payload := line
		if strings.HasPrefix(line, "data:") {
			payload = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		}
		if payload == "[DONE]" {
			it.done = true
			_ = it.Close()
			return nil, io.EOF
		}
		var chunk Response
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			return nil, fmt.Errorf("venice: decode stream chunk: %w", err)
		}
		if len(chunk.Choices) == 0 && chunk.ID == "" && chunk.Usage == nil {
			continue
		}
		return &chunk, nil
	}
	if err := it.sc.Err(); err != nil {
		it.done = true
		_ = it.Close()
		return nil, err
	}
	it.done = true
	_ = it.Close()
	return nil, io.EOF
}

// Err returns any scanner error encountered (nil until the stream ends).
func (it *StreamIterator) Err() error { return it.sc.Err() }

// Close releases the underlying HTTP connection.
func (it *StreamIterator) Close() error {
	if it.resp != nil {
		err := it.resp.Body.Close()
		it.resp = nil
		return err
	}
	return nil
}

// Accumulate reads an entire stream into a single response by merging the
// deltas of each chunk. If includeUsage is false (or unset) usage is nil.
func (it *StreamIterator) Accumulate() (*Response, error) {
	var out Response
	var sb strings.Builder
	var err error
	var chunk *Response
	for {
		chunk, err = it.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if out.ID == "" {
			out.ID, out.Object, out.Created, out.Model = chunk.ID, chunk.Object, chunk.Created, chunk.Model
		}
		if chunk.Usage != nil {
			out.Usage = chunk.Usage
		}
		if chunk.Venice != nil {
			out.Venice = chunk.Venice
		}
		for _, ch := range chunk.Choices {
			if s, ok := ch.Delta.Content.(string); ok && s != "" {
				sb.WriteString(s)
			}
			out.Choices = append(out.Choices, ch)
		}
	}
	out.Object = "chat.completion"
	if out.Choices == nil {
		out.Choices = []Choice{{Message: Message{Role: "assistant", Content: sb.String()}}}
	} else if sb.Len() > 0 {
		out.Choices[0].Message = Message{Role: "assistant", Content: sb.String()}
		out.Choices[0].FinishReason = "stop"
	}
	return &out, nil
}

// Text returns the first choice's text content, trimming leading/trailing
// whitespace. Returns "" if there is no content.
func (r *Response) Text() string {
	if r == nil || len(r.Choices) == 0 {
		return ""
	}
	if s, ok := r.Choices[0].Message.Content.(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

```

## `./modules/embeddings/embeddings.go`

```go
// Package embeddings implements Venice embedding generation:
// POST /embeddings.
package embeddings

import (
	"context"
	"net/http"

	"sunfire/modules"
)

// Request is the body for POST /embeddings/generate.
type Request struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

// Data is a single embedding vector for one input string.
type Data struct {
	Object    string    `json:"object"`
	Index     int       `json:"index"`
	Embedding []float64 `json:"embedding"`
}

// Usage reports token usage for the request.
type Usage struct {
	PromptTokens int `json:"prompt_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

// Response is the parsed response from /embeddings/generate.
type Response struct {
	Object string `json:"object"`
	Data   []Data `json:"data"`
	Model  string `json:"model"`
	Usage  Usage  `json:"usage"`
}

// Client wraps a *venice.Client for embedding endpoints.
type Client struct {
	*venice.Client
}

// NewClient wraps a *venice.Client.
func NewClient(c *venice.Client) *Client { return &Client{Client: c} }

// Generate computes embedding vectors for each input string.
func (c *Client) Generate(ctx context.Context, req *Request) (*Response, error) {
	if req.Model == "" {
		return nil, errNoModel()
	}
	if len(req.Input) == 0 {
		return nil, errNoInput()
	}
	var out Response
	if err := c.DoJSON(ctx, http.MethodPost, "/embeddings", nil, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GenerateOne is a convenience for a single input string.
func (c *Client) GenerateOne(ctx context.Context, model, input string) ([]float64, error) {
	out, err := c.Generate(ctx, &Request{Model: model, Input: []string{input}})
	if err != nil {
		return nil, err
	}
	if len(out.Data) == 0 {
		return nil, errNoData()
	}
	return out.Data[0].Embedding, nil
}

func errNoModel() error {
	return &errUsage{"venice/embeddings: model is required"}
}
func errNoInput() error {
	return &errUsage{"venice/embeddings: at least one input string is required"}
}
func errNoData() error {
	return &errUsage{"venice/embeddings: no embedding data in response"}
}

type errUsage struct{ msg string }

func (e *errUsage) Error() string { return e.msg }

```

## `./providers/client.go`

```go
// client.go — shared HTTP plumbing for the provider clients.
package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"
)

const userAgent = "ElyLightspeed/1.0"

// apiClient wraps an http.Client plus the provider's base URL + auth.
type apiClient struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

func newAPIClient(baseURL, apiKey string) *apiClient {
	return &apiClient{
		baseURL: strings.TrimSuffix(baseURL, "/"),
		apiKey:  apiKey,
		http:    &http.Client{Timeout: 180 * time.Second},
	}
}

// doJSON performs an HTTP request and decodes a JSON response. Returns a
// non-nil error on transport errors or non-2xx HTTP status.
func (c *apiClient) doJSON(ctx context.Context, method, path string, body interface{}) (json.RawMessage, error) {
	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		bodyReader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bodyReader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d from %s%s: %s", resp.StatusCode, c.baseURL, path, strings.TrimSpace(string(data)))
	}
	return data, nil
}

// doMultipart performs a multipart/form-data POST (for STT uploads).
func (c *apiClient) doMultipart(ctx context.Context, path string, fields map[string]string, fileField, fileName string, fileData []byte) (json.RawMessage, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	// Add non-file fields first (Cohere requires fields before the file part).
	for k, v := range fields {
		if k == fileField {
			continue
		}
		_ = w.WriteField(k, v)
	}
	fw, err := w.CreateFormFile(fileField, fileName)
	if err != nil {
		return nil, err
	}
	if _, err := fw.Write(fileData); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, &buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", w.FormDataContentType())
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d from %s%s: %s", resp.StatusCode, c.baseURL, path, strings.TrimSpace(string(data)))
	}
	return data, nil
}

// doRaw performs a POST returning raw bytes (for TTS audio). On non-2xx it
// returns the body as part of the error for readability.
func (c *apiClient) doRaw(ctx context.Context, method, path string, body interface{}) ([]byte, string, error) {
	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, "", err
		}
		bodyReader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bodyReader)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 25<<20))
	if err != nil {
		return nil, "", err
	}
	ct := resp.Header.Get("Content-Type")
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("HTTP %d from %s%s: %s", resp.StatusCode, c.baseURL, path, strings.TrimSpace(string(data)))
	}
	return data, ct, nil
}

// jsonUnmarshal decodes raw JSON into out, returning a friendly error.
func jsonUnmarshal(raw json.RawMessage, out interface{}) error {
	if len(raw) == 0 {
		return fmt.Errorf("empty response body")
	}
	return json.Unmarshal(raw, out)
}

```

## `./providers/config.go`

```go
// config.go — multi-provider config loader for the /cohere-*, /cloudflare-*,
// /groq-*, /mistral-*, /nvidia-*, /openrouter-* commands.
//
// Reads the same repo-root .private/config.json used by the rest of the bot.
// The provider credentials were merged from ElyProjectX's .private/config.json:
// each is a nested object { apiKey, ... } (cloudflare also has accountId).
package providers

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
)

// ProviderConfig holds one provider's credentials block.
type ProviderConfig struct {
	APIKey    string `json:"apiKey"`
	AccountID string `json:"accountId"` // cloudflare only
}

// Config holds the provider credentials for all six services.
type Config struct {
	Cohere     ProviderConfig `json:"cohere"`
	Cloudflare ProviderConfig `json:"cloudflare"`
	Groq       ProviderConfig `json:"groq"`
	Mistral    ProviderConfig `json:"mistral"`
	Nvidia     ProviderConfig `json:"nvidia"`
	OpenRouter ProviderConfig `json:"openrouter"`
}

// cfg is the package-level config, initialized by Init.
var cfg *Config

// loadConfig reads the repo-root .private/config.json (relative to this file).
func loadConfig() (*Config, error) {
	_, file, _, ok := runtime.Caller(0)
	path := ""
	if ok {
		path = filepath.Join(filepath.Dir(file), "..", ".private", "config.json")
	} else {
		path = filepath.Join("..", ".private", "config.json")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var raw struct {
		Cohere     ProviderConfig `json:"cohere"`
		Cloudflare ProviderConfig `json:"cloudflare"`
		Groq       ProviderConfig `json:"groq"`
		Mistral    ProviderConfig `json:"mistral"`
		Nvidia     ProviderConfig `json:"nvidia"`
		OpenRouter ProviderConfig `json:"openrouter"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	return &Config{
		Cohere:     raw.Cohere,
		Cloudflare: raw.Cloudflare,
		Groq:       raw.Groq,
		Mistral:    raw.Mistral,
		Nvidia:     raw.Nvidia,
		OpenRouter: raw.OpenRouter,
	}, nil
}

```

## `./providers/helpers.go`

```go
// helpers.go — providers subpackage: init, Discord response helpers, and
// attachment downloading for transcribe/image/speak commands.
package providers

import (
	"fmt"
	"io"
	"net/http"

	"github.com/bwmarrin/discordgo"
)

// Init loads the provider config. Call once at startup.
func Init() error {
	c, err := loadConfig()
	if err != nil {
		return err
	}
	cfg = c
	return nil
}

// respond sends an interaction response.
func respond(s *discordgo.Session, i *discordgo.InteractionCreate, content string) {
	content = truncateForDiscord(content)
	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Content: content},
	})
}

// respondDefer acknowledges first (for long operations like image/speak).
func respondDefer(s *discordgo.Session, i *discordgo.InteractionCreate) {
	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	})
}

// editResponse edits a deferred response.
func editResponse(s *discordgo.Session, i *discordgo.InteractionCreate, content string) {
	c := truncateForDiscord(content)
	_, _ = s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{Content: &c})
}

// truncateForDiscord keeps replies under the Discord 2000-char limit.
func truncateForDiscord(s string) string {
	const max = 1990
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}

// getOpt reads a string option from the interaction data.
func getOpt(opts []*discordgo.ApplicationCommandInteractionDataOption, key string) string {
	for _, o := range opts {
		if o.Name == key {
			return toString(o.Value)
		}
	}
	return ""
}

func toString(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}

// attachmentBytes downloads the first Discord attachment into memory.
func attachmentBytes(s *discordgo.Session, i *discordgo.InteractionCreate) ([]byte, string, error) {
	att := i.ApplicationCommandData().Resolved
	if att == nil || len(att.Attachments) == 0 {
		return nil, "", fmt.Errorf("no attachment provided")
	}
	var a *discordgo.MessageAttachment
	for _, v := range att.Attachments {
		a = v
		break
	}
	if a == nil || a.URL == "" {
		return nil, "", fmt.Errorf("attachment has no URL")
	}
	req, err := http.NewRequest(http.MethodGet, a.URL, nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := s.Client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, "", fmt.Errorf("downloading attachment: HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 25<<20))
	if err != nil {
		return nil, "", err
	}
	return data, a.Filename, nil
}

// extFor returns a file extension for an audio/media content type.
func extFor(ct string) string {
	ct = trimContentType(ct)
	switch {
	case ct == "audio/wav" || ct == "audio/x-wav" || ct == "audio/wave":
		return "wav"
	case ct == "audio/mpeg" || ct == "audio/mp3":
		return "mp3"
	case ct == "audio/flac":
		return "flac"
	case ct == "audio/ogg":
		return "ogg"
	case ct == "image/png":
		return "png"
	case ct == "image/jpeg":
		return "jpg"
	case ct == "image/webp":
		return "webp"
	default:
		return "bin"
	}
}

func trimContentType(ct string) string {
	for i := 0; i < len(ct); i++ {
		if ct[i] == ';' {
			return ct[:i]
		}
	}
	return ct
}

```

## `./providers/providers_test.go`

```go
package providers

import (
	"encoding/base64"
	"encoding/json"
	"testing"
)

func TestSniffImageType(t *testing.T) {
	png := base64.StdEncoding.EncodeToString([]byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a})
	jpg := base64.StdEncoding.EncodeToString([]byte{0xff, 0xd8, 0xff, 0xe0})
	if got := sniffImageType(png); got != "image/png" {
		t.Errorf("sniffImageType(png) = %q, want image/png", got)
	}
	if got := sniffImageType(jpg); got != "image/jpeg" {
		t.Errorf("sniffImageType(jpg) = %q, want image/jpeg", got)
	}
}

func TestExtFor(t *testing.T) {
	cases := map[string]string{
		"audio/wav":                "wav",
		"audio/mpeg":               "mp3",
		"audio/ogg":                "ogg",
		"image/png":                "png",
		"image/jpeg":               "jpg",
		"image/webp":               "webp",
		"application/octet-stream": "bin",
	}
	for ct, want := range cases {
		if got := extFor(ct); got != want {
			t.Errorf("extFor(%q) = %q, want %q", ct, got, want)
		}
	}
	// content-type with parameters
	if got := extFor("audio/mpeg; charset=utf-8"); got != "mp3" {
		t.Errorf("extFor(with params) = %q, want mp3", got)
	}
}

func TestOpenAIChatRequestMarshals(t *testing.T) {
	req := openAIChatRequest{
		Model:    "test-model",
		Messages: chatMessages("hi"),
		Stream:   false,
		Extra:    map[string]interface{}{"temperature": 0.7},
	}
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if m["model"] != "test-model" {
		t.Errorf("model = %v", m["model"])
	}
	if m["temperature"] != 0.7 {
		t.Errorf("temperature = %v", m["temperature"])
	}
	if _, ok := m["stream"]; !ok {
		t.Error("stream missing")
	}
}

func TestCommandSpecsCount(t *testing.T) {
	specs := CommandSpecs()
	// We build 6 provider root commands.
	if len(specs) != 6 {
		t.Errorf("CommandSpecs() returned %d specs, want 6", len(specs))
	}
	seen := map[string]bool{}
	for _, s := range specs {
		seen[s.Command.Name] = true
	}
	for _, name := range []string{
		"cohere",
		"cloudflare",
		"groq",
		"mistral",
		"nvidia",
		"openrouter",
	} {
		if !seen[name] {
			t.Errorf("missing command %q", name)
		}
	}
}

```

## `./providers/providers_api.go`

```go
// providers_api.go — provider API implementations, ported from ElyProjectX's
// src/providers/*.js adapters (see /home/daniel/Repos/ElyProjectX). Each maps
// one provider's endpoints/models to the Discord commands.
package providers

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// ---- result types ----------------------------------------------------------

type chatResult struct {
	Message string
	Model   string
	Usage   *usage
	Raw     string // brief note for debugging
}

type usage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	CostUSD          float64
	HasCost          bool
}

type imageResult struct {
	Model     string
	B64Data   string
	MediaType string // image/png, image/jpeg, ...
}

type transcriptResult struct {
	Text  string
	Model string
}

type creditsResult struct {
	Summary string
	Notes   []string
}

// ---- providers -------------------------------------------------------------

func cohereClient() *apiClient {
	return newAPIClient("https://api.cohere.com", cfg.Cohere.APIKey)
}
func cloudflareClient() *apiClient {
	return newAPIClient("https://api.cloudflare.com/client/v4", cfg.Cloudflare.APIKey)
}
func groqClient() *apiClient {
	return newAPIClient("https://api.groq.com/openai/v1", cfg.Groq.APIKey)
}
func mistralClient() *apiClient {
	return newAPIClient("https://api.mistral.ai/v1", cfg.Mistral.APIKey)
}
func nvidiaClient() *apiClient {
	return newAPIClient("https://integrate.api.nvidia.com", cfg.Nvidia.APIKey)
}
func openrouterClient() *apiClient {
	return newAPIClient("https://openrouter.ai", cfg.OpenRouter.APIKey)
}

// ---- OpenAI-shape chat helper ----------------------------------------------

// openAIChat posts an OpenAI Chat Completions request to path and returns the
// reply text + model + usage.
func openAIChat(ctx context.Context, c *apiClient, path string, req openAIChatRequest) (*chatResult, error) {
	raw, err := c.doJSON(ctx, "POST", path, req)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			Text         string `json:"text"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		} `json:"usage"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := jsonUnmarshal(raw, &resp); err != nil {
		return nil, err
	}
	if resp.Error != nil && resp.Error.Message != "" {
		return nil, fmt.Errorf("%s", resp.Error.Message)
	}
	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("no choices in response")
	}
	content := resp.Choices[0].Message.Content
	if content == "" {
		content = resp.Choices[0].Text
	}
	if strings.TrimSpace(content) == "" {
		return nil, fmt.Errorf("empty reply (finish_reason=%s)", resp.Choices[0].FinishReason)
	}
	model := resp.Model
	if model == "" {
		model = req.Model
	}
	return &chatResult{
		Message: content,
		Model:   model,
		Usage: &usage{
			PromptTokens:     resp.Usage.PromptTokens,
			CompletionTokens: resp.Usage.CompletionTokens,
			TotalTokens:      resp.Usage.TotalTokens,
		},
	}, nil
}

type openAIChatRequest struct {
	Model    string                 `json:"model"`
	Messages []openAIMessage        `json:"messages"`
	Stream   bool                   `json:"stream"`
	Extra    map[string]interface{} `json:"-"`
}

type openAIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func (r openAIChatRequest) MarshalJSON() ([]byte, error) {
	type alias openAIChatRequest
	base, err := json.Marshal(alias(r))
	if err != nil {
		return nil, err
	}
	if len(r.Extra) == 0 {
		return base, nil
	}
	var m map[string]interface{}
	if err := json.Unmarshal(base, &m); err != nil {
		return nil, err
	}
	for k, v := range r.Extra {
		m[k] = v
	}
	return json.Marshal(m)
}

// chatMessages builds the messages payload from a single user prompt.
func chatMessages(prompt string) []openAIMessage {
	return []openAIMessage{{Role: "user", Content: prompt}}
}

// ---- Cohere ----------------------------------------------------------------

// Cohere v2 chat: POST /v2/chat, reply text lives in {message.content:[{type:"text",text}]}.
func cohereChat(ctx context.Context, prompt string, model string) (*chatResult, error) {
	if model == "" {
		model = "command-a-plus-05-2026"
	}
	c := cohereClient()
	body := map[string]interface{}{
		"model":    model,
		"messages": chatMessages(prompt),
	}
	raw, err := c.doJSON(ctx, "POST", "/v2/chat", body)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Model   string `json:"model"`
		Message *struct {
			Content interface{} `json:"content"`
		} `json:"message"`
		Usage *struct {
			Tokens *struct {
				InputTokens  int `json:"input_tokens"`
				OutputTokens int `json:"output_tokens"`
			} `json:"tokens"`
		} `json:"usage"`
	}
	if err := jsonUnmarshal(raw, &resp); err != nil {
		return nil, err
	}
	content := ""
	if resp.Message != nil {
		switch v := resp.Message.Content.(type) {
		case string:
			content = v
		case []interface{}:
			var parts []string
			for _, b := range v {
				if m, ok := b.(map[string]interface{}); ok {
					if t, ok := m["type"].(string); ok && t == "text" {
						if txt, ok := m["text"].(string); ok {
							parts = append(parts, txt)
						}
					}
				}
			}
			content = strings.Join(parts, "\n")
		}
	}
	if strings.TrimSpace(content) == "" {
		return nil, fmt.Errorf("empty reply from cohere")
	}
	u := &usage{}
	if resp.Usage != nil && resp.Usage.Tokens != nil {
		u.PromptTokens = resp.Usage.Tokens.InputTokens
		u.CompletionTokens = resp.Usage.Tokens.OutputTokens
		u.TotalTokens = resp.Usage.Tokens.InputTokens + resp.Usage.Tokens.OutputTokens
	}
	return &chatResult{Message: content, Model: resp.Model, Usage: u}, nil
}

// Cohere v2 STT: POST /v2/audio/transcriptions (multipart). Language REQUIRED.
func cohereTranscribe(ctx context.Context, file []byte, fileName, language, model string) (*transcriptResult, error) {
	if model == "" {
		model = "cohere-transcribe-03-2026"
	}
	if language == "" {
		return nil, fmt.Errorf("Cohere STT requires a language (e.g. en) — no auto-detection")
	}
	c := cohereClient()
	fields := map[string]string{"model": model, "language": language}
	raw, err := c.doMultipart(ctx, "/v2/audio/transcriptions", fields, "file", fileName, file)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Text  string `json:"text"`
		Model string `json:"model"`
	}
	if err := jsonUnmarshal(raw, &resp); err != nil {
		return nil, err
	}
	if strings.TrimSpace(resp.Text) == "" {
		return nil, fmt.Errorf("no transcript text in response")
	}
	return &transcriptResult{Text: resp.Text, Model: resp.Model}, nil
}

// ---- Cloudflare ------------------------------------------------------------

// Cloudflare OpenAI-compatible chat: POST /accounts/{id}/ai/v1/chat/completions.
// Reply may come back raw OpenAI JSON or wrapped in the v4 envelope {result}.
func cloudflareChat(ctx context.Context, prompt, model string) (*chatResult, error) {
	if model == "" {
		model = "@cf/meta/llama-3.3-70b-instruct-fp8-fast"
	}
	c := cloudflareClient()
	path := "/accounts/" + url.PathEscape(cfg.Cloudflare.AccountID) + "/ai/v1/chat/completions"
	body := openAIChatRequest{
		Model:    model,
		Messages: chatMessages(prompt),
		Stream:   false,
	}
	raw, err := c.doJSON(ctx, "POST", path, body)
	if err != nil {
		return nil, err
	}
	// Detect the v4 envelope: if it has "success", unwrap result.
	var envelope struct {
		Success bool            `json:"success"`
		Errors  []interface{}   `json:"errors"`
		Result  json.RawMessage `json:"result"`
	}
	var msg []byte
	if err := json.Unmarshal(raw, &envelope); err == nil && envelope.Success {
		msg = envelope.Result
	} else {
		msg = raw
	}
	var resp struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			Text string `json:"text"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(msg, &resp); err != nil {
		return nil, err
	}
	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("no choices in cloudflare response")
	}
	content := resp.Choices[0].Message.Content
	if content == "" {
		content = resp.Choices[0].Text
	}
	if strings.TrimSpace(content) == "" {
		return nil, fmt.Errorf("empty reply from cloudflare")
	}
	m := resp.Model
	if m == "" {
		m = model
	}
	return &chatResult{Message: content, Model: m, Usage: &usage{}}, nil
}

// Cloudflare text-to-image: POST /accounts/{id}/ai/run/{model}, response
// v4 envelope with result.image (base64).
func cloudflareImage(ctx context.Context, prompt, model string) (*imageResult, error) {
	if model == "" {
		model = "@cf/black-forest-labs/flux-1-schnell"
	}
	c := cloudflareClient()
	path := "/accounts/" + url.PathEscape(cfg.Cloudflare.AccountID) + "/ai/run/" + model
	body := map[string]interface{}{"prompt": prompt}
	raw, err := c.doJSON(ctx, "POST", path, body)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Success bool            `json:"success"`
		Result  json.RawMessage `json:"result"`
	}
	if err := jsonUnmarshal(raw, &resp); err != nil {
		return nil, err
	}
	var result struct {
		Image   string `json:"image"`
		B64Json string `json:"b64_json"`
	}
	var arr []struct {
		B64Json string `json:"b64_json"`
	}
	b64 := ""
	if err := json.Unmarshal(resp.Result, &result); err == nil {
		b64 = result.Image
		if b64 == "" {
			b64 = result.B64Json
		}
	}
	if b64 == "" {
		_ = json.Unmarshal(resp.Result, &arr)
		if len(arr) > 0 {
			b64 = arr[0].B64Json
		}
	}
	if b64 == "" {
		return nil, fmt.Errorf("no image data in cloudflare response")
	}
	return &imageResult{
		Model:     model,
		B64Data:   b64,
		MediaType: sniffImageType(b64),
	}, nil
}

// sniffImageType detects PNG/JPEG/WEBP from base64 magic bytes.
func sniffImageType(b64 string) string {
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(data) < 4 {
		return "image/png"
	}
	if data[0] == 0x89 && data[1] == 0x50 && data[2] == 0x4e && data[3] == 0x47 {
		return "image/png"
	}
	if data[0] == 0xff && data[1] == 0xd8 && data[2] == 0xff {
		return "image/jpeg"
	}
	if len(data) >= 12 && string(data[8:12]) == "WEBP" {
		return "image/webp"
	}
	return "image/png"
}

// ---- Groq ------------------------------------------------------------------

func groqChat(ctx context.Context, prompt, model string) (*chatResult, error) {
	if model == "" {
		model = "openai/gpt-oss-20b"
	}
	return openAIChat(ctx, groqClient(), "/chat/completions", openAIChatRequest{
		Model: model, Messages: chatMessages(prompt), Stream: false,
	})
}

// Groq STT: POST /audio/transcriptions (multipart).
func groqTranscribe(ctx context.Context, file []byte, fileName, language, model string) (*transcriptResult, error) {
	if model == "" {
		model = "whisper-large-v3-turbo"
	}
	c := groqClient()
	fields := map[string]string{"model": model}
	if language != "" {
		fields["language"] = language
	}
	raw, err := c.doMultipart(ctx, "/audio/transcriptions", fields, "file", fileName, file)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Text  string `json:"text"`
		Model string `json:"model"`
	}
	if err := jsonUnmarshal(raw, &resp); err != nil {
		return nil, err
	}
	if strings.TrimSpace(resp.Text) == "" {
		return nil, fmt.Errorf("no transcript text in response")
	}
	return &transcriptResult{Text: resp.Text, Model: resp.Model}, nil
}

// Groq TTS: POST /audio/speech (JSON body → raw audio bytestream).
func groqSpeak(ctx context.Context, text, voice, model, format string) ([]byte, string, error) {
	if model == "" {
		model = "canopylabs/orpheus-v1-english"
	}
	if voice == "" {
		voice = "autumn"
	}
	if format == "" {
		format = "wav"
	}
	body := map[string]interface{}{
		"input":           text,
		"model":           model,
		"voice":           voice,
		"response_format": format,
	}
	return groqClient().doRaw(ctx, "POST", "/audio/speech", body)
}

// ---- Mistral ---------------------------------------------------------------

func mistralChat(ctx context.Context, prompt, model string) (*chatResult, error) {
	if model == "" {
		model = "mistral-medium-latest"
	}
	return openAIChat(ctx, mistralClient(), "/chat/completions", openAIChatRequest{
		Model: model, Messages: chatMessages(prompt), Stream: false,
	})
}

// ---- NVIDIA ----------------------------------------------------------------

func nvidiaChat(ctx context.Context, prompt, model string) (*chatResult, error) {
	if model == "" {
		model = "nvidia/nemotron-3-super-120b-a12b"
	}
	return openAIChat(ctx, nvidiaClient(), "/v1/chat/completions", openAIChatRequest{
		Model: model, Messages: chatMessages(prompt), Stream: false,
	})
}

// ---- OpenRouter ------------------------------------------------------------

func openrouterChat(ctx context.Context, prompt, model string) (*chatResult, error) {
	if model == "" {
		model = "nvidia/nemotron-3-super-120b-a12b:free"
	}
	return openAIChat(ctx, openrouterClient(), "/api/v1/chat/completions", openAIChatRequest{
		Model: model, Messages: chatMessages(prompt), Stream: false,
	})
}

// OpenRouter image: POST /api/v1/images → {created, data:[{b64_json, media_type}]}.
func openrouterImage(ctx context.Context, prompt, model string) (*imageResult, error) {
	if model == "" {
		model = "bytedance-seed/seedream-4.5"
	}
	c := openrouterClient()
	body := map[string]interface{}{"model": model, "prompt": prompt}
	raw, err := c.doJSON(ctx, "POST", "/api/v1/images", body)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Model string `json:"model"`
		Data  []struct {
			B64Json   string `json:"b64_json"`
			MediaType string `json:"media_type"`
		} `json:"data"`
	}
	if err := jsonUnmarshal(raw, &resp); err != nil {
		return nil, err
	}
	if len(resp.Data) == 0 || resp.Data[0].B64Json == "" {
		return nil, fmt.Errorf("no image data in openrouter response")
	}
	mt := resp.Data[0].MediaType
	if mt == "" {
		mt = "image/png"
	}
	m := resp.Model
	if m == "" {
		m = model
	}
	return &imageResult{Model: m, B64Data: resp.Data[0].B64Json, MediaType: mt}, nil
}

// OpenRouter STT: POST /api/v1/audio/transcriptions (multipart).
func openrouterTranscribe(ctx context.Context, file []byte, fileName, language, model string) (*transcriptResult, error) {
	if model == "" {
		model = "openai/whisper-large-v3"
	}
	c := openrouterClient()
	fields := map[string]string{"model": model}
	if language != "" {
		fields["language"] = language
	}
	raw, err := c.doMultipart(ctx, "/api/v1/audio/transcriptions", fields, "file", fileName, file)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Text  string `json:"text"`
		Model string `json:"model"`
	}
	if err := jsonUnmarshal(raw, &resp); err != nil {
		return nil, err
	}
	if strings.TrimSpace(resp.Text) == "" {
		return nil, fmt.Errorf("no transcript text in response")
	}
	return &transcriptResult{Text: resp.Text, Model: resp.Model}, nil
}

// OpenRouter credits: GET /api/v1/key (per-key quota) then /api/v1/credits fallback.
func openrouterCredits(ctx context.Context) (*creditsResult, error) {
	c := openrouterClient()
	raw, err := c.doJSON(ctx, "GET", "/api/v1/key", nil)
	if err == nil {
		var resp struct {
			Data *struct {
				Label          string   `json:"label"`
				IsFreeTier     bool     `json:"is_free_tier"`
				Limit          *float64 `json:"limit"`
				LimitRemaining *float64 `json:"limit_remaining"`
				Usage          *float64 `json:"usage"`
				UsageDaily     *float64 `json:"usage_daily"`
				UsageWeekly    *float64 `json:"usage_weekly"`
				UsageMonthly   *float64 `json:"usage_monthly"`
			} `json:"data"`
		}
		if err := jsonUnmarshal(raw, &resp); err != nil {
			return nil, err
		}
		d := resp.Data
		if d == nil {
			return nil, fmt.Errorf("openrouter /key returned no data")
		}
		var b strings.Builder
		label := d.Label
		if len(label) > 40 || strings.HasPrefix(label, "sk-or-v1-") {
			label = "(unlabeled)"
		}
		fmt.Fprintf(&b, "**OpenRouter key**: label `%s` · free tier `%v`\n", label, d.IsFreeTier)
		if d.Limit != nil {
			fmt.Fprintf(&b, "Per-key cap: **$%.2f** · remaining **$%.2f**\n", *d.Limit, derefF(d.LimitRemaining))
		} else {
			b.WriteString("Per-key cap: none (unlimited)\n")
		}
		fmt.Fprintf(&b, "Usage: total $%.2f · daily $%.2f · weekly $%.2f · monthly $%.2f",
			derefF(d.Usage), derefF(d.UsageDaily), derefF(d.UsageWeekly), derefF(d.UsageMonthly))
		return &creditsResult{Summary: b.String()}, nil
	}

	// Fallback: management-key credits view.
	raw, err2 := c.doJSON(ctx, "GET", "/api/v1/credits", nil)
	if err2 != nil {
		return nil, fmt.Errorf("GET /api/v1/key failed (%v) and /api/v1/credits also failed (%v)", err, err2)
	}
	var cr struct {
		Credits      float64 `json:"credits"`
		TotalCredits float64 `json:"total_credits"`
		TotalUsage   float64 `json:"total_usage"`
	}
	if err := jsonUnmarshal(raw, &cr); err != nil {
		return nil, err
	}
	return &creditsResult{
		Summary: fmt.Sprintf("**OpenRouter credits**: $%.2f available · purchased $%.2f · used $%.2f",
			cr.Credits, cr.TotalCredits, cr.TotalUsage),
	}, nil
}

func derefF(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}

// bytesBuffer is a small helper (used by TTS callers to feed discordgo).
func bytesBuffer(b []byte) *bytes.Buffer {
	return bytes.NewBuffer(b)
}

```

## `./providers/handlers.go`

```go
// handlers.go — Discord command handlers for the provider commands.
package providers

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/bwmarrin/discordgo"
)

// ---- chat handlers ---------------------------------------------------------

func handleCohereChat(s *discordgo.Session, i *discordgo.InteractionCreate, get func(string) string) {
	doChat(s, i, get, "cohere", func(ctx context.Context, prompt, model string) (*chatResult, error) {
		return cohereChat(ctx, prompt, model)
	})
}

func handleCloudflareChat(s *discordgo.Session, i *discordgo.InteractionCreate, get func(string) string) {
	doChat(s, i, get, "cloudflare", func(ctx context.Context, prompt, model string) (*chatResult, error) {
		return cloudflareChat(ctx, prompt, model)
	})
}

func handleGroqChat(s *discordgo.Session, i *discordgo.InteractionCreate, get func(string) string) {
	doChat(s, i, get, "groq", func(ctx context.Context, prompt, model string) (*chatResult, error) {
		return groqChat(ctx, prompt, model)
	})
}

func handleMistralChat(s *discordgo.Session, i *discordgo.InteractionCreate, get func(string) string) {
	doChat(s, i, get, "mistral", func(ctx context.Context, prompt, model string) (*chatResult, error) {
		return mistralChat(ctx, prompt, model)
	})
}

func handleNvidiaChat(s *discordgo.Session, i *discordgo.InteractionCreate, get func(string) string) {
	doChat(s, i, get, "nvidia", func(ctx context.Context, prompt, model string) (*chatResult, error) {
		return nvidiaChat(ctx, prompt, model)
	})
}

func handleOpenRouterChat(s *discordgo.Session, i *discordgo.InteractionCreate, get func(string) string) {
	doChat(s, i, get, "openrouter", func(ctx context.Context, prompt, model string) (*chatResult, error) {
		return openrouterChat(ctx, prompt, model)
	})
}

func doChat(s *discordgo.Session, i *discordgo.InteractionCreate, get func(string) string, name string, fn func(ctx context.Context, prompt, model string) (*chatResult, error)) {
	prompt := strings.TrimSpace(get("prompt"))
	if prompt == "" {
		respond(s, i, "Please provide a `prompt`.")
		return
	}
	model := get("model")
	respondDefer(s, i)
	r, err := fn(context.Background(), prompt, model)
	if err != nil {
		editResponse(s, i, "❌ **"+strings.Title(name)+" chat failed**: "+err.Error())
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "💬 **%s chat** — `%s`\n\n%s", strings.Title(name), r.Model, r.Message)
	if r.Usage != nil && r.Usage.TotalTokens > 0 {
		fmt.Fprintf(&b, "\n\n_⚙ %d tokens_", r.Usage.TotalTokens)
	}
	editResponse(s, i, b.String())
}

// ---- image handlers --------------------------------------------------------

func handleCloudflareImage(s *discordgo.Session, i *discordgo.InteractionCreate, get func(string) string) {
	doImage(s, i, get, "cloudflare", func(ctx context.Context, prompt, model string) (*imageResult, error) {
		return cloudflareImage(ctx, prompt, model)
	})
}

func handleOpenRouterImage(s *discordgo.Session, i *discordgo.InteractionCreate, get func(string) string) {
	doImage(s, i, get, "openrouter", func(ctx context.Context, prompt, model string) (*imageResult, error) {
		return openrouterImage(ctx, prompt, model)
	})
}

func doImage(s *discordgo.Session, i *discordgo.InteractionCreate, get func(string) string, name string, fn func(ctx context.Context, prompt, model string) (*imageResult, error)) {
	prompt := strings.TrimSpace(get("prompt"))
	if prompt == "" {
		respond(s, i, "Please provide a `prompt`.")
		return
	}
	model := get("model")
	respondDefer(s, i)
	r, err := fn(context.Background(), prompt, model)
	if err != nil {
		editResponse(s, i, "❌ **"+strings.Title(name)+" image failed**: "+err.Error())
		return
	}
	data, err := base64.StdEncoding.DecodeString(r.B64Data)
	if err != nil {
		editResponse(s, i, "❌ Could not decode image data: "+err.Error())
		return
	}
	ext := extFor(r.MediaType)
	_, _ = s.ChannelMessageSendComplex(i.ChannelID, &discordgo.MessageSend{
		Content: fmt.Sprintf("🎨 **%s image** — `%s`", strings.Title(name), r.Model),
		Files: []*discordgo.File{{
			Name:   fmt.Sprintf("%s_image_%s.%s", name, i.ID, ext),
			Reader: bytes.NewReader(data),
		}},
	})
}

// ---- transcribe handlers ---------------------------------------------------

func handleCohereTranscribe(s *discordgo.Session, i *discordgo.InteractionCreate, get func(string) string) {
	doTranscribe(s, i, get, "cohere", cohereTranscribe)
}

func handleGroqTranscribe(s *discordgo.Session, i *discordgo.InteractionCreate, get func(string) string) {
	doTranscribe(s, i, get, "groq", groqTranscribe)
}

func handleOpenRouterTranscribe(s *discordgo.Session, i *discordgo.InteractionCreate, get func(string) string) {
	doTranscribe(s, i, get, "openrouter", openrouterTranscribe)
}

func doTranscribe(s *discordgo.Session, i *discordgo.InteractionCreate, get func(string) string, name string, fn func(ctx context.Context, file []byte, fileName, language, model string) (*transcriptResult, error)) {
	respondDefer(s, i)
	file, fileName, err := attachmentBytes(s, i)
	if err != nil {
		editResponse(s, i, "❌ Could not get audio attachment: "+err.Error())
		return
	}
	language := get("language")
	model := get("model")
	r, err := fn(context.Background(), file, fileName, language, model)
	if err != nil {
		editResponse(s, i, "❌ **"+strings.Title(name)+" transcription failed**: "+err.Error())
		return
	}
	editResponse(s, i, fmt.Sprintf("📝 **%s transcription** (`%s`)\n\n%s", strings.Title(name), r.Model, r.Text))
}

// ---- speak handler ---------------------------------------------------------

func handleGroqSpeak(s *discordgo.Session, i *discordgo.InteractionCreate, get func(string) string) {
	text := strings.TrimSpace(get("text"))
	if text == "" {
		respond(s, i, "Please provide `text` to speak.")
		return
	}
	voice := get("voice")
	model := get("model")
	respondDefer(s, i)
	data, ct, err := groqSpeak(context.Background(), text, voice, model, "wav")
	if err != nil {
		editResponse(s, i, "❌ **Groq TTS failed**: "+err.Error())
		return
	}
	ext := extFor(ct)
	_, _ = s.ChannelMessageSendComplex(i.ChannelID, &discordgo.MessageSend{
		Content: fmt.Sprintf("🔊 **Groq speech** — `%s`", voiceOrDefault(voice)),
		Files: []*discordgo.File{{
			Name:   fmt.Sprintf("groq_speech_%s.%s", i.ID, ext),
			Reader: bytes.NewReader(data),
		}},
	})
}

func voiceOrDefault(v string) string {
	if v == "" {
		return "autumn"
	}
	return v
}

// ---- credits handler -------------------------------------------------------

func handleOpenRouterCredits(s *discordgo.Session, i *discordgo.InteractionCreate, get func(string) string) {
	respondDefer(s, i)
	r, err := openrouterCredits(context.Background())
	if err != nil {
		editResponse(s, i, "❌ **OpenRouter credits failed**: "+err.Error())
		return
	}
	out := r.Summary
	if len(r.Notes) > 0 {
		out += "\n" + strings.Join(r.Notes, "\n")
	}
	editResponse(s, i, out)
}

```

## `./providers/commands.go`

```go
package providers

import (
	"fmt"

	"github.com/bwmarrin/discordgo"
)

type CommandSpec struct {
	Command *discordgo.ApplicationCommand
	Handler func(s *discordgo.Session, i *discordgo.InteractionCreate)
}

func CommandSpecs() []CommandSpec {
	return []CommandSpec{
		{
			Command: &discordgo.ApplicationCommand{
				Name:        "cohere",
				Description: "Cohere AI Models",
				Options: []*discordgo.ApplicationCommandOption{
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "chat",
						Description: "Chat with Cohere (command-a-plus-05-2026)",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionString, Name: "prompt", Description: "Your message", Required: true},
							{Type: discordgo.ApplicationCommandOptionString, Name: "model", Description: "Model id (default command-a-plus-05-2026)"},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "transcribe",
						Description: "Transcribe audio with Cohere (requires language)",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionAttachment, Name: "audio", Description: "Audio file to transcribe", Required: true},
							{Type: discordgo.ApplicationCommandOptionString, Name: "language", Description: "ISO-639-1 language code (REQUIRED, no auto-detect)", Required: true},
							{Type: discordgo.ApplicationCommandOptionString, Name: "model", Description: "Model (default cohere-transcribe-03-2026)"},
						},
					},
				},
			},
			Handler: handleCohereRoot,
		},
		{
			Command: &discordgo.ApplicationCommand{
				Name:        "cloudflare",
				Description: "Cloudflare Workers AI",
				Options: []*discordgo.ApplicationCommandOption{
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "chat",
						Description: "Chat with Cloudflare Workers AI",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionString, Name: "prompt", Description: "Your message", Required: true},
							{Type: discordgo.ApplicationCommandOptionString, Name: "model", Description: "Model id (default @cf/meta/llama-3.3-70b-instruct-fp8-fast)"},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "image",
						Description: "Generate an image with Cloudflare Workers AI (flux-1-schnell)",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionString, Name: "prompt", Description: "Image prompt", Required: true},
							{Type: discordgo.ApplicationCommandOptionString, Name: "model", Description: "Model id (default @cf/black-forest-labs/flux-1-schnell)"},
						},
					},
				},
			},
			Handler: handleCloudflareRoot,
		},
		{
			Command: &discordgo.ApplicationCommand{
				Name:        "groq",
				Description: "Groq LPU Inference Engine",
				Options: []*discordgo.ApplicationCommandOption{
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "chat",
						Description: "Chat with Groq (openai/gpt-oss-20b)",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionString, Name: "prompt", Description: "Your message", Required: true},
							{Type: discordgo.ApplicationCommandOptionString, Name: "model", Description: "Model id (default openai/gpt-oss-20b)"},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "speak",
						Description: "Synthesize speech with Groq (orpheus)",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionString, Name: "text", Description: "Text to speak", Required: true},
							{Type: discordgo.ApplicationCommandOptionString, Name: "voice", Description: "autumn|diana|hannah|austin|daniel|troy (default autumn)"},
							{Type: discordgo.ApplicationCommandOptionString, Name: "model", Description: "Model id (default canopylabs/orpheus-v1-english)"},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "transcribe",
						Description: "Transcribe audio with Groq (whisper-large-v3-turbo)",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionAttachment, Name: "audio", Description: "Audio file to transcribe", Required: true},
							{Type: discordgo.ApplicationCommandOptionString, Name: "language", Description: "ISO-639-1 language code (optional)"},
							{Type: discordgo.ApplicationCommandOptionString, Name: "model", Description: "Model (default whisper-large-v3-turbo)"},
						},
					},
				},
			},
			Handler: handleGroqRoot,
		},
		{
			Command: &discordgo.ApplicationCommand{
				Name:        "mistral",
				Description: "Chat with Mistral AI",
				Options: []*discordgo.ApplicationCommandOption{
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "chat",
						Description: "Chat with Mistral (mistral-medium-latest)",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionString, Name: "prompt", Description: "Your message", Required: true},
							{Type: discordgo.ApplicationCommandOptionString, Name: "model", Description: "Model id (default mistral-medium-latest)"},
						},
					},
				},
			},
			Handler: handleMistralRoot,
		},
		{
			Command: &discordgo.ApplicationCommand{
				Name:        "nvidia",
				Description: "Chat with NVIDIA NIM",
				Options: []*discordgo.ApplicationCommandOption{
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "chat",
						Description: "Chat with NVIDIA (nemotron-3-super-120b)",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionString, Name: "prompt", Description: "Your message", Required: true},
							{Type: discordgo.ApplicationCommandOptionString, Name: "model", Description: "Model id (default nvidia/nemotron-3-super-120b-a12b)"},
						},
					},
				},
			},
			Handler: handleNvidiaRoot,
		},
		{
			Command: &discordgo.ApplicationCommand{
				Name:        "openrouter",
				Description: "OpenRouter AI Models",
				Options: []*discordgo.ApplicationCommandOption{
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "chat",
						Description: "Chat via OpenRouter (default nemotron-3 free)",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionString, Name: "prompt", Description: "Your message", Required: true},
							{Type: discordgo.ApplicationCommandOptionString, Name: "model", Description: "Model id (default nvidia/nemotron-3-super-120b-a12b:free)"},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "image",
						Description: "Generate an image via OpenRouter (seedream-4.5)",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionString, Name: "prompt", Description: "Image prompt", Required: true},
							{Type: discordgo.ApplicationCommandOptionString, Name: "model", Description: "Model id (default bytedance-seed/seedream-4.5)"},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "transcribe",
						Description: "Transcribe audio via OpenRouter (whisper-large-v3)",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionAttachment, Name: "audio", Description: "Audio file to transcribe", Required: true},
							{Type: discordgo.ApplicationCommandOptionString, Name: "language", Description: "ISO-639-1 language code (optional)"},
							{Type: discordgo.ApplicationCommandOptionString, Name: "model", Description: "Model (default openai/whisper-large-v3)"},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "credits",
						Description: "Show OpenRouter account balance and quota",
					},
				},
			},
			Handler: handleOpenRouterRoot,
		},
	}
}

// buildGet generates a scoped 'get' closure for the active subcommand.
func buildGet(subCommand *discordgo.ApplicationCommandInteractionDataOption) func(string) string {
	return func(key string) string {
		for _, o := range subCommand.Options {
			if o.Name == key {
				if s, ok := o.Value.(string); ok {
					return s
				}
				return fmt.Sprintf("%v", o.Value)
			}
		}
		return ""
	}
}

// Subcommand Routers
func handleCohereRoot(s *discordgo.Session, i *discordgo.InteractionCreate) {
	data := i.ApplicationCommandData()
	if len(data.Options) == 0 { return }
	sub := data.Options[0]
	get := buildGet(sub)
	if sub.Name == "chat" {
		handleCohereChat(s, i, get)
	} else if sub.Name == "transcribe" {
		handleCohereTranscribe(s, i, get)
	}
}

func handleCloudflareRoot(s *discordgo.Session, i *discordgo.InteractionCreate) {
	data := i.ApplicationCommandData()
	if len(data.Options) == 0 { return }
	sub := data.Options[0]
	get := buildGet(sub)
	if sub.Name == "chat" {
		handleCloudflareChat(s, i, get)
	} else if sub.Name == "image" {
		handleCloudflareImage(s, i, get)
	}
}

func handleGroqRoot(s *discordgo.Session, i *discordgo.InteractionCreate) {
	data := i.ApplicationCommandData()
	if len(data.Options) == 0 { return }
	sub := data.Options[0]
	get := buildGet(sub)
	if sub.Name == "chat" {
		handleGroqChat(s, i, get)
	} else if sub.Name == "speak" {
		handleGroqSpeak(s, i, get)
	} else if sub.Name == "transcribe" {
		handleGroqTranscribe(s, i, get)
	}
}

func handleMistralRoot(s *discordgo.Session, i *discordgo.InteractionCreate) {
	data := i.ApplicationCommandData()
	if len(data.Options) == 0 { return }
	sub := data.Options[0]
	if sub.Name == "chat" {
		handleMistralChat(s, i, buildGet(sub))
	}
}

func handleNvidiaRoot(s *discordgo.Session, i *discordgo.InteractionCreate) {
	data := i.ApplicationCommandData()
	if len(data.Options) == 0 { return }
	sub := data.Options[0]
	if sub.Name == "chat" {
		handleNvidiaChat(s, i, buildGet(sub))
	}
}

func handleOpenRouterRoot(s *discordgo.Session, i *discordgo.InteractionCreate) {
	data := i.ApplicationCommandData()
	if len(data.Options) == 0 { return }
	sub := data.Options[0]
	get := buildGet(sub)
	switch sub.Name {
	case "chat":
		handleOpenRouterChat(s, i, get)
	case "image":
		handleOpenRouterImage(s, i, get)
	case "transcribe":
		handleOpenRouterTranscribe(s, i, get)
	case "credits":
		handleOpenRouterCredits(s, i, get) // credits doesn't use get
	}
}
```

## `./a2e/voiceassign.go`

```go
// voiceassign.go — local voice-assignment store.
//
// The user assigns friendly names to voice IDs in a local JSON file. This
// module loads/edits that file. The file lives in the repo's data/ dir and is
// gitignored (personal mapping, like config). A committed example template is
// provided as data/voice-assignments.example.json.
package a2e

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const voiceAssignmentsRelPathDefault = "../data/voice-assignments.json"

// voiceAssignmentsRelPath is a var (not const) so tests can override it.
var voiceAssignmentsRelPath = voiceAssignmentsRelPathDefault

// voiceAssignments maps a voice ID -> friendly name chosen by the user.
type voiceAssignments struct {
	// VoiceID -> name
	Names map[string]string `json:"names"`
}

func loadVoiceAssignments() (*voiceAssignments, error) {
	va := &voiceAssignments{Names: map[string]string{}}
	b, err := os.ReadFile(filepath.Clean(voiceAssignmentsRelPath))
	if err != nil {
		if os.IsNotExist(err) {
			return va, nil // not created yet — fine
		}
		return nil, err
	}
	if err := json.Unmarshal(b, va); err != nil {
		return nil, fmt.Errorf("parsing voice-assignments.json: %w", err)
	}
	if va.Names == nil {
		va.Names = map[string]string{}
	}
	return va, nil
}

func (va *voiceAssignments) save() error {
	dir := filepath.Dir(filepath.Clean(voiceAssignmentsRelPath))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(va, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Clean(voiceAssignmentsRelPath), b, 0o644)
}

// assignName sets a friendly name for a voice ID and persists it.
func (va *voiceAssignments) assignName(voiceID, name string) error {
	va.Names[voiceID] = name
	return va.save()
}

// displayName returns the friendly name for a voice ID (or the raw ID if
// unassigned).
func (va *voiceAssignments) displayName(voiceID string) string {
	if n, ok := va.Names[voiceID]; ok && n != "" {
		return n
	}
	return voiceID
}

// sanitizeVoiceID strips whitespace and any stray " name"-style suffix that can
// sneak in from Discord option parsing, leaving just the hex ID.
func sanitizeVoiceID(s string) string {
	s = strings.TrimSpace(s)
	// Take only the first whitespace-delimited token (defensive against
	// concatenated "id name" input).
	if i := strings.IndexAny(s, " \t\n"); i >= 0 {
		s = s[:i]
	}
	return s
}

// isValidVoiceID reports whether s is a valid A2E voice ID (24-char hex).
func isValidVoiceID(s string) bool {
	if len(s) != 24 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'a' && r <= 'f':
		case r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return true
}

// lookupByPrefix returns the voice ID(s) matching a given prefix (short id or
// friendly name fragment). Used to resolve the /tts voice argument.
func (va *voiceAssignments) lookupByPrefix(prefix string) ([]string, string) {
	prefix = strings.ToLower(strings.TrimSpace(prefix))
	var matches []string
	var viaName string
	for id, name := range va.Names {
		if strings.HasPrefix(strings.ToLower(id), prefix) {
			matches = append(matches, id)
			viaName = name
		} else if strings.HasPrefix(strings.ToLower(name), prefix) {
			matches = append(matches, id)
			viaName = name
		}
	}
	sort.Strings(matches)
	return matches, viaName
}

// formatVoiceList renders the completed voices as a compact, aligned block.
func (va *voiceAssignments) formatCompleted(voices []UserVoice, training []UserVoice) string {
	var b strings.Builder
	b.WriteString("🎙️ **Your cloned voices**\n\n")
	if len(voices) == 0 {
		b.WriteString("_No completed cloned voices yet. Use `/voice-clone` to train one._\n")
	} else {
		// group by status
		for _, v := range voices {
			name := v.Name
			if v.CurrentStatus == "" {
				v.CurrentStatus = "completed"
			}
			assigned := va.Names[v.ID]
			b.WriteString(fmt.Sprintf("• **%s**\n", orVal(assigned, name)))
			b.WriteString(fmt.Sprintf("  `%s` · %s · %s\n", v.ID, v.Gender, v.Model))
			if assigned != "" && assigned != name {
				b.WriteString(fmt.Sprintf("  _API name: %s_\n", name))
			}
		}
	}
	if len(training) > 0 {
		b.WriteString("\n**In progress / training**\n")
		shown := 0
		for _, v := range training {
			// trainingRecord returns ALL records (incl. completed); only show
			// ones that are not actually completed.
			if strings.EqualFold(v.CurrentStatus, "completed") {
				continue
			}
			b.WriteString(fmt.Sprintf("• `%s` · **%s** · %s\n", v.ID, v.Name, orVal(v.CurrentStatus, "pending")))
			shown++
		}
		if shown == 0 {
			b.WriteString("_No voice currently training._\n")
		}
	}
	return b.String()
}

// formatBuiltin renders the built-in preset voices grouped by gender.
func (va *voiceAssignments) formatBuiltin(groups []VoiceGroup) string {
	var b strings.Builder
	b.WriteString("🗣️ **Built-in A2E voices**\n\n")
	for _, g := range groups {
		b.WriteString(fmt.Sprintf("**%s** (%d)\n", strings.Title(g.Label), len(g.Children)))
		for _, v := range g.Children {
			b.WriteString(fmt.Sprintf("• `%s` · %s\n", v.Value, v.Label))
		}
	}
	return b.String()
}

```

## `./a2e/config.go`

```go
// config.go — A2E subpackage config: reads the shared repo .private/config.json.
package a2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
)

// Config holds the A2E-relevant settings from the shared config file.
type Config struct {
	A2EAPIKey         string `json:"A2E_API_KEY"`
	A2EBaseURL        string `json:"a2eBaseUrl"`
	DefaultImageModel string `json:"defaultImageModel"`
	DefaultVideoModel string `json:"defaultVideoModel"`
}

// cfg is the package-level config, initialized by Init.
var cfg *Config

// ac is the package-level A2E API client, initialized by Init.
var ac *a2eClient

// loadConfig reads the repo-root .private/config.json (relative to this file).
func loadConfig() (*Config, error) {
	_, file, _, ok := runtime.Caller(0)
	path := ""
	if ok {
		path = filepath.Join(filepath.Dir(file), "..", ".private", "config.json")
	} else {
		path = filepath.Join("..", ".private", "config.json")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var raw struct {
		A2EAPIKey         string `json:"A2E_API_KEY"`
		A2EBaseURL        string `json:"a2eBaseUrl"`
		DefaultImageModel string `json:"defaultImageModel"`
		DefaultVideoModel string `json:"defaultVideoModel"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	c := &Config{
		A2EAPIKey:         raw.A2EAPIKey,
		A2EBaseURL:        raw.A2EBaseURL,
		DefaultImageModel: raw.DefaultImageModel,
		DefaultVideoModel: raw.DefaultVideoModel,
	}
	if c.A2EBaseURL == "" {
		c.A2EBaseURL = "https://video.a2e.ai"
	}
	if c.DefaultImageModel == "" {
		c.DefaultImageModel = "seedream"
	}
	if c.DefaultVideoModel == "" {
		c.DefaultVideoModel = "wan3.0-video"
	}
	return c, nil
}

```

## `./a2e/helpers.go`

```go
// helpers.go — A2E subpackage response helpers + init.
package a2e

import (
	"fmt"
	"log"
	"strings"

	"github.com/bwmarrin/discordgo"
)

// Init loads config and prepares the package-level client. Safe to call once
// at startup. Returns an error if config can't be loaded.
func Init() error {
	c, err := loadConfig()
	if err != nil {
		return err
	}
	cfg = c
	ac = newA2EClient(c)
	return nil
}

// respond sends an interaction response.
func respond(s *discordgo.Session, i *discordgo.InteractionCreate, content string) {
	content = truncateForDiscord(content)
	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Content: content},
	})
}

// respondDefer acknowledges first (for long operations).
func respondDefer(s *discordgo.Session, i *discordgo.InteractionCreate) {
	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	})
}

// editResponse edits a deferred response and logs errors centrally.
func editResponse(s *discordgo.Session, i *discordgo.InteractionCreate, content string) {
	c := truncateForDiscord(content)
	
	// Centralized Error Logging
	lower := strings.ToLower(content)
	if strings.Contains(lower, "error") || strings.Contains(lower, "failed") {
		log.Printf("API Error sent to user /%s: %s", i.ApplicationCommandData().Name, content) // <--- Fixed to use standard log.Printf
	}
	
	_, _ = s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{Content: &c})
}

// truncateForDiscord keeps replies under the Discord 2000-char limit.
func truncateForDiscord(s string) string {
	const max = 1990
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}

// helpText lists the a2e commands.
func helpText() string {
	return "🤖 **ElyA2E commands (now in ElyLightspeed)**\n" +
		"• `/a2e-credits` — show remaining A2E credits\n" +
		"• `/a2e-image` — text-to-image (default seedream)\n" +
		"• `/a2e-video` — Wan 3.0 video (async)\n" +
		"• `/a2e-tasks` — list recent image/video tasks\n" +
		"• `/a2e-tts` — text-to-speech (built-in or cloned voice)\n" +
		"• `/a2e-voices` — list cloned voices or built-ins\n" +
		"• `/a2e-voice-assign` — name a voice ID (local JSON)\n" +
		"• `/a2e-voice-clone` — clone a voice from MP3/URLs"
}

// getOpt is a tiny helper matching the main package's option reader.
func getOpt(opts []*discordgo.ApplicationCommandInteractionDataOption, key string) string {
	for _, o := range opts {
		if o.Name == key {
			return toString(o.Value)
		}
	}
	return ""
}

func toString(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}

```

## `./a2e/audio.go`

```go
package a2e

import (
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// downloadAudioHost is the working host for A2E TTS audio. A2E returns URLs on
// speech.downloadaivideo.com whose DNS is broken (global NXDOMAIN / CF 530);
// the same files are served by this host under a /speech/ path prefix.
// rewriteAudioHost maps one to the other.
const downloadAudioHost = "https://r2-download.makefun.ai"

// rewriteAudioHost rewrites an A2E speech.downloadaivideo.com URL to the
// working r2-download.makefun.ai host, adding the /speech/ path prefix that
// the mirror uses. Returns the input unchanged if it isn't on the broken host.
func rewriteAudioHost(u string) string {
	if strings.HasPrefix(u, "https://speech.downloadaivideo.com/") {
		return downloadAudioHost + "/speech/" + strings.TrimPrefix(u, "https://speech.downloadaivideo.com/")
	}
	return u
}

// httpClient for audio downloads (separate timeout; audio can be a few MB).
var audioHTTPClient = &http.Client{Timeout: 60 * time.Second}

// downloadAudio fetches the audio bytes at url, retrying briefly to account
// for the mirror host's propagation delay (a fresh file can 404 for a few
// seconds after generation before it appears on r2-download.makefun.ai).
func downloadAudio(url string) ([]byte, error) {
	var lastErr error
	for attempt := 1; attempt <= 4; attempt++ {
		data, err := fetchAudio(url)
		if err == nil {
			return data, nil
		}
		lastErr = err
		time.Sleep(time.Duration(attempt) * time.Second) // 1s, 2s, 3s
	}
	return nil, lastErr
}

func fetchAudio(url string) ([]byte, error) {
	resp, err := audioHTTPClient.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, resp.Body)
		return nil, &audioDownloadError{status: resp.StatusCode}
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	// Sanity: must be non-trivial audio.
	if len(data) == 0 {
		return nil, &audioDownloadError{status: 0}
	}
	log.Printf("[audio] downloaded %d bytes from %s", len(data), url)
	return data, nil
}

type audioDownloadError struct{ status int }

func (e *audioDownloadError) Error() string {
	if e.status == 0 {
		return "audio download returned empty body"
	}
	return "audio download HTTP " + strconv.Itoa(e.status)
}

```

## `./a2e/a2e.go`

```go
// a2e.go — minimal HTTP client for the A2E Developer API (https://video.a2e.ai).
// Covers the endpoint families the bot uses: credits, text-to-image,
// image-to-video, Wan 3.0 video, and generic task list/detail.
package a2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// a2eClient is a minimal HTTP client for the A2E API.
type a2eClient struct {
	BaseURL string
	APIKey  string
	HTTP    *http.Client
}

func newA2EClient(cfg *Config) *a2eClient {
	return &a2eClient{
		BaseURL: strings.TrimSuffix(cfg.A2EBaseURL, "/"),
		APIKey:  cfg.A2EAPIKey,
		HTTP:    &http.Client{Timeout: 120 * time.Second},
	}
}

func (c *a2eClient) authHeader() string { return "Bearer " + c.APIKey }

// doJSON sends a JSON request and decodes the response into out.
// The A2E API uses a wrapper envelope {code, data, ...}; most endpoints return
// HTTP 200 with code:0 on success. doJSON returns an error on HTTP errors and
// on non-zero A2E `code` fields.
func (c *a2eClient) doJSON(method, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.BaseURL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", c.authHeader())
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	bodyBytes, _ := io.ReadAll(resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("A2E API %s %s -> HTTP %d: %s", method, path, resp.StatusCode, truncate(string(bodyBytes), 400))
	}

	// Decode envelope to check code.
	var envelope struct {
		Code  int             `json:"code"`
		Data  json.RawMessage `json:"data"`
		Error string          `json:"error"`
	}
	if err := json.Unmarshal(bodyBytes, &envelope); err != nil {
		// Not an envelope — return raw body if out is a *string or *[]byte.
		if out != nil {
			if s, ok := out.(*string); ok {
				*s = string(bodyBytes)
				return nil
			}
			if b, ok := out.(*[]byte); ok {
				*b = bodyBytes
				return nil
			}
			return json.Unmarshal(bodyBytes, out)
		}
		return nil
	}
	if envelope.Code != 0 {
		return fmt.Errorf("A2E API %s %s -> code %d: %s", method, path, envelope.Code, envelope.Error)
	}
	if out != nil && len(envelope.Data) > 0 {
		return json.Unmarshal(envelope.Data, out)
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// --- Credits ---

// a2eCreditsRow is one credits-history transaction.
type a2eCreditsRow struct {
	ID          string `json:"_id"`
	Amount      int    `json:"amount"`
	CreatedAt   string `json:"createdAt"`
	Description string `json:"description"`
}

// GetCredits returns recent credit transactions (consumption history).
func (c *a2eClient) GetCredits(pageSize int) ([]a2eCreditsRow, error) {
	var data struct {
		Rows  []a2eCreditsRow `json:"rows"`
		Count int             `json:"count"`
	}
	err := c.doJSON("GET", fmt.Sprintf("/api/v1/transactionRecord/creditsHistory?pageNum=1&pageSize=%d&is_consumption=true", pageSize), nil, &data)
	return data.Rows, err
}

// creditHistoryPage fetches one page of creditsHistory for the given
// consumption flag and returns the rows and the server-reported total count.
func (c *a2eClient) creditHistoryPage(pageNum, pageSize int, consumption bool) ([]a2eCreditsRow, int, error) {
	var data struct {
		Rows  []a2eCreditsRow `json:"rows"`
		Count int             `json:"count"`
	}
	flag := "false"
	if consumption {
		flag = "true"
	}
	err := c.doJSON("GET", fmt.Sprintf("/api/v1/transactionRecord/creditsHistory?pageNum=%d&pageSize=%d&is_consumption=%s", pageNum, pageSize, flag), nil, &data)
	return data.Rows, data.Count, err
}

// CreditSummary holds the computed remaining-credit balance plus the raw
// income and consumption totals. There is no dedicated A2E balance endpoint,
// so the balance is derived by summing the full transaction history.
type CreditSummary struct {
	Balance  int // net credits remaining
	Income   int // total credits added (positive)
	Consumed int // total credits spent (negative sum)
	TxCount  int // total transactions
}

// GetCreditsBalance computes the remaining credit balance by fetching the
// full income + consumption history and summing the signed amounts.
func (c *a2eClient) GetCreditsBalance() (CreditSummary, error) {
	const pageSize = 100
	var summary CreditSummary

	// Consumption (negative amounts).
	page := 1
	for {
		rows, count, err := c.creditHistoryPage(page, pageSize, true)
		if err != nil {
			return summary, err
		}
		for _, r := range rows {
			summary.Consumed += r.Amount
			summary.TxCount++
		}
		if page*pageSize >= count || len(rows) == 0 {
			break
		}
		page++
	}

	// Income (positive amounts).
	page = 1
	for {
		rows, count, err := c.creditHistoryPage(page, pageSize, false)
		if err != nil {
			return summary, err
		}
		for _, r := range rows {
			summary.Income += r.Amount
			summary.TxCount++
		}
		if page*pageSize >= count || len(rows) == 0 {
			break
		}
		page++
	}

	summary.Balance = summary.Income + summary.Consumed
	return summary, nil
}

// --- Text to Image ---

// text2ImageStartReq is the payload for userText2Image/start.
type text2ImageStartReq struct {
	Prompt         string `json:"prompt"`
	ModelType      string `json:"model_type"`
	NegativePrompt string `json:"negative_prompt,omitempty"`
	Width          int    `json:"width,omitempty"`
	Height         int    `json:"height,omitempty"`
}

// StartText2Image kicks off a text-to-image task and returns the first task
// record from the response array (A2E returns data as an array of tasks).
func (c *a2eClient) StartText2Image(prompt, model string, width, height int, negative string) (map[string]any, error) {
	req := text2ImageStartReq{Prompt: prompt, ModelType: model, NegativePrompt: negative, Width: width, Height: height}
	var arr []map[string]any
	err := c.doJSON("POST", "/api/v1/userText2Image/start", req, &arr)
	if err != nil {
		return nil, err
	}
	if len(arr) > 0 {
		return arr[0], nil
	}
	return map[string]any{}, nil
}

// Text2ImageTask is a text-to-image task record.
type Text2ImageTask struct {
	ID            string `json:"_id"`
	Prompt        string `json:"prompt"`
	Status        string `json:"current_status"`
	ImageURL      string `json:"image_url"`
	ResultURL     string `json:"result_url"`
	FailedMessage string `json:"failed_message"`
}

// Text2ImageDetail fetches a single text-to-image task by _id.
func (c *a2eClient) Text2ImageDetail(id string) (map[string]any, error) {
	var out map[string]any
	err := c.doJSON("GET", "/api/v1/userText2Image/"+id, nil, &out)
	return out, err
}

// Text2ImageRecords lists text-to-image tasks.
func (c *a2eClient) Text2ImageRecords(pageSize int) ([]map[string]any, error) {
	var data struct {
		Rows []map[string]any `json:"rows"`
	}
	err := c.doJSON("GET", fmt.Sprintf("/api/v1/userText2Image/allRecords?pageNum=1&pageSize=%d", pageSize), nil, &data)
	return data.Rows, err
}

// --- Image to Video ---

type image2VideoStartReq struct {
	Prompt   string `json:"prompt"`
	Model    string `json:"model_type"`
	ImageURL string `json:"image_url,omitempty"`
	Ratio    string `json:"ratio,omitempty"`
}

// StartImage2Video kicks off an image-to-video task.
func (c *a2eClient) StartImage2Video(prompt, model, imageURL, ratio string) (map[string]any, error) {
	req := image2VideoStartReq{Prompt: prompt, Model: model, ImageURL: imageURL, Ratio: ratio}
	var out map[string]any
	err := c.doJSON("POST", "/api/v1/userImage2Video/start", req, &out)
	return out, err
}

// Image2VideoRecords lists image-to-video tasks.
func (c *a2eClient) Image2VideoRecords(pageSize int) ([]map[string]any, error) {
	var data struct {
		Rows []map[string]any `json:"rows"`
	}
	err := c.doJSON("GET", fmt.Sprintf("/api/v1/userImage2Video/allRecords?pageNum=1&pageSize=%d", pageSize), nil, &data)
	return data.Rows, err
}

// --- Wan 3.0 Video ---

type wan30Media struct {
	Type     string `json:"type"`
	URL      string `json:"url"`
	Duration int    `json:"duration,omitempty"`
}

type wan30Input struct {
	Prompt string       `json:"prompt"`
	Media  []wan30Media `json:"media,omitempty"`
}

type wan30Params struct {
	Resolution   string `json:"resolution,omitempty"`
	Ratio        string `json:"ratio,omitempty"`
	Duration     int    `json:"duration,omitempty"`
	Audio        bool   `json:"audio,omitempty"`
	PromptExtend bool   `json:"prompt_extend,omitempty"`
}

type wan30StartReq struct {
	Name       string      `json:"name"`
	Model      string      `json:"model"`
	Mode       string      `json:"mode"`
	Input      wan30Input  `json:"input"`
	Parameters wan30Params `json:"parameters"`
}

// StartWan30 kicks off a Wan 3.0 video task.
func (c *a2eClient) StartWan30(name, model, mode, prompt string, media []wan30Media, params wan30Params) (map[string]any, error) {
	req := wan30StartReq{
		Name:       name,
		Model:      model,
		Mode:       mode,
		Input:      wan30Input{Prompt: prompt, Media: media},
		Parameters: params,
	}
	var out map[string]any
	err := c.doJSON("POST", "/api/v1/userWan30/start", req, &out)
	return out, err
}

// Wan30Detail fetches a Wan 3.0 task by _id (start-only in spec, but detail is
// verified to work for polling).
func (c *a2eClient) Wan30Detail(id string) (map[string]any, error) {
	var out map[string]any
	err := c.doJSON("GET", "/api/v1/userWan30/"+id, nil, &out)
	return out, err
}

// --- Text to Speech (verified live 2026-09-24) ---

// UserVoice is a voice clone record (training or completed).
type UserVoice struct {
	ID            string   `json:"_id"`
	Name          string   `json:"name"`
	VoiceURLs     []string `json:"voice_urls"`
	Gender        string   `json:"gender"`
	Model         string   `json:"model"`
	Lang          string   `json:"lang"`
	Language      string   `json:"language"`
	CurrentStatus string   `json:"current_status"`
	SpeakerID     string   `json:"speaker_id"`
	Coins         float64  `json:"coins"`
	CreatedAt     string   `json:"createdAt"`
}

// TTS synthesizes speech and returns the audio URL. Provide exactly one of
// ttsID (system/built-in voice) or userVoiceID (cloned voice).
func (c *a2eClient) TTS(msg, ttsID, userVoiceID, country, region string) (string, error) {
	body := map[string]any{"msg": msg}
	if ttsID != "" {
		body["tts_id"] = ttsID
	}
	if userVoiceID != "" {
		body["user_voice_id"] = userVoiceID
	}
	if country != "" {
		body["country"] = country
	}
	if region != "" {
		body["region"] = region
	}
	var out string
	err := c.doJSON("POST", "/api/v1/video/send_tts", body, &out)
	return out, err
}

// CompletedVoices lists the user's completed cloned voices.
func (c *a2eClient) CompletedVoices() ([]UserVoice, error) {
	var data []UserVoice
	err := c.doJSON("GET", "/api/v1/userVoice/completedRecord", nil, &data)
	return data, err
}

// TrainingVoices lists the user's in-progress/failed voice training records.
func (c *a2eClient) TrainingVoices() ([]UserVoice, error) {
	var data []UserVoice
	err := c.doJSON("GET", "/api/v1/userVoice/trainingRecord", nil, &data)
	return data, err
}

// StartVoiceClone kicks off a voice-clone training task.
func (c *a2eClient) StartVoiceClone(name string, urls []string, gender, model string) (map[string]any, error) {
	body := map[string]any{
		"name":       name,
		"voice_urls": urls,
	}
	if gender != "" {
		body["gender"] = gender
	}
	if model != "" {
		body["model"] = model
	}
	var out map[string]any
	err := c.doJSON("POST", "/api/v1/userVoice/training", body, &out)
	return out, err
}

// BuiltinVoice is a preset TTS voice from anchor/voice_list.
type BuiltinVoice struct {
	Label   string `json:"label"`
	Value   string `json:"value"`
	TTSRate int    `json:"ttsRate"`
}

// VoiceGroup is a gender group in anchor/voice_list (female/male).
type VoiceGroup struct {
	Label    string         `json:"label"`
	Value    string         `json:"value"`
	Children []BuiltinVoice `json:"children"`
}

// BuiltinVoices lists preset TTS voices grouped by gender.
func (c *a2eClient) BuiltinVoices() ([]VoiceGroup, error) {
	var data []VoiceGroup
	err := c.doJSON("GET", "/api/v1/anchor/voice_list", nil, &data)
	return data, err
}

```

## `./a2e/handlers.go`

```go
package a2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
)

// handleCredits: /credits — show remaining A2E credit balance + recent activity.
func handleCredits(s *discordgo.Session, i *discordgo.InteractionCreate, get func(string) string) {
	limit := 5
	if v := get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 20 {
			limit = n
		}
	}

	// Compute the remaining balance from the full transaction history.
	summary, err := ac.GetCreditsBalance()
	if err != nil {
		respond(s, i, "❌ Credits lookup failed: "+err.Error())
		return
	}

	rows, err := ac.GetCredits(limit)
	if err != nil {
		respond(s, i, "❌ Credits lookup failed: "+err.Error())
		return
	}

	var b strings.Builder
	b.WriteString("💳 **A2E credit balance**\n")
	b.WriteString(fmt.Sprintf("**%d credits remaining**\n", summary.Balance))
	b.WriteString(fmt.Sprintf("_Lifetime: +%d added · %d spent · %d transactions_\n", summary.Income, -summary.Consumed, summary.TxCount))

	if len(rows) > 0 {
		b.WriteString("\n**Recent activity**\n")
		for _, r := range rows {
			ts := ""
			if t, err := time.Parse(time.RFC3339, r.CreatedAt); err == nil {
				ts = t.Format("2006-01-02 15:04")
			}
			sign := "+"
			amt := r.Amount
			if amt < 0 {
				sign = "-"
				amt = -amt
			}
			b.WriteString(fmt.Sprintf("`%s` **%s%d** · %s\n", ts, sign, amt, r.Description))
		}
	}
	respond(s, i, b.String())
}

// handleImage: /image — text-to-image generation.
func handleImage(s *discordgo.Session, i *discordgo.InteractionCreate, get func(string) string) {
	prompt := get("prompt")
	if prompt == "" {
		respond(s, i, "Please provide an image prompt.")
		return
	}
	model := get("model")
	usedDefault := false
	if model == "" {
		model = cfg.DefaultImageModel
		usedDefault = true
	}
	width, _ := strconv.Atoi(get("width"))
	height, _ := strconv.Atoi(get("height"))
	negative := get("negative")

	respondDefer(s, i)

	log.Printf("[image] generating with model=%s prompt=%q", model, truncate(prompt, 100))
	task, err := ac.StartText2Image(prompt, model, width, height, negative)
	if err != nil {
		editResponse(s, i, "❌ Image generation failed: "+err.Error())
		return
	}

	// The start response may carry an _id (for polling) and/or an image URL.
	id := mapString(task, "_id")
	imgURL := firstImageURL(task)

	// If there's an _id but no immediate image, poll briefly for completion.
	if id != "" && imgURL == "" {
		imgURL = pollForImage(id, 20, 3) // up to 60s
	}

	if imgURL != "" {
		note := ""
		if usedDefault {
			note = "\n_Model defaulted to `seedream` (omit model to use it)._"
		}
		editResponse(s, i, fmt.Sprintf("🖼️ **Image generated** (model `%s`)\n%s%s", model, imgURL, note))
		return
	}

	// No image yet — return task id/status for manual tracking.
	status := mapString(task, "current_status")
	if status == "" {
		status = "queued"
	}
	editResponse(s, i, fmt.Sprintf("🖼️ Image task started (model `%s`).\n- Task ID: `%s`\n- Status: `%s`\nUse `/tasks type:image` to check.", model, orVal(id, "unknown"), status))
}

// pollForImage polls a text-to-image task detail until an image URL appears or
// the attempt budget runs out.
func pollForImage(id string, attempts, intervalSec int) string {
	for a := 0; a < attempts; a++ {
		detail, err := ac.Text2ImageDetail(id)
		if err == nil {
			if u := firstImageURL(detail); u != "" {
				return u
			}
		}
		time.Sleep(time.Duration(intervalSec) * time.Second)
	}
	return ""
}

// firstImageURL extracts the first usable image URL from a task record, checking
// both single-string fields and the image_urls array.
func firstImageURL(m map[string]any) string {
	if m == nil {
		return ""
	}
	for _, k := range []string{"image_url", "result_url", "url", "image"} {
		if v := mapString(m, k); v != "" && strings.HasPrefix(v, "http") {
			return v
		}
	}
	// image_urls is an array of URL strings (or objects with url).
	if arr, ok := m["image_urls"].([]any); ok {
		for _, item := range arr {
			switch t := item.(type) {
			case string:
				if strings.HasPrefix(t, "http") {
					return t
				}
			case map[string]any:
				if u := mapString(t, "url"); u != "" && strings.HasPrefix(u, "http") {
					return u
				}
			}
		}
	}
	return ""
}

// handleVideo: /video — Wan 3.0 video generation (async).
func handleVideo(s *discordgo.Session, i *discordgo.InteractionCreate, get func(string) string) {
	prompt := get("prompt")
	if prompt == "" {
		respond(s, i, "Please provide a video prompt.")
		return
	}
	model := get("model")
	if model == "" {
		model = cfg.DefaultVideoModel
	}
	mode := get("mode")
	if mode == "" {
		mode = "text-to-video"
	}
	resolution := get("resolution")
	if resolution == "" {
		resolution = "480P"
	}
	ratio := get("ratio")
	if ratio == "" {
		ratio = "16:9"
	}
	duration := 10
	if v := get("duration"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			duration = n
		}
	}
	audio := true
	if v := get("audio"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			audio = b
		}
	}

	respondDefer(s, i)

	log.Printf("[video] generating model=%s mode=%s prompt=%q", model, mode, truncate(prompt, 100))
	task, err := ac.StartWan30("ElyA2E video", model, mode, prompt, nil, wan30Params{
		Resolution: resolution,
		Ratio:      ratio,
		Duration:   duration,
		Audio:      audio,
	})
	if err != nil {
		editResponse(s, i, "❌ Video generation failed: "+err.Error())
		return
	}

	id := mapString(task, "_id")
	status := mapString(task, "current_status")
	if status == "" {
		status = "queued"
	}
	editResponse(s, i, fmt.Sprintf("🎬 **Video task started** (model `%s`, %s, %s, %ds)\n- Task ID: `%s`\n- Status: `%s`\n\nWan 3.0 can take several minutes. Check with `/tasks type:video`.", model, resolution, ratio, duration, orVal(id, "unknown"), status))
}

// handleTasks: /tasks — list recent image or video tasks.
func handleTasks(s *discordgo.Session, i *discordgo.InteractionCreate, get func(string) string) {
	kind := strings.ToLower(get("type"))
	limit := 5
	if v := get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 20 {
			limit = n
		}
	}

	var rows []map[string]any
	var err error
	switch kind {
	case "image":
		rows, err = ac.Text2ImageRecords(limit)
	case "video":
		rows, err = ac.Image2VideoRecords(limit)
	default:
		respond(s, i, "Use `/tasks type:image` or `/tasks type:video`.")
		return
	}
	if err != nil {
		respond(s, i, "❌ Task lookup failed: "+err.Error())
		return
	}
	if len(rows) == 0 {
		respond(s, i, fmt.Sprintf("No recent **%s** tasks found.", kind))
		return
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("🗂️ **Recent %s tasks**\n", kind))
	for _, r := range rows {
		id := mapString(r, "_id")
		status := mapString(r, "current_status")
		if status == "" {
			status = mapString(r, "status")
		}
		desc := mapString(r, "prompt")
		if desc == "" {
			desc = mapString(r, "name")
		}
		desc = truncate(desc, 50)
		b.WriteString(fmt.Sprintf("• `%s` · **%s** · %s\n", id, orVal(status, "?"), orVal(desc, "")))
	}
	respond(s, i, b.String())
}

// --- helpers ---

func mapString(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', 0, 64)
	case json.Number:
		return t.String()
	}
	return fmt.Sprintf("%v", v)
}

// handleTTS: /tts <text> [voice] [country] [region]
// voice may be a built-in voice ID, a cloned voice ID, or a friendly name
// assigned in data/voice-assignments.json.
func handleTTS(s *discordgo.Session, i *discordgo.InteractionCreate, get func(string) string) {
	text := strings.TrimSpace(get("text"))
	if text == "" {
		respond(s, i, "Please provide some text to speak.")
		return
	}
	voice := strings.TrimSpace(get("voice"))
	country := strings.TrimSpace(get("country"))
	region := strings.TrimSpace(get("region"))

	// Determine which voice to use.
	var ttsID, userVoiceID string
	if voice != "" {
		va, err := loadVoiceAssignments()
		if err != nil {
			respond(s, i, "⚠️ Could not read voice assignments: "+err.Error())
			return
		}
		// Reverse lookup: find the ID whose friendly name == voice (or raw ID).
		exactID := ""
		for id, name := range va.Names {
			if name == voice || id == voice {
				exactID = id
				break
			}
		}
		if exactID != "" && !isValidVoiceID(sanitizeVoiceID(exactID)) {
			// Corrupt assignment entry (e.g. "id name" concatenated) — skip it.
			exactID = ""
		}
		if exactID != "" {
			exactID = sanitizeVoiceID(exactID)
			// It's a cloned voice (has an assignment) — but could still be a
			// built-in. Distinguish by checking completed voices.
			completed, _ := ac.CompletedVoices()
			isClone := false
			for _, v := range completed {
				if v.ID == exactID {
					isClone = true
					break
				}
			}
			if isClone {
				userVoiceID = exactID
			} else {
				ttsID = exactID
			}
		} else {
			// Prefix match against cloned voices and built-in voices.
			completed, _ := ac.CompletedVoices()
			for _, v := range completed {
				if strings.HasPrefix(v.ID, voice) || strings.EqualFold(v.Name, voice) {
					userVoiceID = v.ID
					break
				}
			}
			if userVoiceID == "" {
				groups, _ := ac.BuiltinVoices()
				for _, g := range groups {
					for _, v := range g.Children {
						if v.Value == voice || strings.HasPrefix(v.Value, voice) || strings.EqualFold(v.Label, voice) {
							ttsID = v.Value
							break
						}
					}
					if ttsID != "" {
						break
					}
				}
			}
		}
		if userVoiceID == "" && ttsID == "" {
			respond(s, i, fmt.Sprintf("❌ Could not find voice matching `%s`. Use `/voices` to see available voices.", voice))
			return
		}
	}

	respondDefer(s, i)
	log.Printf("[tts] voice=%q user_voice=%q len=%d", ttsID, userVoiceID, len(text))
	audioURL, err := ac.TTS(text, ttsID, userVoiceID, country, region)
	if err != nil {
		editResponse(s, i, "❌ TTS failed: "+err.Error())
		return
	}
	voiceLabel := voice
	if voice == "" {
		voiceLabel = "(default)"
	}

	// Try to download the audio and post it as a Discord file attachment.
	// A2E returns URLs on speech.downloadaivideo.com whose DNS is broken;
	// the same file is served by r2-download.makefun.ai. Rewrite the host and
	// download from there so the audio always plays, then attach it.
	downloadURL := rewriteAudioHost(audioURL)
	data, err := downloadAudio(downloadURL)
	if err == nil {
		filename := fmt.Sprintf("a2e_tts_%s.mp3", i.ID)
		_, sendErr := s.ChannelMessageSendComplex(i.ChannelID, &discordgo.MessageSend{
			Content: fmt.Sprintf("🔊 **TTS** (voice `%s`)", voiceLabel),
			Files:   []*discordgo.File{{Name: filename, Reader: bytes.NewReader(data)}},
		})
		if sendErr == nil {
			editResponse(s, i, fmt.Sprintf("🔊 **TTS** (voice `%s`) ✅", voiceLabel))
			return
		}
		log.Printf("[tts] file post failed (%v); falling back to URL", sendErr)
	}

	// Fallback: reply with the URL.
	editResponse(s, i, fmt.Sprintf("🔊 **TTS** (voice `%s`)\n%s\n\n_Note: audio host may be unreachable; try the A2E site if the link is dead._", voiceLabel, audioURL))
}

// handleVoices: /voices — list the user's cloned voices and built-in voices.
func handleVoices(s *discordgo.Session, i *discordgo.InteractionCreate, get func(string) string) {
	which := strings.ToLower(get("type"))
	if which == "" {
		which = "mine"
	}

	va, err := loadVoiceAssignments()
	if err != nil {
		respond(s, i, "⚠️ Could not read voice assignments: "+err.Error())
		return
	}

	switch which {
	case "mine", "cloned", "my":
		completed, err := ac.CompletedVoices()
		if err != nil {
			respond(s, i, "❌ Failed to fetch cloned voices: "+err.Error())
			return
		}
		training, _ := ac.TrainingVoices()
		respond(s, i, va.formatCompleted(completed, training))
	case "builtin", "system", "preset":
		groups, err := ac.BuiltinVoices()
		if err != nil {
			respond(s, i, "❌ Failed to fetch built-in voices: "+err.Error())
			return
		}
		respond(s, i, va.formatBuiltin(groups))
	default:
		respond(s, i, "Use `/voices type:mine` (your clones) or `/voices type:builtin` (presets).")
	}
}

// handleVoiceAssign: /voice-assign <action:add|delete|view> [id] [name]
// Manages friendly names for voice IDs in the local JSON file.
func handleVoiceAssign(s *discordgo.Session, i *discordgo.InteractionCreate, get func(string) string) {
	action := strings.ToLower(strings.TrimSpace(get("action")))
	if action == "" {
		action = "add"
	}

	va, err := loadVoiceAssignments()
	if err != nil {
		respond(s, i, "⚠️ Could not read voice assignments: "+err.Error())
		return
	}

	switch action {
	case "add", "set":
		id := sanitizeVoiceID(get("id"))
		name := strings.TrimSpace(get("name"))
		if id == "" || name == "" {
			respond(s, i, "Usage: `/voice-assign action:add id:<voice_id> name:<friendly name>`")
			return
		}
		if !isValidVoiceID(id) {
			respond(s, i, fmt.Sprintf("❌ `%s` is not a valid voice ID. Voice IDs are 24-character hex strings — grab one from `/voices`.", id))
			return
		}
		if err := va.assignName(id, name); err != nil {
			respond(s, i, "❌ Failed to save assignment: "+err.Error())
			return
		}
		respond(s, i, fmt.Sprintf("✅ Assigned name **%s** to voice `%s`.\nSaved to `data/voice-assignments.json`.\nNow use `/tts text:... voice:%s`.", name, id, name))

	case "delete", "del", "remove":
		id := sanitizeVoiceID(get("id"))
		name := strings.TrimSpace(get("name"))
		// Support delete by ID or by friendly name.
		var targetID string
		if id != "" {
			targetID = id
		} else if name != "" {
			for vid, n := range va.Names {
				if strings.EqualFold(n, name) {
					targetID = vid
					break
				}
			}
			if targetID == "" {
				respond(s, i, fmt.Sprintf("❌ No voice assigned the name **%s**. Use `/voice-assign action:view` to see assignments.", name))
				return
			}
		} else {
			respond(s, i, "Usage: `/voice-assign action:delete id:<voice_id>` or `action:delete name:<assigned_name>`")
			return
		}
		if _, ok := va.Names[targetID]; !ok {
			respond(s, i, fmt.Sprintf("❌ Voice `%s` has no assignment. Use `/voice-assign action:view` to see assignments.", targetID))
			return
		}
		removed := va.Names[targetID]
		delete(va.Names, targetID)
		if err := va.save(); err != nil {
			respond(s, i, "❌ Failed to save: "+err.Error())
			return
		}
		respond(s, i, fmt.Sprintf("🗑️ Removed name **%s** from voice `%s`.", removed, targetID))

	case "view", "list", "show":
		if len(va.Names) == 0 {
			respond(s, i, "No voice assignments yet. Use `/voice-assign action:add id:<voice_id> name:<name>` to add one.")
			return
		}
		var b strings.Builder
		b.WriteString("🏷️ **Voice assignments** (data/voice-assignments.json)\n")
		for id, n := range va.Names {
			b.WriteString(fmt.Sprintf("• **%s** → `%s`\n", n, id))
		}
		respond(s, i, b.String())

	default:
		respond(s, i, "Unknown action. Use `/voice-assign action:add|delete|view`.")
	}
}

// handleVoiceClone: /voice-clone <name> [urls] [gender] [model] — start voice
// cloning from audio sample URLs or an attached MP3 file.
func handleVoiceClone(s *discordgo.Session, i *discordgo.InteractionCreate, get func(string) string) {
	name := strings.TrimSpace(get("name"))
	if name == "" {
		respond(s, i, "Usage: `/voice-clone name:<name> urls:<url1,url2,...>` or attach an MP3.")
		return
	}
	urlsRaw := strings.TrimSpace(get("urls"))
	gender := strings.TrimSpace(get("gender"))
	if gender == "" {
		gender = "female"
	}
	model := strings.TrimSpace(get("model"))
	if model == "" {
		model = "a2e"
	}

	var urls []string
	for _, u := range strings.Split(urlsRaw, ",") {
		u = strings.TrimSpace(u)
		if u != "" {
			urls = append(urls, u)
		}
	}

	// If no URLs given, use the mp3 attachment option (if provided).
	if len(urls) == 0 {
		attach := getAttachment(i, get("mp3"))
		if attach == nil {
			respond(s, i, "Please provide audio via the **mp3** attachment option (`/voice-clone name:... mp3:<file>`) **or** with `urls:`.")
			return
		}
		// Only accept audio attachments.
		if attach.ContentType != "" && !strings.HasPrefix(attach.ContentType, "audio/") {
			respond(s, i, fmt.Sprintf("❌ Attached file `%s` is `%s`, not audio. Attach an MP3.", attach.Filename, attach.ContentType))
			return
		}
		log.Printf("[voice-clone] using attached file %s (%s, %d bytes)", attach.Filename, attach.ContentType, attach.Size)
		urls = append(urls, attach.URL)
	}

	if len(urls) == 0 {
		respond(s, i, "Please provide at least one audio sample URL.")
		return
	}
	respondDefer(s, i)
	log.Printf("[voice-clone] name=%q model=%s gender=%s samples=%d", name, model, gender, len(urls))
	task, err := ac.StartVoiceClone(name, urls, gender, model)
	if err != nil {
		editResponse(s, i, "❌ Voice cloning failed: "+err.Error())
		return
	}
	id := mapString(task, "_id")
	status := mapString(task, "current_status")
	if status == "" {
		status = "training"
	}
	editResponse(s, i, fmt.Sprintf("🎙️ **Voice clone started** — **%s**\n- Task ID: `%s`\n- Status: `%s`\n- Model: `%s`, %s\n\nIt may take a few minutes. Check with `/voices`.", name, orVal(id, "unknown"), status, model, gender))
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// getAttachment returns the MessageAttachment referenced by an attachment
// option. The attachment option's Value is the attachment ID; it's resolved
// via the interaction's Resolved.Attachments map. Returns nil if none.
func getAttachment(i *discordgo.InteractionCreate, attachmentID string) *discordgo.MessageAttachment {
	if i == nil || i.Interaction == nil || i.Interaction.Data == nil {
		return nil
	}
	// Data is a value ApplicationCommandInteractionData (not a pointer) when
	// the interaction is an application command.
	app, ok := i.Interaction.Data.(discordgo.ApplicationCommandInteractionData)
	if !ok || app.Resolved == nil {
		return nil
	}
	if attachmentID != "" {
		if a, ok := app.Resolved.Attachments[attachmentID]; ok && a != nil {
			return a
		}
	}
	// Fallback: return the first resolved attachment (if any).
	for _, a := range app.Resolved.Attachments {
		if a != nil {
			return a
		}
	}
	return nil
}

func orVal(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

```

## `./a2e/a2e_sub_test.go`

```go
package a2e

import "testing"

func TestRewriteAudioHost(t *testing.T) {
	cases := []struct{ in, want string }{
		{
			"https://speech.downloadaivideo.com/1day/fa/stable/abc.mp3",
			"https://r2-download.makefun.ai/speech/1day/fa/stable/abc.mp3",
		},
		{
			"https://other.host.com/x.mp3",
			"https://other.host.com/x.mp3",
		},
		{"", ""},
	}
	for _, c := range cases {
		if got := rewriteAudioHost(c.in); got != c.want {
			t.Errorf("rewriteAudioHost(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestIsValidVoiceID(t *testing.T) {
	if !isValidVoiceID("6ab51675c9eed119d535cc23") {
		t.Error("valid id rejected")
	}
	for _, bad := range []string{"", "short", "6ab51675c9eed119d535cc2g", "6ab51675c9eed119d535cc23 name"} {
		if isValidVoiceID(bad) {
			t.Errorf("invalid id accepted: %q", bad)
		}
	}
}

func TestSanitizeVoiceID(t *testing.T) {
	if got := sanitizeVoiceID("6ab51675c9eed119d535cc23 name"); got != "6ab51675c9eed119d535cc23" {
		t.Errorf("sanitize = %q", got)
	}
}

```

## `./a2e/commands.go`

```go
package a2e

import (
	"fmt"
	"github.com/bwmarrin/discordgo"
)

type CommandSpec struct {
	Command *discordgo.ApplicationCommand
	Handler func(s *discordgo.Session, i *discordgo.InteractionCreate)
}

func CommandSpecs() []CommandSpec {
	return []CommandSpec{
		{
			Command: &discordgo.ApplicationCommand{
				Name:        "a2e",
				Description: "A2E AI commands (image, video, tts, clone, tasks, etc.)",
				Options: []*discordgo.ApplicationCommandOption{
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "image",
						Description: "Generate an image from text (default model: seedream)",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionString, Name: "prompt", Description: "Image prompt", Required: true},
							{Type: discordgo.ApplicationCommandOptionString, Name: "model", Description: "Image model (default from config)"},
							{Type: discordgo.ApplicationCommandOptionString, Name: "size", Description: "Size e.g. 1024x1024"},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "video",
						Description: "Generate a video using Wan 3.0 (runs asynchronously)",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionString, Name: "prompt", Description: "Video prompt", Required: true},
							{Type: discordgo.ApplicationCommandOptionString, Name: "model", Description: "Video model (default from config)"},
							{Type: discordgo.ApplicationCommandOptionString, Name: "aspect_ratio", Description: "16:9 | 9:16 | 1:1"},
							{Type: discordgo.ApplicationCommandOptionString, Name: "duration", Description: "Duration e.g. 5s"},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "tts",
						Description: "Generate speech using built-in A2E voices or your cloned voices",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionString, Name: "text", Description: "Text to speak", Required: true},
							{Type: discordgo.ApplicationCommandOptionString, Name: "voice", Description: "Voice id or cloned-voice name"},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "voice-clone",
						Description: "Clone a custom voice from an attached MP3 or audio URLs",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionString, Name: "name", Description: "Name for the cloned voice", Required: true},
							{Type: discordgo.ApplicationCommandOptionAttachment, Name: "mp3", Description: "Audio sample to clone from (MP3/WAV/FLAC/M4A)"},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "voice-assign",
						Description: "Assign a friendly name to a raw voice ID (saves to local JSON)",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionString, Name: "name", Description: "The friendly name to assign", Required: true},
							{Type: discordgo.ApplicationCommandOptionString, Name: "id", Description: "The raw A2E voice ID", Required: true},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "voices",
						Description: "List your custom cloned voices and A2E's built-in voice presets",
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "tasks",
						Description: "List recent asynchronous image and video generation tasks",
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "credits",
						Description: "Show remaining A2E account credits",
					},
				},
			},
			Handler: handleA2ERoot,
		},
	}
}

func buildGet(subCommand *discordgo.ApplicationCommandInteractionDataOption) func(string) string {
	return func(key string) string {
		for _, o := range subCommand.Options {
			if o.Name == key {
				if s, ok := o.Value.(string); ok {
					return s
				}
				return fmt.Sprintf("%v", o.Value)
			}
		}
		return ""
	}
}

func handleA2ERoot(s *discordgo.Session, i *discordgo.InteractionCreate) {
	data := i.ApplicationCommandData()
	if len(data.Options) == 0 {
		return
	}
	subCommand := data.Options[0]
	get := buildGet(subCommand)

	switch subCommand.Name {
	case "image":
		handleImage(s, i, get)
	case "video":
		handleVideo(s, i, get)
	case "tts":
		handleTTS(s, i, get)
	case "voice-clone":
		handleVoiceClone(s, i, get)
	case "voice-assign":
		handleVoiceAssign(s, i, get)
	case "voices":
		handleVoices(s, i, get)
	case "tasks":
		handleTasks(s, i, get)
	case "credits":
		handleCredits(s, i, get)
	}
}

```

## Complete Directory Listing

```text
Sunfire/
    .gitignore
    README.md
    Sunfire
    Sunfire_AVATAR.jpg
    Sunfire_BANNER.png
    Sunfire_codebase.md
    analytics.go
    anyapi.go
    automod.go
    automod_test.go
    backup.go
    battle.go
    birthdays.go
    bot.go
    bot_event_test.go
    command_access.go
    command_access_test.go
    commands.go
    commands_test.go
    config.go
    config_test.go
    dice.go
    discord_logger.go
    emojigg.go
    emojigg_test.go
    fallen_star.go
    go.mod
    go.sum
    guild_config.go
    health_checker.go
    help_test.go
    hermes_exec_test.go
    iahandlers.go
    internetarchive.go
    jokequery.go
    lyrics.go
    lyrics_test.go
    main.go
    media.go
    media_logger.go
    media_logger_download_test.go
    media_logger_test.go
    media_test.go
    message_log.go
    message_log_queue_test.go
    message_log_test.go
    message_logger.go
    message_logger_test.go
    moderation.go
    moderation_test.go
    notes.go
    notifier_delivery_test.go
    notifier_freestuff.go
    notifier_hermes.go
    notifier_hermes_test.go
    notifier_kick.go
    notifier_movies.go
    notifier_scrape.go
    notifier_spotify.go
    notifier_test_seams.go
    notifier_youtube.go
    openrouter.go
    openrouter_test.go
    paths.go
    pick.go
    push.sh
    reminders.go
    reminders_test.go
    riddles.go
    roles.go
    searchlogs.go
    serverstats.go
    service.go
    service_test.go
    sysinfo.go
    sysinfo_unix.go
    sysinfo_windows.go
    tickets.go
    timequery.go
    translate.go
    userinfo_test.go
    Data/
        freeStuffNotifierCache.json
        hermes_tips.json
        kickNotifier-cache.json
        moviesThisWeekCache.json
        spotifyReleasesCache.json
        youtubeMonitorCache.json
        Backups/
            1557930395868209162/
                backup_20261027_195454.json
        MediaLogs/
            1557930395868209162/
                audio/
                    20261027_033724_1564483128586936353_1564483128792318083_groq_speech_1564483124350689310.wav
                images/
                    20261027_033624_1564482874940325970_1564482875091587142_cloudflare_image_1564482864697835640.jpg
                    20261027_035446_1564487498124759050_1564487497751339068_cloudflare_image_1564487487852650558.jpg
                videos/
                    20261027_190835_1564717469158088865_1564715760482525275_OpenRouter_FINAL_16x9_Captions.mov
        MessageLogs/
            1557930395868209162/
                1553668018641436702.json
                1557926505751781507.json
                1558612852355371018.json
                1563628032671883384.json
                1563634315521163404.json
                1565854789026455622.json
    a2e/
        a2e.go
        a2e_sub_test.go
        audio.go
        commands.go
        config.go
        handlers.go
        helpers.go
        voiceassign.go
    deploy/
        sunfire.service
    modules/
        README.md
        venice.go
        audio/
            audio.go
        augment/
            augment.go
        billing/
            billing.go
        characters/
            characters.go
        chat/
            chat.go
        embeddings/
            embeddings.go
        image/
            image.go
        models/
            models.go
        video/
            video.go
    providers/
        client.go
        commands.go
        config.go
        handlers.go
        helpers.go
        providers_api.go
        providers_test.go
    skills/
        software-development/
            go-discord-bot-development/
                references/
                    discord-bot-file-and-purge.md
    venice/
        clonedvoices.go
        commands.go
        config.go
        handlers.go
        handlers2.go
        helpers.go
        lyrics.go
        lyricslog.go
        venice.go
        venice_sub_test.go
```
