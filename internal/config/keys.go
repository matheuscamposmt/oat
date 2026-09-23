package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrNoKey means that no Groq key exists in the three places that oat reads.
var ErrNoKey = errors.New(`no Groq API key found. Set GROQ_API_KEY, or add it to the "env" block of ~/.claude/settings.local.json or ~/.claude/settings.json`)

// GroqKey returns the Groq key and the place where oat found it. The order is
// the environment, then settings.local.json, then settings.json in the Claude
// Code folder ($CLAUDE_CONFIG_DIR or ~/.claude).
func GroqKey() (key, source string, err error) {
	if v := strings.TrimSpace(os.Getenv("GROQ_API_KEY")); v != "" {
		return v, "environment variable GROQ_API_KEY", nil
	}
	dir, err := claudeDir()
	if err != nil {
		return "", "", err
	}
	for _, name := range []string{"settings.local.json", "settings.json"} {
		path := filepath.Join(dir, name)
		v, err := keyFromSettings(path)
		if err != nil {
			return "", "", err
		}
		if v != "" {
			return v, path, nil
		}
	}
	return "", "", ErrNoKey
}

// MaskKey shows only the first 4 and the last 4 characters of a key.
func MaskKey(k string) string {
	if len(k) <= 8 {
		return strings.Repeat("*", len(k))
	}
	return k[:4] + "…" + k[len(k)-4:]
}

func claudeDir() (string, error) {
	if v := os.Getenv("CLAUDE_CONFIG_DIR"); v != "" {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude"), nil
}

func keyFromSettings(path string) (string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var s struct {
		Env map[string]any `json:"env"`
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	v, _ := s.Env["GROQ_API_KEY"].(string)
	return strings.TrimSpace(v), nil
}
