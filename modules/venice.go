// Package venice is a small, stdlib-only Go client for the Venice AI API.
//
// It covers the native Venice endpoints: chat completions (OpenAI-compatible
// with Venice-specific parameters), image generation/editing, text-to-speech,
// speech-to-text, music & sound-effect generation, video generation, web
// search/scrape, models, billing balance/usage, and characters.
//
// The client resolves its API key from, in order of precedence:
//
//  1. an explicit WithAPIKey option,
//  2. the VENICE_ELYVENICE_API_KEY environment variable,
//  3. the "VENICE_ELYVENICE_API_KEY" field of ~/.private/config.json.
package venice

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DefaultBaseURL is the public Venice API root.
const DefaultBaseURL = "https://api.venice.ai/api/v1"

// ConfigKey is the JSON field name read from ~/.private/config.json.
const ConfigKey = "VENICE_ELYVENICE_API_KEY"

// EnvKey is the environment variable that overrides the config file.
const EnvKey = "VENICE_ELYVENICE_API_KEY"

// Option configures a Client.
type Option func(*Client)

// WithAPIKey sets the bearer token used for authorization.
func WithAPIKey(key string) Option {
	return func(c *Client) { c.APIKey = key }
}

// WithBaseURL overrides the API base URL (default DefaultBaseURL).
func WithBaseURL(base string) Option {
	return func(c *Client) { c.BaseURL = strings.TrimRight(base, "/") }
}

// WithHTTPClient sets the underlying *http.Client (custom timeouts, transport).
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) { c.HTTP = hc }
}

// Client talks to the Venice AI API. Zero-value is not usable; construct with
// NewClient.
type Client struct {
	APIKey  string
	BaseURL string
	HTTP    *http.Client
}

// NewClient builds a Client. If no API key is supplied via options, the key is
// resolved from the environment (VENICE_ELYVENICE_API_KEY) and then from
// ~/.private/config.json (key VENICE_ELYVENICE_API_KEY). An error is returned
// only if key resolution fails (no key found) — it never panics.
func NewClient(opts ...Option) (*Client, error) {
	c := &Client{
		BaseURL: DefaultBaseURL,
		HTTP:    &http.Client{Timeout: 300 * time.Second},
	}
	for _, o := range opts {
		o(c)
	}
	if c.APIKey == "" {
		if k := os.Getenv(EnvKey); k != "" {
			c.APIKey = k
		}
	}
	if c.APIKey == "" {
		k, err := keyFromConfig()
		if err != nil {
			return nil, fmt.Errorf("venice: %w", err)
		}
		c.APIKey = k
	}
	return c, nil
}

// keyFromConfig reads the VENICE_ELYVENICE_API_KEY field from
// $HOME/.private/config.json (or %USERPROFILE% on Windows).
func keyFromConfig() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot resolve home directory: %w", err)
	}
	path := filepath.Join(home, ".private", "config.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("no API key set: %s is unset and %s unreadable: %w", EnvKey, path, err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return "", fmt.Errorf("parse %s: %w", path, err)
	}
	k, _ := m[ConfigKey].(string)
	if k == "" {
		return "", fmt.Errorf("no API key set: %s is unset and %s has no %q field", EnvKey, path, ConfigKey)
	}
	return k, nil
}

// APIError is a non-2xx response from the Venice API.
type APIError struct {
	StatusCode int
	Status     string
	Type       string `json:"type"`
	Code       string `json:"code"`
	Message    string `json:"message"`
	Raw        []byte
}

func (e *APIError) Error() string {
	msg := strings.TrimSpace(e.Message)
	if msg == "" {
		msg = strings.TrimSpace(string(e.Raw))
	}
	if msg == "" {
		msg = e.Status
	}
	return fmt.Sprintf("venice: %s (%d): %s", e.Status, e.StatusCode, msg)
}

// Send performs an HTTP request with the client's auth and returns the raw
// response. It does not check the status code; callers decide how to interpret
// the body. On a non-2xx status, call DecodeErrorResponse to surface an
// *APIError.
func (c *Client) Send(ctx context.Context, method, path string, query url.Values, contentType string, body []byte) (*http.Response, error) {
	return c.send(ctx, method, path, query, contentType, body)
}

// send performs an HTTP request and returns the raw response. It does not
// check the status code; callers decide how to interpret the body (JSON or
// binary). On a non-2xx status the caller is expected to call
// decodeError to surface an *APIError.
func (c *Client) send(ctx context.Context, method, path string, query url.Values, contentType string, body []byte) (*http.Response, error) {
	if c.APIKey == "" {
		return nil, errors.New("venice: no API key configured")
	}
	u := c.BaseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	return c.HTTP.Do(req)
}

// DoJSON marshals in, sends the request, checks the status and unmarshals the
// JSON response into out. A non-2xx status produces an *APIError.
func (c *Client) DoJSON(ctx context.Context, method, path string, query url.Values, in, out any) error {
	return c.doJSON(ctx, method, path, query, in, out)
}

// doJSON marshals in, sends the request, checks the status and unmarshals the
// JSON response into out. A non-2xx status produces an *APIError.
func (c *Client) doJSON(ctx context.Context, method, path string, query url.Values, in, out any) error {
	var body []byte
	var err error
	if in != nil {
		body, err = json.Marshal(in)
		if err != nil {
			return fmt.Errorf("venice: marshal request: %w", err)
		}
	}
	contentType := "application/json"
	if body == nil {
		contentType = ""
	}
	resp, err := c.send(ctx, method, path, query, contentType, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return decodeError(resp)
	}
	if out == nil {
		io.Copy(io.Discard, resp.Body)
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("venice: decode response: %w", err)
	}
	return nil
}

// DoRaw sends the request and returns the raw response body bytes plus the
// Content-Type of the reply. Non-2xx statuses become *APIError.
func (c *Client) DoRaw(ctx context.Context, method, path string, query url.Values, in any) ([]byte, string, http.Header, error) {
	return c.doRaw(ctx, method, path, query, in)
}

// doRaw sends the request and returns the raw response body bytes plus the
// Content-Type of the reply. Non-2xx statuses become *APIError.
func (c *Client) doRaw(ctx context.Context, method, path string, query url.Values, in any) ([]byte, string, http.Header, error) {
	var body []byte
	var err error
	if in != nil {
		body, err = json.Marshal(in)
		if err != nil {
			return nil, "", nil, fmt.Errorf("venice: marshal request: %w", err)
		}
	}
	resp, err := c.send(ctx, method, path, query, "application/json", body)
	if err != nil {
		return nil, "", nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, "", nil, decodeError(resp)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", nil, err
	}
	return data, resp.Header.Get("Content-Type"), resp.Header, nil
}

func decodeError(resp *http.Response) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	apiErr := &APIError{StatusCode: resp.StatusCode, Status: resp.Status, Raw: raw}
	if err := json.Unmarshal(raw, apiErr); err != nil {
		// Not JSON; keep raw text in Message.
		apiErr.Message = strings.TrimSpace(string(raw))
	}
	if apiErr.Message == "" && apiErr.Code == "" {
		apiErr.Message = strings.TrimSpace(string(raw))
	}
	return apiErr
}

// DecodeErrorResponse builds an *APIError from an already-read error
// response, for packages that performed the request themselves.
func DecodeErrorResponse(resp *http.Response) error {
	return decodeError(resp)
}

// WriteFile writes data to path (0666, as os.WriteFile).
func WriteFile(path string, data []byte) error {
	return os.WriteFile(path, data, 0o666)
}

// BuildMultipart assembles a multipart/form-data body from string fields and
// file parts, returning the buffer and the boundary Content-Type header value.
func BuildMultipart(fields map[string]string, files map[string]MultipartFile) ([]byte, string, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for k, v := range fields {
		if err := w.WriteField(k, v); err != nil {
			return nil, "", err
		}
	}
	for k, f := range files {
		fw, err := w.CreateFormFile(k, f.Name)
		if err != nil {
			return nil, "", err
		}
		if _, err := fw.Write(f.Data); err != nil {
			return nil, "", err
		}
	}
	if err := w.Close(); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), w.FormDataContentType(), nil
}

// MultipartFile is a file part for multipart uploads (STT, text parser).
type MultipartFile struct {
	Name string // filename reported to the server
	MIME string // optional; used when set
	Data []byte
}

// LoadImageArg accepts a base64 image string, a data URL, an http(s) URL, or
// a local file path and returns a base64 data URL suitable for the API image
// fields (e.g. POST /image/edit "image", POST /image/upscale "image"). Binary
// input is returned as-is (assumed already base64). URLs are fetched with the
// client's HTTP client.
func (c *Client) LoadImageArg(arg string) (string, error) {
	return loadImageArg(arg, c.HTTP)
}

// LoadImageArg is a package-level convenience that fetches URLs with
// http.DefaultClient.
func LoadImageArg(arg string) (string, error) {
	return loadImageArg(arg, http.DefaultClient)
}

func loadImageArg(arg string, hc *http.Client) (string, error) {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return "", errors.New("empty image argument")
	}
	// data: URL -> extract the base64 payload
	if strings.HasPrefix(arg, "data:") {
		if i := strings.Index(arg, ","); i >= 0 {
			return arg[i+1:], nil
		}
		return "", errors.New("malformed data URL")
	}
	// http(s) URL -> fetch bytes
	if strings.HasPrefix(arg, "http://") || strings.HasPrefix(arg, "https://") {
		resp, err := hc.Get(arg)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("fetch %s: %s", arg, resp.Status)
		}
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			return "", err
		}
		return base64.StdEncoding.EncodeToString(data), nil
	}
	// local file path
	data, err := os.ReadFile(arg)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(data), nil
}
