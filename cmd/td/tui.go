package main

import (
	"context"
	"flag"
	"strings"

	tea "charm.land/bubbletea/v2"
	zone "github.com/lrstanley/bubblezone/v2"

	"github.com/harpchad/td/internal/client"
	"github.com/harpchad/td/internal/query"
	"github.com/harpchad/td/internal/tui"
)

// runTUI starts the terminal UI. It is what `td` with no arguments does.
func runTUI(ctx context.Context, c *client.Client, cfg client.Config, args []string) error {
	fs := flag.NewFlagSet("td", flag.ContinueOnError)
	noMouse := fs.Bool("no-mouse", false,
		"turn off mouse reporting. Capturing the mouse takes the terminal's own text selection away")
	if err := fs.Parse(args); err != nil {
		return err
	}

	filter := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if filter != "" {
		// Same grammar, same parser: a typo reports its own message before
		// the alt screen swallows it.
		if _, err := query.Parse(filter); err != nil {
			return err
		}
	}

	// Mouse on by default, off with the flag or with `mouse = false` in
	// config.toml, for the terminals where losing text selection is not worth
	// the pointer.
	mouse := true
	if cfg.Mouse != nil {
		mouse = *cfg.Mouse
	}
	if *noMouse {
		mouse = false
	}

	// bubblezone needs a global manager before anything marks a region, and
	// it requires the alt screen, which the TUI uses anyway.
	zone.NewGlobal()
	defer zone.Close()

	model := tui.New(ctx, c, tui.Options{Filter: filter, Mouse: mouse})
	_, err := tea.NewProgram(model, tea.WithContext(ctx)).Run()
	return err
}
