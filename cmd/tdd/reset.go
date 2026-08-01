package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/harpchad/td/internal/store"
)

// resetTasks is the one hard delete in td.
//
// It exists because testing a sync means running it, looking at what it did,
// and running it again from a known state, and the alternative was deleting
// the database file, which also destroys the account, the tokens, and the
// Microsoft connection somebody just signed in for.
//
// Command line only, never a route. A token cannot reach this, which is the
// property that makes an operator-only wrecking tool acceptable at all.
func resetTasks(ctx context.Context, st *store.Store, dbPath string, args []string, stdin io.Reader, stdout io.Writer) error {
	fs := flag.NewFlagSet("reset tasks", flag.ContinueOnError)
	fs.SetOutput(stdout)
	source := fs.String("source", "", `only tasks from one source, such as "planner". Empty means every task`)
	yes := fs.Bool("yes", false, "skip the confirmation, for a script that already knows")
	if err := fs.Parse(args); err != nil {
		return err
	}

	counts, err := st.CountTasks(ctx, *source)
	if err != nil {
		return err
	}
	if counts == 0 {
		fmt.Fprintln(stdout, "Nothing to remove.")
		return nil
	}

	scope := fmt.Sprintf("all %d tasks", counts)
	if *source != "" {
		scope = fmt.Sprintf("%d tasks mirrored from %s", counts, *source)
	}

	if !*yes {
		// Typed in full rather than y/n. This is the one operation in the
		// product that destroys something, and a reflexive keystroke should
		// not be enough to do it.
		fmt.Fprintf(stdout, `About to permanently delete %s from %s,
along with their tags, people links, attachments and history.

Kept: the account and tokens, people and their identity mappings, saved
filters, and plugin settings and connections.

Type "delete" to go ahead: `, scope, dbPath)

		line, err := bufio.NewReader(stdin).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		if strings.TrimSpace(line) != "delete" {
			fmt.Fprintln(stdout, "\nLeft alone.")
			return nil
		}
	}

	removed, err := st.ResetTasks(ctx, *source, time.Now())
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "\nRemoved %d tasks, %d events and %d attachment records.\n",
		removed.Tasks, removed.Events, removed.Attachments)
	if removed.Attachments > 0 {
		fmt.Fprintln(stdout, "The attachment files themselves go on the next weekly sweep.")
	}
	return nil
}
