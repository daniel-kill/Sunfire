// commands.go — Venice subpackage command definitions for the ElyLightspeed bot.
// All commands are registered with the "venice-" prefix (e.g. /venice-chat).
package venice

import (
	"fmt"
	"github.com/bwmarrin/discordgo"
)

// CommandSpec pairs a command definition with its handler, mirroring the
// main package's commandSpec so the main package can register these directly.
type CommandSpec struct {
	Command *discordgo.ApplicationCommand
	Handler func(s *discordgo.Session, i *discordgo.InteractionCreate)
}

// CommandSpecs returns the Venice slash commands for the main package to register.
func CommandSpecs() []CommandSpec {
	minMusicDuration := float64(minMusicDurationSeconds)
	return []CommandSpec{
		{
			Command: &discordgo.ApplicationCommand{
				Name:        "venice",
				Description: "Venice AI commands (chat, image, tts, music, search, etc.)",
				Options: []*discordgo.ApplicationCommandOption{
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "chat",
						Description: "Chat with a Venice text model",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionString, Name: "prompt", Description: "Your message", Required: true},
							{Type: discordgo.ApplicationCommandOptionString, Name: "model", Description: "Model id (default from config)"},
							{Type: discordgo.ApplicationCommandOptionBoolean, Name: "web", Description: "Enable web search"},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "image",
						Description: "Generate an image with Venice",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionString, Name: "prompt", Description: "Image prompt", Required: true},
							{Type: discordgo.ApplicationCommandOptionString, Name: "model", Description: "Image model (default flux-2-pro)"},
							{Type: discordgo.ApplicationCommandOptionString, Name: "size", Description: "Size e.g. 1024x1024"},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "tts",
						Description: "Generate speech audio (text-to-speech)",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionString, Name: "text", Description: "Text to speak", Required: true},
							{Type: discordgo.ApplicationCommandOptionString, Name: "voice", Description: "Voice id or cloned-voice name (default from config)"},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "voice-clone",
						Description: "Clone a voice from an attached audio sample (Chatterbox HD)",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionString, Name: "name", Description: "Name for the cloned voice", Required: true},
							{Type: discordgo.ApplicationCommandOptionAttachment, Name: "mp3", Description: "Audio sample to clone from (MP3/WAV/FLAC/M4A)"},
							{Type: discordgo.ApplicationCommandOptionString, Name: "model", Description: "tts-chatterbox-hd (default) | tts-minimax-speech-02-hd"},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "voices",
						Description: "List your cloned Venice voices",
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "balance",
						Description: "Show Venice account balance",
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "models",
						Description: "List Venice models (by modality)",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionString, Name: "type", Description: "text|image|video|music|embedding|asr", Required: true},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "music",
						Description: "Generate a song with Venice (120-360 sec, cost quoted first)",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionString, Name: "prompt", Description: "Style/genre prompt", Required: true},
							{Type: discordgo.ApplicationCommandOptionString, Name: "lyrics", Description: "Lyrics text"},
							{
								Type:        discordgo.ApplicationCommandOptionInteger,
								Name:        "duration",
								Description: "Song length in seconds (120-360, default 240)",
								MinValue:    &minMusicDuration,
								MaxValue:    maxMusicDurationSeconds,
							},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "stt",
						Description: "Transcribe an attached audio file (speech-to-text)",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionAttachment, Name: "audio", Description: "Audio file to transcribe", Required: true},
							{Type: discordgo.ApplicationCommandOptionString, Name: "model", Description: "ASR model (default openai/whisper-large-v3)"},
							{Type: discordgo.ApplicationCommandOptionString, Name: "language", Description: "Optional language code (e.g. en)"},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "search",
						Description: "Web search via Venice augment",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionString, Name: "query", Description: "Search query", Required: true},
							{Type: discordgo.ApplicationCommandOptionString, Name: "max_results", Description: "Max results (default 5)"},
							{Type: discordgo.ApplicationCommandOptionString, Name: "provider", Description: "brave (default) | google"},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "scrape",
						Description: "Scrape a URL to markdown via Venice augment",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionString, Name: "url", Description: "URL to scrape", Required: true},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "video",
						Description: "Generate a video via Venice (async)",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionString, Name: "prompt", Description: "Video prompt", Required: true},
							{Type: discordgo.ApplicationCommandOptionString, Name: "model", Description: "Video model (default seedance-2-5-text-to-video-basic)"},
							{Type: discordgo.ApplicationCommandOptionString, Name: "aspect_ratio", Description: "21:9 | 16:9 | 4:3 | 1:1 | 3:4 | 9:16 (default 16:9)"},
							{Type: discordgo.ApplicationCommandOptionString, Name: "resolution", Description: "1080p | 720p | 480p (default 1080p)"},
							{Type: discordgo.ApplicationCommandOptionString, Name: "duration", Description: "Duration e.g. 5s"},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "embed",
						Description: "Generate an embedding for text",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionString, Name: "input", Description: "Text to embed", Required: true},
							{Type: discordgo.ApplicationCommandOptionString, Name: "model", Description: "Embedding model (default text-embedding-bge-m3)"},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "characters",
						Description: "List Venice characters",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionString, Name: "search", Description: "Optional search term"},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "upscale",
						Description: "Upscale an image via Venice",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionAttachment, Name: "image", Description: "Image to upscale", Required: true},
							{Type: discordgo.ApplicationCommandOptionString, Name: "model", Description: "Upscale model"},
							{Type: discordgo.ApplicationCommandOptionString, Name: "scale", Description: "Scale factor 2-8"},
						},
					},
				},
			},
			Handler: handleVeniceCommand, // We will map the subcommands here
		},
	}
}

// handleVeniceCommand routes the root /venice command to the correct subcommand handler.
func handleVeniceCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	data := i.ApplicationCommandData()
	if len(data.Options) == 0 {
		return
	}
	subCommand := data.Options[0]

	// Create a dynamic 'get' function that reads options from the subcommand, not the root command
	get := func(key string) string {
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

	switch subCommand.Name {
	case "chat":
		handleChat(s, i, get)
	case "image":
		handleImage(s, i, get)
	case "tts":
		handleTTS(s, i, get)
	case "voice-clone":
		handleVoiceClone(s, i, get)
	case "voices":
		handleVoices(s, i, get)
	case "balance":
		handleBalance(s, i) // balance doesn't use 'get'
	case "models":
		handleModels(s, i, get)
	case "music":
		handleMusic(s, i, get)
	case "stt":
		handleSTT(s, i, get)
	case "search":
		handleSearch(s, i, get)
	case "scrape":
		handleScrape(s, i, get)
	case "video":
		handleVideo(s, i, get)
	case "embed":
		handleEmbed(s, i, get)
	case "characters":
		handleCharacters(s, i, get)
	case "upscale":
		handleUpscale(s, i, get)
	}
}
