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
