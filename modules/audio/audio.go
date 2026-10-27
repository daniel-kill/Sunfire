// Package audio implements Venice audio endpoints: text-to-speech
// (POST /audio/speech), speech-to-text (POST /audio/transcriptions), and the
// asynchronous music / sound-effect generation lifecycle
// (/audio/quote, /audio/queue, /audio/retrieve, /audio/complete).
package audio

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"sunfire/modules"
)

// SpeechRequest is a text-to-speech request (POST /audio/speech).
type SpeechRequest struct {
	Model          string         `json:"model"`
	Input          string         `json:"input"`
	Voice          string         `json:"voice,omitempty"`
	ResponseFormat string         `json:"response_format,omitempty"` // mp3, opus, aac, flac, wav, pcm
	Speed          *float64       `json:"speed,omitempty"`
	Extra          map[string]any `json:"-"`
}

// MarshalJSON merges Extra fields.
func (r SpeechRequest) MarshalJSON() ([]byte, error) {
	type alias SpeechRequest
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

// TranscriptionRequest is a speech-to-text request (POST /audio/transcriptions).
// The audio is uploaded as multipart form data.
type TranscriptionRequest struct {
	Model          string
	File           venice.MultipartFile
	ResponseFormat string // json (default) or text
	Timestamps     bool
	Language       string
	Prompt         string
	Temperature    *float64
	Extra          map[string]string // extra form fields
}

// Transcription is the structured transcription result (response_format=json).
type Transcription struct {
	Text       string                `json:"text"`
	Duration   float64               `json:"duration,omitempty"`
	Timestamps *TranscriptionTimings `json:"timestamps,omitempty"`
}

// TranscriptionTimings holds word- or segment-level timing data.
type TranscriptionTimings struct {
	Word    []WordTiming    `json:"word,omitempty"`
	Segment []SegmentTiming `json:"segment,omitempty"`
}

// WordTiming is a word-level timestamp.
type WordTiming struct {
	Word  string  `json:"word"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}

// SegmentTiming is a segment-level timestamp.
type SegmentTiming struct {
	Text  string  `json:"text"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}

// QuoteRequest is a music / sound-effect pricing request (POST /audio/quote).
type QuoteRequest struct {
	Model           string         `json:"model"`
	DurationSeconds *int           `json:"duration_seconds,omitempty"`
	Extra           map[string]any `json:"-"`
}

// MarshalJSON merges Extra fields.
func (r QuoteRequest) MarshalJSON() ([]byte, error) {
	type alias QuoteRequest
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

// QueueRequest queues a music or sound-effect generation (POST /audio/queue).
type QueueRequest struct {
	Model             string         `json:"model"`
	Prompt            string         `json:"prompt,omitempty"`
	DurationSeconds   *int           `json:"duration_seconds,omitempty"`
	ForceInstrumental *bool          `json:"force_instrumental,omitempty"`
	LyricsPrompt      string         `json:"lyrics_prompt,omitempty"`
	Loop              *bool          `json:"loop,omitempty"`
	Extra             map[string]any `json:"-"`
}

// MarshalJSON merges Extra fields.
func (r QueueRequest) MarshalJSON() ([]byte, error) {
	type alias QueueRequest
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

// QueueResponse is the result of POST /audio/queue.
type QueueResponse struct {
	Model   string `json:"model"`
	QueueID string `json:"queue_id"`
	Status  string `json:"status"`
}

// JobRef identifies a queued job for retrieve/complete calls.
type JobRef struct {
	Model   string `json:"model"`
	QueueID string `json:"queue_id"`
}

// Status is the JSON status returned while a job is still processing.
type Status struct {
	Status               string `json:"status"` // PROCESSING
	AverageExecutionTime int    `json:"average_execution_time,omitempty"`
	ExecutionDuration    int    `json:"execution_duration,omitempty"`
	CreditsRefunded      *bool  `json:"credits_refunded,omitempty"`
	Error                string `json:"error,omitempty"`
}

// Result is the outcome of a retrieve call: either a completed binary payload
// or a Status. ContentType is the response Content-Type.
type Result struct {
	Data        []byte
	ContentType string
	Status      *Status
}

// Complete reports whether the retrieval yielded finished media (audio bytes
// rather than a PROCESSING status).
func (r *Result) Complete() bool {
	return r != nil && r.Data != nil && r.ContentType != "" && r.Status == nil
}

// Client wraps a *venice.Client for audio endpoints.
type Client struct {
	*venice.Client
}

// NewClient wraps a *venice.Client.
func NewClient(c *venice.Client) *Client { return &Client{Client: c} }

// Speech synthesizes text to audio and returns the raw bytes plus the response
// Content-Type (e.g. audio/mpeg).
func (c *Client) Speech(ctx context.Context, req *SpeechRequest) ([]byte, string, error) {
	data, ct, _, err := c.DoRaw(ctx, http.MethodPost, "/audio/speech", nil, req)
	return data, ct, err
}

// SpeechToFile synthesizes speech and writes the bytes to path. It infers a
// file extension from the response Content-Type when path has no extension.
// Returns the path actually written.
func (c *Client) SpeechToFile(ctx context.Context, req *SpeechRequest, path string) (string, error) {
	data, ct, err := c.Speech(ctx, req)
	if err != nil {
		return "", err
	}
	return writeMedia(path, ct, data)
}

// Transcribe uploads audio and returns the transcription. When
// req.ResponseFormat is "text" the returned Transcription.Text holds the raw
// text and the other fields are empty.
func (c *Client) Transcribe(ctx context.Context, req *TranscriptionRequest) (*Transcription, error) {
	if req.File.Name == "" {
		return nil, fmt.Errorf("venice/audio: transcription requires a file with a name")
	}
	fields := map[string]string{
		"model": req.Model,
	}
	if req.ResponseFormat != "" {
		fields["response_format"] = req.ResponseFormat
	}
	if req.Timestamps {
		fields["timestamps"] = "true"
	}
	if req.Language != "" {
		fields["language"] = req.Language
	}
	if req.Prompt != "" {
		fields["prompt"] = req.Prompt
	}
	if req.Temperature != nil {
		fields["temperature"] = fmt.Sprintf("%g", *req.Temperature)
	}
	for k, v := range req.Extra {
		fields[k] = v
	}
	body, ct, err := venice.BuildMultipart(fields, map[string]venice.MultipartFile{"file": req.File})
	if err != nil {
		return nil, err
	}
	resp, err := c.Send(ctx, http.MethodPost, "/audio/transcriptions", nil, ct, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, venice.DecodeErrorResponse(resp)
	}
	var out Transcription
	if req.ResponseFormat == "text" {
		raw, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, err
		}
		out.Text = strings.TrimSpace(string(raw))
		return &out, nil
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Quote estimates the USD cost of a music / sound-effect generation.
func (c *Client) Quote(ctx context.Context, req *QuoteRequest) (float64, error) {
	var out struct {
		Quote float64 `json:"quote"`
	}
	if err := c.DoJSON(ctx, http.MethodPost, "/audio/quote", nil, req, &out); err != nil {
		return 0, err
	}
	return out.Quote, nil
}

// Queue starts a music or sound-effect generation.
func (c *Client) Queue(ctx context.Context, req *QueueRequest) (*QueueResponse, error) {
	var out QueueResponse
	if err := c.DoJSON(ctx, http.MethodPost, "/audio/queue", nil, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Retrieve polls a queued audio job. When the job is still processing the
// returned Result carries a Status; when it is finished Result.Data holds the
// audio bytes.
func (c *Client) Retrieve(ctx context.Context, ref *JobRef) (*Result, error) {
	data, ct, _, err := c.DoRaw(ctx, http.MethodPost, "/audio/retrieve", nil, ref)
	if err != nil {
		return nil, err
	}
	base := strings.SplitN(ct, ";", 2)[0]
	if base != "application/json" {
		return &Result{Data: data, ContentType: ct}, nil
	}
	var st Status
	if err := json.Unmarshal(data, &st); err != nil {
		return &Result{Data: data, ContentType: ct}, nil
	}
	return &Result{ContentType: ct, Status: &st}, nil
}

// Wait polls Retrieve until the job completes or ctx is done. poll is the
// delay between polls (a non-positive value uses 5 seconds).
func (c *Client) Wait(ctx context.Context, ref *JobRef, poll time.Duration) (*Result, error) {
	if poll <= 0 {
		poll = 5 * time.Second
	}
	for {
		res, err := c.Retrieve(ctx, ref)
		if err != nil {
			return nil, err
		}
		if res.Complete() {
			return res, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(poll):
		}
	}
}

// Complete releases provider-held media for a finished audio job.
func (c *Client) Complete(ctx context.Context, ref *JobRef) error {
	return c.DoJSON(ctx, http.MethodPost, "/audio/complete", nil, ref, nil)
}

// GenerateMusic is a convenience that quotes, queues and waits for a music /
// sound-effect generation, returning the finished audio bytes.
func (c *Client) GenerateMusic(ctx context.Context, req *QueueRequest, poll time.Duration) ([]byte, string, error) {
	queued, err := c.Queue(ctx, req)
	if err != nil {
		return nil, "", err
	}
	ref := &JobRef{Model: queued.Model, QueueID: queued.QueueID}
	res, err := c.Wait(ctx, ref, poll)
	if err != nil {
		return nil, "", err
	}
	return res.Data, res.ContentType, nil
}

// writeMedia writes data to path, appending an extension derived from the
// Content-Type when path has none. Returns the final path.
func writeMedia(path, contentType string, data []byte) (string, error) {
	if filepath.Ext(path) == "" {
		ext := extensionFor(contentType)
		path += ext
	}
	if err := venice.WriteFile(path, data); err != nil {
		return "", err
	}
	return path, nil
}

func extensionFor(contentType string) string {
	switch strings.ToLower(strings.SplitN(contentType, ";", 2)[0]) {
	case "audio/mpeg", "audio/mp3":
		return ".mp3"
	case "audio/wav", "audio/x-wav":
		return ".wav"
	case "audio/flac":
		return ".flac"
	case "audio/ogg":
		return ".ogg"
	case "audio/aac":
		return ".aac"
	case "audio/mp4":
		return ".m4a"
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "video/mp4":
		return ".mp4"
	case "audio/pcm":
		return ".pcm"
	default:
		return ".bin"
	}
}
