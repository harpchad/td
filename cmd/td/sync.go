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
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("sync takes a source: planner")
	}

	switch fs.Arg(0) {
	case planner.Source:
		return syncPlanner(ctx, c, cfg, *asJSON, *dryRun)
	default:
		return fmt.Errorf("no plugin for %q, try: planner", fs.Arg(0))
	}
}

func syncPlanner(ctx context.Context, c *client.Client, cfg client.Config, asJSON, dryRun bool) error {
	pc := cfg.Planner
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
	return nil
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
	items := planner.Translate(tasks, users, graph.Config.TaskURLTemplate, loc)

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
