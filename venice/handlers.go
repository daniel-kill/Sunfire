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
