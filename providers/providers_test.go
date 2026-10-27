package providers

import (
	"encoding/base64"
	"encoding/json"
	"testing"
)

func TestSniffImageType(t *testing.T) {
	png := base64.StdEncoding.EncodeToString([]byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a})
	jpg := base64.StdEncoding.EncodeToString([]byte{0xff, 0xd8, 0xff, 0xe0})
	if got := sniffImageType(png); got != "image/png" {
		t.Errorf("sniffImageType(png) = %q, want image/png", got)
	}
	if got := sniffImageType(jpg); got != "image/jpeg" {
		t.Errorf("sniffImageType(jpg) = %q, want image/jpeg", got)
	}
}

func TestExtFor(t *testing.T) {
	cases := map[string]string{
		"audio/wav":                "wav",
		"audio/mpeg":               "mp3",
		"audio/ogg":                "ogg",
		"image/png":                "png",
		"image/jpeg":               "jpg",
		"image/webp":               "webp",
		"application/octet-stream": "bin",
	}
	for ct, want := range cases {
		if got := extFor(ct); got != want {
			t.Errorf("extFor(%q) = %q, want %q", ct, got, want)
		}
	}
	// content-type with parameters
	if got := extFor("audio/mpeg; charset=utf-8"); got != "mp3" {
		t.Errorf("extFor(with params) = %q, want mp3", got)
	}
}

func TestOpenAIChatRequestMarshals(t *testing.T) {
	req := openAIChatRequest{
		Model:    "test-model",
		Messages: chatMessages("hi"),
		Stream:   false,
		Extra:    map[string]interface{}{"temperature": 0.7},
	}
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if m["model"] != "test-model" {
		t.Errorf("model = %v", m["model"])
	}
	if m["temperature"] != 0.7 {
		t.Errorf("temperature = %v", m["temperature"])
	}
	if _, ok := m["stream"]; !ok {
		t.Error("stream missing")
	}
}

func TestCommandSpecsCount(t *testing.T) {
	specs := CommandSpecs()
	// We build 6 provider root commands.
	if len(specs) != 6 {
		t.Errorf("CommandSpecs() returned %d specs, want 6", len(specs))
	}
	seen := map[string]bool{}
	for _, s := range specs {
		seen[s.Command.Name] = true
	}
	for _, name := range []string{
		"cohere",
		"cloudflare",
		"groq",
		"mistral",
		"nvidia",
		"openrouter",
	} {
		if !seen[name] {
			t.Errorf("missing command %q", name)
		}
	}
}
