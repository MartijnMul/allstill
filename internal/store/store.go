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
	"runtime"
	"sort"
	"strconv"
	"strings"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Store is an open database.
type Store struct {
	w *sql.DB // the single writer
	r *sql.DB // readers
}

// Open opens or creates the database at path and brings its schema up to date.
func Open(ctx context.Context, path string) (*Store, error) {
	pragmas := []string{"foreign_keys(1)", "journal_mode(WAL)", "synchronous(NORMAL)", "busy_timeout(10000)"}
	q := url.Values{"_pragma": pragmas}
	// SQLite reads the path as a URI: escape what would end it or be decoded.
	file := "file:" + strings.NewReplacer("%", "%25", "?", "%3f", "#", "%23").Replace(path)
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
	if err := s.migrate(ctx); err != nil {
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

func list() ([]migration, error) {
	names, err := fs.Glob(migrations, "migrations/*.sql")
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
func (s *Store) migrate(ctx context.Context) error {
	ms, err := list()
	if err != nil {
		return err
	}
	for {
		done, err := s.migrateOne(ctx, ms)
		if done || err != nil {
			return err
		}
	}
}

func (s *Store) migrateOne(ctx context.Context, ms []migration) (done bool, err error) {
	tx, err := s.w.BeginTx(ctx, nil)
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
	body, err := migrations.ReadFile(m.name)
	if err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, string(body)); err != nil {
		return false, fmt.Errorf("%s: %w", m.name, err)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", m.version)); err != nil {
		return false, err
	}
	return false, tx.Commit()
}
