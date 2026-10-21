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
	person, _ := st.CreatePerson(ctx, ws, store.NewPerson{Name: "Operator", WorkspaceAdmin: true})
	project, _, _ := st.CreateProject(ctx, ws, "Servers")
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
