package a2e

import (
	"os"
	"testing"
)

func TestRewriteAudioHost(t *testing.T) {
	cases := []struct{ in, want string }{
		{
			"https://speech.downloadaivideo.com/1day/fa/stable/abc.mp3",
			"https://r2-download.makefun.ai/speech/1day/fa/stable/abc.mp3",
		},
		{
			"https://other.host.com/x.mp3",
			"https://other.host.com/x.mp3",
		},
		{"", ""},
	}
	for _, c := range cases {
		if got := rewriteAudioHost(c.in); got != c.want {
			t.Errorf("rewriteAudioHost(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestIsValidVoiceID(t *testing.T) {
	if !isValidVoiceID("6ab51675c9eed119d535cc23") {
		t.Error("valid id rejected")
	}
	for _, bad := range []string{"", "short", "6ab51675c9eed119d535cc2g", "6ab51675c9eed119d535cc23 name"} {
		if isValidVoiceID(bad) {
			t.Errorf("invalid id accepted: %q", bad)
		}
	}
}

func TestSanitizeVoiceID(t *testing.T) {
	if got := sanitizeVoiceID("6ab51675c9eed119d535cc23 name"); got != "6ab51675c9eed119d535cc23" {
		t.Errorf("sanitize = %q", got)
	}
}
func TestA2EConfigPathEnvOverride(t *testing.T) {
	tmpDir := t.TempDir()
	customPath := tmpDir + "/custom_a2e_config.json"
	data := []byte(`{
		"A2E_API_KEY": "test-env-key",
		"a2eBaseUrl": "https://custom.a2e.ai",
		"defaultImageModel": "custom-image",
		"defaultVideoModel": "custom-video"
	}`)
	if err := os.WriteFile(customPath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("SUNFIRE_CONFIG_FILE", customPath)
	if got := configPath(); got != customPath {
		t.Errorf("configPath() = %q, want %q", got, customPath)
	}

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig() failed: %v", err)
	}
	if cfg.A2EAPIKey != "test-env-key" {
		t.Errorf("A2EAPIKey = %q, want test-env-key", cfg.A2EAPIKey)
	}
	if cfg.A2EBaseURL != "https://custom.a2e.ai" {
		t.Errorf("A2EBaseURL = %q, want https://custom.a2e.ai", cfg.A2EBaseURL)
	}
	if cfg.DefaultImageModel != "custom-image" {
		t.Errorf("DefaultImageModel = %q, want custom-image", cfg.DefaultImageModel)
	}
	if cfg.DefaultVideoModel != "custom-video" {
		t.Errorf("DefaultVideoModel = %q, want custom-video", cfg.DefaultVideoModel)
	}
}
