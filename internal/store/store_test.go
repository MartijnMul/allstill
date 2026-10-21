// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
		ms, _ := list()
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
