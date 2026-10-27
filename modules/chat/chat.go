// Package chat implements the Venice Chat Completions API
// (POST /chat/completions), an OpenAI-compatible endpoint extended with
// Venice-specific parameters: web search/citations, character personas,
// thinking controls, and streaming.
package chat

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"sunfire/modules"
)

// Message is a single chat message. Content is either a plain string or a
// []Part slice for multimodal content.
type Message struct {
	Role    string `json:"role"` // system, user, assistant, tool
	Content any    `json:"content"`
}

// Part is a multimodal content part (text, image_url, input_audio,
// video_url, or file).
type Part struct {
	Type       string         `json:"type"`
	Text       string         `json:"text,omitempty"`
	ImageURL   *ImageURL      `json:"image_url,omitempty"`
	InputAudio *InputAudio    `json:"input_audio,omitempty"`
	VideoURL   *VideoURL      `json:"video_url,omitempty"`
	File       *FileRef       `json:"file,omitempty"`
	Refusal    string         `json:"refusal,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
	ToolCalls  []ToolCall     `json:"tool_calls,omitempty"`
	Name       string         `json:"name,omitempty"`
	Extra      map[string]any `json:"-"`
}

// ImageURL references an image for multimodal input.
type ImageURL struct {
	URL string `json:"url,omitempty"` // http(s) URL or data: URL
}

// InputAudio references audio bytes for multimodal input.
type InputAudio struct {
	Data   string `json:"data,omitempty"`   // base64
	Format string `json:"format,omitempty"` // wav, mp3, ...
}

// VideoURL references a video for multimodal input.
type VideoURL struct {
	URL string `json:"url,omitempty"`
}

// FileRef references an uploaded file (PDF, DOCX, ...) for multimodal input.
type FileRef struct {
	URL string `json:"url,omitempty"` // file_id:... or data URL
}

// ToolCall is an assistant tool invocation.
type ToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function ToolCallFunction `json:"function"`
}

// ToolCallFunction is the name/arguments of a tool call.
type ToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// Tool is a function declaration for function calling.
type Tool struct {
	Type     string `json:"type"` // "function"
	Function any    `json:"function"`
}

// ResponseFormat requests structured JSON output (json_object or json_schema).
type ResponseFormat struct {
	Type       string `json:"type"`
	JSONSchema any    `json:"json_schema,omitempty"`
}

// VeniceParameters carries the Venice-specific chat options.
type VeniceParameters struct {
	CharacterSlug                  string `json:"character_slug,omitempty"`
	StripThinkingResponse          bool   `json:"strip_thinking_response,omitempty"`
	DisableThinking                bool   `json:"disable_thinking,omitempty"`
	EnableWebSearch                string `json:"enable_web_search,omitempty"` // off | on | auto
	EnableWebScraping              bool   `json:"enable_web_scraping,omitempty"`
	EnableXSearch                  bool   `json:"enable_x_search,omitempty"`
	EnableWebCitations             bool   `json:"enable_web_citations,omitempty"`
	IncludeSearchResultsInStream   bool   `json:"include_search_results_in_stream,omitempty"`
	ReturnSearchResultsAsDocuments bool   `json:"return_search_results_as_documents,omitempty"`
	IncludeVeniceSystemPrompt      *bool  `json:"include_venice_system_prompt,omitempty"`
}

// StreamOptions is the OpenAI-compatible streaming options object.
type StreamOptions struct {
	IncludeUsage bool `json:"include_usage,omitempty"`
}

// Request is a chat completion request.
type Request struct {
	Model               string            `json:"model"`
	Messages            []Message         `json:"messages"`
	MaxTokens           *int              `json:"max_tokens,omitempty"`
	MaxCompletionTokens *int              `json:"max_completion_tokens,omitempty"`
	Temperature         *float64          `json:"temperature,omitempty"`
	TopP                *float64          `json:"top_p,omitempty"`
	FrequencyPenalty    *float64          `json:"frequency_penalty,omitempty"`
	PresencePenalty     *float64          `json:"presence_penalty,omitempty"`
	Stop                []string          `json:"stop,omitempty"`
	Stream              bool              `json:"stream,omitempty"`
	StreamOptions       *StreamOptions    `json:"stream_options,omitempty"`
	Tools               []Tool            `json:"tools,omitempty"`
	ToolChoice          any               `json:"tool_choice,omitempty"`
	ResponseFormat      *ResponseFormat   `json:"response_format,omitempty"`
	Seed                *int              `json:"seed,omitempty"`
	User                string            `json:"user,omitempty"`
	PromptCacheKey      string            `json:"prompt_cache_key,omitempty"`
	VeniceParameters    *VeniceParameters `json:"venice_parameters,omitempty"`
	Extra               map[string]any    `json:"-"`
}

// MarshalJSON merges Extra fields into the standard request body.
func (r Request) MarshalJSON() ([]byte, error) {
	type alias Request
	b, err := json.Marshal(alias(r))
	if err != nil {
		return nil, err
	}
	if len(r.Extra) == 0 {
		return b, nil
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	for k, v := range r.Extra {
		m[k] = v
	}
	return json.Marshal(m)
}

// Usage reports token consumption.
type Usage struct {
	PromptTokens            int          `json:"prompt_tokens"`
	CompletionTokens        int          `json:"completion_tokens"`
	TotalTokens             int          `json:"total_tokens"`
	PromptTokensDetails     *UsageDetail `json:"prompt_tokens_details,omitempty"`
	CompletionTokensDetails *UsageDetail `json:"completion_tokens_details,omitempty"`
}

// UsageDetail breaks usage into cached/reasoning components.
type UsageDetail struct {
	CachedTokens    int `json:"cached_tokens,omitempty"`
	ReasoningTokens int `json:"reasoning_tokens,omitempty"`
}

// Citation is a web-search citation returned in the response.
type Citation struct {
	Index int    `json:"index"`
	URL   string `json:"url"`
}

// Choice is one generated completion.
type Choice struct {
	Index        int     `json:"index"`
	Message      Message `json:"message"`
	FinishReason string  `json:"finish_reason"`
	Delta        Message `json:"delta,omitempty"`
	Logprobs     any     `json:"logprobs,omitempty"`
}

// Response is a non-streaming chat completion.
type Response struct {
	ID      string          `json:"id"`
	Object  string          `json:"object"`
	Created int64           `json:"created"`
	Model   string          `json:"model"`
	Choices []Choice        `json:"choices"`
	Usage   *Usage          `json:"usage,omitempty"`
	Venice  *VeniceResponse `json:"venice,omitempty"`
}

// VeniceResponse carries Venice-specific response metadata.
type VeniceResponse struct {
	SearchResults []SearchResult `json:"search_results,omitempty"`
}

// SearchResult is a web search result surfaced by the chat endpoint.
type SearchResult struct {
	Index   int    `json:"index"`
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
}

// Client is a chat-completions client bound to a Venice API key.
type Client struct {
	*venice.Client
}

// NewClient wraps a *venice.Client for the chat API.
func NewClient(c *venice.Client) *Client { return &Client{Client: c} }

// Create sends a chat completion request and returns the decoded response.
func (c *Client) Create(ctx context.Context, req *Request) (*Response, error) {
	req.Stream = false
	var out Response
	if err := c.DoJSON(ctx, http.MethodPost, "/chat/completions", nil, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CreateRaw sends a request and returns the raw JSON body, for callers that
// want to decode an evolving schema themselves.
func (c *Client) CreateRaw(ctx context.Context, req *Request) ([]byte, error) {
	data, _, _, err := c.DoRaw(ctx, http.MethodPost, "/chat/completions", nil, req)
	return data, err
}

// Stream iterates an SSE stream of chat completion chunks. It stops on
// [io.EOF] after the final [DONE] marker. Each chunk is delivered via next.
// The iterator must be closed to release the underlying connection.
func (c *Client) Stream(ctx context.Context, req *Request) (*StreamIterator, error) {
	req.Stream = true
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	resp, err := c.Send(ctx, http.MethodPost, "/chat/completions", nil, "application/json", body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		defer resp.Body.Close()
		return nil, c.decodeErrorResponse(resp)
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	return &StreamIterator{resp: resp, sc: sc}, nil
}

// decodeErrorResponse reads an error body and returns an error.
func (c *Client) decodeErrorResponse(resp *http.Response) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return &venice.APIError{StatusCode: resp.StatusCode, Status: resp.Status, Raw: raw, Message: strings.TrimSpace(string(raw))}
}

// StreamIterator walks an SSE response from the chat completions endpoint.
type StreamIterator struct {
	resp *http.Response
	sc   *bufio.Scanner
	done bool
}

// Next decodes the next chunk. Returns io.EOF when the stream ends.
func (it *StreamIterator) Next() (*Response, error) {
	if it.done {
		return nil, io.EOF
	}
	for it.sc.Scan() {
		line := strings.TrimSpace(it.sc.Text())
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue // SSE comment / keepalive
		}
		payload := line
		if strings.HasPrefix(line, "data:") {
			payload = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		}
		if payload == "[DONE]" {
			it.done = true
			_ = it.Close()
			return nil, io.EOF
		}
		var chunk Response
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			return nil, fmt.Errorf("venice: decode stream chunk: %w", err)
		}
		if len(chunk.Choices) == 0 && chunk.ID == "" && chunk.Usage == nil {
			continue
		}
		return &chunk, nil
	}
	if err := it.sc.Err(); err != nil {
		it.done = true
		_ = it.Close()
		return nil, err
	}
	it.done = true
	_ = it.Close()
	return nil, io.EOF
}

// Err returns any scanner error encountered (nil until the stream ends).
func (it *StreamIterator) Err() error { return it.sc.Err() }

// Close releases the underlying HTTP connection.
func (it *StreamIterator) Close() error {
	if it.resp != nil {
		err := it.resp.Body.Close()
		it.resp = nil
		return err
	}
	return nil
}

// Accumulate reads an entire stream into a single response by merging the
// deltas of each chunk. If includeUsage is false (or unset) usage is nil.
func (it *StreamIterator) Accumulate() (*Response, error) {
	var out Response
	var sb strings.Builder
	var err error
	var chunk *Response
	for {
		chunk, err = it.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if out.ID == "" {
			out.ID, out.Object, out.Created, out.Model = chunk.ID, chunk.Object, chunk.Created, chunk.Model
		}
		if chunk.Usage != nil {
			out.Usage = chunk.Usage
		}
		if chunk.Venice != nil {
			out.Venice = chunk.Venice
		}
		for _, ch := range chunk.Choices {
			if s, ok := ch.Delta.Content.(string); ok && s != "" {
				sb.WriteString(s)
			}
			out.Choices = append(out.Choices, ch)
		}
	}
	out.Object = "chat.completion"
	if out.Choices == nil {
		out.Choices = []Choice{{Message: Message{Role: "assistant", Content: sb.String()}}}
	} else if sb.Len() > 0 {
		out.Choices[0].Message = Message{Role: "assistant", Content: sb.String()}
		out.Choices[0].FinishReason = "stop"
	}
	return &out, nil
}

// Text returns the first choice's text content, trimming leading/trailing
// whitespace. Returns "" if there is no content.
func (r *Response) Text() string {
	if r == nil || len(r.Choices) == 0 {
		return ""
	}
	if s, ok := r.Choices[0].Message.Content.(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}
