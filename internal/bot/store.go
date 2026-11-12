package bot

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"github.com/iraosandeep/courier/internal/llm"
)

// maxStoredTurns caps history per chat; the prompt is trimmed further to
// fit each model's context.
const maxStoredTurns = 200

type chatState struct {
	Model   string        `json:"model,omitempty"`
	System  string        `json:"system"`
	History []llm.Message `json:"history,omitempty"`
}

// store keeps per-chat state in memory, mirrored to a JSON file if a path
// is set.
type store struct {
	path  string
	mu    sync.Mutex
	chats map[int64]*chatState
}

func openStore(path string) (*store, error) {
	s := &store{chats: map[int64]*chatState{}}
	if path == "" || path == "-" {
		return s, nil
	}
	s.path = path
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &s.chats); err != nil {
		return nil, errors.New(path + ": " + err.Error())
	}
	return s, nil
}

// get returns a copy of the chat's state, creating it with the default
// system prompt if new.
func (s *store) get(chatID int64, defaultSystem string) chatState {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.chat(chatID, defaultSystem)
	return chatState{Model: c.Model, System: c.System, History: slices.Clone(c.History)}
}

func (s *store) chat(chatID int64, defaultSystem string) *chatState {
	c, ok := s.chats[chatID]
	if !ok {
		c = &chatState{System: defaultSystem}
		s.chats[chatID] = c
	}
	return c
}

func (s *store) update(chatID int64, defaultSystem string, f func(*chatState)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f(s.chat(chatID, defaultSystem))
	s.save()
}

func (s *store) appendTurn(chatID int64, defaultSystem, user, assistant string) {
	s.update(chatID, defaultSystem, func(c *chatState) {
		c.History = append(c.History,
			llm.Message{Role: "user", Content: user},
			llm.Message{Role: "assistant", Content: assistant})
		if n := len(c.History) - maxStoredTurns; n > 0 {
			c.History = c.History[n:]
		}
	})
}

// save writes the file atomically; it is called with s.mu held. Chats hold
// private conversations, so the file is owner-only.
func (s *store) save() {
	if s.path == "" {
		return
	}
	b, err := json.Marshal(s.chats)
	if err != nil {
		return
	}
	os.MkdirAll(filepath.Dir(s.path), 0o700)
	tmp := s.path + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		os.Rename(tmp, s.path)
	}
}
