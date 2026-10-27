// commands.go — slash command definitions, registration, and handling.
package main

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"

	"sunfire/a2e"
	"sunfire/providers"
	"sunfire/venice"
)

// commandSpec couples a command definition with its handler.
type commandSpec struct {
	Command *discordgo.ApplicationCommand
	Handler func(s *discordgo.Session, i *discordgo.InteractionCreate)
}

// buildCommandSpecs populates the commandSpecs list.
// Split from init() because "init" is a reserved auto-call name in this Go variant.
var commandSpecs []commandSpec

func buildCommandSpecs() {
	commandSpecs = []commandSpec{
		{
			Command: &discordgo.ApplicationCommand{
				Name:        "help",
				Description: "Browse commands by category or find one by name.",
				Options: []*discordgo.ApplicationCommandOption{
					{
						Type:        discordgo.ApplicationCommandOptionString,
						Name:        "category",
						Description: "Browse a command category",
						Required:    false,
						Choices:     helpCategoryChoices(),
					},
					{
						Type:        discordgo.ApplicationCommandOptionString,
						Name:        "search",
						Description: "Find commands whose names contain this text (letters, numbers, _ or -)",
						Required:    false,
						MaxLength:   32,
					},
				},
			},
			Handler: handleHelp,
		},
		{
			Command: &discordgo.ApplicationCommand{
				Name:        "userinfo",
				Description: "Show available Discord profile, server, activity, and voice information.",
				Options: []*discordgo.ApplicationCommandOption{
					{Type: discordgo.ApplicationCommandOptionUser, Name: "user", Description: "The user to get info for", Required: false},
				},
			},
			Handler: handleUserInfo,
		},
		{
			Command: &discordgo.ApplicationCommand{
				Name:        "media",
				Description: "Apply an FFmpeg effect to an uploaded image, audio, or video file.",
				Options: []*discordgo.ApplicationCommandOption{
					{Type: discordgo.ApplicationCommandOptionAttachment, Name: "file", Description: "Media file to transform", Required: true},
				},
			},
			Handler: handleMedia,
		},
		{
			Command: &discordgo.ApplicationCommand{
				Name:        "emojigg",
				Description: "Browse downloaded Emoji.gg assets and add them to the server.",
				Options: []*discordgo.ApplicationCommandOption{
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "search",
						Description: "Find emoji assets by name or asset ID.",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionString, Name: "query", Description: "Name or Emoji.gg asset ID to search for", Required: true, MaxLength: 32},
							{Type: discordgo.ApplicationCommandOptionInteger, Name: "page", Description: "Page number when there are more than 15 matches", Required: false},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "list",
						Description: "Browse downloaded emoji assets by page.",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionInteger, Name: "page", Description: "Page number (15 assets per page)", Required: false},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "random",
						Description: "Preview a random downloaded emoji.",
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "preview",
						Description: "Preview a downloaded emoji by name or asset ID.",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionString, Name: "asset", Description: "Emoji name or Emoji.gg asset ID from search/list", Required: true, MaxLength: 64},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "add",
						Description: "Add a downloaded emoji to the server (Manage Expressions required).",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionString, Name: "asset", Description: "Emoji name or Emoji.gg asset ID from search/list", Required: true, MaxLength: 64},
							{Type: discordgo.ApplicationCommandOptionString, Name: "name", Description: "Optional server emoji name (letters, numbers, underscores)", Required: false, MaxLength: 32},
						},
					},
				},
			},
			Handler: handleEmojiGG,
		},
		{
			Command: &discordgo.ApplicationCommand{
				Name:        "purge",
				Description: "Purge messages (amount: number or 'all', max 10000).",
				Options: []*discordgo.ApplicationCommandOption{
					{Type: discordgo.ApplicationCommandOptionString, Name: "amount", Description: "Number of messages to purge, or 'all'", Required: true},
				},
			},
			Handler: handlePurge,
		},
		{Command: &discordgo.ApplicationCommand{Name: "setup", Description: "Create private Sunfire category/logs and read-only INFO/rules channels."}, Handler: handleSetup},
		{Command: &discordgo.ApplicationCommand{Name: "dedup", Description: "Run SHA-256 duplicate media cleanup on server media logs."}, Handler: handleDedup},
		{
			Command: &discordgo.ApplicationCommand{
				Name:        "grant-access",
				Description: "Grant or remove a member's access to one restricted command.",
				Options: []*discordgo.ApplicationCommandOption{
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "grant",
						Description: "Grant a member access to a restricted command.",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionString, Name: "command", Description: "Restricted command name (without /)", Required: true},
							{Type: discordgo.ApplicationCommandOptionUser, Name: "member", Description: "Member to grant access to", Required: true},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "remove",
						Description: "Remove a member's access to a restricted command.",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionString, Name: "command", Description: "Restricted command name (without /)", Required: true},
							{Type: discordgo.ApplicationCommandOptionUser, Name: "member", Description: "Member to remove access from", Required: true},
						},
					},
				},
			},
			Handler: handleGrantAccess,
		},

		// --- SYSTEM & LOGGING COMMANDS ---
		{Command: &discordgo.ApplicationCommand{Name: "sysinfo", Description: "Show live host system resources and uptime."}, Handler: handleSysInfo},
		{
			Command: &discordgo.ApplicationCommand{
				Name:        "remind",
				Description: "Add, list, and delete your reminders.",
				Options: []*discordgo.ApplicationCommandOption{
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "add",
						Description: "Schedule a reminder.",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionString, Name: "when", Description: "e.g. '10m', '1h', or '09/29/2026 at 14:30'", Required: true},
							{Type: discordgo.ApplicationCommandOptionString, Name: "message", Description: "What to remind you about", Required: true, MaxLength: 1800},
							{Type: discordgo.ApplicationCommandOptionString, Name: "repeat", Description: "Optional interval, e.g. '1d' or '1w'"},
							{Type: discordgo.ApplicationCommandOptionString, Name: "timezone", Description: "IANA timezone for local date/time (default: America/New_York)"},
						},
					},
					{Type: discordgo.ApplicationCommandOptionSubCommand, Name: "list", Description: "List your scheduled reminders."},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "delete",
						Description: "Delete one of your reminders.",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionString, Name: "id", Description: "Reminder ID shown by /remind list", Required: true},
						},
					},
				},
			},
			Handler: handleRemind,
		},
		{
			Command: &discordgo.ApplicationCommand{
				Name:        "searchlogs",
				Description: "Search a user's logged chat and edit history.",
				Options: []*discordgo.ApplicationCommandOption{
					{Type: discordgo.ApplicationCommandOptionUser, Name: "user", Description: "User to search", Required: true},
					{Type: discordgo.ApplicationCommandOptionString, Name: "keyword", Description: "Keyword to filter by", Required: false},
				},
			},
			Handler: handleSearchLogs,
		},
		{
			Command: &discordgo.ApplicationCommand{
				Name:        "warn",
				Description: "Issue a warning strike to a user.",
				Options: []*discordgo.ApplicationCommandOption{
					{Type: discordgo.ApplicationCommandOptionUser, Name: "user", Description: "User to warn", Required: true},
					{Type: discordgo.ApplicationCommandOptionString, Name: "reason", Description: "Reason for warning", Required: true},
				},
			},
			Handler: handleWarn,
		},
		{
			Command: &discordgo.ApplicationCommand{
				Name:        "automod",
				Description: "Configure automatic moderation for banned words and phrases.",
				Options: []*discordgo.ApplicationCommandOption{
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "banned-word-add",
						Description: "Add a word or phrase to the banned list.",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionString, Name: "word", Description: "Word or phrase to block", Required: true, MaxLength: 100},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "banned-word-remove",
						Description: "Remove a word or phrase from the banned list.",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionString, Name: "word", Description: "Word or phrase to unblock", Required: true, MaxLength: 100},
						},
					},
					{Type: discordgo.ApplicationCommandOptionSubCommand, Name: "banned-word-list", Description: "List the banned words and phrases."},
					{Type: discordgo.ApplicationCommandOptionSubCommand, Name: "enable", Description: "Enable automatic moderation."},
					{Type: discordgo.ApplicationCommandOptionSubCommand, Name: "disable", Description: "Disable automatic moderation."},
					{Type: discordgo.ApplicationCommandOptionSubCommand, Name: "status", Description: "Show AutoMod's status and banned-word count."},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "view",
						Description: "View a user's recent AutoMod trigger events.",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionUser, Name: "user", Description: "User whose AutoMod events to view", Required: true},
						},
					},
				},
			},
			Handler: handleAutoModCommand,
		},

		// --- ANALYTICS & MEDIA COMMANDS ---
		{
			Command: &discordgo.ApplicationCommand{
				Name:        "analytics",
				Description: "Generate a chat activity heatmap and statistics.",
			},
			Handler: handleAnalytics,
		},

		
		// --- CHANNEL MANAGEMENT COMMAND ---
		{
			Command: &discordgo.ApplicationCommand{
				Name:        "channel",
				Description: "Manage server channels.",
				Options: []*discordgo.ApplicationCommandOption{
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "create",
						Description: "Create a new channel.",
						Options: []*discordgo.ApplicationCommandOption{
							{
								Type:        discordgo.ApplicationCommandOptionString,
								Name:        "type",
								Description: "The type of channel",
								Required:    true,
								Choices: []*discordgo.ApplicationCommandOptionChoice{
									{Name: "Text", Value: "text"},
									{Name: "Voice", Value: "voice"},
									{Name: "Category", Value: "category"},
								},
							},
							{
								Type:        discordgo.ApplicationCommandOptionString,
								Name:        "name",
								Description: "The channel name",
								Required:    true,
							},
							{
								Type:         discordgo.ApplicationCommandOptionChannel,
								Name:         "in",
								Description:  "The parent category (ignored for category types)",
								Required:     false,
								ChannelTypes: []discordgo.ChannelType{discordgo.ChannelTypeGuildCategory},
							},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "delete",
						Description: "Delete a channel.",
						Options: []*discordgo.ApplicationCommandOption{
							{
								Type:        discordgo.ApplicationCommandOptionChannel,
								Name:        "target",
								Description: "The channel to delete",
								Required:    true,
							},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "rename",
						Description: "Rename a channel.",
						Options: []*discordgo.ApplicationCommandOption{
							{
								Type:        discordgo.ApplicationCommandOptionChannel,
								Name:        "target",
								Description: "The channel to rename",
								Required:    true,
							},
							{
								Type:        discordgo.ApplicationCommandOptionString,
								Name:        "name",
								Description: "The new channel name",
								Required:    true,
							},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "lock",
						Description: "Prevent @everyone from sending messages in a channel.",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionChannel, Name: "channel", Description: "The channel to lock", Required: true},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "unlock",
						Description: "Allow @everyone to send messages in a channel.",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionChannel, Name: "channel", Description: "The channel to unlock", Required: true},
						},
					},
				},
			},
			Handler: handleChannel,
		},

		// --- MODERATION COMMANDS ---
		{
			Command: &discordgo.ApplicationCommand{
				Name:        "timeout",
				Description: "Timeout a user.",
				Options: []*discordgo.ApplicationCommandOption{
					{Type: discordgo.ApplicationCommandOptionUser, Name: "user", Description: "User to timeout", Required: true},
					{Type: discordgo.ApplicationCommandOptionInteger, Name: "minutes", Description: "Duration in minutes", Required: true},
					{Type: discordgo.ApplicationCommandOptionString, Name: "reason", Description: "Reason for timeout", Required: false},
				},
			},
			Handler: handleTimeout,
		},
		{
			Command: &discordgo.ApplicationCommand{
				Name:        "kick",
				Description: "Kick a user from the server.",
				Options: []*discordgo.ApplicationCommandOption{
					{Type: discordgo.ApplicationCommandOptionUser, Name: "user", Description: "User to kick", Required: true},
					{Type: discordgo.ApplicationCommandOptionString, Name: "reason", Description: "Reason for kick", Required: false},
				},
			},
			Handler: handleKick,
		},
		{
			Command: &discordgo.ApplicationCommand{
				Name:        "ban",
				Description: "Add, remove, and view server bans.",
				Options: []*discordgo.ApplicationCommandOption{
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "add",
						Description: "Ban a user from the server.",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionUser, Name: "user", Description: "User to ban", Required: true},
							{Type: discordgo.ApplicationCommandOptionInteger, Name: "days", Description: "Days of message history to delete (0-7)", Required: false},
							{Type: discordgo.ApplicationCommandOptionString, Name: "reason", Description: "Reason for ban", Required: false},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "remove",
						Description: "Remove a user's ban.",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionString, Name: "user", Description: "Banned user's Discord ID (shown by /ban view)", Required: true},
						},
					},
					{Type: discordgo.ApplicationCommandOptionSubCommand, Name: "view", Description: "List banned users."},
				},
			},
			Handler: handleBan,
		},
		{
			Command: &discordgo.ApplicationCommand{
				Name:        "slowmode",
				Description: "Set channel slowmode.",
				Options: []*discordgo.ApplicationCommandOption{
					{Type: discordgo.ApplicationCommandOptionInteger, Name: "seconds", Description: "Slowmode delay in seconds (0 to disable)", Required: true},
				},
			},
			Handler: handleSlowmode,
		},

		// --- BACKUP & EXPORT COMMANDS ---
		{Command: &discordgo.ApplicationCommand{Name: "backup", Description: "Export full server configuration to a JSON file."}, Handler: handleBackup},
		{
			Command: &discordgo.ApplicationCommand{
				Name:        "transcript",
				Description: "Export chat history to a file.",
				Options: []*discordgo.ApplicationCommandOption{
					{Type: discordgo.ApplicationCommandOptionInteger, Name: "limit", Description: "Number of messages to export (max 5000)", Required: true},
				},
			},
			Handler: handleTranscript,
		},

		// --- DUAL CLOCK & WEATHER COMMAND ---

		// --- STATS COMMAND ---
		{Command: &discordgo.ApplicationCommand{Name: "serverstats", Description: "Display detailed server, channel, and media storage statistics."}, Handler: handleServerStats},

		// --- INTERNET ARCHIVE ---
		{
			Command: &discordgo.ApplicationCommand{
				Name:        "lyrics",
				Description: "Search Genius for song or album metadata and links.",
				Options: []*discordgo.ApplicationCommandOption{
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "fetch-song",
						Description: "Search Genius for a song; returns metadata and a link, not lyrics.",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionString, Name: "song", Description: "Song title, optionally with artist", Required: true, MaxLength: 200},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "fetch-album",
						Description: "Search Genius for albums by an artist, optionally narrowed to an album.",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionString, Name: "artist", Description: "Artist name", Required: true, MaxLength: 100},
							{Type: discordgo.ApplicationCommandOptionString, Name: "album", Description: "Optional album title", Required: false, MaxLength: 100},
						},
					},
				},
			},
			Handler: handleLyrics,
		},
		{
			Command: &discordgo.ApplicationCommand{
				Name:        "ia",
				Description: "Search and explore the Internet Archive (search, item, download, wayback, stats, fts).",
				Options: []*discordgo.ApplicationCommandOption{
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "search",
						Description: "Metadata search across the archive.",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionString, Name: "query", Description: "Search query (e.g. space, creator:Heinlein).", Required: true},
							{Type: discordgo.ApplicationCommandOptionString, Name: "type", Description: "Optional mediatype filter: audio, texts, movies, image, software, etree, web.", Required: false},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "item",
						Description: "Full metadata for an item by identifier.",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionString, Name: "identifier", Description: "Item identifier (e.g. xfetch).", Required: true},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "download",
						Description: "Post a file from an item (auto-picks a small one if no file given).",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionString, Name: "identifier", Description: "Item identifier.", Required: true},
							{Type: discordgo.ApplicationCommandOptionString, Name: "file", Description: "Optional filename within the item (use /ia item to list files).", Required: false},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "wayback",
						Description: "Check if the Wayback Machine archived a URL.",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionString, Name: "url", Description: "The URL to check (e.g. https://example.com).", Required: true},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "stats",
						Description: "View/download counts for an item.",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionString, Name: "identifier", Description: "Item identifier.", Required: true},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "fts",
						Description: "Full-text search across the archive.",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionString, Name: "query", Description: "Full-text search query.", Required: true},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "help",
						Description: "Show the /ia usage guide.",
					},
				},
			},
			Handler: handleIA,
		},

		// --- GAMES & SIMULATIONS ---
		{
			Command: &discordgo.ApplicationCommand{
				Name:        "dice",
				Description: "Simulate dice odds: your win chance vs roll 0-100, 10,000,000 rolls.",
				Options: []*discordgo.ApplicationCommandOption{
					{Type: discordgo.ApplicationCommandOptionString, Name: "chance", Description: "Your win chance (2.00 to 98.00). Win when roll < chance.", Required: true},
				},
			},
			Handler: handleDice,
		},
		{
			Command: &discordgo.ApplicationCommand{
				Name:        "notifiers",
				Description: "Manage media & stream notification feeds.",
				Options: []*discordgo.ApplicationCommandOption{
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "status",
						Description: "Show status of YouTube, Kick, Spotify, Movies, FreeStuff, & Scrape notifiers.",
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "trigger",
						Description: "Force an immediate poll pass for a notifier.",
						Options: []*discordgo.ApplicationCommandOption{
							{
								Type:        discordgo.ApplicationCommandOptionString,
								Name:        "service",
								Description: "The service to trigger",
								Required:    true,
								Choices: []*discordgo.ApplicationCommandOptionChoice{
									{Name: "YouTube", Value: "youtube"},
									{Name: "Kick", Value: "kick"},
									{Name: "Spotify", Value: "spotify"},
									{Name: "Movies (TMDB)", Value: "movies"},
									{Name: "FreeStuff (GG.deals)", Value: "freestuff"},
									{Name: "Hermes Tips", Value: "hermes"},
									{Name: "Image Scrape", Value: "scrape"},
								},
							},
						},
					},
				},
			},
			Handler: handleNotifiersCommand,
		},
		{
			Command: &discordgo.ApplicationCommand{
				Name:        "note",
				Description: "Manage private staff notes for users.",
				Options: []*discordgo.ApplicationCommandOption{
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "add",
						Description: "Add a note for a user",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionUser, Name: "user", Description: "Target user", Required: true},
							{Type: discordgo.ApplicationCommandOptionString, Name: "text", Description: "Note content", Required: true},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "view",
						Description: "View recent notes for a user",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionUser, Name: "user", Description: "Target user", Required: true},
							{Type: discordgo.ApplicationCommandOptionInteger, Name: "limit", Description: "How many notes to show (default 10)", Required: false},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "delete",
						Description: "Delete a note by ID",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionUser, Name: "user", Description: "Target user", Required: true},
							{Type: discordgo.ApplicationCommandOptionInteger, Name: "noteid", Description: "Note ID to delete", Required: true},
						},
					},
				},
			},
			Handler: handleNoteCommand,
		},
		{
			Command: &discordgo.ApplicationCommand{
				Name:        "role",
				Description: "Add or remove roles from server members.",
				Options: []*discordgo.ApplicationCommandOption{
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "add",
						Description: "Add a role to a member",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionUser, Name: "user", Description: "Target user", Required: true},
							{Type: discordgo.ApplicationCommandOptionRole, Name: "role", Description: "Role to add", Required: true},
							{Type: discordgo.ApplicationCommandOptionString, Name: "reason", Description: "Reason", Required: false},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "remove",
						Description: "Remove a role from a member",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionUser, Name: "user", Description: "Target user", Required: true},
							{Type: discordgo.ApplicationCommandOptionRole, Name: "role", Description: "Role to remove", Required: true},
							{Type: discordgo.ApplicationCommandOptionString, Name: "reason", Description: "Reason", Required: false},
						},
					},
				},
			},
			Handler: handleRoleCommand,
		},
		{
			Command: &discordgo.ApplicationCommand{
				Name:        "rolemenu",
				Description: "Create a dropdown select menu for self-assigned roles.",
				Options: []*discordgo.ApplicationCommandOption{
					{Type: discordgo.ApplicationCommandOptionString, Name: "title", Description: "Header text for the role menu", Required: true},
					{Type: discordgo.ApplicationCommandOptionString, Name: "pairs", Description: "Semicolon separated list (Label:RoleName; Label:@Role)", Required: true},
				},
			},
			Handler: handleRoleMenuCommand,
		},
		{
			Command: &discordgo.ApplicationCommand{
				Name:        "autorole",
				Description: "Configure automatic role assignment for new server members.",
				Options: []*discordgo.ApplicationCommandOption{
					{Type: discordgo.ApplicationCommandOptionSubCommand, Name: "view", Description: "View current auto-role"},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "set",
						Description: "Set auto-role for new members",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionRole, Name: "role", Description: "Role to assign", Required: true},
						},
					},
					{Type: discordgo.ApplicationCommandOptionSubCommand, Name: "clear", Description: "Disable auto-role"},
				},
			},
			Handler: handleAutoRoleCommand,
		},
		{
			Command: &discordgo.ApplicationCommand{
				Name:        "ticket-panel",
				Description: "Post a button panel allowing members to create private support tickets.",
			},
			Handler: handleTicketPanelCommand,
		},
		{
			Command: &discordgo.ApplicationCommand{
				Name:        "close-ticket",
				Description: "Close and archive the current support ticket thread.",
			},
			Handler: handleCloseTicketCommand,
		},
		{
			Command: &discordgo.ApplicationCommand{
				Name:        "pick",
				Description: "Pick random results from server data.",
				Options: []*discordgo.ApplicationCommandOption{
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "random-member",
						Description: "Pick one or more random members from this server.",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionInteger, Name: "count", Description: "How many members to pick (default: 1)", Required: false},
							{Type: discordgo.ApplicationCommandOptionBoolean, Name: "include-bots", Description: "Include bot accounts", Required: false},
							{Type: discordgo.ApplicationCommandOptionRole, Name: "role", Description: "Only pick members with this role", Required: false},
							{Type: discordgo.ApplicationCommandOptionString, Name: "exclude", Description: "Comma-separated user IDs or mentions to exclude", Required: false},
							{Type: discordgo.ApplicationCommandOptionInteger, Name: "countdown-minutes", Description: "Optional countdown timestamp in minutes", Required: false},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "random-role",
						Description: "Pick one or more random roles from this server.",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionInteger, Name: "count", Description: "How many roles to pick (default: 1)", Required: false},
							{Type: discordgo.ApplicationCommandOptionInteger, Name: "countdown-minutes", Description: "Optional countdown timestamp in minutes", Required: false},
						},
					},
				},
			},
			Handler: handlePickCommand,
		},
		{
			Command: &discordgo.ApplicationCommand{
				Name:        "riddle",
				Description: "Start or manage a multi-round riddle game in this channel.",
				Options: []*discordgo.ApplicationCommandOption{
					{Type: discordgo.ApplicationCommandOptionSubCommand, Name: "start", Description: "Start a new 10-round riddle game in this channel"},
					{Type: discordgo.ApplicationCommandOptionSubCommand, Name: "stop", Description: "Stop the active riddle game in this channel"},
					{Type: discordgo.ApplicationCommandOptionSubCommand, Name: "status", Description: "Show status of current riddle round"},
				},
			},
			Handler: handleRiddleCommand,
		},
		{
			Command: &discordgo.ApplicationCommand{
				Name:        "battle",
				Description: "Manage Sweet Dreams multiplayer battles.",
				Options: []*discordgo.ApplicationCommandOption{
					{Type: discordgo.ApplicationCommandOptionSubCommand, Name: "start", Description: "Start a Sweet Dreams battle!"},
					{Type: discordgo.ApplicationCommandOptionSubCommand, Name: "join", Description: "Join an open Sweet Dreams battle!"},
				},
			},
			Handler: handleBattleCommand,
		},
		{
			Command: &discordgo.ApplicationCommand{
				Name:        "fallen-star",
				Description: "Play the Fallen Star dark fantasy CYOA text adventure game.",
				Options: []*discordgo.ApplicationCommandOption{
					{Type: discordgo.ApplicationCommandOptionSubCommand, Name: "start", Description: "Start a new Fallen Star adventure"},
					{Type: discordgo.ApplicationCommandOptionSubCommand, Name: "status", Description: "Resume or view current adventure status"},
				},
			},
			Handler: handleFallenStarCommand,
		},

		// --- COMMUNITY & UTILITY (Ported from ElyAdmin) ---
		{
			Command: &discordgo.ApplicationCommand{
				Name:        "birthday",
				Description: "Register your birthday and let staff view server birthdays.",
				Options: []*discordgo.ApplicationCommandOption{
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "set",
						Description: "Set your birthday one time. This cannot be edited later.",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionInteger, Name: "month", Description: "Birth month (1-12)", Required: true},
							{Type: discordgo.ApplicationCommandOptionInteger, Name: "day", Description: "Birth day (1-31)", Required: true},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "view",
						Description: "View birthdays registered in this server.",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionInteger, Name: "month", Description: "Optional month filter (1-12)", Required: false},
						},
					},
				},
			},
			Handler: handleBirthday,
		},
		{
			Command: &discordgo.ApplicationCommand{
				Name:        "translate",
				Description: "Translate text to a target language.",
				Options: []*discordgo.ApplicationCommandOption{
					{Type: discordgo.ApplicationCommandOptionString, Name: "text", Description: "Text to translate", Required: true},
					{Type: discordgo.ApplicationCommandOptionString, Name: "to", Description: "Target language code, e.g. en, es, fr, de, tl, ja", Required: true},
				},
			},
			Handler: handleTranslate,
		},
		{
			Command: &discordgo.ApplicationCommand{
				Name:        "message-log",
				Description: "Manage which channels are logged to Postgres, and search logged messages.",
				Options: []*discordgo.ApplicationCommandOption{
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "add",
						Description: "Start logging a text channel",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionChannel, Name: "channel", Description: "Text channel to add to message logging", Required: true},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "remove",
						Description: "Stop logging a text channel",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionChannel, Name: "channel", Description: "Text channel to remove from message logging", Required: true},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "list",
						Description: "Show channels currently being logged",
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "search",
						Description: "Search logged messages by text",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionString, Name: "query", Description: "Text to search for", Required: true},
							{Type: discordgo.ApplicationCommandOptionChannel, Name: "channel", Description: "Restrict search to a channel", Required: false},
							{Type: discordgo.ApplicationCommandOptionUser, Name: "user", Description: "Restrict search to a user", Required: false},
							{Type: discordgo.ApplicationCommandOptionInteger, Name: "limit", Description: "Max results (1-25)", Required: false},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "stats",
						Description: "Show message logging stats (total messages, servers, channels)",
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "fetch",
						Description: "Return the full logged message as a .md file attachment",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionString, Name: "messageid", Description: "Message ID to fetch (from a search result)", Required: true},
						},
					},
				},
			},
			Handler: handleMessageLog,
		},
	}

	commandSpecs = append(commandSpecs, openRouterCommandSpec())

	for _, spec := range a2e.CommandSpecs() {
		commandSpecs = append(commandSpecs, commandSpec{Command: spec.Command, Handler: spec.Handler})
	}
	for _, spec := range venice.CommandSpecs() {
		commandSpecs = append(commandSpecs, commandSpec{Command: spec.Command, Handler: spec.Handler})
	}
	for _, spec := range providers.CommandSpecs() {
		// Skip the providers package's openrouter command — openrouter.go's
		// /openrouter (models, deals, model, compare, endpoints, etc.)
		// replaces it with more features.
		if spec.Command.Name == "openrouter" {
			continue
		}
		commandSpecs = append(commandSpecs, commandSpec{Command: spec.Command, Handler: spec.Handler})
	}
}

// registerCommands syncs the slash commands directly to Sunfire's server.
// It also clears any global slash commands previously registered so commands do not duplicate.
func registerCommands(s *discordgo.Session) error {
	// 1. Wipe any previously registered global commands (prevents duplicates in the server)
	if _, err := s.ApplicationCommandBulkOverwrite(s.State.User.ID, "", []*discordgo.ApplicationCommand{}); err != nil {
		logErrorf("warning: failed to clear global commands: %v", err)
	}

	var cmds []*discordgo.ApplicationCommand
	// Extract just the Discord command structures from our specs
	for _, spec := range commandSpecs {
		cmds = append(cmds, spec.Command)
	}

	// 2. Register/overwrite commands specifically for SunfireServerID
	_, err := s.ApplicationCommandBulkOverwrite(s.State.User.ID, SunfireServerID, cmds)
	if err != nil {
		logErrorf("failed to bulk overwrite guild commands: %v", err)
		return err
	}

	return nil
}

func handleHelp(s *discordgo.Session, i *discordgo.InteractionCreate) {
	category, search := "", ""
	for _, option := range i.ApplicationCommandData().Options {
		switch option.Name {
		case "category":
			category = strings.ToLower(option.StringValue())
		case "search":
			search = strings.ToLower(strings.TrimSpace(option.StringValue()))
		}
	}
	if !validHelpSearch(search) {
		respondHelpError(s, i, "Search must contain only letters, numbers, underscores, or hyphens.")
		return
	}
	if category != "" && !validHelpCategory(category) {
		respondHelpError(s, i, "That help category is not available. Use `/help` to browse categories.")
		return
	}
	respondHelpPage(s, i, category, 0, search, false)
}

type helpCategory struct {
	Value              string
	Label              string
	Commands           []string
	IncludedCategories []string
}

var helpCategories = []helpCategory{
	{Value: "a2e", Label: "A2E AI", Commands: []string{"a2e"}},
	{Value: "archive", Label: "Internet Archive", Commands: []string{"ia"}},
	{Value: "cloudflare", Label: "Cloudflare", Commands: []string{"cloudflare"}},
	{Value: "cohere", Label: "Cohere", Commands: []string{"cohere"}},
	{Value: "core", Label: "Core (System, Moderation, Media)"},
	{Value: "groq", Label: "Groq", Commands: []string{"groq"}},
	{Value: "moderation", Label: "Moderation & Staff", Commands: []string{
		"automod", "warn", "timeout", "kick", "ban", "unban", "mute", "unmute", "purge", "note",
		"role", "rolemenu", "autorole", "ticket-panel", "close-ticket", "message-log", "backup", "channel",
		"dedup", "grant-access", "searchlogs", "setup",
		"slowmode",
	}},
	{Value: "mistral", Label: "Mistral", Commands: []string{"mistral"}},
	{Value: "notifiers", Label: "Media & Stream Notifiers", Commands: []string{"notifiers"}},
	{Value: "nvidia", Label: "NVIDIA", Commands: []string{"nvidia"}},
	{Value: "openrouter", Label: "OpenRouter", Commands: []string{"openrouter"}},
	{Value: "providers", Label: "LLM Providers", IncludedCategories: []string{
		"cloudflare", "cohere", "groq", "mistral", "nvidia", "openrouter",
	}},
	{Value: "venice", Label: "Venice AI", Commands: []string{"venice"}},
}

func helpCategoryChoices() []*discordgo.ApplicationCommandOptionChoice {
	choices := make([]*discordgo.ApplicationCommandOptionChoice, 0, len(helpCategories))
	for _, category := range helpCategories {
		choices = append(choices, &discordgo.ApplicationCommandOptionChoice{Name: category.Label, Value: category.Value})
	}
	return choices
}

func helpCategoryLabel(value string) string {
	for _, category := range helpCategories {
		if category.Value == value {
			return category.Label
		}
	}
	return "Commands"
}

func validHelpCategory(value string) bool {
	for _, category := range helpCategories {
		if category.Value == value {
			return true
		}
	}
	return false
}

func validHelpSearch(search string) bool {
	if len(search) > 32 {
		return false
	}
	for _, r := range search {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' && r != '-' {
			return false
		}
	}
	return true
}

func helpCommandCategory(name string) string {
	for _, category := range helpCategories {
		for _, command := range category.Commands {
			if command == name {
				return category.Value
			}
		}
	}
	return "core"
}

func helpCategoryMatches(category, commandCategory string) bool {
	for _, definition := range helpCategories {
		if definition.Value != category {
			continue
		}
		if category == commandCategory {
			return true
		}
		for _, included := range definition.IncludedCategories {
			if included == commandCategory {
				return true
			}
		}
	}
	return false
}

func helpOptionTypeName(optionType discordgo.ApplicationCommandOptionType) string {
	switch optionType {
	case discordgo.ApplicationCommandOptionSubCommand:
		return "subcommand"
	case discordgo.ApplicationCommandOptionSubCommandGroup:
		return "subcommand group"
	case discordgo.ApplicationCommandOptionString:
		return "string"
	case discordgo.ApplicationCommandOptionInteger:
		return "integer"
	case discordgo.ApplicationCommandOptionBoolean:
		return "boolean"
	case discordgo.ApplicationCommandOptionUser:
		return "user"
	case discordgo.ApplicationCommandOptionChannel:
		return "channel"
	case discordgo.ApplicationCommandOptionRole:
		return "role"
	case discordgo.ApplicationCommandOptionMentionable:
		return "mentionable"
	case discordgo.ApplicationCommandOptionNumber:
		return "number"
	case discordgo.ApplicationCommandOptionAttachment:
		return "attachment"
	default:
		return "option"
	}
}

func helpCommandEntry(spec commandSpec) string {
	cmd := spec.Command
	description := cmd.Description
	if cmd.Name == "grant-access" {
		description += " *(Owner only)*"
	} else if !isPublicCommand(cmd.Name) {
		description += " *(Owner only unless access is granted)*"
	}
	var lines []string
	lines = append(lines, fmt.Sprintf("• **/%s** — %s", cmd.Name, description))
	lines = append(lines, helpOptionLines("/"+cmd.Name, cmd.Options, "  ")...)
	return strings.Join(lines, "\n")
}

func helpOptionLines(commandPath string, options []*discordgo.ApplicationCommandOption, indent string) []string {
	var lines []string
	for _, option := range options {
		path := commandPath + " " + option.Name
		switch option.Type {
		case discordgo.ApplicationCommandOptionSubCommand, discordgo.ApplicationCommandOptionSubCommandGroup:
			lines = append(lines, fmt.Sprintf("%s↳ **`%s`** — %s", indent, path, option.Description))
			lines = append(lines, helpOptionLines(path, option.Options, indent+"  ")...)
		default:
			required := "optional"
			if option.Required {
				required = "required"
			}
			line := fmt.Sprintf("%s• `%s` (%s, %s): %s", indent, path, helpOptionTypeName(option.Type), required, option.Description)
			var constraints []string
			if option.MinValue != nil || option.MaxValue != 0 {
				min, max := "any", "any"
				if option.MinValue != nil {
					min = fmt.Sprintf("%g", *option.MinValue)
				}
				if option.MaxValue != 0 {
					max = fmt.Sprintf("%g", option.MaxValue)
				}
				constraints = append(constraints, fmt.Sprintf("Range: %s to %s", min, max))
			}
			if option.MinLength != nil || option.MaxLength > 0 {
				min, max := "any", "any"
				if option.MinLength != nil {
					min = fmt.Sprint(*option.MinLength)
				}
				if option.MaxLength > 0 {
					max = fmt.Sprint(option.MaxLength)
				}
				constraints = append(constraints, fmt.Sprintf("Length: %s to %s characters", min, max))
			}
			if option.Autocomplete {
				constraints = append(constraints, "autocomplete")
			}
			if len(option.Choices) > 0 {
				choices := make([]string, 0, len(option.Choices))
				for _, choice := range option.Choices {
					label := choice.Name
					if fmt.Sprint(choice.Value) != label {
						label = fmt.Sprintf("%s (%v)", label, choice.Value)
					}
					choices = append(choices, label)
				}
				line += " Choices: " + strings.Join(choices, ", ")
			}
			if len(constraints) > 0 {
				line += " (" + strings.Join(constraints, "; ") + ")"
			}
			lines = append(lines, line)
		}
	}
	return lines
}

func helpEntries(category, search string) []string {
	var entries []string
	for _, spec := range commandSpecs {
		name := strings.ToLower(spec.Command.Name)
		commandCategory := helpCommandCategory(name)
		if category != "" && !helpCategoryMatches(category, commandCategory) {
			continue
		}
		if search != "" && !strings.Contains(name, search) {
			continue
		}
		entries = append(entries, helpCommandEntry(spec))
	}
	sort.Strings(entries)
	return entries
}

const helpPageLimit = 1800

func splitHelpLine(line string, limit int) []string {
	runes := []rune(line)
	if len(runes) <= limit {
		return []string{line}
	}
	parts := make([]string, 0, (len(runes)+limit-1)/limit)
	for len(runes) > 0 {
		n := limit
		if len(runes) < n {
			n = len(runes)
		}
		parts = append(parts, string(runes[:n]))
		runes = runes[n:]
	}
	return parts
}

func helpContentPages(category, search string) []string {
	if category == "" && search == "" {
		var lines []string
		counts := make(map[string]int)
		for _, spec := range commandSpecs {
			counts[helpCommandCategory(spec.Command.Name)]++
		}
		for _, categoryInfo := range helpCategories {
			count := counts[categoryInfo.Value]
			for _, provider := range categoryInfo.IncludedCategories {
				count += counts[provider]
			}
			if count > 0 {
				lines = append(lines, fmt.Sprintf("• **%s** — %d commands", categoryInfo.Label, count))
			}
		}
		return []string{strings.Join(lines, "\n") + "\n\nChoose a category below, or use `/help search:<name>` to find a command."}
	}

	entries := helpEntries(category, search)
	if len(entries) == 0 {
		return []string{"No commands found. Try a different category or search term."}
	}
	title := helpCategoryLabel(category)
	if search != "" {
		title = "Search: " + search
	}
	header := fmt.Sprintf("**Sunfire Commands — %s**\n", title)
	bodyLimit := helpPageLimit - len([]rune(header)) - 48
	var pages []string
	current := ""
	flush := func() {
		if current != "" {
			pages = append(pages, header+current)
			current = ""
		}
	}
	for _, entry := range entries {
		for _, line := range strings.Split(entry, "\n") {
			for _, part := range splitHelpLine(line, bodyLimit) {
				part += "\n"
				if len([]rune(current))+len([]rune(part)) > bodyLimit {
					flush()
				}
				current += part
			}
		}
		current += "\n"
	}
	flush()
	if len(pages) == 0 {
		return []string{header}
	}
	return pages
}

func helpComponents(category string, page, pageCount int, search string) []discordgo.MessageComponent {
	options := make([]discordgo.SelectMenuOption, 0, len(helpCategories))
	for _, item := range helpCategories {
		options = append(options, discordgo.SelectMenuOption{Label: item.Label, Value: item.Value, Default: item.Value == category})
	}
	rows := []discordgo.MessageComponent{
		discordgo.ActionsRow{Components: []discordgo.MessageComponent{
			discordgo.SelectMenu{CustomID: "help:category", Placeholder: "Browse command categories", Options: options},
		}},
	}
	if pageCount > 1 {
		rows = append(rows, discordgo.ActionsRow{Components: []discordgo.MessageComponent{
			discordgo.Button{Label: "Previous", Style: discordgo.PrimaryButton, CustomID: helpPageCustomID(category, page-1, search), Disabled: page == 0},
			discordgo.Button{Label: fmt.Sprintf("%d / %d", page+1, pageCount), Style: discordgo.SecondaryButton, CustomID: "help:page:noop", Disabled: true},
			discordgo.Button{Label: "Next", Style: discordgo.PrimaryButton, CustomID: helpPageCustomID(category, page+1, search), Disabled: page+1 >= pageCount},
		}})
	}
	return rows
}

func helpPageCustomID(category string, page int, search string) string {
	if category == "" {
		category = "all"
	}
	return fmt.Sprintf("help:page:%s:%d:%s", category, page, search)
}

func respondHelpError(s *discordgo.Session, i *discordgo.InteractionCreate, message string) {
	if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Content: message, Flags: discordgo.MessageFlagsEphemeral},
	}); err != nil {
		logErrorf("failed to respond to /help: %v", err)
	}
}

func respondHelpPage(s *discordgo.Session, i *discordgo.InteractionCreate, category string, page int, search string, update bool) {
	pages := helpContentPages(category, search)
	if page < 0 {
		page = 0
	}
	if page >= len(pages) {
		page = len(pages) - 1
	}
	content := pages[page]
	if len(pages) > 1 {
		content += fmt.Sprintf("\n*Page %d of %d*", page+1, len(pages))
	}
	data := &discordgo.InteractionResponseData{Content: content, Components: helpComponents(category, page, len(pages), search)}
	responseType := discordgo.InteractionResponseChannelMessageWithSource
	if update {
		responseType = discordgo.InteractionResponseUpdateMessage
	}
	if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{Type: responseType, Data: data}); err != nil {
		logErrorf("failed to render /help category=%q page=%d: %v", category, page, err)
	}
}

func handleHelpComponent(s *discordgo.Session, i *discordgo.InteractionCreate) {
	data := i.MessageComponentData()
	if data.CustomID == "help:category" {
		if len(data.Values) != 1 || !validHelpCategory(data.Values[0]) {
			respondHelpError(s, i, "That help category is not available. Use `/help` to browse categories.")
			return
		}
		respondHelpPage(s, i, data.Values[0], 0, "", true)
		return
	}
	parts := strings.Split(data.CustomID, ":")
	if len(parts) != 5 || parts[0] != "help" || parts[1] != "page" {
		respondHelpError(s, i, "This help control is invalid. Run `/help` again.")
		return
	}
	category := parts[2]
	if category == "all" {
		category = ""
	} else if !validHelpCategory(category) {
		respondHelpError(s, i, "This help category is no longer available. Run `/help` again.")
		return
	}
	page, err := strconv.Atoi(parts[3])
	search := parts[4]
	if err != nil || !validHelpSearch(search) {
		respondHelpError(s, i, "This help control is invalid. Run `/help` again.")
		return
	}
	respondHelpPage(s, i, category, page, search, true)
}

// handleUserInfo displays the profile and guild information Discord makes
// available to this bot for the selected user.
func handleUserInfo(s *discordgo.Session, i *discordgo.InteractionCreate) {
	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	})

	var targetUser *discordgo.User
	if options := i.ApplicationCommandData().Options; len(options) > 0 {
		targetUser = options[0].UserValue(s)
	} else if i.Member != nil {
		targetUser = i.Member.User
	} else {
		targetUser = i.User
	}
	if targetUser == nil || targetUser.ID == "" {
		_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{Content: "❌ Discord did not provide a user for this request."})
		return
	}

	// The user endpoint may add profile fields omitted from the interaction's
	// resolved user object; retain the resolved object if Discord rejects it.
	if fullUser, err := s.User(targetUser.ID); err == nil && fullUser != nil {
		targetUser = fullUser
	} else if err != nil {
		logErrorf("could not fetch full Discord profile for user %s: %v", targetUser.ID, err)
	}

	var member *discordgo.Member
	var guild *discordgo.Guild
	if i.GuildID != "" {
		var err error
		member, err = s.GuildMember(i.GuildID, targetUser.ID)
		if err != nil && i.Member != nil && i.Member.User != nil && i.Member.User.ID == targetUser.ID {
			member = i.Member
		}
		if err != nil && member == nil {
			logErrorf("could not fetch guild membership for user %s in guild %s: %v", targetUser.ID, i.GuildID, err)
		}
		guild, _ = s.State.Guild(i.GuildID)
		if guild == nil {
			var err error
			guild, err = s.Guild(i.GuildID)
			if err != nil {
				logErrorf("could not fetch guild %s for /userinfo: %v", i.GuildID, err)
			}
		}
	}

	embedColor := targetUser.AccentColor
	if embedColor == 0 {
		embedColor = 0xFF6B35
	}
	embed := &discordgo.MessageEmbed{
		Color: embedColor,
		Author: &discordgo.MessageEmbedAuthor{
			Name:    fmt.Sprintf("%s's Profile", targetUser.DisplayName()),
			IconURL: targetUser.AvatarURL("1024"),
		},
		Thumbnail: &discordgo.MessageEmbedThumbnail{URL: targetUser.AvatarURL("1024")},
		Fields:    []*discordgo.MessageEmbedField{},
	}
	if member != nil && member.Avatar != "" {
		embed.Thumbnail.URL = member.AvatarURL("1024")
	}
	if targetUser.Banner != "" {
		embed.Image = &discordgo.MessageEmbedImage{URL: targetUser.BannerURL("1024")}
	}

	accountType := "User 👤"
	if targetUser.Bot {
		accountType = "Bot 🤖"
	}
	if targetUser.System {
		accountType = "Discord System ⚙️"
	}
	created, err := discordgo.SnowflakeTimestamp(targetUser.ID)
	desc := fmt.Sprintf(
		"**Display name:** %s\n**Username:** `%s`\n**User ID:** `%s`\n**Account type:** %s\n",
		targetUser.DisplayName(), targetUser.Username, targetUser.ID, accountType,
	)
	if err == nil {
		desc += fmt.Sprintf("**Created:** <t:%d:f> (<t:%d:R>)\n", created.Unix(), created.Unix())
	}
	if targetUser.Discriminator != "" && targetUser.Discriminator != "0" {
		desc += fmt.Sprintf("**Legacy discriminator:** `%s`\n", targetUser.Discriminator)
	}
	if targetUser.Locale != "" {
		desc += fmt.Sprintf("**Locale:** `%s`\n", targetUser.Locale)
	}
	if targetUser.AccentColor != 0 {
		desc += fmt.Sprintf("**Profile accent:** `#%06X`\n", targetUser.AccentColor)
	}
	if targetUser.PremiumType != discordgo.UserPremiumTypeNone {
		desc += fmt.Sprintf("**Discord premium:** %s\n", userPremiumTypeName(targetUser.PremiumType))
	}
	desc += fmt.Sprintf("**Avatar:** [open profile image](%s)", targetUser.AvatarURL("1024"))
	if member != nil && member.Avatar != "" {
		desc += fmt.Sprintf("\n**Server avatar:** [open server image](%s)", member.AvatarURL("1024"))
	}
	if targetUser.Banner != "" {
		desc += fmt.Sprintf("\n**Banner:** [open banner image](%s)", targetUser.BannerURL("1024"))
	}
	embed.Description = truncateUserInfo(desc, 4000)

	if i.GuildID != "" {
		presence, presenceErr := s.State.Presence(i.GuildID, targetUser.ID)
		activityValue := formatUserPresence(presence, presenceErr)
		voiceValue := formatUserVoiceState(guild, targetUser.ID)
		embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
			Name:   "📡 Presence & Voice",
			Value:  truncateUserInfo("**Presence:** "+activityValue+"\n**Voice:** "+voiceValue, 1024),
			Inline: false,
		})
	}

	if member != nil {
		embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
			Name:   "📥 Server Profile",
			Value:  truncateUserInfo(formatGuildMemberInfo(s, i, targetUser, member, guild), 1024),
			Inline: false,
		})
	}

	badges := getUserBadges(int(targetUser.PublicFlags))
	badgeValue := "No public badges or flags."
	if len(badges) > 0 {
		badgeValue = strings.Join(badges, "\n")
	}
	embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
		Name:   "🎖️ Public Badges & Flags",
		Value:  truncateUserInfo(badgeValue, 1024),
		Inline: false,
	})

	_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{Embeds: []*discordgo.MessageEmbed{embed}})
}

func userPremiumTypeName(premiumType discordgo.UserPremiumType) string {
	switch premiumType {
	case discordgo.UserPremiumTypeNitroClassic:
		return "Nitro Classic"
	case discordgo.UserPremiumTypeNitro:
		return "Nitro"
	case discordgo.UserPremiumTypeNitroBasic:
		return "Nitro Basic"
	default:
		return "Unknown"
	}
}

func formatUserPresence(presence *discordgo.Presence, err error) string {
	if err != nil || presence == nil {
		return "Offline or unavailable (presence intent/data may be unavailable)."
	}
	status := "Unknown"
	switch presence.Status {
	case discordgo.StatusOnline:
		status = "🟢 Online"
	case discordgo.StatusIdle:
		status = "🌙 Idle"
	case discordgo.StatusDoNotDisturb:
		status = "🔴 Do Not Disturb"
	case discordgo.StatusInvisible:
		status = "⚫ Invisible"
	case discordgo.StatusOffline:
		status = "⚫ Offline"
	}
	var details []string
	devices := []struct {
		name   string
		status discordgo.Status
	}{
		{"Desktop", presence.ClientStatus.Desktop},
		{"Mobile", presence.ClientStatus.Mobile},
		{"Web", presence.ClientStatus.Web},
	}
	var activeDevices []string
	for _, device := range devices {
		if device.status != "" && device.status != discordgo.StatusOffline {
			activeDevices = append(activeDevices, device.name+": "+statusLabel(device.status))
		}
	}
	if len(activeDevices) > 0 {
		details = append(details, "Clients: "+strings.Join(activeDevices, ", "))
	}
	for _, activity := range presence.Activities {
		if activity == nil {
			continue
		}
		details = append(details, formatUserActivity(activity))
	}
	if len(details) == 0 {
		return status
	}
	return status + "\n" + strings.Join(details, "\n")
}

func statusLabel(status discordgo.Status) string {
	switch status {
	case discordgo.StatusOnline:
		return "online"
	case discordgo.StatusIdle:
		return "idle"
	case discordgo.StatusDoNotDisturb:
		return "DND"
	case discordgo.StatusInvisible:
		return "invisible"
	case discordgo.StatusOffline:
		return "offline"
	default:
		return string(status)
	}
}

func formatUserActivity(activity *discordgo.Activity) string {
	prefix := "•"
	switch activity.Type {
	case discordgo.ActivityTypeGame:
		prefix = "🎮 Playing"
	case discordgo.ActivityTypeStreaming:
		prefix = "📡 Streaming"
	case discordgo.ActivityTypeListening:
		prefix = "🎵 Listening"
	case discordgo.ActivityTypeWatching:
		prefix = "📺 Watching"
	case discordgo.ActivityTypeCustom:
		prefix = "💬 Status"
	case discordgo.ActivityTypeCompeting:
		prefix = "🏆 Competing"
	}
	label := activity.Name
	if activity.Type == discordgo.ActivityTypeCustom && activity.State != "" {
		label = activity.State
	}
	value := prefix
	if label != "" {
		value += " **" + label + "**"
	}
	if activity.Details != "" {
		value += "\n  " + activity.Details
	}
	if activity.State != "" && !(activity.Type == discordgo.ActivityTypeCustom && activity.State == label) {
		value += "\n  " + activity.State
	}
	if activity.URL != "" && activity.Type == discordgo.ActivityTypeStreaming {
		value += "\n  " + activity.URL
	}
	if activity.Assets.LargeText != "" {
		value += "\n  " + activity.Assets.LargeText
	}
	if activity.Assets.SmallText != "" {
		value += " · " + activity.Assets.SmallText
	}
	if activity.Timestamps.StartTimestamp > 0 {
		value += fmt.Sprintf("\n  Started <t:%d:R>", activity.Timestamps.StartTimestamp/1000)
	}
	return value
}

func formatUserVoiceState(guild *discordgo.Guild, userID string) string {
	if guild == nil {
		return "Unavailable."
	}
	for _, voice := range guild.VoiceStates {
		if voice.UserID != userID {
			continue
		}
		value := fmt.Sprintf("🔊 <#%s>", voice.ChannelID)
		var states []string
		if voice.SelfMute {
			states = append(states, "self-muted")
		}
		if voice.Mute {
			states = append(states, "server-muted")
		}
		if voice.SelfDeaf {
			states = append(states, "self-deafened")
		}
		if voice.Deaf {
			states = append(states, "server-deafened")
		}
		if voice.SelfStream {
			states = append(states, "streaming")
		}
		if voice.SelfVideo {
			states = append(states, "camera on")
		}
		if voice.Suppress {
			states = append(states, "suppressed")
		}
		if voice.RequestToSpeakTimestamp != nil {
			states = append(states, fmt.Sprintf("requested to speak <t:%d:R>", voice.RequestToSpeakTimestamp.Unix()))
		}
		if len(states) > 0 {
			value += " (" + strings.Join(states, ", ") + ")"
		}
		return value
	}
	return "Not connected to voice."
}

func formatGuildMemberInfo(s *discordgo.Session, i *discordgo.InteractionCreate, user *discordgo.User, member *discordgo.Member, guild *discordgo.Guild) string {
	var lines []string
	if guild != nil && guild.OwnerID == user.ID {
		lines = append(lines, "👑 **Server owner**")
	}
	if member.Nick != "" {
		lines = append(lines, "**Server nickname:** "+member.Nick)
	}
	if !member.JoinedAt.IsZero() {
		lines = append(lines, fmt.Sprintf("**Joined:** <t:%d:f> (<t:%d:R>)", member.JoinedAt.Unix(), member.JoinedAt.Unix()))
	}
	if member.Pending {
		lines = append(lines, "**Membership screening:** Pending")
	}
	if member.Flags&discordgo.MemberFlagCompletedOnboarding != 0 {
		lines = append(lines, "**Onboarding:** Completed")
	} else if member.Flags&discordgo.MemberFlagStartedOnboarding != 0 {
		lines = append(lines, "**Onboarding:** Started")
	}
	if member.Flags&discordgo.MemberFlagDidRejoin != 0 {
		lines = append(lines, "**Membership:** Has left and rejoined")
	}
	if member.Flags&discordgo.MemberFlagBypassesVerification != 0 {
		lines = append(lines, "**Verification:** Bypasses guild verification")
	}
	if member.PremiumSince != nil {
		lines = append(lines, fmt.Sprintf("**Boosting since:** <t:%d:f> (<t:%d:R>) 💎", member.PremiumSince.Unix(), member.PremiumSince.Unix()))
	}
	if member.CommunicationDisabledUntil != nil && member.CommunicationDisabledUntil.After(time.Now()) {
		lines = append(lines, fmt.Sprintf("**Timed out until:** <t:%d:f> (<t:%d:R>) 🛑", member.CommunicationDisabledUntil.Unix(), member.CommunicationDisabledUntil.Unix()))
	}
	if member.Deaf || member.Mute {
		lines = append(lines, fmt.Sprintf("**Server voice state:** Muted: %t · Deafened: %t", member.Mute, member.Deaf))
	}

	if permissions, err := s.UserChannelPermissions(user.ID, i.ChannelID); err == nil {
		names := effectivePermissionNames(permissions)
		if len(names) == 0 {
			lines = append(lines, fmt.Sprintf("**Permissions in <#%s>:** No elevated permissions", i.ChannelID))
		} else {
			lines = append(lines, fmt.Sprintf("**Permissions in <#%s>:** %s", i.ChannelID, strings.Join(names, ", ")))
		}
	} else {
		logErrorf("could not calculate channel permissions for user %s in channel %s: %v", user.ID, i.ChannelID, err)
	}

	roleNames, highestRole := guildMemberRoleNames(member, guild)
	lines = append(lines, fmt.Sprintf("**Roles (%d):** %s", len(member.Roles), roleNames))
	if highestRole != "" {
		lines = append(lines, "**Highest role:** "+highestRole)
	}
	if member.Avatar != "" {
		lines = append(lines, fmt.Sprintf("[Server avatar](%s)", member.AvatarURL("1024")))
	}
	if member.Banner != "" {
		lines = append(lines, fmt.Sprintf("[Server profile banner](%s)", member.BannerURL("1024")))
	}
	return strings.Join(lines, "\n")
}

func effectivePermissionNames(permissions int64) []string {
	if permissions&discordgo.PermissionAdministrator != 0 {
		return []string{"Administrator (all permissions)"}
	}
	permissionsToShow := []struct {
		flag int64
		name string
	}{
		{discordgo.PermissionManageGuild, "Manage Server"},
		{discordgo.PermissionManageRoles, "Manage Roles"},
		{discordgo.PermissionManageChannels, "Manage Channels"},
		{discordgo.PermissionManageMessages, "Manage Messages"},
		{discordgo.PermissionManageThreads, "Manage Threads"},
		{discordgo.PermissionKickMembers, "Kick Members"},
		{discordgo.PermissionBanMembers, "Ban Members"},
		{discordgo.PermissionModerateMembers, "Timeout Members"},
		{discordgo.PermissionViewAuditLogs, "View Audit Log"},
		{discordgo.PermissionManageWebhooks, "Manage Webhooks"},
		{discordgo.PermissionManageNicknames, "Manage Nicknames"},
		{discordgo.PermissionMentionEveryone, "Mention Everyone"},
		{discordgo.PermissionUseApplicationCommands, "Use Application Commands"},
		{discordgo.PermissionManageEvents, "Manage Events"},
		{discordgo.PermissionVoiceMoveMembers, "Move Members"},
		{discordgo.PermissionVoiceMuteMembers, "Mute Members"},
		{discordgo.PermissionVoiceDeafenMembers, "Deafen Members"},
		{discordgo.PermissionSendMessages, "Send Messages"},
		{discordgo.PermissionAttachFiles, "Attach Files"},
		{discordgo.PermissionVoiceConnect, "Connect to Voice"},
		{discordgo.PermissionVoiceSpeak, "Speak"},
	}
	var names []string
	for _, permission := range permissionsToShow {
		if permissions&permission.flag != 0 {
			names = append(names, permission.name)
		}
	}
	return names
}

func guildMemberRoleNames(member *discordgo.Member, guild *discordgo.Guild) (string, string) {
	if len(member.Roles) == 0 {
		return "None", ""
	}
	rolesByID := make(map[string]*discordgo.Role)
	if guild != nil {
		for _, role := range guild.Roles {
			if role != nil {
				rolesByID[role.ID] = role
			}
		}
	}
	roles := make([]*discordgo.Role, 0, len(member.Roles))
	for _, roleID := range member.Roles {
		if role := rolesByID[roleID]; role != nil {
			roles = append(roles, role)
		}
	}
	sort.Slice(roles, func(a, b int) bool { return roles[a].Position > roles[b].Position })

	var lines []string
	highest := ""
	for _, role := range roles {
		if highest == "" {
			highest = role.Name
		}
		line := "• " + role.Name
		if role.Managed {
			line += " *(managed)*"
		}
		if role.Hoist {
			line += " *(displayed separately)*"
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return fmt.Sprintf("%d role(s; names unavailable)", len(member.Roles)), ""
	}
	return truncateUserInfo(strings.Join(lines, "\n"), 850), highest
}

func truncateUserInfo(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit-1]) + "…"
}

// getUserBadges uses bitwise operations to detect official Discord badges/flags on the account.
func getUserBadges(flags int) []string {
	var badges []string
	if flags&(1<<0) != 0 {
		badges = append(badges, "• Discord Employee")
	}
	if flags&(1<<1) != 0 {
		badges = append(badges, "• Partnered Server Owner")
	}
	if flags&(1<<2) != 0 {
		badges = append(badges, "• HypeSquad Events Member")
	}
	if flags&(1<<3) != 0 {
		badges = append(badges, "• Bug Hunter Level 1")
	}
	if flags&(1<<6) != 0 {
		badges = append(badges, "• House Bravery Member")
	}
	if flags&(1<<7) != 0 {
		badges = append(badges, "• House Brilliance Member")
	}
	if flags&(1<<8) != 0 {
		badges = append(badges, "• House Balance Member")
	}
	if flags&(1<<9) != 0 {
		badges = append(badges, "• Early Nitro Supporter")
	}
	if flags&(1<<10) != 0 {
		badges = append(badges, "• Team User")
	}
	if flags&(1<<14) != 0 {
		badges = append(badges, "• Bug Hunter Level 2")
	}
	if flags&(1<<16) != 0 {
		badges = append(badges, "• Verified Bot")
	}
	if flags&(1<<17) != 0 {
		badges = append(badges, "• Early Verified Bot Developer")
	}
	if flags&(1<<18) != 0 {
		badges = append(badges, "• Discord Certified Moderator")
	}
	if flags&(1<<19) != 0 {
		badges = append(badges, "• Bot uses HTTP interactions")
	}
	if flags&(1<<22) != 0 {
		badges = append(badges, "• Active Developer")
	}
	return badges
}

func logErrorf(format string, args ...interface{}) {
	fmt.Printf("ERROR: "+format+"\n", args...)
}

func coalesce(s ...string) string {
	for _, str := range s {
		if str != "" {
			return str
		}
	}
	return ""
}

func accentColor(c int) int {
	if c == 0 {
		return 0xFF6B35 // fallback orange
	}
	return c
}

func snowflakeToTimestamp(id string) int64 {
	var snow int64
	fmt.Sscanf(id, "%d", &snow)
	return ((snow >> 22) + 1420070400000) / 1000
}

func handlePurge(s *discordgo.Session, i *discordgo.InteractionCreate) {
	amountStr := ""
	channelID := i.ChannelID
	if i.ApplicationCommandData().Options != nil {
		for _, opt := range i.ApplicationCommandData().Options {
			if opt.Name == "amount" {
				amountStr = opt.StringValue()
			}
		}
	}

	// Confirm the purge before proceeding (optional safety)
	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	})

	var toDelete int
	if amountStr == "all" || amountStr == "" {
		// Purge all messages in the channel
		toDelete = 1000000 // sentinel for "all"
	} else {
		_, err := fmt.Sscanf(amountStr, "%d", &toDelete)
		if err != nil || toDelete < 1 || toDelete > 10000 {
			s.ChannelMessageSend(i.ChannelID, "❌ Amount must be a number between 1 and 10000, or 'all'.")
			return
		}
	}

	var messages []string // message IDs to delete
	beforeID := ""
	maxFetch := 100

	loopCount := 0
	for {
		if toDelete != 1000000 && len(messages) >= toDelete {
			break
		}
		if loopCount > 500 { // safety break
			break
		}
		loopCount++
		limit := maxFetch
		if toDelete != 1000000 && toDelete-len(messages) < limit {
			limit = toDelete - len(messages)
		}
		if limit <= 0 {
			break
		}

		msgs, err := s.ChannelMessages(channelID, limit, beforeID, "", "")
		if err != nil || len(msgs) == 0 {
			break
		}
		for _, msg := range msgs {
			messages = append(messages, msg.ID)
			beforeID = msg.ID
		}
	}

	deletedCount := 0
	// Bulk delete in chunks of 99 (Discord bulk delete max = 100, but safe at 99)
	idx := 0
	for idx < len(messages) {
		end := idx + 99
		if end > len(messages) {
			end = len(messages)
		}
		chunk := messages[idx:end]

		// Try bulk delete
		err := s.ChannelMessagesBulkDelete(channelID, chunk)
		if err != nil {
			// Bulk failed (likely >14 days for some), fall back to single deletes for this chunk
			for _, id := range chunk {
				err2 := s.ChannelMessageDelete(channelID, id)
				if err2 == nil {
					deletedCount++
				}
			}
		} else {
			deletedCount += len(chunk)
		}
		idx = end
	}

	s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
		Content: fmt.Sprintf("✅ Purged %d messages.", deletedCount),
	})
}

func handleSetup(s *discordgo.Session, i *discordgo.InteractionCreate) {
	guildID := i.GuildID
	if guildID == "" {
		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{Content: "❌ /setup must be run in a server."},
		})
		return
	}

	// Defer response to avoid Discord's 3-second interaction timeout during multiple API calls.
	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	})

	respondMsg := func(content string) {
		_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
			Content: content,
		})
	}

	guild, err := s.Guild(guildID)
	if err != nil {
		respondMsg("❌ Could not fetch guild info: " + err.Error())
		return
	}

	var everyoneRoleID string
	for _, r := range guild.Roles {
		if r.Name == "@everyone" {
			everyoneRoleID = r.ID
			break
		}
	}
	if everyoneRoleID == "" {
		everyoneRoleID = guild.ID
	}

	cfg, err := loadConfig()
	if err != nil {
		cfg = &Config{}
	}

	cfgModified := false
	var statusMessages []string

	// 1. Private Sunfire category + chats/logs channels.
	// If loggingChannelId already exists in config.json, do not recreate them.
	if cfg.LoggingChannel != "" {
		statusMessages = append(statusMessages, fmt.Sprintf("• **Logs channel**: <#%s> (already exists in config, skipped creation)", cfg.LoggingChannel))
	} else {
		categoryName := "Sunfire"
		category, err := s.GuildChannelCreateComplex(guildID, discordgo.GuildChannelCreateData{
			Name: categoryName,
			Type: discordgo.ChannelTypeGuildCategory,
			PermissionOverwrites: []*discordgo.PermissionOverwrite{
				{
					Type:  discordgo.PermissionOverwriteTypeRole,
					ID:    everyoneRoleID,
					Allow: 0,
					Deny:  1024 | 2048,
				},
			},
		})
		if err != nil {
			respondMsg("❌ Failed to create Sunfire category: " + err.Error())
			return
		}

		_, err = s.GuildChannelCreateComplex(guildID, discordgo.GuildChannelCreateData{
			Name:                 "chats",
			Type:                 discordgo.ChannelTypeGuildText,
			ParentID:             category.ID,
			PermissionOverwrites: category.PermissionOverwrites,
		})
		if err != nil {
			respondMsg("❌ Failed to create chats channel: " + err.Error())
			return
		}

		logsChannel, err := s.GuildChannelCreateComplex(guildID, discordgo.GuildChannelCreateData{
			Name:                 "logs",
			Type:                 discordgo.ChannelTypeGuildText,
			ParentID:             category.ID,
			PermissionOverwrites: category.PermissionOverwrites,
		})
		if err != nil {
			respondMsg("❌ Failed to create logs channel: " + err.Error())
			return
		}

		cfg.LoggingChannel = logsChannel.ID
		cfgModified = true
		statusMessages = append(statusMessages, fmt.Sprintf("• **Sunfire category**: created private category with #chats and #logs (<#%s>)", logsChannel.ID))
	}

	// Prepare permission overwrites for read-only INFO category & rules channel
	botUserID := ""
	if s.State != nil && s.State.User != nil {
		botUserID = s.State.User.ID
	}
	if botUserID == "" {
		if me, err := s.User("@me"); err == nil && me != nil {
			botUserID = me.ID
		}
	}

	infoOverwrites := []*discordgo.PermissionOverwrite{
		{
			Type:  discordgo.PermissionOverwriteTypeRole,
			ID:    everyoneRoleID,
			Allow: discordgo.PermissionViewChannel | discordgo.PermissionReadMessageHistory,
			Deny:  discordgo.PermissionSendMessages | discordgo.PermissionSendMessagesInThreads | discordgo.PermissionCreatePublicThreads | discordgo.PermissionCreatePrivateThreads | discordgo.PermissionAddReactions,
		},
	}
	if botUserID != "" {
		infoOverwrites = append(infoOverwrites, &discordgo.PermissionOverwrite{
			Type:  discordgo.PermissionOverwriteTypeMember,
			ID:    botUserID,
			Allow: discordgo.PermissionViewChannel | discordgo.PermissionSendMessages | discordgo.PermissionReadMessageHistory | discordgo.PermissionEmbedLinks,
			Deny:  0,
		})
	}

	// 2. Read-only INFO category.
	// If infoCategoryId already exists in config.json, do not recreate it.
	infoCategoryID := cfg.InfoCategoryID
	if infoCategoryID != "" {
		statusMessages = append(statusMessages, fmt.Sprintf("• **INFO category**: <#%s> (already exists in config, skipped creation)", infoCategoryID))
	} else {
		infoCategory, err := s.GuildChannelCreateComplex(guildID, discordgo.GuildChannelCreateData{
			Name:                 "INFO",
			Type:                 discordgo.ChannelTypeGuildCategory,
			PermissionOverwrites: infoOverwrites,
		})
		if err != nil {
			respondMsg("❌ Failed to create INFO category: " + err.Error())
			return
		}
		infoCategoryID = infoCategory.ID
		cfg.InfoCategoryID = infoCategory.ID
		cfgModified = true
		statusMessages = append(statusMessages, fmt.Sprintf("• **INFO category**: created read-only category (<#%s>)", infoCategory.ID))
	}

	// 3. Rules channel under INFO category and 1-time rules message.
	// If rulesChannelId already exists in config.json, do not recreate it or repost rules message.
	if cfg.RulesChannelID != "" {
		statusMessages = append(statusMessages, fmt.Sprintf("• **Rules channel**: <#%s> (already exists in config, rules message not reposted)", cfg.RulesChannelID))
	} else {
		rulesChannel, err := s.GuildChannelCreateComplex(guildID, discordgo.GuildChannelCreateData{
			Name:                 "rules",
			Type:                 discordgo.ChannelTypeGuildText,
			ParentID:             infoCategoryID,
			PermissionOverwrites: infoOverwrites,
		})
		if err != nil {
			respondMsg("❌ Failed to create rules channel: " + err.Error())
			return
		}

		cfg.RulesChannelID = rulesChannel.ID
		cfgModified = true

		// Post 1-time rules message in the rules channel
		rulesEmbed := &discordgo.MessageEmbed{
			Title: "📜 Server Rules & Guidelines",
			Description: "Welcome to **Sunfire**! To ensure a safe, welcoming, and enjoyable community for everyone, all members are required to follow these rules.\n\n" +
				"By participating in this server, you also agree to adhere to Discord's official policies:\n" +
				"• **Discord Terms of Service**: https://discord.com/terms\n" +
				"• **Discord Community Guidelines**: https://discord.com/guidelines",
			Color: 0xFF6B35,
			Fields: []*discordgo.MessageEmbedField{
				{
					Name:   "1. Be Respectful & Civil",
					Value:  "Treat all members with courtesy and respect. Harassment, hate speech, discrimination, personal attacks, slurs, or toxic behavior will not be tolerated.",
					Inline: false,
				},
				{
					Name:   "2. Discord Terms of Service & Community Guidelines",
					Value:  "You must comply with the [Discord Terms of Service](https://discord.com/terms) and [Discord Community Guidelines](https://discord.com/guidelines) at all times.",
					Inline: false,
				},
				{
					Name:   "3. No Spam or Flooding",
					Value:  "Do not spam messages, emojis, reactions, mentions, or post unsolicited self-promotion, advertisements, or invite links.",
					Inline: false,
				},
				{
					Name:   "4. Appropriate Content",
					Value:  "Keep content safe and appropriate. No NSFW, sexually explicit, gory, graphic, or illegal content.",
					Inline: false,
				},
				{
					Name:   "5. Privacy & Safety",
					Value:  "Respect personal privacy. Do not share personal identifying information (doxxing) or suspicious links.",
					Inline: false,
				},
				{
					Name:   "6. Follow Staff Directions",
					Value:  "Moderators and administrators have final discretion on server enforcement. Please cooperate with server staff.",
					Inline: false,
				},
			},
			Footer: &discordgo.MessageEmbedFooter{
				Text: "Sunfire • Please abide by these rules to keep our community safe and fun.",
			},
		}

		_, err = s.ChannelMessageSendComplex(rulesChannel.ID, &discordgo.MessageSend{
			Content: "**Welcome to the server!** Please read and follow our community rules:\n" +
				"• **Discord Terms of Service**: https://discord.com/terms\n" +
				"• **Discord Community Guidelines**: https://discord.com/guidelines",
			Embeds: []*discordgo.MessageEmbed{rulesEmbed},
		})
		if err != nil {
			logErrorf("failed to send rules message to channel %s: %v", rulesChannel.ID, err)
		}

		statusMessages = append(statusMessages, fmt.Sprintf("• **Rules channel**: created <#%s> and posted rules message", rulesChannel.ID))
	}

	// 4. Feeds & Media Category and Channels (YouTube, Spotify, Movies, FreeStuff, Kick).
	gCfg := getGuildSettings(guildID)
	gCfgModified := false

	feedsCategoryName := "Feeds & Media"
	var feedsCategoryID string

	// Check if an existing feeds category already exists in the guild
	guildChannels, err := s.GuildChannels(guildID)
	if err == nil {
		for _, ch := range guildChannels {
			if ch.Type == discordgo.ChannelTypeGuildCategory && strings.EqualFold(ch.Name, feedsCategoryName) {
				feedsCategoryID = ch.ID
				break
			}
		}
	}

	if feedsCategoryID == "" {
		feedsCat, err := s.GuildChannelCreateComplex(guildID, discordgo.GuildChannelCreateData{
			Name:                 feedsCategoryName,
			Type:                 discordgo.ChannelTypeGuildCategory,
			PermissionOverwrites: infoOverwrites,
		})
		if err == nil && feedsCat != nil {
			feedsCategoryID = feedsCat.ID
			statusMessages = append(statusMessages, fmt.Sprintf("• **Feeds category**: created read-only category (<#%s>)", feedsCat.ID))
		}
	}

	createFeedChannel := func(channelName string, currentID string) (string, bool) {
		if currentID != "" {
			// Check if channel still exists
			if ch, err := s.Channel(currentID); err == nil && ch != nil {
				return currentID, false
			}
		}
		newCh, err := s.GuildChannelCreateComplex(guildID, discordgo.GuildChannelCreateData{
			Name:                 channelName,
			Type:                 discordgo.ChannelTypeGuildText,
			ParentID:             feedsCategoryID,
			PermissionOverwrites: infoOverwrites,
		})
		if err != nil || newCh == nil {
			return currentID, false
		}
		return newCh.ID, true
	}

	// YouTube Channel
	if newID, created := createFeedChannel("youtube", gCfg.YouTubeChannelID); created {
		gCfg.YouTubeChannelID = newID
		gCfg.YouTubeEnabled = true
		gCfgModified = true
		statusMessages = append(statusMessages, fmt.Sprintf("• **YouTube channel**: created <#%s>", newID))
	} else if gCfg.YouTubeChannelID != "" {
		statusMessages = append(statusMessages, fmt.Sprintf("• **YouTube channel**: <#%s> (exists)", gCfg.YouTubeChannelID))
	}

	// Spotify Channel
	if newID, created := createFeedChannel("spotify", gCfg.SpotifyChannelID); created {
		gCfg.SpotifyChannelID = newID
		gCfg.SpotifyEnabled = true
		gCfgModified = true
		statusMessages = append(statusMessages, fmt.Sprintf("• **Spotify channel**: created <#%s>", newID))
	} else if gCfg.SpotifyChannelID != "" {
		statusMessages = append(statusMessages, fmt.Sprintf("• **Spotify channel**: <#%s> (exists)", gCfg.SpotifyChannelID))
	}

	// Movies Channel
	if newID, created := createFeedChannel("movies", gCfg.MoviesThisWeekChannelID); created {
		gCfg.MoviesThisWeekChannelID = newID
		gCfg.StreamingThisWeekChannelID = newID
		gCfgModified = true
		statusMessages = append(statusMessages, fmt.Sprintf("• **Movies channel**: created <#%s>", newID))
	} else if gCfg.MoviesThisWeekChannelID != "" {
		statusMessages = append(statusMessages, fmt.Sprintf("• **Movies channel**: <#%s> (exists)", gCfg.MoviesThisWeekChannelID))
	}

	// FreeStuff Channel
	if newID, created := createFeedChannel("free-stuff", gCfg.FreeStuffChannelID); created {
		gCfg.FreeStuffChannelID = newID
		gCfg.FreeStuffEnabled = true
		gCfgModified = true
		statusMessages = append(statusMessages, fmt.Sprintf("• **FreeStuff channel**: created <#%s>", newID))
	} else if gCfg.FreeStuffChannelID != "" {
		statusMessages = append(statusMessages, fmt.Sprintf("• **FreeStuff channel**: <#%s> (exists)", gCfg.FreeStuffChannelID))
	}

	// Kick Streams Channel
	if newID, created := createFeedChannel("kick-streams", gCfg.KickChannelID); created {
		gCfg.KickChannelID = newID
		gCfg.KickEnabled = true
		gCfgModified = true
		statusMessages = append(statusMessages, fmt.Sprintf("• **Kick streams channel**: created <#%s>", newID))
	} else if gCfg.KickChannelID != "" {
		statusMessages = append(statusMessages, fmt.Sprintf("• **Kick streams channel**: <#%s> (exists)", gCfg.KickChannelID))
	}

	if gCfgModified {
		_ = saveGuildSettings()
	}

	if cfgModified {
		if err := saveConfig(configPath(), cfg); err != nil {
			logErrorf("failed to save config: %v", err)
			respondMsg("⚠️️ Channels created, but failed to save config: " + err.Error())
			return
		}
	}

	resultContent := "✅ **Setup Summary:**\n"
	for _, msg := range statusMessages {
		resultContent += msg + "\n"
	}
	respondMsg(resultContent)
}

func handleDedup(s *discordgo.Session, i *discordgo.InteractionCreate) {
	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	})

	deleted, freed, err := DeduplicateAllMediaLogs()
	if err != nil {
		_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
			Content: fmt.Sprintf("❌ Deduplication error: %v", err),
		})
		return
	}

	var content string
	if deleted == 0 {
		content = "✅ **Media Deduplication:** No duplicate files found across server media folders."
	} else {
		content = fmt.Sprintf("✅ **Media Deduplication Complete:** Removed **%d** duplicate file(s) (freed **%.2f MB**).", deleted, float64(freed)/(1024*1024))
	}

	_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
		Content: content,
	})
}

func handleNotifiersCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	data := i.ApplicationCommandData()
	if len(data.Options) == 0 {
		return
	}

	sub := data.Options[0]
	cfg, err := loadConfig()
	if err != nil {
		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{Content: "❌ Failed to load config."},
		})
		return
	}

	switch sub.Name {
	case "status":
		guilds := loadGuildSettings()
		var gInfo string
		for gID, g := range guilds {
			gInfo += fmt.Sprintf("• **Guild %s**:\n  - YouTube: %t (Chan: %s, YouTubers: %d)\n  - Kick: %t (Chan: %s, Streamers: %d)\n  - Spotify: %t (Chan: %s)\n  - Movies: Chan: %s\n  - FreeStuff: %t (Chan: %s)\n",
				gID, g.YouTubeEnabled, g.YouTubeChannelID, len(g.YouTubers),
				g.KickEnabled, g.KickChannelID, len(g.KickStreamers),
				g.SpotifyEnabled, g.SpotifyChannelID,
				g.MoviesThisWeekChannelID,
				g.FreeStuffEnabled, g.FreeStuffChannelID,
			)
		}
		if gInfo == "" {
			gInfo = "No per-guild notifier channels configured."
		}

		embed := &discordgo.MessageEmbed{
			Title: "🔔 Media & Stream Notifiers Status",
			Description: fmt.Sprintf("**Global Credentials:**\n• YouTube API: %t\n• Kick API: %t\n• TMDB API: %t\n• Spotify API: %t\n• Scrape Targets: %d URLs -> %d Channels\n\n**Guild Configurations:**\n%s",
				cfg.YouTubeAPIKey != "",
				cfg.KickClientID != "",
				cfg.TMDBAPIKey != "",
				cfg.SpotifyClientID != "",
				len(cfg.Scrape.URLs), len(cfg.Scrape.ChannelIDs),
				gInfo,
			),
			Color: 0x3498DB,
		}

		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Embeds: []*discordgo.MessageEmbed{embed},
			},
		})

	case "trigger":
		service := ""
		for _, o := range sub.Options {
			if o.Name == "service" {
				service = o.StringValue()
			}
		}

		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: fmt.Sprintf("⚡ Triggering poll pass for **%s**...", service),
			},
		})

		go func() {
			switch service {
			case "youtube":
				checkYouTubeUpdates(s, cfg)
			case "kick":
				checkKickUpdates(s, cfg)
			case "spotify":
				checkSpotifyUpdates(s, cfg)
			case "movies":
				checkMoviesUpdates(s, cfg)
			case "freestuff":
				checkFreeStuffUpdates(s, cfg)
			case "hermes":
				_, _ = checkHermesTipsUpdates(s)
			case "scrape":
				runDailyImageScrape(s, cfg)
			}
		}()
	}
}
