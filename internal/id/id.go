// SPDX-License-Identifier: AGPL-3.0-only

// Package id makes the identifiers Allstill hands out: opaque ids for rows,
// check UUIDs for ping URLs, and random keys.
package id

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Prefixes say what an id names, so a pasted id is recognisable.
const (
	Workspace = "ws"
	Person    = "per"
	Project   = "prj"
	Check     = "chk"
	Run       = "run"
	Passkey   = "pky"
	Session   = "ses"
	Incident  = "inc"
	Channel   = "chn"
	Delivery  = "dlv"
)

// Crockford's base32, lower case: no i, l, o or u, so ids read aloud well.
const alphabet = "0123456789abcdefghjkmnpqrstvwxyz"

var (
	mu       sync.Mutex
	lastMs   uint64
	lastRand [10]byte
)

// New returns prefix_ followed by 26 characters: 48 bits of milliseconds and
// 80 random bits, as in a ULID. Ids made by New increase, even within one
// millisecond, so sorting by id sorts by creation.
func New(prefix string) string {
	ms := uint64(time.Now().UnixMilli())
	mu.Lock()
	var r [10]byte
	if ms <= lastMs {
		ms = lastMs
		r = lastRand
		for i := len(r) - 1; i >= 0; i-- {
			r[i]++
			if r[i] != 0 {
				break
			}
		}
	} else {
		rand.Read(r[:])
	}
	lastMs, lastRand = ms, r
	mu.Unlock()
	return format(prefix, ms, r)
}

// NewAt returns an id for a given time, such as an imported run's, so it sorts
// among the others by that time. Within one millisecond its order is random.
func NewAt(prefix string, t time.Time) string {
	var r [10]byte
	rand.Read(r[:])
	return format(prefix, uint64(t.UnixMilli()), r)
}

func format(prefix string, ms uint64, r [10]byte) string {
	var b [16]byte
	binary.BigEndian.PutUint64(b[:8], ms<<16)
	copy(b[6:], r[:])
	return prefix + "_" + encode(b)
}

func encode(b [16]byte) string {
	hi, lo := binary.BigEndian.Uint64(b[:8]), binary.BigEndian.Uint64(b[8:])
	var out [26]byte
	for i := 25; i >= 0; i-- {
		out[i] = alphabet[lo&31]
		lo = lo>>5 | hi<<59
		hi >>= 5
	}
	return string(out[:])
}

// UUID returns a random version 4 UUID in its canonical lower-case form.
func UUID() string {
	var b [16]byte
	rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// ParseUUID returns s in canonical lower-case form if it is a UUID with
// hyphens, in any letter case.
func ParseUUID(s string) (string, bool) {
	if len(s) != 36 {
		return "", false
	}
	s = strings.ToLower(s)
	for i, c := range s {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return "", false
			}
		} else if !('0' <= c && c <= '9' || 'a' <= c && c <= 'f') {
			return "", false
		}
	}
	return s, true
}

// Key returns a random key of 22 URL-safe characters (132 bits), such as a
// project's ping key.
func Key() string {
	var b [17]byte
	rand.Read(b[:])
	return base64.RawURLEncoding.EncodeToString(b[:])[:22]
}
