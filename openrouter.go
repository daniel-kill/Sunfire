// openrouter.go — Tier 1 /openrouter subcommands for Sunfire.
//
// Subcommands: models, deals, model, compare, endpoints, providers,
// price, key, gen, usage, ask, help.
//
// Auth: reads cfg.OpenRouter.APIKey from .private/config.json.
//   - Catalog reads (models, deals, model, compare, endpoints, providers,
//     price) work with or without a key.
//   - ask / key / gen / usage require a key.
//
// Docs:   https://openrouter.ai/docs
// Catalog: https://openrouter.ai/models
package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
)

const (
	orBaseURL        = "https://openrouter.ai/api/v1"
	orAppURL         = "https://github.com/daniel-kill/Sunfire"
	orAppTitle       = "Sunfire"
	orHTTPTimeout    = 45 * time.Second
	orMaxModelsShown = 20
	orDefaultAsk     = "openrouter/auto"
	orEmbedColor     = 0x6467F2
)

// ---------- flexible JSON number (OpenRouter returns prices as strings) ----------

type flexFloat float64

func (f *flexFloat) UnmarshalJSON(b []byte) error {
	s := strings.Trim(strings.TrimSpace(string(b)), `"`)
	if s == "" || s == "null" {
		*f = 0
		return nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		*f = 0
		return nil
	}
	*f = flexFloat(v)
	return nil
}

// ---------- API types ----------

type orPricing struct {
	Prompt     flexFloat `json:"prompt"`
	Completion flexFloat `json:"completion"`
	Request    flexFloat `json:"request"`
	Image      flexFloat `json:"image"`
	Discount   flexFloat `json:"discount"`
}

type orArchitecture struct {
	Modality         string   `json:"modality"`
	InputModalities  []string `json:"input_modalities"`
	OutputModalities []string `json:"output_modalities"`
	Tokenizer        string   `json:"tokenizer"`
}

type orTopProvider struct {
	ContextLength       int  `json:"context_length"`
	MaxCompletionTokens int  `json:"max_completion_tokens"`
	IsModerated         bool `json:"is_moderated"`
}

type orModel struct {
	ID                  string         `json:"id"`
	CanonicalSlug       string         `json:"canonical_slug"`
	Name                string         `json:"name"`
	ContextLength       int            `json:"context_length"`
	Pricing             orPricing      `json:"pricing"`
	Architecture        orArchitecture `json:"architecture"`
	SupportedParameters []string       `json:"supported_parameters"`
	Description         string         `json:"description"`
	TopProvider         orTopProvider  `json:"top_provider"`
}

type orModelsResponse struct {
	Data       []orModel `json:"data"`
	TotalCount int       `json:"total_count"`
}

type orEndpoint struct {
	Name                    string    `json:"name"`
	ContextLength           int       `json:"context_length"`
	Pricing                 orPricing `json:"pricing"`
	ProviderName            string    `json:"provider_name"`
	Quantization            string    `json:"quantization"`
	MaxCompletionTokens     int       `json:"max_completion_tokens"`
	Status                  int       `json:"status"`
	UptimeLast30m           flexFloat `json:"uptime_last_30m"`
	SupportsImplicitCaching bool      `json:"supports_implicit_caching"`
	Latency                 flexFloat `json:"latency"`
}

type orProvider struct {
	Name             string `json:"name"`
	Slug             string `json:"slug"`
	PrivacyPolicyURL string `json:"privacy_policy_url"`
	StatusPageURL    string `json:"status_page_url"`
}

type orKeyInfo struct {
	Label                  string   `json:"label"`
	Limit                  *float64 `json:"limit"`
	LimitRemaining         *float64 `json:"limit_remaining"`
	LimitReset             *string  `json:"limit_reset"`
	Usage                  float64  `json:"usage"`
	UsageDaily             float64  `json:"usage_daily"`
	UsageWeekly            float64  `json:"usage_weekly"`
	UsageMonthly           float64  `json:"usage_monthly"`
	ByokUsage              float64  `json:"byok_usage"`
	ByokUsageDaily         float64  `json:"byok_usage_daily"`
	ByokUsageWeekly        float64  `json:"byok_usage_weekly"`
	ByokUsageMonthly       float64  `json:"byok_usage_monthly"`
	IsFreeTier             bool     `json:"is_free_tier"`
	IsManagementKey        bool     `json:"is_management_key"`
	FreeModelDailyRequests struct {
		Used      int `json:"used"`
		Limit     int `json:"limit"`
		Remaining int `json:"remaining"`
	} `json:"free_model_daily_requests"`
}

type orGeneration struct {
	ID                    string    `json:"id"`
	Model                 string    `json:"model"`
	ProviderName          string    `json:"provider_name"`
	Router                string    `json:"router"`
	TokensPrompt          int       `json:"tokens_prompt"`
	TokensCompletion      int       `json:"tokens_completion"`
	NativeTokensReasoning int       `json:"native_tokens_reasoning"`
	TotalCost             flexFloat `json:"total_cost"`
	UpstreamInferenceCost flexFloat `json:"upstream_inference_cost"`
	CacheDiscount         flexFloat `json:"cache_discount"`
	Latency               flexFloat `json:"latency"`
	GenerationTime        flexFloat `json:"generation_time"`
	FinishReason          string    `json:"finish_reason"`
	NativeFinishReason    string    `json:"native_finish_reason"`
	CreatedAt             string    `json:"created_at"`
	Streamed              bool      `json:"streamed"`
	Cancelled             bool      `json:"cancelled"`
	IsByok                bool      `json:"is_byok"`
	NumSearchResults      int       `json:"num_search_results"`
}

// ---------- HTTP plumbing ----------

func orAPIKey() string {
	cfg, err := loadConfig()
	if err != nil || cfg == nil {
		return ""
	}
	return cfg.OpenRouter.APIKey
}

func orMgmtAPIKey() string {
	cfg, err := loadConfig()
	if err != nil || cfg == nil {
		return ""
	}
	return cfg.OpenRouter.APIManagementKey
}

func orRequest(method, path string, query url.Values, body []byte) (*http.Request, error) {
	u := orBaseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, u, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("HTTP-Referer", orAppURL)
	req.Header.Set("X-OpenRouter-Title", orAppTitle)
	if k := orAPIKey(); k != "" {
		req.Header.Set("Authorization", "Bearer "+k)
	}
	return req, nil
}

func orRequestMgmt(method, path string, query url.Values, body []byte) (*http.Request, error) {
	u := orBaseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, u, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("HTTP-Referer", orAppURL)
	req.Header.Set("X-OpenRouter-Title", orAppTitle)
	if k := orMgmtAPIKey(); k != "" {
		req.Header.Set("Authorization", "Bearer "+k)
	}
	return req, nil
}

func orDo(req *http.Request) ([]byte, int, error) {
	client := &http.Client{Timeout: orHTTPTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return b, resp.StatusCode, nil
}

func orAPIError(body []byte, status int) string {
	var e struct {
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &e) == nil && e.Error.Message != "" {
		return fmt.Sprintf("OpenRouter: %s", e.Error.Message)
	}
	return fmt.Sprintf("OpenRouter returned HTTP %d", status)
}

// ---------- Discord reply helpers ----------

func orReply(s *discordgo.Session, i *discordgo.InteractionCreate, content string) {
	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Content: clipMessage(content)},
	})
}

func orDefer(s *discordgo.Session, i *discordgo.InteractionCreate) {
	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	})
}

func orFollowUp(s *discordgo.Session, i *discordgo.InteractionCreate, content string) {
	_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
		Content: clipMessage(content),
	})
}

func orFollowUpEmbed(s *discordgo.Session, i *discordgo.InteractionCreate, embeds ...*discordgo.MessageEmbed) {
	_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{Embeds: embeds})
}

// ---------- option helpers ----------

func orOptStr(sub *discordgo.ApplicationCommandInteractionDataOption, name string) string {
	for _, o := range sub.Options {
		if o.Name == name && o.Type == discordgo.ApplicationCommandOptionString {
			return o.StringValue()
		}
	}
	return ""
}

func orOptInt(sub *discordgo.ApplicationCommandInteractionDataOption, name string) (int64, bool) {
	for _, o := range sub.Options {
		if o.Name == name && o.Type == discordgo.ApplicationCommandOptionInteger {
			return o.IntValue(), true
		}
	}
	return 0, false
}

func orOptNum(sub *discordgo.ApplicationCommandInteractionDataOption, name string) (float64, bool) {
	for _, o := range sub.Options {
		if o.Name == name && o.Type == discordgo.ApplicationCommandOptionNumber {
			return o.FloatValue(), true
		}
	}
	return 0, false
}

// ---------- formatting ----------

func orFmtCtx(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.2fM", float64(n)/1_000_000)
	case n >= 1000:
		return fmt.Sprintf("%dK", n/1000)
	default:
		return strconv.Itoa(n)
	}
}

func orPerM(v flexFloat) string {
	p := float64(v) * 1_000_000
	if p <= 0 {
		return "free"
	}
	if p < 0.01 {
		return fmt.Sprintf("$%.4f", p)
	}
	return fmt.Sprintf("$%.2f", p)
}

func orDiscount(v flexFloat) string {
	d := float64(v)
	if d <= 0 {
		return ""
	}
	if d <= 1 {
		d *= 100
	}
	return fmt.Sprintf("%.0f%% off", d)
}

func orPricePair(p orPricing) string {
	return fmt.Sprintf("%s / %s per M", orPerM(p.Prompt), orPerM(p.Completion))
}

func orModelLine(m orModel) string {
	line := fmt.Sprintf("`%s` — %s · %s ctx · %s",
		m.ID, m.Name, orFmtCtx(m.ContextLength), orPricePair(m.Pricing))
	if d := orDiscount(m.Pricing.Discount); d != "" {
		line += " · " + d
	}
	return line
}

// ---------- data fetchers ----------

func orFetchModels(query url.Values) ([]orModel, int, error) {
	req, err := orRequest(http.MethodGet, "/models", query, nil)
	if err != nil {
		return nil, 0, err
	}
	body, status, err := orDo(req)
	if err != nil {
		return nil, 0, err
	}
	if status != http.StatusOK {
		return nil, 0, fmt.Errorf("%s", orAPIError(body, status))
	}
	var resp orModelsResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, 0, err
	}
	return resp.Data, resp.TotalCount, nil
}

type orVideoModel struct {
	ID                    string   `json:"id"`
	Name                  string   `json:"name"`
	SupportedDurations    []int    `json:"supported_durations"`
	SupportedResolutions  []string `json:"supported_resolutions"`
	SupportedAspectRatios []string `json:"supported_aspect_ratios"`
}

func orFetchVideoModels() ([]orVideoModel, error) {
	req, err := orRequest(http.MethodGet, "/videos/models", nil, nil)
	if err != nil {
		return nil, err
	}
	body, status, err := orDo(req)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("%s", orAPIError(body, status))
	}
	var resp struct {
		Data []orVideoModel `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("parsing video model catalog: %w", err)
	}
	return resp.Data, nil
}

func orFetchModel(slug string) (*orModel, error) {
	slug = strings.TrimPrefix(strings.TrimSpace(slug), "/")
	if slug == "" {
		return nil, fmt.Errorf("no model id given")
	}
	req, err := orRequest(http.MethodGet, "/models/"+slug, nil, nil)
	if err != nil {
		return nil, err
	}
	body, status, err := orDo(req)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("%s", orAPIError(body, status))
	}
	var resp struct {
		Data orModel `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	if resp.Data.ID == "" {
		return nil, fmt.Errorf("model `%s` not found", slug)
	}
	return &resp.Data, nil
}

func orFetchKey() (*orKeyInfo, error) {
	req, err := orRequest(http.MethodGet, "/key", nil, nil)
	if err != nil {
		return nil, err
	}
	body, status, err := orDo(req)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("%s", orAPIError(body, status))
	}
	var resp struct {
		Data orKeyInfo `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	return &resp.Data, nil
}

// ---------- embedding types ----------

type orEmbeddingData struct {
	Object    string    `json:"object"`
	Embedding []float64 `json:"embedding"`
	Index     int       `json:"index"`
}

type orEmbeddingResponse struct {
	Data  []orEmbeddingData `json:"data"`
	Model string            `json:"model"`
	Usage struct {
		PromptTokens int `json:"prompt_tokens"`
		TotalTokens  int `json:"total_tokens"`
	} `json:"usage"`
}

// ---------- rerank types ----------

type orRerankResult struct {
	Document struct {
		Text string `json:"text"`
	} `json:"document"`
	Index          int     `json:"index"`
	RelevanceScore float64 `json:"relevance_score"`
}

type orRerankResponse struct {
	ID      string           `json:"id"`
	Model   string           `json:"model"`
	Results []orRerankResult `json:"results"`
	Usage   struct {
		SearchUnits int `json:"search_units"`
		TotalTokens int `json:"total_tokens"`
	} `json:"usage"`
}

// ---------- TTS / STT types ----------

type orTTSResponse struct {
	AudioData string `json:"audio_data"`
	MediaType string `json:"media_type"`
	Usage     struct {
		Cost       int `json:"cost"`
		InputChars int `json:"input_chars"`
		Seconds    int `json:"seconds"`
	} `json:"usage"`
}

type orSTTResponse struct {
	Text  string `json:"text"`
	Usage struct {
		Cost        float64 `json:"cost"`
		InputTokens int     `json:"input_tokens"`
		Seconds     float64 `json:"seconds"`
	} `json:"usage"`
}

// ---------- embedding / rerank / TTS / STT fetchers ----------

func orFetchEmbeddings(model string, input interface{}, dimensions int) (*orEmbeddingResponse, error) {
	payload := map[string]any{
		"model": model,
		"input": input,
	}
	if dimensions > 0 {
		payload["dimensions"] = dimensions
	}
	bodyBytes, _ := json.Marshal(payload)
	req, err := orRequest(http.MethodPost, "/embeddings", nil, bodyBytes)
	if err != nil {
		return nil, err
	}
	body, status, err := orDo(req)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("%s", orAPIError(body, status))
	}
	var resp orEmbeddingResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func orFetchRerank(model, query string, documents []string, topN int) (*orRerankResponse, error) {
	payload := map[string]any{
		"model":     model,
		"query":     query,
		"documents": documents,
		"top_n":     topN,
	}
	bodyBytes, _ := json.Marshal(payload)
	req, err := orRequest(http.MethodPost, "/rerank", nil, bodyBytes)
	if err != nil {
		return nil, err
	}
	body, status, err := orDo(req)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("%s", orAPIError(body, status))
	}
	var resp orRerankResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

type orRawAudioResponse struct {
	Data        []byte
	ContentType string
}

func orFetchTTS(model, input, voice, format string, speed float64) (*orRawAudioResponse, error) {
	if format == "" {
		format = "mp3"
	}
	payload := map[string]any{
		"model":           model,
		"input":           input,
		"response_format": format,
	}
	if voice != "" {
		payload["voice"] = voice
	}
	if speed > 0 {
		payload["speed"] = speed
	}
	bodyBytes, _ := json.Marshal(payload)
	req, err := orRequest(http.MethodPost, "/audio/speech", nil, bodyBytes)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: orHTTPTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s", orAPIError(body, resp.StatusCode))
	}

	ct := resp.Header.Get("Content-Type")
	return &orRawAudioResponse{
		Data:        body,
		ContentType: ct,
	}, nil
}

func orFetchSTT(model string, audioData string, audioFormat string, language string) (*orSTTResponse, error) {
	if audioFormat == "" {
		audioFormat = "mp3"
	}
	payload := map[string]any{
		"model": model,
		"input_audio": map[string]string{
			"data":   audioData,
			"format": audioFormat,
		},
	}
	if language != "" {
		payload["language"] = language
	}
	bodyBytes, _ := json.Marshal(payload)
	req, err := orRequest(http.MethodPost, "/audio/transcriptions", nil, bodyBytes)
	if err != nil {
		return nil, err
	}
	body, status, err := orDo(req)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("%s", orAPIError(body, status))
	}
	var resp orSTTResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// ---------- benchmark types ----------

type orBenchmarkEntry struct {
	Source            string    `json:"source"`
	ModelPermaslug    string    `json:"model_permaslug"`
	DisplayName       string    `json:"display_name"`
	IntelligenceIndex *float64  `json:"intelligence_index"`
	CodingIndex       *float64  `json:"coding_index"`
	AgenticIndex      *float64  `json:"agentic_index"`
	Pricing           orPricing `json:"pricing"`
}

type orBenchmarksResponse struct {
	Data []orBenchmarkEntry `json:"data"`
}

func orFetchBenchmarks() ([]orBenchmarkEntry, error) {
	req, err := orRequest(http.MethodGet, "/benchmarks", nil, nil)
	if err != nil {
		return nil, err
	}
	body, status, err := orDo(req)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("%s", orAPIError(body, status))
	}
	var resp orBenchmarksResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	return resp.Data, nil
}

// ---------- Tier 3 types (management key endpoints) ----------

type orCreditsInfo struct {
	TotalCredits float64 `json:"total_credits"`
	TotalUsage   float64 `json:"total_usage"`
}

type orAPIKeyItem struct {
	Label          string  `json:"label"`
	Name           string  `json:"name"`
	Hash           string  `json:"hash"`
	Disabled       bool    `json:"disabled"`
	Limit          float64 `json:"limit"`
	LimitRemaining float64 `json:"limit_remaining"`
	LimitReset     string  `json:"limit_reset"`
	Usage          float64 `json:"usage"`
	UsageDaily     float64 `json:"usage_daily"`
	UsageMonthly   float64 `json:"usage_monthly"`
	UsageWeekly    float64 `json:"usage_weekly"`
	ByokUsage      float64 `json:"byok_usage"`
	CreatedAt      string  `json:"created_at"`
	ExpiresAt      string  `json:"expires_at,omitempty"`
	WorkspaceID    string  `json:"workspace_id"`
}

type orGuardrailItem struct {
	ID                   string   `json:"id"`
	Name                 string   `json:"name"`
	Description          string   `json:"description"`
	LimitUsd             float64  `json:"limit_usd"`
	ResetInterval        string   `json:"reset_interval"`
	AllowedProviders     []string `json:"allowed_providers"`
	AllowedModels        []string `json:"allowed_models"`
	EnforceZdr           bool     `json:"enforce_zdr"`
	IncludeByokInBudgets bool     `json:"include_byok_in_budgets"`
	WorkspaceID          string   `json:"workspace_id"`
	CreatedAt            string   `json:"created_at"`
	UpdatedAt            string   `json:"updated_at"`
}

type orBatchItem struct {
	ID            string `json:"id"`
	Object        string `json:"object"`
	Endpoint      string `json:"endpoint"`
	Model         string `json:"model"`
	Status        string `json:"status"`
	CreatedAt     int64  `json:"created_at"`
	RequestCounts struct {
		Total     int `json:"total"`
		Completed int `json:"completed"`
		Failed    int `json:"failed"`
	} `json:"request_counts"`
}

// ---------- Tier 3 fetchers (management key) ----------

func orFetchCredits() (*orCreditsInfo, error) {
	req, err := orRequestMgmt(http.MethodGet, "/credits", nil, nil)
	if err != nil {
		return nil, err
	}
	body, status, err := orDo(req)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("%s", orAPIError(body, status))
	}
	var wrap struct {
		Data orCreditsInfo `json:"data"`
	}
	if json.Unmarshal(body, &wrap) != nil {
		return nil, fmt.Errorf("could not parse credits response")
	}
	return &wrap.Data, nil
}

func orFetchKeys() ([]orAPIKeyItem, error) {
	req, err := orRequestMgmt(http.MethodGet, "/keys", nil, nil)
	if err != nil {
		return nil, err
	}
	body, status, err := orDo(req)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("%s", orAPIError(body, status))
	}
	var wrap struct {
		Data []orAPIKeyItem `json:"data"`
	}
	if json.Unmarshal(body, &wrap) != nil {
		return nil, fmt.Errorf("could not parse keys response")
	}
	return wrap.Data, nil
}

func orFetchGuardrails() ([]orGuardrailItem, error) {
	req, err := orRequestMgmt(http.MethodGet, "/guardrails?limit=50", nil, nil)
	if err != nil {
		return nil, err
	}
	body, status, err := orDo(req)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("%s", orAPIError(body, status))
	}
	var wrap struct {
		Data []orGuardrailItem `json:"data"`
	}
	if json.Unmarshal(body, &wrap) != nil {
		return nil, fmt.Errorf("could not parse guardrails response")
	}
	return wrap.Data, nil
}

type orAnalyticsQuery struct {
	Dimensions  []string `json:"dimensions"`
	Metrics     []string `json:"metrics"`
	Granularity string   `json:"granularity"`
	TimeRange   struct {
		Start string `json:"start"`
		End   string `json:"end"`
	} `json:"time_range"`
	Limit int `json:"limit"`
}

func newORAnalyticsQuery(now time.Time, days int) orAnalyticsQuery {
	end := now.UTC()
	start := end.AddDate(0, 0, -days)
	query := orAnalyticsQuery{
		Dimensions:  []string{"model"},
		Metrics:     []string{"request_count"},
		Granularity: "day",
		Limit:       100,
	}
	query.TimeRange.Start = start.Format(time.RFC3339)
	query.TimeRange.End = end.Format(time.RFC3339)
	return query
}

func orFetchAnalytics(query orAnalyticsQuery) (interface{}, error) {
	bodyBytes, err := json.Marshal(query)
	if err != nil {
		return nil, fmt.Errorf("encoding analytics query: %w", err)
	}
	req, err := orRequestMgmt(http.MethodPost, "/analytics/query", nil, bodyBytes)
	if err != nil {
		return nil, err
	}
	body, status, err := orDo(req)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("%s", orAPIError(body, status))
	}
	var wrap struct {
		Data interface{} `json:"data"`
	}
	if json.Unmarshal(body, &wrap) != nil {
		return nil, fmt.Errorf("could not parse analytics response")
	}
	return wrap.Data, nil
}

func orFetchBatches(statusFilter string) ([]orBatchItem, error) {
	u := "/batches"
	if statusFilter != "" {
		u += "?status=" + statusFilter
	}
	req, err := orRequestMgmt(http.MethodGet, u, nil, nil)
	if err != nil {
		return nil, err
	}
	body, status, err := orDo(req)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("%s", orAPIError(body, status))
	}
	var wrap struct {
		Data []orBatchItem `json:"data"`
	}
	if json.Unmarshal(body, &wrap) != nil {
		return nil, fmt.Errorf("could not parse batches response")
	}
	return wrap.Data, nil
}

// ---------- subcommand handlers ----------

func orHandleModels(s *discordgo.Session, i *discordgo.InteractionCreate, sub *discordgo.ApplicationCommandInteractionDataOption) {
	orDefer(s, i)

	q := url.Values{}
	if v := strings.TrimSpace(orOptStr(sub, "query")); v != "" {
		q.Set("q", v)
	}
	if v := orOptStr(sub, "sort"); v != "" {
		q.Set("sort", v)
	}
	if v := orOptStr(sub, "modality"); v != "" {
		q.Set("output_modalities", v)
	}
	if v, ok := orOptNum(sub, "max_price"); ok {
		q.Set("max_price", strconv.FormatFloat(v, 'f', -1, 64))
	}
	limit := 15
	if v, ok := orOptInt(sub, "limit"); ok && v > 0 {
		limit = int(v)
	}
	if limit > orMaxModelsShown {
		limit = orMaxModelsShown
	}
	q.Set("limit", strconv.Itoa(limit))

	models, total, err := orFetchModels(q)
	if err != nil {
		orFollowUp(s, i, "❌ "+err.Error())
		return
	}
	if len(models) == 0 {
		orFollowUp(s, i, "No models matched that filter.")
		return
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("**OpenRouter models** — showing %d of %d\n\n", len(models), total))
	for _, m := range models {
		b.WriteString("• " + orModelLine(m) + "\n")
	}
	orFollowUp(s, i, b.String())
}

func orHandleDeals(s *discordgo.Session, i *discordgo.InteractionCreate, sub *discordgo.ApplicationCommandInteractionDataOption) {
	orDefer(s, i)

	limit := 15
	if v, ok := orOptInt(sub, "limit"); ok && v > 0 {
		limit = int(v)
	}
	if limit > orMaxModelsShown {
		limit = orMaxModelsShown
	}

	models, _, err := orFetchModels(url.Values{"limit": {"1000"}})
	if err != nil {
		orFollowUp(s, i, "❌ "+err.Error())
		return
	}

	var deals []orModel
	for _, m := range models {
		if float64(m.Pricing.Discount) > 0 {
			deals = append(deals, m)
		}
	}

	header := "**OpenRouter — discounted models**\n\n"
	if len(deals) == 0 {
		// No discount metadata surfaced; fall back to the cheapest text models.
		sort.Slice(models, func(a, b int) bool {
			return float64(models[a].Pricing.Prompt) < float64(models[b].Pricing.Prompt)
		})
		if len(models) > limit {
			models = models[:limit]
		}
		var b strings.Builder
		b.WriteString("No active discounts are published right now. Cheapest models instead:\n\n")
		for _, m := range models {
			b.WriteString("• " + orModelLine(m) + "\n")
		}
		orFollowUp(s, i, b.String())
		return
	}

	sort.Slice(deals, func(a, b int) bool {
		return float64(deals[a].Pricing.Discount) > float64(deals[b].Pricing.Discount)
	})
	if len(deals) > limit {
		deals = deals[:limit]
	}

	var b strings.Builder
	b.WriteString(header)
	for _, m := range deals {
		b.WriteString("• " + orModelLine(m) + "\n")
	}
	b.WriteString("\n_Batch variants (`:batch`) stack an extra discount on top._")
	orFollowUp(s, i, b.String())
}

func orHandleModel(s *discordgo.Session, i *discordgo.InteractionCreate, sub *discordgo.ApplicationCommandInteractionDataOption) {
	orDefer(s, i)

	slug := orOptStr(sub, "slug")
	m, err := orFetchModel(slug)
	if err != nil {
		orFollowUp(s, i, "❌ "+err.Error())
		return
	}

	embed := &discordgo.MessageEmbed{
		Title:       truncateString(m.Name, 250),
		Description: truncateString(m.Description, 1200),
		Color:       orEmbedColor,
		Footer:      &discordgo.MessageEmbedFooter{Text: m.ID},
		Fields: []*discordgo.MessageEmbedField{
			{Name: "Context", Value: orFmtCtx(m.ContextLength), Inline: true},
			{Name: "Modality", Value: orOrDash(m.Architecture.Modality), Inline: true},
			{Name: "Tokenizer", Value: orOrDash(m.Architecture.Tokenizer), Inline: true},
			{Name: "Input", Value: orOrDash(strings.Join(m.Architecture.InputModalities, ", ")), Inline: true},
			{Name: "Output", Value: orOrDash(strings.Join(m.Architecture.OutputModalities, ", ")), Inline: true},
			{Name: "Max output", Value: orFmtCtx(m.TopProvider.MaxCompletionTokens), Inline: true},
			{Name: "Price", Value: orPricePair(m.Pricing), Inline: false},
		},
	}
	if d := orDiscount(m.Pricing.Discount); d != "" {
		embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{Name: "Discount", Value: d, Inline: true})
	}
	if len(m.SupportedParameters) > 0 {
		embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
			Name:  fmt.Sprintf("Parameters (%d)", len(m.SupportedParameters)),
			Value: truncateString(strings.Join(m.SupportedParameters, ", "), 1000),
		})
	}
	orFollowUpEmbed(s, i, embed)
}

func orHandleCompare(s *discordgo.Session, i *discordgo.InteractionCreate, sub *discordgo.ApplicationCommandInteractionDataOption) {
	orDefer(s, i)

	aSlug := orOptStr(sub, "model_a")
	bSlug := orOptStr(sub, "model_b")
	a, err := orFetchModel(aSlug)
	if err != nil {
		orFollowUp(s, i, "❌ Model A: "+err.Error())
		return
	}
	b, err := orFetchModel(bSlug)
	if err != nil {
		orFollowUp(s, i, "❌ Model B: "+err.Error())
		return
	}

	row := func(label, av, bv string) *discordgo.MessageEmbedField {
		return &discordgo.MessageEmbedField{
			Name:  label,
			Value: fmt.Sprintf("**A:** %s\n**B:** %s", av, bv),
		}
	}

	embed := &discordgo.MessageEmbed{
		Title: "Model comparison",
		Color: orEmbedColor,
		Fields: []*discordgo.MessageEmbedField{
			{Name: "A", Value: "`" + a.ID + "`", Inline: true},
			{Name: "B", Value: "`" + b.ID + "`", Inline: true},
			{Name: "\u200b", Value: "\u200b", Inline: true},
			row("Context", orFmtCtx(a.ContextLength), orFmtCtx(b.ContextLength)),
			row("Price (in / out per M)", orPerM(a.Pricing.Prompt)+" / "+orPerM(a.Pricing.Completion), orPerM(b.Pricing.Prompt)+" / "+orPerM(b.Pricing.Completion)),
			row("Modality", orOrDash(a.Architecture.Modality), orOrDash(b.Architecture.Modality)),
			row("Max output", orFmtCtx(a.TopProvider.MaxCompletionTokens), orFmtCtx(b.TopProvider.MaxCompletionTokens)),
			row("Params", strconv.Itoa(len(a.SupportedParameters)), strconv.Itoa(len(b.SupportedParameters))),
		},
	}
	orFollowUpEmbed(s, i, embed)
}

func orHandleEndpoints(s *discordgo.Session, i *discordgo.InteractionCreate, sub *discordgo.ApplicationCommandInteractionDataOption) {
	orDefer(s, i)

	slug := strings.TrimPrefix(strings.TrimSpace(orOptStr(sub, "slug")), "/")
	if slug == "" {
		orFollowUp(s, i, "❌ Give a model id, e.g. `openai/gpt-5.6-sol`.")
		return
	}
	req, err := orRequest(http.MethodGet, "/models/"+slug+"/endpoints", nil, nil)
	if err != nil {
		orFollowUp(s, i, "❌ "+err.Error())
		return
	}
	body, status, err := orDo(req)
	if err != nil {
		orFollowUp(s, i, "❌ "+err.Error())
		return
	}
	if status != http.StatusOK {
		orFollowUp(s, i, "❌ "+orAPIError(body, status))
		return
	}

	var wrap struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &wrap); err != nil {
		orFollowUp(s, i, "❌ Could not parse endpoints response.")
		return
	}
	var eps []orEndpoint
	var obj struct {
		Endpoints []orEndpoint `json:"endpoints"`
	}
	if json.Unmarshal(wrap.Data, &obj) == nil && len(obj.Endpoints) > 0 {
		eps = obj.Endpoints
	} else {
		_ = json.Unmarshal(wrap.Data, &eps)
	}
	if len(eps) == 0 {
		orFollowUp(s, i, "No endpoints published for `"+slug+"`.")
		return
	}

	sort.Slice(eps, func(a, b int) bool {
		return float64(eps[a].Pricing.Prompt) < float64(eps[b].Pricing.Prompt)
	})

	var b strings.Builder
	b.WriteString(fmt.Sprintf("**Endpoints for `%s`** (%d providers, cheapest first)\n\n", slug, len(eps)))
	for _, e := range eps {
		up := ""
		if float64(e.UptimeLast30m) > 0 {
			up = fmt.Sprintf(" · %.1f%% up", float64(e.UptimeLast30m))
		}
		quant := ""
		if e.Quantization != "" {
			quant = " · " + e.Quantization
		}
		b.WriteString(fmt.Sprintf("• **%s** — %s · %s ctx%s%s\n",
			orOrDash(e.ProviderName), orPricePair(e.Pricing), orFmtCtx(e.ContextLength), quant, up))
	}
	orFollowUp(s, i, b.String())
}
func orHandleProviders(s *discordgo.Session, i *discordgo.InteractionCreate, _ *discordgo.ApplicationCommandInteractionDataOption) {
	orShowProvidersPage(s, i, 0)
}
func orHandlePrice(s *discordgo.Session, i *discordgo.InteractionCreate, sub *discordgo.ApplicationCommandInteractionDataOption) {
	orDefer(s, i)

	m, err := orFetchModel(orOptStr(sub, "slug"))
	if err != nil {
		orFollowUp(s, i, "❌ "+err.Error())
		return
	}

	inTokens := int64(1000)
	outTokens := int64(500)
	if v, ok := orOptInt(sub, "in_tokens"); ok && v > 0 {
		inTokens = v
	}
	if v, ok := orOptInt(sub, "out_tokens"); ok && v > 0 {
		outTokens = v
	}

	inCost := float64(m.Pricing.Prompt) * float64(inTokens)
	outCost := float64(m.Pricing.Completion) * float64(outTokens)
	total := inCost + outCost

	var b strings.Builder
	b.WriteString(fmt.Sprintf("**`%s`** — %s\n\n", m.ID, m.Name))
	b.WriteString(fmt.Sprintf("• Input:  **%s** per M\n", orPerM(m.Pricing.Prompt)))
	b.WriteString(fmt.Sprintf("• Output: **%s** per M\n", orPerM(m.Pricing.Completion)))
	if d := orDiscount(m.Pricing.Discount); d != "" {
		b.WriteString(fmt.Sprintf("• Discount: **%s**\n", d))
	}
	b.WriteString(fmt.Sprintf("\n**Estimate** for %s in + %s out tokens: **$%.6f**",
		orThousands(inTokens), orThousands(outTokens), total))
	if total < 0.01 && total > 0 {
		b.WriteString(fmt.Sprintf("  _(%.4f cents)_", total*100))
	}
	orFollowUp(s, i, b.String())
}

func orHandleKey(s *discordgo.Session, i *discordgo.InteractionCreate, _ *discordgo.ApplicationCommandInteractionDataOption) {
	orDefer(s, i)

	if orAPIKey() == "" {
		orFollowUp(s, i, "❌ No OpenRouter API key configured (`openrouter.apiKey` in `.private/config.json`).")
		return
	}
	k, err := orFetchKey()
	if err != nil {
		orFollowUp(s, i, "❌ "+err.Error())
		return
	}

	remaining := "unlimited"
	if k.LimitRemaining != nil {
		remaining = fmt.Sprintf("$%.4f", *k.LimitRemaining)
	}
	limit := "none"
	if k.Limit != nil {
		limit = fmt.Sprintf("$%.2f", *k.Limit)
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("**OpenRouter key** — %s\n\n", orOrDash(k.Label)))
	b.WriteString(fmt.Sprintf("• Spend today:   **$%.4f**\n", k.UsageDaily))
	b.WriteString(fmt.Sprintf("• This week:     **$%.4f**\n", k.UsageWeekly))
	b.WriteString(fmt.Sprintf("• This month:    **$%.4f**\n", k.UsageMonthly))
	b.WriteString(fmt.Sprintf("• All time:      **$%.4f**\n", k.Usage))
	if k.ByokUsage > 0 {
		b.WriteString(fmt.Sprintf("• BYOK all time: **$%.4f**\n", k.ByokUsage))
	}
	b.WriteString(fmt.Sprintf("\n• Limit: **%s** · Remaining: **%s**\n", limit, remaining))
	if k.FreeModelDailyRequests.Limit > 0 {
		b.WriteString(fmt.Sprintf("• Free-model requests left today: **%d / %d**\n",
			k.FreeModelDailyRequests.Remaining, k.FreeModelDailyRequests.Limit))
	}
	b.WriteString(fmt.Sprintf("• Free tier: **%v** · Management key: **%v**", k.IsFreeTier, k.IsManagementKey))
	orFollowUp(s, i, b.String())
}

func orHandleCredits(s *discordgo.Session, i *discordgo.InteractionCreate, sub *discordgo.ApplicationCommandInteractionDataOption) {
	orDefer(s, i)

	if orAPIKey() == "" {
		orFollowUp(s, i, "\u274c No OpenRouter API key configured.")
		return
	}

	// Always show key info (regular endpoint)
	k, err := orFetchKey()
	if err != nil {
		orFollowUp(s, i, "\u274c "+err.Error())
		return
	}

	remaining := "unlimited"
	if k.LimitRemaining != nil {
		remaining = fmt.Sprintf("$%.4f", *k.LimitRemaining)
	}
	limit := "none"
	if k.Limit != nil {
		limit = fmt.Sprintf("$%.2f", *k.Limit)
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("**OpenRouter \u2014 Credits & Usage**\n\n"))
	b.WriteString(fmt.Sprintf("**Key** \u2014 %s\n", orOrDash(k.Label)))
	b.WriteString(fmt.Sprintf("\u2022 Spend today:   **$%.4f**\n", k.UsageDaily))
	b.WriteString(fmt.Sprintf("\u2022 This week:     **$%.4f**\n", k.UsageWeekly))
	b.WriteString(fmt.Sprintf("\u2022 This month:    **$%.4f**\n", k.UsageMonthly))
	b.WriteString(fmt.Sprintf("\u2022 All time:      **$%.4f**\n", k.Usage))
	if k.ByokUsage > 0 {
		b.WriteString(fmt.Sprintf("\u2022 BYOK all time: **$%.4f**\n", k.ByokUsage))
	}
	b.WriteString(fmt.Sprintf("\n\u2022 Limit: **%s** \u00b7 Remaining: **%s**\n", limit, remaining))
	if k.FreeModelDailyRequests.Limit > 0 {
		b.WriteString(fmt.Sprintf("\u2022 Free-model requests left today: **%d / %d**\n",
			k.FreeModelDailyRequests.Remaining, k.FreeModelDailyRequests.Limit))
	}
	b.WriteString(fmt.Sprintf("\u2022 Free tier: **%v** \u00b7 Management key: **%v**\n", k.IsFreeTier, k.IsManagementKey))

	// Also try to fetch credits (management key endpoint)
	if orMgmtAPIKey() != "" {
		credits, credErr := orFetchCredits()
		if credErr == nil {
			total := float64(credits.TotalCredits)
			used := float64(credits.TotalUsage)
			pct := 0.0
			if total > 0 {
				pct = (used / total) * 100
			}
			b.WriteString(fmt.Sprintf("\n**Account credits**\n"))
			b.WriteString(fmt.Sprintf("\u2022 Purchased: **$%.2f**\n", total))
			b.WriteString(fmt.Sprintf("\u2022 Used:      **$%.2f** (%.1f%%)\n", used, pct))
			b.WriteString(fmt.Sprintf("\u2022 Remaining: **$%.2f**\n", total-used))
		}
	}

	orFollowUp(s, i, b.String())
}

func orHandleGen(s *discordgo.Session, i *discordgo.InteractionCreate, sub *discordgo.ApplicationCommandInteractionDataOption) {
	orDefer(s, i)

	if orAPIKey() == "" {
		orFollowUp(s, i, "❌ No OpenRouter API key configured.")
		return
	}
	id := strings.TrimSpace(orOptStr(sub, "id"))
	if id == "" {
		orFollowUp(s, i, "❌ Give a generation id, e.g. `gen-1234567890`.")
		return
	}

	req, err := orRequest(http.MethodGet, "/generation", url.Values{"id": {id}}, nil)
	if err != nil {
		orFollowUp(s, i, "❌ "+err.Error())
		return
	}
	body, status, err := orDo(req)
	if err != nil {
		orFollowUp(s, i, "❌ "+err.Error())
		return
	}
	if status != http.StatusOK {
		orFollowUp(s, i, "❌ "+orAPIError(body, status))
		return
	}
	var resp struct {
		Data orGeneration `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		orFollowUp(s, i, "❌ Could not parse generation response.")
		return
	}
	g := resp.Data

	var b strings.Builder
	b.WriteString(fmt.Sprintf("**Generation `%s`**\n\n", g.ID))
	b.WriteString(fmt.Sprintf("• Model: **%s** via **%s**\n", orOrDash(g.Model), orOrDash(g.ProviderName)))
	if g.Router != "" {
		b.WriteString(fmt.Sprintf("• Router: `%s`\n", g.Router))
	}
	b.WriteString(fmt.Sprintf("• Tokens: **%d** in / **%d** out", g.TokensPrompt, g.TokensCompletion))
	if g.NativeTokensReasoning > 0 {
		b.WriteString(fmt.Sprintf(" (%d reasoning)", g.NativeTokensReasoning))
	}
	b.WriteString("\n")
	b.WriteString(fmt.Sprintf("• Cost: **$%.6f**", float64(g.TotalCost)))
	if float64(g.UpstreamInferenceCost) > 0 {
		b.WriteString(fmt.Sprintf(" (upstream $%.6f)", float64(g.UpstreamInferenceCost)))
	}
	b.WriteString("\n")
	if float64(g.CacheDiscount) != 0 {
		b.WriteString(fmt.Sprintf("• Cache discount: **$%.6f**\n", float64(g.CacheDiscount)))
	}
	b.WriteString(fmt.Sprintf("• Latency: **%.0f ms** · generation %.0f ms\n", float64(g.Latency), float64(g.GenerationTime)))
	b.WriteString(fmt.Sprintf("• Finish: **%s**", orOrDash(g.FinishReason)))
	if g.NativeFinishReason != "" {
		b.WriteString(fmt.Sprintf(" (native: %s)", g.NativeFinishReason))
	}
	if g.NumSearchResults > 0 {
		b.WriteString(fmt.Sprintf("\n• Web search results: %d", g.NumSearchResults))
	}
	orFollowUp(s, i, b.String())
}

func orHandleHistory(s *discordgo.Session, i *discordgo.InteractionCreate, sub *discordgo.ApplicationCommandInteractionDataOption) {
	orDefer(s, i)
	if orAPIKey() == "" {
		orFollowUp(s, i, "❌ No OpenRouter API key configured.")
		return
	}

	limit := 10
	if v, ok := orOptInt(sub, "limit"); ok && v > 0 && v <= 50 {
		limit = int(v)
	}

	req, err := orRequest(http.MethodGet, "/generations?limit="+strconv.Itoa(limit), nil, nil)
	if err != nil {
		orFollowUp(s, i, "❌ "+err.Error())
		return
	}
	body, status, err := orDo(req)
	if err != nil {
		orFollowUp(s, i, "❌ "+err.Error())
		return
	}
	if status != http.StatusOK {
		orFollowUp(s, i, "❌ "+orAPIError(body, status))
		return
	}

	var wrap struct {
		Data []orGeneration `json:"data"`
	}
	if json.Unmarshal(body, &wrap) != nil || len(wrap.Data) == 0 {
		orFollowUp(s, i, "No generations found.")
		return
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("**OpenRouter — Recent generations** (last %d)\n\n", len(wrap.Data)))
	for _, g := range wrap.Data {
		cost := float64(g.TotalCost)
		b.WriteString(fmt.Sprintf("• `%s` — **%s** · $%.4f · %d+%d tok\n",
			g.ID[:24], orOrDash(g.Model), cost, g.TokensPrompt, g.TokensCompletion))
	}
	b.WriteString("\n_Use `/openrouter gen id:<id>` to inspect one generation._")
	orFollowUp(s, i, b.String())
}

func orHandleUsage(s *discordgo.Session, i *discordgo.InteractionCreate, _ *discordgo.ApplicationCommandInteractionDataOption) {
	orDefer(s, i)

	if orAPIKey() == "" {
		orFollowUp(s, i, "❌ No OpenRouter API key configured.")
		return
	}
	k, err := orFetchKey()
	if err != nil {
		orFollowUp(s, i, "❌ "+err.Error())
		return
	}

	pctLine := ""
	if k.Limit != nil && *k.Limit > 0 {
		pct := (k.Usage / *k.Limit) * 100
		pctLine = fmt.Sprintf("\n\n**%.1f%%** of a **$%.2f** lifetime limit used.", pct, *k.Limit)
	}

	var b strings.Builder
	b.WriteString("**OpenRouter spend dashboard**\n\n")
	b.WriteString(fmt.Sprintf("Today:   **$%.4f**\n", k.UsageDaily))
	b.WriteString(fmt.Sprintf("Week:    **$%.4f**\n", k.UsageWeekly))
	b.WriteString(fmt.Sprintf("Month:   **$%.4f**\n", k.UsageMonthly))
	b.WriteString(fmt.Sprintf("Lifetime: **$%.4f**\n", k.Usage))
	if k.ByokUsageDaily > 0 || k.ByokUsageMonthly > 0 {
		b.WriteString(fmt.Sprintf("\nBYOK — today **$%.4f** · month **$%.4f** · lifetime **$%.4f**\n",
			k.ByokUsageDaily, k.ByokUsageMonthly, k.ByokUsage))
	}
	b.WriteString(pctLine)
	b.WriteString("\n\n_Use `/openrouter gen id:<gen-...>` to audit a single request's real cost._")
	orFollowUp(s, i, b.String())
}

func orHandleAsk(s *discordgo.Session, i *discordgo.InteractionCreate, sub *discordgo.ApplicationCommandInteractionDataOption) {
	orDefer(s, i)

	if orAPIKey() == "" {
		orFollowUp(s, i, "❌ No OpenRouter API key configured (`openrouter.apiKey`).")
		return
	}

	prompt := strings.TrimSpace(orOptStr(sub, "prompt"))
	if prompt == "" {
		orFollowUp(s, i, "❌ Give a prompt.")
		return
	}
	model := strings.TrimSpace(orOptStr(sub, "model"))
	if model == "" {
		model = orDefaultAsk
	}
	maxTokens := 1024
	if v, ok := orOptInt(sub, "max_tokens"); ok && v > 0 {
		maxTokens = int(v)
	}

	payload := map[string]any{
		"model":      model,
		"max_tokens": maxTokens,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
	}
	if t, ok := orOptNum(sub, "temperature"); ok {
		payload["temperature"] = t
	}
	bodyBytes, _ := json.Marshal(payload)

	req, err := orRequest(http.MethodPost, "/chat/completions", nil, bodyBytes)
	if err != nil {
		orFollowUp(s, i, "❌ "+err.Error())
		return
	}
	body, status, err := orDo(req)
	if err != nil {
		orFollowUp(s, i, "❌ "+err.Error())
		return
	}
	if status != http.StatusOK {
		orFollowUp(s, i, "❌ "+orAPIError(body, status))
		return
	}

	var resp struct {
		ID      string `json:"id"`
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int       `json:"prompt_tokens"`
			CompletionTokens int       `json:"completion_tokens"`
			TotalTokens      int       `json:"total_tokens"`
			Cost             flexFloat `json:"cost"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		orFollowUp(s, i, "❌ Could not parse completion response.")
		return
	}
	if len(resp.Choices) == 0 {
		orFollowUp(s, i, "OpenRouter returned no choices.")
		return
	}

	answer := strings.TrimSpace(resp.Choices[0].Message.Content)
	if answer == "" {
		answer = "_(empty response)_"
	}
	usedModel := resp.Model
	if usedModel == "" {
		usedModel = model
	}

	footer := fmt.Sprintf("%s · %d in / %d out tokens · $%.6f",
		usedModel, resp.Usage.PromptTokens, resp.Usage.CompletionTokens, float64(resp.Usage.Cost))
	if resp.Choices[0].FinishReason != "" && resp.Choices[0].FinishReason != "stop" {
		footer += " · " + resp.Choices[0].FinishReason
	}

	embed := &discordgo.MessageEmbed{
		Description: truncateString(answer, 4000),
		Color:       orEmbedColor,
		Footer:      &discordgo.MessageEmbedFooter{Text: footer},
	}
	if resp.ID != "" {
		embed.Title = "`" + resp.ID + "`"
	}
	orFollowUpEmbed(s, i, embed)
}

// ---------- Tier 2: routing wrappers (free, online, think, route, fallback, fusion, tools) ----------

func orHandleFree(s *discordgo.Session, i *discordgo.InteractionCreate, sub *discordgo.ApplicationCommandInteractionDataOption) {
	// Alias: free is ask with model=openrouter/free
	prompt := strings.TrimSpace(orOptStr(sub, "prompt"))
	if prompt == "" {
		orFollowUp(s, i, "❌ Give a prompt. Usage: `/openrouter free prompt:<text>`")
		return
	}
	if orAPIKey() == "" {
		orFollowUp(s, i, "❌ No OpenRouter API key configured.")
		return
	}
	// Build a minimal ask payload with model=openrouter/free
	payload := map[string]any{
		"model":      "openrouter/free",
		"max_tokens": 1024,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
	}
	if v, ok := orOptInt(sub, "max_tokens"); ok && v > 0 {
		payload["max_tokens"] = v
	}
	bodyBytes, _ := json.Marshal(payload)
	req, err := orRequest(http.MethodPost, "/chat/completions", nil, bodyBytes)
	if err != nil {
		orFollowUp(s, i, "❌ "+err.Error())
		return
	}
	body, status, err := orDo(req)
	if err != nil {
		orFollowUp(s, i, "❌ "+err.Error())
		return
	}
	if status != http.StatusOK {
		orFollowUp(s, i, "❌ "+orAPIError(body, status))
		return
	}

	var resp struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int       `json:"prompt_tokens"`
			CompletionTokens int       `json:"completion_tokens"`
			Cost             flexFloat `json:"cost"`
		} `json:"usage"`
	}
	if json.Unmarshal(body, &resp) != nil || len(resp.Choices) == 0 {
		orFollowUp(s, i, "❌ Could not parse free-model response.")
		return
	}
	answer := strings.TrimSpace(resp.Choices[0].Message.Content)
	if answer == "" {
		answer = "_(empty response)_"
	}
	footer := fmt.Sprintf("%s · %d in / %d out · $%.6f", resp.Model, resp.Usage.PromptTokens, resp.Usage.CompletionTokens, float64(resp.Usage.Cost))
	embed := &discordgo.MessageEmbed{
		Description: truncateString(answer, 4000),
		Color:       orEmbedColor,
		Footer:      &discordgo.MessageEmbedFooter{Text: footer},
	}
	orFollowUpEmbed(s, i, embed)
}

func orHandleOnline(s *discordgo.Session, i *discordgo.InteractionCreate, sub *discordgo.ApplicationCommandInteractionDataOption) {
	orDefer(s, i)
	prompt := strings.TrimSpace(orOptStr(sub, "prompt"))
	if prompt == "" {
		orFollowUp(s, i, "❌ Give a prompt. Usage: `/openrouter online prompt:<text>`")
		return
	}
	if orAPIKey() == "" {
		orFollowUp(s, i, "❌ No OpenRouter API key configured.")
		return
	}
	model := strings.TrimSpace(orOptStr(sub, "model"))
	if model == "" {
		model = "openrouter/auto"
	}
	if !strings.HasSuffix(model, ":online") {
		model += ":online"
	}

	payload := map[string]any{
		"model":      model,
		"max_tokens": 1024,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
	}
	if v, ok := orOptInt(sub, "max_tokens"); ok && v > 0 {
		payload["max_tokens"] = v
	}
	bodyBytes, _ := json.Marshal(payload)
	req, err := orRequest(http.MethodPost, "/chat/completions", nil, bodyBytes)
	if err != nil {
		orFollowUp(s, i, "❌ "+err.Error())
		return
	}
	body, status, err := orDo(req)
	if err != nil {
		orFollowUp(s, i, "❌ "+err.Error())
		return
	}
	if status != http.StatusOK {
		orFollowUp(s, i, "❌ "+orAPIError(body, status))
		return
	}

	var resp struct {
		ID      string `json:"id"`
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int       `json:"prompt_tokens"`
			CompletionTokens int       `json:"completion_tokens"`
			Cost             flexFloat `json:"cost"`
		} `json:"usage"`
	}
	if json.Unmarshal(body, &resp) != nil || len(resp.Choices) == 0 {
		orFollowUp(s, i, "❌ Could not parse online response.")
		return
	}
	answer := strings.TrimSpace(resp.Choices[0].Message.Content)
	if answer == "" {
		answer = "_(empty response)_"
	}
	footer := fmt.Sprintf("%s · %d in / %d out · $%.6f", resp.Model, resp.Usage.PromptTokens, resp.Usage.CompletionTokens, float64(resp.Usage.Cost))
	if resp.ID != "" {
		footer += " · `" + resp.ID + "`"
	}
	embed := &discordgo.MessageEmbed{
		Description: truncateString(answer, 4000),
		Color:       orEmbedColor,
		Footer:      &discordgo.MessageEmbedFooter{Text: footer},
	}
	orFollowUpEmbed(s, i, embed)
}

func orHandleThink(s *discordgo.Session, i *discordgo.InteractionCreate, sub *discordgo.ApplicationCommandInteractionDataOption) {
	orDefer(s, i)
	prompt := strings.TrimSpace(orOptStr(sub, "prompt"))
	if prompt == "" {
		orFollowUp(s, i, "❌ Give a prompt. Usage: `/openrouter think prompt:<text>`")
		return
	}
	if orAPIKey() == "" {
		orFollowUp(s, i, "❌ No OpenRouter API key configured.")
		return
	}
	model := strings.TrimSpace(orOptStr(sub, "model"))
	if model == "" {
		model = "openrouter/auto"
	}

	payload := map[string]any{
		"model":      model,
		"max_tokens": 2048,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
	}
	if v, ok := orOptInt(sub, "max_tokens"); ok && v > 0 {
		payload["max_tokens"] = v
	}
	reasoningMax := 2048
	if v, ok := orOptInt(sub, "reasoning_tokens"); ok && v > 0 {
		reasoningMax = int(v)
	}
	payload["reasoning"] = map[string]any{"max_tokens": reasoningMax}

	bodyBytes, _ := json.Marshal(payload)
	req, err := orRequest(http.MethodPost, "/chat/completions", nil, bodyBytes)
	if err != nil {
		orFollowUp(s, i, "❌ "+err.Error())
		return
	}
	body, status, err := orDo(req)
	if err != nil {
		orFollowUp(s, i, "❌ "+err.Error())
		return
	}
	if status != http.StatusOK {
		orFollowUp(s, i, "❌ "+orAPIError(body, status))
		return
	}

	var resp struct {
		ID      string `json:"id"`
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content   string `json:"content"`
				Reasoning string `json:"reasoning,omitempty"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int       `json:"prompt_tokens"`
			CompletionTokens int       `json:"completion_tokens"`
			Cost             flexFloat `json:"cost"`
		} `json:"usage"`
	}
	if json.Unmarshal(body, &resp) != nil || len(resp.Choices) == 0 {
		orFollowUp(s, i, "❌ Could not parse thinking response.")
		return
	}
	answer := strings.TrimSpace(resp.Choices[0].Message.Content)
	if answer == "" {
		answer = "_(empty response)_"
	}
	footer := fmt.Sprintf("%s · %d in / %d out · $%.6f", resp.Model, resp.Usage.PromptTokens, resp.Usage.CompletionTokens, float64(resp.Usage.Cost))
	embed := &discordgo.MessageEmbed{
		Description: truncateString(answer, 4000),
		Color:       orEmbedColor,
		Footer:      &discordgo.MessageEmbedFooter{Text: footer},
	}
	orFollowUpEmbed(s, i, embed)
}

func orHandleRoute(s *discordgo.Session, i *discordgo.InteractionCreate, sub *discordgo.ApplicationCommandInteractionDataOption) {
	orDefer(s, i)
	prompt := strings.TrimSpace(orOptStr(sub, "prompt"))
	if prompt == "" {
		orFollowUp(s, i, "**OpenRouter — Routing & Provider Preferences**\n\n"+
			"Route requests across upstream providers dynamically:\n\n"+
			"• `/openrouter route prompt:<text> sort:<price|latency|throughput>` — Query with provider sort\n"+
			"• `/openrouter free prompt:<text>` — Free models router (`openrouter/free`)\n"+
			"• `/openrouter online prompt:<text>` — Live web search routing (`:online`)\n"+
			"• `/openrouter think prompt:<text>` — Reasoning/thinking models\n"+
			"• `/openrouter fallback prompt:<text> models:<model1,model2>` — Explicit failover list\n"+
			"• `/openrouter fusion prompt:<text>` — Multi-model deliberation (`openrouter/fusion`)\n"+
			"• `/openrouter endpoints slug:<id>` — Inspect upstream endpoints and uptime\n\n"+
			"_Routing engine honors `sort: price`, `latency`, or `throughput`, fallback chains, and data privacy policies._")
		return
	}
	if orAPIKey() == "" {
		orFollowUp(s, i, "❌ No OpenRouter API key configured.")
		return
	}

	model := strings.TrimSpace(orOptStr(sub, "model"))
	if model == "" {
		model = "openrouter/auto"
	}
	sortPref := strings.TrimSpace(orOptStr(sub, "sort"))
	if sortPref == "" {
		sortPref = "price"
	}

	providerMap := map[string]any{
		"sort":            sortPref,
		"allow_fallbacks": true,
	}
	payload := map[string]any{
		"model":      model,
		"provider":   providerMap,
		"max_tokens": 1024,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
	}
	bodyBytes, _ := json.Marshal(payload)
	req, err := orRequest(http.MethodPost, "/chat/completions", nil, bodyBytes)
	if err != nil {
		orFollowUp(s, i, "❌ "+err.Error())
		return
	}
	body, status, err := orDo(req)
	if err != nil {
		orFollowUp(s, i, "❌ "+err.Error())
		return
	}
	if status != http.StatusOK {
		orFollowUp(s, i, "❌ "+orAPIError(body, status))
		return
	}

	var resp struct {
		ID      string `json:"id"`
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int       `json:"prompt_tokens"`
			CompletionTokens int       `json:"completion_tokens"`
			Cost             flexFloat `json:"cost"`
		} `json:"usage"`
	}
	if json.Unmarshal(body, &resp) != nil || len(resp.Choices) == 0 {
		orFollowUp(s, i, "❌ Could not parse completion response.")
		return
	}
	answer := strings.TrimSpace(resp.Choices[0].Message.Content)
	footer := fmt.Sprintf("%s · routed by %s · %d in / %d out · $%.6f",
		resp.Model, sortPref, resp.Usage.PromptTokens, resp.Usage.CompletionTokens, float64(resp.Usage.Cost))
	embed := &discordgo.MessageEmbed{
		Description: truncateString(answer, 4000),
		Color:       orEmbedColor,
		Footer:      &discordgo.MessageEmbedFooter{Text: footer},
	}
	orFollowUpEmbed(s, i, embed)
}

func orHandleFallback(s *discordgo.Session, i *discordgo.InteractionCreate, sub *discordgo.ApplicationCommandInteractionDataOption) {
	orDefer(s, i)
	prompt := strings.TrimSpace(orOptStr(sub, "prompt"))
	if prompt == "" {
		orFollowUp(s, i, "**OpenRouter — Model Fallback Array**\n\n"+
			"If a primary model fails or is rate-limited, OpenRouter automatically falls back down the list.\n\n"+
			"Usage: `/openrouter fallback prompt:<text> models:<model1,model2,...>`\n\n"+
			"Example: `/openrouter fallback prompt:Hello models:anthropic/claude-3.5-haiku,openai/gpt-4o-mini`")
		return
	}
	if orAPIKey() == "" {
		orFollowUp(s, i, "❌ No OpenRouter API key configured.")
		return
	}

	modelsStr := strings.TrimSpace(orOptStr(sub, "models"))
	var modelsList []string
	if modelsStr != "" {
		for _, m := range strings.Split(modelsStr, ",") {
			m = strings.TrimSpace(m)
			if m != "" {
				modelsList = append(modelsList, m)
			}
		}
	}
	if len(modelsList) == 0 {
		modelsList = []string{"anthropic/claude-3.5-haiku", "openai/gpt-4o-mini", "openrouter/auto"}
	}

	payload := map[string]any{
		"models":     modelsList,
		"max_tokens": 1024,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
	}
	bodyBytes, _ := json.Marshal(payload)
	req, err := orRequest(http.MethodPost, "/chat/completions", nil, bodyBytes)
	if err != nil {
		orFollowUp(s, i, "❌ "+err.Error())
		return
	}
	body, status, err := orDo(req)
	if err != nil {
		orFollowUp(s, i, "❌ "+err.Error())
		return
	}
	if status != http.StatusOK {
		orFollowUp(s, i, "❌ "+orAPIError(body, status))
		return
	}

	var resp struct {
		ID      string `json:"id"`
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int       `json:"prompt_tokens"`
			CompletionTokens int       `json:"completion_tokens"`
			Cost             flexFloat `json:"cost"`
		} `json:"usage"`
	}
	if json.Unmarshal(body, &resp) != nil || len(resp.Choices) == 0 {
		orFollowUp(s, i, "❌ Could not parse fallback response.")
		return
	}
	answer := strings.TrimSpace(resp.Choices[0].Message.Content)
	footer := fmt.Sprintf("%s · fallback list: [%s] · $%.6f", resp.Model, strings.Join(modelsList, ", "), float64(resp.Usage.Cost))
	embed := &discordgo.MessageEmbed{
		Description: truncateString(answer, 4000),
		Color:       orEmbedColor,
		Footer:      &discordgo.MessageEmbedFooter{Text: footer},
	}
	orFollowUpEmbed(s, i, embed)
}

func orHandleFusion(s *discordgo.Session, i *discordgo.InteractionCreate, sub *discordgo.ApplicationCommandInteractionDataOption) {
	orDefer(s, i)
	prompt := strings.TrimSpace(orOptStr(sub, "prompt"))
	if prompt == "" {
		orFollowUp(s, i, "❌ Give a prompt. Usage: `/openrouter fusion prompt:<text>`\n\n"+
			"Model Fusion routes your prompt through `openrouter/fusion`, multi-model deliberation that merges diverse responses.")
		return
	}
	if orAPIKey() == "" {
		orFollowUp(s, i, "❌ No OpenRouter API key configured.")
		return
	}

	payload := map[string]any{
		"model":      "openrouter/fusion",
		"max_tokens": 1500,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
	}
	bodyBytes, _ := json.Marshal(payload)
	req, err := orRequest(http.MethodPost, "/chat/completions", nil, bodyBytes)
	if err != nil {
		orFollowUp(s, i, "❌ "+err.Error())
		return
	}
	body, status, err := orDo(req)
	if err != nil {
		orFollowUp(s, i, "❌ "+err.Error())
		return
	}
	if status != http.StatusOK {
		orFollowUp(s, i, "❌ "+orAPIError(body, status))
		return
	}

	var resp struct {
		ID      string `json:"id"`
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int       `json:"prompt_tokens"`
			CompletionTokens int       `json:"completion_tokens"`
			Cost             flexFloat `json:"cost"`
		} `json:"usage"`
	}
	if json.Unmarshal(body, &resp) != nil || len(resp.Choices) == 0 {
		orFollowUp(s, i, "❌ Could not parse fusion response.")
		return
	}
	answer := strings.TrimSpace(resp.Choices[0].Message.Content)
	footer := fmt.Sprintf("%s · %d in / %d out · $%.6f", resp.Model, resp.Usage.PromptTokens, resp.Usage.CompletionTokens, float64(resp.Usage.Cost))
	embed := &discordgo.MessageEmbed{
		Title:       "⚡ OpenRouter Model Fusion",
		Description: truncateString(answer, 4000),
		Color:       orEmbedColor,
		Footer:      &discordgo.MessageEmbedFooter{Text: footer},
	}
	orFollowUpEmbed(s, i, embed)
}

func orHandleTools(s *discordgo.Session, i *discordgo.InteractionCreate, sub *discordgo.ApplicationCommandInteractionDataOption) {
	orDefer(s, i)
	q := url.Values{}
	q.Set("supported_parameters", "tools")
	q.Set("limit", "15")
	models, total, err := orFetchModels(q)
	if err != nil || len(models) == 0 {
		q.Set("supported_parameters", "tool_calls")
		models, total, err = orFetchModels(q)
	}
	if err != nil {
		orFollowUp(s, i, "❌ "+err.Error())
		return
	}
	if len(models) == 0 {
		orFollowUp(s, i, "No models with tool-calling support found.")
		return
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("**OpenRouter — models with tool calling** (showing %d of %d)\n\n", len(models), total))
	for _, m := range models {
		b.WriteString(fmt.Sprintf("• `%s` — %s · %s ctx\n", m.ID, m.Name, orFmtCtx(m.ContextLength)))
	}
	orFollowUp(s, i, b.String())
}

// ---------- Tier 2: retrieval (embed, similar, rerank) ----------

func orHandleEmbed(s *discordgo.Session, i *discordgo.InteractionCreate, sub *discordgo.ApplicationCommandInteractionDataOption) {
	orDefer(s, i)
	if orAPIKey() == "" {
		orFollowUp(s, i, "❌ No OpenRouter API key configured.")
		return
	}

	input := strings.TrimSpace(orOptStr(sub, "input"))
	if input == "" {
		orFollowUp(s, i, "❌ Give text to embed. Usage: `/openrouter embed input:<text>`")
		return
	}
	model := strings.TrimSpace(orOptStr(sub, "model"))
	if model == "" {
		model = "openai/text-embedding-3-small"
	}
	dimensions := 0
	if v, ok := orOptInt(sub, "dimensions"); ok && v > 0 {
		dimensions = int(v)
	}

	resp, err := orFetchEmbeddings(model, input, dimensions)
	if err != nil {
		orFollowUp(s, i, "❌ "+err.Error())
		return
	}
	if len(resp.Data) == 0 {
		orFollowUp(s, i, "No embeddings returned.")
		return
	}

	embedding := resp.Data[0].Embedding
	var vals []string
	for idx, v := range embedding {
		if idx >= 5 {
			break
		}
		vals = append(vals, fmt.Sprintf("%.6f", v))
	}
	preview := strings.Join(vals, ", ")
	if len(embedding) > 5 {
		preview += ", ..."
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("**Embedding** — `%s`\n\n", resp.Model))
	b.WriteString(fmt.Sprintf("• Dimensions: **%d**\n", len(embedding)))
	b.WriteString(fmt.Sprintf("• Preview: `[%s]`\n", preview))
	b.WriteString(fmt.Sprintf("• Tokens used: **%d**", resp.Usage.TotalTokens))
	orFollowUp(s, i, b.String())
}

func orHandleSimilar(s *discordgo.Session, i *discordgo.InteractionCreate, sub *discordgo.ApplicationCommandInteractionDataOption) {
	orDefer(s, i)
	if orAPIKey() == "" {
		orFollowUp(s, i, "❌ No OpenRouter API key configured.")
		return
	}

	textA := strings.TrimSpace(orOptStr(sub, "text_a"))
	textB := strings.TrimSpace(orOptStr(sub, "text_b"))
	if textA == "" || textB == "" {
		orFollowUp(s, i, "❌ Give two texts to compare. Usage: `/openrouter similar text_a:<text1> text_b:<text2>`")
		return
	}
	model := strings.TrimSpace(orOptStr(sub, "model"))
	if model == "" {
		model = "openai/text-embedding-3-small"
	}

	resp, err := orFetchEmbeddings(model, []string{textA, textB}, 0)
	if err != nil {
		orFollowUp(s, i, "❌ "+err.Error())
		return
	}
	if len(resp.Data) < 2 {
		orFollowUp(s, i, "❌ Failed to generate embeddings for both texts.")
		return
	}

	vecA := resp.Data[0].Embedding
	vecB := resp.Data[1].Embedding
	if len(vecA) != len(vecB) || len(vecA) == 0 {
		orFollowUp(s, i, "❌ Vector dimension mismatch.")
		return
	}

	var dot, magA, magB float64
	for idx := range vecA {
		dot += vecA[idx] * vecB[idx]
		magA += vecA[idx] * vecA[idx]
		magB += vecB[idx] * vecB[idx]
	}
	similarity := 0.0
	if magA > 0 && magB > 0 {
		similarity = dot / (math.Sqrt(magA) * math.Sqrt(magB))
	}

	pct := similarity * 100
	var b strings.Builder
	b.WriteString(fmt.Sprintf("**Semantic Similarity** — `%s`\n\n", resp.Model))
	b.WriteString(fmt.Sprintf("• **Cosine Similarity:** **%.2f%%** (score: `%.4f`)\n\n", pct, similarity))
	b.WriteString(fmt.Sprintf("**Text A:** %s\n", truncateString(textA, 150)))
	b.WriteString(fmt.Sprintf("**Text B:** %s\n\n", truncateString(textB, 150)))
	b.WriteString(fmt.Sprintf("_Dimensions: %d · Tokens used: %d_", len(vecA), resp.Usage.TotalTokens))
	orFollowUp(s, i, b.String())
}

func orHandleRerank(s *discordgo.Session, i *discordgo.InteractionCreate, sub *discordgo.ApplicationCommandInteractionDataOption) {
	orDefer(s, i)
	if orAPIKey() == "" {
		orFollowUp(s, i, "❌ No OpenRouter API key configured.")
		return
	}

	query := strings.TrimSpace(orOptStr(sub, "query"))
	if query == "" {
		orFollowUp(s, i, "❌ Give a query. Usage: `/openrouter rerank query:<text> documents:<doc1|doc2|...>`")
		return
	}
	docsStr := strings.TrimSpace(orOptStr(sub, "documents"))
	if docsStr == "" {
		orFollowUp(s, i, "❌ Give documents separated by `|`. Usage: `/openrouter rerank query:<text> documents:<doc1|doc2>`")
		return
	}
	documents := strings.Split(docsStr, "|")
	model := strings.TrimSpace(orOptStr(sub, "model"))
	if model == "" {
		model = "cohere/rerank-v3.5"
	}
	topN := 5
	if v, ok := orOptInt(sub, "top_n"); ok && v > 0 {
		topN = int(v)
	}

	resp, err := orFetchRerank(model, query, documents, topN)
	if err != nil {
		orFollowUp(s, i, "❌ "+err.Error())
		return
	}
	if len(resp.Results) == 0 {
		orFollowUp(s, i, "No rerank results.")
		return
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("**Rerank results** — `%s`\n\n", resp.Model))
	for _, r := range resp.Results {
		score := r.RelevanceScore * 100
		b.WriteString(fmt.Sprintf("• **%.1f%%** — %s\n", score, truncateString(r.Document.Text, 200)))
	}
	b.WriteString(fmt.Sprintf("\n_%d search units · total tokens %d_", resp.Usage.SearchUnits, resp.Usage.TotalTokens))
	orFollowUp(s, i, b.String())
}

// ---------- Tier 2: media (image, video, tts, stt) ----------

func orHandleImage(s *discordgo.Session, i *discordgo.InteractionCreate, sub *discordgo.ApplicationCommandInteractionDataOption) {
	orDefer(s, i)
	if orAPIKey() == "" {
		orFollowUp(s, i, "❌ No OpenRouter API key configured.")
		return
	}
	prompt := strings.TrimSpace(orOptStr(sub, "prompt"))
	if prompt == "" {
		orFollowUp(s, i, "❌ Give a prompt. Usage: `/openrouter image prompt:<text>`")
		return
	}
	model := strings.TrimSpace(orOptStr(sub, "model"))
	if model == "" {
		model = "bytedance-seed/seedream-5-0-flash"
	}

	payload := map[string]any{
		"model":  model,
		"prompt": prompt,
		"n":      1,
	}
	bodyBytes, _ := json.Marshal(payload)
	req, err := orRequest(http.MethodPost, "/images/generations", nil, bodyBytes)
	if err != nil {
		orFollowUp(s, i, "❌ "+err.Error())
		return
	}
	body, status, err := orDo(req)
	if err != nil {
		orFollowUp(s, i, "❌ "+err.Error())
		return
	}
	if status != http.StatusOK {
		orFollowUp(s, i, "❌ "+orAPIError(body, status))
		return
	}

	var resp struct {
		Created int64 `json:"created"`
		Data    []struct {
			B64JSON   string `json:"b64_json"`
			URL       string `json:"url"`
			MediaType string `json:"media_type,omitempty"`
		} `json:"data"`
		Usage struct {
			Cost flexFloat `json:"cost"`
		} `json:"usage"`
	}
	if json.Unmarshal(body, &resp) != nil || len(resp.Data) == 0 {
		orFollowUp(s, i, "❌ Could not parse image response.")
		return
	}

	costLine := ""
	if float64(resp.Usage.Cost) > 0 {
		costLine = fmt.Sprintf(" · $%.4f", float64(resp.Usage.Cost))
	}

	if resp.Data[0].B64JSON != "" {
		imgBytes, decErr := base64.StdEncoding.DecodeString(resp.Data[0].B64JSON)
		if decErr != nil {
			orFollowUp(s, i, "❌ Failed to decode image: "+decErr.Error())
			return
		}
		ext := "png"
		if resp.Data[0].MediaType == "image/jpeg" {
			ext = "jpg"
		} else if resp.Data[0].MediaType == "image/webp" {
			ext = "webp"
		}
		_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
			Content: fmt.Sprintf("**`%s`** generated%s", model, costLine),
			Files: []*discordgo.File{
				{Name: fmt.Sprintf("or_image_%d.%s", resp.Created, ext), Reader: bytes.NewReader(imgBytes)},
			},
		})
		return
	}

	if resp.Data[0].URL != "" {
		embed := &discordgo.MessageEmbed{
			Title:       fmt.Sprintf("`%s` generated%s", model, costLine),
			Description: truncateString(prompt, 200),
			Image:       &discordgo.MessageEmbedImage{URL: resp.Data[0].URL},
			Color:       orEmbedColor,
		}
		orFollowUpEmbed(s, i, embed)
		return
	}

	orFollowUp(s, i, "❌ No image data or URL returned.")
}

func orHandleVideo(s *discordgo.Session, i *discordgo.InteractionCreate, sub *discordgo.ApplicationCommandInteractionDataOption) {
	orDefer(s, i)
	prompt := strings.TrimSpace(orOptStr(sub, "prompt"))
	if prompt == "" {
		models, err := orFetchVideoModels()
		if err != nil {
			orFollowUp(s, i, "❌ Could not load video model catalog: "+err.Error())
			return
		}
		var b strings.Builder
		b.WriteString("**OpenRouter — Video Generation**\n\n")
		b.WriteString("Available video generation models:\n")
		for _, m := range models {
			name := m.Name
			if name == "" {
				name = m.ID
			}
			b.WriteString(fmt.Sprintf("• `%s` — %s", m.ID, name))
			if len(m.SupportedDurations) > 0 {
				durations := make([]string, len(m.SupportedDurations))
				for idx, duration := range m.SupportedDurations {
					durations[idx] = strconv.Itoa(duration)
				}
				b.WriteString(" · " + strings.Join(durations, ", ") + "s")
			}
			b.WriteByte('\n')
		}
		if len(models) == 0 {
			b.WriteString("No video models are currently listed.\n")
		}
		b.WriteString("\nUsage: `/openrouter video prompt:<description> [model]`\n")
		b.WriteString("_Video generation runs asynchronously and may take several minutes._")
		orFollowUp(s, i, b.String())
		return
	}
	if orAPIKey() == "" {
		orFollowUp(s, i, "❌ No OpenRouter API key configured.")
		return
	}

	model := strings.TrimSpace(orOptStr(sub, "model"))
	if model == "" {
		model = "google/veo-3.1"
	}

	payload := map[string]any{
		"model":  model,
		"prompt": prompt,
	}
	bodyBytes, _ := json.Marshal(payload)
	req, err := orRequest(http.MethodPost, "/videos", nil, bodyBytes)
	if err != nil {
		orFollowUp(s, i, "❌ "+err.Error())
		return
	}
	body, status, err := orDo(req)
	if err != nil {
		orFollowUp(s, i, "❌ "+err.Error())
		return
	}
	if status != http.StatusAccepted && status != http.StatusOK {
		orFollowUp(s, i, "❌ "+orAPIError(body, status))
		return
	}

	var job struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		Error  string `json:"error"`
	}
	if err := json.Unmarshal(body, &job); err != nil {
		orFollowUp(s, i, "❌ Could not parse OpenRouter video job response.")
		return
	}
	if job.ID == "" {
		orFollowUp(s, i, "❌ OpenRouter accepted the request but did not return a video job ID.")
		return
	}

	orFollowUp(s, i, fmt.Sprintf("🎬 Video generation queued with `%s` (job `%s`). I’ll post the video when it’s ready.", model, job.ID))
	deadline := time.Now().Add(8 * time.Minute)
	for time.Now().Before(deadline) {
		time.Sleep(30 * time.Second)
		pollReq, err := orRequest(http.MethodGet, "/videos/"+url.PathEscape(job.ID), nil, nil)
		if err != nil {
			orFollowUp(s, i, "❌ Could not create video status request: "+err.Error())
			return
		}
		pollBody, pollStatus, err := orDo(pollReq)
		if err != nil {
			orFollowUp(s, i, "❌ Could not check video status: "+err.Error())
			return
		}
		if pollStatus != http.StatusOK {
			orFollowUp(s, i, "❌ Could not check video status: "+orAPIError(pollBody, pollStatus))
			return
		}

		var status struct {
			Status       string   `json:"status"`
			Error        string   `json:"error"`
			UnsignedURLs []string `json:"unsigned_urls"`
			Usage        struct {
				Cost flexFloat `json:"cost"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(pollBody, &status); err != nil {
			orFollowUp(s, i, "❌ Could not parse OpenRouter video status response.")
			return
		}
		switch status.Status {
		case "failed":
			reason := strings.TrimSpace(status.Error)
			if reason == "" {
				reason = "OpenRouter reported that video generation failed."
			}
			orFollowUp(s, i, "❌ "+reason)
			return
		case "completed":
			if len(status.UnsignedURLs) == 0 {
				orFollowUp(s, i, "❌ Video generation completed without a downloadable video.")
				return
			}
			video, err := orDownloadVideo(job.ID)
			if err != nil {
				orFollowUp(s, i, "❌ Video is ready, but I couldn’t download it: "+err.Error())
				return
			}
			costLine := ""
			if float64(status.Usage.Cost) > 0 {
				costLine = fmt.Sprintf(" · $%.4f", float64(status.Usage.Cost))
			}
			_, err = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
				Content: fmt.Sprintf("✅ **`%s`** video generated%s", model, costLine),
				Files: []*discordgo.File{{
					Name:   "openrouter_video.mp4",
					Reader: bytes.NewReader(video),
				}},
			})
			if err != nil {
				log.Printf("openrouter video: failed to send generated video: %v", err)
			}
			return
		}
	}

	orFollowUp(s, i, fmt.Sprintf("⏳ Video job `%s` is still processing. Check back later; generation can take several minutes.", job.ID))
}

func orDownloadVideo(jobID string) ([]byte, error) {
	req, err := orRequest(http.MethodGet, "/videos/"+url.PathEscape(jobID)+"/content?index=0", nil, nil)
	if err != nil {
		return nil, err
	}
	resp, err := (&http.Client{Timeout: orHTTPTimeout}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if readErr != nil {
			return nil, readErr
		}
		return nil, fmt.Errorf("%s", orAPIError(body, resp.StatusCode))
	}

	const maxVideoBytes = 24 << 20
	video, err := io.ReadAll(io.LimitReader(resp.Body, maxVideoBytes+1))
	if err != nil {
		return nil, err
	}
	if len(video) > maxVideoBytes {
		return nil, fmt.Errorf("download exceeds the bot’s 24 MiB attachment limit")
	}
	return video, nil
}

func orHandleTTS(s *discordgo.Session, i *discordgo.InteractionCreate, sub *discordgo.ApplicationCommandInteractionDataOption) {
	orDefer(s, i)
	if orAPIKey() == "" {
		orFollowUp(s, i, "❌ No OpenRouter API key configured.")
		return
	}
	text := strings.TrimSpace(orOptStr(sub, "text"))
	if text == "" {
		orFollowUp(s, i, "❌ Give text. Usage: `/openrouter tts text:<words> [model] [voice]`")
		return
	}
	model := strings.TrimSpace(orOptStr(sub, "model"))
	if model == "" {
		model = "hexgrad/kokoro-82m"
	}
	voice := strings.TrimSpace(orOptStr(sub, "voice"))
	if voice == "" {
		voice = "af_bella"
	}

	resp, err := orFetchTTS(model, text, voice, "mp3", 0)
	if err != nil {
		orFollowUp(s, i, "❌ "+err.Error())
		return
	}
	if len(resp.Data) == 0 {
		orFollowUp(s, i, "❌ Empty audio response from OpenRouter.")
		return
	}

	fileName := "speech.mp3"
	if strings.Contains(resp.ContentType, "wav") {
		fileName = "speech.wav"
	}

	_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
		Content: fmt.Sprintf("**TTS** — `%s` (voice: `%s`)", model, voice),
		Files: []*discordgo.File{
			{Name: fileName, Reader: bytes.NewReader(resp.Data)},
		},
	})
}

func orHandleSTT(s *discordgo.Session, i *discordgo.InteractionCreate, sub *discordgo.ApplicationCommandInteractionDataOption) {
	orDefer(s, i)
	if orAPIKey() == "" {
		orFollowUp(s, i, "❌ No OpenRouter API key configured.")
		return
	}
	model := strings.TrimSpace(orOptStr(sub, "model"))
	if model == "" {
		model = "openai/whisper-large-v3"
	}
	language := strings.TrimSpace(orOptStr(sub, "language"))

	var audioBytes []byte
	var audioFmt string = "mp3"

	// 1. Check if user provided audio_url
	audioURL := strings.TrimSpace(orOptStr(sub, "audio_url"))

	// 2. Check if user passed an attachment
	if audioURL == "" {
		for _, o := range sub.Options {
			if o.Type == discordgo.ApplicationCommandOptionAttachment {
				if attID, ok := o.Value.(string); ok && i.ApplicationCommandData().Resolved != nil {
					if att, exists := i.ApplicationCommandData().Resolved.Attachments[attID]; exists {
						audioURL = att.URL
					}
				}
			}
		}
	}

	if audioURL == "" {
		orFollowUp(s, i, "**OpenRouter — Speech-to-Text (STT)**\n\n"+
			"Transcribe audio using Whisper:\n"+
			"• `/openrouter stt audio:<file> [model]` — upload an audio attachment directly\n"+
			"• `/openrouter stt audio_url:<url> [model]` — transcribe audio from a direct URL\n\n"+
			fmt.Sprintf("_Default model: `%s`_", model))
		return
	}

	// Download audio bytes
	httpClient := &http.Client{Timeout: 30 * time.Second}
	dlResp, dlErr := httpClient.Get(audioURL)
	if dlErr != nil {
		orFollowUp(s, i, "❌ Failed to download audio: "+dlErr.Error())
		return
	}
	defer dlResp.Body.Close()
	data, rErr := io.ReadAll(io.LimitReader(dlResp.Body, 20<<20))
	if rErr != nil || len(data) == 0 {
		orFollowUp(s, i, "❌ Could not read audio content.")
		return
	}
	audioBytes = data
	if strings.Contains(strings.ToLower(audioURL), ".wav") {
		audioFmt = "wav"
	} else if strings.Contains(strings.ToLower(audioURL), ".m4a") {
		audioFmt = "m4a"
	}

	b64Data := base64.StdEncoding.EncodeToString(audioBytes)
	sttResp, err := orFetchSTT(model, b64Data, audioFmt, language)
	if err != nil {
		orFollowUp(s, i, "❌ "+err.Error())
		return
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("**Transcription** — `%s`\n\n", model))
	b.WriteString(sttResp.Text + "\n\n")
	if sttResp.Usage.Cost > 0 || sttResp.Usage.Seconds > 0 {
		b.WriteString(fmt.Sprintf("_Audio duration: %.1fs · Cost: $%.6f_", sttResp.Usage.Seconds, sttResp.Usage.Cost))
	}
	orFollowUp(s, i, b.String())
}

// ---------- Tier 2: bench ----------

func orHandleBench(s *discordgo.Session, i *discordgo.InteractionCreate, sub *discordgo.ApplicationCommandInteractionDataOption) {
	orDefer(s, i)
	benchmarks, err := orFetchBenchmarks()
	if err != nil {
		orFollowUp(s, i, "❌ "+err.Error())
		return
	}
	if len(benchmarks) == 0 {
		orFollowUp(s, i, "No benchmarks available.")
		return
	}

	filterSlug := strings.ToLower(strings.TrimSpace(orOptStr(sub, "model")))

	var b strings.Builder
	if filterSlug != "" {
		b.WriteString(fmt.Sprintf("**OpenRouter Benchmarks for `%s`**\n\n", filterSlug))
		found := 0
		for _, bm := range benchmarks {
			if strings.Contains(strings.ToLower(bm.ModelPermaslug), filterSlug) || strings.Contains(strings.ToLower(bm.DisplayName), filterSlug) {
				found++
				intel := "—"
				if bm.IntelligenceIndex != nil {
					intel = fmt.Sprintf("%.1f", *bm.IntelligenceIndex)
				}
				coding := "—"
				if bm.CodingIndex != nil {
					coding = fmt.Sprintf("%.1f", *bm.CodingIndex)
				}
				agentic := "—"
				if bm.AgenticIndex != nil {
					agentic = fmt.Sprintf("%.1f", *bm.AgenticIndex)
				}
				b.WriteString(fmt.Sprintf("• **%s** (`%s`)\n  Intelligence: **%s** · Coding: **%s** · Agentic: **%s** · Price: %s\n",
					bm.DisplayName, bm.Source, intel, coding, agentic, orPricePair(bm.Pricing)))
				if found >= 10 {
					break
				}
			}
		}
		if found == 0 {
			b.WriteString("No benchmark entries found for that model slug.\n")
		}
		orFollowUp(s, i, b.String())
		return
	}

	// Sort by Intelligence Index descending
	var validIntel []orBenchmarkEntry
	for _, bm := range benchmarks {
		if bm.IntelligenceIndex != nil {
			validIntel = append(validIntel, bm)
		}
	}
	sort.Slice(validIntel, func(idxA, idxB int) bool {
		return *validIntel[idxA].IntelligenceIndex > *validIntel[idxB].IntelligenceIndex
	})

	if len(validIntel) > 10 {
		validIntel = validIntel[:10]
	}

	b.WriteString("**OpenRouter — Top Models by Intelligence Benchmark**\n\n")
	for idx, bm := range validIntel {
		intel := fmt.Sprintf("%.1f", *bm.IntelligenceIndex)
		coding := "—"
		if bm.CodingIndex != nil {
			coding = fmt.Sprintf("%.1f", *bm.CodingIndex)
		}
		agentic := "—"
		if bm.AgenticIndex != nil {
			agentic = fmt.Sprintf("%.1f", *bm.AgenticIndex)
		}
		b.WriteString(fmt.Sprintf("**%d. %s** (`%s`)\n   Intelligence: **%s** · Coding: **%s** · Agentic: **%s** · %s\n",
			idx+1, bm.DisplayName, bm.ModelPermaslug, intel, coding, agentic, orPricePair(bm.Pricing)))
	}
	b.WriteString("\n_Use `/openrouter bench model:<slug>` to query specific model benchmark scores._")
	orFollowUp(s, i, b.String())
}

// ---------- Tier 3: management-key commands (budget, analytics, guardrails, keys, batch) ----------

func orHandleBudget(s *discordgo.Session, i *discordgo.InteractionCreate, _ *discordgo.ApplicationCommandInteractionDataOption) {
	orDefer(s, i)
	if orAPIKey() == "" {
		orFollowUp(s, i, "\u274c No OpenRouter API key configured.")
		return
	}
	credits, err := orFetchCredits()
	if err != nil {
		orFollowUp(s, i, "\u274c "+err.Error()+"\n\n_This may need a management key. Use `/openrouter credits` for the basic key endpoint._")
		return
	}
	total := float64(credits.TotalCredits)
	used := float64(credits.TotalUsage)
	pct := 0.0
	if total > 0 {
		pct = (used / total) * 100
	}
	var b strings.Builder
	b.WriteString("**OpenRouter \u2014 Budget & Credits**\n\n")
	b.WriteString(fmt.Sprintf("Total purchased:   **$%.2f**\n", total))
	b.WriteString(fmt.Sprintf("Total used:       **$%.2f** (%.1f%%)\n", used, pct))
	b.WriteString(fmt.Sprintf("Remaining:        **$%.2f**\n", total-used))
	b.WriteString("\n_Use `/openrouter keys` to list API keys and their individual limits._")
	orFollowUp(s, i, b.String())
}

func orHandleAnalytics(s *discordgo.Session, i *discordgo.InteractionCreate, sub *discordgo.ApplicationCommandInteractionDataOption) {
	orDefer(s, i)
	if orMgmtAPIKey() == "" {
		orFollowUp(s, i, "\u274c Analytics requires an OpenRouter management key configured in `.private/config.json` (`openrouter.apiManagementKey`).")
		return
	}
	days := 3
	if v, ok := orOptInt(sub, "days"); ok && v > 0 && v <= 90 {
		days = int(v)
	}
	data, err := orFetchAnalytics(newORAnalyticsQuery(time.Now(), days))
	if err != nil {
		orFollowUp(s, i, "\u274c "+err.Error())
		return
	}
	formatted, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		orFollowUp(s, i, "\u274c Could not format analytics results: "+err.Error())
		return
	}
	if len(formatted) == 0 || string(formatted) == "null" {
		orFollowUp(s, i, fmt.Sprintf("No analytics data returned for the last %d days.", days))
		return
	}
	orFollowUp(s, i, fmt.Sprintf("**OpenRouter \u2014 Analytics** (last %d days)\n```json\n%s\n```",
		days, truncateString(string(formatted), 1700)))
}

func orHandleGuardrails(s *discordgo.Session, i *discordgo.InteractionCreate, _ *discordgo.ApplicationCommandInteractionDataOption) {
	orDefer(s, i)
	if orAPIKey() == "" {
		orFollowUp(s, i, "\u274c No OpenRouter API key configured.")
		return
	}
	rails, err := orFetchGuardrails()
	if err != nil {
		orFollowUp(s, i, "\u274c "+err.Error()+"\n\n_Requires a management key. See https://openrouter.ai/settings/api-keys_")
		return
	}
	if len(rails) == 0 {
		orFollowUp(s, i, "No guardrails configured in your workspace.\n\nCreate them at https://openrouter.ai/workspaces")
		return
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("**OpenRouter \u2014 Guardrails** (%d)\n\n", len(rails)))
	for _, g := range rails {
		providers := orOrDash(strings.Join(g.AllowedProviders, ", "))
		limitLine := ""
		if g.LimitUsd > 0 {
			limitLine = fmt.Sprintf(" \u2022 $%.0f/%s", g.LimitUsd, orOrDash(g.ResetInterval))
		}
		zdr := ""
		if g.EnforceZdr {
			zdr = " \u2022 ZDR"
		}
		b.WriteString(fmt.Sprintf("\u2022 **%s**%s%s\n  Providers: %s\n", g.Name, limitLine, zdr, providers))
	}
	orFollowUp(s, i, b.String())
}

func orHandleKeys(s *discordgo.Session, i *discordgo.InteractionCreate, _ *discordgo.ApplicationCommandInteractionDataOption) {
	orDefer(s, i)
	if orAPIKey() == "" {
		orFollowUp(s, i, "\u274c No OpenRouter API key configured.")
		return
	}
	items, err := orFetchKeys()
	if err != nil {
		orFollowUp(s, i, "\u274c "+err.Error()+"\n\n_Requires a management key. See https://openrouter.ai/settings/api-keys_")
		return
	}
	if len(items) == 0 {
		orFollowUp(s, i, "No API keys found in your workspace.")
		return
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("**OpenRouter \u2014 API Keys** (%d)\n\n", len(items)))
	for _, k := range items {
		status := "\u2705"
		if k.Disabled {
			status = "\u274c"
		}
		limitStr := "unlimited"
		if k.Limit > 0 {
			limitStr = fmt.Sprintf("$%.2f/%s", k.Limit, orOrDash(k.LimitReset))
		}
		remaining := ""
		if k.LimitRemaining > 0 {
			remaining = fmt.Sprintf(" \u2022 $%.2f remaining", k.LimitRemaining)
		}
		b.WriteString(fmt.Sprintf("%s **%s** \u2014 %s%s\n  Usage: $%.2f/mo%s\n", status, orOrDash(k.Label), limitStr, remaining, k.UsageMonthly, orOrDash(k.Name)))
	}
	b.WriteString("\n_Only the key hash is stored; full keys are shown once on creation._")
	orFollowUp(s, i, b.String())
}

func orHandleBatch(s *discordgo.Session, i *discordgo.InteractionCreate, sub *discordgo.ApplicationCommandInteractionDataOption) {
	orDefer(s, i)
	if orAPIKey() == "" {
		orFollowUp(s, i, "\u274c No OpenRouter API key configured.")
		return
	}

	statusFilter := strings.TrimSpace(orOptStr(sub, "status"))
	batches, err := orFetchBatches(statusFilter)
	if err != nil {
		orFollowUp(s, i, "\u274c "+err.Error()+"\n\n_Batch API may need a management key._")
		return
	}
	if len(batches) == 0 {
		var suffix string
		if statusFilter != "" {
			suffix = fmt.Sprintf(" (status: %s)", statusFilter)
		}
		orFollowUp(s, i, "No batches found."+suffix)
		return
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("**OpenRouter \u2014 Batches** (%d)\n\n", len(batches)))
	for _, bt := range batches {
		emoji := ""
		switch bt.Status {
		case "completed":
			emoji = "\u2705"
		case "failed":
			emoji = "\u274c"
		case "validating":
			emoji = "\u23f3"
		case "in_progress":
			emoji = "\u0031\ufe0f\u20e3"
		default:
			emoji = "\u2753"
		}
		createdStr := ""
		if bt.CreatedAt > 0 {
			t := time.Unix(bt.CreatedAt, 0)
			createdStr = fmt.Sprintf(" \u2022 <t:%d:R>", t.Unix())
		}
		b.WriteString(fmt.Sprintf("%s `%s` \u2014 %s%s\n  %s \u2022 %d/%d/%d\n",
			emoji, bt.ID, bt.Status, createdStr,
			orOrDash(bt.Model), bt.RequestCounts.Completed, bt.RequestCounts.Failed, bt.RequestCounts.Total))
	}
	b.WriteString("\n_Use `/openrouter batch status:completed` to filter by status._")
	orFollowUp(s, i, b.String())
}

// ---------- pagination helpers ----------

// orPaginationButtons returns prev / next buttons for a paginated list.
// The custom_id format is "or:page:<type>:<page>" for prev and "or:npage:<type>:<page>" for next.
func orPaginationButtons(page, totalPages int, pageType string) []discordgo.MessageComponent {
	var buttons []discordgo.MessageComponent
	if page > 0 {
		buttons = append(buttons, discordgo.Button{
			CustomID: fmt.Sprintf("or:page:%s:%d", pageType, page),
			Label:    "\u25c0 Prev",
			Style:    discordgo.SecondaryButton,
		})
	}
	if page+1 < totalPages {
		label := "Next \u25b6"
		if page == 0 && totalPages == 1 {
			label = ""
		}
		if label != "" {
			buttons = append(buttons, discordgo.Button{
				CustomID: fmt.Sprintf("or:npage:%s:%d", pageType, page),
				Label:    label,
				Style:    discordgo.SecondaryButton,
			})
		}
	}
	if len(buttons) > 0 {
		return []discordgo.MessageComponent{
			discordgo.ActionsRow{Components: buttons},
		}
	}
	return nil
}

// orHandleComponent handles button interactions with "or:" custom IDs.
func orHandleComponent(s *discordgo.Session, i *discordgo.InteractionCreate) {
	data := i.MessageComponentData()
	parts := strings.Split(data.CustomID, ":")
	if len(parts) < 4 {
		return
	}
	dir := parts[1]      // "page" for prev, "npage" for next
	pageType := parts[2] // "help", "providers", etc.
	curPage, err := strconv.ParseInt(parts[3], 10, 64)
	if err != nil {
		return
	}

	newPage := curPage
	if dir == "page" {
		newPage = curPage - 1
	} else if dir == "npage" {
		newPage = curPage + 1
	}
	if newPage < 0 {
		newPage = 0
	}

	switch pageType {
	case "help":
		orShowHelpPage(s, i, int(newPage))
	case "providers":
		orShowProvidersPage(s, i, int(newPage))
	}
}

// orShowHelpPage sends one page of help text.
func orShowHelpPage(s *discordgo.Session, i *discordgo.InteractionCreate, page int) {
	pages := orHelpPages()
	if page < 0 || page >= len(pages) {
		return
	}
	content := pages[page]
	buttons := orPaginationButtons(page, len(pages), "help")

	// If we can update in-place (button click), use UpdateMessage
	if i.Type == discordgo.InteractionMessageComponent {
		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseUpdateMessage,
			Data: &discordgo.InteractionResponseData{
				Content:    content,
				Components: buttons,
			},
		})
	} else {
		// First call from help command
		if buttons != nil {
			_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content:    content,
					Components: buttons,
				},
			})
		} else {
			orReply(s, i, content)
		}
	}
}

// orHelpPages returns the help text split into pages.
func orHelpPages() []string {
	return []string{
		"**OpenRouter — `/openrouter`**\n" +
			"Browse the catalog, check live prices, audit spend, and route across models.\n" +
			"_Powered by https://openrouter.ai_\n\n" +
			"**Catalog & Discovery**\n" +
			"• `/openrouter models [query] [sort] [modality] [max_price] [limit]`\n" +
			"  Search models with sorting, modalities, price caps.\n" +
			"• `/openrouter model slug:<id>`\n" +
			"  Full specs for one model (context, pricing, parameters, modalities).\n" +
			"• `/openrouter compare model_a:<id> model_b:<id>`\n" +
			"  Side-by-side model comparison.\n" +
			"• `/openrouter deals [limit]`\n" +
			"  Discounted models, ranked by discount percentage.\n" +
			"• `/openrouter endpoints slug:<id>`\n" +
			"  Upstream providers serving a model, with pricing, uptime, latency.\n" +
			"• `/openrouter price slug:<id> [in_tokens] [out_tokens]`\n" +
			"  Price lookup + cost estimator.\n" +
			"• `/openrouter bench [model]`\n" +
			"  Leaderboard & scores (intelligence, coding, agentic) from Artificial Analysis.",

		"**Chat & Intelligent Routing**\n" +
			"• `/openrouter ask prompt:<text> [model] [max_tokens] [temperature]`\n" +
			"  Query any model with real cost/token tracking.\n" +
			"• `/openrouter free prompt:<text>`\n" +
			"  Chat via the free models router (`openrouter/free`).\n" +
			"• `/openrouter online prompt:<text> [model]`\n" +
			"  Live web search–augmented answers (`:online`).\n" +
			"• `/openrouter think prompt:<text> [model] [reasoning_tokens]`\n" +
			"  Reasoning/thinking models with reasoning token budget.\n" +
			"• `/openrouter route [prompt] [sort] [model]`\n" +
			"  Route by provider preference (`price`, `latency`, `throughput`).\n" +
			"• `/openrouter fallback [prompt] [models]`\n" +
			"  Failover array across multiple backup models.\n" +
			"• `/openrouter fusion prompt:<text>`\n" +
			"  Multi-model deliberation (`openrouter/fusion`).\n" +
			"• `/openrouter tools`\n" +
			"  Catalog models supporting function/tool calling.",

		"**Retrieval & Embeddings**\n" +
			"• `/openrouter embed input:<text> [model] [dimensions]`\n" +
			"  Generate vector embeddings with preview.\n" +
			"• `/openrouter similar text_a:<text> text_b:<text> [model]`\n" +
			"  Compute cosine semantic similarity between two texts.\n" +
			"• `/openrouter rerank query:<text> documents:<doc1|doc2|...> [model]`\n" +
			"  Rerank documents by semantic relevance score.",

		"**Multimodal Media & Audio**\n" +
			"• `/openrouter image prompt:<text> [model]`\n" +
			"  Generate images (ByteDance Seedream, Flux, etc.).\n" +
			"• `/openrouter video [prompt] [model]`\n" +
			"  Video generation and catalog models (Luma Ray 2, etc.).\n" +
			"• `/openrouter tts text:<text> [model] [voice]`\n" +
			"  Text-to-speech audio (.mp3 attachment via Kokoro-82M).\n" +
			"• `/openrouter stt [audio] [audio_url] [model]`\n" +
			"  Transcribe voice/audio files via Whisper-large-v3.",

		"**Account, Keys & Spend**\n" +
			"• `/openrouter key`\n" +
			"  Spend limits, free model quota, and key details.\n" +
			"• `/openrouter analytics [days]`\n" +
			"  Request counts by model (requires a management key).\n" +
			"• `/openrouter usage`\n" +
			"  Spend dashboard: today, week, month, lifetime, BYOK.\n" +
			"• `/openrouter gen id:<gen-...>`\n" +
			"  Audit a single request's real cost, latency, tokens, cache discounts.\n" +
			"• `/openrouter help`\n" +
			"  Interactive guide for all /openrouter commands.\n\n" +
			"_Router slugs: `openrouter/auto`, `openrouter/free`, `openrouter/fusion`_\n" +
			"_Docs: https://openrouter.ai/docs_",
	}
}

func orShowProvidersPage(s *discordgo.Session, i *discordgo.InteractionCreate, page int) {
	req, err := orRequest(http.MethodGet, "/providers", nil, nil)
	if err != nil {
		orFollowUp(s, i, "\u274c "+err.Error())
		return
	}
	body, status, err := orDo(req)
	if err != nil {
		orFollowUp(s, i, "\u274c "+err.Error())
		return
	}
	if status != http.StatusOK {
		orFollowUp(s, i, "\u274c "+orAPIError(body, status))
		return
	}
	var resp struct {
		Data []orProvider `json:"data"`
	}
	if json.Unmarshal(body, &resp) != nil || len(resp.Data) == 0 {
		orFollowUp(s, i, "No providers returned.")
		return
	}

	names := make([]string, 0, len(resp.Data))
	for _, p := range resp.Data {
		label := p.Name
		if p.Slug != "" {
			label += " (`" + p.Slug + "`)"
		}
		names = append(names, label)
	}
	sort.Strings(names)
	perPage := 20
	totalPages := (len(names) + perPage - 1) / perPage
	if page >= totalPages {
		page = totalPages - 1
	}
	if page < 0 {
		page = 0
	}

	start := page * perPage
	end := start + perPage
	if end > len(names) {
		end = len(names)
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("**OpenRouter upstream providers** (page %d/%d, showing %d-%d of %d)\n\n", page+1, totalPages, start+1, end, len(names)))
	for _, n := range names[start:end] {
		b.WriteString("\u2022 " + n + "\n")
	}
	buttons := orPaginationButtons(page, totalPages, "providers")

	if i.Type == discordgo.InteractionMessageComponent {
		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseUpdateMessage,
			Data: &discordgo.InteractionResponseData{
				Content:    clipMessage(b.String()),
				Components: buttons,
			},
		})
	} else {
		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content:    clipMessage(b.String()),
				Components: buttons,
			},
		})
	}
}

func openRouterHelpText() string {
	return "**OpenRouter — `/openrouter`**\n" +
		"Use the arrows below to flip through help pages, or use specific subcommand help.\n" +
		"_Powered by https://openrouter.ai/docs_"
}

// ---------- small local helpers ----------

func orOrDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}

func orThousands(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	var b strings.Builder
	for idx, c := range s {
		if idx > 0 && (len(s)-idx)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

// ---------- dispatcher + command spec ----------

func handleOpenRouterCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	data := i.ApplicationCommandData()
	if len(data.Options) == 0 {
		orShowHelpPage(s, i, 0)
		return
	}
	sub := data.Options[0]
	switch sub.Name {
	case "models":
		orHandleModels(s, i, sub)
	case "deals":
		orHandleDeals(s, i, sub)
	case "model":
		orHandleModel(s, i, sub)
	case "compare":
		orHandleCompare(s, i, sub)
	case "endpoints":
		orHandleEndpoints(s, i, sub)
	case "providers":
		orShowProvidersPage(s, i, 0)
	case "price":
		orHandlePrice(s, i, sub)
	case "key", "credits":
		orHandleCredits(s, i, sub)
	case "gen":
		orHandleGen(s, i, sub)
	case "history":
		orHandleHistory(s, i, sub)
	case "usage":
		orHandleUsage(s, i, sub)
	case "ask", "chat":
		orHandleAsk(s, i, sub)
	case "help":
		orShowHelpPage(s, i, 0)
	// Routing: route · fallback · free · online · think · fusion · tools
	case "route":
		orHandleRoute(s, i, sub)
	case "fallback":
		orHandleFallback(s, i, sub)
	case "free":
		orHandleFree(s, i, sub)
	case "online":
		orHandleOnline(s, i, sub)
	case "think":
		orHandleThink(s, i, sub)
	case "fusion":
		orHandleFusion(s, i, sub)
	case "tools":
		orHandleTools(s, i, sub)
	// Retrieval: embed · similar · rerank
	case "embed":
		orHandleEmbed(s, i, sub)
	case "similar":
		orHandleSimilar(s, i, sub)
	case "rerank":
		orHandleRerank(s, i, sub)
	// Media: image · video · tts · stt
	case "image":
		orHandleImage(s, i, sub)
	case "video":
		orHandleVideo(s, i, sub)
	case "tts":
		orHandleTTS(s, i, sub)
	case "stt", "transcribe":
		orHandleSTT(s, i, sub)
	// Benchmark: bench
	case "bench":
		orHandleBench(s, i, sub)
	// Tier 3 — management (needs management key)
	case "budget":
		orHandleBudget(s, i, sub)
	case "analytics":
		orHandleAnalytics(s, i, sub)
	case "guardrails":
		orHandleGuardrails(s, i, sub)
	case "keys":
		orHandleKeys(s, i, sub)
	case "batch":
		orHandleBatch(s, i, sub)
	default:
		orReply(s, i, "Unknown /openrouter subcommand. Try `/openrouter help`.")
	}
}

// openRouterCommandSpec returns the /openrouter command definition + handler,
// ready to drop into commandSpecs in commands.go.
// Note: Discord allows a maximum of 25 subcommands/options at the root level.
func openRouterCommandSpec() commandSpec {
	return commandSpec{
		Command: &discordgo.ApplicationCommand{
			Name:        "openrouter",
			Description: "OpenRouter: browse models, prices, routing, retrieval, media, and benchmarks.",
			Options: []*discordgo.ApplicationCommandOption{
				// Core Catalog (6)
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "models",
					Description: "Search the OpenRouter model catalog.",
					Options: []*discordgo.ApplicationCommandOption{
						{Type: discordgo.ApplicationCommandOptionString, Name: "query", Description: "Keyword search (e.g. 'gemini', 'deepseek')", Required: false},
						{
							Type:        discordgo.ApplicationCommandOptionString,
							Name:        "sort",
							Description: "Sort order",
							Required:    false,
							Choices: []*discordgo.ApplicationCommandOptionChoice{
								{Name: "Most popular", Value: "most-popular"},
								{Name: "Newest", Value: "newest"},
								{Name: "Price: low to high", Value: "pricing-low-to-high"},
								{Name: "Price: high to low", Value: "pricing-high-to-low"},
								{Name: "Context: high to low", Value: "context-high-to-low"},
							},
						},
						{
							Type:        discordgo.ApplicationCommandOptionString,
							Name:        "modality",
							Description: "Filter by output modality",
							Required:    false,
							Choices: []*discordgo.ApplicationCommandOptionChoice{
								{Name: "Text", Value: "text"},
								{Name: "Image", Value: "image"},
								{Name: "Audio", Value: "audio"},
								{Name: "Video", Value: "video"},
								{Name: "Embeddings", Value: "embeddings"},
							},
						},
						{Type: discordgo.ApplicationCommandOptionNumber, Name: "max_price", Description: "Max prompt price in USD per million tokens", Required: false},
						{Type: discordgo.ApplicationCommandOptionInteger, Name: "limit", Description: "How many models to show (max 20)", Required: false},
					},
				},
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "deals",
					Description: "Show currently discounted models, ranked by discount.",
					Options: []*discordgo.ApplicationCommandOption{
						{Type: discordgo.ApplicationCommandOptionInteger, Name: "limit", Description: "How many to show (max 20)", Required: false},
					},
				},
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "model",
					Description: "Full details for one model.",
					Options: []*discordgo.ApplicationCommandOption{
						{Type: discordgo.ApplicationCommandOptionString, Name: "slug", Description: "Model id, e.g. openai/gpt-5.6-sol", Required: true},
					},
				},
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "compare",
					Description: "Compare two models side by side.",
					Options: []*discordgo.ApplicationCommandOption{
						{Type: discordgo.ApplicationCommandOptionString, Name: "model_a", Description: "First model id", Required: true},
						{Type: discordgo.ApplicationCommandOptionString, Name: "model_b", Description: "Second model id", Required: true},
					},
				},
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "endpoints",
					Description: "List providers serving a model, with per-endpoint pricing and latency.",
					Options: []*discordgo.ApplicationCommandOption{
						{Type: discordgo.ApplicationCommandOptionString, Name: "slug", Description: "Model id", Required: true},
					},
				},
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "providers",
					Description: "List all upstream OpenRouter providers.",
				},
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "price",
					Description: "Show a model's price and estimate a cost.",
					Options: []*discordgo.ApplicationCommandOption{
						{Type: discordgo.ApplicationCommandOptionString, Name: "slug", Description: "Model id", Required: true},
						{Type: discordgo.ApplicationCommandOptionInteger, Name: "in_tokens", Description: "Input tokens to estimate (default 1000)", Required: false},
						{Type: discordgo.ApplicationCommandOptionInteger, Name: "out_tokens", Description: "Output tokens to estimate (default 500)", Required: false},
					},
				},

				// Account & Tracking (5)
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "key",
					Description: "Show live usage, limits, and free-model quota for your API key.",
				},
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "gen",
					Description: "Audit one generation's cost and latency by id.",
					Options: []*discordgo.ApplicationCommandOption{
						{Type: discordgo.ApplicationCommandOptionString, Name: "id", Description: "Generation id, e.g. gen-1234567890", Required: true},
					},
				},
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "analytics",
					Description: "Show request counts by model for recent days.",
					Options: []*discordgo.ApplicationCommandOption{
						{Type: discordgo.ApplicationCommandOptionInteger, Name: "days", Description: "Number of previous days to include (1-90, default 3)", Required: false},
					},
				},
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "usage",
					Description: "Spend dashboard: today, week, month, lifetime.",
				},
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "ask",
					Description: "Ask any OpenRouter model; the reply shows the real cost.",
					Options: []*discordgo.ApplicationCommandOption{
						{Type: discordgo.ApplicationCommandOptionString, Name: "prompt", Description: "Your prompt", Required: true},
						{Type: discordgo.ApplicationCommandOptionString, Name: "model", Description: "Model id (default: openrouter/auto)", Required: false},
						{Type: discordgo.ApplicationCommandOptionInteger, Name: "max_tokens", Description: "Max output tokens (default 1024)", Required: false},
						{Type: discordgo.ApplicationCommandOptionNumber, Name: "temperature", Description: "Sampling temperature (0-2)", Required: false},
					},
				},
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "help",
					Description: "Show the /openrouter interactive usage guide.",
				},

				// Routing (7): route · fallback · free · online · think · fusion · tools
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "route",
					Description: "Route prompt across providers with sort preferences (price/latency/throughput).",
					Options: []*discordgo.ApplicationCommandOption{
						{Type: discordgo.ApplicationCommandOptionString, Name: "prompt", Description: "Your prompt", Required: false},
						{
							Type:        discordgo.ApplicationCommandOptionString,
							Name:        "sort",
							Description: "Provider priority sort",
							Required:    false,
							Choices: []*discordgo.ApplicationCommandOptionChoice{
								{Name: "Lowest price", Value: "price"},
								{Name: "Lowest latency", Value: "latency"},
								{Name: "Highest throughput", Value: "throughput"},
							},
						},
						{Type: discordgo.ApplicationCommandOptionString, Name: "model", Description: "Model id (default: openrouter/auto)", Required: false},
					},
				},
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "fallback",
					Description: "Failover prompt across an ordered list of backup models.",
					Options: []*discordgo.ApplicationCommandOption{
						{Type: discordgo.ApplicationCommandOptionString, Name: "prompt", Description: "Your prompt", Required: false},
						{Type: discordgo.ApplicationCommandOptionString, Name: "models", Description: "Comma-separated model IDs in order of priority", Required: false},
					},
				},
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "free",
					Description: "Chat via the zero-cost models router (openrouter/free).",
					Options: []*discordgo.ApplicationCommandOption{
						{Type: discordgo.ApplicationCommandOptionString, Name: "prompt", Description: "Your prompt", Required: true},
						{Type: discordgo.ApplicationCommandOptionInteger, Name: "max_tokens", Description: "Max output tokens (default 1024)", Required: false},
					},
				},
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "online",
					Description: "Chat with web search-augmented answers (:online).",
					Options: []*discordgo.ApplicationCommandOption{
						{Type: discordgo.ApplicationCommandOptionString, Name: "prompt", Description: "Your query", Required: true},
						{Type: discordgo.ApplicationCommandOptionString, Name: "model", Description: "Model id (default: openrouter/auto)", Required: false},
						{Type: discordgo.ApplicationCommandOptionInteger, Name: "max_tokens", Description: "Max output tokens (default 1024)", Required: false},
					},
				},
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "think",
					Description: "Chat with reasoning/thinking models with explicit reasoning budget.",
					Options: []*discordgo.ApplicationCommandOption{
						{Type: discordgo.ApplicationCommandOptionString, Name: "prompt", Description: "Your reasoning prompt", Required: true},
						{Type: discordgo.ApplicationCommandOptionString, Name: "model", Description: "Model id (default: openrouter/auto)", Required: false},
						{Type: discordgo.ApplicationCommandOptionInteger, Name: "reasoning_tokens", Description: "Max reasoning tokens (default 2048)", Required: false},
						{Type: discordgo.ApplicationCommandOptionInteger, Name: "max_tokens", Description: "Max output tokens (default 2048)", Required: false},
					},
				}, {
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "tools",
					Description: "List models that support native function and tool calling.",
				},

				// Retrieval (3): embed · similar · rerank
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "embed",
					Description: "Generate vector embeddings for input text.",
					Options: []*discordgo.ApplicationCommandOption{
						{Type: discordgo.ApplicationCommandOptionString, Name: "input", Description: "Text to generate embedding for", Required: true},
						{Type: discordgo.ApplicationCommandOptionString, Name: "model", Description: "Embedding model (default: openai/text-embedding-3-small)", Required: false},
						{Type: discordgo.ApplicationCommandOptionInteger, Name: "dimensions", Description: "Target dimensions (optional)", Required: false},
					},
				}, {
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "rerank",
					Description: "Rerank candidate documents against a query by relevance score.",
					Options: []*discordgo.ApplicationCommandOption{
						{Type: discordgo.ApplicationCommandOptionString, Name: "query", Description: "Search query", Required: true},
						{Type: discordgo.ApplicationCommandOptionString, Name: "documents", Description: "Candidate documents separated by |", Required: true},
						{Type: discordgo.ApplicationCommandOptionString, Name: "model", Description: "Rerank model (default: cohere/rerank-v3.5)", Required: false},
						{Type: discordgo.ApplicationCommandOptionInteger, Name: "top_n", Description: "Number of top results to return (default 5)", Required: false},
					},
				},

				// Media (4): image · video · tts · stt
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "image",
					Description: "Generate an image via OpenRouter image generation models.",
					Options: []*discordgo.ApplicationCommandOption{
						{Type: discordgo.ApplicationCommandOptionString, Name: "prompt", Description: "Image description prompt", Required: true},
						{Type: discordgo.ApplicationCommandOptionString, Name: "model", Description: "Image model (default: bytedance-seed/seedream-5-0-flash)", Required: false},
					},
				},
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "video",
					Description: "Generate video or browse video models in the catalog.",
					Options: []*discordgo.ApplicationCommandOption{
						{Type: discordgo.ApplicationCommandOptionString, Name: "prompt", Description: "Video description prompt", Required: false},
						{Type: discordgo.ApplicationCommandOptionString, Name: "model", Description: "Video model (default: luma/ray-2)", Required: false},
					},
				},
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "tts",
					Description: "Convert text to speech audio (.mp3 attachment).",
					Options: []*discordgo.ApplicationCommandOption{
						{Type: discordgo.ApplicationCommandOptionString, Name: "text", Description: "Text to speak", Required: true},
						{Type: discordgo.ApplicationCommandOptionString, Name: "model", Description: "TTS model (default: hexgrad/kokoro-82m)", Required: false},
						{Type: discordgo.ApplicationCommandOptionString, Name: "voice", Description: "Voice ID (default: af_bella)", Required: false},
					},
				},
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "stt",
					Description: "Transcribe speech audio file or URL to text.",
					Options: []*discordgo.ApplicationCommandOption{
						{Type: discordgo.ApplicationCommandOptionAttachment, Name: "audio", Description: "Upload audio file attachment", Required: false},
						{Type: discordgo.ApplicationCommandOptionString, Name: "audio_url", Description: "Direct audio URL to transcribe", Required: false},
						{Type: discordgo.ApplicationCommandOptionString, Name: "model", Description: "STT model (default: openai/whisper-large-v3)", Required: false},
						{Type: discordgo.ApplicationCommandOptionString, Name: "language", Description: "Spoken language code (e.g. en, es, fr)", Required: false},
					},
				},
			},
		},
		Handler: handleOpenRouterCommand,
	}
}
