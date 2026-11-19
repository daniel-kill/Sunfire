package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"
)

func TestSetupWorkflowCatalog(t *testing.T) {
	items := GetSetupWorkflowItems()
	if len(items) != 12 {
		t.Fatalf("expected 12 setup workflow items (3 categories + 9 channels), got %d", len(items))
	}

	var foundNist bool
	for _, item := range items {
		if item.Name == "nist" {
			foundNist = true
			if item.ParentName != "Feeds & Media" {
				t.Errorf("expected nist parent to be 'Feeds & Media', got '%s'", item.ParentName)
			}
			if item.Category != "Feeds & Media" {
				t.Errorf("expected nist category to be 'Feeds & Media', got '%s'", item.Category)
			}
		}
	}

	if !foundNist {
		t.Errorf("expected 'nist' channel to be present in setup workflow catalog")
	}
}

func TestAuditSetupWorkflow_AllMissing(t *testing.T) {
	existing, missing := AuditSetupWorkflow([]*discordgo.Channel{})
	if len(existing) != 0 {
		t.Errorf("expected 0 existing items in empty guild, got %d", len(existing))
	}
	if len(missing) != 12 {
		t.Errorf("expected 12 missing items in empty guild, got %d", len(missing))
	}
}

func TestAuditSetupWorkflow_PartialAndNistDetection(t *testing.T) {
	// Simulate an existing server before NIST channel was added:
	// Sunfire category with #chats and #logs
	// INFO category with #rules
	// Feeds & Media category with #youtube, #spotify, #movies, #free-stuff, #kick-streams
	// BUT MISSING: #nist
	channels := []*discordgo.Channel{
		{ID: "cat-sunfire", Name: "Sunfire", Type: discordgo.ChannelTypeGuildCategory},
		{ID: "ch-chats", Name: "chats", ParentID: "cat-sunfire", Type: discordgo.ChannelTypeGuildText},
		{ID: "ch-logs", Name: "logs", ParentID: "cat-sunfire", Type: discordgo.ChannelTypeGuildText},

		{ID: "cat-info", Name: "INFO", Type: discordgo.ChannelTypeGuildCategory},
		{ID: "ch-rules", Name: "rules", ParentID: "cat-info", Type: discordgo.ChannelTypeGuildText},

		{ID: "cat-feeds", Name: "Feeds & Media", Type: discordgo.ChannelTypeGuildCategory},
		{ID: "ch-yt", Name: "youtube", ParentID: "cat-feeds", Type: discordgo.ChannelTypeGuildText},
		{ID: "ch-spot", Name: "spotify", ParentID: "cat-feeds", Type: discordgo.ChannelTypeGuildText},
		{ID: "ch-mov", Name: "movies", ParentID: "cat-feeds", Type: discordgo.ChannelTypeGuildText},
		{ID: "ch-free", Name: "free-stuff", ParentID: "cat-feeds", Type: discordgo.ChannelTypeGuildText},
		{ID: "ch-kick", Name: "kick-streams", ParentID: "cat-feeds", Type: discordgo.ChannelTypeGuildText},
	}

	existing, missing := AuditSetupWorkflow(channels)
	if len(existing) != 11 {
		t.Errorf("expected 11 existing items, got %d", len(existing))
	}
	if len(missing) != 1 {
		t.Fatalf("expected exactly 1 missing item (#nist), got %d", len(missing))
	}
	if missing[0].Name != "nist" {
		t.Errorf("expected missing item to be 'nist', got '%s'", missing[0].Name)
	}

	// Now add the nist channel to simulate after setup creates it
	channels = append(channels, &discordgo.Channel{
		ID:       "ch-nist",
		Name:     "nist",
		ParentID: "cat-feeds",
		Type:     discordgo.ChannelTypeGuildText,
	})

	existingAfter, missingAfter := AuditSetupWorkflow(channels)
	if len(missingAfter) != 0 {
		t.Errorf("expected 0 missing items after adding nist, got %d", len(missingAfter))
	}
	if len(existingAfter) != 12 {
		t.Errorf("expected 12 existing items after adding nist, got %d", len(existingAfter))
	}
}

func TestGuildSettingsNistJSON(t *testing.T) {
	role := "role-12345"
	gs := GuildSettings{
		NistEnabled:   true,
		NistChannelID: "123456789012345678",
		NistRoleID:    &role,
	}

	data, err := json.Marshal(gs)
	if err != nil {
		t.Fatalf("failed to marshal GuildSettings: %v", err)
	}

	var parsed GuildSettings
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("failed to unmarshal GuildSettings: %v", err)
	}

	if !parsed.NistEnabled {
		t.Errorf("expected NistEnabled = true")
	}
	if parsed.NistChannelID != "123456789012345678" {
		t.Errorf("expected NistChannelID = '123456789012345678', got '%s'", parsed.NistChannelID)
	}
	if parsed.NistRoleID == nil || *parsed.NistRoleID != "role-12345" {
		t.Errorf("expected NistRoleID = 'role-12345'")
	}
}

func TestSetupCommandRegistrationDescription(t *testing.T) {
	buildCommandSpecs()
	var foundSetup bool
	for _, spec := range commandSpecs {
		if spec.Command != nil && spec.Command.Name == "setup" {
			foundSetup = true
			if !strings.Contains(spec.Command.Description, "NIST") {
				t.Errorf("expected /setup description to mention NIST, got: %s", spec.Command.Description)
			}
			if !strings.Contains(spec.Command.Description, "Feeds & Media") {
				t.Errorf("expected /setup description to mention Feeds & Media, got: %s", spec.Command.Description)
			}
		}
	}
	if !foundSetup {
		t.Errorf("expected /setup command to be registered in commandSpecs")
	}
}
