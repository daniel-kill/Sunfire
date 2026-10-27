// Package main — bot.go holds the Bot type: session lifecycle, command
// registration, and event-handler wiring.
package main

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
)

// SunfireServerID is the primary server Sunfire runs in.
const SunfireServerID = "1557930395868209162"
const SecondaryServerID = "1557930395868209162"
const SunfireAppID = "1553668018641436702"

var allowedGuildIDs = []string{
	SunfireServerID,
	SecondaryServerID,
}

// isAllowedGuild returns true if the guild ID is one the bot operates in.
func isAllowedGuild(guildID string) bool {
	for _, id := range allowedGuildIDs {
		if id == guildID {
			return true
		}
	}
	return false
}

// Bot wraps a discordgo.Session with the commands/handlers it serves.
type Bot struct {
	Session     *discordgo.Session
	logger      *logger
	registered  bool
	workers     workerLifecycle
	closeOnce   sync.Once
	lifecycleMu sync.Mutex
	closed      bool
}

// NewBot creates a Bot from a token. It wires up the event handlers
// and registers the slash commands.
func NewBot(token string) (*Bot, error) {
	if err := secureExistingMessageLogs(); err != nil {
		return nil, fmt.Errorf("securing existing message logs: %w", err)
	}

	session, err := discordgo.New("Bot " + token)
	if err != nil {
		return nil, err
	}

	// Cache up to 1000 messages per channel so BeforeDelete and BeforeUpdate are populated.
	session.State.MaxMessageCount = 1000

	bot := &Bot{Session: session, logger: newLogger(session)}
	session.AddHandler(bot.onReady)
	session.AddHandler(bot.onInteractionCreate)
	session.AddHandler(bot.onGuildMemberAdd)
	session.AddHandler(bot.onGuildMemberRemove)
	session.AddHandler(bot.onGuildMemberUpdate)
	session.AddHandler(bot.onGuildBanAdd)
	session.AddHandler(bot.onGuildBanRemove)
	session.AddHandler(bot.onGuildChannelCreate)
	session.AddHandler(bot.onGuildChannelUpdate)
	session.AddHandler(bot.onGuildChannelDelete)
	session.AddHandler(bot.onUserUpdate)
	session.AddHandler(bot.onMessageCreate)
	session.AddHandler(bot.onMessageUpdate)
	session.AddHandler(bot.onMessageDelete)
	session.AddHandler(bot.onMessageDeleteBulk)

	return bot, nil
}

// Open establishes the Discord websocket connection.
func (b *Bot) Open() error {
	if err := b.Session.Open(); err != nil {
		return err
	}
	log.Println("Bot is now running. Press Ctrl-C to exit.")
	return nil
}

// Close gracefully closes the session.
func (b *Bot) Close() {
	b.closeOnce.Do(func() {
		b.lifecycleMu.Lock()
		b.closed = true
		b.lifecycleMu.Unlock()
		b.workers.cancel()
		if b.Session != nil {
			if err := b.Session.Close(); err != nil {
				log.Printf("closing Discord session: %v", err)
			}
		}
		b.workers.wait(10 * time.Second)
	})
}

// onReady runs when the bot first connects: registers slash commands once,
// logs startup info to the logging channel, and posts server list.
func (b *Bot) onReady(s *discordgo.Session, r *discordgo.Ready) {
	log.Printf("Logged in as %v (ID %v)", s.State.User.Username, s.State.User.ID)

	// Register slash commands once.
	if !b.registered {
		if err := registerCommands(s); err != nil {
			log.Printf("warning: failed to register slash commands: %v", err)
		}
		b.registered = true
	}

	// Startup log to Discord logging channel.
	if b.logger != nil && channelID() != "" {
		guilds := s.State.Guilds
		var guildInfo string
		for _, g := range guilds {
			guildInfo += fmt.Sprintf("%s (%s) ", g.Name, g.ID)
		}
		b.logger.log("INFO", fmt.Sprintf("Sunfire online — %s | Servers (%d): %s", time.Now().Format("2006-01-02 15:04:05"), len(guilds), guildInfo))
	}

	log.Println("Sunfire online — logged in as " + s.State.User.Username)

	b.startWorkers(s)
}

func (b *Bot) startWorkers(s *discordgo.Session) {
	b.lifecycleMu.Lock()
	defer b.lifecycleMu.Unlock()
	if b.closed {
		return
	}
	b.workers.start(context.Background(),
		func(ctx context.Context) { startPeriodicMediaDeduplication(ctx, time.Hour, b) },
		func(ctx context.Context) { startReminderDaemon(ctx, s) },
		func(ctx context.Context) { startHealthChecker(ctx, s) },
		func(ctx context.Context) { startYouTubeNotifierDaemon(ctx, s) },
		func(ctx context.Context) { startKickNotifierDaemon(ctx, s) },
		func(ctx context.Context) { startSpotifyNotifierDaemon(ctx, s) },
		func(ctx context.Context) { startMoviesNotifierDaemon(ctx, s) },
		func(ctx context.Context) { startFreeStuffNotifierDaemon(ctx, s) },
		func(ctx context.Context) { startScrapeImagesDaemon(ctx, s) },
		func(ctx context.Context) { startBirthdayNotifierDaemon(ctx, s) },
		func(ctx context.Context) { startHermesTipsDaemon(ctx, s) },
	)
}

// isPublicCommand returns true if any server member can use the command.
func isPublicCommand(name string) bool {
	switch name {
	case "analytics", "automod", "battle", "birthday", "close-ticket", "cloudflare", "cohere", "dice", "emojigg", "fallen-star", "groq", "help", "ia", "lyrics", "media", "mistral", "nvidia", "openrouter", "pick", "remind", "riddle", "serverstats", "sysinfo", "transcript", "translate", "userinfo", "video":
		return true
	default:
		return false
	}
}

// onInteractionCreate dispatches a slash-command interaction to its handler.
func (b *Bot) onInteractionCreate(s *discordgo.Session, i *discordgo.InteractionCreate) {
	// Restrict bot strictly to designated servers.
	if !isAllowedGuild(i.GuildID) {
		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "❌ Sunfire only operates in its designated server.",
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
		return
	}

	// Handle Modal Submissions
	if i.Type == discordgo.InteractionModalSubmit {
		data := i.ModalSubmitData()
		if strings.HasPrefix(data.CustomID, "media:resize:") {
			handleMediaResizeModal(s, i)
			return
		}
		return
	}

	// Handle Component Interactions (Buttons, Select Menus)
	if i.Type == discordgo.InteractionMessageComponent {
		data := i.MessageComponentData()
		if strings.HasPrefix(data.CustomID, "help:") {
			handleHelpComponent(s, i)
			return
		}
		if strings.HasPrefix(data.CustomID, "or:") {
			orHandleComponent(s, i)
			return
		}
		if strings.HasPrefix(data.CustomID, "ban:view:") {
			handleBanPageComponent(s, i)
			return
		}
		if strings.HasPrefix(data.CustomID, "emojigg:page:") {
			handleEmojiGGPageComponent(s, i)
			return
		}

		if data.CustomID == "ticket:create" {
			// Create a private support thread for the user
			title := fmt.Sprintf("ticket-%s", i.Member.User.Username)
			thread, err := s.ThreadStart(i.ChannelID, title, discordgo.ChannelTypeGuildPrivateThread, 1440)
			if err != nil {
				_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionResponseData{
						Content: "❌ Failed to create ticket thread: " + err.Error(),
						Flags:   discordgo.MessageFlagsEphemeral,
					},
				})
				return
			}

			// Add member to thread
			_ = s.ThreadMemberAdd(thread.ID, i.Member.User.ID)
			cfg := getGuildSettings(i.GuildID)
			staffPing := ""
			if cfg.TicketStaffRoleID != "" {
				staffPing = fmt.Sprintf("<@&%s> ", cfg.TicketStaffRoleID)
			}

			_, _ = s.ChannelMessageSend(thread.ID, fmt.Sprintf("%s👋 Welcome <@%s>! Staff will assist you shortly. Use `/close-ticket` when resolved.", staffPing, i.Member.User.ID))

			_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content: fmt.Sprintf("✅ Your ticket has been opened: <#%s>", thread.ID),
					Flags:   discordgo.MessageFlagsEphemeral,
				},
			})
			return
		}

		if data.CustomID == "rolemenu:select" {
			// Toggle selected roles for user
			selectedRoles := data.Values
			var added, removed []string

			member, err := s.GuildMember(i.GuildID, i.Member.User.ID)
			if err == nil {
				memberRoleSet := make(map[string]bool)
				for _, rID := range member.Roles {
					memberRoleSet[rID] = true
				}

				for _, rID := range selectedRoles {
					if memberRoleSet[rID] {
						_ = s.GuildMemberRoleRemove(i.GuildID, i.Member.User.ID, rID)
						removed = append(removed, fmt.Sprintf("<@&%s>", rID))
					} else {
						_ = s.GuildMemberRoleAdd(i.GuildID, i.Member.User.ID, rID)
						added = append(added, fmt.Sprintf("<@&%s>", rID))
					}
				}
			}

			respText := "Roles updated!"
			if len(added) > 0 {
				respText += fmt.Sprintf("\n• Added: %s", strings.Join(added, ", "))
			}
			if len(removed) > 0 {
				respText += fmt.Sprintf("\n• Removed: %s", strings.Join(removed, ", "))
			}

			_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content: respText,
					Flags:   discordgo.MessageFlagsEphemeral,
				},
			})
			return
		}

		if data.CustomID == "battle:join" {
			handleJoinBattleComponent(s, i)
			return
		}

		if strings.HasPrefix(data.CustomID, "fs:") {
			handleFallenStarComponent(s, i)
			return
		}
		if strings.HasPrefix(data.CustomID, "media:") {
			handleMediaComponent(s, i)
			return
		}

		return
	}

	if i.Type != discordgo.InteractionApplicationCommand {
		return
	}

	// Owner-only gate: public commands can be used by anyone; others require bot owner.
	cmdName := i.ApplicationCommandData().Name
	if !isPublicCommand(cmdName) {
		cfg, err := loadConfig()
		if err != nil || cfg == nil {
			log.Printf("cannot authorize command %q: failed to load config: %v", cmdName, err)
			_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content: "❌ Bot configuration is unavailable; please try again later.",
					Flags:   discordgo.MessageFlagsEphemeral,
				},
			})
			return
		}
		invokerID := ""
		if i.Member != nil && i.Member.User != nil {
			invokerID = i.Member.User.ID
		} else if i.User != nil {
			invokerID = i.User.ID
		}
		if !isBotAdmin(cfg, invokerID) {
			if cmdName == "grant-access" {
				respondOwnerOnly(s, i)
				return
			}

			ownerConfigured := cfg.OwnerID != "" && cfg.OwnerID != "PASTE_YOUR_DISCORD_USER_ID_HERE"
			if !ownerConfigured && len(cfg.AdminIDs) == 0 {
				respondOwnerOnly(s, i)
				return
			}

			granted, grantErr := hasCommandAccess(dataPath("commandAccess.json"), cmdName, invokerID)
			if grantErr != nil {
				log.Printf("cannot authorize command %q: failed to load command grants: %v", cmdName, grantErr)
				_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionResponseData{
						Content: "❌ Command access data is unavailable; please try again later.",
						Flags:   discordgo.MessageFlagsEphemeral,
					},
				})
				return
			}
			if !granted {
				respondOwnerOnly(s, i)
				return
			}
		}
	}

	// Look up the command by name and dispatch to its handler.
	for _, spec := range commandSpecs {
		if spec.Command.Name == i.ApplicationCommandData().Name {
			spec.Handler(s, i)
			return
		}
	}
}

func respondOwnerOnly(s *discordgo.Session, i *discordgo.InteractionCreate) {
	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: "❌ Bot owner only.",
			Flags:   discordgo.MessageFlagsEphemeral,
		},
	})
}

// onGuildMemberAdd logs when a member joins a guild and assigns autorole if enabled.
func (b *Bot) onGuildMemberAdd(s *discordgo.Session, m *discordgo.GuildMemberAdd) {
	if m.GuildID != SunfireServerID {
		return
	}
	if b.logger != nil && channelID() != "" {
		b.logger.log("MEMBER JOIN", fmt.Sprintf("%s joined %s (%s)", m.User.Username, m.GuildID, m.User.ID))
	}

	// AutoRole assignment
	cfg := getGuildSettings(m.GuildID)
	if cfg.AutoRoleID != nil && *cfg.AutoRoleID != "" {
		_ = s.GuildMemberRoleAdd(m.GuildID, m.User.ID, *cfg.AutoRoleID)
	}
}

// onGuildMemberRemove logs when a member leaves a guild.
func (b *Bot) onGuildMemberRemove(s *discordgo.Session, m *discordgo.GuildMemberRemove) {
	if m.GuildID != SunfireServerID {
		return
	}
	if b.logger != nil && channelID() != "" {
		b.logger.log("MEMBER LEAVE", fmt.Sprintf("%s left %s (%s)", m.User.Username, m.GuildID, m.User.ID))
	}
}

// onGuildMemberUpdate logs mute/deaf/nick changes without spamming unchanged state updates.
func (b *Bot) onGuildMemberUpdate(s *discordgo.Session, m *discordgo.GuildMemberUpdate) {
	if m.GuildID != SunfireServerID {
		return
	}
	if b.logger == nil || channelID() == "" {
		return
	}

	// Only log changes if we have the previous state to compare against
	if m.BeforeUpdate != nil {
		if m.Mute != m.BeforeUpdate.Mute {
			state := "unmuted"
			if m.Mute {
				state = "muted"
			}
			b.logger.log("MEMBER MUTE", fmt.Sprintf("%s %s in %s (%s)", m.User.Username, state, m.GuildID, m.User.ID))
		}

		if m.Deaf != m.BeforeUpdate.Deaf {
			state := "undeafened"
			if m.Deaf {
				state = "deafened"
			}
			b.logger.log("MEMBER DEAF", fmt.Sprintf("%s %s in %s (%s)", m.User.Username, state, m.GuildID, m.User.ID))
		}

		if m.Nick != m.BeforeUpdate.Nick {
			if m.Nick == "" {
				b.logger.log("NICK CHANGE", fmt.Sprintf("%s cleared their nickname in %s (%s)", m.User.Username, m.GuildID, m.User.ID))
			} else {
				b.logger.log("NICK CHANGE", fmt.Sprintf("%s nickname -> %q in %s (%s)", m.User.Username, m.Nick, m.GuildID, m.User.ID))
			}
		}
	}
}

// onGuildBanAdd logs bans.
func (b *Bot) onGuildBanAdd(s *discordgo.Session, u *discordgo.GuildBanAdd) {
	if u.GuildID != SunfireServerID {
		return
	}
	if b.logger != nil && channelID() != "" {
		b.logger.log("BAN", fmt.Sprintf("%s banned in %s (%s)", u.User.Username, u.GuildID, u.User.ID))
	}
}

// onGuildBanRemove logs unbans.
func (b *Bot) onGuildBanRemove(s *discordgo.Session, u *discordgo.GuildBanRemove) {
	if u.GuildID != SunfireServerID {
		return
	}
	if b.logger != nil && channelID() != "" {
		b.logger.log("UNBAN", fmt.Sprintf("%s unbanned in %s (%s)", u.User.Username, u.GuildID, u.User.ID))
	}
}

// callHermes invokes the Hermes CLI directly. It attempts to continue the latest conversational memory thread.
// If no previous session exists, it auto-heals and starts a fresh one to establish the baseline.
func callHermes(ctx context.Context, content string) (string, error) {
	return runHermes(ctx, content, false)
}

func callHermesNewSession(ctx context.Context, content string) (string, error) {
	return runHermes(ctx, content, true)
}

func runHermes(ctx context.Context, content string, newSession bool) (string, error) {
	hermesBinary, err := resolveHermesBinary()
	if err != nil {
		return "", err
	}
	args := hermesChatArgs(content, newSession)
	cmd := exec.CommandContext(ctx, hermesBinary, args...)
	cmd.Dir = applicationDir()

	var outBuf bytes.Buffer
	var errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	err = cmd.Run()
	errStr := strings.TrimSpace(errBuf.String())

	// Attempt 2: If Hermes fails because there is no previous session to continue,
	// run it normally to establish the very first session.
	if !newSession && err != nil && (strings.Contains(errStr, "not found") || strings.Contains(errStr, "No session") || strings.Contains(errStr, "unrecognized")) {
		cmdFallback := exec.CommandContext(ctx, hermesBinary, hermesChatArgs(content, true)...)
		cmdFallback.Dir = applicationDir()

		outBuf.Reset()
		errBuf.Reset()
		cmdFallback.Stdout = &outBuf
		cmdFallback.Stderr = &errBuf

		err = cmdFallback.Run()
		errStr = strings.TrimSpace(errBuf.String())
	}

	if ctx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("Hermes took too long and was terminated")
	}
	if err != nil {
		if errStr == "" {
			errStr = err.Error()
		}
		return "", fmt.Errorf("Hermes execution failed: %s", errStr)
	}

	response := strings.TrimSpace(outBuf.String())
	if response == "" {
		return "", fmt.Errorf("Hermes returned an empty response")
	}

	return response, nil
}

func hermesChatArgs(content string, newSession bool) []string {
	args := []string{"--yolo", "chat", "-q", content, "-Q", "--max-turns", "20"}
	if !newSession {
		args = append([]string{"--continue"}, args...)
	}
	return args
}

func hermesFailureMessage(err error) string {
	message := "Failed to contact Hermes: " + err.Error()
	errText := err.Error()
	if strings.Contains(errText, "Resume retains gateway session policy") ||
		strings.Contains(errText, "--max-turns differs from this session's launch") {
		message += "\n- To start a new session enter exactly: \"start-new-session\""
	}
	return message
}

func resolveHermesBinary() (string, error) {
	if hermesBinary := os.Getenv("SUNFIRE_HERMES_BIN"); hermesBinary != "" {
		return hermesBinary, nil
	}
	if hermesBinary, err := exec.LookPath("hermes"); err == nil {
		return hermesBinary, nil
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("Hermes executable not found on PATH, and could not determine service user's home directory: %w", err)
	}
	hermesBinary := filepath.Join(homeDir, ".local", "bin", "hermes")
	if path, err := exec.LookPath(hermesBinary); err == nil {
		return path, nil
	}

	return "", fmt.Errorf("Hermes executable not found on PATH or at %s; set SUNFIRE_HERMES_BIN to its absolute path", hermesBinary)
}

// sendSmartChunks splits a long response cleanly at newlines and preserves open Markdown code blocks.
func sendSmartChunks(s *discordgo.Session, channelID, text string) {
	const maxLen = 1950
	inCodeBlock := false

	lines := strings.Split(text, "\n")
	var currentChunk strings.Builder

	for _, line := range lines {
		// Toggle code block state if we see ```
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inCodeBlock = !inCodeBlock
		}

		// If adding this line exceeds the chunk limit, send the chunk and start a new one
		if currentChunk.Len()+len(line)+1 > maxLen {
			chunkStr := currentChunk.String()

			// Close the block temporarily if we are inside one so Discord formats it right
			if inCodeBlock {
				chunkStr += "\n```"
			}

			_, _ = s.ChannelMessageSend(channelID, chunkStr)
			currentChunk.Reset()

			// Re-open the block for the new chunk
			if inCodeBlock {
				currentChunk.WriteString("```\n")
			}
		}

		currentChunk.WriteString(line + "\n")
	}

	if currentChunk.Len() > 0 {
		_, _ = s.ChannelMessageSend(channelID, currentChunk.String())
	}
}

// onMessageCreate mirrors every server message to per-user JSON files
// and processes any media attachments into Data/MediaLogs/<GuildID>/.
func (b *Bot) onMessageCreate(s *discordgo.Session, m *discordgo.MessageCreate) {
	if m.GuildID != "" && m.GuildID != SunfireServerID {
		return
	}
	processMessageLogging(m)

	// Skip bot messages for games and automod
	if m.Author == nil || m.Author.Bot {
		_ = logMessageToFile(m)
		processMediaAttachments(m)
		return
	}

	// Check if this message solves an active riddle
	if checkRiddleAnswer(s, m) {
		_ = logMessageToFile(m)
		return
	}

	// AutoMod Check (deletes message and warns if blocked word is found)
	checkAutoMod(s, m)
	_ = logMessageToFile(m)
	processMediaAttachments(m)
}

// onMessageDelete logs when a message is deleted.
func (b *Bot) onMessageDelete(s *discordgo.Session, m *discordgo.MessageDelete) {
	if m == nil {
		return
	}
	guildID := m.GuildID
	if guildID == "" {
		if ch, err := s.State.Channel(m.ChannelID); err == nil && ch != nil {
			guildID = ch.GuildID
		}
	}
	if guildID != SunfireServerID || m.ChannelID == channelID() {
		return
	}

	// Update local JSON log and check for stored message details
	fileRec, _ := logMessageDeleteToFile(guildID, m.ID)

	var authorStr string
	var content string

	if m.BeforeDelete != nil {
		if m.BeforeDelete.Author != nil {
			authorStr = fmt.Sprintf("%s (%s)", m.BeforeDelete.Author.Username, m.BeforeDelete.Author.ID)
		}
		content = m.BeforeDelete.Content
	}

	if authorStr == "" && fileRec != nil {
		if fileRec.AuthorUsername != "" {
			authorStr = fmt.Sprintf("%s (%s)", fileRec.AuthorUsername, fileRec.AuthorID)
		}
		if content == "" {
			content = fileRec.Content
		}
	}

	if b.logger != nil && channelID() != "" {
		if authorStr != "" {
			if content != "" {
				b.logger.log("MESSAGE DELETE", fmt.Sprintf("Message by %s in <#%s> deleted:\n%s", authorStr, m.ChannelID, formatBlockQuote(content, 400)))
			} else {
				b.logger.log("MESSAGE DELETE", fmt.Sprintf("Message by %s in <#%s> deleted (ID: %s)", authorStr, m.ChannelID, m.ID))
			}
		} else {
			b.logger.log("MESSAGE DELETE", fmt.Sprintf("Message (%s) in <#%s> deleted", m.ID, m.ChannelID))
		}
	}
}

// onMessageDeleteBulk logs when multiple messages are deleted at once (e.g. purge).
func (b *Bot) onMessageDeleteBulk(s *discordgo.Session, m *discordgo.MessageDeleteBulk) {
	if m == nil {
		return
	}
	guildID := m.GuildID
	if guildID == "" {
		if ch, err := s.State.Channel(m.ChannelID); err == nil && ch != nil {
			guildID = ch.GuildID
		}
	}
	if guildID != SunfireServerID || m.ChannelID == channelID() {
		return
	}

	_ = logMessageBulkDeleteToFile(guildID, m.Messages)

	if b.logger != nil && channelID() != "" {
		b.logger.log("MESSAGE BULK DELETE", fmt.Sprintf("%d messages deleted in <#%s>", len(m.Messages), m.ChannelID))
	}
}

// onMessageUpdate logs when a message is edited.
func (b *Bot) onMessageUpdate(s *discordgo.Session, m *discordgo.MessageUpdate) {
	if m == nil {
		return
	}
	guildID := m.GuildID
	if guildID == "" {
		if ch, err := s.State.Channel(m.ChannelID); err == nil && ch != nil {
			guildID = ch.GuildID
		}
	}
	if guildID != SunfireServerID || m.ChannelID == channelID() {
		return
	}

	// Discord fires MessageUpdate when link unfurls/embeds load or pins change.
	// If m.Content is empty, it's not a text edit event.
	if m.Content == "" {
		return
	}

	var oldContent string
	var authorStr string

	if m.BeforeUpdate != nil {
		oldContent = m.BeforeUpdate.Content
		if m.BeforeUpdate.Author != nil {
			authorStr = fmt.Sprintf("%s (%s)", m.BeforeUpdate.Author.Username, m.BeforeUpdate.Author.ID)
		}
	}

	// Skip if before and after content are identical (e.g. embed preview was added)
	if m.BeforeUpdate != nil && oldContent == m.Content {
		return
	}

	fileRec, fileOldContent, _ := logMessageUpdateToFile(guildID, m.ID, m.Content)
	if oldContent == "" {
		oldContent = fileOldContent
	}

	// If oldContent was known and matches m.Content, skip
	if oldContent != "" && oldContent == m.Content {
		return
	}

	if authorStr == "" && m.Author != nil {
		authorStr = fmt.Sprintf("%s (%s)", m.Author.Username, m.Author.ID)
	}
	if authorStr == "" && fileRec != nil && fileRec.AuthorUsername != "" {
		authorStr = fmt.Sprintf("%s (%s)", fileRec.AuthorUsername, fileRec.AuthorID)
	}

	if b.logger != nil && channelID() != "" {
		jumpURL := fmt.Sprintf("https://discord.com/channels/%s/%s/%s", guildID, m.ChannelID, m.ID)
		if authorStr != "" {
			if oldContent != "" {
				b.logger.log("MESSAGE UPDATE", fmt.Sprintf("Message by %s in <#%s> edited ([Jump](<%s>)):\n• **Before:** %s\n• **After:** %s", authorStr, m.ChannelID, jumpURL, formatContentPreview(oldContent, 400), formatContentPreview(m.Content, 400)))
			} else {
				b.logger.log("MESSAGE UPDATE", fmt.Sprintf("Message by %s in <#%s> edited ([Jump](<%s>)):\n• **Now:** %s", authorStr, m.ChannelID, jumpURL, formatContentPreview(m.Content, 500)))
			}
		} else {
			b.logger.log("MESSAGE UPDATE", fmt.Sprintf("Message (%s) in <#%s> edited ([Jump](<%s>)):\n• **Now:** %s", m.ID, m.ChannelID, jumpURL, formatContentPreview(m.Content, 500)))
		}
	}
}

func (b *Bot) onGuildChannelCreate(s *discordgo.Session, ch *discordgo.ChannelCreate) {
	if ch.GuildID != SunfireServerID {
		return
	}
	if b.logger != nil && channelID() != "" {
		b.logger.log("CHAN CREATE", fmt.Sprintf("#%s (%s) created in %s", ch.Channel.Name, ch.Channel.ID, ch.GuildID))
	}
}

func (b *Bot) onGuildChannelUpdate(s *discordgo.Session, ch *discordgo.ChannelUpdate) {
	if ch.GuildID != SunfireServerID {
		return
	}
	if b.logger != nil && channelID() != "" {
		b.logger.log("CHAN UPDATE", fmt.Sprintf("#%s (%s) updated in %s", ch.Channel.Name, ch.Channel.ID, ch.GuildID))
	}
}

func (b *Bot) onGuildChannelDelete(s *discordgo.Session, ch *discordgo.ChannelDelete) {
	if ch.GuildID != SunfireServerID {
		return
	}
	if b.logger != nil && channelID() != "" {
		b.logger.log("CHAN DELETE", fmt.Sprintf("#%s (%s) deleted in %s", ch.Channel.Name, ch.Channel.ID, ch.GuildID))
	}
}

func (b *Bot) onUserUpdate(s *discordgo.Session, u *discordgo.UserUpdate) {
	if b.logger != nil && channelID() != "" {
		b.logger.log("USER UPDATE", fmt.Sprintf("%s (%s) username/avatar updated", u.User.Username, u.User.ID))
	}
}

func truncateString(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "..."
}

func formatBlockQuote(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) > maxLen {
		s = string(runes[:maxLen]) + "..."
	}
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = "> " + l
	}
	return strings.Join(lines, "\n")
}

func formatContentPreview(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) > maxLen {
		s = string(runes[:maxLen]) + "..."
	}
	if strings.Contains(s, "\n") {
		lines := strings.Split(s, "\n")
		for i, l := range lines {
			lines[i] = "> " + l
		}
		return "\n" + strings.Join(lines, "\n")
	}
	return fmt.Sprintf("%q", s)
}
