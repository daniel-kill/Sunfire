package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCategorizeMedia(t *testing.T) {
	tests := []struct {
		filename    string
		contentType string
		expected    string
	}{
		{"image.png", "image/png", "images"},
		{"photo.jpg", "", "images"},
		{"clip.mp4", "video/mp4", "videos"},
		{"movie.mkv", "", "videos"},
		{"song.mp3", "audio/mpeg", "audio"},
		{"voice.ogg", "audio/ogg", "audio"},
		{"track.m4a", "", "audio"},
		{"document.pdf", "application/pdf", ""},
		{"script.sh", "text/x-shellscript", ""},
	}

	for _, tt := range tests {
		got := categorizeMedia(tt.filename, tt.contentType)
		if got != tt.expected {
			t.Errorf("categorizeMedia(%q, %q) = %q; want %q", tt.filename, tt.contentType, got, tt.expected)
		}
	}
}

func TestSanitizeFilename(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"normal.png", "normal.png"},
		{"path/to/file.jpg", "file.jpg"},
		{"../../evil.mp4", "evil.mp4"},
		{"my video (1).mp4", "my_video__1_.mp4"},
		{"hello@world!.png", "hello_world_.png"},
	}

	for _, tt := range tests {
		got := sanitizeFilename(tt.input)
		if got != tt.expected {
			t.Errorf("sanitizeFilename(%q) = %q; want %q", tt.input, got, tt.expected)
		}
	}
}

func TestMediaDeduplication(t *testing.T) {
	tmpDir := t.TempDir()
	mediaLogsBaseDir = tmpDir
	defer func() {
		mediaLogsBaseDir = ""
	}()

	serverID := "1557930395868209162"
	serverDir := filepath.Join(tmpDir, serverID)
	imagesDir := filepath.Join(serverDir, "images")
	videosDir := filepath.Join(serverDir, "videos")

	if err := os.MkdirAll(imagesDir, 0755); err != nil {
		t.Fatalf("failed to create images dir: %v", err)
	}
	if err := os.MkdirAll(videosDir, 0755); err != nil {
		t.Fatalf("failed to create videos dir: %v", err)
	}

	// 1. Create duplicate images
	content1 := []byte("identical image content for testing hash deduplication")
	fileA := filepath.Join(imagesDir, "img1.png")
	fileB := filepath.Join(imagesDir, "img2.png")
	fileC := filepath.Join(imagesDir, "img3.png")

	if err := os.WriteFile(fileA, content1, 0644); err != nil {
		t.Fatalf("failed to write fileA: %v", err)
	}
	// Give fileA older timestamp
	oldTime := time.Now().Add(-10 * time.Minute)
	_ = os.Chtimes(fileA, oldTime, oldTime)

	if err := os.WriteFile(fileB, content1, 0644); err != nil {
		t.Fatalf("failed to write fileB: %v", err)
	}
	if err := os.WriteFile(fileC, content1, 0644); err != nil {
		t.Fatalf("failed to write fileC: %v", err)
	}

	// 2. Create unique video
	content2 := []byte("different video content")
	fileD := filepath.Join(videosDir, "vid1.mp4")
	if err := os.WriteFile(fileD, content2, 0644); err != nil {
		t.Fatalf("failed to write fileD: %v", err)
	}

	// Test findFileByHash before deduplication
	hash1, err := computeFileHash(fileA)
	if err != nil {
		t.Fatalf("computeFileHash failed: %v", err)
	}
	foundPath, found := findFileByHash(serverDir, hash1)
	if !found {
		t.Errorf("expected findFileByHash to find file with hash %s", hash1)
	}
	if foundPath == "" {
		t.Errorf("expected non-empty foundPath")
	}

	// 3. Run DeduplicateMediaFolder
	deleted, freed, err := DeduplicateMediaFolder(serverDir)
	if err != nil {
		t.Fatalf("DeduplicateMediaFolder failed: %v", err)
	}

	if deleted != 2 {
		t.Errorf("expected 2 deleted duplicates, got %d", deleted)
	}
	expectedFreed := int64(len(content1) * 2)
	if freed != expectedFreed {
		t.Errorf("expected freed %d bytes, got %d", expectedFreed, freed)
	}

	// Verify fileA (oldest) still exists
	if _, err := os.Stat(fileA); os.IsNotExist(err) {
		t.Errorf("expected original fileA to exist, but was deleted")
	}

	// Verify fileB and fileC were deleted
	if _, err := os.Stat(fileB); !os.IsNotExist(err) {
		t.Errorf("expected duplicate fileB to be deleted")
	}
	if _, err := os.Stat(fileC); !os.IsNotExist(err) {
		t.Errorf("expected duplicate fileC to be deleted")
	}

	// Verify fileD (unique) still exists
	if _, err := os.Stat(fileD); os.IsNotExist(err) {
		t.Errorf("expected unique fileD to exist, but was deleted")
	}

	// 4. Test DeduplicateAllMediaLogs when no duplicates remain
	deleted2, freed2, err := DeduplicateAllMediaLogs()
	if err != nil {
		t.Fatalf("DeduplicateAllMediaLogs failed: %v", err)
	}
	if deleted2 != 0 || freed2 != 0 {
		t.Errorf("expected 0 duplicates on second run, got deleted=%d, freed=%d", deleted2, freed2)
	}
}
