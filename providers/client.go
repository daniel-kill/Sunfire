// client.go — shared HTTP plumbing for the provider clients.
package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"
)

const userAgent = "ElyLightspeed/1.0"

// apiClient wraps an http.Client plus the provider's base URL + auth.
type apiClient struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

func newAPIClient(baseURL, apiKey string) *apiClient {
	return &apiClient{
		baseURL: strings.TrimSuffix(baseURL, "/"),
		apiKey:  apiKey,
		http:    &http.Client{Timeout: 180 * time.Second},
	}
}

// doJSON performs an HTTP request and decodes a JSON response. Returns a
// non-nil error on transport errors or non-2xx HTTP status.
func (c *apiClient) doJSON(ctx context.Context, method, path string, body interface{}) (json.RawMessage, error) {
	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		bodyReader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bodyReader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d from %s%s: %s", resp.StatusCode, c.baseURL, path, strings.TrimSpace(string(data)))
	}
	return data, nil
}

// doMultipart performs a multipart/form-data POST (for STT uploads).
func (c *apiClient) doMultipart(ctx context.Context, path string, fields map[string]string, fileField, fileName string, fileData []byte) (json.RawMessage, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	// Add non-file fields first (Cohere requires fields before the file part).
	for k, v := range fields {
		if k == fileField {
			continue
		}
		_ = w.WriteField(k, v)
	}
	fw, err := w.CreateFormFile(fileField, fileName)
	if err != nil {
		return nil, err
	}
	if _, err := fw.Write(fileData); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, &buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", w.FormDataContentType())
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d from %s%s: %s", resp.StatusCode, c.baseURL, path, strings.TrimSpace(string(data)))
	}
	return data, nil
}

// doRaw performs a POST returning raw bytes (for TTS audio). On non-2xx it
// returns the body as part of the error for readability.
func (c *apiClient) doRaw(ctx context.Context, method, path string, body interface{}) ([]byte, string, error) {
	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, "", err
		}
		bodyReader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bodyReader)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 25<<20))
	if err != nil {
		return nil, "", err
	}
	ct := resp.Header.Get("Content-Type")
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("HTTP %d from %s%s: %s", resp.StatusCode, c.baseURL, path, strings.TrimSpace(string(data)))
	}
	return data, ct, nil
}

// jsonUnmarshal decodes raw JSON into out, returning a friendly error.
func jsonUnmarshal(raw json.RawMessage, out interface{}) error {
	if len(raw) == 0 {
		return fmt.Errorf("empty response body")
	}
	return json.Unmarshal(raw, out)
}
