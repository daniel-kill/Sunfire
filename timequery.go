// timequery.go — answers "what time is it in <place>" questions via the free
// timeapi.io endpoint, with a small city→IANA timezone lookup.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
)

// timeZones maps common city/place names to IANA timezone ids.
var timeZones = map[string]string{
	"manila":        "Asia/Manila",
	"tokyo":         "Asia/Tokyo",
	"singapore":     "Asia/Singapore",
	"hong kong":     "Asia/Hong_Kong",
	"beijing":       "Asia/Shanghai",
	"shanghai":      "Asia/Shanghai",
	"seoul":         "Asia/Seoul",
	"mumbai":        "Asia/Kolkata",
	"new delhi":     "Asia/Kolkata",
	"bangkok":       "Asia/Bangkok",
	"jakarta":       "Asia/Jakarta",
	"kuala lumpur":  "Asia/Kuala_Lumpur",
	"sydney":        "Australia/Sydney",
	"melbourne":     "Australia/Melbourne",
	"auckland":      "Pacific/Auckland",
	"dubai":         "Asia/Dubai",
	"moscow":        "Europe/Moscow",
	"berlin":        "Europe/Berlin",
	"paris":         "Europe/Paris",
	"london":        "Europe/London",
	"madrid":        "Europe/Madrid",
	"rome":          "Europe/Rome",
	"amsterdam":     "Europe/Amsterdam",
	"stockholm":     "Europe/Stockholm",
	"oslo":          "Europe/Oslo",
	"istanbul":      "Europe/Istanbul",
	"new york":      "America/New_York",
	"nyc":           "America/New_York",
	"chicago":       "America/Chicago",
	"los angeles":   "America/Los_Angeles",
	"la":            "America/Los_Angeles",
	"san francisco": "America/Los_Angeles",
	"seattle":       "America/Los_Angeles",
	"toronto":       "America/Toronto",
	"vancouver":     "America/Vancouver",
	"mexico city":   "America/Mexico_City",
	"denver":        "America/Denver",
	"phoenix":       "America/Phoenix",
	"miami":         "America/New_York",
	"atlanta":       "America/New_York",
	"houston":       "America/Chicago",
	"dallas":        "America/Chicago",
	"sao paulo":     "America/Sao_Paulo",
	"buenos aires":  "America/Argentina/Buenos_Aires",
	"lisbon":        "Europe/Lisbon",
	"dublin":        "Europe/Dublin",
	"warsaw":        "Europe/Warsaw",
	"athens":        "Europe/Athens",
	"cairo":         "Africa/Cairo",
	"lagos":         "Africa/Lagos",
	"nairobi":       "Africa/Nairobi",
	"johannesburg":  "Africa/Johannesburg",
	"cape town":     "Africa/Johannesburg",
	"hawaii":        "Pacific/Honolulu",
	"honolulu":      "Pacific/Honolulu",
	"anchorage":     "America/Anchorage",
	"alaska":        "America/Anchorage",
}

// timeAPIZone is the endpoint for a fixed IANA zone.
const timeAPIZone = "https://timeapi.io/api/Time/current/zone?timeZone="

// timeAPIResult mirrors the JSON timeapi.io returns.
type timeAPIResult struct {
	DateTime  string `json:"dateTime"`
	TimeZone  string `json:"timeZone"`
	DayOfWeek string `json:"dayOfWeek"`
}

// isTimeQuery reports whether a query asks for the time in a place.
func isTimeQuery(query string) (string, bool) {
	q := strings.ToLower(strings.TrimSpace(query))
	place := ""
	// Match common phrasings: "time in X", "time is it in X", "time in X?" etc.
	for _, marker := range []string{"time is it in", "time it is in", "time in"} {
		if i := strings.Index(q, marker); i >= 0 {
			place = strings.TrimSpace(q[i+len(marker):])
			place = strings.TrimSuffix(place, "?")
			place = strings.TrimSpace(place)
			break
		}
	}
	if place == "" {
		return "", false
	}
	zone, ok := timeZones[place]
	if !ok {
		return "", false
	}
	return zone, true
}

// fetchTimeZoneTime hits timeapi.io and returns a formatted time string.
func fetchTimeZoneTime(zone string) (string, error) {
	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Get(timeAPIZone + zone)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
	if err != nil {
		return "", err
	}
	var t timeAPIResult
	if err := json.Unmarshal(body, &t); err != nil {
		return "", err
	}
	if t.DateTime == "" {
		return "", fmt.Errorf("empty response from timeapi.io")
	}
	return fmt.Sprintf("🕐 **%s** · %s (%s)", t.DateTime, t.DayOfWeek, t.TimeZone), nil
}

// handleTime answers a "what time is it in X" query directly.
func handleTime(s *discordgo.Session, i *discordgo.InteractionCreate, query string) bool {
	zone, ok := isTimeQuery(query)
	if !ok {
		return false // not a time query — let /any handle it
	}
	msg, err := fetchTimeZoneTime(zone)
	if err != nil {
		msg = "❌ Couldn't fetch the time for that location: " + err.Error()
	}
	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Content: msg},
	})
	return true
}
