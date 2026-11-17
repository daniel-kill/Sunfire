package main

import (
	"strings"
	"testing"
)

func TestValidateReadOnlyQuery(t *testing.T) {
	validQueries := []string{
		"SELECT * FROM logs LIMIT 10",
		"SELECT author_username, count(*) FROM read_json_auto('*.json') GROUP BY author_username",
		"WITH active AS (SELECT author_username FROM logs) SELECT * FROM active",
		"  select count(*) from logs  ",
	}

	invalidQueries := []string{
		"",
		"DROP TABLE users",
		"SELECT * FROM logs; DELETE FROM logs",
		"INSERT INTO logs VALUES (1, 2)",
		"UPDATE logs SET content = 'hacked'",
		"COPY logs TO '/tmp/leak.csv'",
		"ATTACH '/etc/shadow' AS shadow",
		"PRAGMA database_list",
		"EXEC('calc.exe')",
	}

	for _, q := range validQueries {
		if err := validateReadOnlyQuery(q); err != nil {
			t.Errorf("expected valid query %q, got error: %v", q, err)
		}
	}

	for _, q := range invalidQueries {
		if err := validateReadOnlyQuery(q); err == nil {
			t.Errorf("expected invalid query %q to return error, got nil", q)
		}
	}
}

func TestBuildAnalyticsPresetSQL(t *testing.T) {
	presets := []string{
		"top_chatters",
		"daily_trends",
		"top_channels",
		"bot_ratio",
		"longest_messages",
	}

	for _, p := range presets {
		sql, err := buildAnalyticsPresetSQL(p, "Data/MessageLogs/123/*.json")
		if err != nil {
			t.Errorf("unexpected error for preset %q: %v", p, err)
		}
		if !strings.HasPrefix(sql, "SELECT") {
			t.Errorf("expected SELECT query for preset %q, got: %s", p, sql)
		}
		if !strings.Contains(sql, "read_json_auto") {
			t.Errorf("expected read_json_auto in preset %q query", p)
		}
	}

	_, err := buildAnalyticsPresetSQL("invalid_preset", "Data/test/*.json")
	if err == nil {
		t.Error("expected error for unknown preset, got nil")
	}
}

func TestResolveDuckDBBinary(t *testing.T) {
	bin := resolveDuckDBBinary()
	if bin == "" {
		t.Log("Note: DuckDB binary not found in tested paths")
	} else {
		t.Logf("Found DuckDB binary: %s", bin)
	}
}
