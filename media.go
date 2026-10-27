package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
)

const (
	maxMediaInputBytes   = 25 << 20
	maxMediaOutputBytes  = 25 << 20
	maxMediaSessionBytes = 100 << 20
	maxMediaSessions     = 20
	mediaSessionTTL      = 15 * time.Minute
)

type mediaKind int

const (
	mediaImage mediaKind = iota
	mediaAudio
	mediaVideo
)

type mediaEffect struct {
	value         string
	label         string
	videoFilter   string
	audioFilter   string
	audioChannels int
	videoSpeed    float64
	audioSpeed    float64
	resizeWidth   int
	resizeHeight  int
	optimize      bool
	reverse       bool
	mute          bool
}

type mediaSession struct {
	ownerID     string
	directory   string
	inputPath   string
	filename    string
	contentType string
	inputSize   int64
	kind        mediaKind
	frameCount  int
	expiresAt   time.Time
	timer       *time.Timer
}

var mediaSessionStore = struct {
	sync.Mutex
	entries map[string]*mediaSession
	bytes   int64
}{entries: make(map[string]*mediaSession)}

var mediaDownloadSemaphore = make(chan struct{}, 3)
var mediaProcessSemaphore = make(chan struct{}, 2)

var mediaTransformHTTPClient = &http.Client{
	Timeout: 60 * time.Second,
	CheckRedirect: func(request *http.Request, via []*http.Request) error {
		if request.URL.Scheme != "https" || request.URL.User != nil || !isDiscordMediaHost(request.URL.Hostname()) {
			return errors.New("attachment redirected to a non-Discord media host")
		}
		return nil
	},
}

var imageMediaEffects = []mediaEffect{
	{value: "grayscale", label: "Grayscale", videoFilter: "hue=s=0"},
	{value: "sepia", label: "Sepia", videoFilter: "colorchannelmixer=.393:.769:.189:0:.349:.686:.168:0:.272:.534:.131"},
	{value: "invert", label: "Invert colors", videoFilter: "negate"},
	{value: "mirror", label: "Mirror horizontally", videoFilter: "hflip"},
	{value: "flip", label: "Flip vertically", videoFilter: "vflip"},
	{value: "rotate-right", label: "Rotate right", videoFilter: "transpose=clock"},
	{value: "rotate-left", label: "Rotate left", videoFilter: "transpose=cclock"},
	{value: "blur", label: "Blur", videoFilter: "boxblur=10:1"},
	{value: "sharpen", label: "Sharpen", videoFilter: "unsharp=5:5:1.5:5:5:0"},
	{value: "pixelate", label: "Pixelate", videoFilter: "scale=iw/12:ih/12:flags=neighbor,scale=iw*12:ih*12:flags=neighbor"},
	{value: "brighten", label: "Brighten", videoFilter: "eq=brightness=0.15"},
	{value: "darken", label: "Darken", videoFilter: "eq=brightness=-0.15"},
	{value: "contrast-up", label: "Increase contrast", videoFilter: "eq=contrast=1.5"},
	{value: "contrast-down", label: "Decrease contrast", videoFilter: "eq=contrast=0.7"},
	{value: "saturate", label: "Boost color", videoFilter: "eq=saturation=1.8"},
	{value: "desaturate", label: "Soften color", videoFilter: "eq=saturation=0.3"},
	{value: "hue-shift", label: "Shift hue", videoFilter: "hue=h=45"},
	{value: "vignette", label: "Vignette", videoFilter: "vignette"},
	{value: "edges", label: "Edge detection", videoFilter: "edgedetect=low=0.1:high=0.4"},
	{value: "film-grain", label: "Film grain", videoFilter: "noise=alls=20:allf=t+u"},
	{value: "warm", label: "Warm colors", videoFilter: "colorbalance=rs=.2:bs=-.2"},
	{value: "cool", label: "Cool colors", videoFilter: "colorbalance=rs=-.2:bs=.2"},
	{value: "gamma-up", label: "Raise gamma", videoFilter: "eq=gamma=1.25"},
	{value: "resize", label: "Resize..."},
	{value: "optimize", label: "Optimize (lossless)", optimize: true},
}

var audioMediaEffects = []mediaEffect{
	{value: "volume-up", label: "Increase volume", audioFilter: "volume=1.5"},
	{value: "volume-down", label: "Lower volume", audioFilter: "volume=0.65"},
	{value: "silence", label: "Silence audio", audioFilter: "volume=0"},
	{value: "mono", label: "Convert to mono", audioChannels: 1},
	{value: "stereo", label: "Convert to stereo", audioChannels: 2},
	{value: "faster", label: "Speed up", audioSpeed: 1.25},
	{value: "slower", label: "Slow down", audioSpeed: 0.8},
	{value: "reverse", label: "Reverse audio", reverse: true},
	{value: "bass-boost", label: "Boost bass", audioFilter: "bass=g=8"},
	{value: "bass-cut", label: "Reduce bass", audioFilter: "bass=g=-8"},
	{value: "treble-boost", label: "Boost treble", audioFilter: "treble=g=6"},
	{value: "treble-cut", label: "Reduce treble", audioFilter: "treble=g=-6"},
	{value: "low-pass", label: "Low-pass filter", audioFilter: "lowpass=f=3000"},
	{value: "high-pass", label: "High-pass filter", audioFilter: "highpass=f=200"},
	{value: "telephone", label: "Telephone effect", audioFilter: "highpass=f=500,lowpass=f=3000"},
	{value: "echo", label: "Echo", audioFilter: "aecho=0.8:0.9:500:0.3"},
	{value: "chorus", label: "Chorus", audioFilter: "chorus=0.5:0.9:50|60:0.4|0.32:0.25|0.4:2|2.3"},
	{value: "flanger", label: "Flanger", audioFilter: "flanger"},
	{value: "tremolo", label: "Tremolo", audioFilter: "tremolo"},
	{value: "vibrato", label: "Vibrato", audioFilter: "vibrato"},
	{value: "compressor", label: "Audio compressor", audioFilter: "acompressor=threshold=0.125:ratio=4:attack=20:release=250"},
	{value: "normalize", label: "Normalize loudness", audioFilter: "loudnorm"},
	{value: "fade-in", label: "Fade in", audioFilter: "afade=t=in:st=0:d=2"},
	{value: "reverb", label: "Reverb", audioFilter: "aecho=0.8:0.88:60:0.4"},
	{value: "radio", label: "Noisy radio", audioFilter: "highpass=f=500,lowpass=f=3000,acrusher=bits=8:mix=0.3"},
}

var videoMediaEffects = []mediaEffect{
	{value: "grayscale", label: "Grayscale", videoFilter: "hue=s=0"},
	{value: "sepia", label: "Sepia", videoFilter: "colorchannelmixer=.393:.769:.189:0:.349:.686:.168:0:.272:.534:.131"},
	{value: "invert", label: "Invert colors", videoFilter: "negate"},
	{value: "mirror", label: "Mirror horizontally", videoFilter: "hflip"},
	{value: "flip", label: "Flip vertically", videoFilter: "vflip"},
	{value: "rotate-right", label: "Rotate right", videoFilter: "transpose=clock"},
	{value: "rotate-left", label: "Rotate left", videoFilter: "transpose=cclock"},
	{value: "blur", label: "Blur", videoFilter: "boxblur=10:1"},
	{value: "sharpen", label: "Sharpen", videoFilter: "unsharp=5:5:1.5:5:5:0"},
	{value: "pixelate", label: "Pixelate", videoFilter: "scale=iw/12:ih/12:flags=neighbor,scale=iw*12:ih*12:flags=neighbor"},
	{value: "brighten", label: "Brighten", videoFilter: "eq=brightness=0.15"},
	{value: "darken", label: "Darken", videoFilter: "eq=brightness=-0.15"},
	{value: "contrast-up", label: "Increase contrast", videoFilter: "eq=contrast=1.5"},
	{value: "contrast-down", label: "Decrease contrast", videoFilter: "eq=contrast=0.7"},
	{value: "saturate", label: "Boost color", videoFilter: "eq=saturation=1.8"},
	{value: "desaturate", label: "Soften color", videoFilter: "eq=saturation=0.3"},
	{value: "hue-shift", label: "Shift hue", videoFilter: "hue=h=45"},
	{value: "vignette", label: "Vignette", videoFilter: "vignette"},
	{value: "warm", label: "Warm colors", videoFilter: "colorbalance=rs=.2:bs=-.2"},
	{value: "cool", label: "Cool colors", videoFilter: "colorbalance=rs=-.2:bs=.2"},
	{value: "slow-motion", label: "Slow motion", videoSpeed: 2, audioSpeed: 0.5},
	{value: "fast-motion", label: "Speed up", videoSpeed: 0.8, audioSpeed: 1.25},
	{value: "mute", label: "Remove audio", mute: true},
	{value: "resize", label: "Resize..."},
	{value: "optimize", label: "Optimize (lossless)", optimize: true},
}

func mediaEffects(kind mediaKind) []mediaEffect {
	switch kind {
	case mediaImage:
		return imageMediaEffects
	case mediaAudio:
		return audioMediaEffects
	default:
		return videoMediaEffects
	}
}

func handleMedia(s *discordgo.Session, i *discordgo.InteractionCreate) {
	app := i.ApplicationCommandData()
	attachmentID := ""
	for _, option := range app.Options {
		if option.Name == "file" {
			attachmentID, _ = option.Value.(string)
			break
		}
	}
	var attachment *discordgo.MessageAttachment
	if app.Resolved != nil {
		attachment = app.Resolved.Attachments[attachmentID]
	}
	if attachmentID == "" || attachment == nil {
		respondMedia(s, i, "❌ Please attach a media file.")
		return
	}
	if attachment.Size > maxMediaInputBytes {
		respondMedia(s, i, fmt.Sprintf("❌ That file is too large. The maximum input size is %d MiB.", maxMediaInputBytes>>20))
		return
	}
	if attachment.URL == "" {
		respondMedia(s, i, "❌ Discord did not provide a downloadable URL for that attachment.")
		return
	}

	ownerID := mediaInteractionUserID(i)
	if ownerID == "" {
		respondMedia(s, i, "❌ Could not identify the user who uploaded this media.")
		return
	}
	if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Flags: discordgo.MessageFlagsEphemeral},
	}); err != nil {
		log.Printf("[media] failed to acknowledge /media: %v", err)
		return
	}
	select {
	case mediaDownloadSemaphore <- struct{}{}:
		defer func() { <-mediaDownloadSemaphore }()
	default:
		mediaEditMessage(s, i, "❌ Media uploads are busy right now. Please try again shortly.", &[]discordgo.MessageComponent{}, nil)
		return
	}

	directory, inputPath, size, err := downloadMediaAttachment(attachment)
	if err != nil {
		mediaEditMessage(s, i, "❌ "+err.Error(), &[]discordgo.MessageComponent{}, nil)
		return
	}
	kind, frameCount, err := probeMediaKind(inputPath, attachment.Filename, attachment.ContentType)
	if err != nil {
		removeMediaDirectory(directory)
		log.Printf("[media] ffprobe failed for attachment %q: %v", attachment.Filename, err)
		mediaEditMessage(s, i, "❌ This file could not be read as supported audio, video, or image media.", &[]discordgo.MessageComponent{}, nil)
		return
	}

	session := &mediaSession{
		ownerID: ownerID, directory: directory, inputPath: inputPath,
		filename: attachment.Filename, contentType: attachment.ContentType,
		inputSize: size, kind: kind, frameCount: frameCount, expiresAt: time.Now().Add(mediaSessionTTL),
	}
	sessionID, err := storeMediaSession(session)
	if err != nil {
		removeMediaDirectory(directory)
		mediaEditMessage(s, i, "❌ "+err.Error(), &[]discordgo.MessageComponent{}, nil)
		return
	}

	options := make([]discordgo.SelectMenuOption, 0, len(mediaEffects(kind)))
	for _, effect := range mediaEffects(kind) {
		options = append(options, discordgo.SelectMenuOption{
			Label: effect.label,
			Value: effect.value,
		})
	}
	components := []discordgo.MessageComponent{
		discordgo.ActionsRow{Components: []discordgo.MessageComponent{
			discordgo.SelectMenu{
				MenuType:    discordgo.StringSelectMenu,
				CustomID:    "media:" + sessionID,
				Placeholder: "Choose one of the available transformations",
				Options:     options,
				MaxValues:   1,
			},
		}},
	}
	kindLabel := map[mediaKind]string{mediaImage: "image", mediaAudio: "audio", mediaVideo: "video"}[kind]
	message := fmt.Sprintf("Choose a transformation for this %s. This private menu expires in 15 minutes.", kindLabel)
	mediaEditMessage(s, i, message, &components, nil)
}

func handleMediaComponent(s *discordgo.Session, i *discordgo.InteractionCreate) {
	data := i.MessageComponentData()
	sessionID := strings.TrimPrefix(data.CustomID, "media:")
	if len(data.Values) != 1 {
		respondMedia(s, i, "❌ Choose exactly one media transformation.")
		return
	}
	if data.Values[0] == "resize" {
		showMediaResizeModal(s, i, sessionID)
		return
	}
	ownerID := mediaInteractionUserID(i)
	session, ok := lookupMediaSession(sessionID, ownerID)
	if !ok {
		respondMedia(s, i, "❌ This media menu has expired or belongs to another user. Run `/media` again.")
		return
	}
	effect, ok := findMediaEffect(session.kind, data.Values[0])
	if !ok {
		respondMedia(s, i, "❌ That transformation is not available for this media.")
		return
	}
	session, ok = takeMediaSession(sessionID, ownerID)
	if !ok {
		respondMedia(s, i, "❌ This media menu has expired or belongs to another user. Run `/media` again.")
		return
	}
	defer removeMediaDirectory(session.directory)
	if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredMessageUpdate,
	}); err != nil {
		log.Printf("[media] failed to acknowledge transformation: %v", err)
		return
	}
	select {
	case mediaProcessSemaphore <- struct{}{}:
		defer func() { <-mediaProcessSemaphore }()
	default:
		mediaEditMessage(s, i, "❌ Media processing is busy right now. Please try again shortly.", &[]discordgo.MessageComponent{}, nil)
		return
	}

	outputPath, outputName, err := transformMedia(session, effect)
	if err != nil {
		log.Printf("[media] transform %q failed for %q: %v", effect.value, session.filename, err)
		mediaEditMessage(s, i, "❌ The transformation failed. Run `/media` again or try a smaller file.", &[]discordgo.MessageComponent{}, nil)
		return
	}
	file, err := os.Open(outputPath)
	if err != nil {
		log.Printf("[media] could not open transformed file: %v", err)
		mediaEditMessage(s, i, "❌ The transformed file could not be opened for upload.", &[]discordgo.MessageComponent{}, nil)
		return
	}
	defer file.Close()

	content := fmt.Sprintf("✅ Applied **%s**.", effect.label)
	mediaEditMessage(s, i, content, &[]discordgo.MessageComponent{}, []*discordgo.File{{Name: outputName, Reader: file}})
}

func showMediaResizeModal(s *discordgo.Session, i *discordgo.InteractionCreate, sessionID string) {
	session, ok := lookupMediaSession(sessionID, mediaInteractionUserID(i))
	if !ok {
		respondMedia(s, i, "❌ This media menu has expired or belongs to another user. Run `/media` again.")
		return
	}
	if session.kind != mediaImage && session.kind != mediaVideo {
		respondMedia(s, i, "❌ Resize is only available for images and videos.")
		return
	}
	response := &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseModal,
		Data: &discordgo.InteractionResponseData{
			CustomID: "media:resize:" + sessionID,
			Title:    "Resize media",
			Components: []discordgo.MessageComponent{
				discordgo.ActionsRow{Components: []discordgo.MessageComponent{
					discordgo.TextInput{
						CustomID:    "width",
						Label:       "Width in pixels",
						Style:       discordgo.TextInputShort,
						Placeholder: "Required, 1-8192",
						Required:    true,
						MinLength:   1,
						MaxLength:   4,
					},
				}},
				discordgo.ActionsRow{Components: []discordgo.MessageComponent{
					discordgo.TextInput{
						CustomID:    "height",
						Label:       "Height in pixels (optional)",
						Style:       discordgo.TextInputShort,
						Placeholder: "Leave blank to preserve aspect ratio",
						Required:    false,
						MaxLength:   4,
					},
				}},
			},
		},
	}
	if err := s.InteractionRespond(i.Interaction, response); err != nil {
		log.Printf("[media] failed to show resize modal: %v", err)
	}
}

func handleMediaResizeModal(s *discordgo.Session, i *discordgo.InteractionCreate) {
	data := i.ModalSubmitData()
	parts := strings.Split(data.CustomID, ":")
	if len(parts) != 3 || parts[0] != "media" || parts[1] != "resize" {
		respondMedia(s, i, "❌ Invalid resize request. Run `/media` again.")
		return
	}
	widthText, heightText, ok := mediaResizeModalValues(data.Components)
	if !ok {
		respondMedia(s, i, "❌ Enter a valid width and optional height in pixels.")
		return
	}
	width, height, err := parseMediaResizeDimensions(widthText, heightText)
	if err != nil {
		respondMedia(s, i, "❌ "+err.Error())
		return
	}
	ownerID := mediaInteractionUserID(i)
	session, ok := lookupMediaSession(parts[2], ownerID)
	if !ok {
		respondMedia(s, i, "❌ This media menu has expired or belongs to another user. Run `/media` again.")
		return
	}
	if err := validateMediaResizeDimensions(session.kind, width, height); err != nil {
		respondMedia(s, i, "❌ "+err.Error())
		return
	}
	session, ok = takeMediaSession(parts[2], ownerID)
	if !ok {
		respondMedia(s, i, "❌ This media menu has expired or belongs to another user. Run `/media` again.")
		return
	}
	sessionEffect := mediaEffect{
		value: "resize", label: fmt.Sprintf("Resized to %s", mediaResizeLabel(width, height)),
		resizeWidth: width, resizeHeight: height,
	}
	processMediaModalResult(s, i, session, sessionEffect)
}

func mediaResizeModalValues(components []discordgo.MessageComponent) (string, string, bool) {
	var width, height string
	for _, component := range components {
		row, ok := component.(*discordgo.ActionsRow)
		if !ok {
			continue
		}
		for _, nested := range row.Components {
			input, ok := nested.(*discordgo.TextInput)
			if !ok {
				continue
			}
			switch input.CustomID {
			case "width":
				width = strings.TrimSpace(input.Value)
			case "height":
				height = strings.TrimSpace(input.Value)
			}
		}
	}
	return width, height, width != ""
}

func parseMediaResizeDimensions(widthText, heightText string) (int, int, error) {
	width, err := strconv.Atoi(widthText)
	if err != nil || width < 1 || width > 8192 {
		return 0, 0, errors.New("width must be a whole number from 1 to 8192.")
	}
	height := 0
	if heightText != "" {
		height, err = strconv.Atoi(heightText)
		if err != nil || height < 1 || height > 8192 {
			return 0, 0, errors.New("height must be blank or a whole number from 1 to 8192.")
		}
	}
	return width, height, nil
}

func validateMediaResizeDimensions(kind mediaKind, width, height int) error {
	if kind == mediaVideo && (width%2 != 0 || (height != 0 && height%2 != 0)) {
		return errors.New("video width and height must be even numbers for MP4 compatibility.")
	}
	return nil
}

func mediaResizeLabel(width, height int) string {
	if height == 0 {
		return fmt.Sprintf("%d px wide (aspect ratio preserved)", width)
	}
	return fmt.Sprintf("%d x %d px", width, height)
}

func processMediaModalResult(s *discordgo.Session, i *discordgo.InteractionCreate, session *mediaSession, effect mediaEffect) {
	defer removeMediaDirectory(session.directory)
	if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Flags: discordgo.MessageFlagsEphemeral},
	}); err != nil {
		log.Printf("[media] failed to acknowledge modal transformation: %v", err)
		return
	}
	select {
	case mediaProcessSemaphore <- struct{}{}:
		defer func() { <-mediaProcessSemaphore }()
	default:
		mediaEditMessage(s, i, "❌ Media processing is busy right now. Please try again shortly.", &[]discordgo.MessageComponent{}, nil)
		return
	}
	outputPath, outputName, err := transformMedia(session, effect)
	if err != nil {
		log.Printf("[media] transform %q failed for %q: %v", effect.value, session.filename, err)
		mediaEditMessage(s, i, "❌ The transformation failed. Run `/media` again or try a smaller file.", &[]discordgo.MessageComponent{}, nil)
		return
	}
	file, err := os.Open(outputPath)
	if err != nil {
		log.Printf("[media] could not open transformed file: %v", err)
		mediaEditMessage(s, i, "❌ The transformed file could not be opened for upload.", &[]discordgo.MessageComponent{}, nil)
		return
	}
	defer file.Close()
	mediaEditMessage(s, i, "✅ Applied **"+effect.label+"**.", &[]discordgo.MessageComponent{}, []*discordgo.File{{Name: outputName, Reader: file}})
}

func respondMedia(s *discordgo.Session, i *discordgo.InteractionCreate, content string) {
	if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Content: content, Flags: discordgo.MessageFlagsEphemeral},
	}); err != nil {
		log.Printf("[media] failed to send response: %v", err)
	}
}

func mediaEditMessage(s *discordgo.Session, i *discordgo.InteractionCreate, content string, components *[]discordgo.MessageComponent, files []*discordgo.File) {
	if _, err := s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
		Content: &content, Components: components, Files: files,
	}); err != nil {
		log.Printf("[media] failed to update interaction response: %v", err)
	}
}

func mediaInteractionUserID(i *discordgo.InteractionCreate) string {
	if i == nil || i.Interaction == nil {
		return ""
	}
	if i.Member != nil && i.Member.User != nil {
		return i.Member.User.ID
	}
	if i.User != nil {
		return i.User.ID
	}
	return ""
}

func downloadMediaAttachment(attachment *discordgo.MessageAttachment) (string, string, int64, error) {
	parsedURL, err := url.Parse(attachment.URL)
	if err != nil || parsedURL.Scheme != "https" || parsedURL.User != nil || !isDiscordMediaHost(parsedURL.Hostname()) {
		return "", "", 0, errors.New("the attachment URL is not a valid Discord media URL")
	}
	if attachment.Size > maxMediaInputBytes {
		return "", "", 0, fmt.Errorf("the maximum input size is %d MiB", maxMediaInputBytes>>20)
	}
	request, err := http.NewRequest(http.MethodGet, parsedURL.String(), nil)
	if err != nil {
		return "", "", 0, fmt.Errorf("could not create media download request: %w", err)
	}
	response, err := mediaTransformHTTPClient.Do(request)
	if err != nil {
		return "", "", 0, fmt.Errorf("could not download the media attachment: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", "", 0, fmt.Errorf("Discord returned HTTP %d while downloading the attachment", response.StatusCode)
	}
	if response.ContentLength > maxMediaInputBytes {
		return "", "", 0, fmt.Errorf("the maximum input size is %d MiB", maxMediaInputBytes>>20)
	}

	directory, err := os.MkdirTemp("", "sunfire-media-")
	if err != nil {
		return "", "", 0, fmt.Errorf("could not create temporary media storage: %w", err)
	}
	inputPath := filepath.Join(directory, "input")
	output, err := os.OpenFile(inputPath, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		removeMediaDirectory(directory)
		return "", "", 0, fmt.Errorf("could not create temporary media file: %w", err)
	}
	written, copyErr := io.Copy(output, io.LimitReader(response.Body, maxMediaInputBytes+1))
	closeErr := output.Close()
	if copyErr != nil {
		removeMediaDirectory(directory)
		return "", "", 0, fmt.Errorf("could not save the downloaded media: %w", copyErr)
	}
	if closeErr != nil {
		removeMediaDirectory(directory)
		return "", "", 0, fmt.Errorf("could not finish saving the downloaded media: %w", closeErr)
	}
	if written == 0 {
		removeMediaDirectory(directory)
		return "", "", 0, errors.New("the attached file is empty")
	}
	if written > maxMediaInputBytes {
		removeMediaDirectory(directory)
		return "", "", 0, fmt.Errorf("the maximum input size is %d MiB", maxMediaInputBytes>>20)
	}
	return directory, inputPath, written, nil
}

func isDiscordMediaHost(host string) bool {
	switch strings.ToLower(host) {
	case "cdn.discordapp.com", "media.discordapp.net", "cdn.discordapp.net":
		return true
	default:
		return false
	}
}

func probeMediaKind(inputPath, filename, contentType string) (mediaKind, int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-show_entries", "stream=codec_type", "-of", "json", inputPath)
	var stderr limitedBuffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		if ctx.Err() != nil {
			return 0, 0, fmt.Errorf("ffprobe timed out: %w", ctx.Err())
		}
		return 0, 0, fmt.Errorf("ffprobe could not inspect the media: %w (%s)", err, strings.TrimSpace(stderr.String()))
	}
	var result struct {
		Streams []struct {
			CodecType   string `json:"codec_type"`
			Disposition struct {
				AttachedPicture int `json:"attached_pic"`
			} `json:"disposition"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		return 0, 0, fmt.Errorf("ffprobe returned invalid stream information: %w", err)
	}
	hasAudio, hasVideo := false, false
	for _, stream := range result.Streams {
		hasAudio = hasAudio || stream.CodecType == "audio"
		hasVideo = hasVideo || (stream.CodecType == "video" && stream.Disposition.AttachedPicture == 0)
	}
	if hasAudio && !hasVideo {
		return mediaAudio, 0, nil
	}
	if !hasVideo {
		return 0, 0, errors.New("the file contains no supported audio or video stream")
	}
	if isImageAttachment(filename, contentType) {
		frameCount, err := probeImageFrameCount(inputPath)
		if err != nil {
			return 0, 0, err
		}
		return mediaImage, frameCount, nil
	}
	return mediaVideo, 0, nil
}

func probeImageFrameCount(inputPath string) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-count_frames", "-select_streams", "v:0", "-show_entries", "stream=nb_read_frames", "-of", "default=nokey=1:noprint_wrappers=1", inputPath)
	var stderr limitedBuffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		if ctx.Err() != nil {
			return 0, fmt.Errorf("ffprobe timed out while checking image frames: %w", ctx.Err())
		}
		return 0, fmt.Errorf("ffprobe could not count image frames: %w (%s)", err, strings.TrimSpace(stderr.String()))
	}
	frameCount, err := strconv.Atoi(strings.TrimSpace(string(output)))
	if err != nil || frameCount < 1 {
		return 0, fmt.Errorf("ffprobe returned an invalid image frame count %q", strings.TrimSpace(string(output)))
	}
	return frameCount, nil
}

func isImageAttachment(filename, contentType string) bool {
	contentType = strings.ToLower(contentType)
	if strings.HasPrefix(contentType, "image/") {
		return true
	}
	if strings.HasPrefix(contentType, "audio/") || strings.HasPrefix(contentType, "video/") {
		return false
	}
	extension := strings.ToLower(filepath.Ext(filename))
	if contentType := mime.TypeByExtension(extension); strings.HasPrefix(contentType, "image/") {
		return true
	}
	switch extension {
	case ".avif", ".bmp", ".gif", ".heic", ".heif", ".ico", ".jxl", ".tif", ".tiff":
		return true
	default:
		return false
	}
}

func storeMediaSession(session *mediaSession) (string, error) {
	idBytes := make([]byte, 12)
	if _, err := rand.Read(idBytes); err != nil {
		return "", fmt.Errorf("could not create a private media session: %w", err)
	}
	id := hex.EncodeToString(idBytes)
	mediaSessionStore.Lock()
	defer mediaSessionStore.Unlock()
	if len(mediaSessionStore.entries) >= maxMediaSessions || mediaSessionStore.bytes+session.inputSize > maxMediaSessionBytes {
		return "", errors.New("media processing is busy right now; try again after an existing menu expires")
	}
	session.timer = time.AfterFunc(mediaSessionTTL, func() {
		removeMediaSession(id)
	})
	mediaSessionStore.entries[id] = session
	mediaSessionStore.bytes += session.inputSize
	return id, nil
}

func takeMediaSession(id, ownerID string) (*mediaSession, bool) {
	mediaSessionStore.Lock()
	session, exists := mediaSessionStore.entries[id]
	if !exists || ownerID == "" || session.ownerID != ownerID || time.Now().After(session.expiresAt) {
		mediaSessionStore.Unlock()
		return nil, false
	}
	delete(mediaSessionStore.entries, id)
	mediaSessionStore.bytes -= session.inputSize
	session.timer.Stop()
	mediaSessionStore.Unlock()
	return session, true
}

func lookupMediaSession(id, ownerID string) (*mediaSession, bool) {
	mediaSessionStore.Lock()
	defer mediaSessionStore.Unlock()
	session, exists := mediaSessionStore.entries[id]
	if !exists || ownerID == "" || session.ownerID != ownerID || time.Now().After(session.expiresAt) {
		return nil, false
	}
	return session, true
}

func removeMediaSession(id string) {
	mediaSessionStore.Lock()
	session, exists := mediaSessionStore.entries[id]
	if exists {
		delete(mediaSessionStore.entries, id)
		mediaSessionStore.bytes -= session.inputSize
	}
	mediaSessionStore.Unlock()
	if exists {
		removeMediaDirectory(session.directory)
	}
}

func removeMediaDirectory(directory string) {
	if err := os.RemoveAll(directory); err != nil {
		log.Printf("[media] failed to remove temporary directory %q: %v", directory, err)
	}
}

func findMediaEffect(kind mediaKind, value string) (mediaEffect, bool) {
	for _, effect := range mediaEffects(kind) {
		if effect.value == value {
			return effect, true
		}
	}
	return mediaEffect{}, false
}

func transformMedia(session *mediaSession, effect mediaEffect) (string, string, error) {
	if effect.optimize && session.kind == mediaImage && session.frameCount > 1 {
		return session.inputPath, filepath.Base(session.filename), nil
	}
	outputExtension := ".mp4"
	switch session.kind {
	case mediaImage:
		outputExtension = ".png"
		if strings.EqualFold(filepath.Ext(session.filename), ".gif") || strings.EqualFold(session.contentType, "image/gif") {
			outputExtension = ".gif"
		}
	case mediaAudio:
		outputExtension = ".mp3"
	}
	if effect.optimize {
		switch session.kind {
		case mediaImage:
			if outputExtension != ".gif" {
				outputExtension = ".png"
			}
		case mediaVideo:
			outputExtension = ".mkv"
		}
	}
	outputName := "transformed" + outputExtension
	outputPath := filepath.Join(session.directory, outputName)

	args := []string{"-nostdin", "-hide_banner", "-loglevel", "error", "-y", "-filter_threads", "2", "-i", session.inputPath}
	switch session.kind {
	case mediaImage:
		if effect.optimize {
			if outputExtension == ".gif" {
				args = appendImageTransformArgs(args, effect, outputExtension)
			} else {
				args = append(args, "-map", "0:v:0", "-an", "-frames:v", "1", "-c:v", "png", "-compression_level", "9", "-pix_fmt", "+")
			}
		} else {
			args = appendImageTransformArgs(args, effect, outputExtension)
		}
	case mediaAudio:
		args = append(args, "-map", "0:a:0", "-vn")
		audioFilters := make([]string, 0, 3)
		if effect.audioFilter != "" {
			audioFilters = append(audioFilters, effect.audioFilter)
		}
		if effect.reverse {
			audioFilters = append(audioFilters, "areverse")
		}
		if effect.audioSpeed != 0 {
			audioFilters = append(audioFilters, fmt.Sprintf("atempo=%.3f", effect.audioSpeed))
		}
		if len(audioFilters) > 0 {
			args = append(args, "-af", strings.Join(audioFilters, ","))
		}
		if effect.audioChannels != 0 {
			args = append(args, "-ac", fmt.Sprint(effect.audioChannels))
		}
		args = append(args, "-c:a", "libmp3lame", "-q:a", "3")
	case mediaVideo:
		args = append(args, "-map", "0:v:0", "-map", "0:a?")
		if effect.optimize {
			args = append(args, "-c:v", "libx264", "-preset", "veryslow", "-qp", "0", "-pix_fmt", "+", "-c:a", "copy")
		} else if effect.mute {
			args = append(args, "-an")
		} else {
			videoFilters := make([]string, 0, 4)
			if effect.videoSpeed != 0 {
				videoFilters = append(videoFilters, fmt.Sprintf("setpts=%.3f*PTS", effect.videoSpeed))
			}
			if effect.reverse {
				videoFilters = append(videoFilters, "reverse")
			}
			if effect.videoFilter != "" {
				videoFilters = append(videoFilters, effect.videoFilter)
			}
			if effect.resizeWidth > 0 {
				videoFilters = append(videoFilters, mediaScaleFilter(effect.resizeWidth, effect.resizeHeight, true))
			} else {
				videoFilters = append(videoFilters, "scale=trunc(iw/2)*2:trunc(ih/2)*2")
			}
			args = append(args, "-vf", strings.Join(videoFilters, ","))
			audioFilters := make([]string, 0, 2)
			if effect.reverse {
				audioFilters = append(audioFilters, "areverse")
			}
			if effect.audioSpeed != 0 {
				audioFilters = append(audioFilters, fmt.Sprintf("atempo=%.3f", effect.audioSpeed))
			}
			if effect.audioFilter != "" {
				audioFilters = append(audioFilters, effect.audioFilter)
			}
			if len(audioFilters) > 0 {
				args = append(args, "-af", strings.Join(audioFilters, ","))
			}
			args = append(args, "-c:v", "libx264", "-preset", "ultrafast", "-crf", "24", "-pix_fmt", "yuv420p", "-c:a", "aac", "-b:a", "128k", "-movflags", "+faststart")
		}
	}
	args = append(args, "-fs", fmt.Sprint(maxMediaOutputBytes), outputPath)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, "ffmpeg", args...)
	var stderr limitedBuffer
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return "", "", fmt.Errorf("ffmpeg timed out: %w", ctx.Err())
		}
		return "", "", fmt.Errorf("ffmpeg exited with an error: %w (%s)", err, strings.TrimSpace(stderr.String()))
	}
	info, err := os.Stat(outputPath)
	if err != nil {
		return "", "", fmt.Errorf("ffmpeg did not create an output file: %w", err)
	}
	if info.Size() == 0 {
		return "", "", errors.New("ffmpeg produced an empty output file")
	}
	if effect.optimize {
		inputInfo, err := os.Stat(session.inputPath)
		if err != nil {
			return "", "", fmt.Errorf("could not inspect the original media: %w", err)
		}
		if info.Size() >= inputInfo.Size() {
			return session.inputPath, filepath.Base(session.filename), nil
		}
	}
	if info.Size() >= maxMediaOutputBytes {
		return "", "", fmt.Errorf("the transformed file exceeds the %d MiB upload limit", maxMediaOutputBytes>>20)
	}
	return outputPath, outputName, nil
}

func appendImageTransformArgs(args []string, effect mediaEffect, outputExtension string) []string {
	filter := effect.videoFilter
	if effect.resizeWidth > 0 {
		resizeFilter := mediaScaleFilter(effect.resizeWidth, effect.resizeHeight, false)
		if filter == "" {
			filter = resizeFilter
		} else {
			filter += "," + resizeFilter
		}
	}
	if filter == "" {
		filter = "null"
	}
	if outputExtension == ".gif" {
		graph := fmt.Sprintf("[0:v:0]%s,split[v1][v2];[v1]palettegen[p];[v2][p]paletteuse[outv]", filter)
		args = append(args, "-an", "-filter_complex", graph, "-map", "[outv]", "-loop", "0")
	} else {
		args = append(args, "-map", "0:v:0", "-an", "-vf", filter, "-frames:v", "1")
	}
	codec := "png"
	if outputExtension == ".gif" {
		codec = "gif"
	}
	return append(args, "-c:v", codec)
}

func mediaScaleFilter(width, height int, evenDimensions bool) string {
	heightValue := "-1"
	if evenDimensions {
		heightValue = "-2"
	}
	if height > 0 {
		heightValue = strconv.Itoa(height)
	}
	return fmt.Sprintf("scale=%d:%s:flags=lanczos", width, heightValue)
}

type limitedBuffer struct {
	buffer bytes.Buffer
}

func (b *limitedBuffer) Write(data []byte) (int, error) {
	const maxLoggedOutput = 8 << 10
	inputLength := len(data)
	remaining := maxLoggedOutput - b.buffer.Len()
	if remaining > 0 {
		if len(data) > remaining {
			data = data[:remaining]
		}
		_, _ = b.buffer.Write(data)
	}
	return inputLength, nil
}

func (b *limitedBuffer) String() string {
	return b.buffer.String()
}
