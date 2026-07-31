// Package store owns the SQLite database: schema, migrations, full-text
// search, and every query the server runs. It is server-only. A test walks
// the import graph of cmd/td and fails the build if this package ever appears
// in it, because a client that can open the database file is a client that
// only works on the box holding it.
package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	sqlite "modernc.org/sqlite"

	"github.com/harpchad/td/internal/query"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// activeLoc is the timezone every date comparison in SQL resolves against.
// modernc.org/sqlite registers scalar functions per process rather than per
// connection, so the location lives here rather than on Store. One process
// serves one timezone, which is what the config file already assumes.
var activeLoc atomic.Pointer[time.Location]

func init() {
	activeLoc.Store(time.UTC)

	// td_local_date reduces a stored date value to a calendar date in the
	// configured timezone. Doing this in SQL rather than in Go keeps date
	// predicates inside the query, so a filter still pushes down to an index
	// scan instead of loading every row.
	err := sqlite.RegisterScalarFunction("td_local_date", 1,
		func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
			s, ok := args[0].(string)
			if !ok || s == "" {
				return nil, nil
			}
			out := query.LocalDate(s, activeLoc.Load())
			if out == "" {
				return nil, nil
			}
			return out, nil
		})
	if err != nil {
		panic("store: registering td_local_date: " + err.Error())
	}
}

// Store is a handle on the database.
type Store struct {
	db  *sql.DB
	loc *time.Location
}

// Options configures Open.
type Options struct {
	// Location is the timezone every date-only comparison resolves in. A
	// container running UTC while the user lives in Central is the single
	// most likely source of an off-by-one-day bug in this system.
	Location *time.Location
}

// Open opens the database at path, applies any pending migrations, and
// returns a ready Store. Pass ":memory:" for a scratch database.
func Open(path string, opts Options) (*Store, error) {
	loc := opts.Location
	if loc == nil {
		loc = time.UTC
	}
	activeLoc.Store(loc)

	dsn := path
	if path != ":memory:" {
		dsn = path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	}

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	// SQLite takes one writer. Serializing here beats discovering it as
	// intermittent SQLITE_BUSY under load.
	db.SetMaxOpenConns(1)

	ctx := context.Background()
	if path == ":memory:" {
		if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys = ON`); err != nil {
			_ = db.Close()
			return nil, err
		}
	}

	s := &Store{db: db, loc: loc}
	if err := s.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the database handle.
func (s *Store) Close() error { return s.db.Close() }

// DB exposes the underlying handle for the few callers that need raw SQL,
// such as the seed loader.
func (s *Store) DB() *sql.DB { return s.db }

// Location reports the timezone this store resolves dates in.
func (s *Store) Location() *time.Location { return s.loc }

// migrate applies every embedded migration that has not run yet, in filename
// order, each in its own transaction.
func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		name TEXT PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		return fmt.Errorf("migrations table: %w", err)
	}

	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		var seen int
		err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations WHERE name = ?`, name).Scan(&seen)
		if err != nil {
			return err
		}
		if seen > 0 {
			continue
		}

		body, err := migrationFS.ReadFile("migrations/" + name)
		if err != nil {
			return err
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, string(body)); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %s: %w", name, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (name, applied_at) VALUES (?, ?)`,
			name, time.Now().UTC().Format(time.RFC3339)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// ErrNotFound is returned when a lookup by id or num matches nothing.
var ErrNotFound = errors.New("not found")
