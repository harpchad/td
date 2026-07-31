// Package blob is a content-addressed file store.
//
// Files are named by the SHA-256 of their contents and sharded two levels
// deep, so the same PDF attached to four tasks is one file on disk and a
// directory never holds a hundred thousand entries. Nothing here knows about
// tasks or auth: the store hands out paths, and the only route that reads one
// is behind the auth check.
package blob

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// MaxBytes is the per-file cap. Section 17 fixes it at 25 MB: large enough
// for a scanned contract, small enough that a runaway upload cannot fill an
// 8 GB host.
const MaxBytes int64 = 25 << 20

// ErrTooLarge is returned when a file exceeds MaxBytes.
var ErrTooLarge = errors.New("file is larger than 25 MB")

// ErrNotFound is returned when a digest has no file.
var ErrNotFound = errors.New("no such blob")

// Store is a directory of content-addressed files.
type Store struct {
	root string
}

// New opens a blob store rooted at dir, creating it if it does not exist.
func New(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("blob root: %w", err)
	}
	return &Store{root: dir}, nil
}

// Root is the directory the store writes under.
func (s *Store) Root() string { return s.root }

// Put streams r into the store and returns its digest and size.
//
// The content is written to a temporary file first and renamed into place
// only once the digest is known, because the name is the digest. A rename
// within one filesystem is atomic, so a crash mid-upload leaves a temp file
// rather than a truncated blob that would answer to a hash it does not have.
func (s *Store) Put(r io.Reader) (digest string, size int64, err error) {
	tmp, err := os.CreateTemp(s.root, ".upload-*")
	if err != nil {
		return "", 0, err
	}
	defer func() {
		_ = tmp.Close()
		if err != nil {
			_ = os.Remove(tmp.Name())
		}
	}()

	sum := sha256.New()
	// One byte past the cap, so hitting the limit is distinguishable from a
	// file that happens to be exactly MaxBytes.
	size, err = io.Copy(io.MultiWriter(tmp, sum), io.LimitReader(r, MaxBytes+1))
	if err != nil {
		return "", 0, err
	}
	if size > MaxBytes {
		return "", size, ErrTooLarge
	}
	if err = tmp.Sync(); err != nil {
		return "", size, err
	}
	if err = tmp.Close(); err != nil {
		return "", size, err
	}

	digest = hex.EncodeToString(sum.Sum(nil))
	dest := s.Path(digest)
	if err = os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return "", size, err
	}
	if _, statErr := os.Stat(dest); statErr == nil {
		// Already stored. Identical content is one file, which is the point of
		// addressing by hash.
		_ = os.Remove(tmp.Name())
		return digest, size, nil
	}
	if err = os.Rename(tmp.Name(), dest); err != nil {
		return "", size, err
	}
	return digest, size, nil
}

// Path is where a digest lives. It is exported so a caller can stat a blob
// without opening it; nothing outside this package should build the path
// itself.
func (s *Store) Path(digest string) string {
	return filepath.Join(s.root, digest[0:2], digest[2:4], digest)
}

// Open returns a reader over one blob.
func (s *Store) Open(digest string) (*os.File, error) {
	if !validDigest(digest) {
		return nil, ErrNotFound
	}
	f, err := os.Open(s.Path(digest))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	return f, err
}

// Sweep deletes every blob whose digest is not in keep and returns how many
// went. Orphans are collected on a schedule rather than on delete, because a
// dropped task is not gone and its attachment has to survive an undo.
func (s *Store) Sweep(keep map[string]bool) (int, error) {
	removed := 0
	err := filepath.WalkDir(s.root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		name := d.Name()
		if strings.HasPrefix(name, ".upload-") {
			// A temp file from an upload that died. Nothing references it.
			if os.Remove(path) == nil {
				removed++
			}
			return nil
		}
		if !validDigest(name) || keep[name] {
			return nil
		}
		if err := os.Remove(path); err != nil {
			return err
		}
		removed++
		return nil
	})
	return removed, err
}

// validDigest rejects anything that is not 64 lowercase hex characters. It is
// what stops a crafted digest from walking out of the root with "..".
func validDigest(s string) bool {
	if len(s) != sha256.Size*2 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
