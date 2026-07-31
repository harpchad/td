// Package tui is the terminal client: list, detail, quick-add, filters, and
// undo, over the same HTTP API every other client uses. It is client-only and
// never opens the database.
package tui

import "charm.land/lipgloss/v2"

// The TUI renders through the terminal's own ANSI palette and reads no theme
// file. Paper and ink are the terminal's default background and foreground,
// dim is bright black, and the two committed colors are the accent and
// overdue. If you run Tokyo Night in your terminal, the TUI is already Tokyo
// Night, and a theme file would only fight it.
//
// Only ANSI 0-15 appear here on purpose. A 256-color or truecolor value would
// be a color td chose rather than one the terminal did, which is the whole
// thing this design is avoiding.
var (
	ansiRed          = lipgloss.Color("1")
	ansiYellow       = lipgloss.Color("3")
	ansiBrightBlack  = lipgloss.Color("8")
	ansiBrightYellow = lipgloss.Color("11")
)

var (
	// base inherits the terminal's colors and sets nothing.
	base = lipgloss.NewStyle()

	// dim is de-emphasis. Task numbers, tags, people, and chrome rules use
	// it. Bright black is the one palette entry that reads as "quieter than
	// the text" in both light and dark terminals.
	dim = lipgloss.NewStyle().Foreground(ansiBrightBlack)

	// selected is inverse video, never a background tint. Backing off
	// inversion is what turns this into a normal app with a monospace font.
	selected = lipgloss.NewStyle().Reverse(true)

	// overdue is the single exception to de-emphasis-by-opacity: it gets a
	// paired color token because a due date you have missed has to survive
	// being scanned past.
	overdue = lipgloss.NewStyle().Foreground(ansiRed)

	// accent marks the one primary action on a screen and nothing else.
	accent = lipgloss.NewStyle().Foreground(ansiYellow)

	// Priority is encoded in weight and value, never in hue: both color slots
	// are already committed. The ramp is bold, normal, dim, faint, which
	// works unchanged in a terminal.
	priorityStyles = map[int]lipgloss.Style{
		1: lipgloss.NewStyle().Bold(true),
		2: lipgloss.NewStyle(),
		3: lipgloss.NewStyle().Foreground(ansiBrightBlack),
		4: lipgloss.NewStyle().Foreground(ansiBrightBlack).Faint(true),
	}

	// warn is for the offline and error lines in the status bar.
	warn = lipgloss.NewStyle().Foreground(ansiBrightYellow)
)

// priorityStyle returns the ramp entry for a priority, or the plain style for
// an unset one.
func priorityStyle(p *int) lipgloss.Style {
	if p == nil {
		return base
	}
	if s, ok := priorityStyles[*p]; ok {
		return s
	}
	return base
}
