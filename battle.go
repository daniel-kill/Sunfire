package main

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
)

type BattleSession struct {
	ChannelID string
	StarterID string
	Players   map[string]bool
	Started   bool
	CreatedAt time.Time
}

var (
	battleMu           sync.Mutex
	activeBattles      map[string]*BattleSession // channelID -> battle
	eliminationPhrases []string
)

func initBattle() {
	if len(eliminationPhrases) > 0 {
		return
	}
	path := dataPath("elimination-events.json")
	data, err := os.ReadFile(path)
	if err == nil {
		_ = json.Unmarshal(data, &eliminationPhrases)
	}
	if len(eliminationPhrases) == 0 {
		eliminationPhrases = []string{
			"had a nightmare and woke up suddenly! 🍬😰",
			"tripped over a moonbeam and tumbled out of the dream! 🌙💔",
			"got tangled in a blanket of stars and drifted away! ✨🌀",
			"was tickled awake by a swarm of giggling pixies! 🧚‍♀️😂",
		}
	}
	activeBattles = make(map[string]*BattleSession)
}

func handleBattleCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	initBattle()
	battleMu.Lock()
	defer battleMu.Unlock()

	options := i.ApplicationCommandData().Options
	if len(options) == 0 {
		return
	}

	sub := options[0]
	chID := i.ChannelID

	switch sub.Name {
	case "start":
		if b, ok := activeBattles[chID]; ok && b != nil {
			_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content: "🌙 A Sweet Dreams battle is already taking place or gathering dreamers in this channel!",
					Flags:   discordgo.MessageFlagsEphemeral,
				},
			})
			return
		}

		battle := &BattleSession{
			ChannelID: chID,
			StarterID: i.Member.User.ID,
			Players:   map[string]bool{i.Member.User.ID: true},
			Started:   false,
			CreatedAt: time.Now(),
		}
		activeBattles[chID] = battle

		startUnix := time.Now().Add(40 * time.Second).Unix()
		msg := fmt.Sprintf("🌙 **<@%s>** has started a Sweet Dreams battle!\nClick the button or use `/battle join` to enter the dreamscape...\n⏳ The dream will begin <t:%d:R>!", i.Member.User.ID, startUnix)

		joinBtn := discordgo.Button{
			CustomID: "battle:join",
			Label:    "Join Dream Battle",
			Style:    discordgo.SuccessButton,
			Emoji:    &discordgo.ComponentEmoji{Name: "💤"},
		}
		row := discordgo.ActionsRow{
			Components: []discordgo.MessageComponent{joinBtn},
		}

		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content:    msg,
				Components: []discordgo.MessageComponent{row},
			},
		})

		go runBattleCountdown(s, chID)

	case "join":
		b, ok := activeBattles[chID]
		if !ok || b == nil || b.Started {
			_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{Content: "😴 There is no open dream battle to join right now.", Flags: discordgo.MessageFlagsEphemeral},
			})
			return
		}

		uID := i.Member.User.ID
		if b.Players[uID] {
			_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{Content: "✨ You are already drifting in this dream!", Flags: discordgo.MessageFlagsEphemeral},
			})
			return
		}

		b.Players[uID] = true
		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: fmt.Sprintf("💤 **<@%s>** has joined the dream battle! (%d dreamers)", uID, len(b.Players)),
			},
		})
	}
}

func handleJoinBattleComponent(s *discordgo.Session, i *discordgo.InteractionCreate) {
	initBattle()
	battleMu.Lock()
	defer battleMu.Unlock()

	chID := i.ChannelID
	b, ok := activeBattles[chID]
	if !ok || b == nil || b.Started {
		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{Content: "😴 The dream has already begun or faded away.", Flags: discordgo.MessageFlagsEphemeral},
		})
		return
	}

	uID := i.Member.User.ID
	if b.Players[uID] {
		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{Content: "✨ You are already in this dream battle!", Flags: discordgo.MessageFlagsEphemeral},
		})
		return
	}

	b.Players[uID] = true
	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: fmt.Sprintf("💤 **<@%s>** entered the dream! (%d dreamers ready)", uID, len(b.Players)),
		},
	})
}

func runBattleCountdown(s *discordgo.Session, chID string) {
	time.Sleep(40 * time.Second)

	battleMu.Lock()
	b, ok := activeBattles[chID]
	if !ok || b == nil || b.Started {
		battleMu.Unlock()
		return
	}
	b.Started = true

	var players []string
	for p := range b.Players {
		players = append(players, p)
	}
	battleMu.Unlock()

	if len(players) < 2 {
		_, _ = s.ChannelMessageSend(chID, "Not enough dreamers joined. The dream fades away... 🌫️")
		battleMu.Lock()
		delete(activeBattles, chID)
		battleMu.Unlock()
		return
	}

	_, _ = s.ChannelMessageSend(chID, fmt.Sprintf("🌌 **The Sweet Dreams battle begins!** %d dreamers enter the dreamscape...", len(players)))

	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	for len(players) > 1 {
		time.Sleep(6 * time.Second)

		// Pick victim
		victimIdx := r.Intn(len(players))
		victim := players[victimIdx]
		players = append(players[:victimIdx], players[victimIdx+1:]...)

		phrase := eliminationPhrases[r.Intn(len(eliminationPhrases))]
		_, _ = s.ChannelMessageSend(chID, fmt.Sprintf("💤 <@%s> %s", victim, phrase))
	}

	time.Sleep(4 * time.Second)
	winner := players[0]
	embed := &discordgo.MessageEmbed{
		Title:       "🌟 Sweet Dreams Champion!",
		Description: fmt.Sprintf("Congratulations to <@%s> for having the sweetest dream and winning the battle! 🏆✨", winner),
		Color:       0x9B59B6,
	}
	_, _ = s.ChannelMessageSendEmbed(chID, embed)

	battleMu.Lock()
	delete(activeBattles, chID)
	battleMu.Unlock()
}
