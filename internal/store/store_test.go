// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.Context(), filepath.Join(t.TempDir(), "allstill.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestMigrateIsRepeatable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "allstill.db")
	for range 2 {
		s, err := Open(t.Context(), path)
		if err != nil {
			t.Fatal(err)
		}
		var v int
		s.r.QueryRow("PRAGMA user_version").Scan(&v)
		ms, _ := list(migrations)
		if v != len(ms) {
			t.Fatalf("user_version %d, want %d", v, len(ms))
		}
		s.Close()
	}
}

func TestRefusesANewerDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "allstill.db")
	s, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	s.w.Exec("PRAGMA user_version = 999")
	s.Close()
	if _, err := Open(context.Background(), path); err == nil || !strings.Contains(err.Error(), "newer") {
		t.Fatalf("opening a newer database: %v", err)
	}
}

func TestPragmas(t *testing.T) {
	s := open(t)
	var fk int
	var mode string
	s.w.QueryRow("PRAGMA foreign_keys").Scan(&fk)
	s.w.QueryRow("PRAGMA journal_mode").Scan(&mode)
	if fk != 1 || mode != "wal" {
		t.Fatalf("foreign_keys %d, journal_mode %s", fk, mode)
	}
	if _, err := s.r.Exec("CREATE TABLE x (a INTEGER)"); err == nil {
		t.Fatal("the read pool can write")
	}
}

func TestEveryTableHasAWorkspace(t *testing.T) {
	s := open(t)
	rows, err := s.r.Query(`SELECT name FROM sqlite_schema WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var n string
		rows.Scan(&n)
		tables = append(tables, n)
	}
	rows.Close()
	for _, table := range tables {
		if table == "workspaces" || table == "installation" {
			continue
		}
		var n int
		s.r.QueryRow(`SELECT count(*) FROM pragma_table_info(?) WHERE name = 'workspace_id' AND "notnull" = 1`, table).Scan(&n)
		if n != 1 {
			t.Errorf("table %s has no NOT NULL workspace_id", table)
		}
	}
}

func TestAChildKeepsItsParentsWorkspace(t *testing.T) {
	s := open(t)
	_, err := s.w.Exec(`
		INSERT INTO workspaces (id, name, created_at) VALUES ('ws_a', 'A', 0), ('ws_b', 'B', 0);
		INSERT INTO projects (id, workspace_id, name, created_at) VALUES ('prj_a', 'ws_a', 'A', 0);`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.w.Exec(`INSERT INTO checks (id, workspace_id, project_id, ping_uuid, name, kind, schedule_kind,
		interval_seconds, grace_seconds, state_since, created_at, updated_at)
		VALUES ('chk_x', 'ws_b', 'prj_a', '5c2b1d2e-8f3a-4b7c-9d1e-2f3a4b5c6d7e', 'x', 'ping', 'interval', 60, 60, 0, 0, 0)`)
	if err == nil || !strings.Contains(err.Error(), "FOREIGN KEY") {
		t.Fatalf("a check in another workspace than its project: %v", err)
	}
}

func TestOpensThePathAsGiven(t *testing.T) {
	for _, name := range []string{"with space", "q?x", "hash#x", "pct%20x", "amp&x"} {
		dir := filepath.Join(t.TempDir(), name)
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "allstill.db")
		s, err := Open(t.Context(), path)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var fk int
		s.w.QueryRow("PRAGMA foreign_keys").Scan(&fk)
		s.Close()
		if _, err := os.Stat(path); err != nil || fk != 1 {
			t.Fatalf("%s: file %v, foreign_keys %d", name, err, fk)
		}
	}
}

func TestRejectsAnIntervalCheckWithoutAnInterval(t *testing.T) {
	s := open(t)
	s.w.Exec(`INSERT INTO workspaces (id, name, created_at) VALUES ('ws_a', 'A', 0);
		INSERT INTO projects (id, workspace_id, name, created_at) VALUES ('prj_a', 'ws_a', 'A', 0)`)
	insert := func(uuid, kind string, interval any, cron string) error {
		_, err := s.w.Exec(`INSERT INTO checks (id, workspace_id, project_id, ping_uuid, name, kind, schedule_kind,
			interval_seconds, cron_expression, time_zone, grace_seconds, state_since, created_at, updated_at)
			VALUES (?, 'ws_a', 'prj_a', ?, 'x', 'ping', ?, ?, nullif(?, 'NULL'), 'UTC', 60, 0, 0, 0)`,
			"chk_"+uuid[:4], uuid, kind, interval, cron)
		return err
	}
	if err := insert("5c2b1d2e-8f3a-4b7c-9d1e-2f3a4b5c6d7e", "interval", 3600, "NULL"); err != nil {
		t.Fatalf("a valid interval check: %v", err)
	}
	for _, bad := range []struct {
		uuid, kind string
		interval   any
		cron       string
	}{
		{"6c2b1d2e-8f3a-4b7c-9d1e-2f3a4b5c6d7e", "interval", nil, "NULL"},
		{"7c2b1d2e-8f3a-4b7c-9d1e-2f3a4b5c6d7e", "cron", nil, ""},
		{"zzzzzzzz-zzzz-zzzz-zzzz-zzzzzzzzzzzz", "interval", 3600, "NULL"},
		{"8c2b1d2e8-f3a-4b7c-9d1e-2f3a4b5c6d7e", "interval", 3600, "NULL"},
	} {
		if err := insert(bad.uuid, bad.kind, bad.interval, bad.cron); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
}

// withMigration is the schema with one migration added after the last.
func withMigration(t *testing.T, sql string) fstest.MapFS {
	t.Helper()
	schema := fstest.MapFS{}
	ms, err := list(migrations)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		body, _ := migrations.ReadFile(m.name)
		schema[m.name] = &fstest.MapFile{Data: body}
	}
	schema[fmt.Sprintf("migrations/%04d_next.sql", len(ms)+1)] = &fstest.MapFile{Data: []byte(sql)}
	return schema
}

// aCheckWithHistory makes a database with one check, a run and a ping.
func aCheckWithHistory(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "allstill.db")
	s, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, err = s.w.Exec(`
		INSERT INTO workspaces (id, name, created_at) VALUES ('ws_a', 'A', 0);
		INSERT INTO projects (id, workspace_id, name, created_at) VALUES ('prj_a', 'ws_a', 'A', 0);
		INSERT INTO checks (id, workspace_id, project_id, ping_uuid, name, kind, schedule_kind,
			interval_seconds, grace_seconds, state_since, created_at, updated_at)
			VALUES ('chk_a', 'ws_a', 'prj_a', '5c2b1d2e-8f3a-4b7c-9d1e-2f3a4b5c6d7e', 'x', 'ping', 'interval', 60, 60, 0, 0, 0);
		INSERT INTO runs (id, workspace_id, check_id, outcome, ended_at) VALUES ('run_a', 'ws_a', 'chk_a', 'success', 1);
		INSERT INTO pings (workspace_id, check_id, run_id, kind, received_at, method) VALUES ('ws_a', 'chk_a', 'run_a', 'success', 1, 'GET');`)
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func count(t *testing.T, s *Store, table string) int {
	t.Helper()
	var n int
	if err := s.r.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestRebuildingATableKeepsWhatRefersToIt(t *testing.T) {
	path := aCheckWithHistory(t)
	// The way SQLite documents to change a table: a new one, the rows copied,
	// the old one dropped.
	var create string
	s, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	s.r.QueryRow("SELECT sql FROM sqlite_schema WHERE name = 'checks'").Scan(&create)
	s.Close()
	// A table that was renamed before is stored as CREATE TABLE "checks".
	create = regexp.MustCompile(`^CREATE TABLE "?checks"?`).ReplaceAllString(create, "CREATE TABLE checks_next")
	if !strings.HasPrefix(create, "CREATE TABLE checks_next") {
		t.Fatalf("cannot read the table's definition: %.60s", create)
	}
	rebuild := create + `;
		INSERT INTO checks_next SELECT * FROM checks;
		DROP TABLE checks;
		ALTER TABLE checks_next RENAME TO checks;`
	ms, _ := list(migrations)
	schema := withMigration(t, rebuild)
	s, err = openWith(t.Context(), path, schema)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var fk int
	s.w.QueryRow("PRAGMA foreign_keys").Scan(&fk)
	if runs, pings := count(t, s, "runs"), count(t, s, "pings"); runs != 1 || pings != 1 || fk != 1 {
		t.Fatalf("after the rebuild: %d runs, %d pings, foreign_keys %d", runs, pings, fk)
	}
	if _, err := os.Stat(fmt.Sprintf("%s.before-%04d", path, len(ms)+1)); err != nil {
		t.Fatalf("no copy from before: %v", err)
	}
}

func TestAMigrationMayNotLeaveABrokenReference(t *testing.T) {
	path := aCheckWithHistory(t)
	if _, err := openWith(t.Context(), path, withMigration(t, "DELETE FROM checks")); err == nil || !strings.Contains(err.Error(), "refer to a row that is gone") {
		t.Fatalf("a migration that orphans runs: %v", err)
	}
	s, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if n := count(t, s, "checks"); n != 1 {
		t.Fatalf("%d checks after the refused migration", n)
	}
}

func TestBackupWhileInUse(t *testing.T) {
	path := aCheckWithHistory(t)
	s, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	to := filepath.Join(t.TempDir(), "copy.db")
	if err := s.Backup(t.Context(), to); err != nil {
		t.Fatal(err)
	}
	if err := s.Backup(t.Context(), to); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("a backup over a file: %v", err)
	}
	if info, err := os.Stat(to); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("the copy: %v, %v", info, err)
	}
	if left, _ := filepath.Glob(to + ".partial-*"); len(left) != 0 {
		t.Fatalf("left behind: %v", left)
	}
	// A copy that fails leaves nothing.
	bad := filepath.Join(t.TempDir(), "missing", "copy.db")
	if err := s.Backup(t.Context(), bad); err == nil {
		t.Fatal("a copy into a directory that does not exist")
	}
	if left, _ := filepath.Glob(filepath.Dir(bad) + "*"); len(left) != 0 {
		t.Fatalf("left behind: %v", left)
	}
	c, err := Open(t.Context(), to)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if n := count(t, c, "pings"); n != 1 {
		t.Fatalf("%d pings in the copy", n)
	}
}

func TestOpenExistingNeedsADatabase(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()

	missing := filepath.Join(dir, "missing.db")
	if _, err := OpenExisting(ctx, missing); !errors.Is(err, ErrNoDatabase) {
		t.Fatalf("a missing file: %v, want ErrNoDatabase", err)
	}
	if _, err := os.Stat(missing); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("opening a missing file left something behind: %v", err)
	}

	// To SQLite a file with nothing in it is a new database.
	empty := filepath.Join(dir, "empty.db")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenExisting(ctx, empty); !errors.Is(err, ErrNoDatabase) {
		t.Errorf("an empty file: %v, want ErrNoDatabase", err)
	}

	// Another program's database is left exactly as it was.
	other := filepath.Join(dir, "other.db")
	conn, err := sql.Open("sqlite", other)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, "CREATE TABLE theirs (id INTEGER); PRAGMA user_version = 1"); err != nil {
		t.Fatal(err)
	}
	conn.Close()
	before, err := os.ReadFile(other)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenExisting(ctx, other); !errors.Is(err, ErrNoDatabase) {
		t.Errorf("another program's database: %v, want ErrNoDatabase", err)
	}
	if after, _ := os.ReadFile(other); !bytes.Equal(before, after) {
		t.Error("opening another program's database changed it")
	}

	// A database the server has open, with its write-ahead log in use.
	path := filepath.Join(dir, "allstill.db")
	server, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	ws, _ := server.Workspace(ctx)
	if _, _, err := server.CreateProject(ctx, Command("test"), ws, "Backups"); err != nil {
		t.Fatal(err)
	}
	st, err := OpenExisting(ctx, path)
	if err != nil {
		t.Fatalf("a database in use: %v", err)
	}
	defer st.Close()
	if _, err := st.ProjectByName(ctx, ws, "Backups"); err != nil {
		t.Errorf("what it holds: %v", err)
	}
}
