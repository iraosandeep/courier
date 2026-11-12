// Package config loads Courier's settings from a JSON file and environment
// variables. Environment variables win over the file.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/iraosandeep/courier/internal/llm"
)

type Config struct {
	BotToken string `json:"bot_token"`
	// AllowedUsers are the Telegram user IDs the bot answers. Everyone else
	// is refused, so a stranger who finds the bot can't use your models.
	AllowedUsers []int64       `json:"allowed_users"`
	Backends     []llm.Backend `json:"backends,omitempty"`
	// DefaultModel is "backend/model". Empty picks the first model found.
	DefaultModel   string `json:"default_model,omitempty"`
	SystemPrompt   string `json:"system_prompt,omitempty"`
	TelegramAPIURL string `json:"telegram_api_url,omitempty"`
	// StateFile keeps chats, model choices and history across restarts.
	// Empty means state.json next to the config file; "-" disables it.
	StateFile string `json:"state_file,omitempty"`

	Path string `json:"-"` // where the config was loaded from
}

// DefaultBackends are the usual local servers on their default ports.
// Unreachable ones are skipped when listing models.
func DefaultBackends() []llm.Backend {
	return []llm.Backend{
		{Name: "fm", URL: "http://127.0.0.1:1976"},       // Apple Foundation Models (fm serve / Orchard)
		{Name: "ollama", URL: "http://127.0.0.1:11434"},  // Ollama
		{Name: "lmstudio", URL: "http://127.0.0.1:1234"}, // LM Studio
		{Name: "llamacpp", URL: "http://127.0.0.1:8080"}, // llama.cpp llama-server
	}
}

// DefaultPath is ~/.config/courier/config.json (or $XDG_CONFIG_HOME).
func DefaultPath() string {
	dir, err := os.UserConfigDir()
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		dir, err = xdg, nil
	} else if home, herr := os.UserHomeDir(); herr == nil {
		// Prefer ~/.config on macOS too, like most CLI tools.
		dir, err = filepath.Join(home, ".config"), nil
	}
	if err != nil {
		return "courier.json"
	}
	return filepath.Join(dir, "courier", "config.json")
}

// Load reads path (or COURIER_CONFIG, or DefaultPath) and applies env vars.
// A missing file is not an error.
func Load(path string) (*Config, error) {
	if path == "" {
		path = os.Getenv("COURIER_CONFIG")
	}
	if path == "" {
		path = DefaultPath()
	}
	c := &Config{Path: path}
	b, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return nil, err
	default:
		if err := json.Unmarshal(b, c); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
	}

	if v := os.Getenv("COURIER_BOT_TOKEN"); v != "" {
		c.BotToken = v
	}
	if v := os.Getenv("COURIER_ALLOWED_USERS"); v != "" {
		ids, err := ParseIDs(v)
		if err != nil {
			return nil, fmt.Errorf("COURIER_ALLOWED_USERS: %w", err)
		}
		c.AllowedUsers = ids
	}
	if v := os.Getenv("COURIER_DEFAULT_MODEL"); v != "" {
		c.DefaultModel = v
	}
	if v := os.Getenv("COURIER_TELEGRAM_API_URL"); v != "" {
		c.TelegramAPIURL = v
	}
	if len(c.Backends) == 0 {
		c.Backends = DefaultBackends()
	}
	if c.StateFile == "" {
		c.StateFile = filepath.Join(filepath.Dir(path), "state.json")
	}
	return c, nil
}

// Validate reports settings the bot can't run without.
func (c *Config) Validate() error {
	if c.BotToken == "" {
		return errors.New("no bot token: run `courier setup`, or set COURIER_BOT_TOKEN")
	}
	if len(c.AllowedUsers) == 0 {
		return errors.New("no allowed users: run `courier setup`, or set COURIER_ALLOWED_USERS")
	}
	seen := map[string]bool{}
	for _, b := range c.Backends {
		if b.Name == "" || strings.Contains(b.Name, "/") || b.URL == "" {
			return fmt.Errorf("backend %+v needs a name without '/' and a url", b)
		}
		if seen[b.Name] {
			return fmt.Errorf("duplicate backend name %q", b.Name)
		}
		seen[b.Name] = true
	}
	return nil
}

// Save writes the config with owner-only permissions (it holds the token).
func (c *Config) Save() error {
	if err := os.MkdirAll(filepath.Dir(c.Path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := c.Path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, c.Path)
}

// ParseIDs parses a comma-separated list of Telegram user IDs.
func ParseIDs(s string) ([]int64, error) {
	var ids []int64
	for _, f := range strings.Split(s, ",") {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		id, err := strconv.ParseInt(f, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("%q is not a user ID", f)
		}
		ids = append(ids, id)
	}
	return ids, nil
}
