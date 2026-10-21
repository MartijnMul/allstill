// SPDX-License-Identifier: AGPL-3.0-only

package alert

import (
	"context"
	"log/slog"
	"time"

	"github.com/MartijnMul/allstill/internal/store"
)

const (
	// Settle is how long an incident's checks must stay up before it is
	// resolved and a recovery message goes out.
	Settle = 10 * time.Minute
	// Gather is the least time between two alerts to one person; a second
	// one waits and carries whatever came due meanwhile.
	Gather = 3 * time.Minute
	// Every is how often the sender looks for work.
	Every = 10 * time.Second
	// retryNoChannel is when a message that found no channel is tried again.
	retryNoChannel = 10 * time.Minute
	batch          = 500
)

// Store is what the sender needs of the store.
type Store interface {
	SettleIncidents(ctx context.Context, now time.Time, settle time.Duration) (int, error)
	DueRecipients(ctx context.Context, now time.Time, limit int) ([]store.Due, error)
	Record(ctx context.Context, d *store.Delivery, told []store.Told) error
	LastMessageAt(ctx context.Context, personID string) (time.Time, error)
	ShadowMode(ctx context.Context) (bool, error)
	DigestPeople(ctx context.Context) ([]store.DigestPerson, error)
	DigestOf(ctx context.Context, personID string, since time.Time) ([]store.DigestEntry, []store.SilentCheck, error)
	RecordDigest(ctx context.Context, personID string, at time.Time, d *store.Delivery, told []store.Told) error
}

// Sender settles incidents, tells people what they must hear, and sends the
// digests. There are no channels yet: in shadow mode, the default, it records
// what it would have sent; otherwise a message fails for want of a channel.
type Sender struct {
	Store Store
	Log   *slog.Logger
	Now   func() time.Time
}

// Run works every Every until ctx ends.
func (s *Sender) Run(ctx context.Context) error {
	ticker := time.NewTicker(Every)
	defer ticker.Stop()
	for {
		if err := s.Pass(ctx); err != nil && ctx.Err() == nil {
			s.Log.Error("sending alerts", "error", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// Pass does what is due now: settle, alert, and digest.
func (s *Sender) Pass(ctx context.Context) error {
	now := s.Now()
	for {
		n, err := s.Store.SettleIncidents(ctx, now, Settle)
		if err != nil {
			return err
		}
		if n == 0 {
			break
		}
	}
	shadow, err := s.Store.ShadowMode(ctx)
	if err != nil {
		return err
	}
	due, err := s.Store.DueRecipients(ctx, now, batch)
	if err != nil {
		return err
	}
	for len(due) > 0 {
		n := 1
		for n < len(due) && due[n].PersonID == due[0].PersonID {
			n++
		}
		if err := s.alert(ctx, due[:n], shadow, now); err != nil {
			s.Log.Error("alerting a person", "person", due[0].PersonID, "error", err)
		}
		due = due[n:]
	}
	return s.digests(ctx, shadow, now)
}

// alert decides for one person's due recipients, and sends them one message.
func (s *Sender) alert(ctx context.Context, due []store.Due, shadow bool, now time.Time) error {
	var items []store.Item
	var told, later []store.Told
	for _, d := range due {
		quiet, err := ParseQuietHours(d.QuietHours, d.TimeZone)
		if err != nil {
			// Unreadable quiet hours must not keep an alert from going out.
			s.Log.Warn("reading quiet hours", "person", d.PersonID, "error", err)
		}
		dec := Decide(Situation{
			Open: d.Open, Reason: d.Reason, Urgency: d.Urgency, Critical: d.Critical,
			Follows: d.Follows, RecoveryMessages: d.RecoveryMessages,
			Told: Told(d.Told), ToldHow: d.ToldHow, ToldInShadow: d.ToldInShadow, Shadow: shadow, Quiet: quiet,
		}, now)
		if dec.Tell == "" {
			later = append(later, store.Told{IncidentID: d.IncidentID, PersonID: d.PersonID, Again: dec.Again})
			continue
		}
		items = append(items, store.Item{IncidentID: d.IncidentID, What: string(dec.Tell), Title: d.Title})
		told = append(told, store.Told{IncidentID: d.IncidentID, PersonID: d.PersonID, Told: string(dec.Tell), How: Now, Shadow: shadow, At: now})
	}
	if len(items) == 0 {
		return s.Store.Record(ctx, nil, later)
	}
	last, err := s.Store.LastMessageAt(ctx, due[0].PersonID)
	if err != nil {
		return err
	}
	if !last.IsZero() && now.Sub(last) < Gather {
		for _, t := range told {
			later = append(later, store.Told{IncidentID: t.IncidentID, PersonID: t.PersonID, Again: last.Add(Gather)})
		}
		return s.Store.Record(ctx, nil, later)
	}
	d := &store.Delivery{WorkspaceID: due[0].WorkspaceID, PersonID: due[0].PersonID, Kind: "alert", Items: items, State: "shadow", At: now}
	if !shadow {
		// No channel exists yet (Web Push and email follow): the message
		// fails, and is tried again later.
		d.State, d.Error = "failed", "no channel to reach this person yet"
		for _, t := range told {
			later = append(later, store.Told{IncidentID: t.IncidentID, PersonID: t.PersonID, Again: now.Add(retryNoChannel)})
		}
		return s.Store.Record(ctx, d, later)
	}
	return s.Store.Record(ctx, d, append(told, later...))
}

// digests sends each person whose digest time has passed since the sender
// last looked their digest, when there is something to say. A digest missed
// while Allstill was down goes out at the first look.
func (s *Sender) digests(ctx context.Context, shadow bool, now time.Time) error {
	people, err := s.Store.DigestPeople(ctx)
	if err != nil {
		return err
	}
	for _, p := range people {
		next, err := NextDigest(p.LookedAt, p.DigestTime, p.TimeZone)
		if err != nil {
			s.Log.Warn("working out a digest time", "person", p.ID, "error", err)
			continue
		}
		if now.Before(next) {
			continue
		}
		// Not within the gather period of the last message: the digest waits,
		// and the next pass looks again.
		if last, err := s.Store.LastMessageAt(ctx, p.ID); err != nil {
			return err
		} else if !last.IsZero() && now.Sub(last) < Gather {
			continue
		}
		if err := s.digest(ctx, p, shadow, now); err != nil {
			s.Log.Error("sending a digest", "person", p.ID, "error", err)
		}
	}
	return nil
}

func (s *Sender) digest(ctx context.Context, p store.DigestPerson, shadow bool, now time.Time) error {
	entries, silent, err := s.Store.DigestOf(ctx, p.ID, p.LookedAt)
	if err != nil {
		return err
	}
	items, told := DigestItems(entries, silent, shadow)
	for i := range told {
		told[i].PersonID, told[i].At = p.ID, now
	}
	if len(items) == 0 {
		return s.Store.RecordDigest(ctx, p.ID, now, nil, nil)
	}
	d := &store.Delivery{WorkspaceID: p.WorkspaceID, PersonID: p.ID, Kind: "digest", Items: items, State: "shadow", At: now}
	if !shadow {
		d.State, d.Error, told = "failed", "no channel to reach this person yet", nil
	}
	return s.Store.RecordDigest(ctx, p.ID, now, d, told)
}

// DigestItems is what a digest says, and what it tells:
//   - down, for an open incident that waits for the digest;
//   - recovered, for one heard of in a digest that recovered;
//   - still_open, for one open the person knows of or will hear of at once;
//   - resolved_unseen, for one resolved before they heard of it;
//   - silent_check, for a check that can never alert.
func DigestItems(entries []store.DigestEntry, silent []store.SilentCheck, shadow bool) ([]store.Item, []store.Told) {
	var items []store.Item
	var told []store.Told
	for _, e := range entries {
		if !e.Follows || (e.ToldInShadow && !shadow) {
			continue
		}
		item := store.Item{IncidentID: e.IncidentID, Title: e.Title}
		switch {
		case e.Open && e.Told == string(Nothing) && e.Urgency == Digest:
			item.What = string(Down)
			told = append(told, store.Told{IncidentID: e.IncidentID, Told: string(Down), How: Digest, Shadow: shadow})
		case e.Open:
			item.What = "still_open"
		case e.Told == string(Down) && e.ToldHow == Digest && e.Reason == "recovered" && e.RecoveryMessages:
			item.What = string(Recovered)
			told = append(told, store.Told{IncidentID: e.IncidentID, Told: string(Recovered), How: Digest, Shadow: shadow})
		case e.Told == string(Nothing):
			item.What = "resolved_unseen"
		default:
			continue
		}
		items = append(items, item)
	}
	for _, c := range silent {
		items = append(items, store.Item{What: "silent_check", Title: c.Project + ": " + c.Name})
	}
	return items, told
}
