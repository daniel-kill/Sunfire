// iahandlers.go — slash-command handlers for the /ia command (subcommands).
package main

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/bwmarrin/discordgo"
)

// iaReply sends a channel-message reply.
func iaReply(s *discordgo.Session, i *discordgo.InteractionCreate, content string) {
	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: clipMessage(content),
		},
	})
}

// iaDefer acknowledges the interaction (for slow IA calls), then follows up.
func iaDefer(s *discordgo.Session, i *discordgo.InteractionCreate) {
	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	})
}

// iaFollowUp sends the result as a follow-up after a deferred reply.
func iaFollowUp(s *discordgo.Session, i *discordgo.InteractionCreate, content string, files []*discordgo.File) {
	params := &discordgo.WebhookParams{Content: clipMessage(content)}
	if len(files) > 0 {
		params.Files = files
	}
	_, _ = s.FollowupMessageCreate(i.Interaction, true, params)
}

// clipMessage keeps Discord messages under the 2000-char limit.
func clipMessage(s string) string {
	if len(s) <= 1900 {
		return s
	}
	return s[:1900] + "…"
}

// optionString reads a string option by name from an interaction.
func optionString(i *discordgo.InteractionCreate, name string) string {
	data := i.ApplicationCommandData()
	for _, opt := range data.Options {
		if opt.Name == name && opt.Value != nil {
			if v, ok := opt.Value.(string); ok {
				return v
			}
		}
	}
	return ""
}

// handleIA dispatches /ia subcommands.
func handleIA(s *discordgo.Session, i *discordgo.InteractionCreate) {
	data := i.ApplicationCommandData()
	if len(data.Options) == 0 {
		iaReply(s, i, iaHelpText())
		return
	}
	sub := data.Options[0]
	switch sub.Name {
	case "search":
		iaHandleSearch(s, i, sub)
	case "item":
		iaHandleItem(s, i, sub)
	case "download":
		iaHandleDownload(s, i, sub)
	case "wayback":
		iaHandleWayback(s, i, sub)
	case "stats":
		iaHandleStats(s, i, sub)
	case "fts":
		iaHandleFTS(s, i, sub)
	case "help":
		iaReply(s, i, iaHelpText())
	default:
		iaReply(s, i, "Unknown /ia subcommand. Try `/ia help`.")
	}
}

func iaHandleSearch(s *discordgo.Session, i *discordgo.InteractionCreate, sub *discordgo.ApplicationCommandInteractionDataOption) {
	query := optionStringFrom(sub, "query")
	if query == "" {
		iaReply(s, i, "Usage: `/ia search query:<query> [type:audio|texts|movies|image|software]`\nExample: `/ia search query:space audio`")
		return
	}
	// Optional mediatype filter.
	if mt := optionStringFrom(sub, "type"); mt != "" {
		query = query + " AND mediatype:" + mt
	}
	iaDefer(s, i)
	docs, numFound, err := iaAdvancedSearch(query, iaMaxResults)
	if err != nil {
		iaFollowUp(s, i, "❌ Search failed: "+err.Error(), nil)
		return
	}
	iaFollowUp(s, i, formatIASearch(query, docs, numFound), nil)
}

func iaHandleItem(s *discordgo.Session, i *discordgo.InteractionCreate, sub *discordgo.ApplicationCommandInteractionDataOption) {
	identifier := strings.TrimSpace(optionStringFrom(sub, "identifier"))
	if identifier == "" {
		iaReply(s, i, "Usage: `/ia item identifier:<identifier>`\nExample: `/ia item identifier:xfetch`")
		return
	}
	iaDefer(s, i)
	rec, err := iaFetchItem(identifier)
	if err != nil {
		iaFollowUp(s, i, "❌ Could not fetch item: "+err.Error(), nil)
		return
	}
	iaFollowUp(s, i, formatIAItem(rec), nil)
}

func iaHandleDownload(s *discordgo.Session, i *discordgo.InteractionCreate, sub *discordgo.ApplicationCommandInteractionDataOption) {
	identifier := strings.TrimSpace(optionStringFrom(sub, "identifier"))
	if identifier == "" {
		iaReply(s, i, "Usage: `/ia download identifier:<identifier> [file:<filename>]`\nIf no file is given, a small representative file (thumbnail/audio/pdf) is auto-picked.\nExample: `/ia download identifier:xfetch`")
		return
	}
	iaDefer(s, i)
	rec, err := iaFetchItem(identifier)
	if err != nil {
		iaFollowUp(s, i, "❌ Could not fetch item: "+err.Error(), nil)
		return
	}
	filename := strings.TrimSpace(optionStringFrom(sub, "file"))
	if filename == "" {
		filename = iaContentForReply(identifier, rec.Files)
		if filename == "" {
			iaFollowUp(s, i, "ℹ️ Item has no small auto-downloadable file. Use `/ia item identifier:"+identifier+"` to list its files, then `/ia download identifier:"+identifier+" file:<name>`.", nil)
			return
		}
	}
	// Verify the requested file exists.
	exists := false
	for _, f := range rec.Files {
		if f.Name == filename {
			exists = true
			break
		}
	}
	if !exists {
		iaFollowUp(s, i, "❌ File `"+filename+"` not found in item `"+identifier+"`. Use `/ia item identifier:"+identifier+"` to list files.", nil)
		return
	}
	body, err := iaGet(iaDownloadURL(identifier, filename))
	if err != nil {
		iaFollowUp(s, i, "❌ Download failed: "+err.Error(), nil)
		return
	}
	if len(body) > 8*1024*1024 {
		iaFollowUp(s, i, "⚠️ File is too large to post to Discord (>8 MB). Direct link: "+iaDownloadURL(identifier, filename), nil)
		return
	}
	file := &discordgo.File{
		Name:        filename,
		ContentType: contentTypeForName(filename),
		Reader:      bytes.NewReader(body),
	}
	iaFollowUp(s, i, fmt.Sprintf("⬇️ **%s** — `%s`\n🔗 <https://archive.org/download/%s/%s>",
		iaClip(identifier, 80), filename, urlPathEscape(identifier), urlPathEscape(filename)), []*discordgo.File{file})
}

func iaHandleWayback(s *discordgo.Session, i *discordgo.InteractionCreate, sub *discordgo.ApplicationCommandInteractionDataOption) {
	pageURL := strings.TrimSpace(optionStringFrom(sub, "url"))
	if pageURL == "" {
		iaReply(s, i, "Usage: `/ia wayback url:<url>`\nChecks whether the Wayback Machine has a snapshot of a URL.\nExample: `/ia wayback url:https://example.com`")
		return
	}
	if !strings.HasPrefix(pageURL, "http") {
		pageURL = "https://" + pageURL
	}
	iaDefer(s, i)
	res, err := iaWaybackAvailable(pageURL)
	if err != nil {
		iaFollowUp(s, i, "❌ Wayback check failed: "+err.Error(), nil)
		return
	}
	iaFollowUp(s, i, formatIAWayback(res), nil)
}

func iaHandleStats(s *discordgo.Session, i *discordgo.InteractionCreate, sub *discordgo.ApplicationCommandInteractionDataOption) {
	identifier := strings.TrimSpace(optionStringFrom(sub, "identifier"))
	if identifier == "" {
		iaReply(s, i, "Usage: `/ia stats identifier:<identifier>`\nShows views/downloads for an item.\nExample: `/ia stats identifier:xfetch`")
		return
	}
	iaDefer(s, i)
	st, err := iaFetchStats(identifier)
	if err != nil {
		iaFollowUp(s, i, "❌ Stats fetch failed: "+err.Error(), nil)
		return
	}
	iaFollowUp(s, i, formatIAStats(identifier, st), nil)
}

func iaHandleFTS(s *discordgo.Session, i *discordgo.InteractionCreate, sub *discordgo.ApplicationCommandInteractionDataOption) {
	query := strings.TrimSpace(optionStringFrom(sub, "query"))
	if query == "" {
		iaReply(s, i, "Usage: `/ia fts query:<query>`\nFull-text search across items.\nExample: `/ia fts query:\"war of the worlds\"`")
		return
	}
	iaDefer(s, i)
	items, total, err := iaFullText(query, iaMaxResults)
	if err != nil {
		iaFollowUp(s, i, "❌ Full-text search failed: "+err.Error(), nil)
		return
	}
	iaFollowUp(s, i, formatIAFTS(query, items, total), nil)
}

// optionStringFrom reads a string option from a subcommand's options.
func optionStringFrom(sub *discordgo.ApplicationCommandInteractionDataOption, name string) string {
	for _, o := range sub.Options {
		if o.Name == name && o.Value != nil {
			if v, ok := o.Value.(string); ok {
				return v
			}
		}
	}
	return ""
}

// urlPathEscape is a small wrapper so we don't import net/url in the handler.
func urlPathEscape(s string) string {
	esc := strings.NewReplacer(" ", "%20", "/", "%2F", "?", "%3F", "#", "%23", "&", "%26", "=", "%3D")
	return esc.Replace(s)
}

// contentTypeForName guesses a Discord content type from a filename.
func contentTypeForName(name string) string {
	n := strings.ToLower(name)
	switch {
	case strings.HasSuffix(n, ".jpg") || strings.HasSuffix(n, ".jpeg"):
		return "image/jpeg"
	case strings.HasSuffix(n, ".png"):
		return "image/png"
	case strings.HasSuffix(n, ".gif"):
		return "image/gif"
	case strings.HasSuffix(n, ".webp"):
		return "image/webp"
	case strings.HasSuffix(n, ".mp3"):
		return "audio/mpeg"
	case strings.HasSuffix(n, ".ogg"):
		return "audio/ogg"
	case strings.HasSuffix(n, ".wav"):
		return "audio/wav"
	case strings.HasSuffix(n, ".pdf"):
		return "application/pdf"
	case strings.HasSuffix(n, ".txt"):
		return "text/plain"
	case strings.HasSuffix(n, ".json"):
		return "application/json"
	default:
		return "application/octet-stream"
	}
}

// iaHelpText returns the /ia usage guide.
func iaHelpText() string {
	return "**🗂️ Internet Archive — `/ia`**\n\n" +
		"Search and explore the Internet Archive right from Discord. All data is public; no API key needed.\n\n" +
		"**`/ia search query:<q> [type:<type>]`** — metadata search. Optional type: `audio`, `texts`, `movies`, `image`, `software`, `etree`, `web`.\n" +
		"  _e.g. `/ia search query:space audio`_\n\n" +
		"**`/ia item identifier:<id>`** — full metadata for an item (title, creator, files, size).\n" +
		"  _e.g. `/ia item identifier:xfetch`_\n\n" +
		"**`/ia download identifier:<id> [file:<name>]`** — post a file from an item (auto-picks a small one if no file given).\n" +
		"  _e.g. `/ia download identifier:xfetch`_\n\n" +
		"**`/ia wayback url:<url>`** — check if the Wayback Machine archived a URL, with the snapshot date.\n" +
		"  _e.g. `/ia wayback url:https://example.com`_\n\n" +
		"**`/ia stats identifier:<id>`** — view + download counts for an item.\n\n" +
		"**`/ia fts query:<q>`** — full-text search across the archive.\n\n" +
		"**`/ia help`** — show this guide.\n\n" +
		"_Powered by archive.org — see `/home/daniel/Repos/shared/docs/InternetArchive.md`._"
}
