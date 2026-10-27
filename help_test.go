package main

import (
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"
)

func TestHelpCategoriesCoverRegisteredCommands(t *testing.T) {
	buildCommandSpecs()

	choiceValues := make(map[string]bool)
	choiceLabels := make(map[string]bool)
	for _, choice := range helpCategoryChoices() {
		value, ok := choice.Value.(string)
		if !ok {
			t.Fatalf("help category value has type %T, want string", choice.Value)
		}
		choiceValues[value] = true
		choiceLabels[choice.Name] = true
	}
	if len(choiceValues) != len(helpCategories) {
		t.Fatalf("got %d unique category choices, want %d", len(choiceValues), len(helpCategories))
	}
	if !choiceLabels[helpCategoryLabel("core")] {
		t.Fatal("core category is missing from the command choices")
	}

	for _, spec := range commandSpecs {
		category := helpCommandCategory(spec.Command.Name)
		if !choiceValues[category] {
			t.Errorf("command /%s maps to unselectable category %q", spec.Command.Name, category)
		}
	}
}

func TestHelpCommandEntryIncludesOptionTypesChoicesAndNestedPaths(t *testing.T) {
	minLength := 1
	entry := helpCommandEntry(commandSpec{
		Command: &discordgo.ApplicationCommand{
			Name:        "sample",
			Description: "A sample command.",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionSubCommandGroup,
					Name:        "group",
					Description: "A grouped action.",
					Options: []*discordgo.ApplicationCommandOption{
						{
							Type:        discordgo.ApplicationCommandOptionSubCommand,
							Name:        "run",
							Description: "Run the action.",
							Options: []*discordgo.ApplicationCommandOption{
								{
									Type:        discordgo.ApplicationCommandOptionString,
									Name:        "mode",
									Description: "Execution mode.",
									Required:    true,
									MinLength:   &minLength,
									MaxLength:   20,
									Choices: []*discordgo.ApplicationCommandOptionChoice{
										{Name: "Fast", Value: "fast"},
										{Name: "Safe", Value: "safe"},
									},
								},
							},
						},
					},
				},
			},
		},
	})

	for _, want := range []string{
		"/sample group",
		"/sample group run",
		"`/sample group run mode` (string, required)",
		"Choices: Fast (fast), Safe (safe)",
		"Length: 1 to 20 characters",
	} {
		if !strings.Contains(entry, want) {
			t.Errorf("help entry %q does not contain %q", entry, want)
		}
	}
}

func TestHelpSearchAndCategoryFiltering(t *testing.T) {
	buildCommandSpecs()

	entries := helpEntries("", "openrouter")
	if len(entries) != 1 || !strings.Contains(entries[0], "/openrouter") {
		t.Fatalf("help search returned %d entries, want only /openrouter", len(entries))
	}
	entries = helpEntries("moderation", "setup")
	if len(entries) != 1 || !strings.Contains(entries[0], "/setup") {
		t.Fatalf("moderation search returned %d entries, want only /setup", len(entries))
	}
	if validHelpSearch("bad query") || validHelpSearch("name/with/slash") || validHelpSearch(strings.Repeat("x", 33)) {
		t.Fatal("expected malformed or overlong search terms to be rejected")
	}
}

func TestHelpContentPagesStayWithinDiscordLimit(t *testing.T) {
	originalSpecs := commandSpecs
	defer func() { commandSpecs = originalSpecs }()

	commandSpecs = make([]commandSpec, 20)
	for n := range commandSpecs {
		commandSpecs[n] = commandSpec{
			Command: &discordgo.ApplicationCommand{
				Name:        "sample-" + strings.Repeat("x", n%10+1),
				Description: strings.Repeat("description ", 35),
				Options: []*discordgo.ApplicationCommandOption{
					{
						Type:        discordgo.ApplicationCommandOptionString,
						Name:        "parameter",
						Description: strings.Repeat("parameter details ", 25),
					},
				},
			},
		}
	}

	pages := helpContentPages("core", "")
	if len(pages) < 2 {
		t.Fatalf("got %d help pages, want pagination for large category", len(pages))
	}
	for page, content := range pages {
		if length := len([]rune(content)); length > 2000 {
			t.Errorf("help page %d has %d characters, exceeding Discord's 2000-character limit", page+1, length)
		}
	}

	customID := helpPageCustomID("core", 2, "sample")
	parts := strings.Split(customID, ":")
	if len(parts) != 5 || parts[2] != "core" || parts[3] != "2" || parts[4] != "sample" {
		t.Fatalf("help page custom ID %q cannot restore its category, page, and search", customID)
	}
	if len(customID) > 100 {
		t.Fatalf("help page custom ID has %d characters, exceeding Discord's limit", len(customID))
	}
}

func TestHelpOverviewListsAvailableCategories(t *testing.T) {
	buildCommandSpecs()

	pages := helpContentPages("", "")
	if len(pages) != 1 {
		t.Fatalf("overview returned %d pages, want 1", len(pages))
	}
	for _, want := range []string{"Core (System, Moderation, Media)", "LLM Providers", "/help search:"} {
		if !strings.Contains(pages[0], want) {
			t.Errorf("help overview %q does not contain %q", pages[0], want)
		}
	}
}
