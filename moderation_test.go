package main

import (
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"
)

func TestBanUserIDParsing(t *testing.T) {
	for input, want := range map[string]string{
		"123456789012345678":     "123456789012345678",
		"<@123456789012345678>":  "123456789012345678",
		"<@!123456789012345678>": "123456789012345678",
		"not-a-user":             "",
		"":                       "",
	} {
		if got := parseBanUserID(input); got != want {
			t.Errorf("parseBanUserID(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestBanPagePaginationControls(t *testing.T) {
	onePage := make([]*discordgo.GuildBan, 16)
	for i := range onePage {
		onePage[i] = &discordgo.GuildBan{
			User: &discordgo.User{ID: "123456789012345678", Username: "one"},
		}
	}
	content, components := banPage(onePage, 0)
	if len(components) != 0 {
		t.Fatalf("one-page ban list has %d component rows, want none", len(components))
	}
	if !strings.Contains(content, "one") {
		t.Fatalf("ban page omitted user: %q", content)
	}

	manyBans := make([]*discordgo.GuildBan, 50)
	for i := range manyBans {
		manyBans[i] = &discordgo.GuildBan{
			User: &discordgo.User{ID: "123456789012345678", Username: strings.Repeat("x", 32)},
		}
	}
	content, components = banPage(manyBans, 0)
	if len(content) > 2000 {
		t.Fatalf("ban page is %d characters, exceeds Discord's message limit", len(content))
	}
	if len(components) != 1 {
		t.Fatalf("multi-page ban list has %d component rows, want one", len(components))
	}
	row, ok := components[0].(discordgo.ActionsRow)
	if !ok || len(row.Components) != 2 {
		t.Fatalf("multi-page ban list should have previous/next buttons, got %#v", components[0])
	}
}
