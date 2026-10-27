package main

import (
	"net/url"
	"strings"
	"testing"
)

func TestPostgresConnectionStringConfigAndEnvironment(t *testing.T) {
	t.Setenv("POSTGRES_CONNECTION_STRING", " postgres://environment ")

	got, err := postgresConnectionString(&Config{PostgresConnectionString: " postgres://config "})
	if err != nil {
		t.Fatalf("postgresConnectionString(config) error = %v", err)
	}
	parsed, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parsing config connection string: %v", err)
	}
	if parsed.Query().Get("sslmode") != "disable" {
		t.Fatalf("config connection string sslmode = %q; want disable", parsed.Query().Get("sslmode"))
	}

	got, err = postgresConnectionString(&Config{})
	if err != nil {
		t.Fatalf("postgresConnectionString(empty config) error = %v", err)
	}
	if !strings.HasPrefix(got, "postgres://environment?") {
		t.Fatalf("postgresConnectionString(empty config) = %q; want environment URL", got)
	}

	got, err = postgresConnectionString(&Config{PostgresConnectionString: "host=db sslmode=require"})
	if err != nil {
		t.Fatalf("postgresConnectionString(keyword DSN) error = %v", err)
	}
	if !strings.HasSuffix(got, "sslmode=require sslmode=disable") {
		t.Fatalf("keyword DSN = %q; want sslmode=disable to override the configured mode", got)
	}
}

func TestParseBigIntRequiresFullDecimalValue(t *testing.T) {
	tests := []struct {
		input   string
		want    int64
		wantErr bool
	}{
		{input: "1557930395868209162", want: 1557930395868209162},
		{input: "123abc", wantErr: true},
		{input: "9223372036854775808", wantErr: true},
	}

	for _, tt := range tests {
		got, err := parseBigInt(tt.input)
		if (err != nil) != tt.wantErr {
			t.Errorf("parseBigInt(%q) error = %v, wantErr %t", tt.input, err, tt.wantErr)
			continue
		}
		if err == nil && got != tt.want {
			t.Errorf("parseBigInt(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}
