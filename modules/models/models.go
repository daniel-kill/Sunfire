// Package models implements the Venice Models API: GET /models and
// GET /models/traits.
package models

import (
	"context"
	"net/http"
	"net/url"

	"sunfire/modules"
)

// ListOptions filter the model catalog. Type is a filter such as "text",
// "image", "tts", "asr", "audio", "video". All fields are optional.
type ListOptions struct {
	Type string
	// Extra filters passed through as query parameters.
	Extra map[string]string
}

// Model describes one model in the Venice catalog.
type Model struct {
	ID        string     `json:"id"`
	Type      string     `json:"type"`
	Object    string     `json:"object"`
	Created   int64      `json:"created"`
	OwnedBy   string     `json:"owned_by"`
	ModelSpec *ModelSpec `json:"model_spec,omitempty"`
	// Extra holds any unrecognized fields for forward compatibility.
	Extra map[string]any `json:"-"`
}

// ModelSpec carries detailed per-model metadata.
type ModelSpec struct {
	AvailableContextTokens int            `json:"availableContextTokens,omitempty"`
	Capabilities           *Capabilities  `json:"capabilities,omitempty"`
	DefaultFormat          string         `json:"default_format,omitempty"`
	SupportedFormats       []string       `json:"supported_formats,omitempty"`
	Voices                 []string       `json:"voices,omitempty"`
	VoiceChanger           bool           `json:"voice_changer,omitempty"`
	Pricing                *Pricing       `json:"pricing,omitempty"`
	Constraints            map[string]any `json:"constraints,omitempty"`
	Extra                  map[string]any `json:"-"`
}

// Capabilities lists model feature flags.
type Capabilities struct {
	SupportsFunctionCalling bool `json:"supportsFunctionCalling,omitempty"`
	SupportsResponseSchema  bool `json:"supportsResponseSchema,omitempty"`
	SupportsWebSearch       bool `json:"supportsWebSearch,omitempty"`
	SupportsE2EE            bool `json:"supportsE2EE,omitempty"`
	SupportsTeeAttestation  bool `json:"supportsTeeAttestation,omitempty"`
}

// Pricing is a flexible price schedule (resolutions, quality, durations).
type Pricing struct {
	Resolutions map[string]Price            `json:"resolutions,omitempty"`
	Quality     map[string]map[string]Price `json:"quality,omitempty"`
	Durations   map[string]Price            `json:"durations,omitempty"`
}

// Price is a USD/DIEM price pair.
type Price struct {
	USD  float64 `json:"usd"`
	DIEM float64 `json:"diem"`
}

// Response is the list-model response envelope.
type Response struct {
	Object string  `json:"object"`
	Data   []Model `json:"data"`
}

// Traits maps trait names to their current model IDs.
type Traits struct {
	Object string            `json:"object"`
	Data   map[string]string `json:"data"`
}

// Client wraps a *venice.Client for the models API.
type Client struct {
	*venice.Client
}

// NewClient wraps a *venice.Client.
func NewClient(c *venice.Client) *Client { return &Client{Client: c} }

// List returns models, optionally filtered by type.
func (c *Client) List(ctx context.Context, opts *ListOptions) (*Response, error) {
	q := url.Values{}
	if opts != nil {
		if opts.Type != "" {
			q.Set("type", opts.Type)
		}
		for k, v := range opts.Extra {
			q.Set(k, v)
		}
	}
	var out Response
	if err := c.DoJSON(ctx, http.MethodGet, "/models", q, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListRaw returns the raw JSON body of GET /models.
func (c *Client) ListRaw(ctx context.Context, opts *ListOptions) ([]byte, error) {
	q := url.Values{}
	if opts != nil {
		if opts.Type != "" {
			q.Set("type", opts.Type)
		}
		for k, v := range opts.Extra {
			q.Set(k, v)
		}
	}
	data, _, _, err := c.DoRaw(ctx, http.MethodGet, "/models", q, nil)
	return data, err
}

// Traits lists trait-to-model mappings.
func (c *Client) Traits(ctx context.Context) (*Traits, error) {
	var out Traits
	if err := c.DoJSON(ctx, http.MethodGet, "/models/traits", nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ByID finds a model by ID in a list response.
func (r *Response) ByID(id string) *Model {
	if r == nil {
		return nil
	}
	for i := range r.Data {
		if r.Data[i].ID == id {
			return &r.Data[i]
		}
	}
	return nil
}

// IDs returns the model IDs in a list response.
func (r *Response) IDs() []string {
	if r == nil {
		return nil
	}
	out := make([]string, 0, len(r.Data))
	for _, m := range r.Data {
		out = append(out, m.ID)
	}
	return out
}
