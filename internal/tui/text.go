package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/oklog/ulid/v2"

	"github.com/charmbracelet/x/ansi"
)

// newID generates the ULID a capture carries. The client generates it so
// quick-add returns before the server answers.
func newID() string { return ulid.Make().String() }

// truncate cuts a styled string to a display width, counting cells rather
// than bytes and leaving the escape sequences intact.
func truncate(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= width {
		return s
	}
	return ansi.Truncate(s, width, "…")
}

// pad brings a styled string up to an exact display width, and cuts it if it
// overflows. Everything lands on a character grid: a row that is one cell
// wide breaks the box border for every row below it.
func pad(s string, width int) string {
	s = truncate(s, width)
	if gap := width - lipgloss.Width(s); gap > 0 {
		return s + strings.Repeat(" ", gap)
	}
	return s
}

// padStyled left-aligns a styled string in a fixed column.
func padStyled(s string, width int) string {
	if width <= 0 {
		return ""
	}
	return pad(s, width)
}

// padLeftStyled right-aligns a styled string in a fixed column, which is what
// makes a column of dates line up on its right edge.
func padLeftStyled(s string, width int) string {
	if width <= 0 {
		return ""
	}
	s = truncate(s, width)
	if gap := width - lipgloss.Width(s); gap > 0 {
		return strings.Repeat(" ", gap) + s
	}
	return s
}

// padRight pads an unstyled string to a fixed column width.
func padRight(s string, width int) string {
	if len(s) >= width {
		return s
	}
	return s + strings.Repeat(" ", width-len(s))
}
