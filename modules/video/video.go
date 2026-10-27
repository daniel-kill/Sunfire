// Package video implements the asynchronous Venice video generation lifecycle:
// /video/quote, /video/queue, /video/retrieve and /video/complete.
package video

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"sunfire/modules"
)

// QuoteRequest estimates the USD cost of a video generation.
type QuoteRequest struct {
	Model         string         `json:"model"`
	Prompt        string         `json:"prompt,omitempty"`
	Duration      string         `json:"duration,omitempty"`     // e.g. "5s"
	Resolution    string         `json:"resolution,omitempty"`   // e.g. "720p"
	AspectRatio   string         `json:"aspect_ratio,omitempty"` // e.g. "16:9"
	Audio         *bool          `json:"audio,omitempty"`
	UpscaleFactor *int           `json:"upscale_factor,omitempty"` // topaz-video-upscale: 1, 2, 4
	Extra         map[string]any `json:"-"`
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

// QueueRequest queues a video generation (POST /video/queue).
type QueueRequest struct {
	Model                       string         `json:"model"`
	Prompt                      string         `json:"prompt,omitempty"`
	Duration                    string         `json:"duration,omitempty"`
	Resolution                  string         `json:"resolution,omitempty"`
	AspectRatio                 string         `json:"aspect_ratio,omitempty"`
	ImageURL                    string         `json:"image_url,omitempty"`            // image-to-video
	VideoURL                    string         `json:"video_url,omitempty"`            // video-to-video / upscale
	ReferenceImageURLs          []string       `json:"reference_image_urls,omitempty"` // reference-to-video
	ReferenceVideoTotalDuration *float64       `json:"reference_video_total_duration,omitempty"`
	UpscaleFactor               *int           `json:"upscale_factor,omitempty"`
	BitrateMode                 string         `json:"bitrate_mode,omitempty"`
	Extra                       map[string]any `json:"-"`
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

// QueueResponse is the result of POST /video/queue. Private models include a
// short-lived download_url that must be persisted alongside the queue_id.
type QueueResponse struct {
	Model       string `json:"model"`
	QueueID     string `json:"queue_id"`
	DownloadURL string `json:"download_url,omitempty"`
	Status      string `json:"status,omitempty"`
}

// JobRef identifies a queued video job.
type JobRef struct {
	Model   string `json:"model"`
	QueueID string `json:"queue_id"`
}

// Status is the JSON status returned while a job is processing.
type Status struct {
	Status               string `json:"status"` // PROCESSING
	AverageExecutionTime int    `json:"average_execution_time,omitempty"`
	ExecutionDuration    int    `json:"execution_duration,omitempty"`
	Error                string `json:"error,omitempty"`
}

// Result is the outcome of a retrieve call.
type Result struct {
	Data        []byte
	ContentType string
	Status      *Status
	JSON        []byte // raw body when the response was JSON
}

// Complete reports whether the retrieval yielded finished media.
func (r *Result) Complete() bool {
	return r != nil && r.Status == nil && r.Data != nil && r.ContentType != ""
}

// Client wraps a *venice.Client for video endpoints.
type Client struct {
	*venice.Client
}

// NewClient wraps a *venice.Client.
func NewClient(c *venice.Client) *Client { return &Client{Client: c} }

// Quote estimates the USD cost of a video generation.
func (c *Client) Quote(ctx context.Context, req *QuoteRequest) (float64, error) {
	var out struct {
		Quote float64 `json:"quote"`
	}
	if err := c.DoJSON(ctx, http.MethodPost, "/video/quote", nil, req, &out); err != nil {
		return 0, err
	}
	return out.Quote, nil
}

// Queue starts a video generation.
func (c *Client) Queue(ctx context.Context, req *QueueRequest) (*QueueResponse, error) {
	var out QueueResponse
	if err := c.DoJSON(ctx, http.MethodPost, "/video/queue", nil, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Retrieve polls a queued video job. Processing responses carry a Status;
// completed inline responses carry the video bytes. Models that returned a
// download_url at queue time always respond with JSON here — fetch the file
// from the download_url instead.
func (c *Client) Retrieve(ctx context.Context, ref *JobRef) (*Result, error) {
	data, ct, _, err := c.DoRaw(ctx, http.MethodPost, "/video/retrieve", nil, ref)
	if err != nil {
		return nil, err
	}
	base := strings.SplitN(ct, ";", 2)[0]
	if base != "application/json" {
		return &Result{Data: data, ContentType: ct}, nil
	}
	var st Status
	if err := json.Unmarshal(data, &st); err != nil {
		return &Result{Data: data, ContentType: ct, JSON: data}, nil
	}
	return &Result{ContentType: ct, Status: &st, JSON: data}, nil
}

// Wait polls Retrieve until the job completes or ctx is done.
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

// Complete releases provider-held media for a finished video job.
func (c *Client) Complete(ctx context.Context, ref *JobRef) error {
	return c.DoJSON(ctx, http.MethodPost, "/video/complete", nil, ref, nil)
}

// Download fetches a private download_url (pre-signed, no auth header).
func (c *Client) Download(ctx context.Context, downloadURL string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, "", venice.DecodeErrorResponse(resp)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", err
	}
	return data, resp.Header.Get("Content-Type"), nil
}

// GenerateVideo is a convenience that quotes, queues and waits for a video,
// returning the finished bytes.
func (c *Client) GenerateVideo(ctx context.Context, req *QueueRequest, poll time.Duration) ([]byte, string, error) {
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
