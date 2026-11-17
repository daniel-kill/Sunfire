package main

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
)

var validContainerNameRegex = regexp.MustCompile(`^[a-zA-Z0-9_\-\.]+$`)

func isValidContainerName(name string) bool {
	if len(name) < 1 || len(name) > 64 {
		return false
	}
	return validContainerNameRegex.MatchString(name)
}

type containerSummary struct {
	ID     string
	Name   string
	Status string
	Image  string
	Ports  string
}

func dockerCommandSpec() commandSpec {
	return commandSpec{
		Command: &discordgo.ApplicationCommand{
			Name:        "docker",
			Description: "Inspect host Docker containers, view logs, and manage fleet lifecycle.",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "status",
					Description: "Show status of all host Docker containers.",
				},
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "restart",
					Description: "Restart a Docker container.",
					Options: []*discordgo.ApplicationCommandOption{
						{
							Type:        discordgo.ApplicationCommandOptionString,
							Name:        "container",
							Description: "Container name or ID",
							Required:    true,
							MaxLength:   64,
						},
					},
				},
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "start",
					Description: "Start a stopped Docker container.",
					Options: []*discordgo.ApplicationCommandOption{
						{
							Type:        discordgo.ApplicationCommandOptionString,
							Name:        "container",
							Description: "Container name or ID",
							Required:    true,
							MaxLength:   64,
						},
					},
				},
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "stop",
					Description: "Stop a running Docker container.",
					Options: []*discordgo.ApplicationCommandOption{
						{
							Type:        discordgo.ApplicationCommandOptionString,
							Name:        "container",
							Description: "Container name or ID",
							Required:    true,
							MaxLength:   64,
						},
					},
				},
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "logs",
					Description: "Tail recent logs from a container.",
					Options: []*discordgo.ApplicationCommandOption{
						{
							Type:        discordgo.ApplicationCommandOptionString,
							Name:        "container",
							Description: "Container name or ID",
							Required:    true,
							MaxLength:   64,
						},
						{
							Type:        discordgo.ApplicationCommandOptionInteger,
							Name:        "lines",
							Description: "Number of lines to tail (1-100, default: 30)",
							Required:    false,
						},
					},
				},
			},
		},
		Handler: handleDockerCommand,
	}
}

func handleDockerCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Flags: discordgo.MessageFlagsEphemeral,
		},
	})

	options := i.ApplicationCommandData().Options
	if len(options) == 0 {
		sendDockerEphemeral(s, i, "❌ Please specify a subcommand (`status`, `restart`, `start`, `stop`, `logs`).")
		return
	}

	sub := options[0]
	switch sub.Name {
	case "status":
		handleDockerStatus(s, i)
	case "restart":
		handleDockerLifecycle(s, i, sub, "restart")
	case "start":
		handleDockerLifecycle(s, i, sub, "start")
	case "stop":
		handleDockerLifecycle(s, i, sub, "stop")
	case "logs":
		handleDockerLogs(s, i, sub)
	default:
		sendDockerEphemeral(s, i, "❌ Unknown docker subcommand.")
	}
}

func handleDockerStatus(s *discordgo.Session, i *discordgo.InteractionCreate) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "docker", "ps", "-a", "--format", "{{.ID}}\t{{.Names}}\t{{.Status}}\t{{.Image}}\t{{.Ports}}")
	out, err := cmd.CombinedOutput()
	if err != nil {
		sendDockerEphemeral(s, i, fmt.Sprintf("❌ Failed to query Docker: %v\n```\n%s\n```", err, strings.TrimSpace(string(out))))
		return
	}

	containers := parseDockerPsOutput(string(out))
	if len(containers) == 0 {
		sendDockerEphemeral(s, i, "ℹ️ No Docker containers found on the host system.")
		return
	}

	runningCount := 0
	fields := make([]*discordgo.MessageEmbedField, 0, len(containers))
	for _, c := range containers {
		icon := "⚪"
		statusLower := strings.ToLower(c.Status)
		if strings.HasPrefix(statusLower, "up") {
			icon = "🟢"
			runningCount++
		} else if strings.HasPrefix(statusLower, "exited") {
			icon = "🔴"
		} else if strings.HasPrefix(statusLower, "restart") {
			icon = "🟡"
		}

		val := fmt.Sprintf("• **Status:** `%s`\n• **Image:** `%s`", c.Status, c.Image)
		if c.Ports != "" {
			val += fmt.Sprintf("\n• **Ports:** `%s`", c.Ports)
		}

		fields = append(fields, &discordgo.MessageEmbedField{
			Name:   fmt.Sprintf("%s %s (`%s`)", icon, c.Name, c.ID[:minInt(len(c.ID), 12)]),
			Value:  val,
			Inline: false,
		})
	}

	embed := &discordgo.MessageEmbed{
		Title:       "🐳 Docker Fleet Status",
		Description: fmt.Sprintf("Fleet Summary: **%d** running / **%d** total containers", runningCount, len(containers)),
		Color:       0x0080FF,
		Fields:      fields,
		Timestamp:   time.Now().Format(time.RFC3339),
	}

	_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
		Embeds: []*discordgo.MessageEmbed{embed},
		Flags:  discordgo.MessageFlagsEphemeral,
	})
}

func parseDockerPsOutput(raw string) []containerSummary {
	var list []containerSummary
	lines := strings.Split(strings.TrimSpace(raw), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) >= 4 {
			c := containerSummary{
				ID:     strings.TrimSpace(parts[0]),
				Name:   strings.TrimSpace(parts[1]),
				Status: strings.TrimSpace(parts[2]),
				Image:  strings.TrimSpace(parts[3]),
			}
			if len(parts) >= 5 {
				c.Ports = strings.TrimSpace(parts[4])
			}
			list = append(list, c)
		}
	}
	return list
}

func handleDockerLifecycle(s *discordgo.Session, i *discordgo.InteractionCreate, sub *discordgo.ApplicationCommandInteractionDataOption, action string) {
	container := ""
	for _, opt := range sub.Options {
		if opt.Name == "container" {
			container = strings.TrimSpace(opt.StringValue())
		}
	}

	if !isValidContainerName(container) {
		sendDockerEphemeral(s, i, "❌ Invalid container name or ID. Use only alphanumeric characters, dots, underscores, and hyphens.")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "docker", action, container)
	out, err := cmd.CombinedOutput()
	if err != nil {
		sendDockerEphemeral(s, i, fmt.Sprintf("❌ `docker %s %s` failed: %v\n```\n%s\n```", action, container, err, strings.TrimSpace(string(out))))
		return
	}

	sendDockerEphemeral(s, i, fmt.Sprintf("✅ Container `%s` successfully executed `%s`:\n```\n%s\n```", container, action, strings.TrimSpace(string(out))))
}

func handleDockerLogs(s *discordgo.Session, i *discordgo.InteractionCreate, sub *discordgo.ApplicationCommandInteractionDataOption) {
	container := ""
	lines := int64(30)
	for _, opt := range sub.Options {
		switch opt.Name {
		case "container":
			container = strings.TrimSpace(opt.StringValue())
		case "lines":
			lines = opt.IntValue()
		}
	}

	if !isValidContainerName(container) {
		sendDockerEphemeral(s, i, "❌ Invalid container name or ID. Use only alphanumeric characters, dots, underscores, and hyphens.")
		return
	}

	if lines < 1 {
		lines = 30
	} else if lines > 100 {
		lines = 100
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "docker", "logs", "--tail", strconv.FormatInt(lines, 10), container)
	out, err := cmd.CombinedOutput()
	if err != nil {
		sendDockerEphemeral(s, i, fmt.Sprintf("❌ Failed to read logs for container `%s`: %v\n```\n%s\n```", container, err, strings.TrimSpace(string(out))))
		return
	}

	logText := strings.TrimSpace(string(out))
	if logText == "" {
		sendDockerEphemeral(s, i, fmt.Sprintf("ℹ️ Container `%s` produced no logs.", container))
		return
	}

	// Discord message limit is 2000 characters; truncate logs to 1800 characters if needed
	if len(logText) > 1800 {
		logText = "... (truncated)\n" + logText[len(logText)-1780:]
	}

	sendDockerEphemeral(s, i, fmt.Sprintf("📋 **Logs for `%s` (tail %d):**\n```\n%s\n```", container, lines, logText))
}

func sendDockerEphemeral(s *discordgo.Session, i *discordgo.InteractionCreate, content string) {
	_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
		Content: content,
		Flags:   discordgo.MessageFlagsEphemeral,
	})
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
