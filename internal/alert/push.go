// SPDX-License-Identifier: AGPL-3.0-only

package alert

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
		return "Resolved: " + it.Title
	}
	return it.Title
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

// deliver sends the deliveries whose time has come: each is tried twice, a
// minute apart (R12). A subscription the push service no longer knows is
// removed; one made for another key is never pushed to (ADR 0003). Before
// every attempt it checks again what may go: in shadow mode only tests, to a
// device that is still the person's and confirmed, and of an alert only what
// is still true for them (R10, R13, R15).
func (s *Sender) deliver(ctx context.Context, shadow bool, now time.Time) error {
	if s.Push == nil {
		return nil
	}
	pending, err := s.Store.PendingDeliveries(ctx, now, batch)
	if err != nil {
		return err
	}
	key := s.Push.Key.PublicKey()
	for _, d := range pending {
		if d.Kind != "test" && shadow {
			if err := s.Store.Shadowed(ctx, d); err != nil {
				return err
			}
			continue
		}
		if d.ChannelPerson != d.PersonID || (d.Kind != "test" && !d.Confirmed) {
			if err := s.Store.Failed(ctx, d, "the device changed hands or must be confirmed again", false, false, now); err != nil {
				return err
			}
			continue
		}
		if d.Kind == "alert" {
			keep, dropped, err := s.stillTrue(ctx, d)
			if err != nil {
				return err
			}
			if err := s.Store.Unheard(ctx, d.PersonID, dropped); err != nil {
				return err
			}
			if d.Items = keep; len(d.Items) == 0 {
				if err := s.Store.Failed(ctx, d, "nothing in it was still true when it could go", false, false, now); err != nil {
					return err
				}
				continue
			}
		}
		if d.Key != key {
			if err := s.Store.Failed(ctx, d, "the device subscribed for another key and must subscribe again", false, true, now); err != nil {
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
		outcome, reason := s.Push.send(ctx, d, token, now)
		switch {
		case outcome == webpush.Sent:
			err = s.Store.Sent(ctx, d.ID, now)
		case outcome == webpush.Gone:
			err = s.Store.Failed(ctx, d, "the device no longer subscribes; it was removed", true, false, now)
		case outcome == webpush.Retry && d.Attempts == 0:
			err = s.Store.Retry(ctx, d.ID, reason, now.Add(RetryAfter))
		default:
			err = s.Store.Failed(ctx, d, reason, false, true, now)
		}
		if err != nil {
			return err
		}
		if outcome != webpush.Sent {
			s.Log.Warn("pushing a message", "delivery", d.ID, "channel", d.ChannelID, "error", reason)
		}
	}
	return nil
}
