package a2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
)

// handleCredits: /credits — show remaining A2E credit balance + recent activity.
func handleCredits(s *discordgo.Session, i *discordgo.InteractionCreate, get func(string) string) {
	limit := 5
	if v := get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 20 {
			limit = n
		}
	}

	// Compute the remaining balance from the full transaction history.
	summary, err := ac.GetCreditsBalance()
	if err != nil {
		respond(s, i, "❌ Credits lookup failed: "+err.Error())
		return
	}

	rows, err := ac.GetCredits(limit)
	if err != nil {
		respond(s, i, "❌ Credits lookup failed: "+err.Error())
		return
	}

	var b strings.Builder
	b.WriteString("💳 **A2E credit balance**\n")
	b.WriteString(fmt.Sprintf("**%d credits remaining**\n", summary.Balance))
	b.WriteString(fmt.Sprintf("_Lifetime: +%d added · %d spent · %d transactions_\n", summary.Income, -summary.Consumed, summary.TxCount))

	if len(rows) > 0 {
		b.WriteString("\n**Recent activity**\n")
		for _, r := range rows {
			ts := ""
			if t, err := time.Parse(time.RFC3339, r.CreatedAt); err == nil {
				ts = t.Format("2006-01-02 15:04")
			}
			sign := "+"
			amt := r.Amount
			if amt < 0 {
				sign = "-"
				amt = -amt
			}
			b.WriteString(fmt.Sprintf("`%s` **%s%d** · %s\n", ts, sign, amt, r.Description))
		}
	}
	respond(s, i, b.String())
}

// handleImage: /image — text-to-image generation.
func handleImage(s *discordgo.Session, i *discordgo.InteractionCreate, get func(string) string) {
	prompt := get("prompt")
	if prompt == "" {
		respond(s, i, "Please provide an image prompt.")
		return
	}
	model := get("model")
	usedDefault := false
	if model == "" {
		model = cfg.DefaultImageModel
		usedDefault = true
	}
	width, _ := strconv.Atoi(get("width"))
	height, _ := strconv.Atoi(get("height"))
	negative := get("negative")

	respondDefer(s, i)

	log.Printf("[image] generating with model=%s prompt=%q", model, truncate(prompt, 100))
	task, err := ac.StartText2Image(prompt, model, width, height, negative)
	if err != nil {
		editResponse(s, i, "❌ Image generation failed: "+err.Error())
		return
	}

	// The start response may carry an _id (for polling) and/or an image URL.
	id := mapString(task, "_id")
	imgURL := firstImageURL(task)

	// If there's an _id but no immediate image, poll briefly for completion.
	if id != "" && imgURL == "" {
		imgURL = pollForImage(id, 20, 3) // up to 60s
	}

	if imgURL != "" {
		note := ""
		if usedDefault {
			note = "\n_Model defaulted to `seedream` (omit model to use it)._"
		}
		editResponse(s, i, fmt.Sprintf("🖼️ **Image generated** (model `%s`)\n%s%s", model, imgURL, note))
		return
	}

	// No image yet — return task id/status for manual tracking.
	status := mapString(task, "current_status")
	if status == "" {
		status = "queued"
	}
	editResponse(s, i, fmt.Sprintf("🖼️ Image task started (model `%s`).\n- Task ID: `%s`\n- Status: `%s`\nUse `/tasks type:image` to check.", model, orVal(id, "unknown"), status))
}

// pollForImage polls a text-to-image task detail until an image URL appears or
// the attempt budget runs out.
func pollForImage(id string, attempts, intervalSec int) string {
	for a := 0; a < attempts; a++ {
		detail, err := ac.Text2ImageDetail(id)
		if err == nil {
			if u := firstImageURL(detail); u != "" {
				return u
			}
		}
		time.Sleep(time.Duration(intervalSec) * time.Second)
	}
	return ""
}

// firstImageURL extracts the first usable image URL from a task record, checking
// both single-string fields and the image_urls array.
func firstImageURL(m map[string]any) string {
	if m == nil {
		return ""
	}
	for _, k := range []string{"image_url", "result_url", "url", "image"} {
		if v := mapString(m, k); v != "" && strings.HasPrefix(v, "http") {
			return v
		}
	}
	// image_urls is an array of URL strings (or objects with url).
	if arr, ok := m["image_urls"].([]any); ok {
		for _, item := range arr {
			switch t := item.(type) {
			case string:
				if strings.HasPrefix(t, "http") {
					return t
				}
			case map[string]any:
				if u := mapString(t, "url"); u != "" && strings.HasPrefix(u, "http") {
					return u
				}
			}
		}
	}
	return ""
}

// handleVideo: /video — Wan 3.0 video generation (async).
func handleVideo(s *discordgo.Session, i *discordgo.InteractionCreate, get func(string) string) {
	prompt := get("prompt")
	if prompt == "" {
		respond(s, i, "Please provide a video prompt.")
		return
	}
	model := get("model")
	if model == "" {
		model = cfg.DefaultVideoModel
	}
	mode := get("mode")
	if mode == "" {
		mode = "text-to-video"
	}
	resolution := get("resolution")
	if resolution == "" {
		resolution = "480P"
	}
	ratio := get("ratio")
	if ratio == "" {
		ratio = "16:9"
	}
	duration := 10
	if v := get("duration"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			duration = n
		}
	}
	audio := true
	if v := get("audio"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			audio = b
		}
	}

	respondDefer(s, i)

	log.Printf("[video] generating model=%s mode=%s prompt=%q", model, mode, truncate(prompt, 100))
	task, err := ac.StartWan30("ElyA2E video", model, mode, prompt, nil, wan30Params{
		Resolution: resolution,
		Ratio:      ratio,
		Duration:   duration,
		Audio:      audio,
	})
	if err != nil {
		editResponse(s, i, "❌ Video generation failed: "+err.Error())
		return
	}

	id := mapString(task, "_id")
	status := mapString(task, "current_status")
	if status == "" {
		status = "queued"
	}
	editResponse(s, i, fmt.Sprintf("🎬 **Video task started** (model `%s`, %s, %s, %ds)\n- Task ID: `%s`\n- Status: `%s`\n\nWan 3.0 can take several minutes. Check with `/tasks type:video`.", model, resolution, ratio, duration, orVal(id, "unknown"), status))
}

// handleTasks: /tasks — list recent image or video tasks.
func handleTasks(s *discordgo.Session, i *discordgo.InteractionCreate, get func(string) string) {
	kind := strings.ToLower(get("type"))
	limit := 5
	if v := get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 20 {
			limit = n
		}
	}

	var rows []map[string]any
	var err error
	switch kind {
	case "image":
		rows, err = ac.Text2ImageRecords(limit)
	case "video":
		rows, err = ac.Image2VideoRecords(limit)
	default:
		respond(s, i, "Use `/tasks type:image` or `/tasks type:video`.")
		return
	}
	if err != nil {
		respond(s, i, "❌ Task lookup failed: "+err.Error())
		return
	}
	if len(rows) == 0 {
		respond(s, i, fmt.Sprintf("No recent **%s** tasks found.", kind))
		return
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("🗂️ **Recent %s tasks**\n", kind))
	for _, r := range rows {
		id := mapString(r, "_id")
		status := mapString(r, "current_status")
		if status == "" {
			status = mapString(r, "status")
		}
		desc := mapString(r, "prompt")
		if desc == "" {
			desc = mapString(r, "name")
		}
		desc = truncate(desc, 50)
		b.WriteString(fmt.Sprintf("• `%s` · **%s** · %s\n", id, orVal(status, "?"), orVal(desc, "")))
	}
	respond(s, i, b.String())
}

// --- helpers ---

func mapString(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', 0, 64)
	case json.Number:
		return t.String()
	}
	return fmt.Sprintf("%v", v)
}

// handleTTS: /tts <text> [voice] [country] [region]
// voice may be a built-in voice ID, a cloned voice ID, or a friendly name
// assigned in data/voice-assignments.json.
func handleTTS(s *discordgo.Session, i *discordgo.InteractionCreate, get func(string) string) {
	text := strings.TrimSpace(get("text"))
	if text == "" {
		respond(s, i, "Please provide some text to speak.")
		return
	}
	voice := strings.TrimSpace(get("voice"))
	country := strings.TrimSpace(get("country"))
	region := strings.TrimSpace(get("region"))

	// Determine which voice to use.
	var ttsID, userVoiceID string
	if voice != "" {
		va, err := loadVoiceAssignments()
		if err != nil {
			respond(s, i, "⚠️ Could not read voice assignments: "+err.Error())
			return
		}
		// Reverse lookup: find the ID whose friendly name == voice (or raw ID).
		exactID := ""
		for id, name := range va.Names {
			if name == voice || id == voice {
				exactID = id
				break
			}
		}
		if exactID != "" && !isValidVoiceID(sanitizeVoiceID(exactID)) {
			// Corrupt assignment entry (e.g. "id name" concatenated) — skip it.
			exactID = ""
		}
		if exactID != "" {
			exactID = sanitizeVoiceID(exactID)
			// It's a cloned voice (has an assignment) — but could still be a
			// built-in. Distinguish by checking completed voices.
			completed, _ := ac.CompletedVoices()
			isClone := false
			for _, v := range completed {
				if v.ID == exactID {
					isClone = true
					break
				}
			}
			if isClone {
				userVoiceID = exactID
			} else {
				ttsID = exactID
			}
		} else {
			// Prefix match against cloned voices and built-in voices.
			completed, _ := ac.CompletedVoices()
			for _, v := range completed {
				if strings.HasPrefix(v.ID, voice) || strings.EqualFold(v.Name, voice) {
					userVoiceID = v.ID
					break
				}
			}
			if userVoiceID == "" {
				groups, _ := ac.BuiltinVoices()
				for _, g := range groups {
					for _, v := range g.Children {
						if v.Value == voice || strings.HasPrefix(v.Value, voice) || strings.EqualFold(v.Label, voice) {
							ttsID = v.Value
							break
						}
					}
					if ttsID != "" {
						break
					}
				}
			}
		}
		if userVoiceID == "" && ttsID == "" {
			respond(s, i, fmt.Sprintf("❌ Could not find voice matching `%s`. Use `/voices` to see available voices.", voice))
			return
		}
	}

	respondDefer(s, i)
	log.Printf("[tts] voice=%q user_voice=%q len=%d", ttsID, userVoiceID, len(text))
	audioURL, err := ac.TTS(text, ttsID, userVoiceID, country, region)
	if err != nil {
		editResponse(s, i, "❌ TTS failed: "+err.Error())
		return
	}
	voiceLabel := voice
	if voice == "" {
		voiceLabel = "(default)"
	}

	// Try to download the audio and post it as a Discord file attachment.
	// A2E returns URLs on speech.downloadaivideo.com whose DNS is broken;
	// the same file is served by r2-download.makefun.ai. Rewrite the host and
	// download from there so the audio always plays, then attach it.
	downloadURL := rewriteAudioHost(audioURL)
	data, err := downloadAudio(downloadURL)
	if err == nil {
		filename := fmt.Sprintf("a2e_tts_%s.mp3", i.ID)
		_, sendErr := s.ChannelMessageSendComplex(i.ChannelID, &discordgo.MessageSend{
			Content: fmt.Sprintf("🔊 **TTS** (voice `%s`)", voiceLabel),
			Files:   []*discordgo.File{{Name: filename, Reader: bytes.NewReader(data)}},
		})
		if sendErr == nil {
			editResponse(s, i, fmt.Sprintf("🔊 **TTS** (voice `%s`) ✅", voiceLabel))
			return
		}
		log.Printf("[tts] file post failed (%v); falling back to URL", sendErr)
	}

	// Fallback: reply with the URL.
	editResponse(s, i, fmt.Sprintf("🔊 **TTS** (voice `%s`)\n%s\n\n_Note: audio host may be unreachable; try the A2E site if the link is dead._", voiceLabel, audioURL))
}

// handleVoices: /voices — list the user's cloned voices and built-in voices.
func handleVoices(s *discordgo.Session, i *discordgo.InteractionCreate, get func(string) string) {
	which := strings.ToLower(get("type"))
	if which == "" {
		which = "mine"
	}

	va, err := loadVoiceAssignments()
	if err != nil {
		respond(s, i, "⚠️ Could not read voice assignments: "+err.Error())
		return
	}

	switch which {
	case "mine", "cloned", "my":
		completed, err := ac.CompletedVoices()
		if err != nil {
			respond(s, i, "❌ Failed to fetch cloned voices: "+err.Error())
			return
		}
		training, _ := ac.TrainingVoices()
		respond(s, i, va.formatCompleted(completed, training))
	case "builtin", "system", "preset":
		groups, err := ac.BuiltinVoices()
		if err != nil {
			respond(s, i, "❌ Failed to fetch built-in voices: "+err.Error())
			return
		}
		respond(s, i, va.formatBuiltin(groups))
	default:
		respond(s, i, "Use `/voices type:mine` (your clones) or `/voices type:builtin` (presets).")
	}
}

// handleVoiceAssign: /voice-assign <action:add|delete|view> [id] [name]
// Manages friendly names for voice IDs in the local JSON file.
func handleVoiceAssign(s *discordgo.Session, i *discordgo.InteractionCreate, get func(string) string) {
	action := strings.ToLower(strings.TrimSpace(get("action")))
	if action == "" {
		action = "add"
	}

	va, err := loadVoiceAssignments()
	if err != nil {
		respond(s, i, "⚠️ Could not read voice assignments: "+err.Error())
		return
	}

	switch action {
	case "add", "set":
		id := sanitizeVoiceID(get("id"))
		name := strings.TrimSpace(get("name"))
		if id == "" || name == "" {
			respond(s, i, "Usage: `/voice-assign action:add id:<voice_id> name:<friendly name>`")
			return
		}
		if !isValidVoiceID(id) {
			respond(s, i, fmt.Sprintf("❌ `%s` is not a valid voice ID. Voice IDs are 24-character hex strings — grab one from `/voices`.", id))
			return
		}
		if err := va.assignName(id, name); err != nil {
			respond(s, i, "❌ Failed to save assignment: "+err.Error())
			return
		}
		respond(s, i, fmt.Sprintf("✅ Assigned name **%s** to voice `%s`.\nSaved to `data/voice-assignments.json`.\nNow use `/tts text:... voice:%s`.", name, id, name))

	case "delete", "del", "remove":
		id := sanitizeVoiceID(get("id"))
		name := strings.TrimSpace(get("name"))
		// Support delete by ID or by friendly name.
		var targetID string
		if id != "" {
			targetID = id
		} else if name != "" {
			for vid, n := range va.Names {
				if strings.EqualFold(n, name) {
					targetID = vid
					break
				}
			}
			if targetID == "" {
				respond(s, i, fmt.Sprintf("❌ No voice assigned the name **%s**. Use `/voice-assign action:view` to see assignments.", name))
				return
			}
		} else {
			respond(s, i, "Usage: `/voice-assign action:delete id:<voice_id>` or `action:delete name:<assigned_name>`")
			return
		}
		if _, ok := va.Names[targetID]; !ok {
			respond(s, i, fmt.Sprintf("❌ Voice `%s` has no assignment. Use `/voice-assign action:view` to see assignments.", targetID))
			return
		}
		removed := va.Names[targetID]
		delete(va.Names, targetID)
		if err := va.save(); err != nil {
			respond(s, i, "❌ Failed to save: "+err.Error())
			return
		}
		respond(s, i, fmt.Sprintf("🗑️ Removed name **%s** from voice `%s`.", removed, targetID))

	case "view", "list", "show":
		if len(va.Names) == 0 {
			respond(s, i, "No voice assignments yet. Use `/voice-assign action:add id:<voice_id> name:<name>` to add one.")
			return
		}
		var b strings.Builder
		b.WriteString("🏷️ **Voice assignments** (data/voice-assignments.json)\n")
		for id, n := range va.Names {
			b.WriteString(fmt.Sprintf("• **%s** → `%s`\n", n, id))
		}
		respond(s, i, b.String())

	default:
		respond(s, i, "Unknown action. Use `/voice-assign action:add|delete|view`.")
	}
}

// handleVoiceClone: /voice-clone <name> [urls] [gender] [model] — start voice
// cloning from audio sample URLs or an attached MP3 file.
func handleVoiceClone(s *discordgo.Session, i *discordgo.InteractionCreate, get func(string) string) {
	name := strings.TrimSpace(get("name"))
	if name == "" {
		respond(s, i, "Usage: `/voice-clone name:<name> urls:<url1,url2,...>` or attach an MP3.")
		return
	}
	urlsRaw := strings.TrimSpace(get("urls"))
	gender := strings.TrimSpace(get("gender"))
	if gender == "" {
		gender = "female"
	}
	model := strings.TrimSpace(get("model"))
	if model == "" {
		model = "a2e"
	}

	var urls []string
	for _, u := range strings.Split(urlsRaw, ",") {
		u = strings.TrimSpace(u)
		if u != "" {
			urls = append(urls, u)
		}
	}

	// If no URLs given, use the mp3 attachment option (if provided).
	if len(urls) == 0 {
		attach := getAttachment(i, get("mp3"))
		if attach == nil {
			respond(s, i, "Please provide audio via the **mp3** attachment option (`/voice-clone name:... mp3:<file>`) **or** with `urls:`.")
			return
		}
		// Only accept audio attachments.
		if attach.ContentType != "" && !strings.HasPrefix(attach.ContentType, "audio/") {
			respond(s, i, fmt.Sprintf("❌ Attached file `%s` is `%s`, not audio. Attach an MP3.", attach.Filename, attach.ContentType))
			return
		}
		log.Printf("[voice-clone] using attached file %s (%s, %d bytes)", attach.Filename, attach.ContentType, attach.Size)
		urls = append(urls, attach.URL)
	}

	if len(urls) == 0 {
		respond(s, i, "Please provide at least one audio sample URL.")
		return
	}
	respondDefer(s, i)
	log.Printf("[voice-clone] name=%q model=%s gender=%s samples=%d", name, model, gender, len(urls))
	task, err := ac.StartVoiceClone(name, urls, gender, model)
	if err != nil {
		editResponse(s, i, "❌ Voice cloning failed: "+err.Error())
		return
	}
	id := mapString(task, "_id")
	status := mapString(task, "current_status")
	if status == "" {
		status = "training"
	}
	editResponse(s, i, fmt.Sprintf("🎙️ **Voice clone started** — **%s**\n- Task ID: `%s`\n- Status: `%s`\n- Model: `%s`, %s\n\nIt may take a few minutes. Check with `/voices`.", name, orVal(id, "unknown"), status, model, gender))
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// getAttachment returns the MessageAttachment referenced by an attachment
// option. The attachment option's Value is the attachment ID; it's resolved
// via the interaction's Resolved.Attachments map. Returns nil if none.
func getAttachment(i *discordgo.InteractionCreate, attachmentID string) *discordgo.MessageAttachment {
	if i == nil || i.Interaction == nil || i.Interaction.Data == nil {
		return nil
	}
	// Data is a value ApplicationCommandInteractionData (not a pointer) when
	// the interaction is an application command.
	app, ok := i.Interaction.Data.(discordgo.ApplicationCommandInteractionData)
	if !ok || app.Resolved == nil {
		return nil
	}
	if attachmentID != "" {
		if a, ok := app.Resolved.Attachments[attachmentID]; ok && a != nil {
			return a
		}
	}
	// Fallback: return the first resolved attachment (if any).
	for _, a := range app.Resolved.Attachments {
		if a != nil {
			return a
		}
	}
	return nil
}

func orVal(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
