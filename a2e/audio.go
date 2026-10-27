package a2e

import (
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// downloadAudioHost is the working host for A2E TTS audio. A2E returns URLs on
// speech.downloadaivideo.com whose DNS is broken (global NXDOMAIN / CF 530);
// the same files are served by this host under a /speech/ path prefix.
// rewriteAudioHost maps one to the other.
const downloadAudioHost = "https://r2-download.makefun.ai"

// rewriteAudioHost rewrites an A2E speech.downloadaivideo.com URL to the
// working r2-download.makefun.ai host, adding the /speech/ path prefix that
// the mirror uses. Returns the input unchanged if it isn't on the broken host.
func rewriteAudioHost(u string) string {
	if strings.HasPrefix(u, "https://speech.downloadaivideo.com/") {
		return downloadAudioHost + "/speech/" + strings.TrimPrefix(u, "https://speech.downloadaivideo.com/")
	}
	return u
}

// httpClient for audio downloads (separate timeout; audio can be a few MB).
var audioHTTPClient = &http.Client{Timeout: 60 * time.Second}

// downloadAudio fetches the audio bytes at url, retrying briefly to account
// for the mirror host's propagation delay (a fresh file can 404 for a few
// seconds after generation before it appears on r2-download.makefun.ai).
func downloadAudio(url string) ([]byte, error) {
	var lastErr error
	for attempt := 1; attempt <= 4; attempt++ {
		data, err := fetchAudio(url)
		if err == nil {
			return data, nil
		}
		lastErr = err
		time.Sleep(time.Duration(attempt) * time.Second) // 1s, 2s, 3s
	}
	return nil, lastErr
}

func fetchAudio(url string) ([]byte, error) {
	resp, err := audioHTTPClient.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, resp.Body)
		return nil, &audioDownloadError{status: resp.StatusCode}
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	// Sanity: must be non-trivial audio.
	if len(data) == 0 {
		return nil, &audioDownloadError{status: 0}
	}
	log.Printf("[audio] downloaded %d bytes from %s", len(data), url)
	return data, nil
}

type audioDownloadError struct{ status int }

func (e *audioDownloadError) Error() string {
	if e.status == 0 {
		return "audio download returned empty body"
	}
	return "audio download HTTP " + strconv.Itoa(e.status)
}
