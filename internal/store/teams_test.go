// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/MartijnMul/allstill/internal/check"
)

// Before teams, this is how the store said what a person may see and who is told
// about a project. Kept here word for word, so the upgrade is compared with what
// it replaces and not with itself.
const (
	seenBeforeTeams = `
		SELECT per.id || ' ' || p.id || ' ' || CASE WHEN per.workspace_admin = 1 THEN 'owner' ELSE m.role END
		FROM projects p
		JOIN people per ON per.workspace_id = p.workspace_id
		LEFT JOIN memberships m ON m.project_id = p.id AND m.person_id = per.id
		WHERE p.deleted_at IS NULL AND per.disabled_at IS NULL
		  AND (m.role IS NOT NULL OR per.workspace_admin = 1)`
	toldBeforeTeams = `
		SELECT p.id || ' ' || pr.id
		FROM projects pr
		JOIN people p ON p.workspace_id = pr.workspace_id
		LEFT JOIN memberships m ON m.project_id = pr.id AND m.person_id = p.id
		WHERE (m.alerts = 1 OR (m.person_id IS NULL AND p.workspace_admin = 1))`
	ownRowsBeforeTeams = `SELECT person_id || ' ' || project_id || ' ' || role FROM memberships`
)

// beforeTeams is the schema as it was before the migration that adds teams.
func beforeTeams(t *testing.T) fstest.MapFS {
	t.Helper()
	schema := fstest.MapFS{}
	ms, err := list(migrations)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		if m.version >= 16 {
			break
		}
		body, _ := migrations.ReadFile(m.name)
		schema[m.name] = &fstest.MapFile{Data: body}
	}
	return schema
}

func lines(t *testing.T, conn *sql.DB, query string) []string {
	t.Helper()
	rows, err := conn.Query(query)
	if err != nil {
		t.Fatalf("%v\n%s", err, query)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var cols []any
		names, _ := rows.Columns()
		for range names {
			cols = append(cols, new(any))
		}
		if err := rows.Scan(cols...); err != nil {
			t.Fatal(err)
		}
		var parts []string
		for _, c := range cols {
			parts = append(parts, fmt.Sprintf("%v", *(c.(*any))))
		}
		out = append(out, strings.Join(parts, "|"))
	}
	sort.Strings(out)
	return out
}

// Everyone an old database could hold, with the two a database the code made
// cannot: an editor whose alerts are off and a viewer whose alerts are on.
const peopleBeforeTeams = `
	INSERT INTO workspaces (id, name, created_at) VALUES ('ws_a', 'Example', 5);
	INSERT INTO people (id, workspace_id, name, workspace_admin, created_at, disabled_at) VALUES
		('per_admin', 'ws_a', 'Admin', 1, 1, NULL),
		('per_admin_viewer', 'ws_a', 'Admin set to viewer', 1, 1, NULL),
		('per_admin_owner', 'ws_a', 'Admin with a row', 1, 1, NULL),
		('per_admin_gone', 'ws_a', 'Admin, disabled', 1, 1, 9),
		('per_admin_quiet', 'ws_a', 'Admin, an editor with alerts off', 1, 1, NULL),
		('per_owner', 'ws_a', 'Owner', 0, 1, NULL),
		('per_editor', 'ws_a', 'Editor', 0, 1, NULL),
		('per_viewer', 'ws_a', 'Viewer', 0, 1, NULL),
		('per_editor_quiet', 'ws_a', 'Editor, alerts off', 0, 1, NULL),
		('per_viewer_told', 'ws_a', 'Viewer, alerts on', 0, 1, NULL),
		('per_two', 'ws_a', 'In two projects', 0, 1, NULL),
		('per_gone', 'ws_a', 'Disabled', 0, 1, 9),
		('per_nobody', 'ws_a', 'In no project', 0, 1, NULL);
	INSERT INTO projects (id, workspace_id, name, ping_key_hash, recovery_messages, default_interval, default_grace, created_at, deleted_at) VALUES
		('prj_a', 'ws_a', 'Backups', zeroblob(32), 0, 120, 90, 2, NULL),
		('prj_b', 'ws_a', 'Hosts', NULL, 1, 86400, 3600, 3, NULL),
		('prj_empty', 'ws_a', 'Nobody in it', NULL, 1, 86400, 3600, 3, NULL),
		('prj_old', 'ws_a', 'Deleted', NULL, 1, 86400, 3600, 3, 8);
	INSERT INTO memberships (workspace_id, project_id, person_id, role, created_at, alerts) VALUES
		('ws_a', 'prj_a', 'per_admin_viewer', 'viewer', 4, 0),
		('ws_a', 'prj_a', 'per_admin_owner', 'owner', 4, 1),
		('ws_a', 'prj_b', 'per_admin_owner', 'viewer', 4, 1),
		('ws_a', 'prj_a', 'per_admin_gone', 'viewer', 4, 0),
		('ws_a', 'prj_b', 'per_admin_quiet', 'editor', 4, 0),
		('ws_a', 'prj_a', 'per_admin_quiet', 'owner', 4, 0),
		('ws_a', 'prj_a', 'per_owner', 'owner', 4, 1),
		('ws_a', 'prj_a', 'per_editor', 'editor', 4, 1),
		('ws_a', 'prj_a', 'per_viewer', 'viewer', 4, 0),
		('ws_a', 'prj_a', 'per_editor_quiet', 'editor', 4, 0),
		('ws_a', 'prj_a', 'per_viewer_told', 'viewer', 4, 1),
		('ws_a', 'prj_a', 'per_two', 'viewer', 4, 0),
		('ws_a', 'prj_b', 'per_two', 'editor', 4, 1),
		('ws_a', 'prj_old', 'per_two', 'owner', 4, 1),
		('ws_a', 'prj_a', 'per_gone', 'owner', 4, 1);
	INSERT INTO checks (id, workspace_id, project_id, ping_uuid, name, kind, schedule_kind,
		interval_seconds, grace_seconds, state_since, created_at, updated_at) VALUES
		('chk_a', 'ws_a', 'prj_a', '5c2b1d2e-8f3a-4b7c-9d1e-2f3a4b5c6d7e', 'nightly', 'ping', 'interval', 60, 60, 0, 0, 0),
		('chk_probe', 'ws_a', 'prj_b', '6c2b1d2e-8f3a-4b7c-9d1e-2f3a4b5c6d7e', 'Probe office', 'ping', 'interval', 60, 60, 0, 0, 0);
	INSERT INTO probes (id, workspace_id, name, key_hash, check_id, created_at) VALUES
		('prb_a', 'ws_a', 'office', x'0102', 'chk_probe', 6);
	INSERT INTO probe_projects (workspace_id, probe_id, project_id) VALUES ('ws_a', 'prb_a', 'prj_a'), ('ws_a', 'prb_a', 'prj_b');
	INSERT INTO api_keys (id, key_hash, workspace_id, project_id, name, scope, made_by_kind, made_by_id, made_by_name,
		made_by_outsider, made_via, created_at) VALUES
		('key_a', randomblob(32), 'ws_a', 'prj_a', 'deploy', 'manage', 'person', 'per_admin', 'Admin', 1, '', 7);`

// What an upgrade must carry as it is, row for row.
var carriedByTeams = []string{
	`SELECT id, workspace_id, name, hex(ping_key_hash), recovery_messages, default_interval, default_grace, created_at, deleted_at FROM projects`,
	`SELECT id, workspace_id, name, hex(key_hash), check_id, created_at FROM probes`,
	`SELECT * FROM probe_projects`,
	`SELECT * FROM api_keys`,
	`SELECT id, project_id, probe_id, ping_uuid FROM checks`,
	`SELECT * FROM people`,
}

// Nobody sees more or less, or is told about more or less, after the upgrade
// than before it (intent/teams/spec.md, R14 and R16).
func TestTheUpgradeToTeamsChangesNothingAnyoneSees(t *testing.T) {
	path := filepath.Join(t.TempDir(), "allstill.db")
	old, err := openWith(t.Context(), path, beforeTeams(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.w.Exec(peopleBeforeTeams); err != nil {
		t.Fatal(err)
	}
	// Keys a job and a probe hold, which must open the same doors afterwards.
	if _, err := old.w.Exec(`UPDATE projects SET ping_key_hash = ? WHERE id = 'prj_a'`, hash("a-ping-key")); err != nil {
		t.Fatal(err)
	}
	if _, err := old.w.Exec(`UPDATE probes SET key_hash = ? WHERE id = 'prb_a'`, hash("alp_a-probe-key")); err != nil {
		t.Fatal(err)
	}
	seen, told, own := lines(t, old.r, seenBeforeTeams), lines(t, old.r, toldBeforeTeams), lines(t, old.r, ownRowsBeforeTeams)
	var carried [][]string
	for _, q := range carriedByTeams {
		carried = append(carried, lines(t, old.r, q))
	}
	old.Close()
	if len(seen) < 15 || len(told) < 12 {
		t.Fatalf("the fixture says little: %d seen, %d told", len(seen), len(told))
	}

	s, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	var seenNow []string
	for _, person := range lines(t, s.r, "SELECT id FROM people") {
		a, err := s.AccessOfPerson(t.Context(), "ws_a", person)
		if errors.Is(err, ErrNotFound) {
			continue // disabled
		}
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range a.Projects {
			seenNow = append(seenNow, person+" "+p.ID+" "+string(p.Role))
		}
	}
	sort.Strings(seenNow)
	if !reflect.DeepEqual(seenNow, seen) {
		t.Errorf("what people see changed.\nbefore: %v\nafter:  %v", seen, seenNow)
	}
	if now := lines(t, s.r, "SELECT person_id || ' ' || project_id FROM project_alerted"); !reflect.DeepEqual(now, told) {
		t.Errorf("who is told changed.\nbefore: %v\nafter:  %v", told, now)
	}
	// A row of one's own stays, but for an owner of the team: theirs gave nothing,
	// and went in the second step (0017), their choice about alerts staying.
	var kept []string
	for _, row := range own {
		if !strings.HasPrefix(row, "per_admin") {
			kept = append(kept, row)
		}
	}
	if len(kept) == len(own) {
		t.Fatal("the fixture has no administrator with a row of their own")
	}
	if now := lines(t, s.r, ownRowsBeforeTeams); !reflect.DeepEqual(now, kept) {
		t.Errorf("people's own rows changed.\nbefore: %v\nafter:  %v", kept, now)
	}
	for i, q := range carriedByTeams {
		if now := lines(t, s.r, q); !reflect.DeepEqual(now, carried[i]) {
			t.Errorf("%s\nbefore: %v\nafter:  %v", q, carried[i], now)
		}
	}

	// Jobs and probes notice nothing (R45): a ping to a URL from before, one by
	// the project's key that makes a check, and a probe asking for its work.
	at := time.Now()
	if _, err := s.Ping(t.Context(), ping("5c2b1d2e-8f3a-4b7c-9d1e-2f3a4b5c6d7e", check.Success, "", at)); err != nil {
		t.Errorf("a ping to a URL from before the upgrade: %v", err)
	}
	if got := lines(t, s.r, "SELECT state FROM checks WHERE id = 'chk_a'"); got[0] != "up" {
		t.Errorf("the check that was pinged is %s", got[0])
	}
	bySlug := ping("", check.Success, "", at)
	bySlug.PingKey, bySlug.Slug, bySlug.Create = "a-ping-key", "new-job", true
	if out, err := s.Ping(t.Context(), bySlug); err != nil || !out.Created {
		t.Errorf("a ping by the project's key: %+v, %v", out, err)
	}
	probe, err := s.ProbeByKey(t.Context(), "alp_a-probe-key")
	if err != nil || probe.ID != "prb_a" || probe.CheckID != "chk_probe" {
		t.Errorf("the probe by its key: %+v, %v", probe, err)
	}
	if _, err := s.ChecksOfProbe(t.Context(), probe.ID); err != nil {
		t.Errorf("the probe's work: %v", err)
	}

	// One team, named as the installation, owned by its administrators and holding
	// every project and probe.
	if got := lines(t, s.r, "SELECT name, created_at FROM teams"); !reflect.DeepEqual(got, []string{"Example|5"}) {
		t.Errorf("teams: %v", got)
	}
	want := []string{"per_admin_gone|owner", "per_admin_owner|owner", "per_admin_quiet|owner", "per_admin_viewer|owner", "per_admin|owner"}
	if got := lines(t, s.r, "SELECT person_id, role FROM team_members"); !reflect.DeepEqual(got, want) {
		t.Errorf("the team's members: %v", got)
	}
	if got := lines(t, s.r, `SELECT count(*) FROM projects p JOIN teams t ON t.id = p.team_id UNION ALL
		SELECT count(*) FROM probes p JOIN teams t ON t.id = p.team_id`); !reflect.DeepEqual(got, []string{"1", "4"}) {
		t.Errorf("projects and probes in the team: %v", got)
	}
	// Only who was told otherwise than their role gives has a choice.
	want = []string{"per_admin_gone|prj_a|0", "per_admin_quiet|prj_a|0", "per_admin_quiet|prj_b|0", "per_admin_viewer|prj_a|0", "per_editor_quiet|prj_a|0", "per_viewer_told|prj_a|1"}
	if got := lines(t, s.r, "SELECT person_id, project_id, alerts FROM alert_choices"); !reflect.DeepEqual(got, want) {
		t.Errorf("alert choices: %v", got)
	}
}

// A database with no workspace yet, which a server that never started has,
// upgrades to no team, and gets one with its workspace.
func TestAnEmptyDatabaseGetsItsTeamWithItsWorkspace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "allstill.db")
	old, err := openWith(t.Context(), path, beforeTeams(t))
	if err != nil {
		t.Fatal(err)
	}
	old.Close()
	s, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if n := count(t, s, "teams"); n != 0 {
		t.Fatalf("%d teams before a workspace", n)
	}
	ws, err := s.Workspace(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Workspace(t.Context()); err != nil {
		t.Fatal(err)
	}
	if n := count(t, s, "teams"); n != 1 {
		t.Fatalf("%d teams after the workspace was made", n)
	}
	admin, err := s.CreatePerson(t.Context(), Command("test"), ws, NewPerson{Name: "Alice", WorkspaceAdmin: true})
	if err != nil {
		t.Fatal(err)
	}
	project, _, err := s.CreateProject(t.Context(), Command("test"), ws, "Backups")
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.AccessOfPerson(t.Context(), ws, admin)
	if role, ok := a.Role(project); err != nil || !ok || role != Owner {
		t.Fatalf("a new administrator in a new project: %q, %v, %v", role, ok, err)
	}
	if got := lines(t, s.r, "SELECT count(*) FROM project_alerted"); got[0] != "1" {
		t.Fatalf("a new administrator is told about a new project: %v", got)
	}
}

// queriesNamed reads the store's queries from its .sql files, by name.
func queriesNamed(t *testing.T) map[string]string {
	t.Helper()
	files, err := filepath.Glob("queries/*.sql")
	if err != nil || len(files) == 0 {
		t.Fatalf("no queries: %v", err)
	}
	out := map[string]string{}
	for _, f := range files {
		body, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, part := range strings.Split(string(body), "-- name: ")[1:] {
			name, rest, _ := strings.Cut(part, " ")
			_, text, _ := strings.Cut(rest, "\n")
			// The comment above the next query is not this one's.
			var kept []string
			for _, line := range strings.Split(text, "\n") {
				if !strings.HasPrefix(line, "--") {
					kept = append(kept, line)
				}
			}
			out[name] = strings.Join(kept, "\n")
		}
	}
	return out
}

// Who is told is looked up, row by row, as it was before the views: no query
// builds a view in memory or reads a whole table of roles.
func TestWhoIsToldIsLookedUp(t *testing.T) {
	s := open(t)
	queries := queriesNamed(t)
	fill := regexp.MustCompile(`sqlc\.arg\(\w+\)|sqlc\.slice\(\w+\)|\?`)
	for _, name := range []string{"AddFollowers", "DueRecipients", "DigestIncidents", "SilentChecks", "WarningChecks",
		"ProjectsNobodyFollows", "PeopleWhoGetAlerts", "StillFollowed", "ProjectsOfPerson", "IsAlerted", "HighestRoleInProject"} {
		q, ok := queries[name]
		if !ok {
			t.Fatalf("no query named %s", name)
		}
		rows, err := s.r.QueryContext(t.Context(), "EXPLAIN QUERY PLAN "+fill.ReplaceAllString(q, "'x'"))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var plan []string
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				t.Fatal(err)
			}
			plan = append(plan, detail)
		}
		rows.Close()
		text := strings.Join(plan, "; ")
		for _, bad := range []string{"MATERIALIZE", "CO-ROUTINE", "SCAN tm", "SCAN m", "SCAN a", "SCAN r", "SCAN project_"} {
			if strings.Contains(text+";", bad+" ") || strings.Contains(text+";", bad+";") {
				t.Errorf("%s: %s in %s", name, bad, text)
			}
		}
	}
}

// How a role in a project was given and taken before teams, word for word.
const (
	addMemberBeforeTeams = `
		INSERT INTO memberships (workspace_id, project_id, person_id, role, created_at, alerts)
		VALUES ('ws_a', 'prj_a', ?1, ?2, 0, ?3)
		ON CONFLICT (project_id, person_id) DO UPDATE SET
		    role = excluded.role,
		    alerts = CASE
		        WHEN memberships.role = 'viewer' AND excluded.role <> 'viewer' THEN 1
		        WHEN memberships.role <> 'viewer' AND excluded.role = 'viewer' THEN 0
		        ELSE memberships.alerts
		    END`
	removeMemberBeforeTeams = `DELETE FROM memberships WHERE project_id = 'prj_a' AND person_id = ?1`
)

// After the upgrade, giving and taking a role in a project leaves everyone told
// or not told exactly as it did before, whatever came before it: every run of
// up to four changes, for an administrator and for someone who is not, starting
// with no row, and with the two rows only an old database holds, whose flag
// says otherwise than the role and is kept while the role stays on its side of
// viewer.
func TestChangingARoleTellsWhoItDidBeforeTeams(t *testing.T) {
	old, err := openWith(t.Context(), filepath.Join(t.TempDir(), "old.db"), beforeTeams(t))
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	if _, err := old.w.Exec(`INSERT INTO workspaces (id, name, created_at) VALUES ('ws_a', 'A', 0);
		INSERT INTO projects (id, workspace_id, name, created_at) VALUES ('prj_a', 'ws_a', 'A', 0)`); err != nil {
		t.Fatal(err)
	}
	s := open(t)
	ctx := t.Context()
	ws, err := s.Workspace(ctx)
	if err != nil {
		t.Fatal(err)
	}
	project, _, err := s.CreateProject(ctx, Command("test"), ws, "A")
	if err != nil {
		t.Fatal(err)
	}
	steps := []string{"viewer", "editor", "owner", "none"}
	var runs [][]string
	var grow func(run []string)
	grow = func(run []string) {
		if len(run) > 0 {
			runs = append(runs, append([]string(nil), run...))
		}
		if len(run) == 4 {
			return
		}
		for _, step := range steps {
			grow(append(run, step))
		}
	}
	grow(nil)
	type start struct {
		role   string
		alerts bool
	}
	for _, from := range []start{{}, {"editor", false}, {"viewer", true}} {
		for _, admin := range []bool{false, true} {
			for n, run := range runs {
				// An administrator owns the team, so keeps no row of their own, and
				// what such a row said is a choice. The two rows only an old database
				// holds cannot be told from those the code makes once the row has
				// gone, so they are played for someone who is not in the team.
				if admin && from.role != "" {
					continue
				}
				oldID := fmt.Sprintf("per_%s%v_%v_%d", from.role, from.alerts, admin, n)
				if _, err := old.w.Exec(`INSERT INTO people (id, workspace_id, name, workspace_admin, created_at) VALUES (?, 'ws_a', 'x', ?, 0)`, oldID, admin); err != nil {
					t.Fatal(err)
				}
				person, err := s.CreatePerson(ctx, Command("test"), ws, NewPerson{Name: "x", WorkspaceAdmin: admin})
				if err != nil {
					t.Fatal(err)
				}
				if from.role != "" {
					if _, err := old.w.Exec(`INSERT INTO memberships (workspace_id, project_id, person_id, role, created_at, alerts)
					VALUES ('ws_a', 'prj_a', ?, ?, 0, ?)`, oldID, from.role, from.alerts); err != nil {
						t.Fatal(err)
					}
					// As the migration carries it: the row, and a choice where the flag
					// says otherwise than the highest role, an administrator's being owner.
					if _, err := s.w.Exec(`INSERT INTO memberships (workspace_id, project_id, person_id, role, created_at) VALUES (?, ?, ?, ?, 0)`,
						ws, project, person, from.role); err != nil {
						t.Fatal(err)
					}
					if from.alerts != (admin || from.role != "viewer") {
						if _, err := s.w.Exec(`INSERT INTO alert_choices (workspace_id, project_id, person_id, alerts) VALUES (?, ?, ?, ?)`,
							ws, project, person, from.alerts); err != nil {
							t.Fatal(err)
						}
					}
				}
				for i, step := range run {
					if step == "none" {
						_, err = old.w.Exec(removeMemberBeforeTeams, oldID)
						if err == nil {
							if err = s.RemoveMember(ctx, Command("test"), ws, project, person); errors.Is(err, ErrNotFound) {
								err = nil
							}
						}
					} else {
						_, err = old.w.Exec(addMemberBeforeTeams, oldID, step, step != "viewer")
						if err == nil {
							err = s.SetMember(ctx, Command("test"), ws, project, person, Role(step))
						}
					}
					if err != nil {
						t.Fatal(err)
					}
					one := func(conn *sql.DB, query, person string) string {
						t.Helper()
						var v string
						if err := conn.QueryRow(query, person).Scan(&v); err != nil {
							t.Fatalf("%v\n%s", err, query)
						}
						return v
					}
					const row = `SELECT COALESCE((SELECT role FROM memberships WHERE person_id = ?), 'no row')`
					before := one(old.w, `SELECT count(*) FROM (`+toldBeforeTeams+` AND p.id = ?)`, oldID) + " " + one(old.w, row, oldID)
					after := one(s.w, `SELECT count(*) FROM project_alerted WHERE person_id = ?`, person) + " " + one(s.w, row, person)
					if admin {
						// Who is told, not which row: an owner of the team has none.
						if after != before[:1]+" no row" {
							t.Fatalf("administrator, after %v: told was %q before teams and told and row are %q now", run[:i+1], before[:1], after)
						}
						continue
					}
					if before != after {
						t.Fatalf("administrator %v, from %+v, after %v: told and row were %q before teams and are %q now",
							admin, from, run[:i+1], before, after)
					}
				}
			}
		}
	}
	// Nothing is kept that says no more than the role does.
	if got := lines(t, s.r, `SELECT count(*) FROM alert_choices a JOIN project_roles r ON r.project_id = a.project_id AND r.person_id = a.person_id
		WHERE a.alerts = (r.role <> 'viewer')`); got[0] != "0" {
		t.Errorf("%s choices that say what the role says", got[0])
	}
}

// The rule, against the views: the highest role counts, an owner or editor is
// told and a viewer is not, unless they chose otherwise.
func TestTheHighestRoleCountsAndAChoiceDecides(t *testing.T) {
	s := open(t)
	ctx := t.Context()
	ws, err := s.Workspace(ctx)
	if err != nil {
		t.Fatal(err)
	}
	project, _, err := s.CreateProject(ctx, Command("test"), ws, "A")
	if err != nil {
		t.Fatal(err)
	}
	rank := map[string]int{"": 0, "viewer": 1, "editor": 2, "owner": 3}
	roles := []string{"", "viewer", "editor", "owner"}
	for _, team := range roles {
		for _, own := range roles {
			for _, choice := range []string{"", "0", "1"} {
				person, err := s.CreatePerson(ctx, Command("test"), ws, NewPerson{Name: "x"})
				if err != nil {
					t.Fatal(err)
				}
				exec := func(q string, args ...any) {
					t.Helper()
					if _, err := s.w.Exec(q, args...); err != nil {
						t.Fatal(err)
					}
				}
				if team != "" {
					exec(`INSERT INTO team_members (workspace_id, team_id, person_id, role, created_at) SELECT ?, id, ?, ?, 0 FROM teams`, ws, person, team)
				}
				if own != "" {
					exec(`INSERT INTO memberships (workspace_id, project_id, person_id, role, created_at) VALUES (?, ?, ?, ?, 0)`, ws, project, person, own)
				}
				if choice != "" {
					exec(`INSERT INTO alert_choices (workspace_id, project_id, person_id, alerts) VALUES (?, ?, ?, ?)`, ws, project, person, choice)
				}
				want := team
				if rank[own] > rank[team] {
					want = own
				}
				wantTold := want == "editor" || want == "owner"
				if choice != "" {
					wantTold = choice == "1"
				}
				if want == "" {
					wantTold = false // a choice gives nobody a way in
				}
				var got string
				var told bool
				s.w.QueryRow(`SELECT role FROM project_roles WHERE person_id = ?`, person).Scan(&got)
				s.w.QueryRow(`SELECT count(*) FROM project_alerted WHERE person_id = ?`, person).Scan(&told)
				if got != want || told != wantTold {
					t.Errorf("team %q, own %q, choice %q: role %q and told %v, want %q and %v", team, own, choice, got, told, want, wantTold)
				}
				a, err := s.AccessOfPerson(ctx, ws, person)
				if err != nil {
					t.Fatal(err)
				}
				if role, ok := a.Role(project); string(role) != want || ok != (want != "") {
					t.Errorf("team %q, own %q: access says %q, %v", team, own, role, ok)
				}
			}
		}
	}
}

// Who has which role and who is told are each said in one place, the two views.
// A query that works either out for itself would drift from them (R42).
func TestRolesAndAlertsAreSaidInOnePlace(t *testing.T) {
	admin := regexp.MustCompile(`(?s)workspace_admin.*(memberships|team_members)|(memberships|team_members).*workspace_admin`)
	writers := map[string]bool{"SetAlertChoice": true, "ClearAlertChoice": true, "ClearAlertChoicesOutside": true}
	// Who is in a team, which is not the same question as their role in a project:
	// the team's own members, and whether a row of one's own gives more than it.
	teams := map[string]bool{
		"AddTeamMember": true, "RemoveTeamMember": true, "TeamsOfPerson": true, "RoleInTeam": true, "OwnersOfTeam": true,
		"RoleInTeamOfProject": true,
	}
	for name, q := range queriesNamed(t) {
		if strings.Contains(q, "alert_choices") && !writers[name] {
			t.Errorf("%s reads alert_choices: ask project_alerted", name)
		}
		if admin.MatchString(q) {
			t.Errorf("%s works out a role from workspace_admin: ask project_roles", name)
		}
		if strings.Contains(q, "team_members") && !teams[name] {
			t.Errorf("%s reads team_members: ask project_roles", name)
		}
	}
}

// With a second team, a project made without saying where is refused, not guessed.
func TestSeveralTeamsAreNotGuessedBetween(t *testing.T) {
	s := open(t)
	ws, err := s.Workspace(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.w.Exec(`INSERT INTO teams (id, workspace_id, name, created_at) VALUES ('team_b', ?, 'B', 0)`, ws); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CreateProject(t.Context(), Command("test"), ws, "A"); !errors.Is(err, ErrSeveralTeams) {
		t.Fatalf("a project with two teams: %v", err)
	}
	if _, err := s.CreatePerson(t.Context(), Command("test"), ws, NewPerson{Name: "x", WorkspaceAdmin: true}); !errors.Is(err, ErrSeveralTeams) {
		t.Fatalf("an administrator with two teams: %v", err)
	}
}

// teamOf is the team a project belongs to.
func teamOf(t *testing.T, s *Store, projectID string) string {
	t.Helper()
	var id string
	if err := s.r.QueryRow(`SELECT team_id FROM projects WHERE id = ?`, projectID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}
