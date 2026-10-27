// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"path/filepath"
	"testing"
)

// The key browsers subscribe for survives a restart, and a backup (ADR 0003).
func TestTheVAPIDKeyIsKept(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	s, err := Open(ctx, filepath.Join(dir, "allstill.db"))
	if err != nil {
		t.Fatal(err)
	}
	ws, _ := s.Workspace(ctx)
	first, err := s.VAPIDKey(ctx, ws)
	if err != nil {
		t.Fatal(err)
	}
	if again, _ := s.VAPIDKey(ctx, ws); again.PublicKey() != first.PublicKey() {
		t.Fatal("a second key was made")
	}
	if err := s.Backup(ctx, filepath.Join(dir, "copy.db")); err != nil {
		t.Fatal(err)
	}
	s.Close()
	for _, name := range []string{"allstill.db", "copy.db"} {
		s, err := Open(ctx, filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		k, err := s.VAPIDKey(ctx, ws)
		s.Close()
		if err != nil || k.PublicKey() != first.PublicKey() {
			t.Fatalf("%s: %v, a different key", name, err)
		}
	}
}
