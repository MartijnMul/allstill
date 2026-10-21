// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/MartijnMul/allstill/internal/id"
	"github.com/MartijnMul/allstill/internal/store/internal/db"
)

// How long things last. A session moves its end forward while it is used, at
// most once per SessionTouch, but never past SessionMax after it began.
const (
	LinkLifetime = 24 * time.Hour
	SessionIdle  = 30 * 24 * time.Hour
	SessionMax   = 365 * 24 * time.Hour
	SessionTouch = time.Hour
)

var (
	ErrLinkUsed    = errors.New("this link has been used")
	ErrLinkExpired = errors.New("this link has expired")
)

// Person is someone who signs in.
type Person struct {
	ID          string
	WorkspaceID string
	Name        string
	Email       string
	TimeZone    string
	Admin       bool
}

func personOf(p db.Person) Person {
	return Person{
		ID: p.ID, WorkspaceID: p.WorkspaceID, Name: p.Name, Email: p.Email.String,
		TimeZone: p.TimeZone, Admin: p.WorkspaceAdmin == 1,
	}
}

// Person returns a person who is not disabled.
func (s *Store) Person(ctx context.Context, workspaceID, personID string) (Person, error) {
	p, err := db.New(s.r).Person(ctx, db.PersonParams{WorkspaceID: workspaceID, ID: personID})
	if errors.Is(err, sql.ErrNoRows) {
		return Person{}, ErrNotFound
	}
	return personOf(p), err
}

// PersonByEmail returns the person, not disabled, with this email address.
func (s *Store) PersonByEmail(ctx context.Context, workspaceID, email string) (Person, error) {
	p, err := db.New(s.r).PersonByEmail(ctx, db.PersonByEmailParams{
		WorkspaceID: workspaceID, Email: sql.NullString{String: email, Valid: true},
	})
	if errors.Is(err, sql.ErrNoRows) {
		return Person{}, ErrNotFound
	}
	return personOf(p), err
}

// CreatePasskeyLink returns a one-time link token that lets a person add a
// passkey. Their earlier unused links stop working.
func (s *Store) CreatePasskeyLink(ctx context.Context, workspaceID, personID string, at time.Time) (string, error) {
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	q := db.New(tx)
	if _, err := q.Person(ctx, db.PersonParams{WorkspaceID: workspaceID, ID: personID}); errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	} else if err != nil {
		return "", err
	}
	if err := q.DeleteUnusedLinks(ctx, personID); err != nil {
		return "", err
	}
	token := id.Key()
	err = q.CreatePasskeyLink(ctx, db.CreatePasskeyLinkParams{
		TokenHash: hash(token), WorkspaceID: workspaceID, PersonID: personID,
		CreatedAt: at.UnixMilli(), ExpiresAt: at.Add(LinkLifetime).UnixMilli(),
	})
	if err != nil {
		return "", err
	}
	return token, tx.Commit()
}

// PasskeyLink returns the person a link is for: ErrNotFound for an unknown
// link or a disabled person, ErrLinkUsed or ErrLinkExpired otherwise.
func (s *Store) PasskeyLink(ctx context.Context, token string, at time.Time) (Person, error) {
	q := db.New(s.r)
	l, err := q.PasskeyLink(ctx, hash(token))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return Person{}, ErrNotFound
	case err != nil:
		return Person{}, err
	case l.UsedAt.Valid:
		return Person{}, ErrLinkUsed
	case l.ExpiresAt <= at.UnixMilli():
		return Person{}, ErrLinkExpired
	}
	return s.Person(ctx, l.WorkspaceID, l.PersonID)
}

// Passkey is one of a person's passkeys.
type Passkey struct {
	ID           string
	CredentialID []byte
	RPID         string
	Name         string
	Record       []byte // the WebAuthn credential record, JSON
	Created      time.Time
	LastUsed     time.Time
}

// Passkeys returns a person's passkeys, oldest first.
func (s *Store) Passkeys(ctx context.Context, workspaceID, personID string) ([]Passkey, error) {
	rows, err := db.New(s.r).PasskeysOfPerson(ctx, db.PasskeysOfPersonParams{WorkspaceID: workspaceID, PersonID: personID})
	if err != nil {
		return nil, err
	}
	keys := make([]Passkey, len(rows))
	for i, r := range rows {
		keys[i] = Passkey{
			ID: r.ID, CredentialID: r.CredentialID, RPID: r.RpID, Name: r.Name, Record: []byte(r.Record),
			Created: time.UnixMilli(r.CreatedAt).UTC(), LastUsed: msTime(r.LastUsedAt),
		}
	}
	return keys, nil
}

// PasskeysOfOtherHosts counts the passkeys made for another host name than
// rpID: after the server moved, they no longer work.
func (s *Store) PasskeysOfOtherHosts(ctx context.Context, rpID string) (int64, error) {
	return db.New(s.r).PasskeysOfOtherHosts(ctx, rpID)
}

// NewPasskey is a passkey a person just made.
type NewPasskey struct {
	CredentialID []byte
	RPID         string
	Name         string
	Record       []byte
}

// AddPasskey uses a link: it stores the passkey for the link's person and
// opens a session with it, all or nothing. It returns the session's token.
// A link that was used meanwhile gives ErrLinkUsed; a credential id that
// exists already, ErrExists.
func (s *Store) AddPasskey(ctx context.Context, linkToken string, k NewPasskey, userAgent string, at time.Time) (string, error) {
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	q := db.New(tx)
	l, err := q.PasskeyLink(ctx, hash(linkToken))
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	} else if err != nil {
		return "", err
	}
	n, err := q.UseLink(ctx, db.UseLinkParams{At: sql.NullInt64{Int64: at.UnixMilli(), Valid: true}, TokenHash: hash(linkToken)})
	if err != nil {
		return "", err
	}
	if n != 1 {
		if l.UsedAt.Valid {
			return "", ErrLinkUsed
		}
		return "", ErrLinkExpired
	}
	passkeyID := id.New(id.Passkey)
	err = q.CreatePasskey(ctx, db.CreatePasskeyParams{
		ID: passkeyID, WorkspaceID: l.WorkspaceID, PersonID: l.PersonID, CredentialID: k.CredentialID,
		RpID: k.RPID, Name: k.Name, Record: string(k.Record), CreatedAt: at.UnixMilli(),
	})
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed: passkeys.credential_id") {
			return "", ErrExists
		}
		return "", err
	}
	token, err := createSession(ctx, q, l.WorkspaceID, l.PersonID, passkeyID, userAgent, at)
	if err != nil {
		return "", err
	}
	return token, tx.Commit()
}

// SignIn stores a passkey's record as it is after a sign-in and opens a
// session with it. It returns the session's token.
func (s *Store) SignIn(ctx context.Context, workspaceID, personID, passkeyID string, record []byte, userAgent string, at time.Time) (string, error) {
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	q := db.New(tx)
	n, err := q.UsePasskey(ctx, db.UsePasskeyParams{
		Record: string(record), LastUsedAt: sql.NullInt64{Int64: at.UnixMilli(), Valid: true},
		WorkspaceID: workspaceID, PersonID: personID, ID: passkeyID,
	})
	if err != nil {
		return "", err
	}
	if n != 1 {
		return "", ErrNotFound
	}
	token, err := createSession(ctx, q, workspaceID, personID, passkeyID, userAgent, at)
	if err != nil {
		return "", err
	}
	return token, tx.Commit()
}

func createSession(ctx context.Context, q *db.Queries, workspaceID, personID, passkeyID, userAgent string, at time.Time) (string, error) {
	token := id.Key()
	if len(userAgent) > 512 {
		userAgent = userAgent[:512]
	}
	err := q.CreateSession(ctx, db.CreateSessionParams{
		ID: id.New(id.Session), TokenHash: hash(token), WorkspaceID: workspaceID, PersonID: personID,
		PasskeyID: passkeyID, UserAgent: strings.ToValidUTF8(userAgent, ""),
		CreatedAt: at.UnixMilli(), SeenAt: at.UnixMilli(), ExpiresAt: at.Add(SessionIdle).UnixMilli(),
	})
	return token, err
}

// Session is a signed-in browser.
type Session struct {
	ID          string
	WorkspaceID string
	PersonID    string
}

// Session returns the session a token opens, or ErrNotFound when it has ended
// or its person is disabled. It moves the session's end forward.
func (s *Store) Session(ctx context.Context, token string, at time.Time) (Session, error) {
	r, err := db.New(s.r).SessionByToken(ctx, hash(token))
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrNotFound
	}
	if err != nil {
		return Session{}, err
	}
	ms := at.UnixMilli()
	if r.ExpiresAt <= ms {
		return Session{}, ErrNotFound
	}
	if ms-r.SeenAt >= SessionTouch.Milliseconds() {
		end := min(at.Add(SessionIdle).UnixMilli(), r.CreatedAt+SessionMax.Milliseconds())
		if err := db.New(s.w).TouchSession(ctx, db.TouchSessionParams{SeenAt: ms, ExpiresAt: end, ID: r.ID}); err != nil {
			return Session{}, err
		}
	}
	return Session{ID: r.ID, WorkspaceID: r.WorkspaceID, PersonID: r.PersonID}, nil
}

// EndSession signs a browser out.
func (s *Store) EndSession(ctx context.Context, token string) error {
	return db.New(s.w).DeleteSession(ctx, hash(token))
}

// prune removes sessions and links that have ended.
func prune(ctx context.Context, q *db.Queries, at time.Time) error {
	if err := q.DeleteExpiredSessions(ctx, at.UnixMilli()); err != nil {
		return err
	}
	return q.DeleteExpiredLinks(ctx, at.UnixMilli())
}

func hash(secret string) []byte {
	h := sha256.Sum256([]byte(secret))
	return h[:]
}
