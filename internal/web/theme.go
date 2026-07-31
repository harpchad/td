package web

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// Contrast floors a theme has to clear before it loads.
//
// Section 12: 4.5:1 for ink on paper, and 3:1 for ink at --td-dim on paper.
// The second one is the reason --td-dim is a token rather than a constant:
// low-contrast palettes need it above the 0.58 default or task numbers and
// due dates fade out entirely.
const (
	MinInkContrast = 4.5
	MinDimContrast = 3.0
)

// Theme is one palette, as parsed out of a CSS file.
type Theme struct {
	// Name is the data-theme value, which is what the picker binds to.
	Name string
	// Label is what the picker shows.
	Label string
	// Paper, Ink, and Dim are the three values the contrast floor needs.
	Paper string
	Ink   string
	Dim   float64
	// CSS is the whole rule, served as-is.
	CSS string
	// BuiltIn marks the themes that ship in the binary, which cannot be
	// rejected: something has to be left to fall back to.
	BuiltIn bool
}

var (
	themeBlockRE = regexp.MustCompile(`(?s)\[data-theme="([a-z0-9-]+)"\]\s*\{(.*?)\}`)
	rootBlockRE  = regexp.MustCompile(`(?s):root\s*,?\s*(?:\[data-theme="light"\])?\s*\{(.*?)\}`)
	declRE       = regexp.MustCompile(`--td-([a-z-]+)\s*:\s*([^;]+);`)
)

// ParseThemes reads every [data-theme="..."] block out of a stylesheet.
func ParseThemes(css string) []Theme {
	var out []Theme
	for _, match := range themeBlockRE.FindAllStringSubmatch(css, -1) {
		name, body := match[1], match[2]
		if name == "light" {
			// The light palette lives on :root, not in a theme block.
			continue
		}
		t := Theme{Name: name, Label: labelFor(name), CSS: match[0], Dim: 0.58}
		applyDecls(&t, body)
		out = append(out, t)
	}
	return out
}

// parseRootTheme reads the default palette off :root, which is what a theme
// that only overrides some tokens inherits.
func parseRootTheme(css string) Theme {
	t := Theme{Name: "light", Label: "Light", Dim: 0.58, BuiltIn: true}
	if m := rootBlockRE.FindStringSubmatch(css); m != nil {
		applyDecls(&t, m[1])
	}
	return t
}

func applyDecls(t *Theme, body string) {
	for _, d := range declRE.FindAllStringSubmatch(body, -1) {
		value := strings.TrimSpace(d[2])
		switch d[1] {
		case "paper":
			t.Paper = value
		case "ink":
			t.Ink = value
		case "dim":
			if f, err := strconv.ParseFloat(value, 64); err == nil {
				t.Dim = f
			}
		}
	}
}

func labelFor(name string) string {
	words := strings.Split(name, "-")
	for i, w := range words {
		if w == "" {
			continue
		}
		words[i] = strings.ToUpper(w[:1]) + w[1:]
	}
	return strings.Join(words, " ")
}

// CheckContrast reports why a theme is unreadable, or nil when it is fine.
//
// A dropped-in palette that fails this is the only thing between a file drop
// and a list you cannot read, which is why the check runs over every shipped
// theme in a test rather than only over user files at startup.
func (t Theme) CheckContrast() error {
	paper, err := parseHex(t.Paper)
	if err != nil {
		return fmt.Errorf("theme %q: --td-paper: %w", t.Name, err)
	}
	ink, err := parseHex(t.Ink)
	if err != nil {
		return fmt.Errorf("theme %q: --td-ink: %w", t.Name, err)
	}

	if got := contrastRatio(ink, paper); got < MinInkContrast {
		return fmt.Errorf("theme %q: ink on paper is %.2f:1, below the %.1f:1 floor",
			t.Name, got, MinInkContrast)
	}

	// De-emphasis is opacity rather than a grey token, so dimmed text is ink
	// composited onto paper at --td-dim. That composite is what has to clear
	// the second floor.
	dimmed := composite(ink, paper, t.Dim)
	if got := contrastRatio(dimmed, paper); got < MinDimContrast {
		return fmt.Errorf("theme %q: ink at --td-dim %.2f is %.2f:1 on paper, below the %.1f:1 floor. Raise --td-dim",
			t.Name, t.Dim, got, MinDimContrast)
	}
	return nil
}

type rgb struct{ r, g, b float64 }

func parseHex(s string) (rgb, error) {
	s = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "#"))
	if len(s) == 3 {
		s = string([]byte{s[0], s[0], s[1], s[1], s[2], s[2]})
	}
	if len(s) != 6 {
		return rgb{}, fmt.Errorf("%q is not a six digit hex color", s)
	}
	v, err := strconv.ParseUint(s, 16, 32)
	if err != nil {
		return rgb{}, fmt.Errorf("%q is not a hex color", s)
	}
	return rgb{
		r: float64((v>>16)&0xff) / 255,
		g: float64((v>>8)&0xff) / 255,
		b: float64(v&0xff) / 255,
	}, nil
}

// composite blends fg over bg at the given alpha, which is what an opacity
// declaration does.
func composite(fg, bg rgb, alpha float64) rgb {
	return rgb{
		r: fg.r*alpha + bg.r*(1-alpha),
		g: fg.g*alpha + bg.g*(1-alpha),
		b: fg.b*alpha + bg.b*(1-alpha),
	}
}

// relativeLuminance is the WCAG 2.1 definition.
func relativeLuminance(c rgb) float64 {
	lin := func(v float64) float64 {
		if v <= 0.04045 {
			return v / 12.92
		}
		return math.Pow((v+0.055)/1.055, 2.4)
	}
	return 0.2126*lin(c.r) + 0.7152*lin(c.g) + 0.0722*lin(c.b)
}

func contrastRatio(a, b rgb) float64 {
	la, lb := relativeLuminance(a), relativeLuminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}
