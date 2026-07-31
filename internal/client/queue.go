package client

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/harpchad/td/internal/api"
)

// queueFile holds captures made while the server was unreachable, one JSON
// object per line. Append-only, so an interrupted write costs at most the
// line being written.
const queueFile = "queue.jsonl"

// QueuePath returns the offline queue's location.
func QueuePath() (string, error) {
	dir, err := StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, queueFile), nil
}

// Enqueue appends a capture to the offline queue. The task already carries a
// client-generated id, so flushing it later cannot create a duplicate however
// many times the flush is retried.
func Enqueue(in api.TaskCreate) error {
	path, err := QueuePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	line, err := json.Marshal(in)
	if err != nil {
		return err
	}
	_, err = f.Write(append(line, '\n'))
	return err
}

// QueueDepth reports how many captures are waiting.
func QueueDepth() (int, error) {
	pending, err := readQueue()
	return len(pending), err
}

// Flush sends every queued capture and clears the queue. It returns the
// number sent. A failure part-way leaves the unsent remainder on disk, so a
// second flush picks up where this one stopped.
func (c *Client) Flush(ctx context.Context) (int, error) {
	pending, err := readQueue()
	if err != nil || len(pending) == 0 {
		return 0, err
	}

	sent := 0
	for i, in := range pending {
		if _, err := c.Create(ctx, in); err != nil {
			if writeErr := writeQueue(pending[i:]); writeErr != nil {
				return sent, writeErr
			}
			return sent, err
		}
		sent++
	}
	return sent, writeQueue(nil)
}

func readQueue() ([]api.TaskCreate, error) {
	path, err := QueuePath()
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	var out []api.TaskCreate
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var in api.TaskCreate
		if err := json.Unmarshal(line, &in); err != nil {
			// A corrupt line must not strand every capture behind it.
			continue
		}
		out = append(out, in)
	}
	return out, sc.Err()
}

func writeQueue(pending []api.TaskCreate) error {
	path, err := QueuePath()
	if err != nil {
		return err
	}
	if len(pending) == 0 {
		err := os.Remove(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}

	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	for _, in := range pending {
		line, err := json.Marshal(in)
		if err != nil {
			_ = f.Close()
			return err
		}
		if _, err := w.Write(append(line, '\n')); err != nil {
			_ = f.Close()
			return err
		}
	}
	if err := w.Flush(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
