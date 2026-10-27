package a2e

import "testing"

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
