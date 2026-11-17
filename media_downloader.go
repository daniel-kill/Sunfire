package main

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
)

var (
	ytDlpDownloadSemaphore = make(chan struct{}, 2)
	ytDlpBinaryPath        string
	ytDlpBinaryOnce        sync.Once
)

func resolveYtDlpBinary() string {
	ytDlpBinaryOnce.Do(func() {
		candidates := []string{
			"yt-dlp",
			"yt-dlp.exe",
		}
		if runtime.GOOS == "windows" {
			candidates = append(candidates,
				`C:\Users\danie\scoop\shims\yt-dlp.exe`,
				`C:\Users\danie\.local\bin\yt-dlp.exe`,
			)
		} else {
			candidates = append(candidates,
				"/mnt/c/Users/danie/scoop/shims/yt-dlp.exe",
				"/home/daniel/.local/bin/yt-dlp",
				"/usr/local/bin/yt-dlp",
				"/usr/bin/yt-dlp",
			)
		}

		for _, candidate := range candidates {
			if path, err := exec.LookPath(candidate); err == nil {
				ytDlpBinaryPath = path
				return
			}
			if fi, err := os.Stat(candidate); err == nil && !fi.IsDir() {
				ytDlpBinaryPath = candidate
				return
			}
		}
	})
	return ytDlpBinaryPath
}

func isValidDownloadURL(rawURL string) bool {
	u, err := url.ParseRequestURI(rawURL)
	if err != nil {
		return false
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host == "" || host == "localhost" || host == "127.0.0.1" || host == "::1" {
		return false
	}
	return true
}

func handleMediaDownload(s *discordgo.Session, i *discordgo.InteractionCreate, sub *discordgo.ApplicationCommandInteractionDataOption) {
	rawURL := ""
	format := "video"

	for _, opt := range sub.Options {
		switch opt.Name {
		case "url":
			rawURL = strings.TrimSpace(opt.StringValue())
		case "format":
			format = strings.ToLower(strings.TrimSpace(opt.StringValue()))
		}
	}

	if rawURL == "" || !isValidDownloadURL(rawURL) {
		sendMediaDownloadReply(s, i, "❌ Please provide a valid HTTP or HTTPS media stream URL.")
		return
	}

	ytDlp := resolveYtDlpBinary()
	if ytDlp == "" {
		sendMediaDownloadReply(s, i, "❌ yt-dlp binary is not installed or available on this host.")
		return
	}

	// Throttle concurrent downloads
	select {
	case ytDlpDownloadSemaphore <- struct{}{}:
		defer func() { <-ytDlpDownloadSemaphore }()
	default:
		sendMediaDownloadReply(s, i, "⏳ Media downloader is currently busy handling another request. Please try again in a moment.")
		return
	}

	tmpDir, err := os.MkdirTemp("", "sunfire-ytdlp-*")
	if err != nil {
		sendMediaDownloadReply(s, i, "❌ Failed to create temporary staging directory for download.")
		return
	}
	defer os.RemoveAll(tmpDir)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	outputTemplate := filepath.Join(tmpDir, "%(title).80s.%(ext)s")
	var args []string

	if format == "audio" {
		args = []string{
			"--no-playlist",
			"--max-filesize", "50M",
			"-x",
			"--audio-format", "mp3",
			"-o", outputTemplate,
			rawURL,
		}
	} else {
		// Video mode: grab mp4 or best stream
		args = []string{
			"--no-playlist",
			"--max-filesize", "50M",
			"-f", "bestvideo[ext=mp4]+bestaudio[ext=m4a]/best[ext=mp4]/best",
			"-o", outputTemplate,
			rawURL,
		}
	}

	cmd := exec.CommandContext(ctx, ytDlp, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("[media/download] yt-dlp failed: %v, output: %s", err, string(out))
		errSnippet := strings.TrimSpace(string(out))
		if len(errSnippet) > 300 {
			errSnippet = errSnippet[:300] + "..."
		}
		sendMediaDownloadReply(s, i, fmt.Sprintf("❌ Failed to download media: %v\n```\n%s\n```", err, errSnippet))
		return
	}

	// Locate downloaded file in tmpDir
	files, err := os.ReadDir(tmpDir)
	if err != nil || len(files) == 0 {
		sendMediaDownloadReply(s, i, "❌ yt-dlp finished but no output file was produced.")
		return
	}

	var targetFile os.DirEntry
	for _, f := range files {
		if !f.IsDir() {
			targetFile = f
			break
		}
	}
	if targetFile == nil {
		sendMediaDownloadReply(s, i, "❌ Could not find downloaded file.")
		return
	}

	filePath := filepath.Join(tmpDir, targetFile.Name())
	fi, err := os.Stat(filePath)
	if err != nil {
		sendMediaDownloadReply(s, i, "❌ Could not inspect downloaded file.")
		return
	}

	// Discord 25MB Limit Check (25 * 1024 * 1024 bytes)
	const maxDiscordBytes = 25 << 20
	uploadPath := filePath

	if fi.Size() > maxDiscordBytes {
		if format == "video" {
			// Try compressing down with ffmpeg
			compressedPath := filepath.Join(tmpDir, "compressed_"+targetFile.Name())
			compressCtx, compressCancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer compressCancel()

			compressCmd := exec.CommandContext(compressCtx, "ffmpeg", "-y", "-i", filePath,
				"-vf", "scale=-2:720",
				"-c:v", "libx264", "-crf", "28", "-preset", "fast",
				"-c:a", "aac", "-b:a", "128k",
				compressedPath)
			if cOut, cErr := compressCmd.CombinedOutput(); cErr == nil {
				if cFi, cStatErr := os.Stat(compressedPath); cStatErr == nil && cFi.Size() <= maxDiscordBytes {
					uploadPath = compressedPath
				} else {
					sendMediaDownloadReply(s, i, fmt.Sprintf("❌ Downloaded video is %.1f MB and exceeded Discord's 25 MB upload limit even after compression.", float64(fi.Size())/(1024*1024)))
					return
				}
			} else {
				log.Printf("[media/download] ffmpeg compression failed: %v, out: %s", cErr, string(cOut))
				sendMediaDownloadReply(s, i, fmt.Sprintf("❌ Downloaded video is %.1f MB which exceeds Discord's 25 MB limit.", float64(fi.Size())/(1024*1024)))
				return
			}
		} else {
			sendMediaDownloadReply(s, i, fmt.Sprintf("❌ Downloaded audio is %.1f MB which exceeds Discord's 25 MB limit.", float64(fi.Size())/(1024*1024)))
			return
		}
	}

	// Upload file
	fileHandle, err := os.Open(uploadPath)
	if err != nil {
		sendMediaDownloadReply(s, i, "❌ Failed to open downloaded media for upload.")
		return
	}
	defer fileHandle.Close()

	title := targetFile.Name()
	embed := &discordgo.MessageEmbed{
		Title:       "🎬 Ingested Media",
		Description: fmt.Sprintf("**File:** `%s`\n**Format:** %s\n**Source:** [Link](%s)", title, strings.ToUpper(format), rawURL),
		Color:       0x9B59B6,
		Footer: &discordgo.MessageEmbedFooter{
			Text: "Downloaded via yt-dlp & FFmpeg",
		},
		Timestamp: time.Now().Format(time.RFC3339),
	}

	_, err = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
		Content: fmt.Sprintf("✅ Downloaded media for <@%s>", mediaInteractionUserID(i)),
		Embeds:  []*discordgo.MessageEmbed{embed},
		Files: []*discordgo.File{
			{
				Name:   filepath.Base(uploadPath),
				Reader: fileHandle,
			},
		},
	})
	if err != nil {
		log.Printf("[media/download] failed to post followup message: %v", err)
		sendMediaDownloadReply(s, i, fmt.Sprintf("❌ Failed to deliver media file to Discord: %v", err))
	}
}

func sendMediaDownloadReply(s *discordgo.Session, i *discordgo.InteractionCreate, message string) {
	_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
		Content: message,
	})
}
