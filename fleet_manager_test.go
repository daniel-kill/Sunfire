package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFleetRepoNameValidation(t *testing.T) {
	valid := []string{
		"Sunfire",
		"TROSDAHD",
		"The-Apocrypha-of-Expulsion",
		"Windows-NonAdmin-Installers",
		"aegis_logger",
		"hostinger.website",
	}

	invalid := []string{
		"",
		"../secret",
		"foo/bar",
		"repo with spaces",
		"rm -rf /",
		"`echo test`",
		"a-very-long-repo-name-that-exceeds-the-maximum-sixty-four-character-limit-for-repos-which-is-invalid",
	}

	for _, name := range valid {
		if !isValidRepoName(name) {
			t.Errorf("expected %q to be valid", name)
		}
	}

	for _, name := range invalid {
		if isValidRepoName(name) {
			t.Errorf("expected %q to be invalid", name)
		}
	}
}

func TestResolveFleetReposRoot(t *testing.T) {
	orig := os.Getenv("SUNFIRE_REPOS_ROOT")
	defer func() {
		_ = os.Setenv("SUNFIRE_REPOS_ROOT", orig)
	}()

	_ = os.Setenv("SUNFIRE_REPOS_ROOT", "/custom/test/repos")
	if got := resolveFleetReposRoot(); got != "/custom/test/repos" {
		t.Errorf("expected /custom/test/repos, got %q", got)
	}

	_ = os.Unsetenv("SUNFIRE_REPOS_ROOT")
	gotDefault := resolveFleetReposRoot()
	if gotDefault == "" {
		t.Error("expected non-empty default root")
	}
}

func TestFleetCommandSpec(t *testing.T) {
	spec := fleetCommandSpec()
	if spec.Command.Name != "fleet" {
		t.Fatalf("expected command name fleet, got %q", spec.Command.Name)
	}

	subcommands := make(map[string]bool)
	for _, opt := range spec.Command.Options {
		subcommands[opt.Name] = true
	}

	if !subcommands["status"] || !subcommands["repo"] {
		t.Errorf("missing expected subcommands in fleet spec: %+v", subcommands)
	}
}

func TestInspectRepoHealthCurrent(t *testing.T) {
	tmpDir := t.TempDir()
	gitDir := filepath.Join(tmpDir, ".git")
	if err := os.Mkdir(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}

	ctx := t.Context()
	h := inspectRepoHealth(ctx, "test-repo", tmpDir)
	if h.Name != "test-repo" {
		t.Errorf("expected name test-repo, got %q", h.Name)
	}
}
