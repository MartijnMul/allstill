// SPDX-License-Identifier: AGPL-3.0-only

//go:build unix

package main

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/MartijnMul/allstill/internal/store"
)

// The server and `allstill run` together: a job reports through the real
// binary's commands, and the check and its run are in the database.
func TestServeAndRun(t *testing.T) {
	ctx := t.Context()
	dbPath := filepath.Join(t.TempDir(), "allstill.db")
	st, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	ws, _ := st.Workspace(ctx)
	person, _ := st.CreatePerson(ctx, store.Command("test"), ws, store.NewPerson{Name: "Operator", WorkspaceAdmin: true})
	project, _, _ := st.CreateProject(ctx, store.Command("test"), ws, "Servers")
	access, _ := st.AccessOfPerson(ctx, ws, person)
	c, err := st.CreateCheck(ctx, access, project, store.NewCheck{Name: "Backup", Interval: time.Hour, Grace: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	st.Close()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	serveCtx, stop := context.WithCancel(ctx)
	var serveLog bytes.Buffer
	served := make(chan error, 1)
	go func() {
		served <- run(serveCtx, []string{"serve", "-db", dbPath, "-listen", addr}, &bytes.Buffer{}, &serveLog)
	}()
	for i := 0; ; i++ {
		resp, err := http.Get("http://" + addr + "/ping/not-a-check")
		if err == nil {
			resp.Body.Close()
			break
		}
		if i == 50 {
			t.Fatalf("the server did not start: %v\n%s", err, serveLog.String())
		}
		time.Sleep(50 * time.Millisecond)
	}

	resp, err := http.Get("http://" + addr + "/login")
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("the sign-in page: %v %v", resp, err)
	}
	resp.Body.Close()

	var out, errOut bytes.Buffer
	err = run(ctx, []string{"run", "-url", "http://" + addr + "/ping/" + c.PingUUID, "--", "sh", "-c",
		`echo "files=42" > "$ALLSTILL_VALUES_FILE"; echo done; exit 2`}, &out, &errOut)
	var exit exitStatus
	if !errors.As(err, &exit) || exit != 2 || out.String() != "done\n" || errOut.Len() != 0 {
		t.Fatalf("run: %v, out %q, err %q", err, out.String(), errOut.String())
	}
	stop()
	if err := <-served; err != nil {
		t.Fatalf("serve: %v", err)
	}

	// The admin commands print links to the address the server ran with.
	out.Reset()
	if err := run(ctx, []string{"admin", "create-user", "-db", dbPath, "-name", "Ada", "-email", "ada@example.com"}, &out, &errOut); err != nil {
		t.Fatalf("create-user: %v %s", err, errOut.String())
	}
	_, port, _ := net.SplitHostPort(addr)
	if !strings.Contains(out.String(), "http://localhost:"+port+"/setup#") {
		t.Fatalf("create-user printed %q", out.String())
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var state, outcome, measured string
	var exitCode int
	db.QueryRow("SELECT state FROM checks WHERE id = ?", c.ID).Scan(&state)
	db.QueryRow("SELECT outcome, exit_status, measured FROM runs WHERE check_id = ?", c.ID).Scan(&outcome, &exitCode, &measured)
	if state != "down" || outcome != "fail" || exitCode != 2 || measured != `{"files":42}` {
		t.Fatalf("check %s, run %s %d %s", state, outcome, exitCode, measured)
	}
}

func TestAdminCommands(t *testing.T) {
	ctx := t.Context()
	dbPath := filepath.Join(t.TempDir(), "allstill.db")
	var out, errOut bytes.Buffer
	admin := func(args ...string) error {
		out.Reset()
		errOut.Reset()
		return run(ctx, append([]string{"admin"}, args...), &out, &errOut)
	}
	if err := admin("create-user", "-db", dbPath, "-name", "Ada"); err == nil || !strings.Contains(err.Error(), "base-url") {
		t.Fatalf("without a base URL: %v", err)
	}
	if err := admin("create-user", "-db", dbPath, "-name", "Ada", "-base-url", "http://allstill.example.com"); err == nil {
		t.Fatal("a base URL over http was accepted")
	}
	if err := admin("create-user", "-db", dbPath, "-name", "Ada", "-email", "ada@example.com", "-time-zone", "Mars/Olympus"); err == nil {
		t.Fatal("an unknown time zone was accepted")
	}
	if err := admin("create-user", "-db", dbPath, "-name", "Ada", "-email", "ada@example.com", "-time-zone", "Europe/Amsterdam",
		"-base-url", "https://allstill.example.com"); err != nil {
		t.Fatal(err)
	}
	first := out.String()
	if !strings.Contains(first, "https://allstill.example.com/setup#") {
		t.Fatalf("create-user printed %q", first)
	}
	if err := admin("create-user", "-db", dbPath, "-name", "Ada again", "-email", "ADA@example.com", "-base-url", "https://allstill.example.com"); err == nil {
		t.Fatal("a second person with the same email address")
	}
	if err := admin("passkey-link", "-db", dbPath, "-email", "ada@example.com", "-base-url", "https://allstill.example.com"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "for Ada") || out.String() == first {
		t.Fatalf("passkey-link printed %q", out.String())
	}
	if err := admin("passkey-link", "-db", dbPath, "-email", "bob@example.com", "-base-url", "https://allstill.example.com"); err == nil {
		t.Fatal("a link for nobody")
	}
	copyPath := filepath.Join(t.TempDir(), "copy.db")
	if err := run(ctx, []string{"backup", "-db", dbPath, copyPath}, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if err := admin("passkey-link", "-db", copyPath, "-email", "ada@example.com", "-base-url", "https://allstill.example.com"); err != nil {
		t.Fatalf("Ada is not in the copy: %v", err)
	}
}

// An import put the checks of a customer and a home lab in one project; they
// are split by tag and by name.
func TestMoveChecksAndRenameProject(t *testing.T) {
	ctx := t.Context()
	dbPath := filepath.Join(t.TempDir(), "allstill.db")
	st, ws, err := openStore(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	lab, _, _ := st.CreateProject(ctx, store.Command("test"), ws, "Imported")
	admin, _ := st.AdminAccess(ctx, ws, "test")
	for _, c := range []store.NewCheck{
		{Name: "printer-api", Tags: []string{"customer"}},
		{Name: "dashboard", Tags: []string{"customer", "web"}},
		{Name: "shop-backup", Tags: []string{"backup"}},
		{Name: "hypervisor-backup", Tags: []string{"backup"}},
		{Name: "shop-backup", Slug: "shop-db", Tags: []string{"database"}},
	} {
		c.Interval, c.Grace = time.Hour, time.Hour
		if _, err := st.CreateCheck(ctx, admin, lab, c); err != nil {
			t.Fatal(err)
		}
	}
	st.Close()

	var out, errOut bytes.Buffer
	adminCmd := func(args ...string) error {
		out.Reset()
		errOut.Reset()
		return run(ctx, append([]string{"admin"}, args...), &out, &errOut)
	}
	if err := adminCmd("move-checks", "-db", dbPath, "-from", "Imported", "-to", "Customer", "-tag", "customer"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Created project Customer") || !strings.Contains(out.String(), "2 of 2 checks moved") {
		t.Fatalf("move by tag printed %q", out.String())
	}
	if err := adminCmd("move-checks", "-db", dbPath, "-from", "Imported", "-to", "Shop", "-check", "shop-backup", "-check", "nothing"); err != nil {
		t.Fatal(err)
	}
	// Both checks named shop-backup go; the one pinged by slug says so.
	if !strings.Contains(errOut.String(), `no check named "nothing"`) || !strings.Contains(out.String(), "2 of 2 checks moved") ||
		!strings.Contains(out.String(), "shop-backup (pinged by slug") {
		t.Fatalf("move by name printed %q / %q", out.String(), errOut.String())
	}
	if err := adminCmd("move-checks", "-db", dbPath, "-from", "Imported", "-to", "Shop", "-tag", "customer"); err == nil {
		t.Fatal("a move that matched nothing")
	}
	if err := adminCmd("move-checks", "-db", dbPath, "-from", "Shop", "-to", "Shop", "-tag", "backup"); err == nil {
		t.Fatal("a move within one project")
	}
	if err := adminCmd("set-alerting", "-db", dbPath, "-project", "Customer", "-check", "printer-api", "-urgency", "digest", "-critical"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "printer-api in Customer: digest, critical") {
		t.Fatalf("set-alerting printed %q", out.String())
	}
	if err := adminCmd("set-alerting", "-db", dbPath, "-project", "Shop", "-check", "shop-backup"); err == nil {
		t.Fatal("two checks with one name")
	}
	if err := adminCmd("set-alerting", "-db", dbPath, "-project", "Customer", "-check", "printer-api", "-urgency", "loud"); err == nil {
		t.Fatal("an urgency that does not exist")
	}
	if err := adminCmd("rename-project", "-db", dbPath, "Imported", "Home lab"); err != nil {
		t.Fatal(err)
	}

	st, ws, _ = openStore(ctx, dbPath)
	defer st.Close()
	admin, _ = st.AdminAccess(ctx, ws, "test")
	checks, _ := st.Checks(ctx, admin)
	got := map[string]string{}
	for _, c := range checks {
		got[c.Name] = c.ProjectName
	}
	want := map[string]string{"printer-api": "Customer", "dashboard": "Customer", "shop-backup": "Shop", "hypervisor-backup": "Home lab"}
	for name, project := range want {
		if got[name] != project {
			t.Errorf("%s is in %q, want %q", name, got[name], project)
		}
	}
}

// Someone typing 0 asks for the log not to be kept, which is not on offer: to the
// store, zero would mean the default of 400 days (intent/activity-log/spec.md, R35).
func TestTheLogCannotBeKeptTooShort(t *testing.T) {
	for _, days := range []string{"0", "-1", "29"} {
		var stderr bytes.Buffer
		err := run(t.Context(), []string{"serve", "-db", filepath.Join(t.TempDir(), "a.db"), "-keep-log", days}, &bytes.Buffer{}, &stderr)
		if err == nil || !strings.Contains(err.Error(), "at least 30 days") {
			t.Errorf("-keep-log %s: %v", days, err)
		}
	}
}
