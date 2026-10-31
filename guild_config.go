package main

import (
	"sync"
)

// GuildSettings defines per-guild configuration for ElyAdmin features ported into Sunfire.
type GuildSettings struct {
	LockdownExemptRoleIDs        []string               `json:"lockdownExemptRoleIds"`
	DefaultSlowmodeSeconds       int                    `json:"defaultSlowmodeSeconds"`
	ModLogChannelID              string                 `json:"modLogChannelId"`
	CommandRoleAllowlist         map[string]interface{} `json:"commandRoleAllowlist"`
	BirthdayAnnouncementChannelID string                `json:"birthdayAnnouncementChannelId"`
	TicketChannelID              string                 `json:"ticketChannelId"`
	TicketStaffRoleID            string                 `json:"ticketStaffRoleId"`
	AutoRoleID                   *string                `json:"autoRoleId"`
	WelcomeChannelID             *string                `json:"welcomeChannelId"`
	FarewellChannelID            *string                `json:"farewellChannelId"`
	WelcomeTemplate              string                 `json:"welcomeTemplate"`
	FarewellTemplate             string                 `json:"farewellTemplate"`
	RaidProtectionEnabled        bool                   `json:"raidProtectionEnabled"`
	StarboardChannelID           *string                `json:"starboardChannelId"`
	StarboardThreshold           int                    `json:"starboardThreshold"`
	KickEnabled                  bool                   `json:"kickEnabled"`
	KickChannelID                string                 `json:"kickChannelId"`
	KickStreamers                []string               `json:"kickStreamers"`
	KickStreamingRoleID          *string                `json:"kickStreamingRoleId"`
	MoviesThisWeekChannelID      string                 `json:"moviesThisWeekChannelId"`
	MoviesThisWeekRoleID         *string                `json:"moviesThisWeekRoleId"`
	StreamingThisWeekChannelID   string                 `json:"streamingThisWeekChannelId"`
	StreamingThisWeekRoleID      *string                `json:"streamingThisWeekRoleId"`
	MinimumAccountAgeDays        *int                   `json:"minimumAccountAgeDays"`
	YouTubeEnabled               bool                   `json:"youtubeEnabled"`
	YouTubeChannelID             string                 `json:"youtubeChannelId"`
	YouTubeRoleID                *string                `json:"youtubeRoleId"`
	YouTubers                    []string               `json:"youtubers"`
	SpotifyChannelID             string                 `json:"spotifyChannelId"`
	SpotifyRoleID                *string                `json:"spotifyRoleId"`
	SpotifyEnabled               bool                   `json:"spotifyEnabled"`
	FreeStuffEnabled             bool                   `json:"freeStuffEnabled"`
	FreeStuffChannelID           string                 `json:"freeStuffChannelId"`
	FreeStuffRoleID              *string                `json:"freeStuffRoleId"`
	DiscordChangeLogEnabled      bool                   `json:"discordChangeLogEnabled"`
	DiscordChangeLogChannelID    string                 `json:"discordChangeLogChannelId"`
	DiscordChangeLogRoleID       *string                `json:"discordChangeLogRoleId"`
}

var (
	guildSettingsMu sync.RWMutex
	guildSettings   map[string]*GuildSettings
)

// loadGuildSettings loads the guild settings from .private/config.json into memory.
func loadGuildSettings() map[string]*GuildSettings {
	guildSettingsMu.Lock()
	defer guildSettingsMu.Unlock()

	if guildSettings != nil {
		return guildSettings
	}

	guildSettings = make(map[string]*GuildSettings)
	cfg, err := loadConfig()
	if err == nil && cfg != nil {
		guildSettings[SunfireServerID] = &cfg.Guild
	} else {
		guildSettings[SunfireServerID] = &GuildSettings{}
	}

	return guildSettings
}

// getGuildSettings retrieves or creates settings for a given guild ID (Sunfire single-server).
func getGuildSettings(guildID string) *GuildSettings {
	all := loadGuildSettings()
	guildSettingsMu.Lock()
	defer guildSettingsMu.Unlock()

	if g, ok := all[guildID]; ok && g != nil {
		return g
	}

	// For single-server bot, default to the main guild settings
	if g, ok := all[SunfireServerID]; ok && g != nil {
		all[guildID] = g
		return g
	}

	g := &GuildSettings{}
	all[guildID] = g
	return g
}

// saveGuildSettings persists the current guild settings to .private/config.json.
func saveGuildSettings() error {
	guildSettingsMu.Lock()
	defer guildSettingsMu.Unlock()

	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	if g, ok := guildSettings[SunfireServerID]; ok && g != nil {
		cfg.Guild = *g
	}

	return saveConfig(configPath(), cfg)
}
