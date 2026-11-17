// data_pruner_test.go — unit tests for automated log pruning, rolling retention, and archival.
package main

import (
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPruneMessageLogsByAgeAndCap(t *testing.T) {
	tmpDir := t.TempDir()
	messageLogsBaseDir = tmpDir
	defer func() { messageLogsBaseDir = "" }()

	// Overwrite dataDir for testing archives
	t.Setenv("SUNFIRE_DATA_DIR", tmpDir)

	guildID := "test_guild_123"
	authorID := "user_456"
	guildDir := filepath.Join(tmpDir, guildID)
	if err := os.MkdirAll(guildDir, 0o700); err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	// Create 5 records: 2 older than 30 days, 3 recent
	records := []MessageRecord{
		{
			Timestamp:      now.AddDate(0, 0, -45).Format(time.RFC3339),
			MessageID:      "msg_old_1",
			ChannelID:      "ch_1",
			AuthorID:       authorID,
			AuthorUsername: "OldUser",
			Content:        "Old message 1",
		},
		{
			Timestamp:      now.AddDate(0, 0, -35).Format(time.RFC3339),
			MessageID:      "msg_old_2",
			ChannelID:      "ch_1",
			AuthorID:       authorID,
			AuthorUsername: "OldUser",
			Content:        "Old message 2",
		},
		{
			Timestamp:      now.AddDate(0, 0, -5).Format(time.RFC3339),
			MessageID:      "msg_new_1",
			ChannelID:      "ch_1",
			AuthorID:       authorID,
			AuthorUsername: "NewUser",
			Content:        "New message 1",
		},
		{
			Timestamp:      now.AddDate(0, 0, -2).Format(time.RFC3339),
			MessageID:      "msg_new_2",
			ChannelID:      "ch_1",
			AuthorID:       authorID,
			AuthorUsername: "NewUser",
			Content:        "New message 2",
		},
		{
			Timestamp:      now.Format(time.RFC3339),
			MessageID:      "msg_new_3",
			ChannelID:      "ch_1",
			AuthorID:       authorID,
			AuthorUsername: "NewUser",
			Content:        "New message 3",
		},
	}

	filePath := filepath.Join(guildDir, authorID+".json")
	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filePath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	// Prune with 30-day retention and max 2 records per user
	cfg := DataPruneConfig{
		Enabled:           true,
		RetentionDays:     30,
		MaxRecordsPerUser: 2,
		ArchivePrunedLogs: true,
	}

	filesScanned, prunedCount, archivedCount, errs := PruneMessageLogs(cfg)
	if len(errs) > 0 {
		t.Fatalf("unexpected prune errors: %v", errs)
	}
	if filesScanned != 1 {
		t.Errorf("filesScanned = %d, want 1", filesScanned)
	}
	// 2 pruned by age (-45d, -35d), plus 1 excess pruned to meet cap of 2 (msg_new_1)
	if prunedCount != 3 {
		t.Errorf("prunedCount = %d, want 3", prunedCount)
	}
	if archivedCount != 3 {
		t.Errorf("archivedCount = %d, want 3", archivedCount)
	}

	// Verify kept records in author file
	keptData, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	var keptRecords []MessageRecord
	if err := json.Unmarshal(keptData, &keptRecords); err != nil {
		t.Fatal(err)
	}
	if len(keptRecords) != 2 {
		t.Fatalf("keptRecords count = %d, want 2", len(keptRecords))
	}
	if keptRecords[0].MessageID != "msg_new_2" || keptRecords[1].MessageID != "msg_new_3" {
		t.Errorf("unexpected kept records: %+v", keptRecords)
	}

	// Verify archive gzip file was created and readable
	archiveDir := filepath.Join(tmpDir, "Archives", "MessageLogs", guildID)
	entries, err := os.ReadDir(archiveDir)
	if err != nil || len(entries) == 0 {
		t.Fatalf("archive directory missing or empty: %v", err)
	}
	gzFile := filepath.Join(archiveDir, entries[0].Name())
	f, err := os.Open(gzFile)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	unzipped, err := io.ReadAll(gz)
	if err != nil {
		t.Fatal(err)
	}
	var archivedRecords []MessageRecord
	if err := json.Unmarshal(unzipped, &archivedRecords); err != nil {
		t.Fatal(err)
	}
	if len(archivedRecords) != 3 {
		t.Errorf("archivedRecords count = %d, want 3", len(archivedRecords))
	}
}

func TestPruneMediaLogsAgeAndSize(t *testing.T) {
	tmpDir := t.TempDir()
	mediaLogsBaseDir = tmpDir
	defer func() { mediaLogsBaseDir = "" }()

	guildDir := filepath.Join(tmpDir, "guild_abc", "images")
	if err := os.MkdirAll(guildDir, 0o700); err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	// 1. Old file (40 days old)
	oldPath := filepath.Join(guildDir, "old_image.png")
	if err := os.WriteFile(oldPath, []byte("old-data"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldTime := now.AddDate(0, 0, -40)
	_ = os.Chtimes(oldPath, oldTime, oldTime)

	// 2. Recent file A (800 KB)
	recentPathA := filepath.Join(guildDir, "recent_a.png")
	dataA := make([]byte, 800*1024)
	if err := os.WriteFile(recentPathA, dataA, 0o600); err != nil {
		t.Fatal(err)
	}
	timeA := now.Add(-2 * time.Hour)
	_ = os.Chtimes(recentPathA, timeA, timeA)

	// 3. Recent file B (400 KB)
	recentPathB := filepath.Join(guildDir, "recent_b.png")
	dataB := make([]byte, 400*1024)
	if err := os.WriteFile(recentPathB, dataB, 0o600); err != nil {
		t.Fatal(err)
	}
	timeB := now.Add(-1 * time.Hour)
	_ = os.Chtimes(recentPathB, timeB, timeB)

	// Prune with 30-day retention and 1MB size limit (triggering size pruning on recent_a)
	cfg := DataPruneConfig{
		Enabled:            true,
		MediaRetentionDays: 30,
		MaxMediaSizeMB:     1, // 1MB threshold -> 2MB current triggers pruning of oldest file
	}

	filesScanned, filesPruned, bytesFreed, errs := PruneMediaLogs(cfg)
	if len(errs) > 0 {
		t.Fatalf("unexpected media prune errors: %v", errs)
	}
	if filesScanned != 3 {
		t.Errorf("filesScanned = %d, want 3", filesScanned)
	}
	// Old file pruned by age, plus recent_a pruned by size
	if filesPruned != 2 {
		t.Errorf("filesPruned = %d, want 2", filesPruned)
	}
	if bytesFreed < 800*1024 {
		t.Errorf("bytesFreed = %d, expected >= 800KB", bytesFreed)
	}

	// Verify old_image is gone
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Errorf("expected %s to be deleted", oldPath)
	}
	// Verify recent_a is gone
	if _, err := os.Stat(recentPathA); !os.IsNotExist(err) {
		t.Errorf("expected %s to be deleted by size pruner", recentPathA)
	}
	// Verify newest file recent_b is kept
	if _, err := os.Stat(recentPathB); err != nil {
		t.Errorf("expected %s to be kept", recentPathB)
	}
}
