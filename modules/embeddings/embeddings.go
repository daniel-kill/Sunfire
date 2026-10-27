// Package embeddings implements Venice embedding generation:
// POST /embeddings.
package embeddings

import (
	"context"
	"net/http"

	"sunfire/modules"
)

// Request is the body for POST /embeddings/generate.
type Request struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

// Data is a single embedding vector for one input string.
type Data struct {
	Object    string    `json:"object"`
	Index     int       `json:"index"`
	Embedding []float64 `json:"embedding"`
}

// Usage reports token usage for the request.
type Usage struct {
	PromptTokens int `json:"prompt_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

// Response is the parsed response from /embeddings/generate.
type Response struct {
	Object string `json:"object"`
	Data   []Data `json:"data"`
	Model  string `json:"model"`
	Usage  Usage  `json:"usage"`
}

// Client wraps a *venice.Client for embedding endpoints.
type Client struct {
	*venice.Client
}

// NewClient wraps a *venice.Client.
func NewClient(c *venice.Client) *Client { return &Client{Client: c} }

// Generate computes embedding vectors for each input string.
func (c *Client) Generate(ctx context.Context, req *Request) (*Response, error) {
	if req.Model == "" {
		return nil, errNoModel()
	}
	if len(req.Input) == 0 {
		return nil, errNoInput()
	}
	var out Response
	if err := c.DoJSON(ctx, http.MethodPost, "/embeddings", nil, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GenerateOne is a convenience for a single input string.
func (c *Client) GenerateOne(ctx context.Context, model, input string) ([]float64, error) {
	out, err := c.Generate(ctx, &Request{Model: model, Input: []string{input}})
	if err != nil {
		return nil, err
	}
	if len(out.Data) == 0 {
		return nil, errNoData()
	}
	return out.Data[0].Embedding, nil
}

func errNoModel() error {
	return &errUsage{"venice/embeddings: model is required"}
}
func errNoInput() error {
	return &errUsage{"venice/embeddings: at least one input string is required"}
}
func errNoData() error {
	return &errUsage{"venice/embeddings: no embedding data in response"}
}

type errUsage struct{ msg string }

func (e *errUsage) Error() string { return e.msg }
