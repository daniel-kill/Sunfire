package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
)

type StoryChoice struct {
	Text     string                 `json:"text"`
	NextNode string                 `json:"nextNode"`
	Effects  map[string]interface{} `json:"effects"`
}

type StoryNode struct {
	ID       string        `json:"id"`
	Title    string        `json:"title"`
	Content  string        `json:"content"`
	Image    string        `json:"image"`
	Choices  []StoryChoice `json:"choices"`
	IsEnding bool          `json:"isEnding"`
}

type FallenStarGame struct {
	UserID      string                 `json:"userId"`
	GuildID     string                 `json:"guildId"`
	ChannelID   string                 `json:"channelId"`
	CurrentNode string                 `json:"currentNode"`
	Status      string                 `json:"status"` // active, won, lost
	StartedAt   int64                  `json:"startedAt"`
	EndedAt     int64                  `json:"endedAt,omitempty"`
	Stats       map[string]int         `json:"stats"`
	Inventory   []string               `json:"inventory"`
	Metadata    map[string]interface{} `json:"metadata"`
}

var (
	fsMu          sync.Mutex
	fallenStory   map[string]StoryNode
	activeFSGames map[string]*FallenStarGame // userID -> game
	fsGamesPath   string
)

func initFallenStar() {
	if len(fallenStory) > 0 {
		return
	}
	storyPath := dataPath("fallenStarStory.json")
	sData, err := os.ReadFile(storyPath)
	if err == nil {
		_ = json.Unmarshal(sData, &fallenStory)
	}

	fsGamesPath = dataPath("fallenStarGames.json")
	activeFSGames = make(map[string]*FallenStarGame)
	gData, err := os.ReadFile(fsGamesPath)
	if err == nil {
		_ = json.Unmarshal(gData, &activeFSGames)
	}
}

func saveFallenStarGames() {
	if fsGamesPath == "" {
		return
	}
	data, err := json.MarshalIndent(activeFSGames, "", "  ")
	if err == nil {
		_ = os.WriteFile(fsGamesPath, data, 0644)
	}
}

func handleFallenStarCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	initFallenStar()
	fsMu.Lock()
	defer fsMu.Unlock()

	options := i.ApplicationCommandData().Options
	if len(options) == 0 {
		return
	}

	sub := options[0]
	uID := i.Member.User.ID

	switch sub.Name {
	case "start":
		if g, ok := activeFSGames[uID]; ok && g.Status == "active" {
			_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content: fmt.Sprintf("⚠️ You already have an active adventure in node `%s`! Use `/fallen-star status` to continue.", g.CurrentNode),
					Flags:   discordgo.MessageFlagsEphemeral,
				},
			})
			return
		}

		game := &FallenStarGame{
			UserID:      uID,
			GuildID:     i.GuildID,
			ChannelID:   i.ChannelID,
			CurrentNode: "intro",
			Status:      "active",
			StartedAt:   time.Now().UnixMilli(),
			Stats: map[string]int{
				"sanity":        100,
				"corruption":    0,
				"magicAffinity": 0,
			},
			Inventory: []string{},
			Metadata: map[string]interface{}{
				"characterName": i.Member.User.Username,
			},
		}
		activeFSGames[uID] = game
		saveFallenStarGames()

		node, exists := fallenStory["intro"]
		if !exists {
			_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{Content: "❌ Story introductory node missing from Data/fallenStarStory.json."},
			})
			return
		}

		sendStoryNodeInteraction(s, i, node, game)

	case "status":
		game, ok := activeFSGames[uID]
		if !ok || game.Status != "active" {
			_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content: "You don't have an active adventure right now. Use `/fallen-star start` to begin!",
					Flags:   discordgo.MessageFlagsEphemeral,
				},
			})
			return
		}

		node, ok := fallenStory[game.CurrentNode]
		if !ok {
			node = fallenStory["intro"]
		}
		sendStoryNodeInteraction(s, i, node, game)
	}
}

func sendStoryNodeInteraction(s *discordgo.Session, i *discordgo.InteractionCreate, node StoryNode, game *FallenStarGame) {
	embed := &discordgo.MessageEmbed{
		Title:       node.Title,
		Description: node.Content,
		Color:       0x4A0080, // Dark purple
		Footer: &discordgo.MessageEmbedFooter{
			Text: fmt.Sprintf("Sanity: %d/100 | Corruption: %d/100 | Magic: %d/100", game.Stats["sanity"], game.Stats["corruption"], game.Stats["magicAffinity"]),
		},
	}

	var rows []discordgo.MessageComponent
	if len(node.Choices) > 0 {
		var buttons []discordgo.MessageComponent
		for cIdx, ch := range node.Choices {
			label := ch.Text
			if len(label) > 80 {
				label = label[:77] + "..."
			}

			btn := discordgo.Button{
				CustomID: fmt.Sprintf("fs:%s:%s", game.UserID, ch.NextNode),
				Label:    label,
				Style:    discordgo.SecondaryButton,
			}
			buttons = append(buttons, btn)
			if len(buttons) >= 5 || cIdx == len(node.Choices)-1 {
				rows = append(rows, discordgo.ActionsRow{Components: buttons})
				buttons = nil
			}
		}
	}

	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Embeds:     []*discordgo.MessageEmbed{embed},
			Components: rows,
		},
	})
}

func handleFallenStarComponent(s *discordgo.Session, i *discordgo.InteractionCreate) {
	initFallenStar()
	fsMu.Lock()
	defer fsMu.Unlock()

	parts := strings.Split(i.MessageComponentData().CustomID, ":")
	if len(parts) != 3 {
		return
	}

	ownerID := parts[1]
	targetNodeID := parts[2]

	if i.Member.User.ID != ownerID {
		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "❌ This is not your adventure!",
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
		return
	}

	game, ok := activeFSGames[ownerID]
	if !ok || game.Status != "active" {
		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "No active adventure found. Use `/fallen-star start`!",
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
		return
	}

	node, ok := fallenStory[targetNodeID]
	if !ok {
		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{Content: "❌ Path leads into the unknown void (node missing)."},
		})
		return
	}

	game.CurrentNode = targetNodeID
	if node.IsEnding {
		game.Status = "ended"
		game.EndedAt = time.Now().UnixMilli()
	}
	saveFallenStarGames()

	embed := &discordgo.MessageEmbed{
		Title:       node.Title,
		Description: node.Content,
		Color:       0x4A0080,
		Footer: &discordgo.MessageEmbedFooter{
			Text: fmt.Sprintf("Sanity: %d/100 | Corruption: %d/100 | Magic: %d/100", game.Stats["sanity"], game.Stats["corruption"], game.Stats["magicAffinity"]),
		},
	}

	var rows []discordgo.MessageComponent
	if len(node.Choices) > 0 {
		var buttons []discordgo.MessageComponent
		for cIdx, ch := range node.Choices {
			label := ch.Text
			if len(label) > 80 {
				label = label[:77] + "..."
			}

			btn := discordgo.Button{
				CustomID: fmt.Sprintf("fs:%s:%s", game.UserID, ch.NextNode),
				Label:    label,
				Style:    discordgo.SecondaryButton,
			}
			buttons = append(buttons, btn)
			if len(buttons) >= 5 || cIdx == len(node.Choices)-1 {
				rows = append(rows, discordgo.ActionsRow{Components: buttons})
				buttons = nil
			}
		}
	}

	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseUpdateMessage,
		Data: &discordgo.InteractionResponseData{
			Embeds:     []*discordgo.MessageEmbed{embed},
			Components: rows,
		},
	})
}
