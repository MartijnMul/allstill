// SPDX-License-Identifier: AGPL-3.0-only

package alert

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/MartijnMul/allstill/internal/outbound"
	"github.com/MartijnMul/allstill/internal/store"
	"github.com/MartijnMul/allstill/internal/webpush"
)

// Push sends messages to browsers with Web Push (ADR 0003).
type Push struct {
	Key *webpush.Key
	// Base is the dashboard's address: the subject of every push, and where
	// the links in messages lead.
	Base   string
	Client *http.Client
}

// RetryAfter is when a push that failed for now is tried once more (R12).
// It must stay shorter than Gather (see Pass).
const RetryAfter = time.Minute

// payload is what the service worker shows.
type payload struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	URL   string `json:"url"`
	Tag   string `json:"tag"`
}

// render makes the notification for a delivery. A test carries the link
// that confirms its channel.
func (p *Push) render(d store.Pending, token string) payload {
	switch d.Kind {
	case "test":
		return payload{
			Title: "Allstill test message",
			Body:  "Open this to confirm alerts reach this device.",
			URL:   p.Base + "/channels/" + d.ChannelID + "/confirm?token=" + token,
			Tag:   "test",
		}
	case "digest":
		return payload{Title: "Allstill digest", Body: digestLine(d.Items), URL: p.Base + "/messages", Tag: "digest"}
	}
	lines := make([]string, len(d.Items))
	for i, it := range d.Items {
		lines[i] = itemLine(it)
	}
	m := payload{URL: p.Base + "/messages", Tag: "alert"}
	if len(lines) == 1 {
		m.Title = lines[0]
	} else {
		m.Title = fmt.Sprintf("%d changes", len(lines))
		m.Body = strings.Join(lines, "\n")
	}
	return m
}

// itemLine is an incident's title, which says what went wrong, and that it
// was resolved when it was.
func itemLine(it store.Item) string {
	if it.What == string(Recovered) {
		return "Resolved: " + oneLine(it.Title)
	}
	return oneLine(it.Title)
}

// oneLine keeps a title to one line, so it cannot make lines or links of
// its own in a message to someone else.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// digestLine sums a digest up in one line; the digest itself is on Messages.
func digestLine(items []store.Item) string {
	count := map[string]int{}
	for _, it := range items {
		count[it.What]++
	}
	var parts []string
	for _, c := range []struct{ what, one, many string }{
		{"down", "%d down", "%d down"},
		{"still_open", "%d still open", "%d still open"},
		{"recovered", "%d back up", "%d back up"},
		{"resolved_unseen", "%d resolved before you heard", "%d resolved before you heard"},
		{"silent_check", "%d check that cannot alert", "%d checks that cannot alert"},
		{"warning", "%d warning", "%d warnings"},
	} {
		switch n := count[c.what]; {
		case n == 1:
			parts = append(parts, fmt.Sprintf(c.one, n))
		case n > 1:
			parts = append(parts, fmt.Sprintf(c.many, n))
		}
	}
	return strings.Join(parts, ", ")
}

// encode makes the payload JSON, shortening the body until it fits in a push.
func encode(m payload) []byte {
	for {
		b, _ := json.Marshal(m)
		if len(b) <= webpush.MaxPayload || m.Body == "" {
			return b
		}
		r := []rune(m.Body)
		m.Body = string(r[:len(r)*3/4]) + "…"
	}
}

// send pushes one delivery and reports how it went. The error never holds the
// endpoint (ADR 0003).
func (p *Push) send(ctx context.Context, d store.Pending, token string, now time.Time) (webpush.Outcome, string) {
	m := webpush.Message{Payload: encode(p.render(d, token)), TTL: 12 * time.Hour, Urgency: webpush.High}
	switch d.Kind {
	case "digest":
		m.TTL, m.Urgency = 24*time.Hour, webpush.Normal
	case "test":
		m.TTL = 10 * time.Minute
	}
	req, err := p.Key.Request(d.Sub, m, p.Base, now)
	if err != nil {
		return webpush.Refused, err.Error()
	}
	resp, err := outbound.Do(ctx, p.Client, req)
	if err != nil {
		return webpush.Retry, err.Error()
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	o := webpush.Classify(resp.StatusCode)
	if o == webpush.Sent {
		return o, ""
	}
	return o, fmt.Sprintf("the push service answered %s", resp.Status)
}

// stillTrue keeps of an alert's items what may still be said: a down only
// while its incident is open and the person follows it. It also returns the
// incidents of the downs it dropped.
func (s *Sender) stillTrue(ctx context.Context, d store.Pending) (keep []store.Item, dropped []string, err error) {
	var ids []string
	for _, it := range d.Items {
		if it.What == string(Down) {
			ids = append(ids, it.IncidentID)
		}
	}
	if len(ids) == 0 {
		return d.Items, nil, nil
	}
	open, err := s.Store.StillFollowed(ctx, d.PersonID, ids)
	if err != nil {
		return nil, nil, err
	}
	for _, it := range d.Items {
		if it.What != string(Down) || open[it.IncidentID] {
			keep = append(keep, it)
		} else {
			dropped = append(dropped, it.IncidentID)
		}
	}
	return keep, dropped, nil
}

// deliver sends the deliveries whose time has come: pushes first, so a slow
// mail server does not hold them up. A push is tried twice, a minute apart
// (R12); an email up to four times. A subscription the push service no
// longer knows is removed; one made for another key is never pushed to
// (ADR 0003). A delivery that fails for good goes the other way when it can
// (store.Failed).
//
// mailDown is shared by a pass's rounds: once the mail server could not be
// reached, the pass's other emails wait.
func (s *Sender) deliver(ctx context.Context, shadow bool, now time.Time, mailDown *bool) error {
	pending, err := s.Store.PendingDeliveries(ctx, now, batch)
	if err != nil {
		return err
	}
	slices.SortStableFunc(pending, func(a, b store.Pending) int {
		return cmp.Compare(boolInt(a.ChannelKind == "email"), boolInt(b.ChannelKind == "email"))
	})
	for _, d := range pending {
		ok, err := s.checkAgain(ctx, &d, shadow, now)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		if d.ChannelKind == "email" && *mailDown {
			// The mail server could not be reached this pass: the rest
			// wait, without using a try.
			if err := s.Store.Postpone(ctx, d.ID, now.Add(RetryAfter)); err != nil {
				return err
			}
			continue
		}
		var token string
		if d.Kind == "test" {
			token = rand.Text()
			if err := s.Store.SetConfirm(ctx, d, token); err != nil {
				return err
			}
		}
		switch d.ChannelKind {
		case "webpush":
			err = s.pushOne(ctx, d, token, now)
		case "email":
			var down bool
			down, err = s.mailOne(ctx, d, token, now)
			*mailDown = *mailDown || down
		default:
			err = s.Store.Failed(ctx, d, "Allstill cannot send to a channel of kind "+d.ChannelKind, false, false, now, s.ways())
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ways are the ways the sender can send now, for a delivery that falls back.
func (s *Sender) ways() store.Ways {
	return store.Ways{Key: s.key(), Push: s.Push != nil, Mail: s.Mail != nil}
}

// key is the public VAPID key devices must have subscribed for.
func (s *Sender) key() string {
	if s.Push == nil {
		return ""
	}
	return s.Push.Key.PublicKey()
}

// checkAgain checks, before every attempt, what may still go (R10, R13,
// R15): in shadow mode only tests; only to a channel that is still the
// person's and confirmed; of an alert only what is still true for them; to
// a device only when it subscribed for the current key. It records what it
// stops, and says whether d may go.
func (s *Sender) checkAgain(ctx context.Context, d *store.Pending, shadow bool, now time.Time) (bool, error) {
	if d.Kind != "test" && shadow {
		return false, s.Store.Shadowed(ctx, *d)
	}
	if d.ChannelID == "" {
		return false, s.Store.Failed(ctx, *d, "the channel was removed", false, false, now, s.ways())
	}
	if d.ChannelPerson != d.PersonID || (d.Kind != "test" && !d.Confirmed) {
		return false, s.Store.Failed(ctx, *d, "the channel changed hands or must be confirmed again", false, false, now, s.ways())
	}
	if d.Kind == "alert" {
		keep, dropped, err := s.stillTrue(ctx, *d)
		if err != nil {
			return false, err
		}
		if err := s.Store.Unheard(ctx, d.PersonID, dropped); err != nil {
			return false, err
		}
		if d.Items = keep; len(d.Items) == 0 {
			return false, s.Store.Failed(ctx, *d, store.NoLongerTrue, false, false, now, s.ways())
		}
	}
	if d.ChannelKind == "email" && d.Kind != "test" && d.ChannelFailed {
		// The server refused the address since this was queued.
		return false, s.Store.Failed(ctx, *d, "the address was refused; send it a test once it works again", false, false, now, s.ways())
	}
	if d.ChannelKind == "webpush" && d.Key != s.key() {
		return false, s.Store.Failed(ctx, *d, "the device subscribed for another key and must subscribe again", false, true, now, s.ways())
	}
	return true, nil
}

// pushOne pushes one delivery and records how it went.
func (s *Sender) pushOne(ctx context.Context, d store.Pending, token string, now time.Time) error {
	if s.Push == nil {
		return s.Store.Failed(ctx, d, "push is not set up on this server", false, false, now, s.ways())
	}
	outcome, reason := s.Push.send(ctx, d, token, now)
	var err error
	switch {
	case outcome == webpush.Sent:
		err = s.Store.Sent(ctx, d.ID, now)
	case outcome == webpush.Gone:
		err = s.Store.Failed(ctx, d, "the device no longer subscribes; it was removed", true, false, now, s.ways())
	case outcome == webpush.Retry && d.Attempts == 0:
		// Once more in a minute; a recovery of its down waits for it.
		err = s.Store.Retry(ctx, d.ID, reason, now.Add(RetryAfter))
	default:
		err = s.Store.Failed(ctx, d, reason, false, true, now, s.ways())
	}
	if outcome != webpush.Sent {
		s.Log.Warn("pushing a message", "delivery", d.ID, "channel", d.ChannelID, "error", reason)
	}
	return err
}
