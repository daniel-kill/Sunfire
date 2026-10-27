package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
)

func TestMediaCommandAndTransformMenus(t *testing.T) {
	buildCommandSpecs()
	var command *discordgo.ApplicationCommand
	for _, spec := range commandSpecs {
		if spec.Command.Name == "media" {
			command = spec.Command
			break
		}
	}
	if command == nil {
		t.Fatal("/media is not registered")
	}
	if len(command.Options) != 1 || command.Options[0].Type != discordgo.ApplicationCommandOptionAttachment || !command.Options[0].Required {
		t.Fatalf("/media options = %#v, want one required attachment option", command.Options)
	}

	for _, kind := range []mediaKind{mediaImage, mediaAudio, mediaVideo} {
		effects := mediaEffects(kind)
		if len(effects) == 0 || len(effects) > 25 {
			t.Errorf("media kind %d has %d effects; Discord select menus allow 1-25", kind, len(effects))
		}
		seen := make(map[string]bool, len(effects))
		for _, effect := range effects {
			if effect.value == "" || effect.label == "" || seen[effect.value] {
				t.Errorf("media kind %d has an empty or duplicate effect: %#v", kind, effect)
			}
			seen[effect.value] = true
		}
		if kind != mediaAudio && (!seen["resize"] || !seen["optimize"]) {
			t.Errorf("visual media kind %d is missing resize or optimize", kind)
		}
		if kind == mediaAudio && (seen["resize"] || seen["optimize"]) {
			t.Error("audio menu should not contain image/video-only transformations")
		}
		if kind == mediaVideo {
			if err := validateMediaResizeDimensions(kind, 31, 20); err == nil {
				t.Error("video resize should reject odd width")
			}
			if err := validateMediaResizeDimensions(kind, 32, 21); err == nil {
				t.Error("video resize should reject odd height")
			}
		}
	}
}

func TestParseMediaResizeDimensions(t *testing.T) {
	tests := []struct {
		name       string
		width      string
		height     string
		wantWidth  int
		wantHeight int
		wantErr    bool
	}{
		{name: "preserve aspect ratio", width: "640", wantWidth: 640},
		{name: "explicit dimensions", width: "1920", height: "1080", wantWidth: 1920, wantHeight: 1080},
		{name: "invalid width", width: "0", wantErr: true},
		{name: "invalid height", width: "800", height: "9000", wantErr: true},
		{name: "non-integer", width: "640.5", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			width, height, err := parseMediaResizeDimensions(test.width, test.height)
			if (err != nil) != test.wantErr {
				t.Fatalf("parseMediaResizeDimensions() error = %v, wantErr %v", err, test.wantErr)
			}
			if err == nil && (width != test.wantWidth || height != test.wantHeight) {
				t.Errorf("got dimensions %dx%d, want %dx%d", width, height, test.wantWidth, test.wantHeight)
			}
		})
	}
}

func TestMediaHostAndImageClassification(t *testing.T) {
	for _, host := range []string{"cdn.discordapp.com", "media.discordapp.net", "cdn.discordapp.net"} {
		if !isDiscordMediaHost(host) {
			t.Errorf("expected Discord media host %q to be allowed", host)
		}
	}
	for _, host := range []string{"discordapp.com.evil.example", "example.com", ""} {
		if isDiscordMediaHost(host) {
			t.Errorf("unexpectedly allowed media host %q", host)
		}
	}
	if !isImageAttachment("photo.webp", "application/octet-stream") {
		t.Fatal("expected WebP attachment to be treated as an image")
	}
	if isImageAttachment("clip.mp4", "video/mp4") {
		t.Fatal("expected MP4 attachment not to be treated as an image")
	}
}

func TestMediaEffectsRunThroughFFmpeg(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is not installed")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe is not installed")
	}
	directory := t.TempDir()
	audioPath := filepath.Join(directory, "tone.wav")
	videoPath := filepath.Join(directory, "clip.mp4")
	silentVideoPath := filepath.Join(directory, "silent.mp4")
	rawVideoPath := filepath.Join(directory, "raw.avi")
	imagePath := filepath.Join(directory, "still.png")
	unoptimizedImagePath := filepath.Join(directory, "unoptimized.png")
	gifPath := filepath.Join(directory, "animation.gif")

	runMediaFixtureCommand(t, "-f", "lavfi", "-i", "sine=frequency=1000:duration=0.25", "-c:a", "pcm_s16le", audioPath)
	runMediaFixtureCommand(t,
		"-f", "lavfi", "-i", "testsrc=size=64x64:rate=8:duration=0.25",
		"-f", "lavfi", "-i", "sine=frequency=1000:duration=0.25", "-shortest",
		"-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", videoPath)
	runMediaFixtureCommand(t, "-f", "lavfi", "-i", "testsrc=size=64x64:rate=8:duration=0.25", "-c:v", "libx264", "-pix_fmt", "yuv420p", silentVideoPath)
	runMediaFixtureCommand(t, "-f", "lavfi", "-i", "testsrc=size=64x64:rate=8:duration=0.5", "-c:v", "rawvideo", "-pix_fmt", "yuv420p", rawVideoPath)
	runMediaFixtureCommand(t, "-f", "lavfi", "-i", "color=c=red:s=64x64", "-frames:v", "1", imagePath)
	runMediaFixtureCommand(t, "-f", "lavfi", "-i", "testsrc=size=256x256", "-frames:v", "1", "-compression_level", "0", unoptimizedImagePath)
	runMediaFixtureCommand(t, "-f", "lavfi", "-i", "testsrc=size=32x32:rate=5:duration=0.5", "-loop", "0", gifPath)

	cases := []struct {
		kind               mediaKind
		path               string
		filename           string
		content            string
		effects            []mediaEffect
		expectOptimization bool
	}{
		{kind: mediaAudio, path: audioPath, filename: "tone.wav", content: "audio/wav"},
		{kind: mediaVideo, path: videoPath, filename: "clip.mp4", content: "video/mp4"},
		{kind: mediaVideo, path: silentVideoPath, filename: "silent.mp4", content: "video/mp4", effects: videoMediaEffects[22:]},
		{kind: mediaVideo, path: rawVideoPath, filename: "raw.avi", content: "video/x-msvideo", effects: []mediaEffect{{value: "optimize", optimize: true}}, expectOptimization: true},
		{kind: mediaImage, path: imagePath, filename: "still.png", content: "image/png"},
		{kind: mediaImage, path: unoptimizedImagePath, filename: "unoptimized.png", content: "image/png", effects: []mediaEffect{{value: "optimize", optimize: true}}, expectOptimization: true},
		{kind: mediaImage, path: gifPath, filename: "animation.gif", content: "image/gif"},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(fmt.Sprintf("kind-%d-%s", testCase.kind, testCase.filename), func(t *testing.T) {
			inputInfo, err := os.Stat(testCase.path)
			if err != nil {
				t.Fatal(err)
			}
			frameCount := 0
			if testCase.kind == mediaImage {
				frameCount, err = probeImageFrameCount(testCase.path)
				if err != nil {
					t.Fatalf("probe image frame count: %v", err)
				}
			}
			session := &mediaSession{
				ownerID: "test", directory: directory, inputPath: testCase.path,
				filename: testCase.filename, contentType: testCase.content,
				inputSize: inputInfo.Size(), kind: testCase.kind, frameCount: frameCount, expiresAt: time.Now().Add(time.Minute),
			}
			effects := mediaEffects(testCase.kind)
			if testCase.effects != nil {
				effects = testCase.effects
			}
			for _, effect := range effects {
				if effect.value == "resize" {
					effect = mediaEffect{value: "resize", label: "resize", resizeWidth: 32, resizeHeight: 24}
				}
				outputPath, outputName, err := transformMedia(session, effect)
				if err != nil {
					t.Errorf("effect %q failed: %v", effect.value, err)
					continue
				}
				if effect.optimize {
					outputInfo, err := os.Stat(outputPath)
					if err != nil {
						t.Errorf("stat optimized media: %v", err)
					} else if outputInfo.Size() > inputInfo.Size() {
						t.Errorf("optimize returned %d bytes, larger than original %d bytes", outputInfo.Size(), inputInfo.Size())
					}
					if testCase.expectOptimization && outputName == filepath.Base(testCase.filename) {
						t.Errorf("expected optimization to produce a smaller file than the %d-byte original", inputInfo.Size())
					}
					if testCase.kind == mediaImage && outputName != filepath.Base(testCase.filename) {
						originalHashes := mediaFrameHashes(t, testCase.path)
						optimizedHashes := mediaFrameHashes(t, outputPath)
						if strings.Join(originalHashes, ",") != strings.Join(optimizedHashes, ",") {
							t.Errorf("lossless image optimization changed decoded pixels: %v != %v", originalHashes, optimizedHashes)
						}
					}
					if testCase.kind == mediaVideo && outputName == "transformed.mkv" {
						originalHashes := mediaFrameHashes(t, testCase.path)
						optimizedHashes := mediaFrameHashes(t, outputPath)
						if strings.Join(originalHashes, ",") != strings.Join(optimizedHashes, ",") {
							t.Errorf("lossless video optimization changed decoded frames: %v != %v", originalHashes, optimizedHashes)
						}
					}
				}
				if effect.value == "resize" {
					dimensions := probeMediaDimensions(t, outputPath)
					if dimensions.width != 32 || dimensions.height != 24 {
						t.Errorf("explicit resize dimensions = %dx%d, want 32x24", dimensions.width, dimensions.height)
					}
				}
			}
			if testCase.kind != mediaAudio {
				resize := mediaEffect{value: "resize", label: "resize", resizeWidth: 32}
				resizedPath, _, err := transformMedia(session, resize)
				if err != nil {
					t.Fatalf("aspect-preserving resize failed: %v", err)
				}
				dimensions := probeMediaDimensions(t, resizedPath)
				if dimensions.width != 32 || dimensions.height != 32 {
					t.Errorf("resized dimensions = %dx%d, want 32x32", dimensions.width, dimensions.height)
				}
			}
		})
	}
}

func mediaFrameHashes(t *testing.T, path string) []string {
	t.Helper()
	command := exec.Command("ffmpeg", "-v", "error", "-i", path, "-map", "0:v:0", "-vf", "format=rgba", "-f", "framemd5", "-")
	output, err := command.Output()
	if err != nil {
		t.Fatalf("frame hash: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) == 0 {
		t.Fatal("frame hash output was empty")
	}
	var hashes []string
	for _, line := range lines {
		if strings.HasPrefix(line, "#") || strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Split(line, ",")
		hashes = append(hashes, strings.TrimSpace(fields[len(fields)-1]))
	}
	return hashes
}

func probeMediaDimensions(t *testing.T, path string) struct{ width, height int } {
	t.Helper()
	command := exec.Command("ffprobe", "-v", "error", "-select_streams", "v:0", "-show_entries", "stream=width,height", "-of", "csv=s=x:p=0", path)
	output, err := command.Output()
	if err != nil {
		t.Fatalf("ffprobe dimensions: %v", err)
	}
	var dimensions struct{ width, height int }
	if _, err := fmt.Sscanf(string(output), "%dx%d", &dimensions.width, &dimensions.height); err != nil {
		t.Fatalf("parse ffprobe dimensions %q: %v", output, err)
	}
	return dimensions
}

func runMediaFixtureCommand(t *testing.T, args ...string) {
	t.Helper()
	command := exec.Command("ffmpeg", append([]string{"-nostdin", "-hide_banner", "-loglevel", "error", "-y"}, args...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Skipf("could not create FFmpeg test fixtures: %v (%s)", err, output)
	}
}
