// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
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

// pushConfig is a webpush channel's configuration: the subscription, the
// public key it was made for, and the hash of the token in its last test
// message, which confirms the channel when the link in it is opened.
type pushConfig struct {
	Endpoint string `json:"endpoint"`
	P256dh   string `json:"p256dh"`
	Auth     string `json:"auth"`
	Key      string `json:"key"`
	Confirm  []byte `json:"confirm,omitempty"`
}

var b64 = base64.RawURLEncoding

func (c pushConfig) subscription() webpush.Subscription {
	p256dh, _ := b64.DecodeString(c.P256dh)
	auth, _ := b64.DecodeString(c.Auth)
	return webpush.Subscription{Endpoint: c.Endpoint, P256dh: p256dh, Auth: auth}
}

// Channel is one of a person's channels, as they see it. It never holds the
// endpoint or the keys of a subscription (ADR 0003).
type Channel struct {
	ID         string
	Kind       string
	Name       string
	Key        string // for webpush: the public key it was made for
	VerifiedAt time.Time
	FailedAt   time.Time
	CreatedAt  time.Time
}

// Usable says whether alerts may go to the channel: a test message reached
// it, and, for webpush, it was made for the current key.
func (c Channel) Usable(key string) bool {
	return !c.VerifiedAt.IsZero() && (c.Kind != "webpush" || c.Key == key)
}

func channelOf(r db.Channel) Channel {
	var cfg pushConfig
	json.Unmarshal([]byte(r.Config), &cfg)
	return Channel{
		ID: r.ID, Kind: r.Kind, Name: r.Name, Key: cfg.Key,
		VerifiedAt: msTime(r.VerifiedAt), FailedAt: msTime(r.FailedAt), CreatedAt: time.UnixMilli(r.CreatedAt).UTC(),
	}
}

// SavePush keeps a browser's subscription as a webpush channel of the person,
// made for key. The same subscription sent again keeps its channel and its
// confirmation; other keys for the same endpoint, or another person, replace
// it, and it must be confirmed again.
func (s *Store) SavePush(ctx context.Context, workspaceID, personID, name string, sub webpush.Subscription, key string) (Channel, error) {
	cfg := pushConfig{Endpoint: sub.Endpoint, P256dh: b64.EncodeToString(sub.P256dh), Auth: b64.EncodeToString(sub.Auth), Key: key}
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return Channel{}, err
	}
	defer tx.Rollback()
	q := db.New(tx)
	old, err := q.ChannelOfEndpoint(ctx, db.ChannelOfEndpointParams{WorkspaceID: workspaceID, Endpoint: sub.Endpoint})
	switch {
	case err == nil:
		var was pushConfig
		json.Unmarshal([]byte(old.Config), &was)
		if old.PersonID.String == personID && was.P256dh == cfg.P256dh && was.Auth == cfg.Auth && was.Key == key {
			return channelOf(old), nil
		}
		b, _ := json.Marshal(cfg)
		if err := q.ReplaceChannel(ctx, db.ReplaceChannelParams{PersonID: nullString(personID), Name: name, Config: string(b), ID: old.ID}); err != nil {
			return Channel{}, err
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
	n, err := db.New(s.w).DeleteChannel(ctx, db.DeleteChannelParams{ID: channelID, PersonID: nullString(personID)})
	if err == nil && n == 0 {
		err = ErrNotFound
	}
	return err
}

// Test queues a test message to one of the person's channels, to go out even
// in shadow mode.
func (s *Store) Test(ctx context.Context, personID, channelID string, at time.Time) error {
	q := db.New(s.w)
	c, err := q.ChannelOfPerson(ctx, db.ChannelOfPersonParams{ID: channelID, PersonID: nullString(personID)})
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	return q.CreatePendingDelivery(ctx, db.CreatePendingDeliveryParams{
		ID: id.New(id.Delivery), WorkspaceID: c.WorkspaceID, PersonID: personID, ChannelID: nullString(c.ID),
		Kind: "test", Items: "[]", CreatedAt: at.UnixMilli(), NextAt: nullMs(at),
	})
}

// SetConfirm keeps the hash of the token in a channel's test message; opening
// its link confirms the channel. A newer test replaces it. It changes only
// that field, so a subscription saved meanwhile is kept.
// When the channel no longer holds the subscription the test goes to, the
// token confirms nothing.
func (s *Store) SetConfirm(ctx context.Context, p Pending, token string) error {
	// As encoding/json writes []byte, so pushConfig reads it back.
	return db.New(s.w).SetChannelConfirm(ctx, db.SetChannelConfirmParams{
		Confirm: base64.StdEncoding.EncodeToString(hash(token)), ID: p.ChannelID, PersonID: nullString(p.PersonID), Auth: b64.EncodeToString(p.Sub.Auth),
	})
}

// Confirm marks one of the person's channels as reached, when token is the
// one in its last test message.
func (s *Store) Confirm(ctx context.Context, personID, channelID, token string, at time.Time) error {
	q := db.New(s.w)
	c, err := q.ChannelOfPerson(ctx, db.ChannelOfPersonParams{ID: channelID, PersonID: nullString(personID)})
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	var cfg pushConfig
	json.Unmarshal([]byte(c.Config), &cfg)
	if len(cfg.Confirm) == 0 || subtle.ConstantTimeCompare(cfg.Confirm, hash(token)) != 1 {
		return ErrNotFound
	}
	return q.ConfirmChannel(ctx, db.ConfirmChannelParams{VerifiedAt: nullMs(at), ID: c.ID})
}

// PushTarget is a channel a message can be pushed to.
type PushTarget struct {
	ChannelID string
	Sub       webpush.Subscription
}

// PushTargets returns the person's channels that alerts may be pushed to:
// confirmed, and made for key.
func (s *Store) PushTargets(ctx context.Context, personID, key string) ([]PushTarget, error) {
	rows, err := db.New(s.r).ChannelsOfPerson(ctx, nullString(personID))
	if err != nil {
		return nil, err
	}
	var ts []PushTarget
	for _, r := range rows {
		var cfg pushConfig
		if r.Kind != "webpush" || !r.VerifiedAt.Valid || json.Unmarshal([]byte(r.Config), &cfg) != nil || cfg.Key != key {
			continue
		}
		ts = append(ts, PushTarget{ChannelID: r.ID, Sub: cfg.subscription()})
	}
	return ts, nil
}

// Queue writes one pending delivery per channel, to be sent at once, and
// what its recipients were told, in one transaction: once told, the message
// is sent at least once (R14).
func (s *Store) Queue(ctx context.Context, d Delivery, channels []string, told []Told) error {
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	q := db.New(tx)
	if err := queue(ctx, q, d, channels); err != nil {
		return err
	}
	if err := record(ctx, q, nil, told); err != nil {
		return err
	}
	return tx.Commit()
}

// QueueDigest is Queue for a digest, which also records that the sender
// looked at the person's digest at d.At.
func (s *Store) QueueDigest(ctx context.Context, d Delivery, channels []string, told []Told) error {
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	q := db.New(tx)
	if err := queue(ctx, q, d, channels); err != nil {
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

func queue(ctx context.Context, q *db.Queries, d Delivery, channels []string) error {
	items, err := json.Marshal(d.Items)
	if err != nil {
		return err
	}
	for _, c := range channels {
		if err := q.CreatePendingDelivery(ctx, db.CreatePendingDeliveryParams{
			ID: id.New(id.Delivery), WorkspaceID: d.WorkspaceID, PersonID: d.PersonID, ChannelID: nullString(c),
			Kind: d.Kind, Items: string(items), CreatedAt: d.At.UnixMilli(), NextAt: nullMs(d.At),
		}); err != nil {
			return err
		}
	}
	return nil
}

// Pending is a delivery whose time to be sent has come.
type Pending struct {
	ID        string
	PersonID  string
	Kind      string
	Items     []Item
	Attempts  int
	CreatedAt time.Time
	ChannelID string
	// Whose the channel is now, and whether it is confirmed: a device that
	// changed hands or subscribed anew gets nothing meant for before.
	ChannelPerson string
	Confirmed     bool
	Key           string // the public key the channel was made for
	Sub           webpush.Subscription
}

// PendingDeliveries returns up to limit deliveries due by now. Those whose
// channel was removed fail.
func (s *Store) PendingDeliveries(ctx context.Context, now time.Time, limit int) ([]Pending, error) {
	if err := db.New(s.w).FailOrphanDeliveries(ctx); err != nil {
		return nil, err
	}
	rows, err := db.New(s.r).PendingDeliveries(ctx, db.PendingDeliveriesParams{NextAt: nullMs(now), Limit: int64(limit)})
	if err != nil {
		return nil, err
	}
	ps := make([]Pending, 0, len(rows))
	for _, r := range rows {
		p := Pending{
			ID: r.ID, PersonID: r.PersonID, Kind: r.Kind, Attempts: int(r.Attempts), CreatedAt: time.UnixMilli(r.CreatedAt).UTC(),
			ChannelID: r.ChannelID, ChannelPerson: r.ChannelPerson, Confirmed: r.VerifiedAt.Valid,
		}
		if err := json.Unmarshal([]byte(r.Items), &p.Items); err != nil {
			return nil, err
		}
		var cfg pushConfig
		if err := json.Unmarshal([]byte(r.Config), &cfg); err != nil {
			return nil, err
		}
		p.Key, p.Sub = cfg.Key, cfg.subscription()
		ps = append(ps, p)
	}
	return ps, nil
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
// unless one of their devices did, so no recovery follows them (R6).
func (s *Store) Unheard(ctx context.Context, personID string, incidentIDs []string) error {
	if len(incidentIDs) == 0 {
		return nil
	}
	return db.New(s.w).Unheard(ctx, db.UnheardParams{PersonID: personID, Ids: incidentIDs})
}

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
func (s *Store) Failed(ctx context.Context, p Pending, reason string, gone, channelFailed bool, at time.Time) error {
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
	case gone:
		err = q.DeleteChannelByID(ctx, p.ChannelID)
	case channelFailed:
		err = q.SetChannelFailed(ctx, db.SetChannelFailedParams{FailedAt: nullMs(at), ID: p.ChannelID})
	}
	if err != nil {
		return err
	}
	return tx.Commit()
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
