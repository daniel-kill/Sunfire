package main

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestHermesChatArgsChooseResumeOrFreshSession(t *testing.T) {
	content := "hello"
	wantResume := []string{"--continue", "--yolo", "chat", "-q", content, "-Q", "--max-turns", "20"}
	if got := hermesChatArgs(content, false); !reflect.DeepEqual(got, wantResume) {
		t.Fatalf("hermesChatArgs(resume) = %#v; want %#v", got, wantResume)
	}

	wantFresh := []string{"--yolo", "chat", "-q", content, "-Q", "--max-turns", "20"}
	if got := hermesChatArgs(content, true); !reflect.DeepEqual(got, wantFresh) {
		t.Fatalf("hermesChatArgs(new session) = %#v; want %#v", got, wantFresh)
	}
}

func TestHermesFailureMessageIncludesNewSessionInstructionForResumePolicyError(t *testing.T) {
	err := errors.New("Hermes execution failed: Error: Resume retains gateway session policy; creation overrides are unsupported on resume. --max-turns differs from this session's launch")
	want := "Failed to contact Hermes: " + err.Error() + "\n- To start a new session enter exactly: \"start-new-session\""
	if got := hermesFailureMessage(err); got != want {
		t.Fatalf("hermesFailureMessage() = %q; want %q", got, want)
	}
}

func TestHermesFailureMessageOmitsNewSessionInstructionForOtherErrors(t *testing.T) {
	err := errors.New("Hermes executable not found")
	want := "Failed to contact Hermes: " + err.Error()
	if got := hermesFailureMessage(err); got != want {
		t.Fatalf("hermesFailureMessage() = %q; want %q", got, want)
	}
}

func TestResolveHermesBinaryUsesConfiguredPath(t *testing.T) {
	configuredPath := filepath.Join(t.TempDir(), "configured-hermes")
	t.Setenv("SUNFIRE_HERMES_BIN", configuredPath)

	got, err := resolveHermesBinary()
	if err != nil {
		t.Fatal(err)
	}
	if got != configuredPath {
		t.Fatalf("resolveHermesBinary() = %q; want %q", got, configuredPath)
	}
}

func TestResolveHermesBinarySearchesPath(t *testing.T) {
	binDir := t.TempDir()
	binaryPath := filepath.Join(binDir, "hermes")
	if err := os.WriteFile(binaryPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SUNFIRE_HERMES_BIN", "")
	t.Setenv("PATH", binDir)

	got, err := resolveHermesBinary()
	if err != nil {
		t.Fatal(err)
	}
	if got != binaryPath {
		t.Fatalf("resolveHermesBinary() = %q; want %q", got, binaryPath)
	}
}

func TestResolveHermesBinarySearchesUserLocalBin(t *testing.T) {
	homeDir := t.TempDir()
	binDir := filepath.Join(homeDir, ".local", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	binaryPath := filepath.Join(binDir, "hermes")
	if err := os.WriteFile(binaryPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SUNFIRE_HERMES_BIN", "")
	t.Setenv("PATH", "")
	t.Setenv("HOME", homeDir)

	got, err := resolveHermesBinary()
	if err != nil {
		t.Fatal(err)
	}
	if got != binaryPath {
		t.Fatalf("resolveHermesBinary() = %q; want %q", got, binaryPath)
	}
}

func TestResolveHermesBinaryReportsMissingExecutable(t *testing.T) {
	t.Setenv("SUNFIRE_HERMES_BIN", "")
	t.Setenv("PATH", "")
	t.Setenv("HOME", t.TempDir())

	if _, err := resolveHermesBinary(); err == nil {
		t.Fatal("resolveHermesBinary() succeeded without an installed Hermes executable")
	}
}
