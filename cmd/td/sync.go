package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/harpchad/td/internal/client"
	"github.com/harpchad/td/internal/plugins/planner"
	"github.com/harpchad/td/internal/sync"
)

// syncCmd runs a sync plugin.
//
// A plugin is a client. It reads the source system, translates, and posts to
// the td API with a scoped token, exactly like every other client: nothing
// here opens the database, and the server decides what a batch is allowed to
// change. That is what makes a plugin something you can write in an afternoon
// without being able to corrupt anything.
func syncCmd(ctx context.Context, c *client.Client, cfg client.Config, args []string) error {
	fs := flag.NewFlagSet("sync", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print the result as JSON")
	dryRun := fs.Bool("n", false, "read and translate, but post nothing")
	relink := fs.Bool("relink", false,
		"re-apply every item instead of skipping what has not changed, which is how newly mapped people get backfilled")
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("sync takes a source: planner")
	}

	switch fs.Arg(0) {
	case planner.Source:
		return syncPlanner(ctx, c, cfg, *asJSON, *dryRun, *relink)
	default:
		return fmt.Errorf("no plugin for %q, try: planner", fs.Arg(0))
	}
}

func syncPlanner(ctx context.Context, c *client.Client, cfg client.Config, asJSON, dryRun, relink bool) error {
	pc := cfg.Planner
	pc.Relink = relink
	if !pc.Enabled() {
		return errors.New("no plans configured: set planner.plans in config.toml")
	}
	if pc.GraphToken == "" && pc.GraphTokenCommand != "" {
		token, err := runTokenCommand(ctx, pc.GraphTokenCommand)
		if err != nil {
			return err
		}
		pc.GraphToken = token
	}
	if pc.GraphToken == "" {
		return errors.New("no Graph token: set planner.graph_token or planner.graph_token_command")
	}

	graph := planner.New(pc)

	// The clock comes off the server, the same as everywhere else, so a due
	// date read out of Planner lands on the day the server would call it.
	if err := c.SyncClock(ctx); err != nil && !errors.Is(err, client.ErrOffline) {
		return err
	}
	now := c.Now()

	if dryRun {
		return previewPlanner(ctx, graph, now.Location(), asJSON)
	}

	res, err := planner.Run(ctx, graph, c, now, now.Location())
	if err != nil {
		return err
	}
	if asJSON {
		return printJSON(res)
	}
	fmt.Printf("planner  %d created  %d updated  %d unchanged  %d gone\n",
		res.Created, res.Updated, res.Unchanged, res.Gone)
	reportUnresolved(res.Unresolved)
	return nil
}

// reportUnresolved says which upstream people did not get attached, and gives
// the command that fixes each.
//
// This is the part that used to be silent. An identity whose name collides
// with somebody already known is exactly the person you care about, and the
// sync cannot safely guess whether it is them: two people called Stacey is
// ordinary, and merging them is not something you can see afterwards by
// looking at the list. So it asks, once, and the answer is permanent.
func reportUnresolved(unresolved []sync.Unresolved) {
	if len(unresolved) == 0 {
		return
	}
	fmt.Fprintf(os.Stderr, "\ntd: %d upstream %s could not be matched to anybody, so those links are missing:\n",
		len(unresolved), plural(len(unresolved), "person", "people"))
	for _, u := range unresolved {
		who := u.Name
		if u.Email != "" {
			who += " <" + u.Email + ">"
		}
		if strings.TrimSpace(who) == "" {
			who = u.SourceUser
		}
		fmt.Fprintf(os.Stderr, "  %s\n      %s\n      td person map <handle> %s %s\n",
			who, u.Reason, u.Source, u.SourceUser)
	}
	fmt.Fprintln(os.Stderr, "\ntd: mapping one is permanent; the next sync takes the certain path.")
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// previewPlanner reads and translates without posting, which is how you check
// a plan id and a token before letting anything write.
func previewPlanner(ctx context.Context, graph *planner.Client, loc *time.Location, asJSON bool) error {
	var tasks []planner.GraphTask
	for _, planID := range graph.Config.PlanIDs {
		read, err := graph.Tasks(ctx, planID)
		if err != nil {
			return err
		}
		tasks = append(tasks, read...)
	}
	users, err := graph.Users(ctx, planner.UserIDs(tasks))
	if err != nil {
		return err
	}
	items := planner.Translate(tasks, users, graph.Config.TaskURLTemplate, loc, graph.Config.Relink)

	if asJSON {
		return printJSON(items)
	}
	for _, item := range items {
		due := ""
		if item.DueAt != nil {
			due = "  due " + *item.DueAt
		}
		fmt.Printf("%-8s %-10s %s%s\n", item.Status, item.ExternalID, item.Title, due)
	}
	fmt.Fprintf(os.Stderr, "td: %d items, nothing posted\n", len(items))
	return nil
}

// runTokenCommand shells out for a short-lived Graph token, so one does not
// have to sit in a file.
func runTokenCommand(ctx context.Context, command string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("planner.graph_token_command failed: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// personMap attaches an upstream account to a person.
//
// It is the answer to what a sync reports. Once an identity is mapped the
// question never comes back: the next sync takes the certain path, and every
// task that account touches lands on the right person page.
func personMap(ctx context.Context, c *client.Client, args []string) error {
	fs := flag.NewFlagSet("person map", flag.ContinueOnError)
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 3 {
		return errors.New("person map takes a handle, a source, and the id in that source")
	}
	handle := strings.TrimPrefix(fs.Arg(0), "@")
	source, externalID := fs.Arg(1), fs.Arg(2)

	if err := c.MapIdentity(ctx, handle, source, externalID); err != nil {
		return err
	}
	fmt.Printf("mapped  %s:%s  to  @%s\n", source, externalID, handle)
	return nil
}
