package main

import (
	"testing"
)

func TestDockerContainerNameValidation(t *testing.T) {
	valid := []string{
		"aegis",
		"steam-docker",
		"codex_engine.1",
		"2c395513e4af",
		"ollama-prod-01",
	}
	invalid := []string{
		"",
		"rm -rf /",
		"test; cat /etc/passwd",
		"container name with spaces",
		"test$(whoami)",
		"foo`bar`",
		"a-very-long-container-name-that-exceeds-the-maximum-sixty-four-character-limit-for-docker-containers-which-is-invalid",
	}

	for _, name := range valid {
		if !isValidContainerName(name) {
			t.Errorf("expected %q to be valid", name)
		}
	}

	for _, name := range invalid {
		if isValidContainerName(name) {
			t.Errorf("expected %q to be invalid", name)
		}
	}
}

func TestParseDockerPsOutput(t *testing.T) {
	raw := `dd2c7a0998f5	steam-docker	Exited (137) 2 days ago	lscr.io/linuxserver/steam:latest	
2c395513e4af	aegis	Up 6 hours	adb7890916de	0.0.0.0:8080->80/tcp
`
	containers := parseDockerPsOutput(raw)
	if len(containers) != 2 {
		t.Fatalf("expected 2 containers, got %d", len(containers))
	}

	if containers[0].Name != "steam-docker" || containers[0].ID != "dd2c7a0998f5" || containers[0].Status != "Exited (137) 2 days ago" {
		t.Errorf("unexpected container 0: %+v", containers[0])
	}

	if containers[1].Name != "aegis" || containers[1].Ports != "0.0.0.0:8080->80/tcp" {
		t.Errorf("unexpected container 1: %+v", containers[1])
	}
}

func TestDockerCommandSpec(t *testing.T) {
	spec := dockerCommandSpec()
	if spec.Command.Name != "docker" {
		t.Fatalf("expected command name docker, got %q", spec.Command.Name)
	}

	subcommands := make(map[string]bool)
	for _, opt := range spec.Command.Options {
		subcommands[opt.Name] = true
	}

	for _, expected := range []string{"status", "restart", "start", "stop", "logs"} {
		if !subcommands[expected] {
			t.Errorf("missing subcommand %q", expected)
		}
	}
}
