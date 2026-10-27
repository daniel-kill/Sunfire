// anyapi.go — the /any command: fan out the user's query to multiple API
// providers concurrently and return the first successful response.
//
// Providers are configured in .private/config.json:
//
//	"anyApi": [
//	  {"name": "wikipedia", "url": "https://en.wikipedia.org/api/rest_v1/page/summary/{query}", ...},
//	  ...
//	]
//
// The {query} placeholder in each URL is URL-escaped. All providers are hit in
// parallel with goroutines; the first 2xx response wins, so latency is bounded
// by the fastest provider, not the slowest — the "lightspeed" payoff.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
)

const anyAPITimeout = 8 * time.Second

// anyAPIResult is the outcome of one provider call.
type anyAPIResult struct {
	provider string
	body     string
	status   int
	err      error
	query    string // the (normalized) search term, for no-results messages
}

// anyAPIResponse builds the content for a /any response from a result.
// It parses the body according to which provider produced it, so the reply is
// a clean summary rather than a raw JSON dump.
func anyAPIResponse(res anyAPIResult) string {
	if res.err != nil {
		return fmt.Sprintf("`%s` error: %v", res.provider, res.err)
	}
	switch res.provider {
	case "Wikipedia":
		if s := formatWikipedia(res.body); s != "" {
			return s
		}
	case "DuckDuckGo Instant Answer", "DuckDuckGo":
		if s := formatDuckDuckGo(res.body); s != "" {
			return s
		}
	case "SWAPI (Star Wars)", "Rick and Morty", "Dad Jokes", "Chuck Norris":
		if s := formatGenericList(res.provider, res.body); s != "" {
			return s
		}
		// Known list provider returned a 2xx but no parseable results — give a
		// clean "no results" line (with the search term) instead of raw JSON.
		term := res.query
		if term == "" {
			term = "that query"
		}
		return fmt.Sprintf("**%s** — no results found for `%s`.", res.provider, term)
	}
	// Fallback: unknown provider or unparseable body — show a short raw preview.
	body := res.body
	if len(body) > 800 {
		body = body[:800] + "…"
	}
	return fmt.Sprintf("**%s** (HTTP %d)\n```\n%s\n```", res.provider, res.status, body)
}

// formatWikipedia renders a Wikipedia REST summary JSON into a clean reply:
// the title as a bold header, a link, and the extract as body text.
func formatWikipedia(body string) string {
	var d struct {
		Title       string `json:"title"`
		Extract     string `json:"extract"`
		Description string `json:"description"`
		ContentURLs struct {
			Desktop struct {
				Page string `json:"page"`
			} `json:"desktop"`
		} `json:"content_urls"`
	}
	if err := json.Unmarshal([]byte(body), &d); err != nil || d.Extract == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString("**" + d.Title + "**")
	if d.Description != "" {
		b.WriteString(" — " + d.Description)
	}
	b.WriteString("\n\n")
	b.WriteString(d.Extract)
	if d.ContentURLs.Desktop.Page != "" {
		b.WriteString("\n\n" + d.ContentURLs.Desktop.Page)
	}
	return truncateContent(b.String())
}

// formatDuckDuckGo renders a DDG Instant Answer JSON into a clean reply:
// the Heading, any Answer/Abstract, then the top RelatedTopics as bullets.
func formatDuckDuckGo(body string) string {
	var d struct {
		Heading       string            `json:"Heading"`
		Answer        string            `json:"Answer"`
		AbstractText  string            `json:"AbstractText"`
		Definition    string            `json:"Definition"`
		RelatedTopics []json.RawMessage `json:"RelatedTopics"`
		AbstractURL   string            `json:"AbstractURL"`
	}
	if err := json.Unmarshal([]byte(body), &d); err != nil {
		return ""
	}
	// If DDG had no answer fields, don't format it (caller falls back to raw).
	if d.Heading == "" && d.Answer == "" && d.AbstractText == "" && len(d.RelatedTopics) == 0 {
		return ""
	}
	var b strings.Builder
	if d.Heading != "" {
		b.WriteString("**" + d.Heading + "**\n\n")
	}
	if d.Answer != "" {
		b.WriteString(d.Answer + "\n")
	}
	if d.AbstractText != "" {
		b.WriteString(d.AbstractText + "\n")
	}
	if d.Definition != "" {
		b.WriteString("*" + d.Definition + "*\n")
	}
	// RelatedTopics can contain both flat objects and nested {Name, Topics}.
	var topics []string
	appendTopic := func(tm json.RawMessage) {
		var t struct {
			Text string `json:"Text"`
		}
		if err := json.Unmarshal(tm, &t); err == nil && t.Text != "" {
			topics = append(topics, t.Text)
		}
	}
	for _, rm := range d.RelatedTopics {
		// Distinguish a flat topic from a nested group.
		var asMap map[string]json.RawMessage
		_ = json.Unmarshal(rm, &asMap)
		if nested, ok := asMap["Topics"]; ok {
			var inner []json.RawMessage
			if err := json.Unmarshal(nested, &inner); err == nil {
				for _, it := range inner {
					appendTopic(it)
				}
			}
			continue
		}
		appendTopic(rm)
	}
	if len(topics) > 0 {
		b.WriteString("\n")
		limit := len(topics)
		if limit > 5 {
			limit = 5
		}
		for _, t := range topics[:limit] {
			b.WriteString("• " + t + "\n")
		}
		if len(topics) > limit {
			b.WriteString("… and " + itoa(len(topics)-limit) + " more\n")
		}
	}
	return truncateContent(b.String())
}

// formatGenericList renders a "list of results" API (SWAPI, Rick and Morty,
// Dad Jokes, Chuck Norris) into a clean bulleted reply. These all return an
// array under a "results"/"result" key, where each object has a "name" and/or
// "joke"/"value" field. We pick the most descriptive field per provider.
func formatGenericList(provider, body string) string {
	var d struct {
		Results []map[string]any `json:"results"`
		Result  []map[string]any `json:"result"`
	}
	if err := json.Unmarshal([]byte(body), &d); err != nil {
		return ""
	}
	items := d.Results
	if len(items) == 0 {
		items = d.Result
	}
	if len(items) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("**" + provider + "**\n")
	limit := len(items)
	if limit > 5 {
		limit = 5
	}
	for _, it := range items[:limit] {
		line := ""
		switch provider {
		case "Dad Jokes", "Chuck Norris":
			line, _ = it["joke"].(string)
			if line == "" {
				line, _ = it["value"].(string)
			}
		default: // SWAPI, Rick and Morty -> "name"
			line, _ = it["name"].(string)
		}
		if line == "" {
			continue
		}
		b.WriteString("• " + line + "\n")
	}
	if len(items) > limit {
		b.WriteString("… and " + itoa(len(items)-limit) + " more\n")
	}
	return truncateContent(b.String())
}

// truncateContent cuts a formatted reply to fit Discord's 2000-char limit.
func truncateContent(s string) string {
	const max = 1900
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

// itoa is a tiny helper to avoid importing strconv in the hot path.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	if neg {
		digits = append([]byte{'-'}, digits...)
	}
	return string(digits)
}

// fanOutAny hits every provider concurrently, returns the first success.
func fanOutAny(query string, providers []AnyAPIProvider) (anyAPIResult, bool) {
	if len(providers) == 0 {
		return anyAPIResult{provider: "none", err: fmt.Errorf("no providers configured")}, false
	}
	results := make(chan anyAPIResult, len(providers))
	var wg sync.WaitGroup
	for _, p := range providers {
		p := p
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- callProvider(query, p)
		}()
	}
	// Close the channel once all providers finish.
	go func() {
		wg.Wait()
		close(results)
	}()
	// Return the first USEFUL success. A 2xx with an empty/trivial body is not a
	// win (e.g. DuckDuckGo's empty JSON for unanswered queries) — keep looking.
	var firstErr anyAPIResult
	gotErr := false
	for r := range results {
		if r.err == nil && r.status >= 200 && r.status < 300 && useful(r.body) {
			return r, true
		}
		// Only remember genuine errors. A useless 2xx (r.err == nil) must NOT
		// count as an error — otherwise a single useless-200 provider would set
		// gotErr=true and we'd return it with a nil err (panic in the handler).
		if r.err != nil && !gotErr {
			firstErr = r
			gotErr = true
		}
	}
	// No useful result. Report a real error if we saw one; otherwise say all
	// providers returned empty/useless responses (never return a nil err).
	if !gotErr {
		firstErr = anyAPIResult{provider: "all", err: fmt.Errorf("all providers returned empty/useless responses")}
	}
	return firstErr, false
}

// useful reports whether a response body is worth surfacing. Empty or
// near-empty bodies (and bodies that are just an empty JSON object / array or
// whitespace) are treated as useless so the /any command doesn't return
// garbage just because some API answered 200 with nothing.
func useful(body string) bool {
	b := strings.TrimSpace(body)
	if b == "" {
		return false
	}
	// Empty JSON object or array:  {}  []
	if b == "{}" || b == "[]" || b == "null" {
		return false
	}
	// JSON object: useful only if it has at least one content-bearing value.
	if strings.HasPrefix(b, "{") {
		if !json.Valid([]byte(b)) {
			return false
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(b), &m); err == nil && len(m) > 0 {
			// Semantic check: DuckDuckGo's Instant Answer API returns this
			// exact "no answer" shape when it has nothing — all the answer
			// fields empty, only boilerplate meta. Treat it as useless.
			if isDDGNoAnswer(m) {
				return false
			}
			// List-style APIs (Dad Jokes, Chuck Norris, SWAPI, Rick & Morty)
			// return a "results"/"result" array. If that array is empty, the
			// response has no real content even if pagination metadata
			// (current_page, limit, total) is present — treat it as useless so
			// it doesn't win over a provider that DID return results.
			if isEmptyContentArray(m) {
				return false
			}
			return hasContent(m)
		}
	}
	// JSON array: useful if it has a non-empty element.
	if strings.HasPrefix(b, "[") && json.Valid([]byte(b)) {
		var arr []any
		if err := json.Unmarshal([]byte(b), &arr); err == nil {
			return hasContent(arr)
		}
	}
	// Anything else with real content is fine.
	return len([]rune(b)) >= 1
}

// isDDGNoAnswer detects DuckDuckGo's Instant Answer "no result" response:
// the answer-bearing fields (Abstract/Answer/Definition/Heading) are all
// empty AND the topic lists are empty. The `meta` block is boilerplate.
//
// Guard: only applies when the object is actually a DDG Instant Answer
// response (has the "meta" object). Without this guard, ANY JSON object that
// happens to lack these keys (e.g. {"ok":true,"value":"fast"}) would be
// wrongly classified as a "no answer".
func isDDGNoAnswer(m map[string]any) bool {
	if _, hasMeta := m["meta"]; !hasMeta {
		return false
	}
	str := func(k string) string {
		s, _ := m[k].(string)
		return s
	}
	empty := func(k string) bool { return strings.TrimSpace(str(k)) == "" }
	emptyArr := func(k string) bool {
		a, _ := m[k].([]any)
		return len(a) == 0
	}
	return empty("Abstract") && empty("Answer") && empty("Definition") &&
		empty("Heading") && emptyArr("RelatedTopics") && emptyArr("Results")
}

// isEmptyContentArray reports whether a JSON object has a "results" or
// "result" key that is an empty array. Used to reject list-style API responses
// that return no actual content (only pagination/boilerplate metadata).
func isEmptyContentArray(m map[string]any) bool {
	for _, key := range []string{"results", "result", "docs"} {
		if arr, ok := m[key].([]any); ok {
			return len(arr) == 0
		}
	}
	return false
}

// hasContent recursively reports whether a decoded JSON value contains any
// meaningful content (non-empty strings, non-empty arrays/maps of content,
// non-zero numbers, or booleans).
func hasContent(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case string:
		return strings.TrimSpace(t) != ""
	case bool:
		return true
	case float64:
		return t != 0
	case []any:
		for _, e := range t {
			if hasContent(e) {
				return true
			}
		}
		return false
	case map[string]any:
		for _, val := range t {
			if hasContent(val) {
				return true
			}
		}
		return false
	}
	return false
}

// callProvider requests one provider and returns the result.
func callProvider(query string, p AnyAPIProvider) anyAPIResult {
	u := strings.ReplaceAll(p.URL, "{query}", url.QueryEscape(query))
	timeout := p.Timeout
	if timeout <= 0 {
		timeout = 8
	}
	client := &http.Client{Timeout: time.Duration(timeout) * time.Second}
	// All our providers return JSON; explicitly request JSON so we never get a
	// default HTML page (e.g. icanhazdadjoke serves HTML unless Accept is set).
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return anyAPIResult{provider: p.Name, err: err}
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "ElyLightspeed/1.0 (+https://github.com/daniel-kill/ElyLightspeed)")
	resp, err := client.Do(req)
	if err != nil {
		return anyAPIResult{provider: p.Name, err: err}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return anyAPIResult{provider: p.Name, status: resp.StatusCode, err: err}
	}
	res := anyAPIResult{provider: p.Name, body: string(body), status: resp.StatusCode, query: query}

	// Wikipedia enhancement: the REST summary endpoint needs a page TITLE, so
	// question-phrases ("what is a zebra") 404. If we got a 404 (or useless
	// content), search for the best-matching page title and re-fetch its
	// summary. This makes `/any <question>` return real content.
	if p.Name == "Wikipedia" && (res.status == 404 || !useful(res.body)) {
		if summary := wikipediaSearchSummary(query, client); summary != "" {
			res.body = summary
			res.status = 200
		}
	}
	return res
}

// wikipediaSearchSummary uses the Wikipedia search API to find the best page
// title for a natural-language query, then fetches that page's REST summary.
// Returns "" if nothing usable is found.
func wikipediaSearchSummary(query string, client *http.Client) string {
	searchURL := "https://en.wikipedia.org/w/api.php?action=query&list=search&srsearch=" +
		url.QueryEscape(query) + "&srlimit=1&format=json&formatversion=2"
	resp, err := client.Get(searchURL)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return ""
	}
	var sr struct {
		Query struct {
			Search []struct {
				Title string `json:"title"`
			} `json:"search"`
		} `json:"query"`
	}
	if err := json.Unmarshal(body, &sr); err != nil {
		return ""
	}
	if len(sr.Query.Search) == 0 {
		return ""
	}
	title := sr.Query.Search[0].Title
	summaryURL := "https://en.wikipedia.org/api/rest_v1/page/summary/" + url.PathEscape(title)
	resp2, err := client.Get(summaryURL)
	if err != nil {
		return ""
	}
	defer resp2.Body.Close()
	body2, err := io.ReadAll(io.LimitReader(resp2.Body, 64<<10))
	if err != nil {
		return ""
	}
	if resp2.StatusCode != 200 || !useful(string(body2)) {
		return ""
	}
	return string(body2)
}

// anyAPIConfigProviders returns the configured providers (empty if none).
func anyAPIConfigProviders() []AnyAPIProvider {
	cfg, err := loadConfig()
	if err != nil {
		return nil
	}
	return cfg.AnyAPI
}

// normalizeQuery strips leading question words / stopwords and trailing
// punctuation so the underlying APIs get a clean subject to search for.
// Examples: "what is a zebra" -> "zebra", "tell me about Go" -> "Go",
// "define photosynthesis" -> "photosynthesis".
func normalizeQuery(q string) string {
	q = strings.TrimSpace(q)
	q = strings.TrimSuffix(q, "?")
	q = strings.TrimSpace(q)
	// Repeatedly strip leading stopwords. Check multi-word openers first
	// (e.g. "tell me about", "how does") so they're consumed as one unit.
	for {
		fields := strings.Fields(q)
		if len(fields) == 0 {
			return ""
		}
		// Three-word opener: "tell me about X".
		if len(fields) >= 3 {
			three := strings.ToLower(fields[0] + " " + fields[1] + " " + fields[2])
			if isStopword(three) {
				q = strings.TrimSpace(strings.TrimPrefix(q, fields[0]+" "+fields[1]+" "+fields[2]))
				continue
			}
		}
		// Two-word opener like "what is", "how do", "tell me".
		if len(fields) >= 2 {
			two := strings.ToLower(fields[0] + " " + fields[1])
			if isStopword(two) {
				q = strings.TrimSpace(strings.TrimPrefix(q, fields[0]+" "+fields[1]))
				continue
			}
		}
		first := strings.ToLower(fields[0])
		if isStopword(first) {
			q = strings.TrimSpace(strings.TrimPrefix(q, fields[0]))
			continue
		}
		break
	}
	return q
}

// isStopword reports whether w is a common English question/stop word.
func isStopword(w string) bool {
	switch w {
	case "what", "whats", "what is", "what are", "what's",
		"who", "whos", "who is", "who's",
		"where", "wheres", "where is", "where's",
		"when", "whens", "when is", "when's",
		"why", "whys", "why is", "why's",
		"how", "hows", "how is", "how's", "how do", "how does", "how to",
		"is", "are", "am", "was", "were", "be", "being", "been",
		"a", "an", "the", "of", "and", "or", "for", "in", "on", "at", "to", "with",
		"tell", "tell me", "tell me about", "explain", "explain about",
		"define", "describe", "about", "please", "do", "does", "did",
		"can", "could", "would", "should", "may", "might", "will", "shall":
		return true
	}
	return false
}

// helpAny returns the usage guide shown by `/any help`.
func helpAny() string {
	return "**/any — query multiple APIs at once**\n\n" +
		"`/any <query>` sends your query to all configured APIs **in parallel** and returns the best result.\n\n" +
		"**Tips for good results:**\n" +
		"• Use a **subject, not a full question**: `/any zebra` works better than `/any what is a zebra` (the bot strips filler words, but a clean subject is best).\n" +
		"• **Time**: `/any what time is it in Manila` → live time for 55+ cities.\n" +
		"• **Definitions**: `/any <word>` → Wikipedia/DuckDuckGo summary.\n" +
		"• **Random jokes**: `/any dad` and `/any chuck` always return a random joke from the dedicated joke APIs.\n\n" +
		"**What each provider answers:**\n" +
		"• **Wikipedia** — encyclopedia summaries: `/any zebra`\n" +
		"• **DuckDuckGo** — instant answers + related topics: `/any Germany`\n" +
		"• **SWAPI (Star Wars)** — characters: `/any luke`\n" +
		"• **Rick and Morty** — characters: `/any rick`\n" +
		"• **Dad Jokes** — random dad joke: `/any dad`\n" +
		"• **Chuck Norris** — random Chuck Norris joke: `/any chuck`\n\n" +
		"**Examples:**\n" +
		"`/any zebra` · `/any what time is it in Tokyo` · `/any luke` · `/any rick`\n" +
		"`/any Germany` · `/any python` · `/any dad` · `/any chuck`\n\n" +
		"_The bot returns the first useful result across all providers._"
}

// handleAny is the /any slash-command handler.
func handleAny(s *discordgo.Session, i *discordgo.InteractionCreate) {
	// The command takes a "query" string option.
	query := ""
	if opts := i.ApplicationCommandData().Options; len(opts) > 0 {
		query = opts[0].StringValue()
	}
	query = strings.TrimSpace(query)
	// Empty query or explicit "help" -> show usage guide.
	lower := strings.ToLower(query)
	if query == "" || lower == "help" || lower == "help me" || lower == "?" {
		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{Content: helpAny()},
		})
		return
	}

	// Answer "what time is it in <place>" directly (the time API doesn't use a
	// {query} placeholder, so it's handled specially before the fan-out).
	if handleTime(s, i, query) {
		return
	}

	// "/any dad" and "/any chuck" always return a random joke from the
	// dedicated joke APIs.
	if handleJokeQuery(s, i, query) {
		return
	}

	// Strip leading question words/stopwords ("what is a zebra" -> "zebra")
	// so Wikipedia and DDG get a clean subject to search for.
	query = normalizeQuery(query)

	providers := anyAPIConfigProviders()
	res, ok := fanOutAny(query, providers)
	if !ok {
		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "❌ No API results — " + res.err.Error(),
			},
		})
		return
	}
	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Content: anyAPIResponse(res)},
	})
}

// makeAnyAPIJSON is a small helper to demonstrate the config shape (used by
// tests/docs); not required at runtime.
func makeAnyAPIJSON(providers []AnyAPIProvider) string {
	b, _ := json.MarshalIndent(providers, "", "  ")
	return string(b)
}
