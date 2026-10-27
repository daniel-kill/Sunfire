// Package characters implements the Venice Characters API: GET /characters,
// GET /characters/{slug} and GET /characters/{slug}/reviews.
package characters

import (
	"context"
	"fmt"
	"net/http"
	"net/url"

	"sunfire/modules"
)

// Character is a Venice public character.
type Character struct {
	Name        string `json:"name"`
	Slug        string `json:"slug"`
	ModelID     string `json:"modelId"`
	Description string `json:"description"`
	WebEnabled  bool   `json:"webEnabled"`
}

// ListOptions filter and page the characters list.
type ListOptions struct {
	Search   string
	Tags     string
	Category string
	Model    string
	Sort     string
	Page     int
	PageSize int
}

// Response is the characters list envelope.
type Response struct {
	Object string      `json:"object"`
	Data   []Character `json:"data"`
}

// ReviewsPage is a page of character reviews.
type ReviewsPage struct {
	Object     string   `json:"object"`
	Data       []Review `json:"data"`
	Total      int      `json:"total"`
	Page       int      `json:"page"`
	PageSize   int      `json:"pageSize"`
	TotalPages int      `json:"totalPages"`
}

// Review is a single public review of a character.
type Review struct {
	ID        string `json:"id"`
	Author    string `json:"author,omitempty"`
	Rating    int    `json:"rating,omitempty"`
	Title     string `json:"title,omitempty"`
	Content   string `json:"content,omitempty"`
	CreatedAt string `json:"createdAt,omitempty"`
}

// Client wraps a *venice.Client for the characters API.
type Client struct {
	*venice.Client
}

// NewClient wraps a *venice.Client.
func NewClient(c *venice.Client) *Client { return &Client{Client: c} }

// List returns characters, optionally filtered.
func (c *Client) List(ctx context.Context, opts *ListOptions) (*Response, error) {
	q := url.Values{}
	if opts != nil {
		if opts.Search != "" {
			q.Set("search", opts.Search)
		}
		if opts.Tags != "" {
			q.Set("tags", opts.Tags)
		}
		if opts.Category != "" {
			q.Set("category", opts.Category)
		}
		if opts.Model != "" {
			q.Set("model", opts.Model)
		}
		if opts.Sort != "" {
			q.Set("sort", opts.Sort)
		}
		if opts.Page > 0 {
			q.Set("page", fmt.Sprint(opts.Page))
		}
		if opts.PageSize > 0 {
			q.Set("pageSize", fmt.Sprint(opts.PageSize))
		}
	}
	var out Response
	if err := c.DoJSON(ctx, http.MethodGet, "/characters", q, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Get returns a single character by slug.
func (c *Client) Get(ctx context.Context, slug string) (*Character, error) {
	var out struct {
		Object string    `json:"object"`
		Data   Character `json:"data"`
	}
	if err := c.DoJSON(ctx, http.MethodGet, "/characters/"+slug, nil, nil, &out); err != nil {
		return nil, err
	}
	return &out.Data, nil
}

// Reviews returns paginated reviews for a character.
func (c *Client) Reviews(ctx context.Context, slug string, page, pageSize int) (*ReviewsPage, error) {
	q := url.Values{}
	if page > 0 {
		q.Set("page", fmt.Sprint(page))
	}
	if pageSize > 0 {
		q.Set("pageSize", fmt.Sprint(pageSize))
	}
	var out ReviewsPage
	if err := c.DoJSON(ctx, http.MethodGet, "/characters/"+slug+"/reviews", q, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
