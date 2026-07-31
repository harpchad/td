package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/harpchad/td/internal/client"
)

// exportCmd writes the whole database out.
//
// "A task system you cannot get your data out of is a hostage situation."
// JSON is the one that goes back in; markdown is the one you read. Both come
// from the same request, so they cannot disagree about what is in there.
func exportCmd(ctx context.Context, c *client.Client, args []string) error {
	fs := flag.NewFlagSet("export", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "write the whole database as JSON, which imports back")
	asMarkdown := fs.Bool("markdown", false, "write one file per task, for Obsidian")
	out := fs.String("o", "", "write here instead of stdout, or a directory for -markdown")
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	if *asJSON == *asMarkdown {
		return errors.New("export takes --json or --markdown")
	}

	doc, err := c.Export(ctx)
	if err != nil {
		return err
	}

	if *asMarkdown {
		dir := *out
		if dir == "" {
			dir = "td-export"
		}
		return writeMarkdown(doc, dir)
	}

	w := io.Writer(os.Stdout)
	if *out != "" {
		// O_EXCL, because an export that silently replaced yesterday's backup
		// with a half-written one is the worst possible failure here.
		f, err := os.OpenFile(*out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		w = f
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return err
	}
	if *out != "" {
		fmt.Fprintf(os.Stderr, "td: wrote %s\n", *out)
	}
	return nil
}

// importCmd restores an export.
func importCmd(ctx context.Context, c *client.Client, args []string) error {
	fs := flag.NewFlagSet("import", flag.ContinueOnError)
	if err := parseArgs(fs, args); err != nil {
		return err
	}

	r := io.Reader(os.Stdin)
	if fs.NArg() == 1 {
		f, err := os.Open(fs.Arg(0))
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		r = f
	} else if fs.NArg() > 1 {
		return errors.New("import takes one file, or reads stdin")
	}

	var doc json.RawMessage
	if err := json.NewDecoder(r).Decode(&doc); err != nil {
		return fmt.Errorf("reading the export: %w", err)
	}
	res, err := c.Import(ctx, doc)
	if err != nil {
		return err
	}
	fmt.Printf("restored  %d tasks  %d people  %d events\n",
		res["tasks"], res["people"], res["events"])
	return nil
}

// writeMarkdown writes one file per task.
//
// One file per task rather than one big file, because that is what Obsidian
// wants and because a per-task file is what survives being moved around. The
// frontmatter carries the fields; the body carries the notes.
func writeMarkdown(doc client.ExportDoc, dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}

	for _, task := range doc.Tasks {
		var b strings.Builder
		b.WriteString("---\n")
		fmt.Fprintf(&b, "td: %d\n", task.Num)
		fmt.Fprintf(&b, "title: %s\n", yamlString(task.Title))
		fmt.Fprintf(&b, "status: %s\n", task.Status)
		if task.Priority != nil {
			fmt.Fprintf(&b, "priority: %d\n", *task.Priority)
		}
		if task.DueAt != nil && *task.DueAt != "" {
			fmt.Fprintf(&b, "due: %s\n", *task.DueAt)
		}
		if task.CompletedAt != nil && *task.CompletedAt != "" {
			fmt.Fprintf(&b, "completed: %s\n", *task.CompletedAt)
		}
		if len(task.Tags) > 0 {
			fmt.Fprintf(&b, "tags: [%s]\n", strings.Join(task.Tags, ", "))
		}
		for _, p := range task.People {
			fmt.Fprintf(&b, "%s: %s\n", p.Role, yamlString(p.Name))
		}
		if task.ExternalURL != nil && *task.ExternalURL != "" {
			fmt.Fprintf(&b, "source_url: %s\n", *task.ExternalURL)
		}
		fmt.Fprintf(&b, "created: %s\n", task.CreatedAt)
		b.WriteString("---\n\n")

		fmt.Fprintf(&b, "# %s\n", task.Title)
		if task.Notes != "" {
			b.WriteString("\n" + task.Notes + "\n")
		}

		name := fmt.Sprintf("%04d-%s.md", task.Num, slug(task.Title))
		if err := os.WriteFile(filepath.Join(dir, name), []byte(b.String()), 0o600); err != nil {
			return err
		}
	}
	fmt.Fprintf(os.Stderr, "td: wrote %d files to %s\n", len(doc.Tasks), dir)
	return nil
}

// yamlString quotes a value that would otherwise break the frontmatter. A
// title starting with a colon or a bracket is ordinary in a task list and
// invalid in YAML.
func yamlString(s string) string {
	if s == "" {
		return `""`
	}
	if strings.ContainsAny(s, `:#[]{}&*!|>'"%@`+"`\n") || strings.TrimSpace(s) != s {
		return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", " ").Replace(s) + `"`
	}
	return s
}

// slug turns a title into a filename that survives every filesystem anybody
// is going to put this on.
func slug(title string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(title) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			dash = false
		case !dash && b.Len() > 0:
			b.WriteByte('-')
			dash = true
		}
		if b.Len() >= 60 {
			break
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "task"
	}
	return out
}
