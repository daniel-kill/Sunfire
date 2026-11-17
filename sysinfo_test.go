package main

import (
	"strings"
	"testing"
)

func TestParseNvidiaSmiOutput(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		contains []string
	}{
		{
			name:     "valid telemetry single line",
			input:    "NVIDIA GeForce RTX 2060, 6144, 468, 36, 15\n",
			contains: []string{"NVIDIA GeForce RTX 2060", "468 / 6144 MB", "7.6%", "36°C", "15%"},
		},
		{
			name:     "multi-line takes first",
			input:    "NVIDIA GeForce RTX 2060, 6144, 1024, 45, 50\nNVIDIA GeForce GTX 1080, 8192, 2048, 55, 30",
			contains: []string{"NVIDIA GeForce RTX 2060", "1024 / 6144 MB", "16.7%", "45°C", "50%"},
		},
		{
			name:     "empty input",
			input:    "",
			contains: []string{"No dedicated GPU detected"},
		},
		{
			name:     "insufficient columns",
			input:    "NVIDIA RTX, 6144",
			contains: []string{"GPU telemetry unavailable"},
		},
		{
			name:     "unparseable numbers fallback",
			input:    "NVIDIA RTX, N/A, N/A, 40, 20",
			contains: []string{"NVIDIA RTX", "40°C", "20%"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := parseNvidiaSmiOutput(tc.input)
			for _, exp := range tc.contains {
				if !strings.Contains(res, exp) {
					t.Errorf("parseNvidiaSmiOutput(%q) = %q, expected to contain %q", tc.input, res, exp)
				}
			}
		})
	}
}

func TestParseMemLine(t *testing.T) {
	line := "MemTotal:       16248384 kB"
	got := parseMemLine(line)
	if !strings.Contains(got, "GB") {
		t.Errorf("expected GB in %q", got)
	}
	invalid := parseMemLine("InvalidLine")
	if invalid != "Unknown" {
		t.Errorf("expected Unknown for invalid line, got %q", invalid)
	}
}
