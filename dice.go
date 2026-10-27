// dice.go — the /dice command: simulates a classic dice game where the user
// picks a win chance and we roll 0.00-100.00 millions of times to measure how
// often they'd win (roll < chance = win).
package main

import (
	"fmt"
	"math/rand"
	"strconv"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
)

const (
	diceMinChance = 2.0  // minimum win chance %
	diceMaxChance = 98.0 // maximum win chance %
	diceRollMax   = 100.0
	diceDefaultIter = 10_000_000
)

// parseDiceChance parses and validates the user's win chance (2.00-98.00).
func parseDiceChance(raw string) (float64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, fmt.Errorf("no win chance given")
	}
	chance, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, fmt.Errorf("%q is not a valid number", raw)
	}
	if chance < diceMinChance || chance > diceMaxChance {
		return 0, fmt.Errorf("win chance must be between %.2f and %.2f", diceMinChance, diceMaxChance)
	}
	return chance, nil
}

// rollDice returns a random roll in [0, 100).
func rollDice(rng *rand.Rand) float64 {
	return rng.Float64() * diceRollMax
}

// simulateDice runs iterations rolls and returns how many were wins
// (roll < chance).
func simulateDice(chance float64, iterations int) int {
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	wins := 0
	for i := 0; i < iterations; i++ {
		if rollDice(rng) < chance {
			wins++
		}
	}
	return wins
}

// formatDiceResult renders the simulation summary.
func formatDiceResult(chance float64, iterations, wins int) string {
	pct := float64(wins) * 100 / float64(iterations)
	var b strings.Builder
	b.WriteString(fmt.Sprintf("🎲 **Dice Simulation — %d rolls**\n", iterations))
	b.WriteString(fmt.Sprintf("Win chance: **%.2f%%** · roll 0.00-%.0f · win when roll < %.2f\n\n",
		chance, diceRollMax, chance))
	b.WriteString(fmt.Sprintf("• **Wins:** %d times (%.2f%%)\n", wins, pct))
	b.WriteString(fmt.Sprintf("• **Losses:** %d times (%.2f%%)\n", iterations-wins, 100-pct))
	b.WriteString(fmt.Sprintf("\n🎯 **Winning odds: 1 in %s rolls**", oneInX(wins, iterations)))
	return b.String()
}

// handleDice is the /dice slash-command handler.
func handleDice(s *discordgo.Session, i *discordgo.InteractionCreate) {
	raw := ""
	if opts := i.ApplicationCommandData().Options; len(opts) > 0 {
		raw = opts[0].StringValue()
	}
	chance, err := parseDiceChance(raw)
	if err != nil {
		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "❌ " + err.Error() + "\nUsage: `/dice chance:50` (win chance 2.00-98.00)",
			},
		})
		return
	}
	// Acknowledge immediately — the 10M-roll simulation takes a few seconds,
	// longer than Discord's 3s interaction window. Defer, then follow up.
	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	})
	wins := simulateDice(chance, diceDefaultIter)
	reply := formatDiceResult(chance, diceDefaultIter, wins)
	if len(reply) > 1900 {
		reply = reply[:1900] + "…"
	}
	_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{Content: reply})
}

func oneInX(wins, total int) string {
	if wins <= 0 {
		return "N/A"
	}
	ratio := float64(total) / float64(wins)
	if ratio < 10 {
		return fmt.Sprintf("%.2f", ratio)
	}
	return fmt.Sprintf("%.1f", ratio)
}
