// Package client is the td side of the HTTP boundary: config resolution, the
// API client, and the offline queue that lets `td a` return before the server
// has heard about it. It links no database code, no password hashing, and no
// MCP server, which is what keeps a plain `go build` cross-compiling.
package client

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"

	"github.com/harpchad/td/internal/plugins/planner"
)

// Config is the client's half of config.toml.
type Config struct {
	// Server is the base URL of tdd, with no trailing slash.
	Server string `toml:"server"`
	// Token authenticates non-browser clients. Phase 2 issues these; until
	// then the server accepts requests without one on loopback only.
	Token string `toml:"token"`
	// Timezone is the zone dates are read and written in. Empty means the
	// server's.
	Timezone string `toml:"timezone"`
	// Mouse turns TUI mouse reporting off for terminals that handle text
	// selection badly.
	Mouse *bool `toml:"mouse"`
	// Planner configures the Microsoft Planner mirror. It lives in the
	// client's config because a sync plugin is a client: it talks to tdd over
	// HTTP with a scoped token like everything else, and holding the Graph
	// credential next to the server's database is the wrong place for it.
	Planner planner.Config `toml:"planner"`
}

// DefaultConfig is what gets written on first run.
const DefaultConfig = `# td client config.
# Resolution order is flags, then environment, then this file.

# Base URL of the tdd server.
server = "http://127.0.0.1:8080"

# API token, issued by ` + "`tdd token create`" + `. Leave empty on loopback.
token = ""

# Timezone for reading and writing dates. Empty means the server's.
timezone = ""

# Mouse reporting in the TUI. Capturing the mouse takes the terminal's own
# text selection away; most emulators hand it back while shift is held.
# mouse = false

[planner]
# Plans to mirror into td, by id. Empty mirrors nothing, which is the
# default: a plugin that guessed which plans you meant would import
# somebody else's board.
plans = []

# A Microsoft Graph token with Tasks.Read and User.ReadBasic.All. Prefer
# graph_token_command so a short-lived token does not live in a file.
graph_token = ""
# graph_token_command = "az account get-access-token --resource https://graph.microsoft.com --query accessToken -o tsv"

# The deep link a mirrored task carries. %s is the Planner task id.
# Planner's web address has moved once already, so this is configuration.
# task_url_template = "https://tasks.office.com/Home/Task/%s"
`

// ConfigDir resolves $XDG_CONFIG_HOME/td, falling back to ~/.config/td.
//
// Not ~/.td, which the Treasure Data toolbelt already uses.
func ConfigDir() (string, error) {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "td"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "td"), nil
}

// StateDir resolves $XDG_STATE_HOME/td, falling back to ~/.local/state/td. It
// holds the offline queue, which is cache rather than configuration.
func StateDir() (string, error) {
	if dir := os.Getenv("XDG_STATE_HOME"); dir != "" {
		return filepath.Join(dir, "td"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state", "td"), nil
}

// LoadConfig reads config.toml, writing a commented default first if none
// exists. Environment variables override the file, and the caller applies
// flags over the result.
func LoadConfig() (Config, error) {
	var cfg Config

	dir, err := ConfigDir()
	if err != nil {
		return cfg, err
	}
	path := filepath.Join(dir, "config.toml")

	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return cfg, err
		}
		if err := os.WriteFile(path, []byte(DefaultConfig), 0o600); err != nil {
			return cfg, err
		}
	} else if err != nil {
		return cfg, err
	}

	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return cfg, fmt.Errorf("parse %s: %w", path, err)
	}

	if v := os.Getenv("TD_SERVER"); v != "" {
		cfg.Server = v
	}
	if v := os.Getenv("TD_TOKEN"); v != "" {
		cfg.Token = v
	}
	if v := os.Getenv("TD_TIMEZONE"); v != "" {
		cfg.Timezone = v
	}
	if cfg.Server == "" {
		cfg.Server = "http://127.0.0.1:8080"
	}
	return cfg, nil
}
