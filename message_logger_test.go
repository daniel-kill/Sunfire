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
