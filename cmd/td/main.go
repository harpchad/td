// Command td is the client: one-shot CLI commands now, a TUI from phase 3.
// It talks to the server over HTTP only and never opens the database file.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/harpchad/td/internal/api"
	"github.com/harpchad/td/internal/client"
	"github.com/harpchad/td/internal/query"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "td:", err)
		os.Exit(1)
	}
}

const usage = `td - task manager

  td                   open the TUI
  td a <text>          add a task; #tag @person p:2 due:friday are read inline
  td sub <ref> <text>  add a subtask under an existing task
  td ls [filter]       list tasks in the default order
  td show <ref>        show one task
  td edit <ref>        change fields: -title -notes -priority -due -start -tags -notify
  td note <ref> <text> append a note, or -replace it, or -show it
  td snooze <ref> <t>  hide it for a while: 1h, 2d, friday
  td done <ref>        complete a task
  td drop <ref>        drop a task
  td repeat <ref> [rule]  make it recur: every monday, every 2 weeks, monthly on the 1st
  td attach <ref> <file>  attach a file; -list, -get <id>, -rm <id>
  td triage            work the inbox one task at a time
  td undo              reverse your last change
  td people            list people
  td person <handle>   the person page: what they owe you, what you owe them
  td filters           list saved filters
  td whoami            show which credential is in use and what it may do
  td flush             send anything queued while offline

<ref> is a task number or a ULID. Every command takes --json.
`

func run(args []string) error {
	var cmd string
	rest := args
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, rest = args[0], args[1:]
	}

	switch cmd {
	case "-h", "--help", "help":
		fmt.Print(usage)
		return nil
	case "--version", "version":
		fmt.Println(api.Version)
		return nil
	}

	cfg, err := client.LoadConfig()
	if err != nil {
		return err
	}
	c := client.New(cfg)

	// The TUI runs until the user quits, so it gets a cancellable context
	// rather than the one-shot commands' deadline.
	if cmd == "" {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		return runTUI(ctx, c, cfg, rest)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	switch cmd {
	case "a", "add":
		return add(ctx, c, rest)
	case "sub":
		return sub(ctx, c, rest)
	case "repeat", "recur":
		return repeat(ctx, c, rest)
	case "attach":
		return attach(ctx, c, rest)
	case "triage":
		// Triage runs until you quit, so it gets the TUI's cancellable
		// context rather than the one-shot deadline.
		tctx, tcancel := context.WithCancel(context.Background())
		defer tcancel()
		return runTUI(tctx, c, cfg, append([]string{"-triage"}, rest...))
	case "ls", "list":
		return list(ctx, c, rest)
	case "show":
		return show(ctx, c, rest)
	case "edit":
		return edit(ctx, c, rest)
	case "note":
		return note(ctx, c, rest)
	case "snooze":
		return snooze(ctx, c, rest)
	case "done":
		return done(ctx, c, rest)
	case "drop":
		return drop(ctx, c, rest)
	case "undo":
		return undo(ctx, c, rest)
	case "people":
		return people(ctx, c, rest)
	case "person":
		return person(ctx, c, rest)
	case "filters":
		return filters(ctx, c, rest)
	case "whoami":
		return whoami(ctx, c, rest)
	case "flush":
		return flush(ctx, c)
	default:
		return fmt.Errorf("unknown command %q, try `td help`", cmd)
	}
}

// add captures a task. It exits immediately whether or not the server is
// reachable: quick-add has to be faster than opening the app or it does not
// get used, and a capture that fails because a container is restarting is a
// capture you lose.
func add(ctx context.Context, c *client.Client, args []string) error {
	fs := flag.NewFlagSet("a", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print the created task as JSON")
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	line := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if line == "" {
		return errors.New("say what to add")
	}

	// Inline dates resolve against the server's clock, not the laptop's:
	// "due:friday" has to mean the same day whichever client typed it.
	// Offline, the local clock stands in, which is the best available answer
	// and is recorded with the queued capture either way.
	if err := c.SyncClock(ctx); err != nil && !errors.Is(err, client.ErrOffline) {
		return err
	}

	cap := query.ParseCapture(line, c.Now())
	if cap.Title == "" {
		return errors.New("that is all tags and no task")
	}

	in := api.TaskCreate{
		ID:       newID(),
		Title:    cap.Title,
		Priority: cap.Priority,
		DueAt:    cap.Due,
		StartAt:  cap.Start,
		Tags:     cap.Tags,
		People:   cap.People,
	}

	task, err := c.Create(ctx, in)
	if errors.Is(err, client.ErrOffline) {
		if qerr := client.Enqueue(in); qerr != nil {
			return qerr
		}
		fmt.Printf("queued  %s\n", in.Title)
		fmt.Fprintln(os.Stderr, "td: offline, queued locally. `td flush` sends it.")
		return nil
	}
	if err != nil {
		return err
	}

	if *asJSON {
		return printJSON(task)
	}
	fmt.Printf("%-4d %s  %s\n", task.Num, task.Status, task.Title)
	return nil
}

func list(ctx context.Context, c *client.Client, args []string) error {
	fs := flag.NewFlagSet("ls", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print the list as JSON")
	limit := fs.Int("limit", 0, "stop after N tasks")
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	filter := strings.TrimSpace(strings.Join(fs.Args(), " "))

	// Parse locally first so a typo reports its own message without a round
	// trip. Same grammar, same parser, so the server cannot disagree.
	if _, err := query.Parse(filter); err != nil {
		return err
	}

	out, err := c.List(ctx, filter, *limit)
	if err != nil {
		return err
	}
	if *asJSON {
		return printJSON(out)
	}
	if len(out.Tasks) == 0 {
		if filter == "" {
			fmt.Println("Nothing here yet. `td a \"something\"` puts the first thing in the inbox.")
		} else {
			fmt.Printf("Nothing matches `%s`.\n", filter)
		}
		return nil
	}
	// Sort order and display order are not the same thing: a subtask is
	// lifted out of its sorted position and drawn under its parent.
	now := c.Now()
	for _, row := range query.Arrange(out.Tasks) {
		fmt.Println(strings.Repeat("  ", row.Depth) + formatRow(row.Task, now))
	}
	return nil
}

func show(ctx context.Context, c *client.Client, args []string) error {
	fs := flag.NewFlagSet("show", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print the task as JSON")
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("show takes one task number or id")
	}
	task, err := c.Get(ctx, fs.Arg(0))
	if err != nil {
		return err
	}
	if *asJSON {
		return printJSON(task)
	}
	printDetail(task)
	return nil
}

func done(ctx context.Context, c *client.Client, args []string) error {
	fs := flag.NewFlagSet("done", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print the result as JSON")
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return errors.New("done takes at least one task number or id")
	}
	for _, ref := range fs.Args() {
		res, err := c.Complete(ctx, ref)
		if err != nil {
			return err
		}
		if *asJSON {
			if err := printJSON(res); err != nil {
				return err
			}
			continue
		}
		fmt.Printf("done  %d  %s\n", res.Task.Num, res.Task.Title)
		if res.ChildrenOpen > 0 {
			fmt.Printf("      %d subtask(s) still open. The parent is the commitment; finish or drop them separately.\n",
				res.ChildrenOpen)
		}
	}
	return nil
}

func drop(ctx context.Context, c *client.Client, args []string) error {
	fs := flag.NewFlagSet("drop", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print the task as JSON")
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return errors.New("drop takes at least one task number or id")
	}
	for _, ref := range fs.Args() {
		task, err := c.Drop(ctx, ref)
		if err != nil {
			return err
		}
		if *asJSON {
			if err := printJSON(task); err != nil {
				return err
			}
			continue
		}
		fmt.Printf("dropped  %d  %s\n", task.Num, task.Title)
	}
	return nil
}

func undo(ctx context.Context, c *client.Client, args []string) error {
	fs := flag.NewFlagSet("undo", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print the result as JSON")
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	res, err := c.Undo(ctx)
	if err != nil {
		return err
	}
	if *asJSON {
		return printJSON(res)
	}
	if res.Task != nil {
		fmt.Printf("undid %s  %d  %s\n", res.Kind, res.Task.Num, res.Task.Title)
	} else {
		fmt.Printf("undid %s\n", res.Kind)
	}
	return nil
}

func people(ctx context.Context, c *client.Client, args []string) error {
	fs := flag.NewFlagSet("people", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print people as JSON")
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	list, err := c.People(ctx)
	if err != nil {
		return err
	}
	if *asJSON {
		return printJSON(list)
	}
	for _, p := range list {
		fmt.Printf("@%-12s %s\n", p.Handle, p.Name)
	}
	return nil
}

func filters(ctx context.Context, c *client.Client, args []string) error {
	fs := flag.NewFlagSet("filters", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print filters as JSON")
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	list, err := c.Filters(ctx)
	if err != nil {
		return err
	}
	if *asJSON {
		return printJSON(list)
	}
	for _, f := range list {
		fmt.Printf("%d  %-12s %s\n", f.Slot, f.Name, f.Query)
	}
	return nil
}

func whoami(ctx context.Context, c *client.Client, args []string) error {
	fs := flag.NewFlagSet("whoami", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print as JSON")
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	me, err := c.WhoAmI(ctx)
	if err != nil {
		return err
	}
	if *asJSON {
		return printJSON(me)
	}
	if me.Username != "" {
		fmt.Printf("token   %s\n", me.Username)
	}
	fmt.Printf("kind    %s\nactor   %s\nscopes  %s\n",
		me.Kind, me.Actor, strings.Join(me.Scopes, ", "))
	return nil
}

func flush(ctx context.Context, c *client.Client) error {
	depth, err := client.QueueDepth()
	if err != nil {
		return err
	}
	if depth == 0 {
		fmt.Println("Nothing queued.")
		return nil
	}
	sent, err := c.Flush(ctx)
	if err != nil {
		fmt.Printf("sent %d of %d\n", sent, depth)
		return err
	}
	fmt.Printf("sent %d\n", sent)
	return nil
}

// person renders the screen you open before a 1:1, in section 5's order.
func person(ctx context.Context, c *client.Client, args []string) error {
	fs := flag.NewFlagSet("person", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print the page as JSON")
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("person takes one handle or id")
	}

	page, err := c.PersonPage(ctx, strings.TrimPrefix(fs.Arg(0), "@"))
	if err != nil {
		return err
	}
	if *asJSON {
		return printJSON(page)
	}

	now := c.Now()
	fmt.Printf("%s  @%s\n", page.Person.Name, page.Person.Handle)
	if len(page.Identities) > 0 {
		var ids []string
		for _, i := range page.Identities {
			ids = append(ids, i.Source+":"+i.ExternalID)
		}
		fmt.Printf("also  %s\n", strings.Join(ids, "  "))
	}

	section := func(title string, tasks []api.Task) {
		fmt.Printf("\n%s\n", title)
		if len(tasks) == 0 {
			fmt.Println("  nothing")
			return
		}
		for _, t := range tasks {
			fmt.Println("  " + formatRow(t, now))
		}
	}

	section("assigned to them", page.Assigned)
	section("you owe them", page.Owed)

	// Waiting carries its age, which is what makes the section actionable.
	fmt.Printf("\nwaiting on them\n")
	if len(page.Waiting) == 0 {
		fmt.Println("  nothing")
	}
	for i, t := range page.Waiting {
		days := 0
		if i < len(page.WaitingDays) {
			days = page.WaitingDays[i]
		}
		fmt.Printf("  %s  %d days\n", formatRow(t, now), days)
	}

	section("agenda", page.Agenda)
	section("involved", page.Involved)
	if len(page.Groups) > 0 {
		section(strings.Join(page.Groups, ", "), page.GroupTasks)
	}
	return nil
}
