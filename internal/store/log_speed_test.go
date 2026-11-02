// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// With 3 million entries any page of the log reads in well under a second (R32). It
// takes a few minutes, so `make log-speed` runs it and `make check` does not.
func TestTheLogIsQuickAtThreeMillionEntries(t *testing.T) {
	if os.Getenv("ALLSTILL_LOG_SPEED") == "" {
		t.Skip("run with make log-speed")
	}
	ctx := t.Context()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "speed.db"))
	must(t, err)
	defer s.Close()
	ws, _ := s.Workspace(ctx)
	const total = 3_000_000
	const projects = 20
	r := rand.New(rand.NewSource(1))
	// One project holds a third of it; the last holds very little. A few kinds are rare.
	projectOf := func() string {
		switch n := r.Intn(100); {
		case n < 33:
			return "prj_big"
		case n == 99 && r.Intn(100) == 0:
			return "prj_small"
		default:
			return fmt.Sprintf("prj_%d", r.Intn(projects))
		}
	}
	kinds := []string{"check.state", "check.state", "check.late", "check.late", "message.reached", "message.reached", "check.changed", "check.state"}
	start := time.Now()
	for done := 0; done < total; {
		tx, err := s.w.BeginTx(ctx, nil)
		must(t, err)
		stmt, err := tx.PrepareContext(ctx, `INSERT INTO log_entries (workspace_id, happened_at, recorded_at, project_id, project_name, kind,
			about_kind, about_id, about_name, by_kind, by_id, by_name, person_id, person_name, details)
			VALUES (?, ?, ?, ?, 'A project', ?, 'check', ?, ?, 'person', ?, 'Someone', ?, 'Someone', '{"from":"up","to":"down","reason":"The job reported a failure."}')`)
		must(t, err)
		for range 20_000 {
			kind := kinds[r.Intn(len(kinds))]
			if r.Intn(100_000) == 0 {
				kind = "signin"
			}
			check := fmt.Sprintf("chk_%d", r.Intn(5000))
			if kind == "check.late" && r.Intn(2) == 0 {
				check = "chk_always_late" // a third of a million lines, on one check
			}
			person := fmt.Sprintf("per_%d", r.Intn(50))
			at := int64(done) * 1000
			_, err := stmt.ExecContext(ctx, ws, at, at, projectOf(), kind, check, check, person, person)
			must(t, err)
			done++
		}
		stmt.Close()
		must(t, tx.Commit())
	}
	t.Logf("wrote %d entries in %s", total, time.Since(start).Round(time.Second))
	if fi, err := os.Stat(filepath.Join(filepath.Dir(s.path(t)), "speed.db")); err == nil {
		t.Logf("%d MB, %d bytes per entry", fi.Size()>>20, fi.Size()/total)
	}

	access := func(role Role, ps ...string) Access {
		a := Access{WorkspaceID: ws, PersonID: "per_1"}
		for _, p := range ps {
			a.Projects = append(a.Projects, ProjectAccess{ID: p, Role: role})
		}
		return a
	}
	all := make([]string, 0, projects)
	for i := range projects {
		all = append(all, fmt.Sprintf("prj_%d", i))
	}
	admin := Access{WorkspaceID: ws, PersonID: "per_1", Admin: true}
	cases := []struct {
		name string
		a    Access
		f    LogFilter
	}{
		{"administrator, everything", admin, LogFilter{}},
		{"administrator, sign-ins, which are rare", admin, LogFilter{What: "signins"}},
		{"administrator, what was done", admin, LogFilter{What: "done"}},
		{"administrator, one person", admin, LogFilter{PersonID: "per_7"}},
		{"administrator, one check", admin, LogFilter{CheckID: "chk_42"}},
		{"administrator, an always-late check", admin, LogFilter{CheckID: "chk_always_late"}},
		{"administrator, an always-late check, late too", admin, LogFilter{CheckID: "chk_always_late", Late: true}},
		{"editor of the big project, late too", access(Editor, "prj_big"), LogFilter{Late: true}},
		{"administrator, a name", admin, LogFilter{Name: "chk_42"}},
		{"administrator, a name nothing has", admin, LogFilter{Name: "nothing"}},
		{"administrator, one project", admin, LogFilter{ProjectID: "prj_small"}},
		{"editor of the big project", access(Editor, "prj_big"), LogFilter{}},
		{"editor of the big project, who was reached", access(Editor, "prj_big"), LogFilter{What: "reached"}},
		{"editor of the big project, sign-ins", access(Editor, "prj_big"), LogFilter{What: "signins"}},
		{"editor of twenty projects", access(Editor, all...), LogFilter{}},
		{"viewer of the big project", access(Viewer, "prj_big"), LogFilter{}},
		{"viewer of the big project, who was reached", access(Viewer, "prj_big"), LogFilter{What: "reached"}},
		{"viewer of a small project", access(Viewer, "prj_small"), LogFilter{}},
		{"viewer of a project with nothing", access(Viewer, "prj_empty"), LogFilter{}},
		{"reader of one person they may not read", access(Viewer, "prj_small"), LogFilter{PersonID: "per_7"}},
		{"viewer of a project with nothing, late too", access(Viewer, "prj_empty"), LogFilter{Late: true}},
		{"administrator, a name, late too", admin, LogFilter{Name: "chk_always_late", Late: true}},
		{"administrator, a name with only late lines", admin, LogFilter{Name: "chk_always_late"}},
	}
	run := func(label string) {
		for _, c := range cases {
			begin := time.Now()
			p, err := s.ReadLog(ctx, c.a, c.f)
			must(t, err)
			took := time.Since(begin)
			t.Logf("%-8s %-45s %4d lines, older %-8v %s", label, c.name, len(p.Entries), p.Older != 0, took.Round(100*time.Microsecond))
			if took >= time.Second {
				t.Errorf("%s: %s took %s", label, c.name, took)
			}
		}
	}
	run("cold")
	_, err = s.w.ExecContext(ctx, "PRAGMA optimize=0x10002")
	must(t, err)
	run("analysed")
}

func (s *Store) path(t *testing.T) string {
	t.Helper()
	var seq int
	var name, file string
	must(t, s.r.QueryRowContext(t.Context(), "PRAGMA database_list").Scan(&seq, &name, &file))
	return file
}
