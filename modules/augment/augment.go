// Package augment implements Venice web-retrieval endpoints: web search
// (POST /augment/search), page scraping (POST /augment/scrape), and document
// text parsing (POST /augment/text-parser).
package augment

import (
	"context"
	"encoding/json"
	"net/http"

	"sunfire/modules"
)

// SearchRequest searches the web (POST /augment/search).
type SearchRequest struct {
	Query      string         `json:"query"`
	Provider   string         `json:"provider,omitempty"` // brave (default) | google
	MaxResults *int           `json:"max_results,omitempty"`
	Extra      map[string]any `json:"-"`
}

// MarshalJSON merges Extra fields.
func (r SearchRequest) MarshalJSON() ([]byte, error) {
	type alias SearchRequest
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

// SearchResult is a single web search hit.
type SearchResult struct {
	Index       int    `json:"index"`
	Title       string `json:"title"`
	URL         string `json:"url"`
	Snippet     string `json:"snippet"`
	Description string `json:"description,omitempty"`
	Date        string `json:"date,omitempty"`
}

// SearchResponse is the structured result of a web search.
type SearchResponse struct {
	Object  string         `json:"object"`
	Results []SearchResult `json:"results"`
	Query   string         `json:"query,omitempty"`
	Total   int            `json:"total,omitempty"`
}

// ScrapeRequest scrapes a public URL to markdown (POST /augment/scrape).
type ScrapeRequest struct {
	URL   string         `json:"url"`
	Extra map[string]any `json:"-"`
}

// MarshalJSON merges Extra fields.
func (r ScrapeRequest) MarshalJSON() ([]byte, error) {
	type alias ScrapeRequest
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

// TextParserRequest extracts text from an uploaded document (up to 25MB).
// Supported: PDF, EPUB, DOCX, PPTX, XLSX, XLS, plain text, markdown, CSV,
// JSON, YAML, TOML, XML, source code.
type TextParserRequest struct {
	File           venice.MultipartFile
	ResponseFormat string // json (default) or text
}

// TextParserResult is the structured text-parser response.
type TextParserResult struct {
	Text    string `json:"text"`
	Tokens  *int   `json:"token_count,omitempty"`
	RawText string `json:"raw_text,omitempty"`
	URL     string `json:"url,omitempty"`
}

// Client wraps a *venice.Client for web-retrieval endpoints.
type Client struct {
	*venice.Client
}

// NewClient wraps a *venice.Client.
func NewClient(c *venice.Client) *Client { return &Client{Client: c} }

// Search runs a web search and returns structured results.
func (c *Client) Search(ctx context.Context, req *SearchRequest) (*SearchResponse, error) {
	var out SearchResponse
	if err := c.DoJSON(ctx, http.MethodPost, "/augment/search", nil, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// SearchRaw returns the raw JSON body for schema evolution.
func (c *Client) SearchRaw(ctx context.Context, req *SearchRequest) ([]byte, error) {
	data, _, _, err := c.DoRaw(ctx, http.MethodPost, "/augment/search", nil, req)
	return data, err
}

// Scrape returns a web page's content as markdown.
func (c *Client) Scrape(ctx context.Context, url string) (string, error) {
	req := &ScrapeRequest{URL: url}
	var out struct {
		Content  string `json:"content"`
		Markdown string `json:"markdown"`
		Scrape   string `json:"scrape"`
	}
	if err := c.DoJSON(ctx, http.MethodPost, "/augment/scrape", nil, req, &out); err != nil {
		return "", err
	}
	switch {
	case out.Content != "":
		return out.Content, nil
	case out.Markdown != "":
		return out.Markdown, nil
	default:
		return out.Scrape, nil
	}
}

// TextParser extracts text from an uploaded document file.
func (c *Client) TextParser(ctx context.Context, req *TextParserRequest) (*TextParserResult, error) {
	if req.File.Name == "" {
		req.File.Name = "document.bin"
	}
	fields := map[string]string{}
	if req.ResponseFormat != "" {
		fields["response_format"] = req.ResponseFormat
	}
	body, ct, err := venice.BuildMultipart(fields, map[string]venice.MultipartFile{"file": req.File})
	if err != nil {
		return nil, err
	}
	resp, err := c.Send(ctx, http.MethodPost, "/augment/text-parser", nil, ct, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, venice.DecodeErrorResponse(resp)
	}
	var out TextParserResult
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}
