package blob_test

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/harpchad/td/internal/blob"
)

func newStore(t *testing.T) *blob.Store {
	t.Helper()
	s, err := blob.New(filepath.Join(t.TempDir(), "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// TestIdenticalContentIsStoredOnce is the reason the store is addressed by
// hash: the same attachment on four tasks is one file.
func TestIdenticalContentIsStoredOnce(t *testing.T) {
	s := newStore(t)

	first, size, err := s.Put(strings.NewReader("the quarterly report"))
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := s.Put(strings.NewReader("the quarterly report"))
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("same bytes hashed to %s and %s", first, second)
	}
	if size != int64(len("the quarterly report")) {
		t.Errorf("size = %d", size)
	}

	// The digest, from shasum -a 256 rather than from this code.
	if first != "840c4da41cae1692be468785e53571fcd7ea0406c248a6e9b1f4af1b05c58e10" {
		t.Errorf("digest = %s, want the sha256 of the payload", first)
	}

	files := 0
	_ = filepath.WalkDir(s.Root(), func(_ string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			files++
		}
		return nil
	})
	if files != 1 {
		t.Errorf("%d files on disk for one distinct payload", files)
	}
}

// TestTheCapIsEnforcedWhileStreaming matters because the alternative is
// buffering 25 MB in memory to find out it was too big.
func TestTheCapIsEnforcedWhileStreaming(t *testing.T) {
	s := newStore(t)

	oversize := bytes.NewReader(bytes.Repeat([]byte("x"), int(blob.MaxBytes)+1))
	if _, _, err := s.Put(oversize); !errors.Is(err, blob.ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}

	// And nothing was left behind.
	files := 0
	_ = filepath.WalkDir(s.Root(), func(_ string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			files++
		}
		return nil
	})
	if files != 0 {
		t.Errorf("a rejected upload left %d files", files)
	}

	// Exactly at the cap is accepted, so the boundary is not off by one.
	atCap := bytes.NewReader(bytes.Repeat([]byte("y"), int(blob.MaxBytes)))
	if _, size, err := s.Put(atCap); err != nil {
		t.Fatalf("a file of exactly the cap was rejected: %v", err)
	} else if size != blob.MaxBytes {
		t.Errorf("size = %d, want %d", size, blob.MaxBytes)
	}
}

// TestACraftedDigestCannotEscapeTheRoot is the traversal check. The digest
// arrives from a URL path, so it is user input.
func TestACraftedDigestCannotEscapeTheRoot(t *testing.T) {
	s := newStore(t)

	for _, bad := range []string{
		"../../../../etc/passwd",
		"..",
		"",
		strings.Repeat("g", 64),
		strings.Repeat("A", 64),
		strings.Repeat("a", 63),
	} {
		if _, err := s.Open(bad); !errors.Is(err, blob.ErrNotFound) {
			t.Errorf("Open(%q) = %v, want ErrNotFound", bad, err)
		}
	}
}

// TestRoundTrip is the ordinary path.
func TestRoundTrip(t *testing.T) {
	s := newStore(t)
	want := "attachment body\x00with bytes that are not text"

	digest, _, err := s.Put(strings.NewReader(want))
	if err != nil {
		t.Fatal(err)
	}
	f, err := s.Open(digest)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	got, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Errorf("read back %q", got)
	}
}

// TestSweepKeepsWhatIsReferenced covers weekly orphan collection. Getting
// this backwards deletes a live attachment, so the keep set is what survives
// and everything else goes.
func TestSweepKeepsWhatIsReferenced(t *testing.T) {
	s := newStore(t)

	keeper, _, err := s.Put(strings.NewReader("still attached"))
	if err != nil {
		t.Fatal(err)
	}
	orphan, _, err := s.Put(strings.NewReader("nothing points at this"))
	if err != nil {
		t.Fatal(err)
	}

	// A half-finished upload is an orphan too.
	tmp, err := os.CreateTemp(s.Root(), ".upload-*")
	if err != nil {
		t.Fatal(err)
	}
	_ = tmp.Close()

	removed, err := s.Sweep(map[string]bool{keeper: true})
	if err != nil {
		t.Fatal(err)
	}
	if removed != 2 {
		t.Errorf("swept %d, want the orphan and the temp file", removed)
	}
	if _, err := s.Open(keeper); err != nil {
		t.Errorf("the referenced blob was swept: %v", err)
	}
	if _, err := s.Open(orphan); !errors.Is(err, blob.ErrNotFound) {
		t.Error("the orphan survived the sweep")
	}
}
