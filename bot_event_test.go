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
