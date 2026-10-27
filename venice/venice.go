package venice

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"strings"
	"time"
)

// veniceClient is a minimal HTTP client for the Venice API (base /api/v1).
type veniceClient struct {
	BaseURL string
	APIKey  string
	HTTP    *http.Client
}

func newVeniceClient(cfg *Config) *veniceClient {
	return &veniceClient{
		BaseURL: strings.TrimSuffix(cfg.VeniceBaseURL, "/"),
		APIKey:  cfg.VeniceAPIKey,
		HTTP:    &http.Client{Timeout: 180 * time.Second},
	}
}

func (v *veniceClient) authHeader() string { return "Bearer " + v.APIKey }

// doJSON sends a JSON request and decodes the response into out.
func (v *veniceClient) doJSON(method, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, v.BaseURL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", v.authHeader())
	req.Header.Set("Content-Type", "application/json")
	resp, err := v.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	bodyBytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("Venice API %s %s -> HTTP %d: %s", method, path, resp.StatusCode, truncate(string(bodyBytes), 500))
	}
	if out != nil && len(bodyBytes) > 0 {
		return json.Unmarshal(bodyBytes, out)
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// --- Chat ---

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model            string         `json:"model"`
	Messages         []chatMessage  `json:"messages"`
	VeniceParameters map[string]any `json:"venice_parameters,omitempty"`
}

type chatChoice struct {
	Message struct {
		Content string `json:"content"`
	} `json:"message"`
}

type chatResponse struct {
	Choices []chatChoice `json:"choices"`
}

// Chat sends a chat completion; returns the assistant text.
func (v *veniceClient) Chat(model, system, user string, web bool) (string, error) {
	messages := []chatMessage{}
	if system != "" {
		messages = append(messages, chatMessage{Role: "system", Content: system})
	}
	messages = append(messages, chatMessage{Role: "user", Content: user})
	req := chatRequest{Model: model, Messages: messages}
	if web {
		req.VeniceParameters = map[string]any{"enable_web_search": "on"}
	}
	var resp chatResponse
	if err := v.doJSON("POST", "/chat/completions", req, &resp); err != nil {
		return "", err
	}
	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("no choices in chat response")
	}
	return strings.TrimSpace(resp.Choices[0].Message.Content), nil
}

// --- Image ---

type imageRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
	Size   string `json:"size,omitempty"`
}

type imageResponse struct {
	ID     string   `json:"id"`
	Images []string `json:"images"`
}

// GenerateImage returns the decoded image bytes + detected extension.
func (v *veniceClient) GenerateImage(model, prompt, size string) ([]byte, string, error) {
	req := imageRequest{Model: model, Prompt: prompt, Size: size}
	var resp imageResponse
	if err := v.doJSON("POST", "/image/generate", req, &resp); err != nil {
		return nil, "", err
	}
	if len(resp.Images) == 0 {
		return nil, "", fmt.Errorf("no image in response")
	}
	b64 := resp.Images[0]
	// Strip possible data URL prefix.
	if i := strings.Index(b64, "base64,"); i >= 0 {
		b64 = b64[i+7:]
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, "", fmt.Errorf("decoding image: %w", err)
	}
	return raw, detectImageExt(raw), nil
}

func detectImageExt(b []byte) string {
	if len(b) >= 4 {
		switch {
		case b[0] == 0x89 && b[1] == 'P' && b[2] == 'N' && b[3] == 'G':
			return "png"
		case b[0] == 0xFF && b[1] == 0xD8:
			return "jpg"
		case b[0] == 'R' && b[1] == 'I' && b[2] == 'F' && b[3] == 'F':
			return "webp"
		}
	}
	return "png"
}

// --- TTS ---

type ttsRequest struct {
	Model          string `json:"model"`
	Input          string `json:"input"`
	Voice          string `json:"voice"`
	ResponseFormat string `json:"response_format"`
}

// TTS generates speech audio and returns the bytes + detected extension.
func (v *veniceClient) TTS(model, text, voice, format string) ([]byte, string, error) {
	req := ttsRequest{Model: model, Input: text, Voice: voice, ResponseFormat: format}
	payload, _ := json.Marshal(req)
	httpReq, err := http.NewRequest("POST", v.BaseURL+"/audio/speech", bytes.NewReader(payload))
	if err != nil {
		return nil, "", err
	}
	httpReq.Header.Set("Authorization", v.authHeader())
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := v.HTTP.Do(httpReq)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 50<<20))
	if err != nil {
		return nil, "", err
	}
	if resp.StatusCode != 200 {
		return nil, "", fmt.Errorf("TTS HTTP %d: %s", resp.StatusCode, truncate(string(b), 500))
	}
	ct := resp.Header.Get("Content-Type")
	ext := "mp3"
	switch {
	case strings.Contains(ct, "wav"):
		ext = "wav"
	case strings.Contains(ct, "flac"):
		ext = "flac"
	case strings.Contains(ct, "ogg"):
		ext = "ogg"
	}
	return b, ext, nil
}

// --- Voice Cloning ---

// clonedVoiceResponse is the response from POST /audio/voices.
type clonedVoiceResponse struct {
	ID    string `json:"id"`
	Model string `json:"model"`
}

// CloneVoice uploads an audio sample to clone a voice and returns the
// `vv_...` handle. model must be tts-chatterbox-hd (default) or
// tts-minimax-speech-02-hd. The handle is passed as `voice` to TTS with the
// same model.
func (v *veniceClient) CloneVoice(model string, sample []byte, filename string) (*clonedVoiceResponse, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)

	// model field
	if err := mw.WriteField("model", model); err != nil {
		return nil, err
	}
	// file field
	fw, err := mw.CreateFormFile("file", filename)
	if err != nil {
		return nil, err
	}
	if _, err := fw.Write(sample); err != nil {
		return nil, err
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequest("POST", v.BaseURL+"/audio/voices", &buf)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Authorization", v.authHeader())
	httpReq.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := v.HTTP.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("voice clone HTTP %d: %s", resp.StatusCode, truncate(string(b), 500))
	}
	var out clonedVoiceResponse
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("parsing clone response: %w", err)
	}
	if out.ID == "" {
		return nil, fmt.Errorf("clone response missing id: %s", truncate(string(b), 200))
	}
	return &out, nil
}

// ttsFormatForModel returns the response_format supported by a TTS model.
// tts-chatterbox-hd and tts-minimax-speech-02-hd support only WAV; other TTS
// models (e.g. tts-kokoro) accept mp3.
func ttsFormatForModel(model string) string {
	switch model {
	case "tts-chatterbox-hd", "tts-minimax-speech-02-hd":
		return "wav"
	default:
		return "mp3"
	}
}

// --- STT ---

// Transcribe sends an audio file to /audio/transcriptions (multipart).
func (v *veniceClient) Transcribe(model, filePath, format string) (string, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("model", model)
	mw.WriteField("response_format", format)
	fw, err := mw.CreateFormFile("file", filePath)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(fw, f); err != nil {
		return "", err
	}
	mw.Close()

	httpReq, err := http.NewRequest("POST", v.BaseURL+"/audio/transcriptions", &buf)
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Authorization", v.authHeader())
	httpReq.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := v.HTTP.Do(httpReq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("STT HTTP %d: %s", resp.StatusCode, truncate(string(b), 500))
	}
	// If response_format is text, return raw; otherwise try to extract text.
	if format == "text" {
		return strings.TrimSpace(string(b)), nil
	}
	var parsed map[string]any
	if err := json.Unmarshal(b, &parsed); err == nil {
		if t, ok := parsed["text"].(string); ok {
			return strings.TrimSpace(t), nil
		}
	}
	return strings.TrimSpace(string(b)), nil
}

// --- Balance ---

type balanceResponse struct {
	Balances struct {
		USD  *float64 `json:"usd"`
		DIEM *float64 `json:"diem"`
	} `json:"balances"`
	CanConsume          bool   `json:"canConsume"`
	ConsumptionCurrency string `json:"consumptionCurrency"`
}

// Balance returns a formatted balance string.
func (v *veniceClient) Balance() (string, error) {
	var resp balanceResponse
	if err := v.doJSON("GET", "/billing/balance", nil, &resp); err != nil {
		return "", err
	}
	var sb strings.Builder
	if resp.Balances.USD != nil {
		fmt.Fprintf(&sb, "USD: $%.2f\n", *resp.Balances.USD)
	}
	if resp.Balances.DIEM != nil {
		fmt.Fprintf(&sb, "DIEM: %.4f\n", *resp.Balances.DIEM)
	}
	sb.WriteString("canConsume: ")
	sb.WriteString(fmt.Sprintf("%v", resp.CanConsume))
	return strings.TrimSpace(sb.String()), nil
}

// --- Models ---

type modelInfo struct {
	ID        string `json:"id"`
	ModelSpec struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	} `json:"model_spec"`
}

// Models returns model IDs for a modality (text|image|video|music|embedding|asr).
func (v *veniceClient) Models(modality string) ([]modelInfo, error) {
	path := "/models"
	if modality != "" {
		path += "?type=" + modality
	}
	var resp struct {
		Data []modelInfo `json:"data"`
	}
	if err := v.doJSON("GET", path, nil, &resp); err != nil {
		return nil, err
	}
	return resp.Data, nil
}

// --- Async audio (music/SFX) ---

type audioQueueRequest struct {
	Model        string `json:"model"`
	Prompt       string `json:"prompt"`
	Lyrics       string `json:"lyrics_prompt,omitempty"`
	DurationSec  *int   `json:"duration_seconds,omitempty"`
	Instrumental bool   `json:"force_instrumental,omitempty"`
}

type audioQueueResponse struct {
	Model   string `json:"model"`
	QueueID string `json:"queue_id"`
	Status  string `json:"status"`
}

type audioQuoteRequest struct {
	Model       string `json:"model"`
	DurationSec int    `json:"duration_seconds"`
}

type audioQuoteResponse struct {
	Quote float64 `json:"quote"`
}

// QuoteAudio asks Venice for the estimated cost of a music generation.
func (v *veniceClient) QuoteAudio(model string, duration int) (float64, error) {
	var resp audioQuoteResponse
	if err := v.doJSON("POST", "/audio/quote", audioQuoteRequest{
		Model: model, DurationSec: duration,
	}, &resp); err != nil {
		return 0, err
	}
	return resp.Quote, nil
}

// QueueAudio starts an async music/audio generation job; returns the queue_id.
func (v *veniceClient) QueueAudio(model, prompt, lyrics string, instrumental bool, duration int) (string, error) {
	req := audioQueueRequest{Model: model, Prompt: prompt, Lyrics: lyrics, Instrumental: instrumental}
	if duration > 0 {
		req.DurationSec = &duration
	}
	var resp audioQueueResponse
	if err := v.doJSON("POST", "/audio/queue", req, &resp); err != nil {
		return "", err
	}
	if resp.QueueID == "" {
		return "", fmt.Errorf("no queue_id in audio/queue response")
	}
	return resp.QueueID, nil
}

type audioRetrieveRequest struct {
	Model                   string `json:"model"`
	QueueID                 string `json:"queue_id"`
	DeleteMediaOnCompletion bool   `json:"delete_media_on_completion,omitempty"`
}

// WaitAudio polls /audio/retrieve until the clip is ready (binary body), then
// calls /audio/complete to clean up. Returns audio bytes + extension.
func (v *veniceClient) WaitAudio(model, queueID string, pollSec, timeoutMin int) ([]byte, string, error) {
	deadline := time.Now().Add(time.Duration(timeoutMin) * time.Minute)
	for time.Now().Before(deadline) {
		req := audioRetrieveRequest{Model: model, QueueID: queueID, DeleteMediaOnCompletion: false}
		payload, _ := json.Marshal(req)
		httpReq, err := http.NewRequest("POST", v.BaseURL+"/audio/retrieve", bytes.NewReader(payload))
		if err != nil {
			return nil, "", err
		}
		httpReq.Header.Set("Authorization", v.authHeader())
		httpReq.Header.Set("Content-Type", "application/json")
		resp, err := v.HTTP.Do(httpReq)
		if err != nil {
			return nil, "", err
		}
		ct := resp.Header.Get("Content-Type")
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 60<<20))
		closeErr := resp.Body.Close()
		if readErr != nil {
			return nil, "", fmt.Errorf("reading audio retrieve response: %w", readErr)
		}
		if closeErr != nil {
			log.Printf("audio retrieve response close failed: %v", closeErr)
		}
		if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
			return nil, "", fmt.Errorf("Venice audio retrieve returned HTTP %d: %s", resp.StatusCode, truncate(string(body), 500))
		}

		// Still processing → JSON status body.
		if strings.Contains(ct, "application/json") {
			var st struct {
				Status string `json:"status"`
			}
			json.Unmarshal(body, &st)
			log.Printf("audio poll: status=%s queue=%s", st.Status, queueID)
			time.Sleep(time.Duration(pollSec) * time.Second)
			continue
		}
		if !strings.HasPrefix(strings.ToLower(ct), "audio/") {
			return nil, "", fmt.Errorf("unexpected audio retrieve content type %q: %s", ct, truncate(string(body), 500))
		}

		ext := "mp3"
		switch {
		case strings.Contains(ct, "wav"):
			ext = "wav"
		case strings.Contains(ct, "flac"):
			ext = "flac"
		case strings.Contains(ct, "ogg"):
			ext = "ogg"
		}
		// Clean up Venice's copy after receiving the audio.
		complete := audioRetrieveRequest{Model: model, QueueID: queueID}
		if err := v.doJSON("POST", "/audio/complete", complete, nil); err != nil {
			log.Printf("audio completion cleanup failed for queue %s: %v", queueID, err)
		}
		return body, ext, nil
	}
	return nil, "", fmt.Errorf("timed out waiting for audio generation")
}
