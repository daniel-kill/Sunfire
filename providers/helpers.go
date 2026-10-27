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
