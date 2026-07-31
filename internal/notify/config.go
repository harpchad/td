package notify

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// ServerConfig is the server's config.toml.
//
// Section 14: config resolves in the order flags, then environment, then
// $XDG_CONFIG_HOME/td/config.toml, and a commented default is written on
// first start if none exists.
type ServerConfig struct {
	Notify Policy `toml:"notify"`
}

// DefaultConfigFile is written on first start. It documents the policy in
// place rather than in a manual, because the one thing anyone edits here is
// the notify block.
const DefaultConfigFile = `# td server config.
# Resolution order is flags, then environment, then this file.

[notify]
# Where reminders go. Empty turns them off, which is the default: a server
# that has not been told where to push does not guess.
topic = ""

# Resolves notify = "auto" on a task. This is a filter query, the same
# grammar as the filter bar: "*" for always on, "" for always off,
# "p:<=2 -#someday" for anything in between.
default_rule = "p:<=2"

# How long before a datetime due the push fires. A date-only due ignores
# this and fires at date_only_at instead, because a date is not an instant.
lead_minutes = 30

# Pushes that would land in this window are held until it opens. They are
# held, not dropped.
quiet_hours = "22:00-06:00"

# When a date-only due fires on its day.
date_only_at = "08:00"

# The token the Done and Snooze buttons authenticate with. Mint one with
# ` + "`tdd token create -name ntfy -scopes write`" + ` and paste it here.
# Without it the notification is a click-through with no buttons.
action_token = ""
`

// LoadServerConfig reads config.toml, writing a commented default first if
// none exists. An empty path skips the file entirely.
func LoadServerConfig(path string) (ServerConfig, error) {
	cfg := ServerConfig{Notify: DefaultPolicy}
	if path == "" {
		return cfg, nil
	}

	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return cfg, err
		}
		if err := os.WriteFile(path, []byte(DefaultConfigFile), 0o600); err != nil {
			return cfg, err
		}
		return cfg, nil
	} else if err != nil {
		return cfg, err
	}

	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return cfg, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := cfg.Notify.Validate(); err != nil {
		return cfg, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}
