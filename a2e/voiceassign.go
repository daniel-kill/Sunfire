// voiceassign.go — local voice-assignment store.
//
// The user assigns friendly names to voice IDs in a local JSON file. This
// module loads/edits that file. The file lives in the repo's data/ dir and is
// gitignored (personal mapping, like config). A committed example template is
// provided as data/voice-assignments.example.json.
package a2e

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const voiceAssignmentsRelPathDefault = "../data/voice-assignments.json"

// voiceAssignmentsRelPath is a var (not const) so tests can override it.
var voiceAssignmentsRelPath = voiceAssignmentsRelPathDefault

// voiceAssignments maps a voice ID -> friendly name chosen by the user.
type voiceAssignments struct {
	// VoiceID -> name
	Names map[string]string `json:"names"`
}

func loadVoiceAssignments() (*voiceAssignments, error) {
	va := &voiceAssignments{Names: map[string]string{}}
	b, err := os.ReadFile(filepath.Clean(voiceAssignmentsRelPath))
	if err != nil {
		if os.IsNotExist(err) {
			return va, nil // not created yet — fine
		}
		return nil, err
	}
	if err := json.Unmarshal(b, va); err != nil {
		return nil, fmt.Errorf("parsing voice-assignments.json: %w", err)
	}
	if va.Names == nil {
		va.Names = map[string]string{}
	}
	return va, nil
}

func (va *voiceAssignments) save() error {
	dir := filepath.Dir(filepath.Clean(voiceAssignmentsRelPath))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(va, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Clean(voiceAssignmentsRelPath), b, 0o644)
}

// assignName sets a friendly name for a voice ID and persists it.
func (va *voiceAssignments) assignName(voiceID, name string) error {
	va.Names[voiceID] = name
	return va.save()
}

// displayName returns the friendly name for a voice ID (or the raw ID if
// unassigned).
func (va *voiceAssignments) displayName(voiceID string) string {
	if n, ok := va.Names[voiceID]; ok && n != "" {
		return n
	}
	return voiceID
}

// sanitizeVoiceID strips whitespace and any stray " name"-style suffix that can
// sneak in from Discord option parsing, leaving just the hex ID.
func sanitizeVoiceID(s string) string {
	s = strings.TrimSpace(s)
	// Take only the first whitespace-delimited token (defensive against
	// concatenated "id name" input).
	if i := strings.IndexAny(s, " \t\n"); i >= 0 {
		s = s[:i]
	}
	return s
}

// isValidVoiceID reports whether s is a valid A2E voice ID (24-char hex).
func isValidVoiceID(s string) bool {
	if len(s) != 24 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'a' && r <= 'f':
		case r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return true
}

// lookupByPrefix returns the voice ID(s) matching a given prefix (short id or
// friendly name fragment). Used to resolve the /tts voice argument.
func (va *voiceAssignments) lookupByPrefix(prefix string) ([]string, string) {
	prefix = strings.ToLower(strings.TrimSpace(prefix))
	var matches []string
	var viaName string
	for id, name := range va.Names {
		if strings.HasPrefix(strings.ToLower(id), prefix) {
			matches = append(matches, id)
			viaName = name
		} else if strings.HasPrefix(strings.ToLower(name), prefix) {
			matches = append(matches, id)
			viaName = name
		}
	}
	sort.Strings(matches)
	return matches, viaName
}

// formatVoiceList renders the completed voices as a compact, aligned block.
func (va *voiceAssignments) formatCompleted(voices []UserVoice, training []UserVoice) string {
	var b strings.Builder
	b.WriteString("🎙️ **Your cloned voices**\n\n")
	if len(voices) == 0 {
		b.WriteString("_No completed cloned voices yet. Use `/voice-clone` to train one._\n")
	} else {
		// group by status
		for _, v := range voices {
			name := v.Name
			if v.CurrentStatus == "" {
				v.CurrentStatus = "completed"
			}
			assigned := va.Names[v.ID]
			b.WriteString(fmt.Sprintf("• **%s**\n", orVal(assigned, name)))
			b.WriteString(fmt.Sprintf("  `%s` · %s · %s\n", v.ID, v.Gender, v.Model))
			if assigned != "" && assigned != name {
				b.WriteString(fmt.Sprintf("  _API name: %s_\n", name))
			}
		}
	}
	if len(training) > 0 {
		b.WriteString("\n**In progress / training**\n")
		shown := 0
		for _, v := range training {
			// trainingRecord returns ALL records (incl. completed); only show
			// ones that are not actually completed.
			if strings.EqualFold(v.CurrentStatus, "completed") {
				continue
			}
			b.WriteString(fmt.Sprintf("• `%s` · **%s** · %s\n", v.ID, v.Name, orVal(v.CurrentStatus, "pending")))
			shown++
		}
		if shown == 0 {
			b.WriteString("_No voice currently training._\n")
		}
	}
	return b.String()
}

// formatBuiltin renders the built-in preset voices grouped by gender.
func (va *voiceAssignments) formatBuiltin(groups []VoiceGroup) string {
	var b strings.Builder
	b.WriteString("🗣️ **Built-in A2E voices**\n\n")
	for _, g := range groups {
		b.WriteString(fmt.Sprintf("**%s** (%d)\n", strings.Title(g.Label), len(g.Children)))
		for _, v := range g.Children {
			b.WriteString(fmt.Sprintf("• `%s` · %s\n", v.Value, v.Label))
		}
	}
	return b.String()
}
