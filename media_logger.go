// media_logger.go — downloads images/videos/audio posted to servers to Data/MediaLogs/<ServerID>/
// and periodically runs SHA-256 hashing across folders to prune duplicate content.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
)

var (
	mediaLogsBaseDir string                   // testing override
	downloadSem      = make(chan struct{}, 5) // max 5 concurrent downloads
	mediaHTTPClient  = &http.Client{Timeout: 60 * time.Second}
	mediaDedupMu     sync.Mutex
)

// mediaLogsDir returns the path to Data/MediaLogs/<ServerID>/
func mediaLogsDir(serverID string) string {
	if mediaLogsBaseDir != "" {
		return filepath.Join(mediaLogsBaseDir, serverID)
	}
	return dataPath("MediaLogs", serverID)
}

// mediaLogsRoot returns the base Data/MediaLogs directory.
func mediaLogsRoot() string {
	if mediaLogsBaseDir != "" {
		return mediaLogsBaseDir
	}
	return dataPath("MediaLogs")
}

// categorizeMedia identifies whether a file is an image, video, or audio file.
func categorizeMedia(filename, contentType string) string {
	ct := strings.ToLower(contentType)
	if strings.HasPrefix(ct, "image/") {
		return "images"
	}
	if strings.HasPrefix(ct, "video/") {
		return "videos"
	}
	if strings.HasPrefix(ct, "audio/") {
		return "audio"
	}

	ext := strings.ToLower(filepath.Ext(filename))
	switch ext {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp", ".tiff", ".svg", ".ico":
		return "images"
	case ".mp4", ".mov", ".avi", ".mkv", ".webm", ".flv", ".wmv", ".m4v", ".3gp":
		return "videos"
	case ".mp3", ".wav", ".ogg", ".m4a", ".flac", ".aac", ".opus", ".wma", ".aiff":
		return "audio"
	default:
		return ""
	}
}

// sanitizeFilename strips dangerous characters and keeps the filename safe and concise.
func sanitizeFilename(name string) string {
	base := filepath.Base(name)
	base = strings.ReplaceAll(base, "/", "_")
	base = strings.ReplaceAll(base, "\\", "_")
	base = strings.ReplaceAll(base, "..", "_")
	base = strings.ReplaceAll(base, " ", "_")

	var clean []rune
	for _, r := range base {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-' {
			clean = append(clean, r)
		} else {
			clean = append(clean, '_')
		}
	}
	result := string(clean)
	if len(result) > 100 {
		ext := filepath.Ext(result)
		result = result[:100-len(ext)] + ext
	}
	if result == "" || result == "." {
		return "media"
	}
	return result
}

// processMediaAttachments checks a message for media attachments and downloads them in the background.
func processMediaAttachments(m *discordgo.MessageCreate) {
	if m == nil || m.GuildID == "" {
		return
	}

	for _, att := range m.Attachments {
		category := categorizeMedia(att.Filename, att.ContentType)
		if category == "" {
			continue
		}

		go func(attachment *discordgo.MessageAttachment, cat string) {
			downloadSem <- struct{}{}
			defer func() { <-downloadSem }()

			if err := downloadAndSaveMedia(m.GuildID, m.ID, attachment, cat); err != nil {
				logErrorf("media_logger: download failed for %s (%s): %v", attachment.Filename, attachment.URL, err)
			}
		}(att, category)
	}
}

// downloadAndSaveMedia streams the attachment to a temporary file, computes its SHA-256 hash,
// drops duplicates if already present, and renames to the final destination.
func downloadAndSaveMedia(guildID, messageID string, att *discordgo.MessageAttachment, category string) error {
	folder := filepath.Join(mediaLogsDir(guildID), category)
	if err := os.MkdirAll(folder, 0755); err != nil {
		return err
	}

	cleanName := sanitizeFilename(att.Filename)
	timestamp := time.Now().Format("20060102_150405")
	finalName := fmt.Sprintf("%s_%s_%s_%s", timestamp, messageID, att.ID, cleanName)
	finalPath := filepath.Join(folder, finalName)
	tmpPath := finalPath + ".tmp"

	resp, err := mediaHTTPClient.Get(att.URL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("bad status: %s", resp.Status)
	}

	out, err := os.Create(tmpPath)
	if err != nil {
		return err
	}

	hasher := sha256.New()
	writer := io.MultiWriter(out, hasher)

	if _, err := io.Copy(writer, resp.Body); err != nil {
		out.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	out.Close()

	newHash := hex.EncodeToString(hasher.Sum(nil))

	// Check if this exact hash already exists in this guild's media logs
	existingFile, found := findFileByHash(mediaLogsDir(guildID), newHash)
	if found {
		_ = os.Remove(tmpPath)
		log.Printf("[MediaLogs] Skipped duplicate %s (identical to %s)", cleanName, existingFile)
		return nil
	}

	return os.Rename(tmpPath, finalPath)
}

// computeFileHash computes the SHA-256 hash of a file.
func computeFileHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	hasher := sha256.New()
	if _, err := io.Copy(hasher, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// findFileByHash searches rootDir recursively for an existing file with targetHash.
func findFileByHash(rootDir, targetHash string) (string, bool) {
	var foundPath string
	found := false

	_ = filepath.WalkDir(rootDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if strings.HasSuffix(path, ".tmp") {
			return nil
		}
		hash, err := computeFileHash(path)
		if err == nil && hash == targetHash {
			foundPath = path
			found = true
			return io.EOF // stop walking early
		}
		return nil
	})

	return foundPath, found
}

type fileInfoItem struct {
	path    string
	modTime time.Time
	size    int64
}

// DeduplicateMediaFolder scans folder recursively, hashes all files,
// retains the oldest version of each hash, and deletes any duplicate files.
func DeduplicateMediaFolder(folder string) (int, int64, error) {
	mediaDedupMu.Lock()
	defer mediaDedupMu.Unlock()

	if _, err := os.Stat(folder); os.IsNotExist(err) {
		return 0, 0, nil
	}

	hashMap := make(map[string][]fileInfoItem)

	err := filepath.WalkDir(folder, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if strings.HasSuffix(path, ".tmp") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		hash, err := computeFileHash(path)
		if err != nil {
			return nil
		}
		hashMap[hash] = append(hashMap[hash], fileInfoItem{
			path:    path,
			modTime: info.ModTime(),
			size:    info.Size(),
		})
		return nil
	})
	if err != nil {
		return 0, 0, err
	}

	deletedCount := 0
	var freedBytes int64

	for hash, items := range hashMap {
		if len(items) <= 1 {
			continue
		}
		// Sort by modTime ascending so the oldest file is kept as the original
		sort.Slice(items, func(i, j int) bool {
			if items[i].modTime.Equal(items[j].modTime) {
				return items[i].path < items[j].path
			}
			return items[i].modTime.Before(items[j].modTime)
		})

		keep := items[0]
		for _, dup := range items[1:] {
			if err := os.Remove(dup.path); err == nil {
				deletedCount++
				freedBytes += dup.size
				log.Printf("[MediaLogs] Deduplication: removed duplicate %s (matches %s, hash: %s)", dup.path, keep.path, hash[:8])
			} else {
				logErrorf("media_logger: failed to remove duplicate %s: %v", dup.path, err)
			}
		}
	}

	return deletedCount, freedBytes, nil
}

// DeduplicateAllMediaLogs runs deduplication across all server directories in Data/MediaLogs.
func DeduplicateAllMediaLogs() (int, int64, error) {
	root := mediaLogsRoot()
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, 0, nil
		}
		return 0, 0, err
	}

	totalDeleted := 0
	var totalFreed int64

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		serverDir := filepath.Join(root, entry.Name())
		deleted, freed, err := DeduplicateMediaFolder(serverDir)
		if err != nil {
			logErrorf("media_logger: dedup failed for %s: %v", serverDir, err)
			continue
		}
		totalDeleted += deleted
		totalFreed += freed
	}

	return totalDeleted, totalFreed, nil
}

// startPeriodicMediaDeduplication runs DeduplicateAllMediaLogs periodically in the background.
func startPeriodicMediaDeduplication(ctx context.Context, interval time.Duration, b *Bot) {
	log.Printf("media deduplication worker started (interval %s)", interval)
	runPeriodic(ctx, 10*time.Second, interval, func() {
		if deleted, freed, err := DeduplicateAllMediaLogs(); err == nil && deleted > 0 {
			msg := fmt.Sprintf("Media deduplication cleaned up %d duplicate file(s) (%.2f MB freed)", deleted, float64(freed)/(1024*1024))
			log.Println("[MediaLogs] " + msg)
			if b != nil && b.logger != nil && channelID() != "" {
				b.logger.log("MEDIA DEDUP", msg)
			}
		}
	})
}
