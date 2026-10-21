// SPDX-License-Identifier: AGPL-3.0-only

package ingest

import (
	"sync"
	"time"
)

// A check may be pinged in bursts of Burst, and then PerSecond times a second.
// That is far more than any job needs; the limit is there for a script stuck
// in a loop, which would otherwise fill the database and hold up other pings.
const (
	Burst     = 60
	PerSecond = 1.0
)

// maxBuckets bounds the memory of the limiter. Past it, new keys are not
// limited: they are mostly unknown checks, which cost a read and no write.
const maxBuckets = 100_000

// limiter keeps a token bucket per ping URL.
type limiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	swept   time.Time
}

type bucket struct {
	tokens float64
	at     time.Time // when tokens was last brought up to date
	warned time.Time // when dropping this key's pings was last logged
}

// allow takes a token for key, and says whether there was one. warn says
// whether to log that pings are dropped: at most once an hour per key.
func (l *limiter) allow(key string, now time.Time) (ok, warn bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.buckets == nil {
		l.buckets = map[string]*bucket{}
	}
	if now.Sub(l.swept) >= time.Minute {
		l.sweep(now)
	}
	b := l.buckets[key]
	if b == nil {
		if len(l.buckets) >= maxBuckets {
			return true, false
		}
		b = &bucket{tokens: Burst, at: now}
		l.buckets[key] = b
	}
	b.refill(now)
	if b.tokens >= 1 {
		b.tokens--
		return true, false
	}
	if now.Sub(b.warned) >= time.Hour {
		b.warned = now
		return false, true
	}
	return false, false
}

func (b *bucket) refill(now time.Time) {
	if d := now.Sub(b.at); d > 0 {
		b.tokens = min(Burst, b.tokens+d.Seconds()*PerSecond)
		b.at = now
	}
}

// sweep forgets buckets that are full again: they hold nothing a new bucket
// would not, except when their key was last warned about.
func (l *limiter) sweep(now time.Time) {
	l.swept = now
	for k, b := range l.buckets {
		b.refill(now)
		if b.tokens >= Burst && now.Sub(b.warned) >= time.Hour {
			delete(l.buckets, k)
		}
	}
}
