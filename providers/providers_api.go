// providers_api.go — provider API implementations, ported from ElyProjectX's
// src/providers/*.js adapters (see /home/daniel/Repos/ElyProjectX). Each maps
// one provider's endpoints/models to the Discord commands.
package providers

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// ---- result types ----------------------------------------------------------

type chatResult struct {
	Message string
	Model   string
	Usage   *usage
	Raw     string // brief note for debugging
}

type usage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	CostUSD          float64
	HasCost          bool
}

type imageResult struct {
	Model     string
	B64Data   string
	MediaType string // image/png, image/jpeg, ...
}

type transcriptResult struct {
	Text  string
	Model string
}

type creditsResult struct {
	Summary string
	Notes   []string
}

// ---- providers -------------------------------------------------------------

func cohereClient() *apiClient {
	return newAPIClient("https://api.cohere.com", cfg.Cohere.APIKey)
}
func cloudflareClient() *apiClient {
	return newAPIClient("https://api.cloudflare.com/client/v4", cfg.Cloudflare.APIKey)
}
func groqClient() *apiClient {
	return newAPIClient("https://api.groq.com/openai/v1", cfg.Groq.APIKey)
}
func mistralClient() *apiClient {
	return newAPIClient("https://api.mistral.ai/v1", cfg.Mistral.APIKey)
}
func nvidiaClient() *apiClient {
	return newAPIClient("https://integrate.api.nvidia.com", cfg.Nvidia.APIKey)
}
func openrouterClient() *apiClient {
	return newAPIClient("https://openrouter.ai", cfg.OpenRouter.APIKey)
}

// ---- OpenAI-shape chat helper ----------------------------------------------

// openAIChat posts an OpenAI Chat Completions request to path and returns the
// reply text + model + usage.
func openAIChat(ctx context.Context, c *apiClient, path string, req openAIChatRequest) (*chatResult, error) {
	raw, err := c.doJSON(ctx, "POST", path, req)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			Text         string `json:"text"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		} `json:"usage"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := jsonUnmarshal(raw, &resp); err != nil {
		return nil, err
	}
	if resp.Error != nil && resp.Error.Message != "" {
		return nil, fmt.Errorf("%s", resp.Error.Message)
	}
	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("no choices in response")
	}
	content := resp.Choices[0].Message.Content
	if content == "" {
		content = resp.Choices[0].Text
	}
	if strings.TrimSpace(content) == "" {
		return nil, fmt.Errorf("empty reply (finish_reason=%s)", resp.Choices[0].FinishReason)
	}
	model := resp.Model
	if model == "" {
		model = req.Model
	}
	return &chatResult{
		Message: content,
		Model:   model,
		Usage: &usage{
			PromptTokens:     resp.Usage.PromptTokens,
			CompletionTokens: resp.Usage.CompletionTokens,
			TotalTokens:      resp.Usage.TotalTokens,
		},
	}, nil
}

type openAIChatRequest struct {
	Model    string                 `json:"model"`
	Messages []openAIMessage        `json:"messages"`
	Stream   bool                   `json:"stream"`
	Extra    map[string]interface{} `json:"-"`
}

type openAIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func (r openAIChatRequest) MarshalJSON() ([]byte, error) {
	type alias openAIChatRequest
	base, err := json.Marshal(alias(r))
	if err != nil {
		return nil, err
	}
	if len(r.Extra) == 0 {
		return base, nil
	}
	var m map[string]interface{}
	if err := json.Unmarshal(base, &m); err != nil {
		return nil, err
	}
	for k, v := range r.Extra {
		m[k] = v
	}
	return json.Marshal(m)
}

// chatMessages builds the messages payload from a single user prompt.
func chatMessages(prompt string) []openAIMessage {
	return []openAIMessage{{Role: "user", Content: prompt}}
}

// ---- Cohere ----------------------------------------------------------------

// Cohere v2 chat: POST /v2/chat, reply text lives in {message.content:[{type:"text",text}]}.
func cohereChat(ctx context.Context, prompt string, model string) (*chatResult, error) {
	if model == "" {
		model = "command-a-plus-05-2026"
	}
	c := cohereClient()
	body := map[string]interface{}{
		"model":    model,
		"messages": chatMessages(prompt),
	}
	raw, err := c.doJSON(ctx, "POST", "/v2/chat", body)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Model   string `json:"model"`
		Message *struct {
			Content interface{} `json:"content"`
		} `json:"message"`
		Usage *struct {
			Tokens *struct {
				InputTokens  int `json:"input_tokens"`
				OutputTokens int `json:"output_tokens"`
			} `json:"tokens"`
		} `json:"usage"`
	}
	if err := jsonUnmarshal(raw, &resp); err != nil {
		return nil, err
	}
	content := ""
	if resp.Message != nil {
		switch v := resp.Message.Content.(type) {
		case string:
			content = v
		case []interface{}:
			var parts []string
			for _, b := range v {
				if m, ok := b.(map[string]interface{}); ok {
					if t, ok := m["type"].(string); ok && t == "text" {
						if txt, ok := m["text"].(string); ok {
							parts = append(parts, txt)
						}
					}
				}
			}
			content = strings.Join(parts, "\n")
		}
	}
	if strings.TrimSpace(content) == "" {
		return nil, fmt.Errorf("empty reply from cohere")
	}
	u := &usage{}
	if resp.Usage != nil && resp.Usage.Tokens != nil {
		u.PromptTokens = resp.Usage.Tokens.InputTokens
		u.CompletionTokens = resp.Usage.Tokens.OutputTokens
		u.TotalTokens = resp.Usage.Tokens.InputTokens + resp.Usage.Tokens.OutputTokens
	}
	return &chatResult{Message: content, Model: resp.Model, Usage: u}, nil
}

// Cohere v2 STT: POST /v2/audio/transcriptions (multipart). Language REQUIRED.
func cohereTranscribe(ctx context.Context, file []byte, fileName, language, model string) (*transcriptResult, error) {
	if model == "" {
		model = "cohere-transcribe-03-2026"
	}
	if language == "" {
		return nil, fmt.Errorf("Cohere STT requires a language (e.g. en) — no auto-detection")
	}
	c := cohereClient()
	fields := map[string]string{"model": model, "language": language}
	raw, err := c.doMultipart(ctx, "/v2/audio/transcriptions", fields, "file", fileName, file)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Text  string `json:"text"`
		Model string `json:"model"`
	}
	if err := jsonUnmarshal(raw, &resp); err != nil {
		return nil, err
	}
	if strings.TrimSpace(resp.Text) == "" {
		return nil, fmt.Errorf("no transcript text in response")
	}
	return &transcriptResult{Text: resp.Text, Model: resp.Model}, nil
}

// ---- Cloudflare ------------------------------------------------------------

// Cloudflare OpenAI-compatible chat: POST /accounts/{id}/ai/v1/chat/completions.
// Reply may come back raw OpenAI JSON or wrapped in the v4 envelope {result}.
func cloudflareChat(ctx context.Context, prompt, model string) (*chatResult, error) {
	if model == "" {
		model = "@cf/meta/llama-3.3-70b-instruct-fp8-fast"
	}
	c := cloudflareClient()
	path := "/accounts/" + url.PathEscape(cfg.Cloudflare.AccountID) + "/ai/v1/chat/completions"
	body := openAIChatRequest{
		Model:    model,
		Messages: chatMessages(prompt),
		Stream:   false,
	}
	raw, err := c.doJSON(ctx, "POST", path, body)
	if err != nil {
		return nil, err
	}
	// Detect the v4 envelope: if it has "success", unwrap result.
	var envelope struct {
		Success bool            `json:"success"`
		Errors  []interface{}   `json:"errors"`
		Result  json.RawMessage `json:"result"`
	}
	var msg []byte
	if err := json.Unmarshal(raw, &envelope); err == nil && envelope.Success {
		msg = envelope.Result
	} else {
		msg = raw
	}
	var resp struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			Text string `json:"text"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(msg, &resp); err != nil {
		return nil, err
	}
	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("no choices in cloudflare response")
	}
	content := resp.Choices[0].Message.Content
	if content == "" {
		content = resp.Choices[0].Text
	}
	if strings.TrimSpace(content) == "" {
		return nil, fmt.Errorf("empty reply from cloudflare")
	}
	m := resp.Model
	if m == "" {
		m = model
	}
	return &chatResult{Message: content, Model: m, Usage: &usage{}}, nil
}

// Cloudflare text-to-image: POST /accounts/{id}/ai/run/{model}, response
// v4 envelope with result.image (base64).
func cloudflareImage(ctx context.Context, prompt, model string) (*imageResult, error) {
	if model == "" {
		model = "@cf/black-forest-labs/flux-1-schnell"
	}
	c := cloudflareClient()
	path := "/accounts/" + url.PathEscape(cfg.Cloudflare.AccountID) + "/ai/run/" + model
	body := map[string]interface{}{"prompt": prompt}
	raw, err := c.doJSON(ctx, "POST", path, body)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Success bool            `json:"success"`
		Result  json.RawMessage `json:"result"`
	}
	if err := jsonUnmarshal(raw, &resp); err != nil {
		return nil, err
	}
	var result struct {
		Image   string `json:"image"`
		B64Json string `json:"b64_json"`
	}
	var arr []struct {
		B64Json string `json:"b64_json"`
	}
	b64 := ""
	if err := json.Unmarshal(resp.Result, &result); err == nil {
		b64 = result.Image
		if b64 == "" {
			b64 = result.B64Json
		}
	}
	if b64 == "" {
		_ = json.Unmarshal(resp.Result, &arr)
		if len(arr) > 0 {
			b64 = arr[0].B64Json
		}
	}
	if b64 == "" {
		return nil, fmt.Errorf("no image data in cloudflare response")
	}
	return &imageResult{
		Model:     model,
		B64Data:   b64,
		MediaType: sniffImageType(b64),
	}, nil
}

// sniffImageType detects PNG/JPEG/WEBP from base64 magic bytes.
func sniffImageType(b64 string) string {
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(data) < 4 {
		return "image/png"
	}
	if data[0] == 0x89 && data[1] == 0x50 && data[2] == 0x4e && data[3] == 0x47 {
		return "image/png"
	}
	if data[0] == 0xff && data[1] == 0xd8 && data[2] == 0xff {
		return "image/jpeg"
	}
	if len(data) >= 12 && string(data[8:12]) == "WEBP" {
		return "image/webp"
	}
	return "image/png"
}

// ---- Groq ------------------------------------------------------------------

func groqChat(ctx context.Context, prompt, model string) (*chatResult, error) {
	if model == "" {
		model = "openai/gpt-oss-20b"
	}
	return openAIChat(ctx, groqClient(), "/chat/completions", openAIChatRequest{
		Model: model, Messages: chatMessages(prompt), Stream: false,
	})
}

// Groq STT: POST /audio/transcriptions (multipart).
func groqTranscribe(ctx context.Context, file []byte, fileName, language, model string) (*transcriptResult, error) {
	if model == "" {
		model = "whisper-large-v3-turbo"
	}
	c := groqClient()
	fields := map[string]string{"model": model}
	if language != "" {
		fields["language"] = language
	}
	raw, err := c.doMultipart(ctx, "/audio/transcriptions", fields, "file", fileName, file)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Text  string `json:"text"`
		Model string `json:"model"`
	}
	if err := jsonUnmarshal(raw, &resp); err != nil {
		return nil, err
	}
	if strings.TrimSpace(resp.Text) == "" {
		return nil, fmt.Errorf("no transcript text in response")
	}
	return &transcriptResult{Text: resp.Text, Model: resp.Model}, nil
}

// Groq TTS: POST /audio/speech (JSON body → raw audio bytestream).
func groqSpeak(ctx context.Context, text, voice, model, format string) ([]byte, string, error) {
	if model == "" {
		model = "canopylabs/orpheus-v1-english"
	}
	if voice == "" {
		voice = "autumn"
	}
	if format == "" {
		format = "wav"
	}
	body := map[string]interface{}{
		"input":           text,
		"model":           model,
		"voice":           voice,
		"response_format": format,
	}
	return groqClient().doRaw(ctx, "POST", "/audio/speech", body)
}

// ---- Mistral ---------------------------------------------------------------

func mistralChat(ctx context.Context, prompt, model string) (*chatResult, error) {
	if model == "" {
		model = "mistral-medium-latest"
	}
	return openAIChat(ctx, mistralClient(), "/chat/completions", openAIChatRequest{
		Model: model, Messages: chatMessages(prompt), Stream: false,
	})
}

// ---- NVIDIA ----------------------------------------------------------------

func nvidiaChat(ctx context.Context, prompt, model string) (*chatResult, error) {
	if model == "" {
		model = "nvidia/nemotron-3-super-120b-a12b"
	}
	return openAIChat(ctx, nvidiaClient(), "/v1/chat/completions", openAIChatRequest{
		Model: model, Messages: chatMessages(prompt), Stream: false,
	})
}

// ---- OpenRouter ------------------------------------------------------------

func openrouterChat(ctx context.Context, prompt, model string) (*chatResult, error) {
	if model == "" {
		model = "nvidia/nemotron-3-super-120b-a12b:free"
	}
	return openAIChat(ctx, openrouterClient(), "/api/v1/chat/completions", openAIChatRequest{
		Model: model, Messages: chatMessages(prompt), Stream: false,
	})
}

// OpenRouter image: POST /api/v1/images → {created, data:[{b64_json, media_type}]}.
func openrouterImage(ctx context.Context, prompt, model string) (*imageResult, error) {
	if model == "" {
		model = "bytedance-seed/seedream-4.5"
	}
	c := openrouterClient()
	body := map[string]interface{}{"model": model, "prompt": prompt}
	raw, err := c.doJSON(ctx, "POST", "/api/v1/images", body)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Model string `json:"model"`
		Data  []struct {
			B64Json   string `json:"b64_json"`
			MediaType string `json:"media_type"`
		} `json:"data"`
	}
	if err := jsonUnmarshal(raw, &resp); err != nil {
		return nil, err
	}
	if len(resp.Data) == 0 || resp.Data[0].B64Json == "" {
		return nil, fmt.Errorf("no image data in openrouter response")
	}
	mt := resp.Data[0].MediaType
	if mt == "" {
		mt = "image/png"
	}
	m := resp.Model
	if m == "" {
		m = model
	}
	return &imageResult{Model: m, B64Data: resp.Data[0].B64Json, MediaType: mt}, nil
}

// OpenRouter STT: POST /api/v1/audio/transcriptions (multipart).
func openrouterTranscribe(ctx context.Context, file []byte, fileName, language, model string) (*transcriptResult, error) {
	if model == "" {
		model = "openai/whisper-large-v3"
	}
	c := openrouterClient()
	fields := map[string]string{"model": model}
	if language != "" {
		fields["language"] = language
	}
	raw, err := c.doMultipart(ctx, "/api/v1/audio/transcriptions", fields, "file", fileName, file)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Text  string `json:"text"`
		Model string `json:"model"`
	}
	if err := jsonUnmarshal(raw, &resp); err != nil {
		return nil, err
	}
	if strings.TrimSpace(resp.Text) == "" {
		return nil, fmt.Errorf("no transcript text in response")
	}
	return &transcriptResult{Text: resp.Text, Model: resp.Model}, nil
}

// OpenRouter credits: GET /api/v1/key (per-key quota) then /api/v1/credits fallback.
func openrouterCredits(ctx context.Context) (*creditsResult, error) {
	c := openrouterClient()
	raw, err := c.doJSON(ctx, "GET", "/api/v1/key", nil)
	if err == nil {
		var resp struct {
			Data *struct {
				Label          string   `json:"label"`
				IsFreeTier     bool     `json:"is_free_tier"`
				Limit          *float64 `json:"limit"`
				LimitRemaining *float64 `json:"limit_remaining"`
				Usage          *float64 `json:"usage"`
				UsageDaily     *float64 `json:"usage_daily"`
				UsageWeekly    *float64 `json:"usage_weekly"`
				UsageMonthly   *float64 `json:"usage_monthly"`
			} `json:"data"`
		}
		if err := jsonUnmarshal(raw, &resp); err != nil {
			return nil, err
		}
		d := resp.Data
		if d == nil {
			return nil, fmt.Errorf("openrouter /key returned no data")
		}
		var b strings.Builder
		label := d.Label
		if len(label) > 40 || strings.HasPrefix(label, "sk-or-v1-") {
			label = "(unlabeled)"
		}
		fmt.Fprintf(&b, "**OpenRouter key**: label `%s` · free tier `%v`\n", label, d.IsFreeTier)
		if d.Limit != nil {
			fmt.Fprintf(&b, "Per-key cap: **$%.2f** · remaining **$%.2f**\n", *d.Limit, derefF(d.LimitRemaining))
		} else {
			b.WriteString("Per-key cap: none (unlimited)\n")
		}
		fmt.Fprintf(&b, "Usage: total $%.2f · daily $%.2f · weekly $%.2f · monthly $%.2f",
			derefF(d.Usage), derefF(d.UsageDaily), derefF(d.UsageWeekly), derefF(d.UsageMonthly))
		return &creditsResult{Summary: b.String()}, nil
	}

	// Fallback: management-key credits view.
	raw, err2 := c.doJSON(ctx, "GET", "/api/v1/credits", nil)
	if err2 != nil {
		return nil, fmt.Errorf("GET /api/v1/key failed (%v) and /api/v1/credits also failed (%v)", err, err2)
	}
	var cr struct {
		Credits      float64 `json:"credits"`
		TotalCredits float64 `json:"total_credits"`
		TotalUsage   float64 `json:"total_usage"`
	}
	if err := jsonUnmarshal(raw, &cr); err != nil {
		return nil, err
	}
	return &creditsResult{
		Summary: fmt.Sprintf("**OpenRouter credits**: $%.2f available · purchased $%.2f · used $%.2f",
			cr.Credits, cr.TotalCredits, cr.TotalUsage),
	}, nil
}

func derefF(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}

// bytesBuffer is a small helper (used by TTS callers to feed discordgo).
func bytesBuffer(b []byte) *bytes.Buffer {
	return bytes.NewBuffer(b)
}
