package tui_test

import (
	"slices"
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
//
// At p95, like every other target in section 15, and for the reason those are
// written that way. This asserted on the single worst sample and failed in CI
// at 20ms against a 1.7ms mean, which is a scheduler preemption on a shared
// runner rather than a slow redraw: no change to this code could have made one
// sample in fifty twelve times the cost of the other forty-nine. The absolute
// ceiling below is what still catches a real regression, where the whole
// distribution moves rather than one sample.
func TestKeystrokeToRedrawIsUnder16ms(t *testing.T) {
	f := newFake(t)
	h := newHarness(t, f, tui.Options{Mouse: true})

	// Warm the render path so the first allocation is not what gets timed.
	for range 5 {
		h.key("j")
		_ = h.view()
	}

	const samples = 50
	taken := make([]time.Duration, samples)
	total := time.Duration(0)

	for i := range samples {
		key := "j"
		if i%2 == 1 {
			key = "k"
		}
		start := time.Now()
		h.model.Update(keyPress(key))
		_ = h.model.View()

		taken[i] = time.Since(start)
		total += taken[i]
	}

	slices.Sort(taken)
	mean := total / samples
	p95 := taken[(samples*95)/100]
	worst := taken[samples-1]
	t.Logf("keystroke to redraw: mean %s, p95 %s, worst %s over %d samples", mean, p95, worst, samples)

	if p95 > 16*time.Millisecond {
		t.Errorf("p95 keystroke to redraw was %s, over the 16ms budget", p95)
	}
	// A redraw that is genuinely broken is slow in every sample, so it fails
	// the line above. This one is for the case where it is slow in a way p95
	// hides: a stall long enough that the cursor visibly lags, however rarely.
	if worst > 100*time.Millisecond {
		t.Errorf("worst keystroke to redraw was %s, which is a visible stall", worst)
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
