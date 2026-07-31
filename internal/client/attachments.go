package client

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path/filepath"

	"github.com/harpchad/td/internal/api"
)

// Attachments lists a task's files.
func (c *Client) Attachments(ctx context.Context, ref string) ([]api.Attachment, error) {
	var out []api.Attachment
	err := c.do(ctx, http.MethodGet,
		"/api/v1/tasks/"+url.PathEscape(ref)+"/attachments", nil, nil, &out)
	return out, err
}

// Attach uploads a file to a task. The body is the file itself and the name
// rides in a header, because there is one field and multipart buys nothing
// but a parser.
//
// The reader is streamed rather than buffered: a 25 MB file has no business
// sitting in memory on either end.
func (c *Client) Attach(ctx context.Context, ref, filename string, body io.Reader) (api.Attachment, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.base+"/api/v1/tasks/"+url.PathEscape(ref)+"/attachments", body)
	if err != nil {
		return api.Attachment{}, err
	}
	req.Header.Set("X-Td-Client", api.Version)
	req.Header.Set("X-Td-Filename", filepath.Base(filename))
	req.Header.Set("Content-Type", guessMime(filename))
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return api.Attachment{}, fmt.Errorf("%w: %w", ErrOffline, err)
	}
	defer func() { _ = resp.Body.Close() }()

	c.checkVersion(resp.Header.Get("X-Td-Server"))
	if resp.StatusCode >= 400 {
		return api.Attachment{}, decodeError(resp)
	}
	var out api.Attachment
	return out, json.NewDecoder(resp.Body).Decode(&out)
}

// Download streams one attachment into w.
func (c *Client) Download(ctx context.Context, ref, attachmentID string, w io.Writer) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.base+"/api/v1/tasks/"+url.PathEscape(ref)+
			"/attachments/"+url.PathEscape(attachmentID), nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Td-Client", api.Version)
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrOffline, err)
	}
	defer func() { _ = resp.Body.Close() }()

	c.checkVersion(resp.Header.Get("X-Td-Server"))
	if resp.StatusCode >= 400 {
		return decodeError(resp)
	}
	_, err = io.Copy(w, resp.Body)
	return err
}

// Detach removes a file from a task.
func (c *Client) Detach(ctx context.Context, ref, attachmentID string) error {
	return c.do(ctx, http.MethodDelete,
		"/api/v1/tasks/"+url.PathEscape(ref)+"/attachments/"+url.PathEscape(attachmentID),
		nil, nil, nil)
}

// guessMime reads a type off the extension. The server keeps whatever it is
// told only if it parses, and everything unrecognized is an opaque blob.
func guessMime(filename string) string {
	if t := mime.TypeByExtension(filepath.Ext(filename)); t != "" {
		return t
	}
	return "application/octet-stream"
}
