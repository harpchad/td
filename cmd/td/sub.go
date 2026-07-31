package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strings"

	"github.com/harpchad/td/internal/api"
	"github.com/harpchad/td/internal/client"
	"github.com/harpchad/td/internal/query"
)

// sub adds a subtask under an existing task.
//
// A separate verb rather than a flag on add, because the parent is the point:
// `td sub 412 "draft the email"` reads as what it does, and the inline
// grammar still applies to everything after the parent.
func sub(ctx context.Context, c *client.Client, args []string) error {
	fs := flag.NewFlagSet("sub", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print the created task as JSON")
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	if fs.NArg() < 2 {
		return errors.New("sub takes a parent task and the subtask's text")
	}

	parent, err := c.Get(ctx, fs.Arg(0))
	if err != nil {
		return err
	}

	if err := c.SyncClock(ctx); err != nil && !errors.Is(err, client.ErrOffline) {
		return err
	}
	line := strings.TrimSpace(strings.Join(fs.Args()[1:], " "))
	cap := query.ParseCapture(line, c.Now())
	if cap.Title == "" {
		return errors.New("that is all tags and no task")
	}

	// Tags are omitted rather than empty when none were typed inline: the
	// server reads that as "inherit the parent's", which is a copy taken at
	// creation and never live.
	in := api.TaskCreate{
		ID:       newID(),
		Title:    cap.Title,
		Priority: cap.Priority,
		DueAt:    cap.Due,
		StartAt:  cap.Start,
		Tags:     cap.Tags,
		People:   cap.People,
		ParentID: &parent.ID,
	}

	task, err := c.Create(ctx, in)
	if err != nil {
		return err
	}
	if *asJSON {
		return printJSON(task)
	}
	fmt.Printf("%-4d %s  %s\n", task.Num, task.Status, task.Title)
	fmt.Printf("     under %d  %s\n", parent.Num, parent.Title)
	return nil
}

// repeat turns a task into a recurring series, or shows and edits the rule of
// one that already is.
//
// Editing an instance edits that instance. This is the separate action that
// edits the series, which is why the rule lives behind its own verb rather
// than behind a flag on `td edit`.
func repeat(ctx context.Context, c *client.Client, args []string) error {
	fs := flag.NewFlagSet("repeat", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print the series as JSON")
	mode := fs.String("mode", "", "fixed, or after_completion to count the interval from when you finish")
	catchup := fs.String("catchup", "", "skip rolls past what you missed and logs it; pile creates the backlog")
	ends := fs.String("until", "", "stop after this date")
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return errors.New("repeat takes a task number or id")
	}

	task, err := c.Get(ctx, fs.Arg(0))
	if err != nil {
		return err
	}

	// With no rule, this is a read: say what the task repeats on.
	if fs.NArg() == 1 && *mode == "" && *catchup == "" && *ends == "" {
		if task.SeriesID == nil {
			fmt.Printf("%d does not repeat. Give it a rule: td repeat %d \"every monday\"\n",
				task.Num, task.Num)
			return nil
		}
		res, err := c.Series(ctx, *task.SeriesID)
		if err != nil {
			return err
		}
		if *asJSON {
			return printJSON(res)
		}
		printSeries(res.Series)
		return nil
	}

	rule, err := query.ParseRecurrence(strings.Join(fs.Args()[1:], " "))
	if err != nil {
		return err
	}

	in := client.Series{
		RRule: rule, Mode: *mode, Catchup: *catchup,
		Template: api.TaskCreate{
			Title:    task.Title,
			Notes:    task.Notes,
			Priority: task.Priority,
			DueAt:    task.DueAt,
			Tags:     task.Tags,
		},
	}
	if *ends != "" {
		in.EndsAt = ends
	}

	// An existing series is edited rather than replaced, so the instance
	// already in the list is left alone.
	var res client.SeriesResult
	if task.SeriesID != nil {
		res, err = c.UpdateSeries(ctx, *task.SeriesID, in)
	} else {
		res, err = c.CreateSeries(ctx, in)
	}
	if err != nil {
		return err
	}
	if *asJSON {
		return printJSON(res)
	}
	printSeries(res.Series)
	if res.Task != nil {
		fmt.Printf("     first    %d  %s\n", res.Task.Num, res.Task.Title)
	}
	return nil
}

func printSeries(s client.Series) {
	fmt.Printf("repeats  %s\n", s.RRule)
	fmt.Printf("     mode     %s\n", s.Mode)
	fmt.Printf("     catchup  %s\n", s.Catchup)
	if s.NextAt != nil {
		fmt.Printf("     next     %s\n", *s.NextAt)
	}
	if s.EndsAt != nil {
		fmt.Printf("     until    %s\n", *s.EndsAt)
	}
	fmt.Printf("     id       %s\n", s.ID)
}
