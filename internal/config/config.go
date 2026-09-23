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
	Lang      string `toml:"lang"`
	Model     string `toml:"model"`
	Echo      string `toml:"echo"`
	KeepAudio bool   `toml:"keep_audio"`
}

// Overrides holds the command flags. An empty string or false keeps the file value.
type Overrides struct {
	Lang      string
	Model     string
	Echo      string
	KeepAudio bool
}

// Default returns the configuration that oat uses without a file.
func Default() Config {
	return Config{Lang: "pt", Model: "whisper-large-v3-turbo", Echo: "auto"}
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
