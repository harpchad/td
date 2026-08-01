package main

// version is the build, injected with -ldflags "-X main.version=...".
//
// It is a variable rather than a constant because that is the only shape the
// linker can write to, and it has a default because a `go build` with no
// flags has to produce something that runs and says so.
//
// The trap this exists to close: `-X` naming a symbol that does not exist is
// silently discarded by the Go linker. The Dockerfile was injecting into a
// variable nobody had declared, so every image reported the same number and
// the CI step that checked it proved only that the binary started.
var version = "dev"
