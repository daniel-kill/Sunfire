package main

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
)

func TestHermesTipsParsingAndDeduplication(t *testing.T) {
	tmpDir := t.TempDir()
	origPath := hermesJSONPath
	origDelay := hermesRateLimitDelay
	oldHTTPClient := notifierHTTPClient
	notifierHTTPClient = &http.Client{Transport: notifierRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		response := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Request: req}
		switch req.URL.Host {
		case "hermes-agent.nousresearch.com":
			response.Body = io.NopCloser(strings.NewReader("- [Deterministic test](https://example.test): fixture tip\n"))
		case "api.github.com":
			response.Body = io.NopCloser(strings.NewReader(`[{"name":"v1","body":"- A deterministic release note with enough characters"}]`))
		default:
			t.Fatalf("unexpected Hermes test request: %s", req.URL)
		}
		return response, nil
	})}
	defer func() {
		hermesJSONPath = origPath
		hermesRateLimitDelay = origDelay
		notifierHTTPClient = oldHTTPClient
	}()

	hermesJSONPath = filepath.Join(tmpDir, "hermes_tips.json")
	hermesRateLimitDelay = 10 * time.Millisecond

	initHermesTipsState()
	if len(hermesState.Tips) != 0 || len(hermesState.PostedKeys) != 0 {
		t.Fatalf("expected empty initial state, got %+v", hermesState)
	}

	count1, err := checkHermesTipsUpdates(nil)
	if err != nil {
		t.Fatalf("checkHermesTipsUpdates failed: %v", err)
	}
	if count1 == 0 {
		t.Fatalf("expected new tips to be collected, got %d", count1)
	}
	if _, err := os.Stat(hermesJSONPath); err != nil {
		t.Fatalf("expected hermes_tips.json to exist at %s", hermesJSONPath)
	}

	count2, err := checkHermesTipsUpdates(nil)
	if err != nil {
		t.Fatalf("checkHermesTipsUpdates second pass failed: %v", err)
	}
	if count2 != 0 {
		t.Fatalf("expected 0 new tips on second pass, got %d (deduplication failed)", count2)
	}

	initHermesTipsState()
	if len(hermesState.PostedKeys) != count1 {
		t.Errorf("expected %d posted keys loaded from file, got %d", count1, len(hermesState.PostedKeys))
	}
}

func TestHermesNotifierMarksTipsPostedOnlyAfterSuccessfulDelivery(t *testing.T) {
	tmpDir := t.TempDir()
	oldPath, oldState, oldDelay, oldChan := hermesJSONPath, hermesState, hermesRateLimitDelay, HermesTipsChannelID
	oldClient, oldSend := notifierHTTPClient, sendNotifierMessage
	defer func() {
		hermesJSONPath = oldPath
		hermesState = oldState
		hermesRateLimitDelay = oldDelay
		HermesTipsChannelID = oldChan
		notifierHTTPClient = oldClient
		sendNotifierMessage = oldSend
	}()

	HermesTipsChannelID = "test-channel"

	hermesJSONPath = filepath.Join(tmpDir, "hermes_tips.json")
	hermesRateLimitDelay = time.Millisecond
	notifierHTTPClient = &http.Client{Transport: notifierRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		response := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Request: req}
		switch req.URL.Host {
		case "hermes-agent.nousresearch.com":
			response.Body = io.NopCloser(strings.NewReader("- [Delivery sequence](https://example.test): deterministic fixture\n"))
		case "api.github.com":
			response.Body = io.NopCloser(strings.NewReader("[]"))
		default:
			t.Fatalf("unexpected Hermes test request: %s", req.URL)
		}
		return response, nil
	})}
	if err := initHermesTipsState(); err != nil {
		t.Fatal(err)
	}
	failSend := true
	sendNotifierMessage = func(_ *discordgo.Session, _, _ string) (*discordgo.Message, error) {
		if failSend {
			return nil, io.ErrUnexpectedEOF
		}
		return &discordgo.Message{ID: "sent"}, nil
	}
	session := &discordgo.Session{}

	if _, err := checkHermesTipsUpdates(session); err != nil {
		t.Fatal(err)
	}
	if len(hermesState.PostedKeys) != 0 {
		t.Fatalf("failed Discord send was recorded as delivered: %#v", hermesState.PostedKeys)
	}

	failSend = false
	count, err := checkHermesTipsUpdates(session)
	if err != nil {
		t.Fatal(err)
	}
	if count == 0 || len(hermesState.PostedKeys) == 0 {
		t.Fatalf("successful Discord send was not recorded: count=%d state=%#v", count, hermesState)
	}
}


func TestHermesNotifierPausesWhenChannelNotConfigured(t *testing.T) {
	tmpDir := t.TempDir()
	oldPath, oldState, oldDelay, oldChan := hermesJSONPath, hermesState, hermesRateLimitDelay, HermesTipsChannelID
	defer func() {
		hermesJSONPath = oldPath
		hermesState = oldState
		hermesRateLimitDelay = oldDelay
		HermesTipsChannelID = oldChan
	}()

	hermesJSONPath = filepath.Join(tmpDir, "hermes_tips.json")
	hermesRateLimitDelay = time.Millisecond
	HermesTipsChannelID = ""

	session := &discordgo.Session{}
	count, err := checkHermesTipsUpdates(session)
	if err != nil {
		t.Fatalf("expected no error when channel not configured, got: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected 0 delivered tips when channel not configured, got: %d", count)
	}
}