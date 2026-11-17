package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
)

var validRepoNameRegex = regexp.MustCompile(`^[a-zA-Z0-9_\-\.]+$`)

func isValidRepoName(name string) bool {
	if len(name) < 1 || len(name) > 64 {
		return false
	}
	return validRepoNameRegex.MatchString(name)
}

func resolveFleetReposRoot() string {
	if env := strings.TrimSpace(os.Getenv("SUNFIRE_REPOS_ROOT")); env != "" {
		return env
	}
	if runtime.GOOS == "windows" {
		return `C:\Users\danie\Repos`
	}
	return "/home/daniel/Repos"
}

type repoHealthSummary struct {
	Name        string
	Path        string
	Branch      string
	DirtyCount  int
	AheadCount  int
	BehindCount int
	LastCommit  string
	Error       string
}

func fleetCommandSpec() commandSpec {
	return commandSpec{
		Command: &discordgo.ApplicationCommand{
			Name:        "fleet",
			Description: "Monitor Git repository health, uncommitted changes, and push status across Daniel's fleet.",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "status",
					Description: "Overview of all active Git repositories and their branch/cleanliness status.",
				},
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "repo",
					Description: "Inspect a single repository in detail (branches, remotes, dirty files, GPG commits).",
					Options: []*discordgo.ApplicationCommandOption{
						{
							Type:        discordgo.ApplicationCommandOptionString,
							Name:        "name",
							Description: "Repository directory name (e.g. Sunfire, TROSDAHD, Aegis-Logger)",
							Required:    true,
							MaxLength:   64,
						},
					},
				},
			},
		},
		Handler: handleFleetCommand,
	}
}

func handleFleetCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Flags: discordgo.MessageFlagsEphemeral,
		},
	})

	options := i.ApplicationCommandData().Options
	if len(options) == 0 {
		sendFleetEphemeral(s, i, "❌ Please specify a subcommand (`status` or `repo`).")
		return
	}

	sub := options[0]
	switch sub.Name {
	case "status":
		handleFleetStatus(s, i)
	case "repo":
		handleFleetRepo(s, i, sub)
	default:
		sendFleetEphemeral(s, i, "❌ Unknown fleet subcommand.")
	}
}

func handleFleetStatus(s *discordgo.Session, i *discordgo.InteractionCreate) {
	root := resolveFleetReposRoot()
	entries, err := os.ReadDir(root)
	if err != nil {
		sendFleetEphemeral(s, i, fmt.Sprintf("❌ Could not read repos root `%s`: %v", root, err))
		return
	}

	var repoNames []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		gitDir := filepath.Join(root, entry.Name(), ".git")
		if _, err := os.Stat(gitDir); err == nil {
			repoNames = append(repoNames, entry.Name())
		}
	}
	sort.Strings(repoNames)

	if len(repoNames) == 0 {
		sendFleetEphemeral(s, i, fmt.Sprintf("ℹ️ No Git repositories found in `%s`.", root))
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	summaries := make([]repoHealthSummary, 0, len(repoNames))
	cleanCount := 0
	dirtyCount := 0
	aheadCount := 0

	for _, name := range repoNames {
		repoPath := filepath.Join(root, name)
		h := inspectRepoHealth(ctx, name, repoPath)
		summaries = append(summaries, h)

		if h.DirtyCount > 0 {
			dirtyCount++
		}
		if h.AheadCount > 0 {
			aheadCount++
		}
		if h.DirtyCount == 0 && h.AheadCount == 0 && h.Error == "" {
			cleanCount++
		}
	}

	color := 0x2ECC71 // green
	if dirtyCount > 0 || aheadCount > 0 {
		color = 0xF1C40F // amber
	}

	fields := make([]*discordgo.MessageEmbedField, 0, len(summaries))
	for _, h := range summaries {
		icon := "🟢"
		statusNote := "clean"
		if h.Error != "" {
			icon = "⚠️"
			statusNote = h.Error
		} else if h.DirtyCount > 0 {
			icon = "🟡"
			statusNote = fmt.Sprintf("%d dirty file(s)", h.DirtyCount)
		} else if h.AheadCount > 0 {
			icon = "🚀"
			statusNote = fmt.Sprintf("%d commit(s) ahead", h.AheadCount)
		}

		desc := fmt.Sprintf("• **Branch:** `%s` • **Status:** %s", h.Branch, statusNote)
		if h.LastCommit != "" {
			desc += fmt.Sprintf("\n• **Latest:** `%s`", h.LastCommit)
		}

		fields = append(fields, &discordgo.MessageEmbedField{
			Name:   fmt.Sprintf("%s %s", icon, h.Name),
			Value:  desc,
			Inline: false,
		})
	}

	embed := &discordgo.MessageEmbed{
		Title:       "🌐 Git Fleet Health Monitor",
		Description: fmt.Sprintf("Root: `%s`\n**%d** Repositories • 🟢 **%d** Clean • 🟡 **%d** Dirty • 🚀 **%d** Ahead", root, len(summaries), cleanCount, dirtyCount, aheadCount),
		Color:       color,
		Fields:      fields,
		Timestamp:   time.Now().Format(time.RFC3339),
	}

	_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
		Embeds: []*discordgo.MessageEmbed{embed},
		Flags:  discordgo.MessageFlagsEphemeral,
	})
}

func inspectRepoHealth(ctx context.Context, name, repoPath string) repoHealthSummary {
	h := repoHealthSummary{Name: name, Path: repoPath}

	// 1. Branch
	cmdBranch := exec.CommandContext(ctx, "git", "-C", repoPath, "rev-parse", "--abbrev-ref", "HEAD")
	if out, err := cmdBranch.Output(); err == nil {
		h.Branch = strings.TrimSpace(string(out))
	} else {
		h.Branch = "unknown"
	}

	// 2. Dirty count
	cmdStatus := exec.CommandContext(ctx, "git", "-C", repoPath, "status", "--porcelain")
	if out, err := cmdStatus.Output(); err == nil {
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		count := 0
		for _, l := range lines {
			if strings.TrimSpace(l) != "" {
				count++
			}
		}
		h.DirtyCount = count
	}

	// 3. Ahead / behind
	cmdRev := exec.CommandContext(ctx, "git", "-C", repoPath, "rev-list", "--left-right", "--count", "HEAD...@{upstream}")
	if out, err := cmdRev.Output(); err == nil {
		parts := strings.Fields(strings.TrimSpace(string(out)))
		if len(parts) >= 2 {
			h.AheadCount, _ = strconv.Atoi(parts[0])
			h.BehindCount, _ = strconv.Atoi(parts[1])
		}
	}

	// 4. Last commit
	cmdLog := exec.CommandContext(ctx, "git", "-C", repoPath, "log", "-1", "--format=%h %s (%cr)")
	if out, err := cmdLog.Output(); err == nil {
		h.LastCommit = strings.TrimSpace(string(out))
	}

	return h
}

func handleFleetRepo(s *discordgo.Session, i *discordgo.InteractionCreate, sub *discordgo.ApplicationCommandInteractionDataOption) {
	repoName := ""
	for _, opt := range sub.Options {
		if opt.Name == "name" {
			repoName = strings.TrimSpace(opt.StringValue())
		}
	}

	if !isValidRepoName(repoName) {
		sendFleetEphemeral(s, i, "❌ Invalid repository name. Use only letters, numbers, hyphens, and underscores.")
		return
	}

	root := resolveFleetReposRoot()
	repoPath := filepath.Join(root, repoName)
	gitDir := filepath.Join(repoPath, ".git")
	if _, err := os.Stat(gitDir); err != nil {
		sendFleetEphemeral(s, i, fmt.Sprintf("❌ Repository `%s` not found in `%s`.", repoName, root))
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	branch := "unknown"
	if out, err := exec.CommandContext(ctx, "git", "-C", repoPath, "rev-parse", "--abbrev-ref", "HEAD").Output(); err == nil {
		branch = strings.TrimSpace(string(out))
	}

	remoteURL := "None"
	if out, err := exec.CommandContext(ctx, "git", "-C", repoPath, "remote", "get-url", "origin").Output(); err == nil {
		remoteURL = strings.TrimSpace(string(out))
	}

	dirtyLines := []string{}
	if out, err := exec.CommandContext(ctx, "git", "-C", repoPath, "status", "--porcelain").Output(); err == nil {
		for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			l = strings.TrimSpace(l)
			if l != "" {
				dirtyLines = append(dirtyLines, l)
			}
		}
	}

	commitsFormatted := "No recent commits."
	if out, err := exec.CommandContext(ctx, "git", "-C", repoPath, "log", "-3", "--format=%h|%G?|%s|%cr").Output(); err == nil {
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		var formatted []string
		for _, line := range lines {
			parts := strings.Split(line, "|")
			if len(parts) >= 4 {
				hash := parts[0]
				sig := parts[1]
				msg := parts[2]
				rel := parts[3]

				sigBadge := "⚪"
				switch sig {
				case "G":
					sigBadge = "🔐 GPG"
				case "B":
					sigBadge = "❌ Bad Sig"
				case "U":
					sigBadge = "⚠️ Untrusted"
				}

				formatted = append(formatted, fmt.Sprintf("• `%s` [%s] %s *(%s)*", hash, sigBadge, msg, rel))
			}
		}
		if len(formatted) > 0 {
			commitsFormatted = strings.Join(formatted, "\n")
		}
	}

	treeStatus := "🟢 Working tree clean"
	if len(dirtyLines) > 0 {
		preview := dirtyLines
		if len(preview) > 5 {
			preview = append(preview[:5], fmt.Sprintf("... and %d more", len(dirtyLines)-5))
		}
		treeStatus = fmt.Sprintf("🟡 **%d uncommitted file(s):**\n```\n%s\n```", len(dirtyLines), strings.Join(preview, "\n"))
	}

	embed := &discordgo.MessageEmbed{
		Title: fmt.Sprintf("📁 Repository: %s", repoName),
		Color: 0x3498DB,
		Fields: []*discordgo.MessageEmbedField{
			{Name: "🌿 Branch", Value: fmt.Sprintf("`%s`", branch), Inline: true},
			{Name: "🔗 Remote Origin", Value: fmt.Sprintf("`%s`", remoteURL), Inline: true},
			{Name: "🧹 Working Tree", Value: treeStatus, Inline: false},
			{Name: "📜 Recent Commits & GPG", Value: commitsFormatted, Inline: false},
		},
		Footer: &discordgo.MessageEmbedFooter{
			Text: repoPath,
		},
	}

	_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
		Embeds: []*discordgo.MessageEmbed{embed},
		Flags:  discordgo.MessageFlagsEphemeral,
	})
}

func sendFleetEphemeral(s *discordgo.Session, i *discordgo.InteractionCreate, content string) {
	_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
		Content: content,
		Flags:   discordgo.MessageFlagsEphemeral,
	})
}
