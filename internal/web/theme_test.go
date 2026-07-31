package web_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/harpchad/td/internal/web"
)

func readCSS(t *testing.T, name string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "..", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

// TestShippedThemesClearTheContrastFloor is the check section 12 calls a unit
// test rather than a runtime nicety. It runs over the palettes in themes.css,
// which are the ones a user is most likely to pick.
func TestShippedThemesClearTheContrastFloor(t *testing.T) {
	themes := web.ParseThemes(readCSS(t, "themes.css"))
	if len(themes) < 4 {
		t.Fatalf("parsed %d themes out of themes.css, want at least the four it ships", len(themes))
	}

	seen := map[string]bool{}
	for _, theme := range themes {
		seen[theme.Name] = true
		t.Run(theme.Name, func(t *testing.T) {
			if err := theme.CheckContrast(); err != nil {
				t.Error(err)
			}
		})
	}
	for _, want := range []string{"nord", "solarized-light", "dracula", "tokyo-night"} {
		if !seen[want] {
			t.Errorf("themes.css does not define %s", want)
		}
	}
}

// TestBuiltInThemesClearTheFloor covers the two palettes in tokens.css. These
// are what a failing theme falls back to, so they have to be readable or
// there is nothing to fall back to.
func TestBuiltInThemesClearTheFloor(t *testing.T) {
	for _, theme := range web.BuiltInThemes() {
		t.Run(theme.Name, func(t *testing.T) {
			if err := theme.CheckContrast(); err != nil {
				t.Error(err)
			}
		})
	}
}

// TestTheFloorActuallyRejects checks the check. A contrast test that passes
// everything is worse than none, because it reads as coverage.
func TestTheFloorActuallyRejects(t *testing.T) {
	tests := []struct {
		name  string
		theme web.Theme
		want  string
	}{
		{
			name:  "ink too close to paper",
			theme: web.Theme{Name: "mud", Paper: "#777777", Ink: "#888888", Dim: 0.58},
			want:  "ink on paper",
		},
		{
			name: "readable ink but a dim that erases it",
			// Black on white passes the first floor easily. At 0.12 opacity
			// the dimmed text is a pale grey that does not clear the second.
			theme: web.Theme{Name: "faded", Paper: "#ffffff", Ink: "#000000", Dim: 0.12},
			want:  "--td-dim",
		},
		{
			name:  "a colour that does not parse",
			theme: web.Theme{Name: "broken", Paper: "not-a-color", Ink: "#000000", Dim: 0.58},
			want:  "--td-paper",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.theme.CheckContrast()
			if err == nil {
				t.Fatal("the floor accepted an unreadable theme")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to name %q", err, tc.want)
			}
			// The message has to name the theme, since the whole point is to
			// log which file was dropped.
			if !strings.Contains(err.Error(), tc.theme.Name) {
				t.Errorf("error = %q, want it to name the theme", err)
			}
		})
	}
}

// TestLowContrastThemesRaiseDim locks the note in themes.css that Nord and
// Solarized need --td-dim above the 0.58 default. If someone resets them to
// the default, the floor test above catches it, and this says why.
func TestLowContrastThemesRaiseDim(t *testing.T) {
	themes := map[string]web.Theme{}
	for _, theme := range web.ParseThemes(readCSS(t, "themes.css")) {
		themes[theme.Name] = theme
	}

	for _, name := range []string{"nord", "solarized-light"} {
		theme, ok := themes[name]
		if !ok {
			t.Fatalf("no theme %s", name)
		}
		if theme.Dim <= 0.58 {
			t.Errorf("%s has --td-dim %.2f, which is the default. Low contrast palettes need it raised or task numbers fade out",
				name, theme.Dim)
		}
	}
}

// TestAThemeSetsColoursOnly covers the rule that a theme never sets type,
// spacing, radius, or shadow: those belong to the system, not the palette.
func TestAThemeSetsColoursOnly(t *testing.T) {
	structural := []string{
		"--td-font", "--td-size", "--td-weight", "--td-row", "--td-gap",
		"--td-pad", "--td-border", "--td-rule-w", "--td-shadow",
	}

	for _, theme := range web.ParseThemes(readCSS(t, "themes.css")) {
		for _, token := range structural {
			if strings.Contains(theme.CSS, token+":") {
				t.Errorf("theme %s sets %s, which is structural rather than a colour", theme.Name, token)
			}
		}
	}
}

// TestAutoDarkIsDerivedFromTheDarkBlock covers the "follow the system"
// default. The rule is generated from tokens.css rather than written out, so
// there is exactly one dark palette and the media query cannot drift from it.
func TestAutoDarkIsDerivedFromTheDarkBlock(t *testing.T) {
	tokens := readCSS(t, "tokens.css")
	got := web.AutoDarkCSS(tokens)

	if !strings.Contains(got, "@media (prefers-color-scheme: dark)") {
		t.Fatalf("no media query generated:\n%s", got)
	}
	if !strings.Contains(got, ":root:not([data-theme])") {
		t.Error("the rule is not scoped to pages with no theme picked, so it would override an explicit choice")
	}

	// Every value the dark block sets has to appear, or a page following the
	// system gets a half-applied palette.
	var dark web.Theme
	for _, theme := range web.ParseThemes(tokens) {
		if theme.Name == "dark" {
			dark = theme
		}
	}
	if dark.Paper == "" || dark.Ink == "" {
		t.Fatal("tokens.css defines no dark block")
	}
	for _, value := range []string{dark.Paper, dark.Ink} {
		if !strings.Contains(got, value) {
			t.Errorf("the generated rule is missing %s", value)
		}
	}
}

// TestDeEmphasisNeverUsesAFixedGrey covers the rule tokens.css states in its
// own header and then broke: de-emphasis is opacity, never a grey token,
// because a fixed grey is correct on one background and invisible on the
// other.
//
// The status bar was set with color: var(--td-grey), which measures 1.69:1 on
// Nord. --td-grey and --td-grey-faint are for fills that are not text.
func TestDeEmphasisNeverUsesAFixedGrey(t *testing.T) {
	tokens := readCSS(t, "tokens.css")

	for _, line := range strings.Split(tokens, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "*") || strings.HasPrefix(trimmed, "/*") {
			continue
		}
		if !strings.Contains(trimmed, "--td-grey") {
			continue
		}
		// A declaration that paints text.
		for _, property := range []string{"color:", "-color:"} {
			idx := strings.Index(trimmed, property)
			if idx < 0 {
				continue
			}
			// scrollbar-color and border-color paint a rule or a thumb, not
			// text, and those are the fills the token is for.
			if strings.Contains(trimmed, "scrollbar-color") || strings.Contains(trimmed, "border-color") {
				continue
			}
			t.Errorf("tokens.css paints text from a fixed grey, which is unreadable on low-contrast palettes: %s", trimmed)
		}
	}
}

// TestFixedGreysAreOnlyEverFills is the other half: whatever --td-grey is
// used for has to survive every palette, and a fill has no contrast floor.
func TestFixedGreysAreOnlyEverFills(t *testing.T) {
	tokens := readCSS(t, "tokens.css")

	allowed := []string{"background:", "scrollbar-color:"}
	for _, line := range strings.Split(tokens, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.Contains(trimmed, "var(--td-grey") || strings.HasPrefix(trimmed, "*") {
			continue
		}
		if strings.HasPrefix(trimmed, "--td-grey") {
			continue // the declaration itself
		}
		ok := false
		for _, property := range allowed {
			if strings.Contains(trimmed, property) {
				ok = true
			}
		}
		if !ok {
			t.Errorf("--td-grey used somewhere other than a fill: %s", trimmed)
		}
	}
}

// TestControlsInAModalInvertWithIt covers the gap that made the login page's
// inputs invisible.
//
// A modal repaints its surface with --td-surface and --td-surface-ink. Any
// control that draws itself from --td-ink or --td-paper is therefore wrong
// inside one: both are dark in the light theme and both are light in the dark
// theme, so the control disappears into the panel. tokens.css had overrides
// for the button, the toggle, and the link, and none for the input, which is
// the only control on the one screen that is nothing but controls.
//
// The check is per property, not per selector. A first version asked only
// whether a .td-modal rule existed for the class, which the :focus override
// satisfied on its own, so deleting the rule that actually mattered left the
// test green.
func TestControlsInAModalInvertWithIt(t *testing.T) {
	tokens := readCSS(t, "tokens.css")
	controls := []string{"td-btn", "td-toggle", "td-input", "td-check", "td-radio", "td-done"}

	for _, class := range controls {
		t.Run(class, func(t *testing.T) {
			needed := propertiesPaintedFromPageColours(tokens, class)
			if len(needed) == 0 {
				return // draws with currentColor or inherit, so it follows the panel
			}
			override := modalRules(tokens, class)
			for _, property := range needed {
				if !overridesProperty(override, property) {
					t.Errorf(".%s sets %s from --td-ink or --td-paper and no .td-modal rule "+
						"resets it, so it is invisible inside a modal", class, property)
				}
			}
		})
	}
}

// propertiesPaintedFromPageColours lists the declarations in a class's base
// rules whose value comes from the page palette rather than from
// currentColor.
func propertiesPaintedFromPageColours(css, class string) []string {
	var out []string
	seen := map[string]bool{}

	for _, block := range strings.Split(css, "}") {
		selector, body, ok := strings.Cut(block, "{")
		if !ok || strings.Contains(selector, ".td-modal") {
			continue
		}
		if !selectorTargets(selector, class) {
			continue
		}
		for _, decl := range strings.Split(body, ";") {
			property, value, ok := strings.Cut(decl, ":")
			if !ok {
				continue
			}
			if !strings.Contains(value, "var(--td-ink)") && !strings.Contains(value, "var(--td-paper)") {
				continue
			}
			property = strings.TrimSpace(property)
			if !seen[property] {
				seen[property] = true
				out = append(out, property)
			}
		}
	}
	return out
}

// selectorTargets reports whether a selector styles the class itself rather
// than something that merely mentions it.
func selectorTargets(selector, class string) bool {
	for _, part := range strings.Split(selector, ",") {
		part = strings.TrimSpace(part)
		if part == "."+class || strings.HasPrefix(part, "."+class+":") ||
			strings.HasPrefix(part, "."+class+"[") {
			return true
		}
	}
	return false
}

func modalRules(css, class string) string {
	var out strings.Builder
	for _, block := range strings.Split(css, "}") {
		selector, body, ok := strings.Cut(block, "{")
		if ok && strings.Contains(selector, ".td-modal") && strings.Contains(selector, "."+class) {
			out.WriteString(body)
			out.WriteString(";")
		}
	}
	return out.String()
}

// overridesProperty allows a longhand to answer for a shorthand, so
// border-bottom-color satisfies border-bottom.
func overridesProperty(rules, property string) bool {
	for _, decl := range strings.Split(rules, ";") {
		name, _, ok := strings.Cut(decl, ":")
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		if name == property || strings.HasPrefix(name, property+"-") {
			return true
		}
	}
	return false
}

// TestTheFocusedCaretIsVisible covers the one animation in the product. The
// field inverts on focus, so a caret left at --td-ink sits on an --td-ink
// background and cannot be seen.
func TestTheFocusedCaretIsVisible(t *testing.T) {
	tokens := readCSS(t, "tokens.css")

	focus := baseRules(tokens, "td-input:focus")
	if !strings.Contains(focus, "caret-color") {
		t.Error(".td-input:focus inverts the field without inverting the caret, so the caret is invisible while typing")
	}

	modalFocus := ""
	for _, block := range strings.Split(tokens, "}") {
		selector, body, ok := strings.Cut(block, "{")
		if ok && strings.Contains(selector, ".td-modal .td-input:focus") {
			modalFocus = body
		}
	}
	if modalFocus == "" || !strings.Contains(modalFocus, "caret-color") {
		t.Error("a focused input inside a modal has no caret colour of its own")
	}
}

// baseRules returns every rule matching a selector fragment that is not
// itself a modal override, concatenated.
func baseRules(css, fragment string) string {
	var out strings.Builder
	for _, block := range strings.Split(css, "}") {
		selector, body, ok := strings.Cut(block, "{")
		if !ok || strings.Contains(selector, ".td-modal") {
			continue
		}
		if !strings.Contains(selector, "."+fragment) {
			continue
		}
		out.WriteString(body)
		out.WriteString(";")
	}
	return out.String()
}
