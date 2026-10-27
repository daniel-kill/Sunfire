package main

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
)

type RiddleEntry struct {
	ID            int      `json:"id"`
	Question      string   `json:"question"`
	Answers       []string `json:"answers"`
	DisplayAnswer string   `json:"displayAnswer"`
}

type RiddleSession struct {
	ChannelID   string
	GuildID     string
	StartedByID string
	RoundIndex  int
	TotalRounds int
	Current     RiddleEntry
	Scores      map[string]int // userID -> points
	Active      bool
	Timer       *time.Timer
}

var (
	riddlesMu     sync.Mutex
	allRiddles    []RiddleEntry
	activeRiddles map[string]*RiddleSession // channelID -> session
)

func initRiddles() {
	if len(allRiddles) > 0 {
		return
	}
	path := dataPath("riddles.json")
	data, err := os.ReadFile(path)
	if err == nil {
		_ = json.Unmarshal(data, &allRiddles)
	}
	activeRiddles = make(map[string]*RiddleSession)
}

func handleRiddleCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	initRiddles()
	riddlesMu.Lock()
	defer riddlesMu.Unlock()

	options := i.ApplicationCommandData().Options
	if len(options) == 0 {
		return
	}

	sub := options[0]
	chID := i.ChannelID

	switch sub.Name {
	case "start":
		if session, ok := activeRiddles[chID]; ok && session.Active {
			_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content: fmt.Sprintf("⚠️ A riddle game is already active here! Current round: %d/%d", session.RoundIndex+1, session.TotalRounds),
					Flags:   discordgo.MessageFlagsEphemeral,
				},
			})
			return
		}

		if len(allRiddles) == 0 {
			_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{Content: "❌ No riddles available in Data/riddles.json."},
			})
			return
		}

		session := &RiddleSession{
			ChannelID:   chID,
			GuildID:     i.GuildID,
			StartedByID: i.Member.User.ID,
			RoundIndex:  0,
			TotalRounds: 10,
			Scores:      make(map[string]int),
			Active:      true,
		}
		activeRiddles[chID] = session

		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "🧩 **10-Round Riddle Game Started!** Type your answers in this channel. First to guess correctly scores a point!",
			},
		})

		go startRiddleRound(s, session)

	case "stop":
		session, ok := activeRiddles[chID]
		if !ok || !session.Active {
			_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{Content: "There is no active riddle game in this channel.", Flags: discordgo.MessageFlagsEphemeral},
			})
			return
		}

		session.Active = false
		if session.Timer != nil {
			session.Timer.Stop()
		}
		delete(activeRiddles, chID)

		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{Content: "🛑 Riddle game has been stopped."},
		})

	case "status":
		session, ok := activeRiddles[chID]
		if !ok || !session.Active {
			_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{Content: "No active riddle game in this channel.", Flags: discordgo.MessageFlagsEphemeral},
			})
			return
		}

		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: fmt.Sprintf("🧩 **Riddle Game:** Round %d/%d\n**Current Question:** %s", session.RoundIndex+1, session.TotalRounds, session.Current.Question),
			},
		})
	}
}

func startRiddleRound(s *discordgo.Session, session *RiddleSession) {
	riddlesMu.Lock()
	if !session.Active || session.RoundIndex >= session.TotalRounds {
		riddlesMu.Unlock()
		endRiddleGame(s, session)
		return
	}

	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	session.Current = allRiddles[rng.Intn(len(allRiddles))]
	currQ := session.Current
	roundNum := session.RoundIndex + 1
	total := session.TotalRounds
	riddlesMu.Unlock()

	embed := &discordgo.MessageEmbed{
		Title:       fmt.Sprintf("🧩 Riddle Round %d of %d", roundNum, total),
		Description: fmt.Sprintf("**%s**\n\n*(You have 45 seconds to solve it!)*", currQ.Question),
		Color:       0xF1C40F,
	}

	_, _ = s.ChannelMessageSendEmbed(session.ChannelID, embed)

	time.Sleep(45 * time.Second)

	riddlesMu.Lock()
	if !session.Active || session.RoundIndex+1 != roundNum {
		riddlesMu.Unlock()
		return
	}

	// Time expired for this round without correct answer
	_, _ = s.ChannelMessageSend(session.ChannelID, fmt.Sprintf("⏰ Time's up! The answer was: **%s**", currQ.DisplayAnswer))
	session.RoundIndex++
	riddlesMu.Unlock()

	time.Sleep(3 * time.Second)
	startRiddleRound(s, session)
}

func checkRiddleAnswer(s *discordgo.Session, m *discordgo.MessageCreate) bool {
	riddlesMu.Lock()
	session, ok := activeRiddles[m.ChannelID]
	if !ok || !session.Active || session.Current.Question == "" {
		riddlesMu.Unlock()
		return false
	}

	ans := strings.ToLower(strings.TrimSpace(m.Content))
	matched := false
	for _, valid := range session.Current.Answers {
		if strings.EqualFold(ans, strings.TrimSpace(valid)) {
			matched = true
			break
		}
	}

	if matched {
		session.Scores[m.Author.ID]++
		displayAns := session.Current.DisplayAnswer
		session.RoundIndex++
		riddlesMu.Unlock()

		_, _ = s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("🎉 Correct, <@%s>! The answer was **%s** (+1 pt)!", m.Author.ID, displayAns))
		time.Sleep(3 * time.Second)
		go startRiddleRound(s, session)
		return true
	}

	riddlesMu.Unlock()
	return false
}

func endRiddleGame(s *discordgo.Session, session *RiddleSession) {
	riddlesMu.Lock()
	session.Active = false
	delete(activeRiddles, session.ChannelID)

	var scoreLines []string
	for uID, pts := range session.Scores {
		scoreLines = append(scoreLines, fmt.Sprintf("• <@%s>: **%d** pt(s)", uID, pts))
	}
	riddlesMu.Unlock()

	scoreText := "No one scored any points!"
	if len(scoreLines) > 0 {
		scoreText = strings.Join(scoreLines, "\n")
	}

	embed := &discordgo.MessageEmbed{
		Title:       "🏆 Riddle Game Finished!",
		Description: fmt.Sprintf("**Final Standings:**\n%s", scoreText),
		Color:       0x2ECC71,
	}
	_, _ = s.ChannelMessageSendEmbed(session.ChannelID, embed)
}
