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
