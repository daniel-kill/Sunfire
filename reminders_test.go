package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseReminderTimeRelativeAndExact(t *testing.T) {
	now := time.Date(2026, time.September, 29, 12, 0, 0, 0, time.UTC)
	location, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatal(err)
	}

	relative, err := parseReminderTimeAt(" in 90m ", location, now)
	if err != nil {
		t.Fatalf("parse relative time: %v", err)
	}
	if want := now.Add(90 * time.Minute); !relative.Equal(want) {
		t.Fatalf("relative time = %s; want %s", relative, want)
	}

	exact, err := parseReminderTimeAt("09/29/2026 at 14:30", location, now)
	if err != nil {
		t.Fatalf("parse local exact time: %v", err)
	}
	if got, want := exact.Format(time.RFC3339), "2026-09-29T14:30:00-07:00"; got != want {
		t.Fatalf("local exact time = %s; want %s", got, want)
	}

	iso, err := parseReminderTimeAt("2026-09-29T14:30:00-04:00", location, now)
	if err != nil {
		t.Fatalf("parse ISO 8601 time: %v", err)
	}
	if _, offset := iso.Zone(); offset != -4*60*60 {
		t.Fatalf("ISO 8601 offset = %d seconds; want -14400", offset)
	}
}

func TestParseReminderTimeRejectsInvalidInputs(t *testing.T) {
	for _, value := range []string{"", "0s", "-5m", "next Tuesday"} {
		t.Run(value, func(t *testing.T) {
			_, err := parseReminderTimeAt(value, time.UTC, time.Now())
			if err == nil {
				t.Fatalf("parseReminderTimeAt(%q) succeeded; want error", value)
			}
		})
	}
	if _, err := parseReminderTimeInLocation("10m", "Not/A_Timezone"); err == nil {
		t.Fatal("invalid timezone succeeded; want error")
	}
}

func TestParseRepeatInterval(t *testing.T) {
	for _, test := range []struct {
		value string
		want  time.Duration
	}{
		{"30m", 30 * time.Minute},
		{"1h30m", 90 * time.Minute},
		{"1d", 24 * time.Hour},
		{"2w", 14 * 24 * time.Hour},
	} {
		t.Run(test.value, func(t *testing.T) {
			got, err := parseRepeatInterval(test.value)
			if err != nil {
				t.Fatalf("parseRepeatInterval(%q): %v", test.value, err)
			}
			if got != test.want {
				t.Fatalf("parseRepeatInterval(%q) = %s; want %s", test.value, got, test.want)
			}
		})
	}

	for _, value := range []string{"0s", "-1h", "tomorrow", "999999999999999999999w"} {
		if _, err := parseRepeatInterval(value); err == nil {
			t.Errorf("parseRepeatInterval(%q) succeeded; want error", value)
		}
	}
}

func TestNextReminderTimeSkipsMissedIntervals(t *testing.T) {
	previous := time.Date(2026, time.October, 1, 10, 0, 0, 0, time.UTC)
	now := previous.Add(35 * time.Minute)
	got := nextReminderTime(previous, 10*time.Minute, now)
	if want := previous.Add(40 * time.Minute); !got.Equal(want) {
		t.Fatalf("nextReminderTime = %s; want %s", got, want)
	}

	now = previous.Add(30 * time.Minute)
	got = nextReminderTime(previous, 10*time.Minute, now)
	if want := previous.Add(40 * time.Minute); !got.Equal(want) {
		t.Fatalf("nextReminderTime at interval boundary = %s; want %s", got, want)
	}

	ancient := time.Date(1, time.January, 1, 0, 0, 0, 0, time.UTC)
	farFuture := time.Date(9999, time.January, 1, 0, 0, 0, 0, time.UTC)
	got = nextReminderTime(ancient, time.Nanosecond, farFuture)
	if !got.After(farFuture) {
		t.Fatalf("nextReminderTime = %s; want a time after %s", got, farFuture)
	}
}

func TestReminderPersistenceIsBackwardCompatibleAndKeepsRepeat(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("SUNFIRE_DATA_DIR", dataDir)

	original := reminders
	defer func() {
		remindersMu.Lock()
		reminders = original
		remindersMu.Unlock()
	}()

	path := getRemindersFile()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`[{"id":"legacy","user_id":"u","channel_id":"c","message":"old","fire_time":"2026-10-01T10:00:00Z"}]`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := loadReminders(); err != nil {
		t.Fatalf("load legacy reminder: %v", err)
	}
	if len(reminders) != 1 || reminders[0].Repeat != "" {
		t.Fatalf("legacy reminder did not load as one-shot: %+v", reminders)
	}

	remindersMu.Lock()
	reminders = []Reminder{{
		ID: "repeat", UserID: "u", ChannelID: "c", Message: "daily",
		FireTime: time.Date(2026, time.October, 2, 10, 0, 0, 0, time.UTC), Repeat: "1d",
	}}
	err := saveReminders()
	remindersMu.Unlock()
	if err != nil {
		t.Fatalf("save recurring reminder: %v", err)
	}
	if err := loadReminders(); err != nil {
		t.Fatalf("reload recurring reminder: %v", err)
	}
	if len(reminders) != 1 || reminders[0].Repeat != "1d" {
		t.Fatalf("repeat interval was not persisted: %+v", reminders)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var persisted []map[string]json.RawMessage
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatalf("persisted reminder JSON is invalid: %v", err)
	}
	if _, exists := persisted[0]["repeat"]; !exists || !strings.Contains(string(data), `"repeat": "1d"`) {
		t.Fatalf("persisted JSON is missing the repeat interval: %s", data)
	}
}

func TestFormatReminderListShowsRepeat(t *testing.T) {
	got := formatReminderList([]Reminder{{
		ID: "id", Message: "check in", FireTime: time.Unix(1_800_000_000, 0), Repeat: "1w",
	}})
	if !strings.Contains(got, "(every 1w)") || !strings.Contains(got, "check in") {
		t.Fatalf("reminder list does not show recurring schedule: %q", got)
	}
}
