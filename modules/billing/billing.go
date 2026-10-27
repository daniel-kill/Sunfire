// Package billing implements Venice billing endpoints: GET /billing/balance,
// GET /billing/usage-analytics, and the cursor-paginated GET /billing/usage-history.
// (The legacy GET /billing/usage endpoint is sunset and returns 410.)
package billing

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"sunfire/modules"
)

// Balance is the account balance from GET /billing/balance.
type Balance struct {
	CanConsume          bool      `json:"canConsume"`
	ConsumptionCurrency string    `json:"consumptionCurrency"`
	Balances            *Balances `json:"balances,omitempty"`
	DiemEpochAllocation *float64  `json:"diemEpochAllocation,omitempty"`
	BalanceUSD          *float64  `json:"balanceUsd,omitempty"`
	BalanceDIEM         *float64  `json:"balanceDiem,omitempty"`
}

// Balances holds the per-currency balances.
type Balances struct {
	DIEM *float64 `json:"diem,omitempty"`
	USD  *float64 `json:"usd,omitempty"`
}

// UsageAnalytics is the aggregated usage from GET /billing/usage-analytics.
type UsageAnalytics struct {
	Lookback     string           `json:"lookback"`
	ByDate       []DailyUsage     `json:"byDate,omitempty"`
	ByModel      []ModelUsage     `json:"byModel,omitempty"`
	ByModelDaily []map[string]any `json:"byModelDaily,omitempty"`
	TopModels    []string         `json:"topModels,omitempty"`
	ByKey        []KeyUsage       `json:"byKey,omitempty"`
	ByKeyDaily   []map[string]any `json:"byKeyDaily,omitempty"`
	TopKeyNames  []string         `json:"topKeyNames,omitempty"`
}

// DailyUsage is one day's spend.
type DailyUsage struct {
	Date string  `json:"date"`
	USD  float64 `json:"USD"`
	DIEM float64 `json:"DIEM"`
}

// ModelUsage is per-model usage.
type ModelUsage struct {
	ModelName  string           `json:"modelName"`
	UnitType   string           `json:"unitType"`
	ModelType  string           `json:"modelType"`
	TotalUSD   float64          `json:"totalUsd"`
	TotalDIEM  float64          `json:"totalDiem"`
	TotalUnits float64          `json:"totalUnits"`
	Breakdown  []UsageBreakdown `json:"breakdown,omitempty"`
}

// UsageBreakdown is a per-token-type breakdown of model usage.
type UsageBreakdown struct {
	Type  string  `json:"type"`
	USD   float64 `json:"usd"`
	DIEM  float64 `json:"diem"`
	Units float64 `json:"units"`
}

// KeyUsage is per-API-key usage.
type KeyUsage struct {
	APIKeyID    string  `json:"apiKeyId"`
	Description string  `json:"description"`
	TotalUSD    float64 `json:"totalUsd"`
	TotalDIEM   float64 `json:"totalDiem"`
	TotalUnits  float64 `json:"totalUnits"`
}

// UsageHistoryOptions filter the usage-history walk.
type UsageHistoryOptions struct {
	StartTimestamp int64 // unix seconds
	EndTimestamp   int64
	PageSize       int
	// CSV requests a text/csv page instead of JSON.
	CSV bool
}

// UsageHistoryPage is one cursor-paginated page of usage entries.
type UsageHistoryPage struct {
	Entries    []UsageEntry `json:"entries"`
	NextCursor string       `json:"nextCursor"`
	HasMore    bool         `json:"hasMore"`
	CSV        string       `json:"-"` // populated when CSV was requested
}

// UsageEntry is a single billing ledger entry.
type UsageEntry struct {
	Timestamp        time.Time         `json:"timestamp"`
	SKU              string            `json:"sku"`
	PricePerUnitUSD  float64           `json:"pricePerUnitUsd"`
	Unit             float64           `json:"unit"`
	Amount           float64           `json:"amount"`
	Currency         string            `json:"currency"`
	Notes            string            `json:"notes,omitempty"`
	InferenceDetails *InferenceDetails `json:"inferenceDetails,omitempty"`
}

// InferenceDetails describes the inference behind a usage entry.
type InferenceDetails struct {
	RequestID              string `json:"requestId,omitempty"`
	InferenceExecutionTime int64  `json:"inferenceExecutionTime,omitempty"`
	PromptTokens           int    `json:"promptTokens,omitempty"`
	CompletionTokens       int    `json:"completionTokens,omitempty"`
}

// Client wraps a *venice.Client for billing endpoints.
type Client struct {
	*venice.Client
}

// NewClient wraps a *venice.Client.
func NewClient(c *venice.Client) *Client { return &Client{Client: c} }

// Balance returns the current account balance.
func (c *Client) Balance(ctx context.Context) (*Balance, error) {
	var out Balance
	if err := c.DoJSON(ctx, http.MethodGet, "/billing/balance", nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// UsageAnalytics returns aggregated usage. An empty opts uses the 7-day
// default lookback.
func (c *Client) UsageAnalytics(ctx context.Context, lookback string, startDate, endDate string) (*UsageAnalytics, error) {
	q := url.Values{}
	if lookback != "" {
		q.Set("lookback", lookback)
	}
	if startDate != "" {
		q.Set("startDate", startDate)
	}
	if endDate != "" {
		q.Set("endDate", endDate)
	}
	var out UsageAnalytics
	if err := c.DoJSON(ctx, http.MethodGet, "/billing/usage-analytics", q, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// UsageHistory fetches one page of the usage-history walk. Pass the
// nextCursor from a previous page to continue; filters travel inside the
// cursor and must be omitted on continuation requests.
func (c *Client) UsageHistory(ctx context.Context, opts *UsageHistoryOptions, nextCursor string) (*UsageHistoryPage, error) {
	q := url.Values{}
	if nextCursor != "" {
		q.Set("cursor", nextCursor)
	} else if opts != nil {
		if opts.StartTimestamp > 0 {
			q.Set("startTimestamp", strconv.FormatInt(opts.StartTimestamp, 10))
		}
		if opts.EndTimestamp > 0 {
			q.Set("endTimestamp", strconv.FormatInt(opts.EndTimestamp, 10))
		}
		if opts.PageSize > 0 {
			q.Set("pageSize", strconv.Itoa(opts.PageSize))
		}
	}
	accept := "application/json"
	if opts != nil && opts.CSV && nextCursor == "" {
		accept = "text/csv"
	}
	resp, err := c.Send(ctx, http.MethodGet, "/billing/usage-history", q, "", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, venice.DecodeErrorResponse(resp)
	}
	// When CSV was requested the body is CSV regardless of the Accept header
	// echoed back; detect by header Content-Type first, then by Accept.
	isCSV := false
	ct := resp.Header.Get("Content-Type")
	if len(ct) >= 9 && ct[:9] == "text/csv" {
		isCSV = true
	} else if accept == "text/csv" {
		isCSV = true
	}
	page := &UsageHistoryPage{}
	if isCSV {
		sb, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, err
		}
		page.CSV = string(sb)
		return page, nil
	}
	if err := json.NewDecoder(resp.Body).Decode(page); err != nil {
		return nil, err
	}
	return page, nil
}

// WalkUsageHistory iterates the entire usage history via its cursor.
// onPage is called for each page; return false to stop early.
func (c *Client) WalkUsageHistory(ctx context.Context, opts *UsageHistoryOptions, onPage func(*UsageHistoryPage) (bool, error)) error {
	cursor := ""
	for {
		page, err := c.UsageHistory(ctx, opts, cursor)
		if err != nil {
			return err
		}
		cont, err := onPage(page)
		if err != nil {
			return err
		}
		if !cont || !page.HasMore || page.NextCursor == "" {
			return nil
		}
		cursor = page.NextCursor
		opts = nil // filters travel inside the cursor on continuation
	}
}
