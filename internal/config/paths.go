package config

import (
	"os"
	"path/filepath"
)

// Paths holds the folders and files that oat reads and writes.
type Paths struct {
	Data   string // $XDG_DATA_HOME/oat
	State  string // $XDG_STATE_HOME/oat
	Config string // $XDG_CONFIG_HOME/oat/config.toml
}

// DefaultPaths resolves the XDG base directories.
func DefaultPaths() (Paths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Paths{}, err
	}
	xdg := func(env, fallback string) string {
		if v := os.Getenv(env); v != "" {
			return v
		}
		return filepath.Join(home, fallback)
	}
	return Paths{
		Data:   filepath.Join(xdg("XDG_DATA_HOME", ".local/share"), "oat"),
		State:  filepath.Join(xdg("XDG_STATE_HOME", ".local/state"), "oat"),
		Config: filepath.Join(xdg("XDG_CONFIG_HOME", ".config"), "oat", "config.toml"),
	}, nil
}

// Meetings is the folder that holds one folder for each meeting.
func (p Paths) Meetings() string { return filepath.Join(p.Data, "meetings") }

// EchoState is the file that records a loaded echo-cancel module.
func (p Paths) EchoState() string { return filepath.Join(p.State, "echo.json") }
