// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bytes"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/MartijnMul/allstill/internal/alert"
	"github.com/MartijnMul/allstill/internal/check"
	"github.com/MartijnMul/allstill/internal/store"
)

func TestCompareHealthchecks(t *testing.T) {
	ctx := t.Context()
	dbPath := filepath.Join(t.TempDir(), "allstill.db")
	st, ws, err := openStore(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	home, _, _ := st.CreateProject(ctx, ws, "Home")
	admin, _ := st.AdminAccess(ctx, ws)
	uuids := map[string]string{
		"backup":    "31365bce-8da9-4729-8ff3-aaa71d56b712",
		"heartbeat": "41365bce-8da9-4729-8ff3-aaa71d56b712",
		"laptop":    "51365bce-8da9-4729-8ff3-aaa71d56b712",
		"spare":     "71365bce-8da9-4729-8ff3-aaa71d56b712",
	}
	for name, u := range uuids {
		if _, err := st.CreateCheck(ctx, admin, home, store.NewCheck{Name: name, PingUUID: u, Interval: time.Hour, Grace: time.Hour}); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC().Truncate(time.Second)
	ago := func(m int) time.Time { return now.Add(-time.Duration(m) * time.Minute) }
	ping := func(name string, k check.Kind, at time.Time) {
		t.Helper()
		if _, err := st.Ping(ctx, store.PingIn{CheckUUID: uuids[name], Kind: k, ExitStatus: -1, Method: "POST", At: at}); err != nil {
			t.Fatal(err)
		}
	}
	// The backup failed for ten minutes; the heartbeat kept pinging.
	ping("backup", check.Fail, ago(50))
	ping("backup", check.Success, ago(40))
	if _, err := st.SettleIncidents(ctx, ago(25), alert.Settle); err != nil {
		t.Fatal(err)
	}
	ping("heartbeat", check.Success, ago(50))
	st.Close()
	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	// Added to Allstill two hours ago.
	if _, err := raw.Exec("UPDATE checks SET created_at = ?", ago(120).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	raw.Close()

	// Healthchecks saw the backup the same, the heartbeat down for ten
	// minutes, and has a check Allstill does not.
	flips := map[string]string{
		uuids["backup"]:    fmt.Sprintf(`{"flips": [{"timestamp": %q, "up": 1}, {"timestamp": %q, "up": 0}]}`, ago(39).Format(time.RFC3339), ago(51).Format(time.RFC3339)),
		uuids["heartbeat"]: fmt.Sprintf(`{"flips": [{"timestamp": %q, "up": 1}, {"timestamp": %q, "up": 0}]}`, ago(20).Format(time.RFC3339), ago(30).Format(time.RFC3339)),
	}
	hc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v3/checks/" {
			fmt.Fprintf(w, `{"checks": [{"name": "backup", "uuid": %q}, {"name": "heartbeat", "uuid": %q}, {"name": "laptop", "uuid": %q, "last_ping": %q}, {"name": "spare", "uuid": %q, "last_ping": null},
				{"name": "elsewhere", "uuid": "61365bce-8da9-4729-8ff3-aaa71d56b712"}]}`, uuids["backup"], uuids["heartbeat"], uuids["laptop"], ago(15).Format(time.RFC3339), uuids["spare"])
			return
		}
		u := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v3/checks/"), "/flips/")
		if f, ok := flips[u]; ok {
			w.Write([]byte(f))
			return
		}
		http.NotFound(w, r)
	}))
	defer hc.Close()

	t.Setenv("HEALTHCHECKS_API_KEY", "rw-key")
	var out, errOut bytes.Buffer
	if err := run(ctx, []string{"compare", "healthchecks", "-db", dbPath, "-url", hc.URL, "-tz", "UTC", "-since", ago(24 * 60).Format(time.DateOnly)}, &out, &errOut); err != nil {
		t.Fatalf("%v: %s", err, errOut.String())
	}
	for _, want := range []string{
		"Home / backup: the same, down once at both\n",
		"Home / heartbeat:\n  " + ago(30).Format("Mon 2 Jan 15:04") + "  missed: down at Healthchecks for 10m, never at Allstill\n",
		"Home / laptop: never pinged Allstill, so it cannot alert; Healthchecks last heard from it " + ago(15).Format("Mon 2 Jan 15:04") + "\n",
		"Home / spare: never pinged Allstill, so it cannot alert; Healthchecks never heard from it either\n",
		"1 the same, 1 different, 2 never pinged Allstill.\nNot in Allstill: elsewhere.\n",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in:\n%s", want, out.String())
		}
	}

	out.Reset()
	if err := run(ctx, []string{"compare", "healthchecks", "-db", dbPath, "-url", hc.URL, "-since", "2026-10-01", "-project", "Elsewhere"}, &out, &errOut); err == nil {
		t.Fatal("a project with no checks at Healthchecks")
	}
}
