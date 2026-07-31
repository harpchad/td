// Package web serves the browser UI: server-rendered Go templates plus htmx,
// one stylesheet, no build step. It is server-only.
//
// The visual system is not defined here. `tokens.css` is the system and
// `themes.css` holds the palettes; both are embedded verbatim and served as
// part of one stylesheet. When this code and those files disagree, the files
// win, the same way testdata/ wins elsewhere.
package web

import (
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static/htmx.min.js static/td.js static/app.css
var staticFS embed.FS

// tokensCSS and themesCSS are the two files that define the look. They live
// at the repository root because they are the specification, not an
// implementation detail of this package.
//
//go:embed all:css
var cssFS embed.FS

// Assets holds the composed stylesheet and script, and the hashes the CSP
// needs.
type Assets struct {
	// CSS is tokens, themes, user themes, and the app layer, concatenated.
	// One stylesheet from the browser's point of view, which is what section
	// 12 asks for, assembled from the files that are the authority.
	CSS []byte
	// Script is the vendored htmx bundle plus the keymap.
	HTMX   []byte
	Script []byte

	// Themes is every palette that cleared the contrast floor.
	Themes []Theme
}

var (
	assetsOnce sync.Once
	assets     *Assets
)

// BuiltInThemes returns the two palettes that ship inside the binary. A
// dropped-in theme that fails the contrast floor falls back to one of these,
// so they are the ones that cannot be rejected.
func BuiltInThemes() []Theme {
	tokens := mustRead("css/tokens.css")

	light := parseRootTheme(tokens)
	light.Label = "Light"

	dark := Theme{Name: "dark", Label: "Dark", Dim: light.Dim, BuiltIn: true}
	for _, t := range ParseThemes(tokens) {
		if t.Name == "dark" {
			// The dark block overrides only some tokens, so it starts from
			// the light palette and takes what it redefines.
			dark = t
			dark.Label = "Dark"
			dark.BuiltIn = true
			if dark.Dim == 0 {
				dark.Dim = light.Dim
			}
		}
	}
	return []Theme{light, dark}
}

// Load assembles the assets once. userThemeDir is scanned for extra palettes;
// an empty path skips it.
func Load(userThemeDir string, log *slog.Logger) *Assets {
	assetsOnce.Do(func() {
		assets = build(userThemeDir, log)
	})
	return assets
}

func build(userThemeDir string, log *slog.Logger) *Assets {
	if log == nil {
		log = slog.Default()
	}

	tokens := mustRead("css/tokens.css")
	themes := mustRead("css/themes.css")
	app := mustReadStatic("static/app.css")

	// Auto is the default and the first entry in the picker. It sets no
	// data-theme attribute, which is what lets the media query apply.
	all := []Theme{{Name: ThemeAuto, Label: "Auto, match the system", BuiltIn: true}}
	all = append(all, BuiltInThemes()...)
	for _, t := range ParseThemes(themes) {
		if err := t.CheckContrast(); err != nil {
			// Fail the check, log the theme name, fall back to the built-in.
			log.Warn("theme rejected", "err", err)
			continue
		}
		all = append(all, t)
	}

	extra, extraCSS := loadUserThemes(userThemeDir, log)
	all = append(all, extra...)

	var css strings.Builder
	css.WriteString(tokens)
	css.WriteString("\n")
	// Follow the system when nothing has been picked. Generated from the
	// dark block above rather than written twice.
	css.WriteString(AutoDarkCSS(tokens))
	css.WriteString("\n")
	css.WriteString(themes)
	css.WriteString("\n")
	css.WriteString(extraCSS)
	css.WriteString("\n")
	css.WriteString(app)

	htmx := mustReadStatic("static/htmx.min.js")
	keymap := mustReadStatic("static/td.js")

	return &Assets{
		CSS:    []byte(css.String()),
		HTMX:   []byte(htmx),
		Script: []byte(keymap),
		Themes: all,
	}
}

// loadUserThemes reads $XDG_CONFIG_HOME/td/themes/*.css, so a palette you
// like is a file drop rather than a pull request.
func loadUserThemes(dir string, log *slog.Logger) ([]Theme, string) {
	if dir == "" {
		return nil, ""
	}
	matches, err := filepath.Glob(filepath.Join(dir, "*.css"))
	if err != nil {
		log.Warn("scanning user themes", "dir", dir, "err", err)
		return nil, ""
	}
	sort.Strings(matches)

	var kept []Theme
	var css strings.Builder
	for _, path := range matches {
		body, err := os.ReadFile(path)
		if err != nil {
			log.Warn("reading theme", "path", path, "err", err)
			continue
		}
		found := ParseThemes(string(body))
		if len(found) == 0 {
			log.Warn("theme file defines no [data-theme] block", "path", path)
			continue
		}
		for _, t := range found {
			if err := t.CheckContrast(); err != nil {
				log.Warn("theme rejected", "path", path, "err", err)
				continue
			}
			kept = append(kept, t)
			css.WriteString(t.CSS)
			css.WriteString("\n")
		}
	}
	return kept, css.String()
}

// SRIHash returns the base64 SHA-256 of an asset, in the form a CSP
// script-src hash source takes.
func SRIHash(body []byte) string {
	sum := sha256.Sum256(body)
	return "sha256-" + base64.StdEncoding.EncodeToString(sum[:])
}

func mustRead(name string) string {
	body, err := cssFS.ReadFile(name)
	if err != nil {
		panic(fmt.Sprintf("web: embedded %s: %v", name, err))
	}
	return string(body)
}

func mustReadStatic(name string) string {
	body, err := staticFS.ReadFile(name)
	if err != nil {
		panic(fmt.Sprintf("web: embedded %s: %v", name, err))
	}
	return string(body)
}
