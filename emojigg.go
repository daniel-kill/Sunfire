package main

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log"
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/bwmarrin/discordgo"
)

const (
	emojiGGPageSize       = 15
	maxGuildEmojiFileSize = 256 << 10
)

var (
	emojiAssetDirectory = func() string { return applicationPath("emojis") }
	emojiAssetNameRE    = regexp.MustCompile(`^[a-zA-Z0-9_]{2,32}$`)
	emojiSourceIDRE     = regexp.MustCompile(`^(\d+)-`)
)

type emojiGGAsset struct {
	ID       string
	Name     string
	FileName string
	Path     string
	Size     int64
}

func loadEmojiGGAssets() ([]emojiGGAsset, error) {
	dir := emojiAssetDirectory()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading emoji asset directory %s: %w", dir, err)
	}

	var assets []emojiGGAsset
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if ext != ".gif" && ext != ".png" {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, fmt.Errorf("reading emoji asset metadata for %s: %w", entry.Name(), err)
		}
		if !info.Mode().IsRegular() {
			continue
		}

		baseName := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
		id := ""
		if match := emojiSourceIDRE.FindStringSubmatch(baseName); match != nil {
			id = match[1]
			baseName = strings.TrimPrefix(baseName, id+"-")
		}
		name := discordEmojiName(baseName)
		assets = append(assets, emojiGGAsset{
			ID:       id,
			Name:     name,
			FileName: entry.Name(),
			Path:     filepath.Join(dir, entry.Name()),
			Size:     info.Size(),
		})
	}
	sort.Slice(assets, func(i, j int) bool {
		if assets[i].Name == assets[j].Name {
			return assets[i].FileName < assets[j].FileName
		}
		return assets[i].Name < assets[j].Name
	})
	return assets, nil
}

func discordEmojiName(name string) string {
	name = strings.ToLower(name)
	var out strings.Builder
	lastUnderscore := false
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_':
			out.WriteRune(r)
			lastUnderscore = r == '_'
		case r == '-' || r == ' ':
			if out.Len() > 0 && !lastUnderscore {
				out.WriteByte('_')
				lastUnderscore = true
			}
		}
	}
	return strings.Trim(out.String(), "_")
}

func searchEmojiGGAssets(assets []emojiGGAsset, query string) []emojiGGAsset {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return nil
	}
	var matches []emojiGGAsset
	for _, asset := range assets {
		if strings.Contains(asset.Name, query) || (asset.ID != "" && strings.Contains(asset.ID, query)) {
			matches = append(matches, asset)
		}
	}
	return matches
}

func findEmojiGGAsset(assets []emojiGGAsset, selector string) (emojiGGAsset, error) {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return emojiGGAsset{}, fmt.Errorf("provide an emoji name or emoji.gg asset ID")
	}
	var matches []emojiGGAsset
	for _, asset := range assets {
		if asset.ID == selector || strings.EqualFold(asset.Name, selector) || strings.EqualFold(asset.FileName, selector) {
			matches = append(matches, asset)
		}
	}
	if len(matches) == 0 {
		return emojiGGAsset{}, fmt.Errorf("no emoji asset found for %q; use `/emojigg search` to find an asset", selector)
	}
	if len(matches) > 1 {
		var ids []string
		for _, asset := range matches {
			ids = append(ids, asset.ID)
		}
		return emojiGGAsset{}, fmt.Errorf("more than one asset is named `%s`; use an asset ID instead: %s", selector, strings.Join(ids, ", "))
	}
	return matches[0], nil
}

func emojiGGCreateParams(asset emojiGGAsset, requestedName string) (*discordgo.EmojiParams, error) {
	name := asset.Name
	if requestedName != "" {
		name = strings.TrimSpace(requestedName)
	}
	if !emojiAssetNameRE.MatchString(name) {
		return nil, fmt.Errorf("emoji name must contain 2–32 letters, numbers, or underscores; provide a valid `name` option")
	}
	if emojiGGAssetTooLarge(asset) {
		return nil, fmt.Errorf("this asset is too large; Discord requires the encoded emoji image to be smaller than %d bytes", maxGuildEmojiFileSize)
	}
	data, err := os.ReadFile(asset.Path)
	if err != nil {
		return nil, fmt.Errorf("reading emoji asset: %w", err)
	}
	if len(data) != int(asset.Size) {
		return nil, fmt.Errorf("emoji asset changed while being read; try again")
	}
	contentType := "image/gif"
	if strings.EqualFold(filepath.Ext(asset.FileName), ".png") {
		contentType = "image/png"
	}
	image := "data:" + contentType + ";base64," + base64.StdEncoding.EncodeToString(data)
	if len(image) >= maxGuildEmojiFileSize {
		return nil, fmt.Errorf("this asset is too large; Discord requires the encoded emoji image to be smaller than %d bytes", maxGuildEmojiFileSize)
	}
	return &discordgo.EmojiParams{
		Name:  name,
		Image: image,
	}, nil
}

func emojiGGAssetTooLarge(asset emojiGGAsset) bool {
	if asset.Size < 0 || asset.Size >= maxGuildEmojiFileSize {
		return true
	}
	contentType := "image/gif"
	if strings.EqualFold(filepath.Ext(asset.FileName), ".png") {
		contentType = "image/png"
	}
	prefixBytes := int64(len("data:" + contentType + ";base64,"))
	encodedBytes := ((asset.Size + 2) / 3 * 4) + prefixBytes
	return encodedBytes >= maxGuildEmojiFileSize
}

func canManageGuildEmojis(i *discordgo.InteractionCreate) bool {
	if i == nil || i.Member == nil {
		return false
	}
	required := int64(discordgo.PermissionManageGuildExpressions | discordgo.PermissionAdministrator)
	return i.Member.Permissions&required != 0
}

func handleEmojiGG(s *discordgo.Session, i *discordgo.InteractionCreate) {
	options := i.ApplicationCommandData().Options
	if len(options) == 0 {
		respondEmojiGG(s, i, "Choose `search`, `list`, `random`, `preview`, or `add`.")
		return
	}
	subcommand := options[0]

	assets, err := loadEmojiGGAssets()
	if err != nil {
		log.Printf("[emojigg] %v", err)
		respondEmojiGG(s, i, "❌ Emoji assets are unavailable. Check the bot's `emojis` folder.")
		return
	}
	if len(assets) == 0 {
		respondEmojiGG(s, i, "❌ No supported GIF or PNG emoji assets were found.")
		return
	}

	switch subcommand.Name {
	case "search":
		query := stringOptionValue(subcommand.Options, "query")
		matches := searchEmojiGGAssets(assets, query)
		if len(matches) == 0 {
			respondEmojiGG(s, i, fmt.Sprintf("No emoji assets found matching %q.", query))
			return
		}
		page := intOptionValue(subcommand.Options, "page", 1)
		respondEmojiGGAssetPage(s, i, matches, page, query, false)
	case "list":
		page := intOptionValue(subcommand.Options, "page", 1)
		respondEmojiGGAssetPage(s, i, assets, page, "", false)
	case "random":
		index, err := rand.Int(rand.Reader, big.NewInt(int64(len(assets))))
		if err != nil {
			log.Printf("[emojigg] selecting random emoji asset: %v", err)
			respondEmojiGG(s, i, "❌ Could not select a random emoji. Please try again.")
			return
		}
		previewEmojiGGAsset(s, i, assets[index.Int64()], true)
	case "preview":
		asset, err := findEmojiGGAsset(assets, stringOptionValue(subcommand.Options, "asset"))
		if err != nil {
			respondEmojiGG(s, i, "❌ "+err.Error())
			return
		}
		previewEmojiGGAsset(s, i, asset, false)
	case "add":
		if !canManageGuildEmojis(i) {
			respondEmojiGG(s, i, "❌ You need **Manage Expressions** or **Administrator** permission to add server emojis.")
			return
		}
		asset, err := findEmojiGGAsset(assets, stringOptionValue(subcommand.Options, "asset"))
		if err != nil {
			respondEmojiGG(s, i, "❌ "+err.Error())
			return
		}
		requestedName := stringOptionValue(subcommand.Options, "name")
		params, err := emojiGGCreateParams(asset, requestedName)
		if err != nil {
			respondEmojiGG(s, i, "❌ "+err.Error())
			return
		}

		if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{Flags: discordgo.MessageFlagsEphemeral},
		}); err != nil {
			log.Printf("[emojigg] deferring add response: %v", err)
			return
		}
		existing, err := s.GuildEmojis(i.GuildID)
		if err != nil {
			log.Printf("[emojigg] checking existing guild emojis: %v", err)
			followupEmojiGG(s, i, "❌ Could not check the server's existing emojis. Please try again.")
			return
		}
		for _, guildEmoji := range existing {
			if strings.EqualFold(guildEmoji.Name, params.Name) {
				followupEmojiGG(s, i, fmt.Sprintf("ℹ️ The server already has an emoji named `%s`.", params.Name))
				return
			}
		}
		emoji, err := s.GuildEmojiCreate(i.GuildID, params)
		if err != nil {
			log.Printf("[emojigg] adding asset %s as %s in guild %s: %v", asset.FileName, params.Name, i.GuildID, err)
			followupEmojiGG(s, i, fmt.Sprintf("❌ Discord could not add `%s`. Check the bot's Manage Expressions permission, emoji slot availability, and whether the file meets server limits. Error: %v", params.Name, err))
			return
		}
		followupEmojiGG(s, i, fmt.Sprintf("✅ Added %s (`%s`) to the server.", emoji.MessageFormat(), emoji.Name))
	default:
		respondEmojiGG(s, i, "Choose `search`, `list`, `random`, `preview`, or `add`.")
	}
}

func previewEmojiGGAsset(s *discordgo.Session, i *discordgo.InteractionCreate, asset emojiGGAsset, random bool) {
	file, err := os.Open(asset.Path)
	if err != nil {
		log.Printf("[emojigg] opening emoji asset %s: %v", asset.FileName, err)
		respondEmojiGG(s, i, "❌ Could not read that emoji asset. Please try again.")
		return
	}
	defer file.Close()

	selector := asset.ID
	if selector == "" {
		selector = asset.FileName
	}
	prefix := "👀"
	if random {
		prefix = "🎲"
	}
	if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: fmt.Sprintf("%s **%s** · asset `%s`\nUse `/emojigg add asset:%s` to add it to the server.", prefix, asset.Name, selector, selector),
			Files:   []*discordgo.File{{Name: asset.FileName, Reader: file}},
			Flags:   discordgo.MessageFlagsEphemeral,
		},
	}); err != nil {
		log.Printf("[emojigg] responding with emoji asset preview %s: %v", asset.FileName, err)
	}
}

func formatEmojiGGAssetPage(assets []emojiGGAsset, page, total int) string {
	pageCount := (total + emojiGGPageSize - 1) / emojiGGPageSize
	if page < 1 || page > pageCount {
		return fmt.Sprintf("Page must be between 1 and %d.", pageCount)
	}
	start := (page - 1) * emojiGGPageSize
	end := start + emojiGGPageSize
	if end > total {
		end = total
	}
	var lines []string
	for _, asset := range assets[start:end] {
		sizeNote := ""
		if emojiGGAssetTooLarge(asset) {
			sizeNote = " · too large to add"
		}
		id := asset.ID
		if id == "" {
			id = asset.FileName
		}
		lines = append(lines, fmt.Sprintf("• `%s` · asset `%s`%s", asset.Name, id, sizeNote))
	}
	return fmt.Sprintf("**Emoji.gg assets (%d total) — page %d/%d**\n%s\n\nUse `/emojigg add asset:<name-or-ID>` to add one.", total, page, pageCount, strings.Join(lines, "\n"))
}

func emojiGGPageCustomID(page int, query string) string {
	encodedQuery := base64.RawURLEncoding.EncodeToString([]byte(query))
	return fmt.Sprintf("emojigg:page:%d:%s", page, encodedQuery)
}

func emojiGGPageComponents(page, pageCount int, query string) []discordgo.MessageComponent {
	if pageCount <= 1 {
		return nil
	}
	return []discordgo.MessageComponent{
		discordgo.ActionsRow{Components: []discordgo.MessageComponent{
			discordgo.Button{Label: "Previous", Style: discordgo.PrimaryButton, CustomID: emojiGGPageCustomID(page-1, query), Disabled: page <= 1},
			discordgo.Button{Label: fmt.Sprintf("%d / %d", page, pageCount), Style: discordgo.SecondaryButton, CustomID: "emojigg:page:noop:", Disabled: true},
			discordgo.Button{Label: "Next", Style: discordgo.PrimaryButton, CustomID: emojiGGPageCustomID(page+1, query), Disabled: page >= pageCount},
		}},
	}
}

func respondEmojiGGAssetPage(s *discordgo.Session, i *discordgo.InteractionCreate, assets []emojiGGAsset, page int, query string, update bool) {
	pageCount := (len(assets) + emojiGGPageSize - 1) / emojiGGPageSize
	content := formatEmojiGGAssetPage(assets, page, len(assets))
	data := &discordgo.InteractionResponseData{
		Content:    content,
		Components: emojiGGPageComponents(page, pageCount, query),
	}
	responseType := discordgo.InteractionResponseChannelMessageWithSource
	if update {
		responseType = discordgo.InteractionResponseUpdateMessage
	} else {
		data.Flags = discordgo.MessageFlagsEphemeral
	}
	if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: responseType,
		Data: data,
	}); err != nil {
		log.Printf("[emojigg] rendering asset page %d: %v", page, err)
	}
}

func handleEmojiGGPageComponent(s *discordgo.Session, i *discordgo.InteractionCreate) {
	parts := strings.Split(i.MessageComponentData().CustomID, ":")
	if len(parts) != 4 || parts[0] != "emojigg" || parts[1] != "page" {
		respondEmojiGG(s, i, "❌ This emoji page control is invalid. Run `/emojigg list` again.")
		return
	}
	page, err := strconv.Atoi(parts[2])
	if err != nil || page < 1 {
		respondEmojiGG(s, i, "❌ This emoji page control is invalid. Run `/emojigg list` again.")
		return
	}
	queryBytes, err := base64.RawURLEncoding.DecodeString(parts[3])
	if err != nil {
		respondEmojiGG(s, i, "❌ This emoji page control is invalid. Run `/emojigg list` again.")
		return
	}
	query := string(queryBytes)
	assets, err := loadEmojiGGAssets()
	if err != nil {
		log.Printf("[emojigg] loading assets for page interaction: %v", err)
		respondEmojiGG(s, i, "❌ Emoji assets are unavailable. Check the bot's `emojis` folder.")
		return
	}
	if query != "" {
		assets = searchEmojiGGAssets(assets, query)
	}
	if len(assets) == 0 {
		respondEmojiGG(s, i, "No emoji assets were found for this page. Run `/emojigg list` again.")
		return
	}
	pageCount := (len(assets) + emojiGGPageSize - 1) / emojiGGPageSize
	if page > pageCount {
		page = pageCount
	}
	respondEmojiGGAssetPage(s, i, assets, page, query, true)
}

func stringOptionValue(options []*discordgo.ApplicationCommandInteractionDataOption, name string) string {
	for _, option := range options {
		if option.Name == name {
			return option.StringValue()
		}
	}
	return ""
}

func intOptionValue(options []*discordgo.ApplicationCommandInteractionDataOption, name string, fallback int) int {
	for _, option := range options {
		if option.Name == name {
			return int(option.IntValue())
		}
	}
	return fallback
}

func respondEmojiGG(s *discordgo.Session, i *discordgo.InteractionCreate, content string) {
	if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: content,
			Flags:   discordgo.MessageFlagsEphemeral,
		},
	}); err != nil {
		log.Printf("[emojigg] responding to command: %v", err)
	}
}

func followupEmojiGG(s *discordgo.Session, i *discordgo.InteractionCreate, content string) {
	if _, err := s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
		Content: content,
		Flags:   discordgo.MessageFlagsEphemeral,
	}); err != nil {
		log.Printf("[emojigg] sending follow-up: %v", err)
	}
}
