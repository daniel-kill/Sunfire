package venice

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// clonedVoiceStore persists cloned-voice handles locally, because the Venice
// API has no list endpoint for them (a handle is only returned at creation).
// File lives in Data/cloned-voices.json (gitignored).
var clonedVoiceStorePathDefault = func() string {
	if dir := os.Getenv("SUNFIRE_DATA_DIR"); dir != "" {
		return filepath.Join(dir, "cloned-voices.json")
	}
	return filepath.Join("Data", "cloned-voices.json")
}()

// clonedVoiceStorePath is a var (not const) so tests can override it.
var clonedVoiceStorePath = clonedVoiceStorePathDefault

// clonedVoice is one stored cloned-voice handle.
type clonedVoice struct {
	ID        string `json:"id"`        // vv_... handle
	Model     string `json:"model"`     // tts-chatterbox-hd | tts-minimax-speech-02-hd
	Name      string `json:"name"`      // friendly name chosen by user
	CreatedAt string `json:"createdAt"` // RFC3339
}

type clonedVoiceStore struct {
	Voices []clonedVoice `json:"voices"`
}

func loadClonedVoiceStore() (*clonedVoiceStore, error) {
	s := &clonedVoiceStore{Voices: []clonedVoice{}}
	b, err := os.ReadFile(filepath.Clean(clonedVoiceStorePath))
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, err
	}
	if err := json.Unmarshal(b, s); err != nil {
		return nil, fmt.Errorf("parsing cloned-voices.json: %w", err)
	}
	if s.Voices == nil {
		s.Voices = []clonedVoice{}
	}
	return s, nil
}

func (s *clonedVoiceStore) save() error {
	dir := filepath.Dir(filepath.Clean(clonedVoiceStorePath))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Clean(clonedVoiceStorePath), b, 0o644)
}

// add appends (or replaces by ID) a cloned voice and persists it.
func (s *clonedVoiceStore) add(id, model, name string) error {
	for i := range s.Voices {
		if s.Voices[i].ID == id {
			s.Voices[i].Model = model
			s.Voices[i].Name = name
			s.Voices[i].CreatedAt = time.Now().UTC().Format(time.RFC3339)
			return s.save()
		}
	}
	s.Voices = append(s.Voices, clonedVoice{
		ID: id, Model: model, Name: name,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	})
	return s.save()
}

// remove deletes a handle by ID or by name.
func (s *clonedVoiceStore) remove(idOrName string) bool {
	idx := -1
	for i, v := range s.Voices {
		if v.ID == idOrName || strings.EqualFold(v.Name, idOrName) {
			idx = i
			break
		}
	}
	if idx < 0 {
		return false
	}
	s.Voices = append(s.Voices[:idx], s.Voices[idx+1:]...)
	_ = s.save()
	return true
}

// find returns the handle matching a name or exact id, plus its model.
func (s *clonedVoiceStore) find(idOrName string) (string, string, bool) {
	for _, v := range s.Voices {
		if v.ID == idOrName || strings.EqualFold(v.Name, idOrName) {
			return v.ID, v.Model, true
		}
	}
	return "", "", false
}

// format renders the stored cloned voices.
func (s *clonedVoiceStore) format() string {
	if len(s.Voices) == 0 {
		return "No cloned voices stored yet. Use `/voice-clone name:<name> mp3:<file>` to create one."
	}
	var b strings.Builder
	b.WriteString("🎙️ **Your Venice cloned voices**\n")
	for _, v := range s.Voices {
		name := v.Name
		if name == "" {
			name = "(unnamed)"
		}
		b.WriteString(fmt.Sprintf("• **%s**\n  `%s` · %s\n", name, v.ID, v.Model))
	}
	return b.String()
}
