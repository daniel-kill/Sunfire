package a2e

import (
	"fmt"
	"github.com/bwmarrin/discordgo"
)

type CommandSpec struct {
	Command *discordgo.ApplicationCommand
	Handler func(s *discordgo.Session, i *discordgo.InteractionCreate)
}

func CommandSpecs() []CommandSpec {
	return []CommandSpec{
		{
			Command: &discordgo.ApplicationCommand{
				Name:        "a2e",
				Description: "A2E AI commands (image, video, tts, clone, tasks, etc.)",
				Options: []*discordgo.ApplicationCommandOption{
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "image",
						Description: "Generate an image from text (default model: seedream)",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionString, Name: "prompt", Description: "Image prompt", Required: true},
							{Type: discordgo.ApplicationCommandOptionString, Name: "model", Description: "Image model (default from config)"},
							{Type: discordgo.ApplicationCommandOptionString, Name: "size", Description: "Size e.g. 1024x1024"},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "video",
						Description: "Generate a video using Wan 3.0 (runs asynchronously)",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionString, Name: "prompt", Description: "Video prompt", Required: true},
							{Type: discordgo.ApplicationCommandOptionString, Name: "model", Description: "Video model (default from config)"},
							{Type: discordgo.ApplicationCommandOptionString, Name: "aspect_ratio", Description: "16:9 | 9:16 | 1:1"},
							{Type: discordgo.ApplicationCommandOptionString, Name: "duration", Description: "Duration e.g. 5s"},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "tts",
						Description: "Generate speech using built-in A2E voices or your cloned voices",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionString, Name: "text", Description: "Text to speak", Required: true},
							{Type: discordgo.ApplicationCommandOptionString, Name: "voice", Description: "Voice id or cloned-voice name"},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "voice-clone",
						Description: "Clone a custom voice from an attached MP3 or audio URLs",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionString, Name: "name", Description: "Name for the cloned voice", Required: true},
							{Type: discordgo.ApplicationCommandOptionAttachment, Name: "mp3", Description: "Audio sample to clone from (MP3/WAV/FLAC/M4A)"},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "voice-assign",
						Description: "Assign a friendly name to a raw voice ID (saves to local JSON)",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionString, Name: "name", Description: "The friendly name to assign", Required: true},
							{Type: discordgo.ApplicationCommandOptionString, Name: "id", Description: "The raw A2E voice ID", Required: true},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "voices",
						Description: "List your custom cloned voices and A2E's built-in voice presets",
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "tasks",
						Description: "List recent asynchronous image and video generation tasks",
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "credits",
						Description: "Show remaining A2E account credits",
					},
				},
			},
			Handler: handleA2ERoot,
		},
	}
}

func buildGet(subCommand *discordgo.ApplicationCommandInteractionDataOption) func(string) string {
	return func(key string) string {
		for _, o := range subCommand.Options {
			if o.Name == key {
				if s, ok := o.Value.(string); ok {
					return s
				}
				return fmt.Sprintf("%v", o.Value)
			}
		}
		return ""
	}
}

func handleA2ERoot(s *discordgo.Session, i *discordgo.InteractionCreate) {
	data := i.ApplicationCommandData()
	if len(data.Options) == 0 {
		return
	}
	subCommand := data.Options[0]
	get := buildGet(subCommand)

	switch subCommand.Name {
	case "image":
		handleImage(s, i, get)
	case "video":
		handleVideo(s, i, get)
	case "tts":
		handleTTS(s, i, get)
	case "voice-clone":
		handleVoiceClone(s, i, get)
	case "voice-assign":
		handleVoiceAssign(s, i, get)
	case "voices":
		handleVoices(s, i, get)
	case "tasks":
		handleTasks(s, i, get)
	case "credits":
		handleCredits(s, i, get)
	}
}
