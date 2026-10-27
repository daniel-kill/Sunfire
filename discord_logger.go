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
	cid := channelID()
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
