package main

import (
	"testing"
)

func TestIsValidDownloadURL(t *testing.T) {
	valid := []string{
		"https://www.youtube.com/watch?v=dQw4w9WgXcQ",
		"http://twitch.tv/example",
		"https://twitter.com/user/status/123456789",
		"https://vimeo.com/1234567",
	}

	invalid := []string{
		"",
		"ftp://example.com/file.mp4",
		"http://localhost:8080/video.mp4",
		"http://127.0.0.1/video.mp4",
		"javascript:alert(1)",
		"not-a-url",
	}

	for _, u := range valid {
		if !isValidDownloadURL(u) {
			t.Errorf("expected %q to be valid", u)
		}
	}

	for _, u := range invalid {
		if isValidDownloadURL(u) {
			t.Errorf("expected %q to be invalid", u)
		}
	}
}

func TestResolveYtDlpBinary(t *testing.T) {
	bin := resolveYtDlpBinary()
	// On this host, yt-dlp is installed
	if bin == "" {
		t.Log("Note: yt-dlp binary not found in tested paths")
	} else {
		t.Logf("Found yt-dlp binary: %s", bin)
	}
}
