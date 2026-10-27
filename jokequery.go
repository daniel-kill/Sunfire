// jokequery.go — answers "/any chuck" and "/any dad" with a random joke from
// the dedicated joke APIs. These short-circuit before the general fan-out.
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

// randomChuckNorris fetches a random Chuck Norris joke.
func randomChuckNorris() (string, error) {
	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Get("https://api.chucknorris.io/jokes/random")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
	if err != nil {
		return "", err
	}
	var d struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal(body, &d); err != nil {
		return "", err
	}
	if strings.TrimSpace(d.Value) == "" {
		return "", fmt.Errorf("empty Chuck Norris response")
	}
	return d.Value, nil
}

// randomDadJoke fetches a random dad joke. icanhazdadjoke requires the
// Accept: application/json header (otherwise it returns HTML).
func randomDadJoke() (string, error) {
	client := &http.Client{Timeout: 8 * time.Second}
	req, err := http.NewRequest("GET", "https://icanhazdadjoke.com/", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
	if err != nil {
		return "", err
	}
	var d struct {
		Joke string `json:"joke"`
	}
	if err := json.Unmarshal(body, &d); err != nil {
		return "", err
	}
	if strings.TrimSpace(d.Joke) == "" {
		return "", fmt.Errorf("empty dad joke response")
	}
	return d.Joke, nil
}

// handleJokeQuery answers "/any chuck" and "/any dad" with a random joke.
// Returns true if it handled the query.
func handleJokeQuery(s *discordgo.Session, i *discordgo.InteractionCreate, query string) bool {
	q := strings.ToLower(strings.TrimSpace(query))
	var (
		msg string
		err error
	)
	switch q {
	case "chuck", "chuck norris":
		msg, err = randomChuckNorris()
	case "dad", "dad joke", "dadjoke":
		msg, err = randomDadJoke()
	default:
		return false
	}
	if err != nil {
		msg = "❌ Couldn't fetch a joke: " + err.Error()
	} else if q == "dad" || q == "dad joke" || q == "dadjoke" {
		msg = "👨‍👦 **Dad Joke:** " + msg
	} else {
		msg = "🤠 **Chuck Norris:** " + msg
	}
	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Content: msg},
	})
	return true
}
