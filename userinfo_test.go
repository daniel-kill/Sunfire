package main

import (
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"
)

func TestFormatUserPresenceIncludesClientsAndActivity(t *testing.T) {
	got := formatUserPresence(&discordgo.Presence{
		Status: discordgo.StatusOnline,
		ClientStatus: discordgo.ClientStatus{
			Desktop: discordgo.StatusOnline,
			Mobile:  discordgo.StatusIdle,
		},
		Activities: []*discordgo.Activity{
			{
				Type:    discordgo.ActivityTypeGame,
				Name:    "Example Game",
				Details: "In a match",
				State:   "Ranked",
			},
			{
				Type:  discordgo.ActivityTypeCustom,
				State: "At work",
			},
		},
	}, nil)
	for _, want := range []string{"Online", "Desktop: online", "Mobile: idle", "Example Game", "In a match", "Ranked", "At work"} {
		if !strings.Contains(got, want) {
			t.Errorf("formatted presence %q does not contain %q", got, want)
		}
	}
}

func TestFormatUserVoiceStateIncludesVoiceFlags(t *testing.T) {
	got := formatUserVoiceState(&discordgo.Guild{
		VoiceStates: []*discordgo.VoiceState{{
			UserID:     "user-id",
			ChannelID:  "channel-id",
			SelfMute:   true,
			Deaf:       true,
			SelfStream: true,
			SelfVideo:  true,
		}},
	}, "user-id")
	for _, want := range []string{"<#channel-id>", "self-muted", "server-deafened", "streaming", "camera on"} {
		if !strings.Contains(got, want) {
			t.Errorf("formatted voice state %q does not contain %q", got, want)
		}
	}
}

func TestGuildMemberRoleNamesAreOrderedByHierarchy(t *testing.T) {
	member := &discordgo.Member{Roles: []string{"lower", "higher"}}
	guild := &discordgo.Guild{Roles: []*discordgo.Role{
		{ID: "lower", Name: "Lower", Position: 1},
		{ID: "higher", Name: "Higher", Position: 5, Managed: true},
	}}

	got, highest := guildMemberRoleNames(member, guild)
	if highest != "Higher" {
		t.Fatalf("highest role = %q, want Higher", highest)
	}
	if strings.Index(got, "Higher") > strings.Index(got, "Lower") {
		t.Fatalf("roles are not ordered highest-first: %q", got)
	}
	if !strings.Contains(got, "managed") {
		t.Fatalf("managed role marker missing from %q", got)
	}
}

func TestEffectivePermissionNames(t *testing.T) {
	got := effectivePermissionNames(discordgo.PermissionAdministrator | discordgo.PermissionBanMembers)
	if len(got) != 1 || got[0] != "Administrator (all permissions)" {
		t.Fatalf("administrator permissions = %#v", got)
	}

	got = effectivePermissionNames(discordgo.PermissionBanMembers | discordgo.PermissionManageMessages)
	if len(got) != 2 || got[0] != "Manage Messages" || got[1] != "Ban Members" {
		t.Fatalf("selected permissions = %#v", got)
	}
}

func TestGetUserBadgesIncludesAllKnownPublicFlags(t *testing.T) {
	flags := discordgo.UserFlagDiscordEmployee |
		discordgo.UserFlagDiscordPartner |
		discordgo.UserFlagHypeSquadEvents |
		discordgo.UserFlagBugHunterLevel1 |
		discordgo.UserFlagHouseBravery |
		discordgo.UserFlagHouseBrilliance |
		discordgo.UserFlagHouseBalance |
		discordgo.UserFlagEarlySupporter |
		discordgo.UserFlagTeamUser |
		discordgo.UserFlagBugHunterLevel2 |
		discordgo.UserFlagVerifiedBot |
		discordgo.UserFlagVerifiedBotDeveloper |
		discordgo.UserFlagDiscordCertifiedModerator |
		discordgo.UserFlagBotHTTPInteractions |
		discordgo.UserFlagActiveBotDeveloper
	got := getUserBadges(int(flags))
	if len(got) != 15 {
		t.Fatalf("got %d public flag labels, want 15: %#v", len(got), got)
	}
	if !strings.Contains(strings.Join(got, "\n"), "HTTP interactions") {
		t.Fatalf("public bot interaction flag was omitted: %#v", got)
	}
}

func TestUserInfoTruncationPreservesUTF8(t *testing.T) {
	got := truncateUserInfo(strings.Repeat("é", 20), 10)
	if len([]rune(got)) != 10 || !strings.HasSuffix(got, "…") {
		t.Fatalf("truncated text = %q", got)
	}
}
