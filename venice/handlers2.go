// handlers2.go — handlers for the expanded ElyVenice command set, using the
// reusable modules library (enit-sunfire-bot/modules).
//
// Adds: /stt, /search, /scrape, /video, /embed, /characters, /upscale.
package venice

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
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

// ---- helpers -----------------------------------------------------------------

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

// ---- /stt: transcribe an attached audio file ---------------------------------

func handleSTT(s *discordgo.Session, i *discordgo.InteractionCreate, get func(string) string) {
	data, name, err := attachmentBytes(s, i)
	if err != nil {
		respond(s, i, "Please attach an audio file to transcribe. "+err.Error())
		return
	}
	if len(data) == 0 {
		respond(s, i, "The attached file is empty.")
		return
	}
	respondDefer(s, i)

	model := get("model")
	if model == "" {
		model = "openai/whisper-large-v3"
	}
	req := &audio.TranscriptionRequest{
		Model:          model,
		File:           venice.MultipartFile{Name: name, Data: data},
		ResponseFormat: "json",
	}
	if lang := get("language"); lang != "" {
		req.Language = lang
	}
	out, err := mc.Audio.Transcribe(bg, req)
	if err != nil {
		editResponse(s, i, "Transcription error: "+err.Error())
		return
	}
	text := strings.TrimSpace(out.Text)
	if text == "" {
		editResponse(s, i, "(no speech detected)")
		return
	}
	editResponse(s, i, fmt.Sprintf("📝 **Transcription** (`%s`)\n%s", model, text))
}

// ---- /search: web search ------------------------------------------------------

func handleSearch(s *discordgo.Session, i *discordgo.InteractionCreate, get func(string) string) {
	query := strings.TrimSpace(get("query"))
	if query == "" {
		respond(s, i, "Please provide a search query.")
		return
	}
	respondDefer(s, i)

	req := &augment.SearchRequest{Query: query}
	if n := get("max_results"); n != "" {
		var v int
		fmt.Sscanf(n, "%d", &v)
		if v > 0 {
			req.MaxResults = &v
		}
	}
	if p := get("provider"); p != "" {
		req.Provider = p
	}
	res, err := mc.Augment.Search(bg, req)
	if err != nil {
		editResponse(s, i, "Search error: "+err.Error())
		return
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("🔎 **Web search** — `%s`\n\n", query))
	if len(res.Results) == 0 {
		sb.WriteString("No results.")
	} else {
		max := len(res.Results)
		if max > 8 {
			max = 8
		}
		for _, r := range res.Results[:max] {
			sb.WriteString(fmt.Sprintf("**%d.** [%s](%s)\n", r.Index+1, truncateForDiscord(r.Title), r.URL))
			if r.Description != "" {
				sb.WriteString(truncateForDiscord(r.Description) + "\n")
			} else if r.Snippet != "" {
				sb.WriteString(truncateForDiscord(r.Snippet) + "\n")
			}
			sb.WriteString("\n")
		}
	}
	editResponse(s, i, truncateForDiscord(sb.String()))
}

// ---- /scrape: scrape a URL to markdown ----------------------------------------

func handleScrape(s *discordgo.Session, i *discordgo.InteractionCreate, get func(string) string) {
	target := strings.TrimSpace(get("url"))
	if target == "" {
		respond(s, i, "Please provide a URL to scrape.")
		return
	}
	if !strings.HasPrefix(target, "http") {
		target = "https://" + target
	}
	respondDefer(s, i)

	text, err := mc.Augment.Scrape(bg, target)
	if err != nil {
		editResponse(s, i, "Scrape error: "+err.Error())
		return
	}
	if strings.TrimSpace(text) == "" {
		editResponse(s, i, "(page returned no extractable text)")
		return
	}
	editResponse(s, i, fmt.Sprintf("📄 **Scraped** %s\n\n%s", target, truncateForDiscord(text)))
}

// ---- /video: generate a video (async) ------------------------------------------

// videoResolutions is the set the API accepts for resolution.
var videoResolutions = map[string]bool{"1080p": true, "720p": true, "480p": true}

// videoAspectRatios is the set the API accepts for aspect_ratio.
var videoAspectRatios = map[string]bool{"21:9": true, "16:9": true, "4:3": true, "1:1": true, "3:4": true, "9:16": true}

// normalizeVideoParam clamps a user-supplied value to a known set; returns ""
// when empty/invalid so a default applies.
func normalizeVideoParam(v string, valid map[string]bool) string {
	v = strings.ToLower(strings.TrimSpace(v))
	if valid[v] {
		return v
	}
	return ""
}

func handleVideo(s *discordgo.Session, i *discordgo.InteractionCreate, get func(string) string) {
	prompt := strings.TrimSpace(get("prompt"))
	if prompt == "" {
		respond(s, i, "Please provide a video prompt.")
		return
	}
	model := get("model")
	if model == "" {
		model = "seedance-2-5-text-to-video-basic" // live catalog (verified 2026-09-21)
	}
	// aspect_ratio is REQUIRED by the API; default to 16:9 landscape.
	aspect := normalizeVideoParam(get("aspect_ratio"), videoAspectRatios)
	if aspect == "" {
		aspect = "16:9"
	}
	// resolution must be one of 1080p|720p|480p.
	resolution := normalizeVideoParam(get("resolution"), videoResolutions)
	if resolution == "" {
		resolution = "1080p"
	}

	// Strict Duration Sanitizer: Pull only the digits from the input
	durationRaw := strings.TrimSpace(get("duration"))
	var digits []rune
	for _, r := range durationRaw {
		if r >= '0' && r <= '9' {
			digits = append(digits, r)
		}
	}
	duration := "5s" // Default fallback
	if len(digits) > 0 {
		duration = string(digits) + "s" // The API strictly expects strings like "5s"
	}

	respondDefer(s, i)

	req := &video.QueueRequest{
		Model:       model,
		Prompt:      prompt,
		Duration:    duration,
		Resolution:  resolution,
		AspectRatio: aspect,
	}
	editResponse(s, i, fmt.Sprintf("🎬 Queued video generation (`%s`, %s, %s, %s). This takes a minute or more — I'll post it when ready.", model, aspect, resolution, duration))

	go func() {
		data, ct, err := mc.Video.GenerateVideo(bg, req, 5*time.Second)
		if err != nil {
			editResponse(s, i, "Video generation failed: "+err.Error())
			return
		}
		ext := extensionForContentType(ct)
		_, err = s.ChannelMessageSendComplex(i.ChannelID, &discordgo.MessageSend{
			Content: fmt.Sprintf("🎬 **Video** — `%s`", model),
			Files:   []*discordgo.File{{Name: fmt.Sprintf("venice_video_%s.%s", i.ID, ext), Reader: bytes.NewReader(data)}},
		})
		if err != nil {
			editResponse(s, i, "Error posting video: "+err.Error())
		}
	}()
}

// ---- /embed: generate embeddings ----------------------------------------------

func handleEmbed(s *discordgo.Session, i *discordgo.InteractionCreate, get func(string) string) {
	input := strings.TrimSpace(get("input"))
	if input == "" {
		respond(s, i, "Please provide text to embed.")
		return
	}
	model := get("model")
	if model == "" {
		model = "text-embedding-bge-m3"
	}
	respondDefer(s, i)

	out, err := mc.Embeddings.Generate(bg, &embeddings.Request{Model: model, Input: []string{input}})
	if err != nil {
		editResponse(s, i, "Embedding error: "+err.Error())
		return
	}
	if len(out.Data) == 0 {
		editResponse(s, i, "(no embedding data returned)")
		return
	}
	vec := out.Data[0].Embedding
	n := len(vec)
	if n > 8 {
		n = 8
	}
	var dims []string
	for _, v := range vec[:n] {
		dims = append(dims, fmt.Sprintf("%.4f", v))
	}
	msg := fmt.Sprintf("🧠 **Embedding** (`%s`)\nDimensions: **%d**\nPreview: `[%s]`\nTokens: %d",
		model, len(vec), strings.Join(dims, ", "), out.Usage.PromptTokens)
	editResponse(s, i, msg)
}

// ---- /characters: list/search Venice characters ----------------------------------

func handleCharacters(s *discordgo.Session, i *discordgo.InteractionCreate, get func(string) string) {
	search := strings.TrimSpace(get("search"))
	respondDefer(s, i)

	opts := &characters.ListOptions{PageSize: 10}
	if search != "" {
		opts.Search = search
	}
	res, err := mc.Characters.List(bg, opts)
	if err != nil {
		editResponse(s, i, "Characters error: "+err.Error())
		return
	}
	var sb strings.Builder
	if search != "" {
		sb.WriteString(fmt.Sprintf("🎭 **Characters** — search `%s`\n\n", search))
	} else {
		sb.WriteString("🎭 **Characters**\n\n")
	}
	if len(res.Data) == 0 {
		sb.WriteString("No characters found.")
	} else {
		for _, c := range res.Data {
			web := ""
			if c.WebEnabled {
				web = " 🌐"
			}
			sb.WriteString(fmt.Sprintf("**%s** (`%s`)%s\n", c.Name, c.Slug, web))
			if c.Description != "" {
				sb.WriteString(truncateForDiscord(c.Description) + "\n")
			}
			sb.WriteString("\n")
		}
	}
	editResponse(s, i, truncateForDiscord(sb.String()))
}

// ---- /upscale: upscale an attached image -----------------------------------------

func handleUpscale(s *discordgo.Session, i *discordgo.InteractionCreate, get func(string) string) {
	data, name, err := attachmentBytes(s, i)
	if err != nil {
		respond(s, i, "Please attach an image to upscale. "+err.Error())
		return
	}
	if len(data) == 0 {
		respond(s, i, "The attached file is empty.")
		return
	}
	model := get("model")
	if model == "" {
		model = "upscale-model"
	}
	respondDefer(s, i)

	req := &image.UpscaleRequest{
		Model: model,
		// The API expects RAW base64 (no data: prefix) — verified live
		// 2026-09-21: a data-URL causes 400 "image appears to be incomplete
		// or corrupted". This matches modules' LoadImageArg output.
		Image: base64.StdEncoding.EncodeToString(data),
	}
	if scale := get("scale"); scale != "" {
		var v int
		fmt.Sscanf(scale, "%d", &v)
		if v >= 2 && v <= 8 {
			req.Scale = &v
		}
	}
	up, err := mc.Image.Upscale(bg, req)
	if err != nil {
		editResponse(s, i, "Upscale error: "+err.Error())
		return
	}
	ext := detectImageExt(up)
	_, err = s.ChannelMessageSendComplex(i.ChannelID, &discordgo.MessageSend{
		Content: fmt.Sprintf("🔍 **Upscaled** — `%s` (%d → %d bytes)", name, len(data), len(up)),
		Files:   []*discordgo.File{{Name: fmt.Sprintf("venice_upscaled_%s.%s", i.ID, ext), Reader: bytes.NewReader(up)}},
	})
	if err != nil {
		editResponse(s, i, "Error posting upscaled image: "+err.Error())
		return
	}
	editResponse(s, i, "Image upscaled ✅")
}

// ---- small helpers ----------------------------------------------------------------

func extensionForContentType(ct string) string {
	switch {
	case strings.Contains(ct, "mp4"), strings.Contains(ct, "mpeg"), strings.Contains(ct, "quicktime"):
		return "mp4"
	case strings.Contains(ct, "webm"):
		return "webm"
	case strings.Contains(ct, "gif"):
		return "gif"
	case strings.Contains(ct, "png"):
		return "png"
	case strings.Contains(ct, "jpeg"):
		return "jpg"
	default:
		return "mp4"
	}
}
