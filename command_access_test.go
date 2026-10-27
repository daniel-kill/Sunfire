package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCommandAccessPersistsAndDoesNotDuplicate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "commandAccess.json")

	if err := grantCommandAccess(path, "setup", "member-1"); err != nil {
		t.Fatalf("grantCommandAccess failed: %v", err)
	}
	if err := grantCommandAccess(path, "setup", "member-1"); err != nil {
		t.Fatalf("duplicate grantCommandAccess failed: %v", err)
	}

	granted, err := hasCommandAccess(path, "setup", "member-1")
	if err != nil {
		t.Fatalf("hasCommandAccess failed: %v", err)
	}
	if !granted {
		t.Fatal("expected grant to persist")
	}
	granted, err = hasCommandAccess(path, "setup", "member-2")
	if err != nil {
		t.Fatalf("hasCommandAccess failed: %v", err)
	}
	if granted {
		t.Fatal("unexpected grant for a different member")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading persisted grants failed: %v", err)
	}
	if got := string(data); got == "" {
		t.Fatal("expected persisted grant data")
	}

	removed, err := removeCommandAccess(path, "setup", "member-1")
	if err != nil {
		t.Fatalf("removeCommandAccess failed: %v", err)
	}
	if !removed {
		t.Fatal("expected grant to be removed")
	}
	granted, err = hasCommandAccess(path, "setup", "member-1")
	if err != nil {
		t.Fatalf("hasCommandAccess after removal failed: %v", err)
	}
	if granted {
		t.Fatal("grant remained after removal")
	}
	removed, err = removeCommandAccess(path, "setup", "member-1")
	if err != nil {
		t.Fatalf("removing an absent grant failed: %v", err)
	}
	if removed {
		t.Fatal("reported removing a grant that was absent")
	}
}

func TestIsGrantableCommand(t *testing.T) {
	buildCommandSpecs()

	for _, name := range []string{"setup", "/SETUP"} {
		if !isGrantableCommand(name) {
			t.Errorf("expected %q to be grantable", name)
		}
	}
	for _, name := range []string{"", "help", "grant-access", "keno-best", "not-a-command"} {
		if isGrantableCommand(name) {
			t.Errorf("expected %q not to be grantable", name)
		}
	}
}
