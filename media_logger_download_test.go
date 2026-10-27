package main

import (
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"
)

type mediaReadErrorBody struct{}

func (mediaReadErrorBody) Read([]byte) (int, error) { return 0, errors.New("injected body read error") }
func (mediaReadErrorBody) Close() error             { return nil }

func TestDownloadAndSaveMediaReportsHTTPAndBodyFailures(t *testing.T) {
	tests := []struct {
		name      string
		transport func(*http.Request) (*http.Response, error)
		wantError bool
	}{
		{
			name: "http status",
			transport: func(req *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusServiceUnavailable, Status: "503 Service Unavailable", Body: io.NopCloser(strings.NewReader("down")), Request: req}, nil
			},
			wantError: true,
		},
		{
			name: "body read",
			transport: func(req *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Body: mediaReadErrorBody{}, Request: req}, nil
			},
			wantError: true,
		},
		{
			name: "transport",
			transport: func(*http.Request) (*http.Response, error) {
				return nil, errors.New("injected transport failure")
			},
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			oldBase, oldClient := mediaLogsBaseDir, mediaHTTPClient
			mediaLogsBaseDir = root
			mediaHTTPClient = &http.Client{Transport: notifierRoundTripFunc(tt.transport)}
			t.Cleanup(func() {
				mediaLogsBaseDir = oldBase
				mediaHTTPClient = oldClient
			})

			err := downloadAndSaveMedia("guild", "message", &discordgo.MessageAttachment{
				ID: "attachment", Filename: "photo.png", URL: "https://media.example/photo.png",
			}, "images")
			if (err != nil) != tt.wantError {
				t.Fatalf("downloadAndSaveMedia error = %v, wantError %t", err, tt.wantError)
			}
			files, readErr := filepath.Glob(filepath.Join(root, "guild", "images", "*"))
			if readErr != nil {
				t.Fatal(readErr)
			}
			if len(files) != 0 {
				t.Fatalf("failed download left partial files: %v", files)
			}
		})
	}
}
