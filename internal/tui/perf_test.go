package tui_test

import (
	"testing"
	"time"

	"github.com/harpchad/td/internal/tui"
)

// TestKeystrokeToRedrawIsUnder16ms covers the performance target for the TUI:
// a keystroke on a cached list has to redraw inside one frame at 60Hz.
//
// It measures the part that is actually on that path, which is the cursor
// move and the render. Anything that talks to the server is a command and
// runs off the update loop, so it is not what this budget is about.
func TestKeystrokeToRedrawIsUnder16ms(t *testing.T) {
	f := newFake(t)
	h := newHarness(t, f, tui.Options{Mouse: true})

	// Warm the render path so the first allocation is not what gets timed.
	for range 5 {
		h.key("j")
		_ = h.view()
	}

	const samples = 50
	worst := time.Duration(0)
	total := time.Duration(0)

	for i := range samples {
		key := "j"
		if i%2 == 1 {
			key = "k"
		}
		start := time.Now()
		h.model.Update(keyPress(key))
		_ = h.model.View()
		elapsed := time.Since(start)

		total += elapsed
		worst = max(worst, elapsed)
	}

	mean := total / samples
	t.Logf("keystroke to redraw: mean %s, worst %s over %d samples", mean, worst, samples)

	if worst > 16*time.Millisecond {
		t.Errorf("worst keystroke to redraw was %s, over the 16ms budget", worst)
	}
}

// TestRedrawScalesToAFullScreen checks the same budget with the viewport full
// rather than with the eight fixture rows, since rendering is per visible row.
func TestRedrawScalesToAFullScreen(t *testing.T) {
	f := newFake(t)
	h := newHarness(t, f, tui.Options{Mouse: true})

	// A tall terminal draws every fixture row plus the blank filler.
	h.send(windowSize(200, 60))
	for range 5 {
		_ = h.view()
	}

	start := time.Now()
	for range 20 {
		h.model.Update(keyPress("j"))
		_ = h.model.View()
	}
	mean := time.Since(start) / 20

	t.Logf("keystroke to redraw at 200x60: mean %s", mean)
	if mean > 16*time.Millisecond {
		t.Errorf("mean keystroke to redraw was %s, over the 16ms budget", mean)
	}
}
