package web_test

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/harpchad/td/internal/api"
	"github.com/harpchad/td/internal/web"
)

// TestEmbeddedCSSMatchesTheAuthority guards a copy.
//
// tokens.css and themes.css at the repository root are the authority:
// CLAUDE.md names them as outranking the prose. go:embed cannot reach outside
// a package directory, so internal/web/css holds mirrors. This test is what
// makes editing one and not the other a build failure rather than a slow
// divergence nobody notices until the mockup and the app disagree.
func TestEmbeddedCSSMatchesTheAuthority(t *testing.T) {
	for _, name := range []string{"tokens.css", "themes.css"} {
		t.Run(name, func(t *testing.T) {
			authority, err := os.ReadFile(filepath.Join("..", "..", name))
			if err != nil {
				t.Fatal(err)
			}
			mirror, err := os.ReadFile(filepath.Join("css", name))
			if err != nil {
				t.Fatal(err)
			}
			if string(authority) != string(mirror) {
				t.Errorf("internal/web/css/%s has drifted from the copy at the repository root.\n"+
					"The root file is the authority. Run `make sync-css`.", name)
			}
		})
	}
}

// TestVendoredHTMXIsIntact checks that the bundle is what it was when it was
// vendored. A dependency you cannot verify is a dependency you are trusting
// on the strength of its filename.
func TestVendoredHTMXIsIntact(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("static", "htmx.min.js"))
	if err != nil {
		t.Fatal(err)
	}
	if len(body) < 10_000 {
		t.Fatalf("htmx.min.js is %d bytes, which is not the bundle", len(body))
	}
	// The recorded hash is in DEPENDENCIES.md; this is the same value.
	const want = "sha256-YCMa5rqds4JesVomESLV9VkhxNU7Zr9jfcGLTuJ8efk="
	if got := web.SRIHash(body); got != want {
		t.Errorf("htmx.min.js hash = %s, want %s. The vendored bundle changed.", got, want)
	}
}

// TestAssetVersionTracksTheAssets covers the cache key. The stylesheet and
// the scripts are served immutable with a one year max-age, so a version that
// did not move with them would leave a returning browser on a stale
// stylesheet indefinitely. It was the API version, which changes for
// unrelated reasons and might not change at all.
func TestAssetVersionTracksTheAssets(t *testing.T) {
	a := web.Load("", slog.New(slog.DiscardHandler))
	if a.Version == "" {
		t.Fatal("no asset version")
	}
	if a.Version == api.Version {
		t.Error("the asset version is the API version, which does not move when the CSS does")
	}

	// The same inputs give the same key, or every deploy busts every cache.
	if again := web.Load("", slog.New(slog.DiscardHandler)); again.Version != a.Version {
		t.Errorf("version is not stable: %s then %s", a.Version, again.Version)
	}

	// And different content gives a different key.
	other := web.SRIHash(append(a.CSS, '\n'))
	if other == web.SRIHash(a.CSS) {
		t.Error("the hash does not distinguish different content")
	}
}
