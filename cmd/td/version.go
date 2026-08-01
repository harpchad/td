package main

// version is the build, injected with -ldflags "-X main.version=...". The
// client releases separately from the server, so the two numbers move apart
// on purpose and the API version in the handshake is what catches it.
var version = "dev"
