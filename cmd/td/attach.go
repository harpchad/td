package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/harpchad/td/internal/api"
	"github.com/harpchad/td/internal/client"
)

// attach uploads files to a task.
func attach(ctx context.Context, c *client.Client, args []string) error {
	fs := flag.NewFlagSet("attach", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print each attachment as JSON")
	list := fs.Bool("list", false, "list the task's files instead of adding one")
	remove := fs.String("rm", "", "detach the attachment with this id")
	get := fs.String("get", "", "download the attachment with this id")
	out := fs.String("o", "", "write the download here instead of the current directory")
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return errors.New("attach takes a task number or id")
	}
	ref := fs.Arg(0)

	switch {
	case *list:
		files, err := c.Attachments(ctx, ref)
		if err != nil {
			return err
		}
		if *asJSON {
			return printJSON(files)
		}
		if len(files) == 0 {
			fmt.Println("no files")
			return nil
		}
		for _, f := range files {
			fmt.Printf("%s  %-30s %8s  %s\n", f.ID, f.Filename, humanBytes(f.Bytes), f.Mime)
		}
		return nil

	case *remove != "":
		if err := c.Detach(ctx, ref, *remove); err != nil {
			return err
		}
		fmt.Printf("detached  %s\n", *remove)
		return nil

	case *get != "":
		return download(ctx, c, ref, *get, *out)
	}

	if fs.NArg() < 2 {
		return errors.New("attach takes a task and one or more files")
	}
	for _, path := range fs.Args()[1:] {
		saved, err := uploadOne(ctx, c, ref, path)
		if err != nil {
			return err
		}
		if *asJSON {
			if err := printJSON(saved); err != nil {
				return err
			}
			continue
		}
		fmt.Printf("attached  %s  %s  %s\n", saved.Filename, humanBytes(saved.Bytes), saved.ID)
	}
	return nil
}

func uploadOne(ctx context.Context, c *client.Client, ref, path string) (api.Attachment, error) {
	f, err := os.Open(path)
	if err != nil {
		return api.Attachment{}, err
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return api.Attachment{}, err
	}
	if info.IsDir() {
		return api.Attachment{}, fmt.Errorf("%s is a directory", path)
	}
	return c.Attach(ctx, ref, filepath.Base(path), f)
}

// download writes an attachment to disk. Without -o it lands in the current
// directory under the name it was uploaded with, and an existing file is
// never overwritten: a download that silently replaces something is a
// surprise nobody wants twice.
func download(ctx context.Context, c *client.Client, ref, id, dest string) error {
	files, err := c.Attachments(ctx, ref)
	if err != nil {
		return err
	}
	name := ""
	for _, f := range files {
		if f.ID == id {
			name = f.Filename
			break
		}
	}
	if name == "" {
		return fmt.Errorf("task %s has no attachment %s", ref, id)
	}
	if dest == "" {
		dest = name
	} else if info, err := os.Stat(dest); err == nil && info.IsDir() {
		dest = filepath.Join(dest, name)
	}

	out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = out.Close() }()

	if err := c.Download(ctx, ref, id, out); err != nil {
		_ = os.Remove(dest)
		return err
	}
	fmt.Printf("wrote  %s\n", dest)
	return nil
}

// humanBytes is for a terminal column, not for arithmetic.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for size := n / unit; size >= unit; size /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGT"[exp])
}
