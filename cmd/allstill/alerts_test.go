// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MartijnMul/allstill/internal/store"
)

func TestSetAlerts(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "allstill.db")
	st, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	ws, _ := st.Workspace(ctx)
	st.CreatePerson(ctx, store.Command("test"), ws, store.NewPerson{Name: "Ada", WorkspaceAdmin: true})
	st.CreateProject(ctx, store.Command("test"), ws, "Backups")
	st.Close()

	var out bytes.Buffer
	if err := adminCommand(ctx, []string{"set-alerts", "-db", path, "on"}, &out, &out); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "Alerts are on") || !strings.Contains(out.String(), "alerts cannot reach them: Ada") {
		t.Errorf("output:\n%s", out.String())
	}
	st, _ = store.Open(ctx, path)
	if shadow, _ := st.ShadowMode(ctx); shadow {
		t.Error("still in shadow mode")
	}
	st.Close()

	out.Reset()
	if err := adminCommand(ctx, []string{"set-alerts", "-db", path, "shadow"}, &out, &out); err != nil {
		t.Fatal(err)
	}
	st, _ = store.Open(ctx, path)
	defer st.Close()
	if shadow, _ := st.ShadowMode(ctx); !shadow {
		t.Error("not back in shadow mode")
	}
	if err := adminCommand(ctx, []string{"set-alerts", "-db", path, "maybe"}, &out, &out); err == nil {
		t.Error("an unknown mode was taken")
	}
}
