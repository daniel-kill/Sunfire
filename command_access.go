package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/bwmarrin/discordgo"
)

var commandAccessMu sync.Mutex

func loadCommandAccess(path string) (map[string][]string, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string][]string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading command grants: %w", err)
	}

	var grants map[string][]string
	if err := json.Unmarshal(data, &grants); err != nil {
		return nil, fmt.Errorf("parsing command grants: %w", err)
	}
	if grants == nil {
		grants = map[string][]string{}
	}
	return grants, nil
}

func saveCommandAccess(path string, grants map[string][]string) error {
	data, err := json.MarshalIndent(grants, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding command grants: %w", err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating command grants directory: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".command-access-*.tmp")
	if err != nil {
		return fmt.Errorf("creating temporary command grants file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("securing temporary command grants file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing command grants: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing command grants file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replacing command grants file: %w", err)
	}
	return nil
}

func grantCommandAccess(path, commandName, memberID string) error {
	commandAccessMu.Lock()
	defer commandAccessMu.Unlock()

	grants, err := loadCommandAccess(path)
	if err != nil {
		return err
	}
	for _, id := range grants[commandName] {
		if id == memberID {
			return nil
		}
	}
	grants[commandName] = append(grants[commandName], memberID)
	sort.Strings(grants[commandName])
	return saveCommandAccess(path, grants)
}

func removeCommandAccess(path, commandName, memberID string) (bool, error) {
	commandAccessMu.Lock()
	defer commandAccessMu.Unlock()

	grants, err := loadCommandAccess(path)
	if err != nil {
		return false, err
	}
	ids := grants[commandName]
	for i, id := range ids {
		if id != memberID {
			continue
		}
		grants[commandName] = append(ids[:i], ids[i+1:]...)
		if len(grants[commandName]) == 0 {
			delete(grants, commandName)
		}
		if err := saveCommandAccess(path, grants); err != nil {
			return false, err
		}
		return true, nil
	}
	return false, nil
}

func hasCommandAccess(path, commandName, memberID string) (bool, error) {
	commandAccessMu.Lock()
	defer commandAccessMu.Unlock()

	grants, err := loadCommandAccess(path)
	if err != nil {
		return false, err
	}
	for _, id := range grants[commandName] {
		if id == memberID {
			return true, nil
		}
	}
	return false, nil
}

func isGrantableCommand(name string) bool {
	name = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(name)), "/")
	if name == "" || name == "grant-access" || isPublicCommand(name) {
		return false
	}
	for _, spec := range commandSpecs {
		if spec.Command.Name == name {
			return true
		}
	}
	return false
}

func handleGrantAccess(s *discordgo.Session, i *discordgo.InteractionCreate) {
	options := i.ApplicationCommandData().Options
	if len(options) == 0 {
		respondGrantAccess(s, i, "❌ Choose `grant` or `remove`.")
		return
	}
	subcommand := options[0]
	commandName := ""
	var member *discordgo.User
	for _, opt := range subcommand.Options {
		switch opt.Name {
		case "command":
			commandName = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(opt.StringValue())), "/")
		case "member":
			member = opt.UserValue(s)
		}
	}

	if !isGrantableCommand(commandName) {
		respondGrantAccess(s, i, "❌ Choose an existing restricted command; public commands and `/grant-access` cannot be delegated.")
		return
	}
	if member == nil || member.ID == "" {
		respondGrantAccess(s, i, "❌ A valid member is required.")
		return
	}

	switch subcommand.Name {
	case "grant":
		if err := grantCommandAccess(dataPath("commandAccess.json"), commandName, member.ID); err != nil {
			log.Printf("failed to grant /%s access to member %s: %v", commandName, member.ID, err)
			respondGrantAccess(s, i, "❌ Could not save command access; please try again later.")
			return
		}
		respondGrantAccess(s, i, fmt.Sprintf("✅ Granted <@%s> access to `/%s`.", member.ID, commandName))
	case "remove":
		removed, err := removeCommandAccess(dataPath("commandAccess.json"), commandName, member.ID)
		if err != nil {
			log.Printf("failed to remove /%s access from member %s: %v", commandName, member.ID, err)
			respondGrantAccess(s, i, "❌ Could not save command access; please try again later.")
			return
		}
		if !removed {
			respondGrantAccess(s, i, fmt.Sprintf("ℹ️ <@%s> did not have access to `/%s`.", member.ID, commandName))
			return
		}
		respondGrantAccess(s, i, fmt.Sprintf("✅ Removed <@%s>'s access to `/%s`.", member.ID, commandName))
	default:
		respondGrantAccess(s, i, "❌ Choose `grant` or `remove`.")
	}
}

func respondGrantAccess(s *discordgo.Session, i *discordgo.InteractionCreate, content string) {
	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: content,
			Flags:   discordgo.MessageFlagsEphemeral,
		},
	})
}
