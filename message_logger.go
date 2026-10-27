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
