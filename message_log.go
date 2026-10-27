// Package main — message_log.go handles PostgreSQL message archiving,
// channel tracking via Data/messageLogChannels.json, and full-text search.
package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	_ "github.com/lib/pq"
)

var (
	msgLogChannelsFilePath = dataPath("messageLogChannels.json")
	msgLogStoreMu          sync.Mutex

	pgPool   *sql.DB
	pgPoolMu sync.Mutex

	msgQueue             []*loggedMessageRow
	msgQueueMu           sync.Mutex
	isFlushing           bool
	messageLogRetryDelay = 10 * time.Second
	messageLogDBProvider = getPostgresPool

	channelIDRegex = regexp.MustCompile(`^\d{10,25}$`)
)

type loggedMessageRow struct {
	ID        int64
	ServerID  int64
	ChannelID int64
	UserID    int64
	Content   string
	CreatedAt time.Time
}

type LoggedMessageResult struct {
	ID        string
	ServerID  string
	ChannelID string
	UserID    string
	Content   string
	CreatedAt time.Time
}

func loadLoggedChannelIDs(guildID string) []string {
	msgLogStoreMu.Lock()
	defer msgLogStoreMu.Unlock()

	data, err := os.ReadFile(msgLogChannelsFilePath)
	if err != nil {
		return nil
	}

	var root map[string][]string
	if err := json.Unmarshal(data, &root); err != nil {
		return nil
	}

	return root[guildID]
}

func addLoggedChannelID(guildID, channelID string) (ok bool, reason string) {
	if !channelIDRegex.MatchString(channelID) {
		return false, "invalid-id"
	}

	msgLogStoreMu.Lock()
	defer msgLogStoreMu.Unlock()

	_ = os.MkdirAll(filepath.Dir(msgLogChannelsFilePath), 0755)
	root := make(map[string][]string)
	data, err := os.ReadFile(msgLogChannelsFilePath)
	if err == nil {
		_ = json.Unmarshal(data, &root)
	}

	list := root[guildID]
	for _, id := range list {
		if id == channelID {
			return false, "already-added"
		}
	}

	list = append(list, channelID)
	root[guildID] = list

	b, _ := json.MarshalIndent(root, "", "  ")
	_ = os.WriteFile(msgLogChannelsFilePath, b, 0644)
	return true, ""
}

func removeLoggedChannelID(guildID, channelID string) (ok bool, reason string) {
	if !channelIDRegex.MatchString(channelID) {
		return false, "invalid-id"
	}

	msgLogStoreMu.Lock()
	defer msgLogStoreMu.Unlock()

	data, err := os.ReadFile(msgLogChannelsFilePath)
	if err != nil {
		return false, "not-found"
	}

	root := make(map[string][]string)
	if err := json.Unmarshal(data, &root); err != nil {
		return false, "not-found"
	}

	list := root[guildID]
	var next []string
	found := false
	for _, id := range list {
		if id == channelID {
			found = true
		} else {
			next = append(next, id)
		}
	}

	if !found {
		return false, "not-found"
	}

	root[guildID] = next
	b, _ := json.MarshalIndent(root, "", "  ")
	_ = os.WriteFile(msgLogChannelsFilePath, b, 0644)
	return true, ""
}

func isChannelLogged(guildID, channelID string) bool {
	ids := loadLoggedChannelIDs(guildID)
	for _, id := range ids {
		if id == channelID {
			return true
		}
	}
	return false
}

func getPostgresPool() *sql.DB {
	db, err := getPostgresPoolWithError()
	if err != nil {
		log.Printf("[message-log-db] %v", err)
	}
	return db
}

func getPostgresPoolWithError() (*sql.DB, error) {
	pgPoolMu.Lock()
	defer pgPoolMu.Unlock()

	if pgPool != nil {
		return pgPool, nil
	}

	cfg, err := loadConfig()
	if err != nil {
		return nil, fmt.Errorf("loading PostgreSQL configuration: %w", err)
	}
	connectionString, err := postgresConnectionString(cfg)
	if err != nil {
		return nil, fmt.Errorf("preparing PostgreSQL connection: %w", err)
	}
	if connectionString == "" {
		return nil, fmt.Errorf("PostgreSQL is not configured (set POSTGRES_CONNECTION_STRING in config or the environment)")
	}

	db, err := sql.Open("postgres", connectionString)
	if err != nil {
		return nil, fmt.Errorf("opening PostgreSQL connection: %w", err)
	}

	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(30 * time.Minute)

	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("connecting to PostgreSQL: %w", err)
	}

	pgPool = db
	return pgPool, nil
}

func postgresConnectionString(cfg *Config) (string, error) {
	connectionString := ""
	if cfg != nil && strings.TrimSpace(cfg.PostgresConnectionString) != "" {
		connectionString = strings.TrimSpace(cfg.PostgresConnectionString)
	} else {
		connectionString = strings.TrimSpace(os.Getenv("POSTGRES_CONNECTION_STRING"))
	}
	if connectionString == "" {
		return "", nil
	}

	if strings.HasPrefix(connectionString, "postgres://") || strings.HasPrefix(connectionString, "postgresql://") {
		parsed, err := url.Parse(connectionString)
		if err != nil {
			return "", err
		}
		query := parsed.Query()
		query.Set("sslmode", "disable")
		parsed.RawQuery = query.Encode()
		return parsed.String(), nil
	}

	return connectionString + " sslmode=disable", nil
}

func enqueueLoggedMessage(m *discordgo.MessageCreate) {
	if m == nil || m.ID == "" || m.GuildID == "" || m.ChannelID == "" || m.Author == nil {
		return
	}

	id, _ := parseBigInt(m.ID)
	serverID, _ := parseBigInt(m.GuildID)
	channelID, _ := parseBigInt(m.ChannelID)
	userID, _ := parseBigInt(m.Author.ID)

	if id == 0 || serverID == 0 || channelID == 0 || userID == 0 {
		return
	}

	createdAt := m.Timestamp.UTC()

	content := m.Content
	if len(content) > 4000 {
		content = content[:4000]
	}

	row := &loggedMessageRow{
		ID:        id,
		ServerID:  serverID,
		ChannelID: channelID,
		UserID:    userID,
		Content:   content,
		CreatedAt: createdAt,
	}

	msgQueueMu.Lock()
	msgQueue = append(msgQueue, row)
	shouldFlush := !isFlushing
	if shouldFlush {
		isFlushing = true
	}
	msgQueueMu.Unlock()

	if shouldFlush {
		go flushMessageLogQueue()
	}
}

func flushMessageLogQueue() {
	db := messageLogDBProvider()
	if db == nil {
		msgQueueMu.Lock()
		queueLength := len(msgQueue)
		msgQueueMu.Unlock()
		log.Printf("[message-log-db] PostgreSQL unavailable; retaining %d queued messages for retry", queueLength)
		requeueBatch(nil)
		return
	}

	msgQueueMu.Lock()
	batch := msgQueue
	msgQueue = nil
	msgQueueMu.Unlock()

	if len(batch) == 0 {
		msgQueueMu.Lock()
		isFlushing = false
		msgQueueMu.Unlock()
		return
	}

	tx, err := db.Begin()
	if err != nil {
		log.Printf("[message-log-db] begin tx failed: %v", err)
		requeueBatch(batch)
		return
	}

	stmt, err := tx.Prepare(`
		INSERT INTO messages (id, server_id, channel_id, user_id, content, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (id) DO NOTHING
	`)
	if err != nil {
		_ = tx.Rollback()
		log.Printf("[message-log-db] prepare statement failed: %v", err)
		requeueBatch(batch)
		return
	}
	defer stmt.Close()

	for _, r := range batch {
		_, err := stmt.Exec(r.ID, r.ServerID, r.ChannelID, r.UserID, r.Content, r.CreatedAt)
		if err != nil {
			_ = tx.Rollback()
			log.Printf("[message-log-db] exec insert failed: %v", err)
			requeueBatch(batch)
			return
		}
	}

	if err := tx.Commit(); err != nil {
		log.Printf("[message-log-db] commit failed: %v", err)
		requeueBatch(batch)
		return
	}

	msgQueueMu.Lock()
	if len(msgQueue) > 0 {
		go func() {
			time.Sleep(1 * time.Second)
			flushMessageLogQueue()
		}()
	} else {
		isFlushing = false
	}
	msgQueueMu.Unlock()
}

func requeueBatch(batch []*loggedMessageRow) {
	msgQueueMu.Lock()
	msgQueue = append(batch, msgQueue...)
	isFlushing = false
	msgQueueMu.Unlock()
	time.AfterFunc(messageLogRetryDelay, func() {
		msgQueueMu.Lock()
		if !isFlushing && len(msgQueue) > 0 {
			isFlushing = true
			msgQueueMu.Unlock()
			flushMessageLogQueue()
			return
		}
		msgQueueMu.Unlock()
	})
}

func parseBigInt(s string) (int64, error) {
	return strconv.ParseInt(s, 10, 64)
}

func searchPostgresMessages(serverID, channelID, userID, query string, limit int) ([]LoggedMessageResult, error) {
	db := getPostgresPool()
	if db == nil {
		return nil, fmt.Errorf("PostgreSQL is not configured (missing POSTGRES_CONNECTION_STRING)")
	}

	if limit < 1 {
		limit = 10
	} else if limit > 25 {
		limit = 25
	}

	var clauses []string
	var args []any
	n := 1

	if serverID != "" {
		sID, err := parseBigInt(serverID)
		if err != nil {
			return nil, fmt.Errorf("invalid server ID: %w", err)
		}
		clauses = append(clauses, fmt.Sprintf("server_id = $%d", n))
		args = append(args, sID)
		n++
	}
	if channelID != "" {
		cID, err := parseBigInt(channelID)
		if err != nil {
			return nil, fmt.Errorf("invalid channel ID: %w", err)
		}
		clauses = append(clauses, fmt.Sprintf("channel_id = $%d", n))
		args = append(args, cID)
		n++
	}
	if userID != "" {
		uID, err := parseBigInt(userID)
		if err != nil {
			return nil, fmt.Errorf("invalid user ID: %w", err)
		}
		clauses = append(clauses, fmt.Sprintf("user_id = $%d", n))
		args = append(args, uID)
		n++
	}
	if query != "" {
		clauses = append(clauses, fmt.Sprintf("content ILIKE $%d", n))
		args = append(args, "%"+query+"%")
		n++
	}

	whereClause := "TRUE"
	if len(clauses) > 0 {
		whereClause = strings.Join(clauses, " AND ")
	}

	sqlText := fmt.Sprintf(`
		SELECT id, server_id, channel_id, user_id, content, created_at
		FROM messages
		WHERE %s
		ORDER BY created_at DESC
		LIMIT $%d
	`, whereClause, n)
	args = append(args, limit)

	rows, err := db.Query(sqlText, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []LoggedMessageResult
	for rows.Next() {
		var id, sID, cID, uID int64
		var content sql.NullString
		var createdAt time.Time

		if err := rows.Scan(&id, &sID, &cID, &uID, &content, &createdAt); err != nil {
			return nil, fmt.Errorf("reading message search result: %w", err)
		}

		results = append(results, LoggedMessageResult{
			ID:        fmt.Sprintf("%d", id),
			ServerID:  fmt.Sprintf("%d", sID),
			ChannelID: fmt.Sprintf("%d", cID),
			UserID:    fmt.Sprintf("%d", uID),
			Content:   content.String,
			CreatedAt: createdAt,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating message search results: %w", err)
	}

	return results, nil
}

func getPostgresMessageByID(messageID string) (*LoggedMessageResult, error) {
	db := getPostgresPool()
	if db == nil {
		return nil, fmt.Errorf("PostgreSQL is not configured")
	}

	id, err := parseBigInt(messageID)
	if err != nil {
		return nil, fmt.Errorf("invalid message ID")
	}

	row := db.QueryRow(`
		SELECT id, server_id, channel_id, user_id, content, created_at
		FROM messages
		WHERE id = $1
	`, id)

	var mID, sID, cID, uID int64
	var content sql.NullString
	var createdAt time.Time

	if err := row.Scan(&mID, &sID, &cID, &uID, &content, &createdAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("message is not in the archive; fetch an ID from `/message-log search` (only messages captured while logging was enabled can be fetched)")
		}
		return nil, err
	}

	return &LoggedMessageResult{
		ID:        fmt.Sprintf("%d", mID),
		ServerID:  fmt.Sprintf("%d", sID),
		ChannelID: fmt.Sprintf("%d", cID),
		UserID:    fmt.Sprintf("%d", uID),
		Content:   content.String,
		CreatedAt: createdAt,
	}, nil
}

func getPostgresStats() (total int64, servers int64, channels int64, err error) {
	db, err := getPostgresPoolWithError()
	if err != nil {
		return 0, 0, 0, err
	}

	row := db.QueryRow(`
		SELECT count(*)::bigint AS total, count(DISTINCT server_id) AS servers, count(DISTINCT channel_id) AS channels
		FROM messages
	`)
	if err := row.Scan(&total, &servers, &channels); err != nil {
		return 0, 0, 0, err
	}
	return total, servers, channels, nil
}

// Hook called in onMessageCreate
func processMessageLogging(m *discordgo.MessageCreate) {
	if m.GuildID == "" || m.ChannelID == "" {
		return
	}
	if isChannelLogged(m.GuildID, m.ChannelID) {
		enqueueLoggedMessage(m)
	}
}

// Slash command: /message-log [add|remove|list|search|stats|fetch]
func handleMessageLog(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if i.GuildID == "" {
		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "❌ This command can only be used inside a server.",
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
		return
	}

	options := i.ApplicationCommandData().Options
	if len(options) == 0 {
		return
	}

	sub := options[0]
	switch sub.Name {
	case "add":
		var channelID string
		for _, opt := range sub.Options {
			if opt.Name == "channel" {
				channelID = opt.ChannelValue(s).ID
			}
		}
		if channelID == "" {
			channelID = i.ChannelID
		}

		ok, reason := addLoggedChannelID(i.GuildID, channelID)
		if !ok {
			msg := "❌ That channel ID is not valid."
			if reason == "already-added" {
				msg = fmt.Sprintf("ℹ️ <#%s> is already being logged.", channelID)
			}
			_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content: msg,
					Flags:   discordgo.MessageFlagsEphemeral,
				},
			})
			return
		}

		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: fmt.Sprintf("✅ Now logging <#%s> (`%s`) to PostgreSQL.", channelID, channelID),
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})

	case "remove":
		var channelID string
		for _, opt := range sub.Options {
			if opt.Name == "channel" {
				channelID = opt.ChannelValue(s).ID
			}
		}
		if channelID == "" {
			channelID = i.ChannelID
		}

		ok, reason := removeLoggedChannelID(i.GuildID, channelID)
		if !ok {
			msg := "❌ That channel ID is not valid."
			if reason == "not-found" {
				msg = fmt.Sprintf("ℹ️ <#%s> is not currently being logged.", channelID)
			}
			_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content: msg,
					Flags:   discordgo.MessageFlagsEphemeral,
				},
			})
			return
		}

		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: fmt.Sprintf("✅ Stopped logging <#%s> (`%s`).", channelID, channelID),
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})

	case "list":
		ids := loadLoggedChannelIDs(i.GuildID)
		if len(ids) == 0 {
			_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content: "ℹ️ No channels are being logged. Use `/message-log add <channel>`.",
					Flags:   discordgo.MessageFlagsEphemeral,
				},
			})
			return
		}

		var lines []string
		for idx, id := range ids {
			lines = append(lines, fmt.Sprintf("%d. <#%s> (`%s`)", idx+1, id, id))
		}

		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: fmt.Sprintf("📋 **Channels being logged (%d):**\n%s", len(ids), strings.Join(lines, "\n")),
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})

	case "search":
		var query, channelID, userID string
		limit := 10
		for _, opt := range sub.Options {
			switch opt.Name {
			case "query":
				query = opt.StringValue()
			case "channel":
				channelID = opt.ChannelValue(s).ID
			case "user":
				userID = opt.UserValue(s).ID
			case "limit":
				limit = int(opt.IntValue())
			}
		}

		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Flags: discordgo.MessageFlagsEphemeral,
			},
		})

		results, err := searchPostgresMessages(i.GuildID, channelID, userID, query, limit)
		if err != nil {
			_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
				Content: fmt.Sprintf("❌ Search failed: %v", err),
				Flags:   discordgo.MessageFlagsEphemeral,
			})
			return
		}

		if len(results) == 0 {
			_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
				Content: fmt.Sprintf("No messages found matching %q.", query),
				Flags:   discordgo.MessageFlagsEphemeral,
			})
			return
		}

		content := fmt.Sprintf("🔎 **%d result(s)** for %q:\n", len(results), query)
		for _, r := range results {
			snippet := r.Content
			if len(snippet) > 120 {
				snippet = snippet[:120] + "..."
			}
			ts := fmt.Sprintf("<t:%d:R>", r.CreatedAt.Unix())
			content += fmt.Sprintf("• <#%s> · <@%s> · %s · ID `%s`\n  `%s`\n", r.ChannelID, r.UserID, ts, r.ID, snippet)
		}
		content += "\n_Use `/message-log fetch messageid:<id>` to download the full message as a markdown file._"

		_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
			Content: content,
			Flags:   discordgo.MessageFlagsEphemeral,
		})

	case "stats":
		total, servers, channels, err := getPostgresStats()
		if err != nil {
			_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content: fmt.Sprintf("❌ Stats unavailable: %v", err),
					Flags:   discordgo.MessageFlagsEphemeral,
				},
			})
			return
		}

		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: fmt.Sprintf("📊 **Message log stats**\n• Messages: %d\n• Servers: %d\n• Channels: %d", total, servers, channels),
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})

	case "fetch":
		var messageID string
		for _, opt := range sub.Options {
			if opt.Name == "messageid" {
				messageID = opt.StringValue()
			}
		}

		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Flags: discordgo.MessageFlagsEphemeral,
			},
		})

		msg, err := getPostgresMessageByID(messageID)
		if err != nil {
			_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
				Content: fmt.Sprintf("❌ Could not fetch message `%s`: %v", messageID, err),
				Flags:   discordgo.MessageFlagsEphemeral,
			})
			return
		}

		mdContent := fmt.Sprintf("# Message Log — %s\n\n"+
			"- **ID**: `%s`\n"+
			"- **Server**: `%s`\n"+
			"- **Channel**: <#%s> (`%s`)\n"+
			"- **Author**: <@%s> (`%s`)\n"+
			"- **Timestamp**: %s\n\n"+
			"---\n\n%s\n",
			msg.ID, msg.ID, msg.ServerID, msg.ChannelID, msg.ChannelID, msg.UserID, msg.UserID,
			msg.CreatedAt.Format(time.RFC3339), msg.Content)

		filename := fmt.Sprintf("message-%s.md", msg.ID)
		reader := strings.NewReader(mdContent)

		_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
			Content: fmt.Sprintf("Full message **`%s`** (%d chars):", msg.ID, len(msg.Content)),
			Files: []*discordgo.File{
				{
					Name:   filename,
					Reader: reader,
				},
			},
			Flags: discordgo.MessageFlagsEphemeral,
		})
	}
}
