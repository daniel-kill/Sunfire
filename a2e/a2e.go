// a2e.go — minimal HTTP client for the A2E Developer API (https://video.a2e.ai).
// Covers the endpoint families the bot uses: credits, text-to-image,
// image-to-video, Wan 3.0 video, and generic task list/detail.
package a2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// a2eClient is a minimal HTTP client for the A2E API.
type a2eClient struct {
	BaseURL string
	APIKey  string
	HTTP    *http.Client
}

func newA2EClient(cfg *Config) *a2eClient {
	return &a2eClient{
		BaseURL: strings.TrimSuffix(cfg.A2EBaseURL, "/"),
		APIKey:  cfg.A2EAPIKey,
		HTTP:    &http.Client{Timeout: 120 * time.Second},
	}
}

func (c *a2eClient) authHeader() string { return "Bearer " + c.APIKey }

// doJSON sends a JSON request and decodes the response into out.
// The A2E API uses a wrapper envelope {code, data, ...}; most endpoints return
// HTTP 200 with code:0 on success. doJSON returns an error on HTTP errors and
// on non-zero A2E `code` fields.
func (c *a2eClient) doJSON(method, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.BaseURL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", c.authHeader())
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	bodyBytes, _ := io.ReadAll(resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("A2E API %s %s -> HTTP %d: %s", method, path, resp.StatusCode, truncate(string(bodyBytes), 400))
	}

	// Decode envelope to check code.
	var envelope struct {
		Code  int             `json:"code"`
		Data  json.RawMessage `json:"data"`
		Error string          `json:"error"`
	}
	if err := json.Unmarshal(bodyBytes, &envelope); err != nil {
		// Not an envelope — return raw body if out is a *string or *[]byte.
		if out != nil {
			if s, ok := out.(*string); ok {
				*s = string(bodyBytes)
				return nil
			}
			if b, ok := out.(*[]byte); ok {
				*b = bodyBytes
				return nil
			}
			return json.Unmarshal(bodyBytes, out)
		}
		return nil
	}
	if envelope.Code != 0 {
		return fmt.Errorf("A2E API %s %s -> code %d: %s", method, path, envelope.Code, envelope.Error)
	}
	if out != nil && len(envelope.Data) > 0 {
		return json.Unmarshal(envelope.Data, out)
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// --- Credits ---

// a2eCreditsRow is one credits-history transaction.
type a2eCreditsRow struct {
	ID          string `json:"_id"`
	Amount      int    `json:"amount"`
	CreatedAt   string `json:"createdAt"`
	Description string `json:"description"`
}

// GetCredits returns recent credit transactions (consumption history).
func (c *a2eClient) GetCredits(pageSize int) ([]a2eCreditsRow, error) {
	var data struct {
		Rows  []a2eCreditsRow `json:"rows"`
		Count int             `json:"count"`
	}
	err := c.doJSON("GET", fmt.Sprintf("/api/v1/transactionRecord/creditsHistory?pageNum=1&pageSize=%d&is_consumption=true", pageSize), nil, &data)
	return data.Rows, err
}

// creditHistoryPage fetches one page of creditsHistory for the given
// consumption flag and returns the rows and the server-reported total count.
func (c *a2eClient) creditHistoryPage(pageNum, pageSize int, consumption bool) ([]a2eCreditsRow, int, error) {
	var data struct {
		Rows  []a2eCreditsRow `json:"rows"`
		Count int             `json:"count"`
	}
	flag := "false"
	if consumption {
		flag = "true"
	}
	err := c.doJSON("GET", fmt.Sprintf("/api/v1/transactionRecord/creditsHistory?pageNum=%d&pageSize=%d&is_consumption=%s", pageNum, pageSize, flag), nil, &data)
	return data.Rows, data.Count, err
}

// CreditSummary holds the computed remaining-credit balance plus the raw
// income and consumption totals. There is no dedicated A2E balance endpoint,
// so the balance is derived by summing the full transaction history.
type CreditSummary struct {
	Balance  int // net credits remaining
	Income   int // total credits added (positive)
	Consumed int // total credits spent (negative sum)
	TxCount  int // total transactions
}

// GetCreditsBalance computes the remaining credit balance by fetching the
// full income + consumption history and summing the signed amounts.
func (c *a2eClient) GetCreditsBalance() (CreditSummary, error) {
	const pageSize = 100
	var summary CreditSummary

	// Consumption (negative amounts).
	page := 1
	for {
		rows, count, err := c.creditHistoryPage(page, pageSize, true)
		if err != nil {
			return summary, err
		}
		for _, r := range rows {
			summary.Consumed += r.Amount
			summary.TxCount++
		}
		if page*pageSize >= count || len(rows) == 0 {
			break
		}
		page++
	}

	// Income (positive amounts).
	page = 1
	for {
		rows, count, err := c.creditHistoryPage(page, pageSize, false)
		if err != nil {
			return summary, err
		}
		for _, r := range rows {
			summary.Income += r.Amount
			summary.TxCount++
		}
		if page*pageSize >= count || len(rows) == 0 {
			break
		}
		page++
	}

	summary.Balance = summary.Income + summary.Consumed
	return summary, nil
}

// --- Text to Image ---

// text2ImageStartReq is the payload for userText2Image/start.
type text2ImageStartReq struct {
	Prompt         string `json:"prompt"`
	ModelType      string `json:"model_type"`
	NegativePrompt string `json:"negative_prompt,omitempty"`
	Width          int    `json:"width,omitempty"`
	Height         int    `json:"height,omitempty"`
}

// StartText2Image kicks off a text-to-image task and returns the first task
// record from the response array (A2E returns data as an array of tasks).
func (c *a2eClient) StartText2Image(prompt, model string, width, height int, negative string) (map[string]any, error) {
	req := text2ImageStartReq{Prompt: prompt, ModelType: model, NegativePrompt: negative, Width: width, Height: height}
	var arr []map[string]any
	err := c.doJSON("POST", "/api/v1/userText2Image/start", req, &arr)
	if err != nil {
		return nil, err
	}
	if len(arr) > 0 {
		return arr[0], nil
	}
	return map[string]any{}, nil
}

// Text2ImageTask is a text-to-image task record.
type Text2ImageTask struct {
	ID            string `json:"_id"`
	Prompt        string `json:"prompt"`
	Status        string `json:"current_status"`
	ImageURL      string `json:"image_url"`
	ResultURL     string `json:"result_url"`
	FailedMessage string `json:"failed_message"`
}

// Text2ImageDetail fetches a single text-to-image task by _id.
func (c *a2eClient) Text2ImageDetail(id string) (map[string]any, error) {
	var out map[string]any
	err := c.doJSON("GET", "/api/v1/userText2Image/"+id, nil, &out)
	return out, err
}

// Text2ImageRecords lists text-to-image tasks.
func (c *a2eClient) Text2ImageRecords(pageSize int) ([]map[string]any, error) {
	var data struct {
		Rows []map[string]any `json:"rows"`
	}
	err := c.doJSON("GET", fmt.Sprintf("/api/v1/userText2Image/allRecords?pageNum=1&pageSize=%d", pageSize), nil, &data)
	return data.Rows, err
}

// --- Image to Video ---

type image2VideoStartReq struct {
	Prompt   string `json:"prompt"`
	Model    string `json:"model_type"`
	ImageURL string `json:"image_url,omitempty"`
	Ratio    string `json:"ratio,omitempty"`
}

// StartImage2Video kicks off an image-to-video task.
func (c *a2eClient) StartImage2Video(prompt, model, imageURL, ratio string) (map[string]any, error) {
	req := image2VideoStartReq{Prompt: prompt, Model: model, ImageURL: imageURL, Ratio: ratio}
	var out map[string]any
	err := c.doJSON("POST", "/api/v1/userImage2Video/start", req, &out)
	return out, err
}

// Image2VideoRecords lists image-to-video tasks.
func (c *a2eClient) Image2VideoRecords(pageSize int) ([]map[string]any, error) {
	var data struct {
		Rows []map[string]any `json:"rows"`
	}
	err := c.doJSON("GET", fmt.Sprintf("/api/v1/userImage2Video/allRecords?pageNum=1&pageSize=%d", pageSize), nil, &data)
	return data.Rows, err
}

// --- Wan 3.0 Video ---

type wan30Media struct {
	Type     string `json:"type"`
	URL      string `json:"url"`
	Duration int    `json:"duration,omitempty"`
}

type wan30Input struct {
	Prompt string       `json:"prompt"`
	Media  []wan30Media `json:"media,omitempty"`
}

type wan30Params struct {
	Resolution   string `json:"resolution,omitempty"`
	Ratio        string `json:"ratio,omitempty"`
	Duration     int    `json:"duration,omitempty"`
	Audio        bool   `json:"audio,omitempty"`
	PromptExtend bool   `json:"prompt_extend,omitempty"`
}

type wan30StartReq struct {
	Name       string      `json:"name"`
	Model      string      `json:"model"`
	Mode       string      `json:"mode"`
	Input      wan30Input  `json:"input"`
	Parameters wan30Params `json:"parameters"`
}

// StartWan30 kicks off a Wan 3.0 video task.
func (c *a2eClient) StartWan30(name, model, mode, prompt string, media []wan30Media, params wan30Params) (map[string]any, error) {
	req := wan30StartReq{
		Name:       name,
		Model:      model,
		Mode:       mode,
		Input:      wan30Input{Prompt: prompt, Media: media},
		Parameters: params,
	}
	var out map[string]any
	err := c.doJSON("POST", "/api/v1/userWan30/start", req, &out)
	return out, err
}

// Wan30Detail fetches a Wan 3.0 task by _id (start-only in spec, but detail is
// verified to work for polling).
func (c *a2eClient) Wan30Detail(id string) (map[string]any, error) {
	var out map[string]any
	err := c.doJSON("GET", "/api/v1/userWan30/"+id, nil, &out)
	return out, err
}

// --- Text to Speech (verified live 2026-09-24) ---

// UserVoice is a voice clone record (training or completed).
type UserVoice struct {
	ID            string   `json:"_id"`
	Name          string   `json:"name"`
	VoiceURLs     []string `json:"voice_urls"`
	Gender        string   `json:"gender"`
	Model         string   `json:"model"`
	Lang          string   `json:"lang"`
	Language      string   `json:"language"`
	CurrentStatus string   `json:"current_status"`
	SpeakerID     string   `json:"speaker_id"`
	Coins         float64  `json:"coins"`
	CreatedAt     string   `json:"createdAt"`
}

// TTS synthesizes speech and returns the audio URL. Provide exactly one of
// ttsID (system/built-in voice) or userVoiceID (cloned voice).
func (c *a2eClient) TTS(msg, ttsID, userVoiceID, country, region string) (string, error) {
	body := map[string]any{"msg": msg}
	if ttsID != "" {
		body["tts_id"] = ttsID
	}
	if userVoiceID != "" {
		body["user_voice_id"] = userVoiceID
	}
	if country != "" {
		body["country"] = country
	}
	if region != "" {
		body["region"] = region
	}
	var out string
	err := c.doJSON("POST", "/api/v1/video/send_tts", body, &out)
	return out, err
}

// CompletedVoices lists the user's completed cloned voices.
func (c *a2eClient) CompletedVoices() ([]UserVoice, error) {
	var data []UserVoice
	err := c.doJSON("GET", "/api/v1/userVoice/completedRecord", nil, &data)
	return data, err
}

// TrainingVoices lists the user's in-progress/failed voice training records.
func (c *a2eClient) TrainingVoices() ([]UserVoice, error) {
	var data []UserVoice
	err := c.doJSON("GET", "/api/v1/userVoice/trainingRecord", nil, &data)
	return data, err
}

// StartVoiceClone kicks off a voice-clone training task.
func (c *a2eClient) StartVoiceClone(name string, urls []string, gender, model string) (map[string]any, error) {
	body := map[string]any{
		"name":       name,
		"voice_urls": urls,
	}
	if gender != "" {
		body["gender"] = gender
	}
	if model != "" {
		body["model"] = model
	}
	var out map[string]any
	err := c.doJSON("POST", "/api/v1/userVoice/training", body, &out)
	return out, err
}

// BuiltinVoice is a preset TTS voice from anchor/voice_list.
type BuiltinVoice struct {
	Label   string `json:"label"`
	Value   string `json:"value"`
	TTSRate int    `json:"ttsRate"`
}

// VoiceGroup is a gender group in anchor/voice_list (female/male).
type VoiceGroup struct {
	Label    string         `json:"label"`
	Value    string         `json:"value"`
	Children []BuiltinVoice `json:"children"`
}

// BuiltinVoices lists preset TTS voices grouped by gender.
func (c *a2eClient) BuiltinVoices() ([]VoiceGroup, error) {
	var data []VoiceGroup
	err := c.doJSON("GET", "/api/v1/anchor/voice_list", nil, &data)
	return data, err
}
