package server_test

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/harpchad/td/internal/api"
	"github.com/harpchad/td/internal/blob"
)

// upload posts a file to a task and returns the status and the body.
func upload(t *testing.T, ts *harness, ref, filename string, body []byte, token string) (respMeta, []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
		ts.URL+"/api/v1/tasks/"+ref+"/attachments", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/pdf")
	req.Header.Set("X-Td-Filename", filename)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return respMeta{StatusCode: resp.StatusCode, Header: resp.Header}, out
}

// TestAttachmentRoundTrip is the ordinary path: upload, list, download.
func TestAttachmentRoundTrip(t *testing.T) {
	ts := newServer(t)
	payload := []byte("%PDF-1.7\nthe signed contract\n")

	resp, body := upload(t, ts, "103", "contract.pdf", payload, ts.token)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d: %s", resp.StatusCode, body)
	}
	var a api.Attachment
	decodeInto(t, body, &a)
	if a.Bytes != int64(len(payload)) {
		t.Errorf("bytes = %d, want %d", a.Bytes, len(payload))
	}
	if a.Filename != "contract.pdf" || a.Mime != "application/pdf" {
		t.Errorf("attachment = %+v", a)
	}

	// The task now reports it, which is what has:attachment filters on.
	_, body = do(t, ts, http.MethodGet, "/api/v1/tasks/103", nil)
	var task api.Task
	decodeInto(t, body, &task)
	if task.Attachments != 1 {
		t.Errorf("task reports %d attachments", task.Attachments)
	}

	resp, got := do(t, ts, http.MethodGet, "/api/v1/tasks/103/attachments/"+a.ID, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("download status = %d: %s", resp.StatusCode, got)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("downloaded %d bytes, want %d", len(got), len(payload))
	}

	// Never inline. A stored HTML file rendered in the origin would be a
	// stored XSS with a valid session attached to it.
	if cd := resp.Header.Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment;") {
		t.Errorf("Content-Disposition = %q", cd)
	}
	if resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Error("no nosniff on an attachment download")
	}

	// Detaching leaves the task without it.
	resp, got = do(t, ts, http.MethodDelete, "/api/v1/tasks/103/attachments/"+a.ID, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete status = %d: %s", resp.StatusCode, got)
	}
	_, body = do(t, ts, http.MethodGet, "/api/v1/tasks/103/attachments", nil)
	var list []api.Attachment
	decodeInto(t, body, &list)
	if len(list) != 0 {
		t.Errorf("%d attachments after detaching", len(list))
	}
}

// TestNoRouteServesAnAttachmentWithoutCheckingAuthFirst is the security
// assertion from section 15, as a test rather than a review item.
func TestNoRouteServesAnAttachmentWithoutCheckingAuthFirst(t *testing.T) {
	ts := newServer(t)
	payload := []byte("the private one")

	_, body := upload(t, ts, "103", "private.txt", payload, ts.token)
	var a api.Attachment
	decodeInto(t, body, &a)

	// The download route with no credential.
	resp, out := doAnon(t, ts, http.MethodGet, "/api/v1/tasks/103/attachments/"+a.ID, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("anonymous download status = %d, want 401", resp.StatusCode)
	}
	if len(out) != 0 {
		t.Errorf("401 carried a body: %s", out)
	}
	if bytes.Contains(out, payload) {
		t.Fatal("the bytes came back without a credential")
	}

	// Uploading without one is refused too.
	resp, _ = upload(t, ts, "103", "sneaky.txt", []byte("x"), "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("anonymous upload status = %d, want 401", resp.StatusCode)
	}

	// And there is no static handler over the blob directory. The digest is
	// the only other name the bytes have, so if any path serves it the store
	// is reachable around the auth check.
	for _, guess := range []string{
		"/blobs/" + a.SHA256,
		"/data/blobs/" + a.SHA256,
		"/static/" + a.SHA256,
		"/" + a.SHA256,
	} {
		resp, out := doAnon(t, ts, http.MethodGet, guess, nil)
		if resp.StatusCode == http.StatusOK && bytes.Contains(out, payload) {
			t.Errorf("%s served the blob", guess)
		}
	}
}

// TestAnAttachmentIsScopedToItsTask stops a valid attachment id from being
// readable through the wrong parent.
func TestAnAttachmentIsScopedToItsTask(t *testing.T) {
	ts := newServer(t)

	_, body := upload(t, ts, "103", "notes.txt", []byte("scoped"), ts.token)
	var a api.Attachment
	decodeInto(t, body, &a)

	resp, _ := do(t, ts, http.MethodGet, "/api/v1/tasks/101/attachments/"+a.ID, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d through the wrong task, want 404", resp.StatusCode)
	}
}

// TestTheCapIsRefusedWithA413 covers the 25 MB limit at the HTTP edge, where
// a lying Content-Length has to be caught by the stream rather than by the
// header.
func TestTheCapIsRefusedWithA413(t *testing.T) {
	ts := newServer(t)

	oversize := bytes.Repeat([]byte("x"), int(blob.MaxBytes)+1024)
	resp, body := upload(t, ts, "103", "huge.bin", oversize, ts.token)
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413: %s", resp.StatusCode, body)
	}

	// Nothing was recorded.
	_, body = do(t, ts, http.MethodGet, "/api/v1/tasks/103/attachments", nil)
	var list []api.Attachment
	decodeInto(t, body, &list)
	if len(list) != 0 {
		t.Errorf("a rejected upload left %d rows", len(list))
	}
}

// TestTheSameFileOnTwoTasksIsOneBlob is the dedupe, seen from the API.
func TestTheSameFileOnTwoTasksIsOneBlob(t *testing.T) {
	ts := newServer(t)
	payload := []byte("the same slide deck")

	_, first := upload(t, ts, "103", "deck.pdf", payload, ts.token)
	_, second := upload(t, ts, "101", "deck.pdf", payload, ts.token)

	var a, b api.Attachment
	decodeInto(t, first, &a)
	decodeInto(t, second, &b)

	if a.ID == b.ID {
		t.Error("two attachments share one row")
	}
	if a.SHA256 != b.SHA256 {
		t.Errorf("the same bytes hashed to %s and %s", a.SHA256, b.SHA256)
	}

	// Detaching one leaves the other downloadable, which is the thing a
	// reference-counted delete would break.
	if resp, _ := do(t, ts, http.MethodDelete, "/api/v1/tasks/103/attachments/"+a.ID, nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete status = %d", resp.StatusCode)
	}
	resp, got := do(t, ts, http.MethodGet, "/api/v1/tasks/101/attachments/"+b.ID, nil)
	if resp.StatusCode != http.StatusOK || !bytes.Equal(got, payload) {
		t.Errorf("the surviving attachment broke: status %d, %d bytes", resp.StatusCode, len(got))
	}
}

// TestAFilenameCannotBeAPath keeps a crafted name out of the response header
// and out of anything that might later join it onto a directory.
func TestAFilenameCannotBeAPath(t *testing.T) {
	ts := newServer(t)

	_, body := upload(t, ts, "103", "../../etc/passwd", []byte("x"), ts.token)
	var a api.Attachment
	decodeInto(t, body, &a)
	if strings.Contains(a.Filename, "/") || strings.Contains(a.Filename, "..") {
		t.Errorf("filename = %q", a.Filename)
	}

	_, body = upload(t, ts, "103", "evil\"; filename=\"x.exe", []byte("x"), ts.token)
	decodeInto(t, body, &a)
	resp, _ := do(t, ts, http.MethodGet, "/api/v1/tasks/103/attachments/"+a.ID, nil)
	// The name is percent-encoded, so the quote and the semicolon it carried
	// cannot end the header's first parameter and start a second one.
	cd := resp.Header.Get("Content-Disposition")
	if strings.Count(cd, "filename*=") != 1 || strings.Contains(cd, `"`) || strings.Contains(cd, "; filename=") {
		t.Errorf("a crafted name split the header: %q", cd)
	}
}
