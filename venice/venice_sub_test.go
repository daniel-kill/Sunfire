package venice

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"
)

func TestTTSFormatForModel(t *testing.T) {
	cases := []struct {
		model string
		want  string
	}{
		{"tts-chatterbox-hd", "wav"},
		{"tts-minimax-speech-02-hd", "wav"},
		{"tts-kokoro", "mp3"},
		{"", "mp3"},
	}
	for _, c := range cases {
		if got := ttsFormatForModel(c.model); got != c.want {
			t.Errorf("ttsFormatForModel(%q) = %q, want %q", c.model, got, c.want)
		}
	}
}

func TestMusicQueueMessageShowsDurationAndQuotedCost(t *testing.T) {
	got := musicQueueMessage("minimax-music-v26", 240, 0.18)
	if !strings.Contains(got, "4m") || !strings.Contains(got, "$0.1800") {
		t.Fatalf("musicQueueMessage() = %q, want duration and cost", got)
	}
}

func TestMusicCommandRegistersDurationBounds(t *testing.T) {
	for _, spec := range CommandSpecs() {
		if spec.Command.Name != "venice" {
			continue
		}
		for _, subcommand := range spec.Command.Options {
			if subcommand.Name != "music" {
				continue
			}
			for _, option := range subcommand.Options {
				if option.Name != "duration" {
					continue
				}
				if option.Type != discordgo.ApplicationCommandOptionInteger ||
					option.MinValue == nil || *option.MinValue != minMusicDurationSeconds ||
					option.MaxValue != maxMusicDurationSeconds || option.Required {
					t.Fatalf("duration option has unexpected constraints: %#v", option)
				}
				return
			}
		}
	}
	t.Fatal("music duration option was not registered")
}

func TestFormatDuration(t *testing.T) {
	if got := formatDuration(45); got != "45s" {
		t.Errorf("formatDuration(45) = %q, want 45s", got)
	}
	if got := formatDuration(105); got != "1m 45s" {
		t.Errorf("formatDuration(105) = %q, want 1m 45s", got)
	}
	if got := formatDuration(120); got != "2m" {
		t.Errorf("formatDuration(120) = %q, want 2m", got)
	}
}

func TestQuoteAudioAndQueueAudioSendRecommendedRequestFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("Authorization = %q, want bearer token", r.Header.Get("Authorization"))
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("reading request body: %v", err)
			return
		}
		var payload map[string]json.RawMessage
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Errorf("decoding request body: %v", err)
			return
		}

		switch r.URL.Path {
		case "/audio/quote":
			if got := string(payload["model"]); got != `"minimax-music-v26"` {
				t.Errorf("quote model = %s", got)
			}
			if got := string(payload["duration_seconds"]); got != "240" {
				t.Errorf("quote duration_seconds = %s, want 240", got)
			}
			_, _ = io.WriteString(w, `{"quote":0.18}`)
		case "/audio/queue":
			for key, want := range map[string]string{
				"model":            `"minimax-music-v26"`,
				"prompt":           `"dark synth pop"`,
				"lyrics_prompt":    `"[Verse]\nHello\n[Chorus]\nAgain"`,
				"duration_seconds": "240",
			} {
				if got := string(payload[key]); got != want {
					t.Errorf("queue %s = %s, want %s", key, got, want)
				}
			}
			if _, ok := payload["force_instrumental"]; ok {
				t.Error("force_instrumental should be omitted for vocal songs")
			}
			_, _ = io.WriteString(w, `{"queue_id":"queue-123"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := &veniceClient{BaseURL: server.URL, APIKey: "test-key", HTTP: server.Client()}
	quote, err := client.QuoteAudio("minimax-music-v26", 240)
	if err != nil {
		t.Fatalf("QuoteAudio() error = %v", err)
	}
	if quote != 0.18 {
		t.Fatalf("QuoteAudio() = %v, want 0.18", quote)
	}

	queueID, err := client.QueueAudio("minimax-music-v26", "dark synth pop", "[Verse]\nHello\n[Chorus]\nAgain", false, 240)
	if err != nil {
		t.Fatalf("QueueAudio() error = %v", err)
	}
	if queueID != "queue-123" {
		t.Fatalf("QueueAudio() = %q, want queue-123", queueID)
	}
}

func TestWaitAudioRetrievesAudioAndCompletesQueue(t *testing.T) {
	completed := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/audio/retrieve":
			w.Header().Set("Content-Type", "audio/mpeg")
			_, _ = io.WriteString(w, "audio-data")
		case "/audio/complete":
			completed = true
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := &veniceClient{BaseURL: server.URL, APIKey: "test-key", HTTP: server.Client()}
	data, ext, err := client.WaitAudio("minimax-music-v26", "queue-123", 1, 1)
	if err != nil {
		t.Fatalf("WaitAudio() error = %v", err)
	}
	if string(data) != "audio-data" || ext != "mp3" {
		t.Fatalf("WaitAudio() = (%q, %q), want audio bytes and mp3", data, ext)
	}
	if !completed {
		t.Fatal("WaitAudio() did not call /audio/complete")
	}
}
