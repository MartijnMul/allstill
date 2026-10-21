// SPDX-License-Identifier: AGPL-3.0-only

package id

import (
	"strings"
	"testing"
	"time"
)

func TestNewIncreases(t *testing.T) {
	prev := New(Check)
	for i := range 1000 {
		next := New(Check)
		if next <= prev {
			t.Fatalf("id %d: %s is not after %s", i, next, prev)
		}
		prev = next
	}
	if !strings.HasPrefix(prev, "chk_") || len(prev) != len("chk_")+26 {
		t.Fatalf("unexpected form %q", prev)
	}
	if strings.ContainsAny(prev[4:], "ilou") {
		t.Fatalf("%q uses letters outside the alphabet", prev)
	}
}

func TestNewKeepsTheTime(t *testing.T) {
	a := NewAt(Run, time.UnixMilli(1_000_000_000_000))
	b := NewAt(Run, time.UnixMilli(2_000_000_000_000))
	if a[:14] >= b[:14] {
		t.Fatalf("the time part does not order: %s, %s", a, b)
	}
	New(Run)
	if old := NewAt(Run, time.UnixMilli(1_000_000_000_000)); old[:14] != a[:14] {
		t.Fatalf("NewAt after New lost its time: %s, %s", old, a)
	}
}

func TestUUID(t *testing.T) {
	for range 100 {
		u := UUID()
		got, ok := ParseUUID(u)
		if !ok || got != u {
			t.Fatalf("ParseUUID(%q) = %q, %v", u, got, ok)
		}
		if u[14] != '4' || !strings.ContainsRune("89ab", rune(u[19])) {
			t.Fatalf("%s is not version 4", u)
		}
	}
}

func TestParseUUID(t *testing.T) {
	cases := map[string]string{
		"5C2B1D2E-8F3A-4B7C-9D1E-2F3A4B5C6D7E": "5c2b1d2e-8f3a-4b7c-9d1e-2f3a4b5c6d7e",
		"5c2b1d2e8f3a4b7c9d1e2f3a4b5c6d7e":     "",
		"5c2b1d2e-8f3a-4b7c-9d1e-2f3a4b5c6d7":  "",
		"5c2b1d2e-8f3a-4b7c-9d1e-2f3a4b5c6d7g": "",
		"5c2b1d2e+8f3a-4b7c-9d1e-2f3a4b5c6d7e": "",
		"AbCdEfGhIjKlMnOpQrStUv":               "",
	}
	for in, want := range cases {
		got, ok := ParseUUID(in)
		if got != want || ok != (want != "") {
			t.Errorf("ParseUUID(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
}

func TestKey(t *testing.T) {
	k := Key()
	if len(k) != 22 || strings.Trim(k, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_") != "" {
		t.Fatalf("unexpected key %q", k)
	}
	if _, ok := ParseUUID(k); ok {
		t.Fatalf("a key parses as a UUID: %q", k)
	}
}
