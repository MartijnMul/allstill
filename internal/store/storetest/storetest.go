// SPDX-License-Identifier: AGPL-3.0-only

// Package storetest gives tests outside the store a database that is already
// migrated. Migrating is most of what a test costs under the race detector, so
// it is done once for a test binary and each test gets its own copy.
package storetest

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/MartijnMul/allstill/internal/store"
)

var migrated struct {
	once  sync.Once
	image []byte
	err   error
}

// Path returns the path of a database with every migration applied, in a
// directory of this test's own. It is as store.Open leaves a new path, made a
// moment earlier; a test of what happens at a path with nothing there, or of
// migrating, starts from its own empty path instead.
func Path(t testing.TB) string {
	t.Helper()
	migrated.once.Do(func() { migrated.image, migrated.err = image() })
	if migrated.err != nil {
		t.Fatalf("making the migrated database: %v", migrated.err)
	}
	path := filepath.Join(t.TempDir(), "allstill.db")
	if err := os.WriteFile(path, migrated.image, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func image() ([]byte, error) {
	dir, err := os.MkdirTemp("", "allstill-storetest")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	ctx := context.Background()
	s, err := store.Open(ctx, filepath.Join(dir, "allstill.db"))
	if err != nil {
		return nil, err
	}
	defer s.Close()
	to := filepath.Join(dir, "copy.db")
	if err := s.Backup(ctx, to); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(to)
	if err == nil && len(b) == 0 {
		// An empty file would be migrated again by every test, slowly and unseen.
		err = errors.New("the copy is empty")
	}
	return b, err
}
