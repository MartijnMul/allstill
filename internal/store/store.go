// SPDX-License-Identifier: AGPL-3.0-only

// Package store keeps Allstill's data in one SQLite file.
//
// SQLite allows one writer at a time, so the store holds two pools: one
// connection that writes, with transactions that take the write lock when they
// begin, and several that only read. Both run in WAL mode, so reads never wait
// for the writer.
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Store is an open database.
type Store struct {
	// Retention is how much history Tidy keeps; zero is DefaultRetention.
	Retention Retention

	w *sql.DB // the single writer
	r *sql.DB // readers

	tidy      sync.Mutex
	tidyAfter string // the check id the last Tidy stopped after
}

// Open opens or creates the database at path and brings its schema up to date.
// Before it changes the schema of a database in use, it copies the database to
// path.before-NNNN, NNNN being the first migration it applies.
func Open(ctx context.Context, path string) (*Store, error) {
	return openWith(ctx, path, migrations)
}

// ErrNoDatabase means a path holds no Allstill database: nothing, an empty file,
// which SQLite would take for a new one, or another program's SQLite file, which
// migrating would write Allstill's tables into.
var ErrNoDatabase = errors.New("no database")

// OpenExisting opens the database at path as Open does, but creates nothing: only
// the server starts a database, so a command run somewhere else cannot make one of
// its own and appear to work.
func OpenExisting(ctx context.Context, path string) (*Store, error) {
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) || err == nil && info.Size() == 0 {
		return nil, ErrNoDatabase
	}
	if err != nil {
		return nil, err
	}
	conn, err := sql.Open("sqlite", fileURI(path)+"?mode=rw&_pragma=busy_timeout(10000)")
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	// Its own tables say whose it is: other programs use user_version as well.
	var tables int
	err = conn.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name IN ('workspaces', 'checks')`).Scan(&tables)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	if tables != 2 {
		return nil, ErrNoDatabase
	}
	return Open(ctx, path)
}

// fileURI is path as SQLite reads it, a URI: it escapes what would end it or be
// decoded.
func fileURI(path string) string {
	return "file:" + strings.NewReplacer("%", "%25", "?", "%3f", "#", "%23").Replace(path)
}

func openWith(ctx context.Context, path string, schema fs.FS) (*Store, error) {
	// journal_size_limit shrinks the WAL file back after a burst of writes.
	pragmas := []string{"foreign_keys(1)", "journal_mode(WAL)", "synchronous(NORMAL)", "busy_timeout(10000)", "journal_size_limit(67108864)"}
	q := url.Values{"_pragma": pragmas}
	file := fileURI(path)
	w, err := sql.Open("sqlite", file+"?"+q.Encode()+"&_txlock=immediate")
	if err != nil {
		return nil, err
	}
	w.SetMaxOpenConns(1)
	q.Add("_pragma", "query_only(1)")
	r, err := sql.Open("sqlite", file+"?"+q.Encode())
	if err != nil {
		w.Close()
		return nil, err
	}
	r.SetMaxOpenConns(max(4, runtime.NumCPU()))
	s := &Store{w: w, r: r}
	if err := s.migrate(ctx, path, schema); err != nil {
		s.Close()
		return nil, fmt.Errorf("migrating %s: %w", path, err)
	}
	return s, nil
}

// Close closes both pools.
func (s *Store) Close() error {
	return errors.Join(s.r.Close(), s.w.Close())
}

type migration struct {
	version int
	name    string
}

func list(schema fs.FS) ([]migration, error) {
	names, err := fs.Glob(schema, "migrations/*.sql")
	if err != nil {
		return nil, err
	}
	var ms []migration
	for _, n := range names {
		base := strings.TrimPrefix(n, "migrations/")
		num, _, ok := strings.Cut(base, "_")
		v, err := strconv.Atoi(num)
		if !ok || err != nil || v < 1 {
			return nil, fmt.Errorf("migration %s: name must start with its number, as 0001_name.sql", base)
		}
		ms = append(ms, migration{v, n})
	}
	sort.Slice(ms, func(i, j int) bool { return ms[i].version < ms[j].version })
	for i, m := range ms {
		if m.version != i+1 {
			return nil, fmt.Errorf("migration %s: expected number %d", m.name, i+1)
		}
	}
	return ms, nil
}

// migrate applies every migration newer than the database's user_version, each
// in its own transaction with the version bump, so a failed migration leaves the
// database as it was. The version is read inside the transaction, so two
// processes opening a new file at once do not both migrate it. A database newer
// than this binary is refused.
//
// Foreign keys are off while migrating: a migration that rebuilds a table
// drops it, and with them on, dropping a table deletes every row that refers
// to it. SQLite ignores the pragma inside a transaction, so it is set around
// them, and each migration must leave no reference broken.
func (s *Store) migrate(ctx context.Context, path string, schema fs.FS) (err error) {
	ms, err := list(schema)
	if err != nil {
		return err
	}
	if err := s.backUp(ctx, path, len(ms)); err != nil {
		return err
	}
	conn, err := s.w.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "PRAGMA foreign_keys = OFF"); err != nil {
		return err
	}
	defer func() {
		// The connection is the store's only writer: it must not keep them off.
		_, on := conn.ExecContext(context.WithoutCancel(ctx), "PRAGMA foreign_keys = ON")
		err = errors.Join(err, on)
	}()
	for {
		done, err := migrateOne(ctx, conn, schema, ms)
		if done || err != nil {
			return err
		}
	}
}

func migrateOne(ctx context.Context, conn *sql.Conn, schema fs.FS, ms []migration) (done bool, err error) {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var current int
	if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&current); err != nil {
		return false, err
	}
	if current > len(ms) {
		return false, fmt.Errorf("the database is at version %d, newer than this binary (%d); use a newer Allstill", current, len(ms))
	}
	if current == len(ms) {
		return true, nil
	}
	m := ms[current]
	body, err := fs.ReadFile(schema, m.name)
	if err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, string(body)); err != nil {
		return false, fmt.Errorf("%s: %w", m.name, err)
	}
	var table string
	switch err := tx.QueryRowContext(ctx, "PRAGMA foreign_key_check").Scan(&table, new(any), new(any), new(any)); {
	case err == nil:
		return false, fmt.Errorf("%s: leaves rows in %s that refer to a row that is gone", m.name, table)
	case !errors.Is(err, sql.ErrNoRows):
		return false, fmt.Errorf("%s: %w", m.name, err)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", m.version)); err != nil {
		return false, err
	}
	return false, tx.Commit()
}

// backUp copies a database in use whose schema is about to change. A copy
// already there is kept: it is complete, and from before an earlier attempt.
func (s *Store) backUp(ctx context.Context, path string, latest int) error {
	var current int
	if err := s.w.QueryRowContext(ctx, "PRAGMA user_version").Scan(&current); err != nil {
		return err
	}
	if current == 0 || current >= latest {
		return nil
	}
	err := s.Backup(ctx, fmt.Sprintf("%s.before-%04d", path, current+1))
	if err != nil && !errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("copying the database before changing it: %w", err)
	}
	return nil
}

// Backup writes a consistent copy of the database to a new file, while it is
// in use, readable by its owner only. The copy is written under another name
// first, so a copy cut short leaves nothing at to. VACUUM INTO only reads, but
// SQLite refuses it on a read-only connection; the copy holds up this store's
// writes, not those of another process.
func (s *Store) Backup(ctx context.Context, to string) error {
	if _, err := os.Lstat(to); err == nil {
		return fmt.Errorf("%s: %w", to, fs.ErrExist)
	}
	part := fmt.Sprintf("%s.partial-%d", to, os.Getpid())
	os.Remove(part)
	defer os.Remove(part)
	// VACUUM INTO takes an empty file, made here so it is never readable by others.
	f, err := os.OpenFile(part, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	f.Close()
	if _, err := s.w.ExecContext(ctx, "VACUUM INTO ?", part); err != nil {
		return err
	}
	// A link, unlike a rename, never replaces a copy another process made.
	if err := os.Link(part, to); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("%s: %w", to, fs.ErrExist)
		}
		return os.Rename(part, to)
	}
	return nil
}
