// internetarchive.go — /ia slash commands wrapping the Internet Archive public
// APIs (https://archive.org/developers/). All endpoints used here are public
// and need no authentication:
//
//	Metadata read   GET https://archive.org/metadata/{identifier}
//	Advanced search GET https://archive.org/advancedsearch.php?q=...&output=json
//	Scrape search   GET https://archive.org/services/search/v1/scrape?q=...
//	Full-text       GET https://archive.org/services/search/v1/scrape?... (ft via q)
//	Downloads       GET https://archive.org/download/{identifier}/{filename}
//	Thumbnails      GET https://archive.org/services/img/{identifier}
//	Wayback avail.  GET https://archive.org/wayback/available?url=...
//
// A descriptive User-Agent is sent on every request per the Archive's bots
// policy (https://archive.org/developers/bots.html).
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	iaBase       = "https://archive.org"
	iaTimeout    = 15 * time.Second
	iaUserAgent  = "ElyLightspeedBot/1.0 (Discord bot; https://github.com/daniel-kill/ElyLightspeed)"
	iaMaxResults = 5
)

// iaGet fetches a URL and returns the raw bytes, or an error on non-2xx.
func iaGet(rawURL string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", iaUserAgent)
	client := &http.Client{Timeout: iaTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return body, fmt.Errorf("HTTP %d from %s", resp.StatusCode, rawURL)
	}
	return body, nil
}

// iaGetJSON fetches and decodes a JSON body.
func iaGetJSON(rawURL string, out interface{}) error {
	body, err := iaGet(rawURL)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, out)
}

// ---- /ia search ------------------------------------------------------------

// iaAdvancedSearch runs a metadata search against the Advanced Search API.
func iaAdvancedSearch(query string, rows int) ([]map[string]interface{}, int64, error) {
	var out struct {
		Response struct {
			NumFound int64                    `json:"numFound"`
			Start    int                      `json:"start"`
			Docs     []map[string]interface{} `json:"docs"`
		} `json:"response"`
	}
	params := url.Values{}
	params.Set("q", query)
	params.Set("fl[]", "identifier,title,creator,year,mediatype,description")
	params.Set("rows", fmt.Sprintf("%d", rows))
	params.Set("output", "json")
	raw := iaBase + "/advancedsearch.php?" + params.Encode()
	if err := iaGetJSON(raw, &out); err != nil {
		return nil, 0, err
	}
	return out.Response.Docs, out.Response.NumFound, nil
}

// ---- /ia item ---------------------------------------------------------------

type iaItemMeta struct {
	Identifier  string      `json:"identifier"`
	Mediatype   string      `json:"mediatype"`
	Title       string      `json:"title"`
	Creator     interface{} `json:"creator"`
	Description interface{} `json:"description"`
	Date        string      `json:"date"`
	Year        string      `json:"year"`
	Collection  interface{} `json:"collection"`
	Subject     interface{} `json:"subject"`
	Uploader    string      `json:"uploader"`
}

type iaItemRecord struct {
	Created    int64        `json:"created"`
	ItemSize   int64        `json:"item_size"`
	FilesCount int          `json:"files_count"`
	Metadata   iaItemMeta   `json:"metadata"`
	Files      []iaItemFile `json:"files"`
}

type iaItemFile struct {
	Name   string `json:"name"`
	Size   string `json:"size"`
	Format string `json:"format"`
	Source string `json:"source"`
}

// iaFetchItem fetches the full metadata record for one item.
func iaFetchItem(identifier string) (*iaItemRecord, error) {
	var rec iaItemRecord
	raw := iaBase + "/metadata/" + url.PathEscape(identifier)
	if err := iaGetJSON(raw, &rec); err != nil {
		return nil, err
	}
	return &rec, nil
}

// ---- /ia wayback ------------------------------------------------------------

type iaWaybackResult struct {
	URL               string `json:"url"`
	ArchivedSnapshots struct {
		Closest struct {
			Available bool   `json:"available"`
			URL       string `json:"url"`
			Timestamp string `json:"timestamp"`
			Status    string `json:"status"`
		} `json:"closest"`
	} `json:"archived_snapshots"`
}

// iaWaybackAvailable checks if a URL has a Wayback snapshot.
func iaWaybackAvailable(pageURL string) (*iaWaybackResult, error) {
	var out iaWaybackResult
	params := url.Values{}
	params.Set("url", pageURL)
	raw := iaBase + "/wayback/available?" + params.Encode()
	if err := iaGetJSON(raw, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ---- /ia stats (views API) --------------------------------------------------

type iaStatsView struct {
	AllTime   int64 `json:"all_time"`
	Last30Day int64 `json:"last_30day"`
	Last7Day  int64 `json:"last_7day"`
	HaveData  bool  `json:"have_data"`
}

// iaFetchStats pulls view counts for an item (Views data service API).
// https://be-api.us.archive.org/views/v1/short/<identifier>
func iaFetchStats(identifier string) (*iaStatsView, error) {
	var out map[string]iaStatsView
	raw := "https://be-api.us.archive.org/views/v1/short/" + url.PathEscape(identifier)
	if err := iaGetJSON(raw, &out); err != nil {
		return nil, err
	}
	v, ok := out[identifier]
	if !ok {
		return &iaStatsView{}, nil
	}
	return &v, nil
}

// ---- /ia fts (full-text search via scrape API) ------------------------------

func iaFullText(query string, count int) ([]map[string]interface{}, int64, error) {
	var out struct {
		Items []map[string]interface{} `json:"items"`
		Total int64                    `json:"total"`
	}
	params := url.Values{}
	params.Set("q", query)
	params.Set("fields", "identifier,title,mediatype,creator")
	// The scraping API requires count >= 100; we only display iaMaxResults.
	if count < 100 {
		count = 100
	}
	params.Set("count", fmt.Sprintf("%d", count))
	raw := iaBase + "/services/search/v1/scrape?" + params.Encode()
	if err := iaGetJSON(raw, &out); err != nil {
		return nil, 0, err
	}
	if len(out.Items) > iaMaxResults {
		out.Items = out.Items[:iaMaxResults]
	}
	return out.Items, out.Total, nil
}

// ---- Discord rendering ------------------------------------------------------

// iaStrFrom is a small helper: interface{} -> trimmed string.
func iaStrFrom(v interface{}) string {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case []interface{}:
		var parts []string
		for _, e := range t {
			if s := iaStrFrom(e); s != "" {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, ", ")
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", t)
	}
}

// iaClip keeps a string under a max length, appending an ellipsis.
func iaClip(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return strings.TrimSpace(s[:max-1]) + "…"
}

// iaField renders "**Name:** value" when value is non-empty.
func iaField(name, value string) string {
	if value == "" {
		return ""
	}
	return fmt.Sprintf("**%s:** %s\n", name, value)
}

// iaThumbURL returns the thumbnail URL for an item.
func iaThumbURL(identifier string) string {
	return fmt.Sprintf("%s/services/img/%s", iaBase, url.PathEscape(identifier))
}

// iaDetailsURL returns the details (item) page URL.
func iaDetailsURL(identifier string) string {
	return fmt.Sprintf("%s/details/%s", iaBase, url.PathEscape(identifier))
}

// iaDownloadURL returns a file download URL.
func iaDownloadURL(identifier, filename string) string {
	return fmt.Sprintf("%s/download/%s/%s", iaBase,
		url.PathEscape(identifier), url.PathEscape(filename))
}

// formatIASearch renders the /ia search result list.
func formatIASearch(query string, docs []map[string]interface{}, numFound int64) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("🔎 **Internet Archive search — `%s`** (%d results)\n\n", iaClip(query, 60), numFound))
	if len(docs) == 0 {
		b.WriteString("No results found.")
		return b.String()
	}
	for i, d := range docs {
		id := iaStrFrom(d["identifier"])
		title := iaStrFrom(d["title"])
		if title == "" {
			title = id
		}
		creator := iaClip(iaStrFrom(d["creator"]), 80)
		year := iaStrFrom(d["year"])
		media := iaStrFrom(d["mediatype"])
		if media == "" {
			media = "item"
		}
		b.WriteString(fmt.Sprintf("**%d.** **%s**\n", i+1, iaClip(title, 120)))
		if creator != "" {
			b.WriteString(fmt.Sprintf("    _%s_", iaClip(creator, 80)))
			if year != "" {
				b.WriteString(fmt.Sprintf(" · %s", year))
			}
			b.WriteString("\n")
		}
		b.WriteString(fmt.Sprintf("    `%s` · %s · <https://archive.org/details/%s>\n",
			id, media, url.PathEscape(id)))
	}
	return b.String()
}

// formatIAItem renders the /ia item metadata record.
func formatIAItem(rec *iaItemRecord) string {
	md := rec.Metadata
	var b strings.Builder
	title := md.Title
	if title == "" {
		title = md.Identifier
	}
	b.WriteString(fmt.Sprintf("📦 **%s**\n", iaClip(title, 150)))
	b.WriteString(fmt.Sprintf("`%s` · %s\n", md.Identifier, iaFieldVal(md.Mediatype, "mediatype")))
	if c := iaClip(iaStrFrom(md.Creator), 100); c != "" {
		b.WriteString(fmt.Sprintf("_by %s_\n", c))
	}
	if d := iaStrFrom(md.Date); d == "" {
		if y := iaStrFrom(md.Year); y != "" {
			d = y
		}
	}
	if d := iaStrFrom(md.Date); d != "" {
		b.WriteString(fmt.Sprintf("**Date:** %s\n", d))
	}
	if s := iaClip(iaStrFrom(md.Subject), 200); s != "" {
		b.WriteString(fmt.Sprintf("**Subjects:** %s\n", s))
	}
	if u := iaStrFrom(md.Uploader); u != "" {
		b.WriteString(fmt.Sprintf("**Uploader:** %s\n", u))
	}
	b.WriteString(fmt.Sprintf("**Size:** %s\n", humanBytes(rec.ItemSize)))
	b.WriteString(fmt.Sprintf("**Files:** %d\n", rec.FilesCount))
	// List the first few files.
	if len(rec.Files) > 0 {
		b.WriteString("\n**Files:**\n")
		for i, f := range rec.Files {
			if i >= 8 {
				b.WriteString(fmt.Sprintf("_… and %d more_", len(rec.Files)-i))
				break
			}
			b.WriteString(fmt.Sprintf("  • `%s` (%s · %s)\n",
				iaClip(f.Name, 60), f.Format, humanBytesStr(f.Size)))
		}
	}
	b.WriteString(fmt.Sprintf("\n🔗 <https://archive.org/details/%s>", url.PathEscape(md.Identifier)))
	return b.String()
}

// iaFieldVal is a tiny helper: return "value (key)" when non-empty.
func iaFieldVal(v, key string) string {
	if v == "" || v == "null" {
		return ""
	}
	return v
}

// formatIAWayback renders the /ia wayback availability result.
func formatIAWayback(r *iaWaybackResult) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("🕰️ **Wayback Machine — `%s`**\n", iaClip(r.URL, 80)))
	closest := r.ArchivedSnapshots.Closest
	if !closest.Available || closest.URL == "" {
		b.WriteString("No snapshot found for this URL.")
		return b.String()
	}
	ts := closest.Timestamp
	date := "unknown"
	if len(ts) >= 8 {
		date = fmt.Sprintf("%s-%s-%s", ts[0:4], ts[4:6], ts[6:8])
	}
	b.WriteString(fmt.Sprintf("✅ Snapshot found: **%s** (status %s)\n", date, closest.Status))
	b.WriteString(fmt.Sprintf("🔗 <https://web.archive.org/web/%s/%s>", ts, strings.TrimPrefix(closest.URL, "http")))
	return b.String()
}

// formatIAStats renders the /ia stats result.
func formatIAStats(identifier string, st *iaStatsView) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("📊 **Internet Archive stats — `%s`**\n", identifier))
	b.WriteString(fmt.Sprintf("👁️ **All-time views:** %d\n", st.AllTime))
	b.WriteString(fmt.Sprintf("📅 **Last 30 days:** %d\n", st.Last30Day))
	b.WriteString(fmt.Sprintf("📅 **Last 7 days:** %d\n", st.Last7Day))
	if !st.HaveData {
		b.WriteString("_No view data available for this item._\n")
	}
	b.WriteString(fmt.Sprintf("🔗 <https://archive.org/details/%s>", url.PathEscape(identifier)))
	return b.String()
}

// formatIAFTS renders the /ia fts result.
func formatIAFTS(query string, items []map[string]interface{}, total int64) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("📖 **Full-text search — `%s`** (%d matches)\n\n", iaClip(query, 60), total))
	if len(items) == 0 {
		b.WriteString("No matches found.")
		return b.String()
	}
	for i, d := range items {
		id := iaStrFrom(d["identifier"])
		title := iaStrFrom(d["title"])
		if title == "" {
			title = id
		}
		media := iaStrFrom(d["mediatype"])
		if media == "" {
			media = "item"
		}
		b.WriteString(fmt.Sprintf("**%d.** **%s**\n", i+1, iaClip(title, 120)))
		b.WriteString(fmt.Sprintf("    `%s` · %s · <https://archive.org/details/%s>\n",
			id, media, url.PathEscape(id)))
	}
	return b.String()
}

// humanBytes formats a byte count.
func humanBytes(n int64) string {
	if n <= 0 {
		return "0 B"
	}
	units := []string{"B", "KB", "MB", "GB", "TB"}
	f := float64(n)
	i := 0
	for f >= 1024 && i < len(units)-1 {
		f /= 1024
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%d B", n)
	}
	return fmt.Sprintf("%.1f %s", f, units[i])
}

// humanBytesStr parses a numeric string (IA file sizes are strings) and formats it.
func humanBytesStr(s string) string {
	var n int64
	fmt.Sscanf(s, "%d", &n)
	return humanBytes(n)
}

// iaContentForReply picks a small file (image/pdf) from an item to attach,
// or "" if none is suitable.
func iaContentForReply(identifier string, files []iaItemFile) string {
	preferred := []string{"__ia_thumb.jpg", "thumb", ".jpg", ".png", ".gif", ".webp", ".mp3", ".ogg", ".pdf"}
	for _, p := range preferred {
		for _, f := range files {
			if strings.Contains(f.Name, p) {
				// Only auto-attach small files (< 4 MB) to keep replies snappy.
				var size int64
				fmt.Sscanf(f.Size, "%d", &size)
				if size > 0 && size > 4*1024*1024 {
					continue
				}
				return f.Name
			}
		}
	}
	return ""
}
