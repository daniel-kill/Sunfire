// Package image implements Venice image endpoints: native generation
// (POST /image/generate), OpenAI-compatible generation (POST /images/generations),
// editing/inpainting, multi-image editing, background removal, upscaling, and
// style listing.
package image

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"

	"sunfire/modules"
)

// Request is a native image generation request (POST /image/generate).
type Request struct {
	Model           string         `json:"model"`
	Prompt          string         `json:"prompt"`
	NegativePrompt  string         `json:"negative_prompt,omitempty"`
	Width           *int           `json:"width,omitempty"`
	Height          *int           `json:"height,omitempty"`
	Format          string         `json:"format,omitempty"` // jpeg, png, webp
	Variants        *int           `json:"variants,omitempty"`
	ReturnBinary    *bool          `json:"return_binary,omitempty"`
	SafeMode        *bool          `json:"safe_mode,omitempty"`
	Seed            *int           `json:"seed,omitempty"`
	CFGScale        *float64       `json:"cfg_scale,omitempty"`
	StylePreset     string         `json:"style_preset,omitempty"`
	AspectRatio     string         `json:"aspect_ratio,omitempty"`
	Resolution      string         `json:"resolution,omitempty"`
	EnhancePrompt   *bool          `json:"enhance_prompt,omitempty"`
	Quality         string         `json:"quality,omitempty"`
	EnableWebSearch *bool          `json:"enable_web_search,omitempty"`
	Extra           map[string]any `json:"-"`
}

// MarshalJSON merges Extra fields into the request body.
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

// Response is a native image generation response. Images holds the generated
// images as base64-encoded strings (unless the request used return_binary).
type Response struct {
	Images []string `json:"images"`
}

// Decoded returns the generated images decoded from base64.
func (r *Response) Decoded() ([][]byte, error) {
	out := make([][]byte, 0, len(r.Images))
	for _, s := range r.Images {
		b, err := base64.StdEncoding.DecodeString(s)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, nil
}

// EditRequest is an image edit/inpaint request (POST /image/edit). The image
// is provided as base64 (Data), as an http(s) URL (URL), or as a local file
// path (Path) which is loaded at call time.
type EditRequest struct {
	Model          string         `json:"model"`
	Prompt         string         `json:"prompt"`
	Image          string         `json:"image,omitempty"` // base64
	ImageURL       string         `json:"image_url,omitempty"`
	Path           string         `json:"-"` // local file path, resolved to base64
	NegativePrompt string         `json:"negative_prompt,omitempty"`
	Mask           string         `json:"mask,omitempty"` // base64 (inpaint region)
	Width          *int           `json:"width,omitempty"`
	Height         *int           `json:"height,omitempty"`
	Format         string         `json:"format,omitempty"`
	Seed           *int           `json:"seed,omitempty"`
	CFGScale       *float64       `json:"cfg_scale,omitempty"`
	AspectRatio    string         `json:"aspect_ratio,omitempty"`
	Resolution     string         `json:"resolution,omitempty"`
	Quality        string         `json:"quality,omitempty"`
	EnhancePrompt  *bool          `json:"enhance_prompt,omitempty"`
	SafeMode       *bool          `json:"safe_mode,omitempty"`
	Extra          map[string]any `json:"-"`
}

// MarshalJSON merges Extra fields.
func (r EditRequest) MarshalJSON() ([]byte, error) {
	type alias EditRequest
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

// MultiEditRequest is a multi-image edit (POST /image/multi-edit).
type MultiEditRequest struct {
	Model          string         `json:"model"`
	Prompt         string         `json:"prompt"`
	Images         []string       `json:"images,omitempty"` // base64
	ImageURLs      []string       `json:"image_urls,omitempty"`
	NegativePrompt string         `json:"negative_prompt,omitempty"`
	Format         string         `json:"format,omitempty"`
	Seed           *int           `json:"seed,omitempty"`
	Resolution     string         `json:"resolution,omitempty"`
	AspectRatio    string         `json:"aspect_ratio,omitempty"`
	Quality        string         `json:"quality,omitempty"`
	Extra          map[string]any `json:"-"`
}

// MarshalJSON merges Extra fields.
func (r MultiEditRequest) MarshalJSON() ([]byte, error) {
	type alias MultiEditRequest
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

// UpscaleRequest upsizes an image (POST /image/upscale).
type UpscaleRequest struct {
	Model    string         `json:"model"`
	Image    string         `json:"image,omitempty"` // base64
	ImageURL string         `json:"image_url,omitempty"`
	Scale    *int           `json:"scale,omitempty"`
	Extra    map[string]any `json:"-"`
}

// MarshalJSON merges Extra fields.
func (r UpscaleRequest) MarshalJSON() ([]byte, error) {
	type alias UpscaleRequest
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

// OpenAIRequest is an OpenAI-compatible generation request
// (POST /images/generations). Safe-mode style moderation uses the
// "moderation" field (auto|low).
type OpenAIRequest struct {
	Model          string         `json:"model"`
	Prompt         string         `json:"prompt"`
	Size           string         `json:"size,omitempty"`            // e.g. 1024x1024
	ResponseFormat string         `json:"response_format,omitempty"` // b64_json | url
	Moderation     string         `json:"moderation,omitempty"`
	Extra          map[string]any `json:"-"`
}

// MarshalJSON merges Extra fields.
func (r OpenAIRequest) MarshalJSON() ([]byte, error) {
	type alias OpenAIRequest
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

// OpenAIResponse is the OpenAI-compatible generation response.
type OpenAIResponse struct {
	Created int64         `json:"created"`
	Data    []OpenAIImage `json:"data"`
}

// OpenAIImage is a single generated image (b64 or URL).
type OpenAIImage struct {
	URL           string `json:"url,omitempty"`
	B64JSON       string `json:"b64_json,omitempty"`
	RevisedPrompt string `json:"revised_prompt,omitempty"`
}

// Client wraps a *venice.Client for image endpoints.
type Client struct {
	*venice.Client
}

// NewClient wraps a *venice.Client.
func NewClient(c *venice.Client) *Client { return &Client{Client: c} }

// Generate runs a native image generation. Returns decoded bytes of the first
// image plus the full response (for multi-variant calls).
func (c *Client) Generate(ctx context.Context, req *Request) (*Response, error) {
	var out Response
	if err := c.DoJSON(ctx, http.MethodPost, "/image/generate", nil, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GenerateRaw returns the raw JSON body, in case the caller wants to
// decode fields not covered by Response.
func (c *Client) GenerateRaw(ctx context.Context, req *Request) ([]byte, error) {
	data, _, _, err := c.DoRaw(ctx, http.MethodPost, "/image/generate", nil, req)
	return data, err
}

// Edit runs an image edit. Path/URL are resolved into base64 before sending.
func (c *Client) Edit(ctx context.Context, req *EditRequest) (*Response, error) {
	if err := req.resolve(); err != nil {
		return nil, err
	}
	var out Response
	if err := c.DoJSON(ctx, http.MethodPost, "/image/edit", nil, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// MultiEdit runs a multi-image edit.
func (c *Client) MultiEdit(ctx context.Context, req *MultiEditRequest) (*Response, error) {
	var out Response
	if err := c.DoJSON(ctx, http.MethodPost, "/image/multi-edit", nil, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RemoveBackground strips the background from an image and returns the PNG
// bytes. The input can be base64, an http(s) URL, or a local file path.
func (c *Client) RemoveBackground(ctx context.Context, image string) ([]byte, error) {
	data, err := loadImageArg(image)
	if err != nil {
		return nil, err
	}
	req := map[string]string{"image": data}
	raw, _, _, err := c.DoRaw(ctx, http.MethodPost, "/image/background-remove", nil, req)
	return raw, err
}

// Upscale returns upscaled image bytes.
func (c *Client) Upscale(ctx context.Context, req *UpscaleRequest) ([]byte, error) {
	if req.Image == "" && req.ImageURL == "" {
		return nil, fmt.Errorf("venice/image: upscale requires Image or ImageURL")
	}
	raw, _, _, err := c.DoRaw(ctx, http.MethodPost, "/image/upscale", nil, req)
	return raw, err
}

// Styles lists the available style presets for image generation.
func (c *Client) Styles(ctx context.Context) ([]string, error) {
	var out struct {
		Object string   `json:"object"`
		Data   []string `json:"data"`
	}
	if err := c.DoJSON(ctx, http.MethodGet, "/image/styles", nil, nil, &out); err != nil {
		return nil, err
	}
	return out.Data, nil
}

// GenerateOpenAI runs the OpenAI-compatible generation endpoint.
func (c *Client) GenerateOpenAI(ctx context.Context, req *OpenAIRequest) (*OpenAIResponse, error) {
	var out OpenAIResponse
	if err := c.DoJSON(ctx, http.MethodPost, "/images/generations", nil, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// resolve converts a local path or URL into the base64 image field.
func (r *EditRequest) resolve() error {
	if r.Image != "" {
		return nil
	}
	if r.ImageURL != "" {
		data, err := loadImageArg(r.ImageURL)
		if err != nil {
			return err
		}
		r.Image = data
		r.ImageURL = ""
		return nil
	}
	if r.Path != "" {
		data, err := loadImageArg(r.Path)
		if err != nil {
			return err
		}
		r.Image = data
		r.Path = ""
	}
	return nil
}

// loadImageArg turns an http(s) URL, data URL, or local file path into a
// base64 data URL suitable for the API image fields.
func loadImageArg(arg string) (string, error) {
	data, err := venice.LoadImageArg(arg)
	if err != nil {
		return "", fmt.Errorf("venice/image: %w", err)
	}
	return data, nil
}
