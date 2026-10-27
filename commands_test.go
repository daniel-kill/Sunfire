package main

import (
	"testing"

	"github.com/bwmarrin/discordgo"
)

func TestCommandConsolidations(t *testing.T) {
	buildCommandSpecs()

	commands := make(map[string]*discordgo.ApplicationCommand, len(commandSpecs))
	for _, spec := range commandSpecs {
		commands[spec.Command.Name] = spec.Command
	}
	for _, removed := range []string{"any", "keno-best", "keno", "lock", "unlock"} {
		if _, exists := commands[removed]; exists {
			t.Errorf("command /%s should not be registered", removed)
		}
	}

	for commandName, wantSubcommands := range map[string][]string{
		"ban":          {"add", "remove", "view"},
		"channel":      {"lock", "unlock"},
		"grant-access": {"grant", "remove"},
		"remind":       {"add", "list", "delete"},
		"emojigg":      {"search", "list", "random", "preview", "add"},
	} {
		command, exists := commands[commandName]
		if !exists {
			t.Errorf("command /%s is not registered", commandName)
			continue
		}
		got := make(map[string]bool, len(command.Options))
		for _, option := range command.Options {
			if option.Type == discordgo.ApplicationCommandOptionSubCommand {
				got[option.Name] = true
			}
		}
		for _, subcommand := range wantSubcommands {
			if !got[subcommand] {
				t.Errorf("/%s is missing subcommand %q", commandName, subcommand)
			}
		}
	}

	if commands["emojigg"] == nil {
		t.Fatal("/emojigg is not registered")
	}
	var emojiGGAdd *discordgo.ApplicationCommandOption
	for _, option := range commands["emojigg"].Options {
		if option.Name == "add" {
			emojiGGAdd = option
			break
		}
	}
	if emojiGGAdd == nil {
		t.Fatal("/emojigg is missing the add subcommand")
	}
	emojiGGAddOptions := make(map[string]*discordgo.ApplicationCommandOption, len(emojiGGAdd.Options))
	for _, option := range emojiGGAdd.Options {
		emojiGGAddOptions[option.Name] = option
	}
	for _, optionName := range []string{"asset", "name"} {
		if emojiGGAddOptions[optionName] == nil {
			t.Errorf("/emojigg add is missing option %q", optionName)
		}
	}
	if emojiGGAddOptions["asset"] != nil && !emojiGGAddOptions["asset"].Required {
		t.Error("/emojigg add asset option must be required")
	}
	if emojiGGAddOptions["name"] != nil && emojiGGAddOptions["name"].Required {
		t.Error("/emojigg add name option must remain optional")
	}
	var emojiGGSearch *discordgo.ApplicationCommandOption
	for _, option := range commands["emojigg"].Options {
		if option.Name == "search" {
			emojiGGSearch = option
			break
		}
	}
	if emojiGGSearch == nil {
		t.Fatal("/emojigg is missing the search subcommand")
	}
	for _, option := range emojiGGSearch.Options {
		if option.Name == "page" && option.Required {
			t.Error("/emojigg search page option must remain optional")
		}
	}
	var emojiGGPreview *discordgo.ApplicationCommandOption
	for _, option := range commands["emojigg"].Options {
		if option.Name == "preview" {
			emojiGGPreview = option
			break
		}
	}
	if emojiGGPreview == nil {
		t.Fatal("/emojigg is missing the preview subcommand")
	}
	if len(emojiGGPreview.Options) != 1 || emojiGGPreview.Options[0].Name != "asset" || !emojiGGPreview.Options[0].Required {
		t.Error("/emojigg preview must require one asset option")
	}

	var remindAdd *discordgo.ApplicationCommandOption
	for _, option := range commands["remind"].Options {
		if option.Name == "add" {
			remindAdd = option
			break
		}
	}
	if remindAdd == nil {
		t.Fatal("/remind is missing the add subcommand")
	}
	addOptions := make(map[string]*discordgo.ApplicationCommandOption, len(remindAdd.Options))
	for _, option := range remindAdd.Options {
		addOptions[option.Name] = option
	}
	for _, optionName := range []string{"when", "message", "repeat", "timezone"} {
		if addOptions[optionName] == nil {
			t.Errorf("/remind add is missing option %q", optionName)
		}
	}
	for _, optionName := range []string{"when", "message"} {
		if addOptions[optionName] != nil && !addOptions[optionName].Required {
			t.Errorf("/remind add %s option must remain required", optionName)
		}
	}
	if addOptions["repeat"] != nil && addOptions["repeat"].Required {
		t.Error("/remind add repeat option must remain optional for backward compatibility")
	}
	if addOptions["timezone"] != nil && addOptions["timezone"].Required {
		t.Error("/remind add timezone option must remain optional for backward compatibility")
	}
}
