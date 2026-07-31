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
