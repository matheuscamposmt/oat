package config

import (
	"errors"
	"os"
	"strings"
)

// ErrNoKey means that no Groq key exists in the two places that oat reads.
var ErrNoKey = errors.New(`no Groq API key found. Set GROQ_API_KEY, or add groq_api_key = "gsk_..." to ~/.config/oat/config.toml`)

// GroqKey returns the Groq key and the place where oat found it. The order is
// the environment variable GROQ_API_KEY, then groq_api_key in the
// configuration file at configPath.
func GroqKey(cfg Config, configPath string) (key, source string, err error) {
	if v := strings.TrimSpace(os.Getenv("GROQ_API_KEY")); v != "" {
		return v, "environment variable GROQ_API_KEY", nil
	}
	if v := strings.TrimSpace(cfg.GroqAPIKey); v != "" {
		return v, configPath, nil
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
