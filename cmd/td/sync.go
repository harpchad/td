package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/harpchad/td/internal/client"
	"github.com/harpchad/td/internal/sync"
)

// syncCmd asks the server to run a sync plugin now.
//
// The plugin itself lives on the server and runs on its own schedule. This is
// the "do it now" button for somebody at a terminal, and it holds no
// credentials of its own: the Graph connection belongs to the server, which
// is the whole point of it being there.
func syncCmd(ctx context.Context, c *client.Client, args []string) error {
	fs := flag.NewFlagSet("sync", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print the result as JSON")
	relink := fs.Bool("relink", false,
		"re-apply every item instead of skipping what has not changed, which is how newly mapped people get backfilled")
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("sync takes a source: planner")
	}
	source := fs.Arg(0)

	res, err := c.RunSync(ctx, source, *relink)
	if err != nil {
		return err
	}
	if *asJSON {
		return printJSON(res)
	}
	fmt.Printf("%s  %d created  %d updated  %d unchanged  %d gone\n",
		source, res.Created, res.Updated, res.Unchanged, res.Gone)
	reportUnresolved(res.Unresolved)
	return nil
}

// reportUnresolved says which upstream people did not get attached, and gives
// the command that fixes each.
//
// An identity whose name collides with somebody already known is exactly the
// person you care about, and the sync cannot safely guess whether it is them:
// two people called Stacey is ordinary, and merging them is not something you
// can see afterwards by looking at the list. So it asks, once, and the answer
// is permanent. The same list appears on the settings page with a dropdown,
// which is the better place to answer it from.
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

// personMap attaches an upstream account to a person.
//
// Once an identity is mapped the question never comes back: the next sync
// takes the certain path, and every task that account touches lands on the
// right person page.
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
