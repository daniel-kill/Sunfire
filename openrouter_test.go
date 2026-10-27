package main

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
)

func TestOpenRouterCommandRegistersAnalyticsAndVideoWithinDiscordLimit(t *testing.T) {
	spec := openRouterCommandSpec()
	if len(spec.Command.Options) > 25 {
		t.Fatalf("openrouter has %d options; Discord permits at most 25", len(spec.Command.Options))
	}

	options := make(map[string]*discordgo.ApplicationCommandOption, len(spec.Command.Options))
	for _, option := range spec.Command.Options {
		options[option.Name] = option
	}
	for _, name := range []string{"analytics", "video"} {
		if options[name] == nil {
			t.Errorf("/openrouter %s is not registered", name)
		}
	}
	analytics := options["analytics"]
	if analytics != nil {
		if len(analytics.Options) != 1 || analytics.Options[0].Name != "days" {
			t.Errorf("/openrouter analytics options = %+v, want optional days parameter", analytics.Options)
		}
	}
}

func TestOpenRouterAnalyticsBuildsSupportedQuery(t *testing.T) {
	now := time.Date(2026, time.October, 4, 5, 59, 32, 0, time.FixedZone("UTC+2", 2*60*60))
	query := newORAnalyticsQuery(now, 7)

	if len(query.Metrics) != 1 || query.Metrics[0] != "request_count" {
		t.Fatalf("analytics metrics = %v, want only documented request_count metric", query.Metrics)
	}
	if query.Granularity != "day" || len(query.Dimensions) != 1 || query.Dimensions[0] != "model" {
		t.Fatalf("unexpected analytics grouping: dimensions=%v granularity=%q", query.Dimensions, query.Granularity)
	}
	if query.TimeRange.Start != "2026-09-27T03:59:32Z" || query.TimeRange.End != "2026-10-04T03:59:32Z" {
		t.Errorf("unexpected UTC analytics range: start=%q end=%q", query.TimeRange.Start, query.TimeRange.End)
	}
	encoded, err := json.Marshal(query)
	if err != nil {
		t.Fatalf("failed to marshal analytics query: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatalf("failed to decode analytics query: %v", err)
	}
	if _, exists := payload["total_cost"]; exists {
		t.Error("analytics query includes unsupported top-level total_cost field")
	}
	if payload["limit"] != float64(100) {
		t.Errorf("analytics limit = %v, want 100", payload["limit"])
	}
}
