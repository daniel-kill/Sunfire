package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"
)

func withEmojiAssets(t *testing.T, files map[string][]byte) {
	t.Helper()
	dir := t.TempDir()
	oldDirectory := emojiAssetDirectory
	emojiAssetDirectory = func() string { return dir }
	t.Cleanup(func() { emojiAssetDirectory = oldDirectory })
	for name, contents := range files {
		if err := os.WriteFile(filepath.Join(dir, name), contents, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLoadEmojiGGAssetsAndFindDuplicateNames(t *testing.T) {
	withEmojiAssets(t, map[string][]byte{
		"123-blue-heart.gif": []byte("gif"),
		"456-blue-heart.gif": []byte("gif"),
		"789-party-time.png": []byte("png"),
		"manifest.json":      []byte("{}"),
		"ignored.webp":       []byte("webp"),
	})
	assets, err := loadEmojiGGAssets()
	if err != nil {
		t.Fatal(err)
	}
	if len(assets) != 3 {
		t.Fatalf("assets = %d; want 3 supported image assets", len(assets))
	}
	if assets[0].Name != "blue_heart" || assets[0].ID != "123" {
		t.Fatalf("first sorted asset = %#v; want blue_heart asset 123", assets[0])
	}
	if _, err := findEmojiGGAsset(assets, "blue_heart"); err == nil || !strings.Contains(err.Error(), "use an asset ID") {
		t.Fatalf("duplicate asset name error = %v; want disambiguation hint", err)
	}
	asset, err := findEmojiGGAsset(assets, "456")
	if err != nil {
		t.Fatal(err)
	}
	if asset.FileName != "456-blue-heart.gif" {
		t.Fatalf("asset by ID = %#v; want second blue-heart file", asset)
	}
}

func TestEmojiGGCreateParamsValidatesNameAndFileSize(t *testing.T) {
	withEmojiAssets(t, map[string][]byte{
		"123-party-time.gif": []byte("GIF89a"),
		"456-large.gif":      make([]byte, 200<<10),
	})
	assets, err := loadEmojiGGAssets()
	if err != nil {
		t.Fatal(err)
	}
	asset, err := findEmojiGGAsset(assets, "123")
	if err != nil {
		t.Fatal(err)
	}
	params, err := emojiGGCreateParams(asset, "")
	if err != nil {
		t.Fatal(err)
	}
	if params.Name != "party_time" || !strings.HasPrefix(params.Image, "data:image/gif;base64,") {
		t.Fatalf("create params = %#v; want normalized name and GIF data URI", params)
	}
	if _, err := emojiGGCreateParams(asset, "bad name!"); err == nil {
		t.Fatal("invalid emoji name was accepted")
	}
	if _, err := emojiGGCreateParams(asset, "custom_name"); err != nil {
		t.Fatalf("valid override name rejected: %v", err)
	}
	large, err := findEmojiGGAsset(assets, "456")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := emojiGGCreateParams(large, "large"); err == nil || !strings.Contains(err.Error(), "smaller than") {
		t.Fatalf("oversized asset error = %v; want size limit error", err)
	}
}

func TestEmojiGGPermissionsRequireManageExpressionsOrAdministrator(t *testing.T) {
	for _, tt := range []struct {
		name        string
		permissions int64
		want        bool
	}{
		{name: "no permission"},
		{name: "manage expressions", permissions: int64(discordgo.PermissionManageGuildExpressions), want: true},
		{name: "administrator", permissions: int64(discordgo.PermissionAdministrator), want: true},
		{name: "unrelated permission", permissions: int64(discordgo.PermissionManageMessages)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			interaction := &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{
				Member: &discordgo.Member{Permissions: tt.permissions},
			}}
			if got := canManageGuildEmojis(interaction); got != tt.want {
				t.Fatalf("canManageGuildEmojis() = %t; want %t", got, tt.want)
			}
		})
	}
	if canManageGuildEmojis(nil) {
		t.Fatal("nil interaction unexpectedly has emoji management permission")
	}
}

func TestFormatEmojiGGAssetPage(t *testing.T) {
	assets := make([]emojiGGAsset, emojiGGPageSize+1)
	for index := range assets {
		assets[index] = emojiGGAsset{Name: "emoji", ID: "123"}
	}
	page := formatEmojiGGAssetPage(assets, 2, len(assets))
	if !strings.Contains(page, "page 2/2") || strings.Count(page, "• `emoji`") != 1 {
		t.Fatalf("second page output = %q; want last asset only and page 2/2", page)
	}
	if got := formatEmojiGGAssetPage(assets, 0, len(assets)); !strings.Contains(got, "between 1 and 2") {
		t.Fatalf("invalid page response = %q; want valid page range", got)
	}
}

func TestEmojiGGPageComponentsKeepSearchAndPageState(t *testing.T) {
	query := "happy heart"
	components := emojiGGPageComponents(2, 5, query)
	if len(components) != 1 {
		t.Fatalf("component rows = %d; want one row", len(components))
	}
	buttons := components[0].(discordgo.ActionsRow).Components
	if len(buttons) != 3 {
		t.Fatalf("page buttons = %d; want previous, page indicator, next", len(buttons))
	}
	previous := buttons[0].(discordgo.Button)
	next := buttons[2].(discordgo.Button)
	if previous.Disabled || next.Disabled {
		t.Fatal("middle-page navigation buttons unexpectedly disabled")
	}
	for _, button := range []discordgo.Button{previous, next} {
		parts := strings.Split(button.CustomID, ":")
		if len(parts) != 4 {
			t.Fatalf("button custom ID %q is malformed", button.CustomID)
		}
		if parts[3] != "aGFwcHkgaGVhcnQ" {
			t.Fatalf("button custom ID query = %q; want encoded search query", parts[3])
		}
	}
	firstPage := emojiGGPageComponents(1, 5, "")
	if firstPage[0].(discordgo.ActionsRow).Components[0].(discordgo.Button).Disabled != true {
		t.Fatal("previous button should be disabled on first page")
	}
	lastPage := emojiGGPageComponents(5, 5, "")
	if lastPage[0].(discordgo.ActionsRow).Components[2].(discordgo.Button).Disabled != true {
		t.Fatal("next button should be disabled on last page")
	}
	if got := emojiGGPageComponents(1, 1, ""); got != nil {
		t.Fatalf("single-page controls = %#v; want no navigation buttons", got)
	}
}
