// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/MartijnMul/allstill/internal/id"
	"github.com/MartijnMul/allstill/internal/store/internal/db"
	"github.com/MartijnMul/allstill/internal/webpush"
)

// VAPIDKey returns the workspace's key for signing pushes, making it the
// first time (ADR 0003). It is never replaced: browsers subscribe for it.
func (s *Store) VAPIDKey(ctx context.Context, workspaceID string) (*webpush.Key, error) {
	q := db.New(s.w)
	b, err := q.VAPIDKey(ctx, workspaceID)
	if errors.Is(err, sql.ErrNoRows) {
		k, kerr := webpush.NewKey()
		if kerr != nil {
			return nil, kerr
		}
		// Two first calls race harmlessly: the second insert is ignored, and
		// both read back the one that was kept.
		if err := q.CreateVAPIDKey(ctx, db.CreateVAPIDKeyParams{
			WorkspaceID: workspaceID, PrivateKey: k.Bytes(), PublicKey: k.PublicKey(), CreatedAt: now(),
		}); err != nil {
			return nil, err
		}
		b, err = q.VAPIDKey(ctx, workspaceID)
	}
	if err != nil {
		return nil, err
	}
	return webpush.ParseKey(b)
}

// channelConfig is a channel's configuration: for webpush the subscription
// and the public key it was made for, for email the address; and the hash
// of the token in its last test message, which confirms the channel when
// the link in it is opened.
type channelConfig struct {
	Endpoint string `json:"endpoint,omitempty"`
	P256dh   string `json:"p256dh,omitempty"`
	Auth     string `json:"auth,omitempty"`
	Key      string `json:"key,omitempty"`
	Address  string `json:"address,omitempty"`
	Confirm  []byte `json:"confirm,omitempty"`
}

var b64 = base64.RawURLEncoding

func (c channelConfig) subscription() webpush.Subscription {
	p256dh, _ := b64.DecodeString(c.P256dh)
	auth, _ := b64.DecodeString(c.Auth)
	return webpush.Subscription{Endpoint: c.Endpoint, P256dh: p256dh, Auth: auth}
}

// Channel is one of a person's channels, as they see it. It never holds the
// endpoint or the keys of a subscription (ADR 0003).
type Channel struct {
	ID         string
	Kind       string // webpush or email
	Name       string
	Key        string // for webpush: the public key it was made for
	Address    string // for email
	VerifiedAt time.Time
	FailedAt   time.Time
	CreatedAt  time.Time
}

// Usable says whether alerts may go to the channel: a test message reached
// it, and, for webpush, it was made for the current key; for email, it has
// not failed since.
func (c Channel) Usable(key string) bool {
	if c.VerifiedAt.IsZero() {
		return false
	}
	if c.Kind == "email" {
		return c.FailedAt.IsZero()
	}
	return c.Key == key
}

func channelOf(r db.Channel) Channel {
	var cfg channelConfig
	json.Unmarshal([]byte(r.Config), &cfg)
	return Channel{
		ID: r.ID, Kind: r.Kind, Name: r.Name, Key: cfg.Key, Address: cfg.Address,
		VerifiedAt: msTime(r.VerifiedAt), FailedAt: msTime(r.FailedAt), CreatedAt: time.UnixMilli(r.CreatedAt).UTC(),
	}
}

// SavePush keeps a browser's subscription as a webpush channel of the person,
// made for key. The same subscription sent again keeps its channel and its
// confirmation; other keys for the same endpoint, or another person, replace
// it, and it must be confirmed again.
func (s *Store) SavePush(ctx context.Context, workspaceID, personID, name string, sub webpush.Subscription, key string) (Channel, error) {
	cfg := channelConfig{Endpoint: sub.Endpoint, P256dh: b64.EncodeToString(sub.P256dh), Auth: b64.EncodeToString(sub.Auth), Key: key}
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return Channel{}, err
	}
	defer tx.Rollback()
	q := db.New(tx)
	old, err := q.ChannelOfEndpoint(ctx, db.ChannelOfEndpointParams{WorkspaceID: workspaceID, Endpoint: sub.Endpoint})
	switch {
	case err == nil:
		var was channelConfig
		json.Unmarshal([]byte(old.Config), &was)
		if old.PersonID.String == personID && was.P256dh == cfg.P256dh && was.Auth == cfg.Auth && was.Key == key {
			return channelOf(old), nil
		}
		b, _ := json.Marshal(cfg)
		if err := q.ReplaceChannel(ctx, db.ReplaceChannelParams{PersonID: nullString(personID), Name: name, Config: string(b), ID: old.ID}); err != nil {
			return Channel{}, err
		}
		if old.PersonID.String != personID {
			// The one who lost it should be able to read that too.
			if err := logChannel(ctx, q, workspaceID, personID, old.ID, old.Kind, "channel.handed_on", true, time.Now()); err != nil {
				return Channel{}, err
			}
			if old.PersonID.Valid {
				if err := logChannel(ctx, q, workspaceID, old.PersonID.String, old.ID, old.Kind, "channel.handed_away", false, time.Now()); err != nil {
					return Channel{}, err
				}
			}
		}
		old.PersonID, old.Name, old.Config, old.VerifiedAt, old.FailedAt = nullString(personID), name, string(b), sql.NullInt64{}, sql.NullInt64{}
		return channelOf(old), tx.Commit()
	case errors.Is(err, sql.ErrNoRows):
		b, _ := json.Marshal(cfg)
		r := db.Channel{ID: id.New(id.Channel), WorkspaceID: workspaceID, PersonID: nullString(personID), Kind: "webpush", Name: name, Config: string(b), CreatedAt: now()}
		if err := q.CreateChannel(ctx, db.CreateChannelParams{
			ID: r.ID, WorkspaceID: r.WorkspaceID, PersonID: r.PersonID, Kind: r.Kind, Name: r.Name, Config: r.Config, CreatedAt: r.CreatedAt,
		}); err != nil {
			return Channel{}, err
		}
		return channelOf(r), tx.Commit()
	default:
		return Channel{}, err
	}
}

// MaxEmails is how many email addresses one person may add.
const MaxEmails = 3

// LimitError is a request refused for now, with what the person can do.
type LimitError struct{ Reason string }

func (e LimitError) Error() string { return e.Reason }

// SaveEmail adds an email address as a channel of the person, to be
// confirmed by the link in a test message. The same address again keeps
// its channel.
func (s *Store) SaveEmail(ctx context.Context, workspaceID, personID, address string) (Channel, error) {
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return Channel{}, err
	}
	defer tx.Rollback()
	q := db.New(tx)
	existing, err := q.EmailChannelsOfPerson(ctx, nullString(personID))
	if err != nil {
		return Channel{}, err
	}
	for _, r := range existing {
		if c := channelOf(r); strings.EqualFold(c.Address, address) {
			return c, nil
		}
	}
	if len(existing) >= MaxEmails {
		return Channel{}, LimitError{fmt.Sprintf("You have %d email addresses already; remove one first.", MaxEmails)}
	}
	b, _ := json.Marshal(channelConfig{Address: address})
	// The address is in the configuration; it may be longer than a name.
	r := db.Channel{ID: id.New(id.Channel), WorkspaceID: workspaceID, PersonID: nullString(personID), Kind: "email", Name: "Email", Config: string(b), CreatedAt: now()}
	if err := q.CreateChannel(ctx, db.CreateChannelParams{
		ID: r.ID, WorkspaceID: r.WorkspaceID, PersonID: r.PersonID, Kind: r.Kind, Name: r.Name, Config: r.Config, CreatedAt: r.CreatedAt,
	}); err != nil {
		return Channel{}, err
	}
	return channelOf(r), tx.Commit()
}

// Channels returns a person's channels.
func (s *Store) Channels(ctx context.Context, personID string) ([]Channel, error) {
	rows, err := db.New(s.r).ChannelsOfPerson(ctx, nullString(personID))
	if err != nil {
		return nil, err
	}
	cs := make([]Channel, len(rows))
	for i, r := range rows {
		cs[i] = channelOf(r)
	}
	return cs, nil
}

// DeleteChannel removes one of the person's channels.
func (s *Store) DeleteChannel(ctx context.Context, personID, channelID string) error {
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	q := db.New(tx)
	c, err := q.ChannelOfPerson(ctx, db.ChannelOfPersonParams{ID: channelID, PersonID: nullString(personID)})
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	n, err := q.DeleteChannel(ctx, db.DeleteChannelParams{ID: channelID, PersonID: nullString(personID)})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	if err := logChannel(ctx, q, c.WorkspaceID, personID, c.ID, c.Kind, "channel.removed", true, time.Now()); err != nil {
		return err
	}
	return tx.Commit()
}

// logChannel writes a change to where a person is reached: never the address or
// the device's name (C2). byThem says the person did it, not Allstill.
func logChannel(ctx context.Context, q *db.Queries, workspaceID, personID, channelID, channelKind, kind string, byThem bool, at time.Time) error {
	person, err := personActor(ctx, q, workspaceID, personID)
	if err != nil {
		return err
	}
	actor := byAllstill
	if byThem {
		actor = person
	}
	return writeLog(ctx, q, logEntry{
		WorkspaceID: workspaceID, At: at, Kind: kind, AboutKind: "channel", AboutID: channelID,
		By: actor, Person: person, Details: map[string]any{"channel": channelKind},
	})
}

// TestsPerDay is how many test messages one person may send in a day, and
// TestEvery how often to one channel: a test can go to any address.
const (
	TestsPerDay = 10
	TestEvery   = time.Minute
)

// Test queues a test message to one of the person's channels, to go out even
// in shadow mode.
func (s *Store) Test(ctx context.Context, personID, channelID string, at time.Time) error {
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	q := db.New(tx)
	c, err := q.ChannelOfPerson(ctx, db.ChannelOfPersonParams{ID: channelID, PersonID: nullString(personID)})
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	last, err := q.LastTestOfChannel(ctx, nullString(c.ID))
	if err != nil {
		return err
	}
	if last != 0 && at.Sub(time.UnixMilli(last)) < TestEvery {
		return LimitError{"A test went to it less than a minute ago; wait a moment before the next."}
	}
	n, err := q.TestsOfPersonSince(ctx, db.TestsOfPersonSinceParams{PersonID: personID, CreatedAt: at.Add(-24 * time.Hour).UnixMilli()})
	if err != nil {
		return err
	}
	if n >= TestsPerDay {
		return LimitError{fmt.Sprintf("You sent %d test messages in the last day; try again tomorrow.", TestsPerDay)}
	}
	d := id.New(id.Delivery)
	if err := q.CreatePendingDelivery(ctx, db.CreatePendingDeliveryParams{
		ID: d, WorkspaceID: c.WorkspaceID, PersonID: personID, ChannelID: nullString(c.ID), ChannelKind: nullString(c.Kind),
		Kind: "test", Items: "[]", CreatedAt: at.UnixMilli(), NextAt: nullMs(at), MessageID: nullString(d),
	}); err != nil {
		return err
	}
	return tx.Commit()
}

// SetConfirm keeps the hash of the token in a channel's test message; opening
// its link confirms the channel. A newer test replaces it. It changes only
// that field, so a subscription saved meanwhile is kept.
// When the channel no longer holds the subscription or address the test
// goes to, the token confirms nothing.
func (s *Store) SetConfirm(ctx context.Context, p Pending, token string) error {
	holds := p.Address
	if p.ChannelKind == "webpush" {
		holds = b64.EncodeToString(p.Sub.Auth)
	}
	// As encoding/json writes []byte, so channelConfig reads it back.
	return db.New(s.w).SetChannelConfirm(ctx, db.SetChannelConfirmParams{
		Confirm: base64.StdEncoding.EncodeToString(hash(token)), ID: p.ChannelID, PersonID: nullString(p.PersonID), Holds: holds,
	})
}

// Confirm marks one of the person's channels as reached, when token is the
// one in its last test message.
func (s *Store) Confirm(ctx context.Context, personID, channelID, token string, at time.Time) error {
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	q := db.New(tx)
	c, err := q.ChannelOfPerson(ctx, db.ChannelOfPersonParams{ID: channelID, PersonID: nullString(personID)})
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	var cfg channelConfig
	json.Unmarshal([]byte(c.Config), &cfg)
	if len(cfg.Confirm) == 0 || subtle.ConstantTimeCompare(cfg.Confirm, hash(token)) != 1 {
		return ErrNotFound
	}
	if err := q.ConfirmChannel(ctx, db.ConfirmChannelParams{VerifiedAt: nullMs(at), ID: c.ID}); err != nil {
		return err
	}
	if !c.VerifiedAt.Valid {
		if err := logChannel(ctx, q, c.WorkspaceID, personID, c.ID, c.Kind, "channel.confirmed", true, at); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Targets are the person's channels a message may go to now: confirmed
// devices made for key, and confirmed email addresses that did not fail.
func (s *Store) Targets(ctx context.Context, personID, key string) (push, email []string, err error) {
	return targets(ctx, db.New(s.r), personID, key)
}

func targets(ctx context.Context, q *db.Queries, personID, key string) (push, email []string, err error) {
	rows, err := q.ChannelsOfPerson(ctx, nullString(personID))
	if err != nil {
		return nil, nil, err
	}
	for _, r := range rows {
		c := channelOf(r)
		switch {
		case !c.Usable(key):
		case c.Kind == "webpush":
			push = append(push, c.ID)
		case c.Kind == "email":
			email = append(email, c.ID)
		}
	}
	return push, email, nil
}

// Queue writes one pending delivery per channel, to be sent at once, and
// what its recipients were told, in one transaction: once told, the message
// is sent at least once (R14). The deliveries share one message id.
func (s *Store) Queue(ctx context.Context, d Delivery, kind string, channels []string, told []Told) error {
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	q := db.New(tx)
	if err := queue(ctx, q, d, kind, channels, id.New(id.Delivery), d.At); err != nil {
		return err
	}
	if err := record(ctx, q, nil, told); err != nil {
		return err
	}
	return tx.Commit()
}

// QueueDigest is Queue for a digest, which also records that the sender
// looked at the person's digest at d.At.
func (s *Store) QueueDigest(ctx context.Context, d Delivery, kind string, channels []string, told []Told) error {
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	q := db.New(tx)
	if err := queue(ctx, q, d, kind, channels, id.New(id.Delivery), d.At); err != nil {
		return err
	}
	if err := record(ctx, q, nil, told); err != nil {
		return err
	}
	if err := q.SetDigestAt(ctx, db.SetDigestAtParams{DigestAt: nullMs(d.At), ID: d.PersonID}); err != nil {
		return err
	}
	return tx.Commit()
}

// queue writes a delivery of message to each channel, due at next. They
// carry the message's time, so a later fallback does not move the gather
// period.
func queue(ctx context.Context, q *db.Queries, d Delivery, kind string, channels []string, message string, next time.Time) error {
	items, err := json.Marshal(d.Items)
	if err != nil {
		return err
	}
	for _, c := range channels {
		if err := q.CreatePendingDelivery(ctx, db.CreatePendingDeliveryParams{
			ID: id.New(id.Delivery), WorkspaceID: d.WorkspaceID, PersonID: d.PersonID, ChannelID: nullString(c), ChannelKind: nullString(kind),
			Kind: d.Kind, Items: string(items), CreatedAt: d.At.UnixMilli(), NextAt: nullMs(next), MessageID: nullString(message),
		}); err != nil {
			return err
		}
	}
	return nil
}

// Pending is a delivery whose time to be sent has come.
type Pending struct {
	ID          string
	WorkspaceID string
	MessageID   string
	PersonID    string
	Kind        string
	Items       []Item
	Attempts    int
	CreatedAt   time.Time
	ChannelID   string
	// Whose the channel is now, and whether it is confirmed: a device that
	// changed hands or subscribed anew gets nothing meant for before.
	ChannelPerson string
	Confirmed     bool
	ChannelKind   string // webpush or email
	ChannelFailed bool   // alerts to it failed since it was confirmed
	Key           string // for webpush: the public key the channel was made for
	Sub           webpush.Subscription
	Address       string // for email
}

// PendingDeliveries returns up to limit deliveries due by now. One whose
// channel was removed has no ChannelID.
func (s *Store) PendingDeliveries(ctx context.Context, now time.Time, limit int) ([]Pending, error) {
	rows, err := db.New(s.r).PendingDeliveries(ctx, db.PendingDeliveriesParams{NextAt: nullMs(now), Limit: int64(limit)})
	if err != nil {
		return nil, err
	}
	ps := make([]Pending, 0, len(rows))
	for _, r := range rows {
		p := Pending{
			ID: r.ID, WorkspaceID: r.WorkspaceID, MessageID: r.MessageID, PersonID: r.PersonID, Kind: r.Kind,
			Attempts: int(r.Attempts), CreatedAt: time.UnixMilli(r.CreatedAt).UTC(),
			ChannelID: r.ChannelID, ChannelKind: r.ChannelKind, ChannelPerson: r.ChannelPerson,
			Confirmed: r.VerifiedAt.Valid, ChannelFailed: r.FailedAt.Valid,
		}
		if err := json.Unmarshal([]byte(r.Items), &p.Items); err != nil {
			return nil, err
		}
		var cfg channelConfig
		if err := json.Unmarshal([]byte(r.Config), &cfg); err != nil {
			return nil, err
		}
		p.Key, p.Sub, p.Address = cfg.Key, cfg.subscription(), cfg.Address
		ps = append(ps, p)
	}
	return ps, nil
}

// PendingDowns returns the incidents whose down is still on its way to the
// person, which a recovery waits for (R6).
func (s *Store) PendingDowns(ctx context.Context, personID string) (map[string]bool, error) {
	ids, err := db.New(s.r).PendingDowns(ctx, personID)
	if err != nil {
		return nil, err
	}
	pending := make(map[string]bool, len(ids))
	for _, id := range ids {
		pending[id] = true
	}
	return pending, nil
}

// StillFollowed returns which of the incidents are open and followed by the
// person now: what a delivery may still say is down (R13, R15).
func (s *Store) StillFollowed(ctx context.Context, personID string, incidentIDs []string) (map[string]bool, error) {
	ids, err := db.New(s.r).StillFollowed(ctx, db.StillFollowedParams{PersonID: personID, Ids: incidentIDs})
	if err != nil {
		return nil, err
	}
	open := make(map[string]bool, len(ids))
	for _, id := range ids {
		open[id] = true
	}
	return open, nil
}

// Unheard records that the person never heard the downs of these incidents,
// unless one of their channels did, so no recovery follows them (R6).
func (s *Store) Unheard(ctx context.Context, personID string, incidentIDs []string) error {
	return unheard(ctx, db.New(s.w), personID, incidentIDs, time.Time{})
}

// unheard is Unheard, with when the sender looks at them again; zero for
// never, as when they are no longer true.
func unheard(ctx context.Context, q *db.Queries, personID string, incidentIDs []string, again time.Time) error {
	if len(incidentIDs) == 0 {
		return nil
	}
	return q.Unheard(ctx, db.UnheardParams{DueAt: nullMs(again), PersonID: personID, Ids: incidentIDs})
}

// LookAgain is when a message that reached no channel is decided again.
const LookAgain = 10 * time.Minute

// Shadowed records that a pending delivery was not sent because shadow mode
// was on when its time came: what it said was told in shadow mode only.
func (s *Store) Shadowed(ctx context.Context, p Pending) error {
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	q := db.New(tx)
	if err := q.FinishDelivery(ctx, db.FinishDeliveryParams{State: "shadow", ID: p.ID}); err != nil {
		return err
	}
	// Only what it told: a digest's still-open and resolved-unseen lines
	// tell nothing.
	var ids []string
	for _, it := range p.Items {
		if it.IncidentID != "" && (it.What == "down" || it.What == "recovered") {
			ids = append(ids, it.IncidentID)
		}
	}
	if len(ids) > 0 {
		if err := q.HeardInShadow(ctx, db.HeardInShadowParams{PersonID: p.PersonID, Ids: ids}); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Sent records that a pending delivery went out.
func (s *Store) Sent(ctx context.Context, deliveryID string, at time.Time) error {
	return db.New(s.w).FinishDelivery(ctx, db.FinishDeliveryParams{State: "sent", SentAt: nullMs(at), ID: deliveryID})
}

// Failed records that a pending delivery cannot go out, and why. When gone,
// its channel no longer exists at the push service and is removed;
// otherwise, when channelFailed, the channel is marked as failing.
//
// When no other delivery of its message is on its way or went out, the
// message goes the other way, in the same transaction: an alert by email, a
// digest by push (intent/alerts/plan.md, step 4). When there is no other way
// left, it reached nobody, and its downs are unheard and decided again in
// LookAgain. ways says which ways the server can send now.
func (s *Store) Failed(ctx context.Context, p Pending, reason string, gone, channelFailed bool, at time.Time, ways Ways) error {
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	q := db.New(tx)
	if err := q.FinishDelivery(ctx, db.FinishDeliveryParams{State: "failed", LastError: nullString(reason), ID: p.ID}); err != nil {
		return err
	}
	switch {
	case p.ChannelID == "":
	case gone:
		// Another message to it, sent from the same batch, can find it gone already.
		var n int64
		if n, err = q.DeleteChannelByID(ctx, p.ChannelID); err == nil && n > 0 {
			err = logChannel(ctx, q, p.WorkspaceID, p.PersonID, p.ChannelID, p.ChannelKind, "channel.removed", false, at)
		}
	case channelFailed:
		err = q.SetChannelFailed(ctx, db.SetChannelFailedParams{FailedAt: nullMs(at), ID: p.ChannelID})
	}
	if err != nil {
		return err
	}
	if p.Kind != "test" && reason != NoLongerTrue {
		if err := fallBack(ctx, q, p, at, ways); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Ways are the ways a server can send now: Key is the current VAPID key,
// which devices must have subscribed for, and Push and Mail say whether it
// is set up to push and to email.
type Ways struct {
	Key        string
	Push, Mail bool
}

func fallBack(ctx context.Context, q *db.Queries, p Pending, at time.Time, ways Ways) error {
	siblings, err := q.MessageDeliveries(ctx, nullString(p.MessageID))
	if err != nil {
		return err
	}
	tried := map[string]bool{p.ChannelKind: true}
	for _, d := range siblings {
		if d.State == "pending" || d.State == "sent" {
			return nil
		}
		tried[d.ChannelKind] = true
	}
	push, email, err := targets(ctx, q, p.PersonID, ways.Key)
	if err != nil {
		return err
	}
	if !ways.Push {
		push = nil
	}
	if !ways.Mail {
		email = nil
	}
	var next []string
	kind := "email"
	switch {
	case !tried["email"]:
		next = email
	case !tried["webpush"] && p.Kind == "digest":
		next, kind = push, "webpush"
	}
	if len(next) > 0 {
		d := Delivery{WorkspaceID: p.WorkspaceID, PersonID: p.PersonID, Kind: p.Kind, Items: p.Items, At: p.CreatedAt}
		return queue(ctx, q, d, kind, next, p.MessageID, at)
	}
	var downs []string
	for _, it := range p.Items {
		if it.What == "down" && it.IncidentID != "" {
			downs = append(downs, it.IncidentID)
		}
	}
	return unheard(ctx, q, p.PersonID, downs, at.Add(LookAgain))
}

// Postpone has a delivery wait until next without using a try, as when the
// mail server cannot be reached this pass.
func (s *Store) Postpone(ctx context.Context, deliveryID string, next time.Time) error {
	return db.New(s.w).PostponeDelivery(ctx, db.PostponeDeliveryParams{NextAt: nullMs(next), ID: deliveryID})
}

// Retry records a failed attempt, and when to try again.
func (s *Store) Retry(ctx context.Context, deliveryID, reason string, next time.Time) error {
	return db.New(s.w).RetryDelivery(ctx, db.RetryDeliveryParams{LastError: nullString(reason), NextAt: nullMs(next), ID: deliveryID})
}

// OldestDue is the earliest moment the sender should already have acted on:
// a recipient to look at, or a delivery to send. It is zero when nothing
// waits.
func (s *Store) OldestDue(ctx context.Context) (time.Time, error) {
	ms, err := db.New(s.r).OldestDue(ctx)
	if err != nil || ms == 0 {
		return time.Time{}, err
	}
	return time.UnixMilli(ms).UTC(), nil
}

// NoLongerTrue is why a message was left out: by the time it could go,
// nothing in it was still true (R13). It is not a failure to reach anyone.
const NoLongerTrue = "nothing in it was still true when it could go"

// FailedSince counts the person's messages that failed since then.
func (s *Store) FailedSince(ctx context.Context, personID string, since time.Time) (int, error) {
	n, err := db.New(s.r).FailedSince(ctx, db.FailedSinceParams{PersonID: personID, CreatedAt: since.UnixMilli(), NotFailed: nullString(NoLongerTrue)})
	return int(n), err
}
