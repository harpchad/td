# Do not edit these

`tokens.css` and `themes.css` here are copies. The authority is the pair at
the repository root, which `CLAUDE.md` names as outranking the prose.

They are mirrored here only because `go:embed` cannot reach outside a
package directory. `TestEmbeddedCSSMatchesTheAuthority` compares them byte
for byte, so editing one and not the other fails `make check`.

Run `make sync-css` after changing the originals.
