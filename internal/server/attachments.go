package server

import (
	"errors"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/harpchad/td/internal/api"
	"github.com/harpchad/td/internal/blob"
)

// listAttachments returns a task's attachments.
func (s *Server) listAttachments(w http.ResponseWriter, r *http.Request) {
	id, ok := s.resolve(w, r)
	if !ok {
		return
	}
	list, err := s.store.Attachments(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// addAttachment takes a file on a task.
//
// The body is the file itself rather than a multipart form: there is one
// field, the client is a CLI as often as a browser, and multipart buys
// nothing but a parser. The filename rides in a header.
func (s *Server) addAttachment(w http.ResponseWriter, r *http.Request) {
	if s.blobs == nil {
		s.fail(w, &api.Error{Code: api.ErrBadRequest, Message: "attachments are not configured"})
		return
	}
	id, ok := s.resolve(w, r)
	if !ok {
		return
	}
	// The task has to exist before 25 MB crosses the wire.
	if _, err := s.store.Get(r.Context(), id); err != nil {
		s.fail(w, err)
		return
	}

	// The declared length is a courtesy check. The real cap is enforced while
	// streaming, because a lying Content-Length is free to send.
	if r.ContentLength > blob.MaxBytes {
		s.tooLarge(w)
		return
	}

	digest, size, err := s.blobs.Put(http.MaxBytesReader(w, r.Body, blob.MaxBytes+1))
	if err != nil {
		if errors.Is(err, blob.ErrTooLarge) || isMaxBytes(err) {
			s.tooLarge(w)
			return
		}
		s.fail(w, err)
		return
	}

	saved, err := s.store.AddAttachment(r.Context(), s.actorOf(r), api.Attachment{
		TaskID:   id,
		SHA256:   digest,
		Filename: safeFilename(r.Header.Get("X-Td-Filename")),
		Bytes:    size,
		Mime:     contentType(r.Header.Get("Content-Type")),
	}, s.Now())
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, saved)
}

// getAttachment streams one file back.
//
// The route is under /api/v1 and therefore behind the same authentication
// middleware as everything else, which is the point: section 15 requires the
// auth check on every download, and a static file handler over the blob
// directory would be a guessable path that skips it.
func (s *Server) getAttachment(w http.ResponseWriter, r *http.Request) {
	if s.blobs == nil {
		s.fail(w, &api.Error{Code: api.ErrNotFound, Message: "attachments are not configured"})
		return
	}
	a, err := s.store.Attachment(r.Context(), r.PathValue("att"))
	if err != nil {
		s.fail(w, err)
		return
	}
	// The attachment id is enough to find the row, but the URL carries the
	// task too. A mismatch is a wrong link rather than a 200 on the wrong
	// parent.
	taskID, ok := s.resolve(w, r)
	if !ok {
		return
	}
	if a.TaskID != taskID {
		s.fail(w, &api.Error{Code: api.ErrNotFound, Message: "no such attachment on that task"})
		return
	}

	f, err := s.blobs.Open(a.SHA256)
	if err != nil {
		s.fail(w, err)
		return
	}
	defer func() { _ = f.Close() }()

	// Always an attachment, never inline. A stored HTML file rendered in the
	// origin would be a stored XSS with a valid session attached to it.
	w.Header().Set("Content-Type", a.Mime)
	w.Header().Set("Content-Length", strconv.FormatInt(a.Bytes, 10))
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+urlEscape(a.Filename))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if _, err := io.Copy(w, f); err != nil {
		s.log.Error("streaming attachment", "id", a.ID, "err", err)
	}
}

// deleteAttachment detaches a file. The bytes stay until the weekly sweep,
// because another task may point at the same blob.
func (s *Server) deleteAttachment(w http.ResponseWriter, r *http.Request) {
	if err := s.store.RemoveAttachment(r.Context(), s.actorOf(r), r.PathValue("att"), s.Now()); err != nil {
		s.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) tooLarge(w http.ResponseWriter) {
	writeJSON(w, http.StatusRequestEntityTooLarge, &api.Error{
		Code:    api.ErrBadRequest,
		Message: "attachments cap at 25 MB",
	})
}

// isMaxBytes recognizes what http.MaxBytesReader returns. It is its own type
// with no exported constructor, so the check is on the interface it
// satisfies.
func isMaxBytes(err error) bool {
	var maxErr *http.MaxBytesError
	return errors.As(err, &maxErr)
}

// safeFilename keeps a name for display and strips everything that would make
// it a path. The name never touches the filesystem, since the blob is stored
// under its digest, but it does end up in a Content-Disposition header.
func safeFilename(name string) string {
	name = filepath.Base(strings.TrimSpace(name))
	if name == "." || name == string(filepath.Separator) || name == ".." {
		return "attachment"
	}
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == '"' || r == '\\' || r == '/' {
			return -1
		}
		return r
	}, name)
	if name == "" {
		return "attachment"
	}
	if len(name) > 200 {
		name = name[:200]
	}
	return name
}

// contentType keeps a declared type only when it parses. Anything else
// becomes octet-stream, which is what an unknown blob is.
func contentType(declared string) string {
	parsed, _, err := mime.ParseMediaType(declared)
	if err != nil || parsed == "" {
		return "application/octet-stream"
	}
	return parsed
}

// urlEscape percent-encodes a filename for the RFC 5987 form of
// Content-Disposition.
func urlEscape(name string) string {
	var b strings.Builder
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '-', c == '.', c == '_', c == '~':
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			const hex = "0123456789ABCDEF"
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&0x0f])
		}
	}
	return b.String()
}
