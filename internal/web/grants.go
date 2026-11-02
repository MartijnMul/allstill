// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"crypto/rand"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/MartijnMul/allstill/internal/id"
)

// The sign-ins waiting for someone to approve them (RFC 8628). They live in memory,
// not in the database, as anyone on the internet can ask for one: asking costs no
// write, a restart just means asking again, and there is one server process by
// design. Limiting how many are asked for a minute bounds how many are held.

const (
	grantLifetime = 10 * time.Minute
	// grantKept is how long a finished grant can still say what became of it, to a
	// command line that polled just before.
	grantKept = time.Minute
	// fetchWithin is how long a command line has to fetch a token once it was approved:
	// it polls every few seconds, so someone who waits on purpose gets nothing.
	fetchWithin = time.Minute
	// pollInterval is how long a command line waits between polls; slow_down adds pollSlower.
	pollInterval = 5 * time.Second
	pollSlower   = 5 * time.Second
	// pollMargin keeps a poll that arrives a little early, after a wait of the right
	// length, from being told to slow down.
	pollMargin = time.Second
	// asksPerMinute is how many grants the whole installation hands out: the server
	// cannot tell who asks, behind a proxy (spec C3).
	asksPerMinute = 60
	// wrongCodesPerMinute is how many codes one person gets to enter (R3).
	wrongCodesPerMinute = 5
)

type grantState int

const (
	grantPending   grantState = iota
	grantApproving            // a passkey's answer holds, and its token is being made
	grantApproved
	grantDenied
	grantRedeemed
)

// grant is one sign-in being asked for.
type grant struct {
	id         string // what a passkey's answer is bound to; no secret
	deviceCode string
	userCode   string // without its dash
	name       string // what the command line says it is called
	scope      string
	asked      time.Time
	expires    time.Time
	lastPoll   time.Time
	interval   time.Duration
	state      grantState
	token      string // once approved, until it is fetched
}

type grants struct {
	mu     sync.Mutex
	byCode map[string]*grant // by device code
	byUser map[string]*grant // by user code
	asks   []time.Time
}

func newGrants() *grants {
	return &grants{byCode: map[string]*grant{}, byUser: map[string]*grant{}}
}

// A user code avoids vowels, so it spells nothing, and letters easily taken for
// digits. It has 20^8 values, and someone guessing has a few tries a minute.
const userCodeLetters = "BCDFGHJKLMNPQRSTVWXZ"

const userCodeLength = 8

func newUserCode() string {
	var b strings.Builder
	var buf [1]byte
	for b.Len() < userCodeLength {
		rand.Read(buf[:])
		// Below a multiple of the alphabet's size, so each letter is as likely.
		if int(buf[0]) < 256-256%len(userCodeLetters) {
			b.WriteByte(userCodeLetters[int(buf[0])%len(userCodeLetters)])
		}
	}
	return b.String()
}

// showUserCode writes a code as people read it: WDJB-MJHT.
func showUserCode(code string) string {
	return code[:userCodeLength/2] + "-" + code[userCodeLength/2:]
}

// normalUserCode ignores case, dashes and whatever else was typed around the letters.
func normalUserCode(typed string) string {
	var b strings.Builder
	for _, c := range strings.ToUpper(typed) {
		if 'A' <= c && c <= 'Z' {
			b.WriteRune(c)
		}
	}
	return b.String()
}

// sweep forgets what has finished. The caller holds the lock.
func (g *grants) sweep(at time.Time) {
	for code, gr := range g.byCode {
		if !at.Before(gr.expires.Add(grantKept)) {
			delete(g.byCode, code)
			delete(g.byUser, gr.userCode)
		}
	}
	for len(g.asks) > 0 && at.Sub(g.asks[0]) >= time.Minute {
		g.asks = g.asks[1:]
	}
}

// ask starts a grant. False means too many were asked for this minute.
func (g *grants) ask(name, scope string, at time.Time) (*grant, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.sweep(at)
	if len(g.asks) >= asksPerMinute {
		return nil, false
	}
	g.asks = append(g.asks, at)
	gr := &grant{
		id: id.Key(), deviceCode: id.Key(), userCode: newUserCode(), name: name, scope: scope,
		asked: at, expires: at.Add(grantLifetime), interval: pollInterval,
	}
	for g.byUser[gr.userCode] != nil {
		gr.userCode = newUserCode()
	}
	g.byCode[gr.deviceCode], g.byUser[gr.userCode] = gr, gr
	return gr, true
}

// waiting returns a copy of the grant a user code names, while it can be approved.
func (g *grants) waiting(userCode string, at time.Time) (grant, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	gr, ok := g.byUser[normalUserCode(userCode)]
	if !ok || gr.state != grantPending || !at.Before(gr.expires) {
		return grant{}, false
	}
	return *gr, true
}

// claim takes a grant someone has answered for, which must still be the one that was
// shown, while its token is made. Polling goes on saying it is pending, and it can no
// longer be refused.
func (g *grants) claim(grantID string, at time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, gr := range g.byUser {
		if gr.id == grantID && gr.state == grantPending && at.Before(gr.expires) {
			gr.state = grantApproving
			return true
		}
	}
	return false
}

// approved hands a claimed grant its token, to be fetched soon.
func (g *grants) approved(grantID, token string, at time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, gr := range g.byUser {
		if gr.id == grantID && gr.state == grantApproving {
			gr.state, gr.token = grantApproved, token
			if end := at.Add(fetchWithin); end.Before(gr.expires) {
				gr.expires = end
			}
			// Sweeping needs someone to ask or poll, and a token should not wait for that.
			time.AfterFunc(fetchWithin, func() {
				g.mu.Lock()
				defer g.mu.Unlock()
				gr.token = ""
			})
		}
	}
}

// unclaim lets a grant be answered again, as its token could not be made.
func (g *grants) unclaim(grantID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, gr := range g.byUser {
		if gr.id == grantID && gr.state == grantApproving {
			gr.state = grantPending
		}
	}
}

// deny ends a grant, whose command line is told at its next poll.
func (g *grants) deny(userCode string, at time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	gr, ok := g.byUser[normalUserCode(userCode)]
	if !ok || gr.state != grantPending || !at.Before(gr.expires) {
		return false
	}
	gr.state = grantDenied
	return true
}

// RFC 8628's answers to a poll, and RFC 6749's for a code that is not known or used.
var (
	errPending  = errors.New("authorization_pending")
	errSlowDown = errors.New("slow_down")
	errDenied   = errors.New("access_denied")
	errExpired  = errors.New("expired_token")
	errBadGrant = errors.New("invalid_grant")
)

// poll says what became of a grant, and hands over its token, once.
func (g *grants) poll(deviceCode string, at time.Time) (grant, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.sweep(at)
	gr, ok := g.byCode[deviceCode]
	if !ok {
		return grant{}, errBadGrant
	}
	early := !gr.lastPoll.IsZero() && at.Sub(gr.lastPoll) < gr.interval-pollMargin
	gr.lastPoll = at
	if early {
		gr.interval += pollSlower
		return grant{}, errSlowDown
	}
	switch gr.state {
	case grantDenied:
		return grant{}, errDenied
	case grantRedeemed:
		return grant{}, errBadGrant
	}
	if !at.Before(gr.expires) {
		gr.token = ""
		return grant{}, errExpired
	}
	if gr.state != grantApproved {
		return grant{}, errPending
	}
	if gr.token == "" {
		return grant{}, errExpired
	}
	handed := *gr
	gr.state, gr.token = grantRedeemed, ""
	return handed, nil
}
