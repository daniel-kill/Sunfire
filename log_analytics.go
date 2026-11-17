package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
)

var (
	duckDBBinaryPath string
	duckDBBinaryOnce sync.Once
)

func resolveDuckDBBinary() string {
	duckDBBinaryOnce.Do(func() {
		candidates := []string{
			"duckdb",
			"duckdb.exe",
		}
		if runtime.GOOS == "windows" {
			candidates = append(candidates,
				`C:\Users\danie\scoop\shims\duckdb.exe`,
				`C:\Users\danie\.local\bin\duckdb.exe`,
			)
		} else {
			candidates = append(candidates,
				"/mnt/c/Users/danie/scoop/shims/duckdb.exe",
				"/home/daniel/.local/bin/duckdb",
				"/usr/local/bin/duckdb",
				"/usr/bin/duckdb",
			)
		}

		for _, candidate := range candidates {
			if path, err := exec.LookPath(candidate); err == nil {
				duckDBBinaryPath = path
				return
			}
			if fi, err := os.Stat(candidate); err == nil && !fi.IsDir() {
				duckDBBinaryPath = candidate
				return
			}
		}
	})
	return duckDBBinaryPath
}

var forbiddenSQLWords = regexp.MustCompile(`(?i)\b(DROP|DELETE|INSERT|UPDATE|ALTER|COPY|ATTACH|EXPORT|PRAGMA|CREATE|REPLACE|EXEC|CALL|INSTALL|LOAD)\b`)

func validateReadOnlyQuery(query string) error {
	trimmed := strings.TrimSpace(query)
	if trimmed == "" {
		return errors.New("query cannot be empty")
	}

	// Must begin with SELECT or WITH
	upper := strings.ToUpper(trimmed)
	if !strings.HasPrefix(upper, "SELECT") && !strings.HasPrefix(upper, "WITH") {
		return errors.New("only read-only SELECT or WITH queries are permitted")
	}

	// Semicolons chaining multiple statements are forbidden
	if strings.Contains(strings.TrimRight(trimmed, ";"), ";") {
		return errors.New("multiple queries or semicolons are not permitted")
	}

	// Disallowed DDL, DML, or disk operations
	if match := forbiddenSQLWords.FindString(trimmed); match != "" {
		return fmt.Errorf("SQL keyword %q is forbidden in analytics queries", match)
	}

	return nil
}

func buildAnalyticsPresetSQL(preset string, jsonGlobPath string) (string, error) {
	escapedGlob := strings.ReplaceAll(jsonGlobPath, "'", "''")

	switch strings.ToLower(strings.TrimSpace(preset)) {
	case "top_chatters":
		return fmt.Sprintf(`SELECT author_username AS "User", count(*) AS "Messages", sum(CASE WHEN deleted THEN 1 ELSE 0 END) AS "Deleted" FROM read_json_auto('%s') GROUP BY author_username ORDER BY "Messages" DESC LIMIT 10;`, escapedGlob), nil
	case "daily_trends":
		return fmt.Sprintf(`SELECT strftime(cast(timestamp as timestamp), '%%Y-%%m-%%d') AS "Date", count(*) AS "Messages" FROM read_json_auto('%s') GROUP BY "Date" ORDER BY "Date" DESC LIMIT 14;`, escapedGlob), nil
	case "top_channels":
		return fmt.Sprintf(`SELECT channel_id AS "Channel ID", count(*) AS "Messages" FROM read_json_auto('%s') GROUP BY channel_id ORDER BY "Messages" DESC LIMIT 10;`, escapedGlob), nil
	case "bot_ratio":
		return fmt.Sprintf(`SELECT CASE WHEN bot THEN 'Bot' ELSE 'Human' END AS "Type", count(*) AS "Messages", round(count(*) * 100.0 / sum(count(*)) over (), 1) AS "Pct" FROM read_json_auto('%s') GROUP BY bot;`, escapedGlob), nil
	case "longest_messages":
		return fmt.Sprintf(`SELECT author_username AS "User", count(*) AS "Messages", round(avg(length(content)), 1) AS "Avg Chars" FROM read_json_auto('%s') WHERE length(content) > 0 GROUP BY author_username HAVING count(*) >= 3 ORDER BY "Avg Chars" DESC LIMIT 10;`, escapedGlob), nil
	default:
		return "", fmt.Errorf("unknown preset %q; available: top_chatters, daily_trends, top_channels, bot_ratio, longest_messages", preset)
	}
}

func handleMessageLogQuery(s *discordgo.Session, i *discordgo.InteractionCreate, sub *discordgo.ApplicationCommandInteractionDataOption) {
	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Flags: discordgo.MessageFlagsEphemeral,
		},
	})

	preset := ""
	customSQL := ""
	for _, opt := range sub.Options {
		switch opt.Name {
		case "preset":
			preset = strings.TrimSpace(opt.StringValue())
		case "sql":
			customSQL = strings.TrimSpace(opt.StringValue())
		}
	}

	if preset == "" && customSQL == "" {
		preset = "top_chatters"
	}

	guildID := i.GuildID
	if guildID == "" {
		sendMessageLogQueryReply(s, i, "❌ This command must be executed within a Discord server.")
		return
	}

	// Resolve path to JSON message logs
	logsDir := messageLogsDir(guildID)
	if entries, err := os.ReadDir(logsDir); err != nil || len(entries) == 0 {
		sendMessageLogQueryReply(s, i, fmt.Sprintf("ℹ️ No JSON message logs found for server `%s` in `%s`.", guildID, logsDir))
		return
	}

	jsonGlob := filepath.ToSlash(filepath.Join(logsDir, "*.json"))
	if abs, err := filepath.Abs(jsonGlob); err == nil {
		jsonGlob = filepath.ToSlash(abs)
	}

	var query string
	if customSQL != "" {
		if err := validateReadOnlyQuery(customSQL); err != nil {
			sendMessageLogQueryReply(s, i, fmt.Sprintf("❌ Query validation error: %v", err))
			return
		}
		// If query uses shorthand "FROM logs" or "FROM messages", replace with read_json_auto(...)
		query = customSQL
		reLogs := regexp.MustCompile(`(?i)\bFROM\s+(logs|messages)\b`)
		query = reLogs.ReplaceAllString(query, fmt.Sprintf("FROM read_json_auto('%s')", strings.ReplaceAll(jsonGlob, "'", "''")))

		// If query doesn't specify read_json_auto, ensure it targets the guild log files
		if !strings.Contains(strings.ToLower(query), "read_json_auto") {
			sendMessageLogQueryReply(s, i, "❌ Custom query must query `FROM logs` or use `read_json_auto('...')`.")
			return
		}
	} else {
		var err error
		query, err = buildAnalyticsPresetSQL(preset, jsonGlob)
		if err != nil {
			sendMessageLogQueryReply(s, i, fmt.Sprintf("❌ %v", err))
			return
		}
	}

	duckDB := resolveDuckDBBinary()
	if duckDB == "" {
		sendMessageLogQueryReply(s, i, "❌ DuckDB binary is not available on this host.")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, duckDB, "-box", "-c", query)
	out, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("[message-log/query] duckdb failed: %v, out: %s", err, string(out))
		errSnippet := strings.TrimSpace(string(out))
		if len(errSnippet) > 300 {
			errSnippet = errSnippet[:300] + "..."
		}
		sendMessageLogQueryReply(s, i, fmt.Sprintf("❌ Query execution failed: %v\n```\n%s\n```", err, errSnippet))
		return
	}

	table := strings.TrimSpace(string(out))
	if table == "" {
		sendMessageLogQueryReply(s, i, "ℹ️ Query returned 0 rows.")
		return
	}

	// Discord limit is 2000 chars; truncate if table exceeds 1800
	if len(table) > 1800 {
		table = table[:1780] + "\n... (truncated)"
	}

	heading := fmt.Sprintf("🦆 **DuckDB Analytics Result** (`%s`):", preset)
	if customSQL != "" {
		heading = "🦆 **DuckDB Custom Query Result:**"
	}

	sendMessageLogQueryReply(s, i, fmt.Sprintf("%s\n```\n%s\n```", heading, table))
}

func sendMessageLogQueryReply(s *discordgo.Session, i *discordgo.InteractionCreate, message string) {
	_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
		Content: message,
		Flags:   discordgo.MessageFlagsEphemeral,
	})
}
