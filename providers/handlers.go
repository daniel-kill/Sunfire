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
