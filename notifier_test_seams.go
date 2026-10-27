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
