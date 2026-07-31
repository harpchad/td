package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/harpchad/td/internal/api"
	"github.com/harpchad/td/internal/client"
	"github.com/harpchad/td/internal/query"
)

// splitRef takes the task reference off the front of an argument list.
//
// Go's flag package stops parsing at the first non-flag argument, so
// `td edit 103 -title x` would otherwise leave every flag unparsed and
// `td note 103 -show` would append the literal string "-show" as a note.
// The reference is positional and comes first, which is how these commands
// read, so it is removed before the flags are parsed.
func splitRef(args []string) (ref string, rest []string, err error) {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		// No reference, but there may be -h to answer.
		return "", args, errNoRef
	}
	return args[0], args[1:], nil
}

var errNoRef = errors.New("say which task, by number or id")

// edit changes fields on an existing task.
//
// Every flag is a pointer in the patch, so a flag you did not pass leaves its
// field alone and an empty value clears it. That is the same distinction the
// API makes between an absent key and an explicit null, and it is why -due ""
// removes a due date rather than being ignored.
func edit(ctx context.Context, c *client.Client, args []string) error {
	ref, rest, refErr := splitRef(args)

	fs := flag.NewFlagSet("edit", flag.ContinueOnError)
	title := fs.String("title", "", "replace the title")
	notes := fs.String("notes", "", "replace the notes")
	priority := fs.String("priority", "", `1 to 4, or "" to clear`)
	due := fs.String("due", "", `a date or keyword like friday, or "" to clear`)
	start := fs.String("start", "", `hide until this date, or "" to clear`)
	tags := fs.String("tags", "", "replace the tags, comma separated")
	notifyMode := fs.String("notify", "", "auto, on, or off")
	asJSON := fs.Bool("json", false, "print the task as JSON")
	if err := parseArgs(fs, rest); err != nil {
		return err
	}
	if refErr != nil {
		return errors.New("edit takes a task number or id first, then flags. `td edit -h` lists them")
	}
	if err := c.SyncClock(ctx); err != nil {
		return err
	}
	now := c.Now()

	patch := map[string]any{}
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "title":
			patch["title"] = *title
		case "notes":
			patch["notes"] = *notes
		case "notify":
			patch["notify"] = *notifyMode
		case "tags":
			patch["tags"] = splitTags(*tags)
		case "priority":
			patch["priority"] = nil
			if *priority != "" {
				n, err := strconv.Atoi(*priority)
				if err != nil || n < 1 || n > 4 {
					patch["priority"] = "invalid"
					return
				}
				patch["priority"] = n
			}
		case "due":
			patch["due_at"] = resolveDateFlag(*due, now)
		case "start":
			patch["start_at"] = resolveDateFlag(*start, now)
		}
	})

	if patch["priority"] == "invalid" {
		return errors.New("priority must be 1 to 4, or empty to clear")
	}
	if len(patch) == 0 {
		return errors.New("nothing to change. `td edit -h` lists the flags")
	}
	for key, value := range patch {
		if value == errBadDate {
			return fmt.Errorf("could not read the %s date", strings.TrimSuffix(key, "_at"))
		}
	}

	task, err := c.Patch(ctx, ref, patch, "")
	if err != nil {
		return err
	}
	if *asJSON {
		return printJSON(task)
	}
	fmt.Printf("%-4d %s  %s\n", task.Num, task.Status, task.Title)
	return nil
}

// errBadDate marks a date flag that did not resolve, so the caller can name
// which one rather than failing on the first.
const errBadDate = "\x00bad-date"

// resolveDateFlag turns a flag value into what the patch should carry: nil to
// clear, or a resolved date. Keywords work here for the same reason they work
// in the filter bar, which is that there is one date vocabulary.
//
// now is the server's clock. Resolving "friday" against the local one puts
// the task on a different day than the same word typed into the web box
// whenever the two machines disagree about what day it is.
func resolveDateFlag(value string, now time.Time) any {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	resolved, err := query.ResolveDate(value, now)
	if err != nil {
		// Not a keyword or a date: it might still be an instant.
		if _, err := time.Parse(time.RFC3339, value); err == nil {
			return value
		}
		return errBadDate
	}
	return resolved
}

func splitTags(s string) []string {
	out := []string{}
	for _, tag := range strings.Split(s, ",") {
		tag = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(tag), "#"))
		if tag != "" {
			out = append(out, tag)
		}
	}
	return out
}

// note replaces or appends to a task's notes. Appending is the common case:
// a note is usually the next thing you learned, not a correction of the last.
func note(ctx context.Context, c *client.Client, args []string) error {
	ref, rest, refErr := splitRef(args)

	fs := flag.NewFlagSet("note", flag.ContinueOnError)
	replace := fs.Bool("replace", false, "replace the notes instead of appending")
	show := fs.Bool("show", false, "print the notes and exit")
	if err := parseArgs(fs, rest); err != nil {
		return err
	}
	if refErr != nil {
		return errors.New("note takes a task number or id first, then the text")
	}

	task, err := c.Get(ctx, ref)
	if err != nil {
		return err
	}
	if *show {
		if task.Notes == "" {
			fmt.Fprintln(os.Stderr, "td: no notes on that task")
			return nil
		}
		fmt.Println(task.Notes)
		return nil
	}

	text := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if text == "" {
		return errors.New("say what the note is")
	}
	if !*replace && task.Notes != "" {
		text = task.Notes + "\n" + text
	}

	updated, err := c.Patch(ctx, ref, map[string]any{"notes": text}, "")
	if err != nil {
		return err
	}
	fmt.Printf("%-4d %s\n", updated.Num, updated.Title)
	return nil
}

// snooze hides a task until later.
func snooze(ctx context.Context, c *client.Client, args []string) error {
	ref, rest, refErr := splitRef(args)

	fs := flag.NewFlagSet("snooze", flag.ContinueOnError)
	if err := parseArgs(fs, rest); err != nil {
		return err
	}
	if refErr != nil || fs.NArg() != 1 {
		return errors.New(`snooze takes a task and how long, like "td snooze 103 1h" or "td snooze 103 friday"`)
	}
	when := fs.Arg(0)

	req := api.SnoozeRequest{}
	if _, err := time.ParseDuration(when); err == nil {
		req.Duration = when
	} else {
		if err := c.SyncClock(ctx); err != nil {
			return err
		}
		resolved := resolveDateFlag(when, c.Now())
		date, ok := resolved.(string)
		if !ok || date == errBadDate {
			return fmt.Errorf("could not read %q as a duration or a date", when)
		}
		// A bare date means the start of that day.
		req.Until = date + "T00:00:00Z"
	}

	task, err := c.Snooze(ctx, ref, req)
	if err != nil {
		return err
	}
	until := ""
	if task.SnoozeUntil != nil {
		until = *task.SnoozeUntil
	}
	fmt.Printf("%-4d %s  snoozed until %s\n", task.Num, task.Title, until)
	return nil
}
