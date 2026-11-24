// Package config loads the configuration file, the command flags, the folder
// paths, and the Groq key.
package config

import (
	"errors"
	"fmt"
	"os"

	"github.com/BurntSushi/toml"
)

// Config holds the values that change how oat records and transcribes.
type Config struct {
	Transcriber string `toml:"transcriber"` // claude or groq
	Live        bool   `toml:"live"`        // claude only: show the text while people speak
	Lang        string `toml:"lang"`
	Model       string `toml:"model"`
	Echo        string `toml:"echo"`
	KeepAudio   bool   `toml:"keep_audio"`
	GroqAPIKey  string `toml:"groq_api_key"` // used when GROQ_API_KEY is not set
}

// Overrides holds the command flags. An empty string or false keeps the file value.
type Overrides struct {
	Transcriber string
	NoLive      bool
	Lang        string
	Model       string
	Echo        string
	KeepAudio   bool
}

// Default returns the configuration that oat uses without a file.
func Default() Config {
	return Config{Transcriber: "claude", Live: true, Lang: "pt", Model: "whisper-large-v3-turbo", Echo: "auto"}
}

// ClaudeModel is the model name that oat saves in meta.json for the Claude backend.
const ClaudeModel = "claude-voice-stream"

// MeetingModel returns the model name to save in meta.json.
func (c Config) MeetingModel() string {
	if c.Transcriber == "claude" {
		return ClaudeModel
	}
	return c.Model
}

// GroqModel returns the Whisper model for a meeting: the one in its meta.json,
// or the configured one when the meeting used another backend.
func (c Config) GroqModel(meetingModel string) string {
	if meetingModel == "" || meetingModel == ClaudeModel {
		return c.Model
	}
	return meetingModel
}

// Load reads the TOML file at path on top of the defaults. A missing file is not an error.
func Load(path string) (Config, error) {
	cfg := Default()
	_, err := toml.DecodeFile(path, &cfg)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, fmt.Errorf("read %s: %w", path, err)
	}
	return cfg, nil
}

// Apply returns the configuration with the flags on top.
func (c Config) Apply(o Overrides) Config {
	if o.Transcriber != "" {
		c.Transcriber = o.Transcriber
	}
	if o.NoLive {
		c.Live = false
	}
	if o.Lang != "" {
		c.Lang = o.Lang
	}
	if o.Model != "" {
		c.Model = o.Model
	}
	if o.Echo != "" {
		c.Echo = o.Echo
	}
	if o.KeepAudio {
		c.KeepAudio = true
	}
	return c
}

// Validate returns an error for a value that oat cannot use.
func (c Config) Validate() error {
	switch c.Transcriber {
	case "claude", "groq":
	default:
		return fmt.Errorf("transcriber must be claude or groq, not %q", c.Transcriber)
	}
	switch c.Echo {
	case "auto", "on", "off":
	default:
		return fmt.Errorf("echo must be auto, on, or off, not %q", c.Echo)
	}
	if c.Lang == "" {
		return errors.New("lang must not be empty")
	}
	if c.Model == "" {
		return errors.New("model must not be empty")
	}
	return nil
}
