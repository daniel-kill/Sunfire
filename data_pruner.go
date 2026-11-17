// data_pruner.go — automated data pruning and rolling retention daemon for message and media logs.
package main

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

var (
	dataPrunerMu sync.Mutex
)

// PruneResult summarizes the outcome of a pruning pass.
type PruneResult struct {
	MessageFilesScanned int           `json:"messageFilesScanned"`
	MessagesPruned      int           `json:"messagesPruned"`
	MessagesArchived    int           `json:"messagesArchived"`
	MediaFilesScanned   int           `json:"mediaFilesScanned"`
	MediaFilesPruned    int           `json:"mediaFilesPruned"`
	MediaBytesFreed     int64         `json:"mediaBytesFreed"`
	Duration            time.Duration `json:"duration"`
	Errors              []string      `json:"errors,omitempty"`
}

func archivesRoot() string {
	return dataPath("Archives")
}

func archivesMessageLogsDir(guildID string) string {
	return filepath.Join(archivesRoot(), "MessageLogs", guildID)
}

// archivePrunedRecords compresses pruned message records into a .json.gz file under Data/Archives/MessageLogs/<guildID>/.
func archivePrunedRecords(guildID, authorID string, records []MessageRecord) error {
	if len(records) == 0 {
		return nil
	}
	dir := archivesMessageLogsDir(guildID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating archives dir %s: %w", dir, err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("chmod archives dir %s: %w", dir, err)
	}

	ts := time.Now().Format("20060102-150405")
	archivePath := filepath.Join(dir, fmt.Sprintf("%s_%s.json.gz", authorID, ts))

	f, err := os.OpenFile(archivePath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("creating archive file %s: %w", archivePath, err)
	}
	defer f.Close()

	gz := gzip.NewWriter(f)
	defer gz.Close()

	data, err := json.Marshal(records)
	if err != nil {
		return fmt.Errorf("marshaling archive records: %w", err)
	}

	if _, err := gz.Write(data); err != nil {
		return fmt.Errorf("writing gzip archive %s: %w", archivePath, err)
	}
	return nil
}

// PruneMessageLogs inspects Data/MessageLogs and prunes records older than RetentionDays
// or beyond MaxRecordsPerUser, optionally compressing pruned records into archives.
func PruneMessageLogs(cfg DataPruneConfig) (filesScanned, prunedCount, archivedCount int, errs []string) {
	root := messageLogsRoot()
	guildDirs, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, 0, 0, nil
		}
		return 0, 0, 0, []string{fmt.Sprintf("reading message logs root %s: %v", root, err)}
	}

	now := time.Now()
	var cutoff time.Time
	if cfg.RetentionDays > 0 {
		cutoff = now.AddDate(0, 0, -cfg.RetentionDays)
	}

	for _, gDir := range guildDirs {
		if !gDir.IsDir() {
			continue
		}
		guildID := gDir.Name()
		guildPath := filepath.Join(root, guildID)
		files, err := os.ReadDir(guildPath)
		if err != nil {
			errs = append(errs, fmt.Sprintf("reading guild dir %s: %v", guildPath, err))
			continue
		}

		for _, f := range files {
			if f.IsDir() || filepath.Ext(f.Name()) != ".json" {
				continue
			}
			filesScanned++
			filePath := filepath.Join(guildPath, f.Name())
			authorID := strings.TrimSuffix(f.Name(), ".json")

			func() {
				messageMu.Lock()
				defer messageMu.Unlock()

				data, err := os.ReadFile(filePath)
				if err != nil || len(data) == 0 {
					return
				}
				var records []MessageRecord
				if err := json.Unmarshal(data, &records); err != nil {
					errs = append(errs, fmt.Sprintf("parsing %s: %v", filePath, err))
					return
				}

				if len(records) == 0 {
					return
				}

				var toKeep []MessageRecord
				var toPrune []MessageRecord

				for _, rec := range records {
					isOld := false
					if !cutoff.IsZero() && rec.Timestamp != "" {
						if t, err := time.Parse(time.RFC3339, rec.Timestamp); err == nil {
							if t.Before(cutoff) {
								isOld = true
							}
						}
					}
					if isOld {
						toPrune = append(toPrune, rec)
					} else {
						toKeep = append(toKeep, rec)
					}
				}

				// Also cap by MaxRecordsPerUser if specified
				if cfg.MaxRecordsPerUser > 0 && len(toKeep) > cfg.MaxRecordsPerUser {
					excess := len(toKeep) - cfg.MaxRecordsPerUser
					toPrune = append(toPrune, toKeep[:excess]...)
					toKeep = toKeep[excess:]
				}

				if len(toPrune) == 0 {
					return
				}

				// Archive before removing
				if cfg.ArchivePrunedLogs {
					if err := archivePrunedRecords(guildID, authorID, toPrune); err != nil {
						errs = append(errs, fmt.Sprintf("archiving pruned logs for %s/%s: %v", guildID, authorID, err))
					} else {
						archivedCount += len(toPrune)
					}
				}

				// Write kept records back
				newData, err := json.MarshalIndent(toKeep, "", "  ")
				if err != nil {
					errs = append(errs, fmt.Sprintf("marshaling kept records for %s: %v", filePath, err))
					return
				}
				if err := writePrivateMessageLog(filePath, newData); err != nil {
					errs = append(errs, fmt.Sprintf("writing pruned log %s: %v", filePath, err))
					return
				}
				prunedCount += len(toPrune)
			}()
		}
	}
	return filesScanned, prunedCount, archivedCount, errs
}

// PruneMediaLogs inspects Data/MediaLogs and applies age-based and size-based pruning.
func PruneMediaLogs(cfg DataPruneConfig) (filesScanned, filesPruned int, bytesFreed int64, errs []string) {
	mediaDedupMu.Lock()
	defer mediaDedupMu.Unlock()

	root := mediaLogsRoot()
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return 0, 0, 0, nil
	}

	now := time.Now()
	var mediaAgeCutoff time.Time
	if cfg.MediaRetentionDays > 0 {
		mediaAgeCutoff = now.AddDate(0, 0, -cfg.MediaRetentionDays)
	}

	var allMediaFiles []fileInfoItem
	var totalBytes int64

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if strings.HasSuffix(path, ".tmp") {
			return nil
		}
		filesScanned++
		info, err := d.Info()
		if err != nil {
			return nil
		}
		size := info.Size()
		modTime := info.ModTime()

		// Check age cutoff first
		if !mediaAgeCutoff.IsZero() && modTime.Before(mediaAgeCutoff) {
			if err := os.Remove(path); err == nil {
				filesPruned++
				bytesFreed += size
				return nil
			} else {
				errs = append(errs, fmt.Sprintf("failed removing aged media %s: %v", path, err))
			}
		}

		allMediaFiles = append(allMediaFiles, fileInfoItem{
			path:    path,
			modTime: modTime,
			size:    size,
		})
		totalBytes += size
		return nil
	})
	if err != nil {
		errs = append(errs, fmt.Sprintf("walking media logs %s: %v", root, err))
	}

	// Size-based pruning: if remaining media files exceed MaxMediaSizeMB, prune oldest files until under target
	if cfg.MaxMediaSizeMB > 0 {
		var currentBytes int64
		for _, item := range allMediaFiles {
			currentBytes += item.size
		}
		maxBytes := int64(cfg.MaxMediaSizeMB) * 1024 * 1024
		if currentBytes > maxBytes {
			targetBytes := int64(float64(maxBytes) * 0.8)
			// Sort oldest first
			sort.Slice(allMediaFiles, func(i, j int) bool {
				return allMediaFiles[i].modTime.Before(allMediaFiles[j].modTime)
			})

			for _, item := range allMediaFiles {
				if currentBytes <= targetBytes {
					break
				}
				// Check if already pruned
				if _, err := os.Stat(item.path); os.IsNotExist(err) {
					continue
				}
				if err := os.Remove(item.path); err == nil {
					filesPruned++
					bytesFreed += item.size
					currentBytes -= item.size
				} else {
					errs = append(errs, fmt.Sprintf("failed removing media over quota %s: %v", item.path, err))
				}
			}
		}
	}

	return filesScanned, filesPruned, bytesFreed, errs
}

// RunDataPrune executes a full pruning and archival cycle across message and media logs.
func RunDataPrune(cfg DataPruneConfig) PruneResult {
	dataPrunerMu.Lock()
	defer dataPrunerMu.Unlock()

	start := time.Now()
	res := PruneResult{}

	msgFiles, msgPruned, msgArchived, msgErrs := PruneMessageLogs(cfg)
	res.MessageFilesScanned = msgFiles
	res.MessagesPruned = msgPruned
	res.MessagesArchived = msgArchived
	res.Errors = append(res.Errors, msgErrs...)

	medScanned, medPruned, medFreed, medErrs := PruneMediaLogs(cfg)
	res.MediaFilesScanned = medScanned
	res.MediaFilesPruned = medPruned
	res.MediaBytesFreed = medFreed
	res.Errors = append(res.Errors, medErrs...)

	res.Duration = time.Since(start)
	return res
}

// startPeriodicDataPruner runs RunDataPrune periodically in the background.
func startPeriodicDataPruner(ctx context.Context, b *Bot) {
	cfg, err := loadConfig()
	pruneCfg := defaultDataPruneConfig()
	if err == nil && cfg != nil {
		pruneCfg = resolveDataPruneConfig(cfg)
	}

	if !pruneCfg.Enabled {
		log.Println("[DataPrune] Data pruning daemon is disabled by configuration")
		return
	}

	interval := time.Duration(pruneCfg.IntervalHours) * time.Hour
	if interval < time.Hour {
		interval = 24 * time.Hour
	}

	log.Printf("[DataPrune] Data pruning worker started (interval %s, msg retention: %dd, media retention: %dd, max media: %dMB)",
		interval, pruneCfg.RetentionDays, pruneCfg.MediaRetentionDays, pruneCfg.MaxMediaSizeMB)

	runPeriodic(ctx, 45*time.Second, interval, func() {
		res := RunDataPrune(pruneCfg)
		if res.MessagesPruned > 0 || res.MediaFilesPruned > 0 {
			summary := fmt.Sprintf("Pruned %d messages (%d archived), %d media files (%.2f MB freed) in %s",
				res.MessagesPruned, res.MessagesArchived, res.MediaFilesPruned, float64(res.MediaBytesFreed)/(1024*1024), res.Duration.Round(time.Millisecond))
			log.Println("[DataPrune] " + summary)
			if b != nil && b.logger != nil && channelID() != "" {
				b.logger.log("DATA PRUNE", summary)
			}
		}
	})
}
