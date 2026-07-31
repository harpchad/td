package main

import (
	"flag"
	"strings"
)

// parseArgs parses flags wherever they appear, rather than only before the
// first positional argument.
//
// Go's flag package stops at the first non-flag argument, so `td show 103
// -json` leaves -json unparsed and `td ls "is:open" -json` folds it into the
// filter. Both are what a person types. This walks the arguments, separates
// flags from positionals, and hands the flag package the order it wants.
//
// A flag that takes a value consumes the next argument unless it was written
// as -name=value. Whether a flag is boolean comes from the FlagSet itself, so
// this stays correct as flags are added.
func parseArgs(fs *flag.FlagSet, args []string) error {
	var flags, positional []string

	for i := 0; i < len(args); i++ {
		arg := args[i]

		// A bare "-" or anything not starting with one is positional.
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			positional = append(positional, arg)
			continue
		}
		// Everything after "--" is positional, as usual.
		if arg == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}

		flags = append(flags, arg)

		name, _, hasValue := strings.Cut(strings.TrimLeft(arg, "-"), "=")
		if hasValue {
			continue
		}
		f := fs.Lookup(name)
		if f == nil {
			continue // let the flag package report it
		}
		if boolFlag, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && boolFlag.IsBoolFlag() {
			continue
		}
		if i+1 < len(args) {
			i++
			flags = append(flags, args[i])
		}
	}

	return fs.Parse(append(flags, positional...))
}
