package providers

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
				Name:        "cohere",
				Description: "Cohere AI Models",
				Options: []*discordgo.ApplicationCommandOption{
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "chat",
						Description: "Chat with Cohere (command-a-plus-05-2026)",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionString, Name: "prompt", Description: "Your message", Required: true},
							{Type: discordgo.ApplicationCommandOptionString, Name: "model", Description: "Model id (default command-a-plus-05-2026)"},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "transcribe",
						Description: "Transcribe audio with Cohere (requires language)",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionAttachment, Name: "audio", Description: "Audio file to transcribe", Required: true},
							{Type: discordgo.ApplicationCommandOptionString, Name: "language", Description: "ISO-639-1 language code (REQUIRED, no auto-detect)", Required: true},
							{Type: discordgo.ApplicationCommandOptionString, Name: "model", Description: "Model (default cohere-transcribe-03-2026)"},
						},
					},
				},
			},
			Handler: handleCohereRoot,
		},
		{
			Command: &discordgo.ApplicationCommand{
				Name:        "cloudflare",
				Description: "Cloudflare Workers AI",
				Options: []*discordgo.ApplicationCommandOption{
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "chat",
						Description: "Chat with Cloudflare Workers AI",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionString, Name: "prompt", Description: "Your message", Required: true},
							{Type: discordgo.ApplicationCommandOptionString, Name: "model", Description: "Model id (default @cf/meta/llama-3.3-70b-instruct-fp8-fast)"},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "image",
						Description: "Generate an image with Cloudflare Workers AI (flux-1-schnell)",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionString, Name: "prompt", Description: "Image prompt", Required: true},
							{Type: discordgo.ApplicationCommandOptionString, Name: "model", Description: "Model id (default @cf/black-forest-labs/flux-1-schnell)"},
						},
					},
				},
			},
			Handler: handleCloudflareRoot,
		},
		{
			Command: &discordgo.ApplicationCommand{
				Name:        "groq",
				Description: "Groq LPU Inference Engine",
				Options: []*discordgo.ApplicationCommandOption{
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "chat",
						Description: "Chat with Groq (openai/gpt-oss-20b)",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionString, Name: "prompt", Description: "Your message", Required: true},
							{Type: discordgo.ApplicationCommandOptionString, Name: "model", Description: "Model id (default openai/gpt-oss-20b)"},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "speak",
						Description: "Synthesize speech with Groq (orpheus)",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionString, Name: "text", Description: "Text to speak", Required: true},
							{Type: discordgo.ApplicationCommandOptionString, Name: "voice", Description: "autumn|diana|hannah|austin|daniel|troy (default autumn)"},
							{Type: discordgo.ApplicationCommandOptionString, Name: "model", Description: "Model id (default canopylabs/orpheus-v1-english)"},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "transcribe",
						Description: "Transcribe audio with Groq (whisper-large-v3-turbo)",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionAttachment, Name: "audio", Description: "Audio file to transcribe", Required: true},
							{Type: discordgo.ApplicationCommandOptionString, Name: "language", Description: "ISO-639-1 language code (optional)"},
							{Type: discordgo.ApplicationCommandOptionString, Name: "model", Description: "Model (default whisper-large-v3-turbo)"},
						},
					},
				},
			},
			Handler: handleGroqRoot,
		},
		{
			Command: &discordgo.ApplicationCommand{
				Name:        "mistral",
				Description: "Chat with Mistral AI",
				Options: []*discordgo.ApplicationCommandOption{
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "chat",
						Description: "Chat with Mistral (mistral-medium-latest)",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionString, Name: "prompt", Description: "Your message", Required: true},
							{Type: discordgo.ApplicationCommandOptionString, Name: "model", Description: "Model id (default mistral-medium-latest)"},
						},
					},
				},
			},
			Handler: handleMistralRoot,
		},
		{
			Command: &discordgo.ApplicationCommand{
				Name:        "nvidia",
				Description: "Chat with NVIDIA NIM",
				Options: []*discordgo.ApplicationCommandOption{
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "chat",
						Description: "Chat with NVIDIA (nemotron-3-super-120b)",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionString, Name: "prompt", Description: "Your message", Required: true},
							{Type: discordgo.ApplicationCommandOptionString, Name: "model", Description: "Model id (default nvidia/nemotron-3-super-120b-a12b)"},
						},
					},
				},
			},
			Handler: handleNvidiaRoot,
		},
		{
			Command: &discordgo.ApplicationCommand{
				Name:        "openrouter",
				Description: "OpenRouter AI Models",
				Options: []*discordgo.ApplicationCommandOption{
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "chat",
						Description: "Chat via OpenRouter (default nemotron-3 free)",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionString, Name: "prompt", Description: "Your message", Required: true},
							{Type: discordgo.ApplicationCommandOptionString, Name: "model", Description: "Model id (default nvidia/nemotron-3-super-120b-a12b:free)"},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "image",
						Description: "Generate an image via OpenRouter (seedream-4.5)",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionString, Name: "prompt", Description: "Image prompt", Required: true},
							{Type: discordgo.ApplicationCommandOptionString, Name: "model", Description: "Model id (default bytedance-seed/seedream-4.5)"},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "transcribe",
						Description: "Transcribe audio via OpenRouter (whisper-large-v3)",
						Options: []*discordgo.ApplicationCommandOption{
							{Type: discordgo.ApplicationCommandOptionAttachment, Name: "audio", Description: "Audio file to transcribe", Required: true},
							{Type: discordgo.ApplicationCommandOptionString, Name: "language", Description: "ISO-639-1 language code (optional)"},
							{Type: discordgo.ApplicationCommandOptionString, Name: "model", Description: "Model (default openai/whisper-large-v3)"},
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Name:        "credits",
						Description: "Show OpenRouter account balance and quota",
					},
				},
			},
			Handler: handleOpenRouterRoot,
		},
	}
}

// buildGet generates a scoped 'get' closure for the active subcommand.
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

// Subcommand Routers
func handleCohereRoot(s *discordgo.Session, i *discordgo.InteractionCreate) {
	data := i.ApplicationCommandData()
	if len(data.Options) == 0 { return }
	sub := data.Options[0]
	get := buildGet(sub)
	if sub.Name == "chat" {
		handleCohereChat(s, i, get)
	} else if sub.Name == "transcribe" {
		handleCohereTranscribe(s, i, get)
	}
}

func handleCloudflareRoot(s *discordgo.Session, i *discordgo.InteractionCreate) {
	data := i.ApplicationCommandData()
	if len(data.Options) == 0 { return }
	sub := data.Options[0]
	get := buildGet(sub)
	if sub.Name == "chat" {
		handleCloudflareChat(s, i, get)
	} else if sub.Name == "image" {
		handleCloudflareImage(s, i, get)
	}
}

func handleGroqRoot(s *discordgo.Session, i *discordgo.InteractionCreate) {
	data := i.ApplicationCommandData()
	if len(data.Options) == 0 { return }
	sub := data.Options[0]
	get := buildGet(sub)
	if sub.Name == "chat" {
		handleGroqChat(s, i, get)
	} else if sub.Name == "speak" {
		handleGroqSpeak(s, i, get)
	} else if sub.Name == "transcribe" {
		handleGroqTranscribe(s, i, get)
	}
}

func handleMistralRoot(s *discordgo.Session, i *discordgo.InteractionCreate) {
	data := i.ApplicationCommandData()
	if len(data.Options) == 0 { return }
	sub := data.Options[0]
	if sub.Name == "chat" {
		handleMistralChat(s, i, buildGet(sub))
	}
}

func handleNvidiaRoot(s *discordgo.Session, i *discordgo.InteractionCreate) {
	data := i.ApplicationCommandData()
	if len(data.Options) == 0 { return }
	sub := data.Options[0]
	if sub.Name == "chat" {
		handleNvidiaChat(s, i, buildGet(sub))
	}
}

func handleOpenRouterRoot(s *discordgo.Session, i *discordgo.InteractionCreate) {
	data := i.ApplicationCommandData()
	if len(data.Options) == 0 { return }
	sub := data.Options[0]
	get := buildGet(sub)
	switch sub.Name {
	case "chat":
		handleOpenRouterChat(s, i, get)
	case "image":
		handleOpenRouterImage(s, i, get)
	case "transcribe":
		handleOpenRouterTranscribe(s, i, get)
	case "credits":
		handleOpenRouterCredits(s, i, get) // credits doesn't use get
	}
}