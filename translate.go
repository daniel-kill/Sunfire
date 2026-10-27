// Package main — translate.go provides multi-language translation using
// LibreTranslate and MyMemory fallback.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
)

var langCodeRegex = regexp.MustCompile(`^[a-zA-Z]{2,5}(?:-[a-zA-Z]{2,5})?$`)

func normalizeTargetLanguage(input string) string {
	raw := strings.ToLower(strings.TrimSpace(input))
	switch raw {
	case "ph", "filipino":
		return "tl"
	case "jp":
		return "ja"
	case "kr":
		return "ko"
	default:
		return raw
	}
}

func translateWithLibreTranslate(text, target string) (string, error) {
	reqBody, _ := json.Marshal(map[string]string{
		"q":      text,
		"source": "auto",
		"target": target,
		"format": "text",
	})

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post("https://libretranslate.de/translate", "application/json", bytes.NewReader(reqBody))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("libretranslate status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	var data struct {
		TranslatedText string `json:"translatedText"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return "", err
	}

	res := strings.TrimSpace(data.TranslatedText)
	if res == "" {
		return "", fmt.Errorf("empty translation from libretranslate")
	}
	return res, nil
}

func translateWithMyMemory(text, target string) (string, error) {
	queryEndpoint := func(source string) (string, error) {
		reqURL := fmt.Sprintf("https://api.mymemory.translated.net/get?q=%s&langpair=%s|%s",
			url.QueryEscape(text), url.QueryEscape(source), url.QueryEscape(target))

		client := &http.Client{Timeout: 10 * time.Second}
		resp, err := client.Get(reqURL)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("mymemory status %d", resp.StatusCode)
		}

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return "", err
		}

		var data struct {
			ResponseData struct {
				TranslatedText string `json:"translatedText"`
			} `json:"responseData"`
			ResponseDetails string `json:"responseDetails"`
		}
		if err := json.Unmarshal(body, &data); err != nil {
			return "", err
		}

		translated := strings.TrimSpace(data.ResponseData.TranslatedText)
		details := strings.TrimSpace(data.ResponseDetails)

		if strings.Contains(strings.ToLower(translated), "invalid source language") ||
			strings.Contains(strings.ToLower(details), "invalid source language") {
			return "", fmt.Errorf("invalid source language")
		}

		if translated == "" {
			return "", fmt.Errorf("%s", details)
		}
		return translated, nil
	}

	// Try auto source first
	if res, err := queryEndpoint("auto"); err == nil && res != "" {
		return res, nil
	}

	// Fallback to explicit English source
	if res, err := queryEndpoint("en"); err == nil && res != "" {
		return res, nil
	}

	return "", fmt.Errorf("mymemory could not translate text")
}

func translateText(text, target string) (string, error) {
	if res, err := translateWithLibreTranslate(text, target); err == nil && res != "" {
		return res, nil
	}
	return translateWithMyMemory(text, target)
}

func buildLanguageHelpMessage(code string) string {
	return fmt.Sprintf("❌ Invalid or unsupported target language code: `%s`.\n"+
		"Use ISO language codes like: `en`, `es`, `fr`, `de`, `ja`, `ko`, `tl`, `zh-CN`.\n"+
		"Note: Filipino/Tagalog is `tl` (not `ph`).\n"+
		"Reference: <https://en.wikipedia.org/wiki/List_of_ISO_639-1_codes>", code)
}

// Slash command: /translate text: <text> to: <lang>
func handleTranslate(s *discordgo.Session, i *discordgo.InteractionCreate) {
	options := i.ApplicationCommandData().Options
	var text, rawTo string
	for _, opt := range options {
		if opt.Name == "text" {
			text = opt.StringValue()
		} else if opt.Name == "to" {
			rawTo = opt.StringValue()
		}
	}

	to := normalizeTargetLanguage(rawTo)
	if !langCodeRegex.MatchString(to) {
		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: buildLanguageHelpMessage(rawTo),
			},
		})
		return
	}

	// Defer reply while translating
	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	})

	translated, err := translateText(text, to)
	if err != nil {
		_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
			Content: fmt.Sprintf("❌ Translation failed: %v", err),
		})
		return
	}

	respContent := fmt.Sprintf("**Original:** %s\n\n**Translated (%s):** %s", text, to, translated)
	_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
		Content: respContent,
	})
}
