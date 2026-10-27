package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
)

const (
	geniusSearchURL     = "https://genius.com/api/search/multi"
	geniusSearchTimeout = 15 * time.Second
	geniusResultLimit   = 5
	geniusReplyLimit    = 1900
)

type geniusSearchResult struct {
	Title                 string `json:"title"`
	FullTitle             string `json:"full_title"`
	ArtistNames           string `json:"artist_names"`
	PrimaryArtistNames    string `json:"primary_artist_names"`
	ReleaseDateForDisplay string `json:"release_date_for_display"`
	URL                   string `json:"url"`
}

type geniusSearchResponse struct {
	Response struct {
		Sections []struct {
			Type string `json:"type"`
			Hits []struct {
				Result geniusSearchResult `json:"result"`
			} `json:"hits"`
		} `json:"sections"`
	} `json:"response"`
}

func searchGenius(kind, query string) ([]geniusSearchResult, error) {
	return searchGeniusWithClient(&http.Client{Timeout: geniusSearchTimeout}, geniusSearchURL, kind, query)
}

func searchGeniusWithClient(client *http.Client, endpoint, kind, query string) ([]geniusSearchResult, error) {
	if kind != "song" && kind != "album" {
		return nil, fmt.Errorf("unsupported Genius search type %q", kind)
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("search query cannot be empty")
	}

	parsedEndpoint, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("invalid Genius search endpoint: %w", err)
	}
	params := parsedEndpoint.Query()
	params.Set("q", query)
	parsedEndpoint.RawQuery = params.Encode()

	req, err := http.NewRequest(http.MethodGet, parsedEndpoint.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("creating Genius search request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Sunfire/1.0 (Discord lyrics metadata search)")

	if client == nil {
		client = &http.Client{Timeout: geniusSearchTimeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("requesting Genius search: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Genius search returned HTTP %d", resp.StatusCode)
	}

	var payload geniusSearchResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decoding Genius search response: %w", err)
	}

	results := make([]geniusSearchResult, 0, geniusResultLimit)
	seenURLs := make(map[string]struct{})
	for _, section := range payload.Response.Sections {
		if section.Type != kind {
			continue
		}
		for _, hit := range section.Hits {
			result := hit.Result
			result.URL = validGeniusResultURL(result.URL)
			if result.URL == "" {
				continue
			}
			if _, exists := seenURLs[result.URL]; exists {
				continue
			}
			seenURLs[result.URL] = struct{}{}
			results = append(results, result)
			if len(results) == geniusResultLimit {
				return results, nil
			}
		}
	}
	return results, nil
}

func validGeniusResultURL(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" {
		return ""
	}
	host := strings.ToLower(parsed.Hostname())
	if (host != "genius.com" && !strings.HasSuffix(host, ".genius.com")) || parsed.User != nil || len(rawURL) > 512 {
		return ""
	}
	return parsed.String()
}

func handleLyrics(s *discordgo.Session, i *discordgo.InteractionCreate) {
	data := i.ApplicationCommandData()
	if len(data.Options) == 0 {
		lyricsReply(s, i, "Choose `/lyrics fetch-song` or `/lyrics fetch-album`.")
		return
	}

	sub := data.Options[0]
	var kind, query string
	switch sub.Name {
	case "fetch-song":
		kind = "song"
		query = optionStringFrom(sub, "song")
	case "fetch-album":
		kind = "album"
		artist := optionStringFrom(sub, "artist")
		album := optionStringFrom(sub, "album")
		query = strings.TrimSpace(strings.Join([]string{artist, album}, " "))
	default:
		lyricsReply(s, i, "Unknown `/lyrics` action. Try `/lyrics fetch-song` or `/lyrics fetch-album`.")
		return
	}
	if strings.TrimSpace(query) == "" {
		lyricsReply(s, i, "❌ Enter a search term.")
		return
	}

	if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	}); err != nil {
		log.Printf("lyrics: acknowledging interaction failed: %v", err)
		return
	}

	results, err := searchGenius(kind, query)
	if err != nil {
		log.Printf("lyrics: Genius %s search failed for %q: %v", kind, query, err)
		lyricsFollowUp(s, i, "❌ Genius search failed. Please try again later.")
		return
	}
	lyricsFollowUp(s, i, formatGeniusSearchResults(kind, query, results))
}

func formatGeniusSearchResults(kind, query string, results []geniusSearchResult) string {
	label := "song"
	if kind == "album" {
		label = "album"
	}
	if len(results) == 0 {
		return fmt.Sprintf("No %ss found on Genius for **%s**.", label, discordSafeText(query))
	}

	var out strings.Builder
	fmt.Fprintf(&out, "Genius %s search results for **%s** (metadata and links only):\n", label, boundedGeniusText(query, 250))
	omitted := "\n… Additional results omitted to fit Discord's message limit."
	for index, result := range results {
		title := result.Title
		if title == "" {
			title = result.FullTitle
		}
		if title == "" {
			title = "Untitled"
		}
		artist := result.PrimaryArtistNames
		if artist == "" {
			artist = result.ArtistNames
		}
		var item strings.Builder
		fmt.Fprintf(&item, "\n• **%s**", boundedGeniusText(title, 120))
		if artist != "" {
			fmt.Fprintf(&item, " — %s", boundedGeniusText(artist, 100))
		}
		if result.ReleaseDateForDisplay != "" {
			fmt.Fprintf(&item, " · %s", boundedGeniusText(result.ReleaseDateForDisplay, 48))
		}
		fmt.Fprintf(&item, "\n  <%s>\n", result.URL)
		nextLength := len([]rune(out.String())) + len([]rune(item.String()))
		if nextLength > geniusReplyLimit || (index < len(results)-1 && nextLength+len([]rune(omitted)) > geniusReplyLimit) {
			out.WriteString(omitted)
			break
		}
		out.WriteString(item.String())
	}
	return out.String()
}

func discordSafeText(value string) string {
	value = strings.ReplaceAll(value, "@", "@\u200b")
	value = strings.ReplaceAll(value, "`", "'")
	return strings.Join(strings.Fields(value), " ")
}

func boundedGeniusText(value string, limit int) string {
	runes := []rune(discordSafeText(value))
	if len(runes) <= limit {
		return string(runes)
	}
	return string(runes[:limit-1]) + "…"
}

func lyricsReply(s *discordgo.Session, i *discordgo.InteractionCreate, content string) {
	if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Content: content},
	}); err != nil {
		log.Printf("lyrics: replying to interaction failed: %v", err)
	}
}

func lyricsFollowUp(s *discordgo.Session, i *discordgo.InteractionCreate, content string) {
	if _, err := s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{Content: content}); err != nil {
		log.Printf("lyrics: sending search result failed: %v", err)
	}
}
