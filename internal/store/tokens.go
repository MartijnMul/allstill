// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/MartijnMul/allstill/internal/id"
	"github.com/MartijnMul/allstill/internal/store/internal/db"
)

// A person's sign-ins from the command line (intent/cli-anywhere/spec.md). A
// sign-in is good for as long as a browser session is, and acts with the person's
// role as it is at that moment.

// The scopes a credential can have. With read it acts as a viewer (R5).
const (
	ScopeRead   = "read"
	ScopeManage = "manage"
)

// personTokenPrefix starts every sign-in's token, so the server knows where to look
// and a leaked one can be recognised.
const personTokenPrefix = "alt_"

// IsPersonToken says a bearer value claims to be a person's sign-in.
func IsPersonToken(token string) bool { return strings.HasPrefix(token, personTokenPrefix) }

// PersonToken is a sign-in, without its token.
type PersonToken struct {
	ID          string
	WorkspaceID string
	PersonID    string
	Name        string
	Scope       string
	Created     time.Time
	LastUsed    time.Time
	Expires     time.Time
}

// NewPersonToken is a sign-in someone just approved with a passkey.
type NewPersonToken struct {
	WorkspaceID string
	PersonID    string
	PasskeyID   string
	// Record is the passkey's record as it is after approving this.
	Record []byte
	Name   string
	Scope  string
}

// CreatePersonToken makes a sign-in, all or nothing with storing the passkey's
// record, and returns its token, which is not kept. It is made when it is approved,
// not when it is fetched, so from then on its person sees it, and can end it. A
// person who is disabled or a passkey that is gone gives ErrNotFound. It opens no
// session, unlike SignIn.
func (s *Store) CreatePersonToken(ctx context.Context, t NewPersonToken, at time.Time) (string, error) {
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	q := db.New(tx)
	if _, err := q.Person(ctx, db.PersonParams{WorkspaceID: t.WorkspaceID, ID: t.PersonID}); errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	} else if err != nil {
		return "", err
	}
	n, err := q.UsePasskey(ctx, db.UsePasskeyParams{
		Record: string(t.Record), LastUsedAt: sql.NullInt64{Int64: at.UnixMilli(), Valid: true},
		WorkspaceID: t.WorkspaceID, PersonID: t.PersonID, ID: t.PasskeyID,
	})
	if err != nil {
		return "", err
	}
	if n != 1 {
		return "", ErrNotFound
	}
	token, tokenID := personTokenPrefix+id.Key(), id.New(id.SignIn)
	err = q.CreatePersonToken(ctx, db.CreatePersonTokenParams{
		ID: tokenID, TokenHash: hash(token), WorkspaceID: t.WorkspaceID, PersonID: t.PersonID, PasskeyID: t.PasskeyID,
		Name: t.Name, Scope: t.Scope, CreatedAt: at.UnixMilli(), UsedAt: at.UnixMilli(), ExpiresAt: at.Add(SessionIdle).UnixMilli(),
	})
	if err != nil {
		return "", err
	}
	who, err := personActor(ctx, q, t.WorkspaceID, t.PersonID)
	if err != nil {
		return "", err
	}
	if err := writeLog(ctx, q, logEntry{
		WorkspaceID: t.WorkspaceID, At: at, Kind: "token.made", AboutKind: "sign-in", AboutID: tokenID, AboutName: t.Name,
		By: who, Person: who, Details: map[string]any{"scope": t.Scope},
	}); err != nil {
		return "", err
	}
	return token, tx.Commit()
}

// AccessOfPersonToken returns what a sign-in may do now, with its person's role as
// it is at this moment, or ErrNotFound when it has ended or its person is disabled.
// It moves the sign-in's end forward, at most once per SessionTouch and never past
// SessionMax after it began.
func (s *Store) AccessOfPersonToken(ctx context.Context, token string, at time.Time) (Access, PersonToken, error) {
	r, err := db.New(s.r).PersonTokenByHash(ctx, hash(token))
	if errors.Is(err, sql.ErrNoRows) {
		return Access{}, PersonToken{}, ErrNotFound
	}
	if err != nil {
		return Access{}, PersonToken{}, err
	}
	ms := at.UnixMilli()
	if r.ExpiresAt <= ms {
		return Access{}, PersonToken{}, ErrNotFound
	}
	if ms-r.UsedAt >= SessionTouch.Milliseconds() {
		end := min(at.Add(SessionIdle).UnixMilli(), r.CreatedAt+SessionMax.Milliseconds())
		if err := db.New(s.w).TouchPersonToken(ctx, db.TouchPersonTokenParams{UsedAt: ms, ExpiresAt: end, ID: r.ID}); err != nil {
			return Access{}, PersonToken{}, err
		}
		r.UsedAt, r.ExpiresAt = ms, end
	}
	a, err := s.AccessOfPerson(ctx, r.WorkspaceID, r.PersonID)
	if err != nil {
		return Access{}, PersonToken{}, err
	}
	a.Actor.Via = r.Name
	if r.Scope != ScopeManage {
		a = a.ReadOnly()
	}
	return a, PersonToken{
		ID: r.ID, WorkspaceID: r.WorkspaceID, PersonID: r.PersonID, Name: r.Name, Scope: r.Scope,
		Created: time.UnixMilli(r.CreatedAt).UTC(), LastUsed: time.UnixMilli(r.UsedAt).UTC(), Expires: time.UnixMilli(r.ExpiresAt).UTC(),
	}, nil
}

// PersonTokens returns a person's sign-ins that have not ended, newest first.
func (s *Store) PersonTokens(ctx context.Context, workspaceID, personID string, at time.Time) ([]PersonToken, error) {
	rows, err := db.New(s.r).PersonTokensOfPerson(ctx, db.PersonTokensOfPersonParams{WorkspaceID: workspaceID, PersonID: personID, At: at.UnixMilli()})
	if err != nil {
		return nil, err
	}
	tokens := make([]PersonToken, len(rows))
	for i, r := range rows {
		tokens[i] = PersonToken{
			ID: r.ID, WorkspaceID: workspaceID, PersonID: personID, Name: r.Name, Scope: r.Scope,
			Created: time.UnixMilli(r.CreatedAt).UTC(), LastUsed: time.UnixMilli(r.UsedAt).UTC(), Expires: time.UnixMilli(r.ExpiresAt).UTC(),
		}
	}
	return tokens, nil
}

// BrowserSession is a signed-in browser, without its token.
type BrowserSession struct {
	ID        string
	UserAgent string
	Created   time.Time
	LastUsed  time.Time
}

// BrowserSessions returns a person's browser sessions that have not ended, the one
// last used first.
func (s *Store) BrowserSessions(ctx context.Context, workspaceID, personID string, at time.Time) ([]BrowserSession, error) {
	rows, err := db.New(s.r).SessionsOfPerson(ctx, db.SessionsOfPersonParams{WorkspaceID: workspaceID, PersonID: personID, At: at.UnixMilli()})
	if err != nil {
		return nil, err
	}
	sessions := make([]BrowserSession, len(rows))
	for i, r := range rows {
		sessions[i] = BrowserSession{ID: r.ID, UserAgent: r.UserAgent, Created: time.UnixMilli(r.CreatedAt).UTC(), LastUsed: time.UnixMilli(r.SeenAt).UTC()}
	}
	return sessions, nil
}

// EndPersonToken ends one of a person's sign-ins, which stops working at once. One
// of someone else gives ErrNotFound, as one that does not exist does.
func (s *Store) EndPersonToken(ctx context.Context, by Actor, workspaceID, personID, tokenID string, at time.Time) error {
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	q := db.New(tx)
	t, err := q.PersonTokenOfPerson(ctx, db.PersonTokenOfPersonParams{WorkspaceID: workspaceID, PersonID: personID, ID: tokenID})
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	who, err := personActor(ctx, q, workspaceID, personID)
	if err != nil {
		return err
	}
	if err := endToken(ctx, q, by, who, workspaceID, t.ID, t.Name, t.Scope, at); err != nil {
		return err
	}
	return tx.Commit()
}

func endToken(ctx context.Context, q *db.Queries, by, who Actor, workspaceID, tokenID, name, scope string, at time.Time) error {
	if _, err := q.DeletePersonToken(ctx, tokenID); err != nil {
		return err
	}
	return writeLog(ctx, q, logEntry{
		WorkspaceID: workspaceID, At: at, Kind: "token.ended", AboutKind: "sign-in", AboutID: tokenID, AboutName: name,
		By: by, Person: who, Details: map[string]any{"scope": scope},
	})
}

func logSessionEnded(ctx context.Context, q *db.Queries, by, who Actor, workspaceID, sessionID string, at time.Time) error {
	return writeLog(ctx, q, logEntry{
		WorkspaceID: workspaceID, At: at, Kind: "session.ended", AboutKind: "session", AboutID: sessionID, By: by, Person: who,
	})
}

// EndBrowserSession ends a browser session of a person from elsewhere, which stops
// working at once. One of someone else gives ErrNotFound. A browser signing itself
// out is EndSession, which the log leaves out.
func (s *Store) EndBrowserSession(ctx context.Context, by Actor, workspaceID, personID, sessionID string, at time.Time) error {
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	q := db.New(tx)
	n, err := q.DeleteSessionOfPerson(ctx, db.DeleteSessionOfPersonParams{WorkspaceID: workspaceID, PersonID: personID, ID: sessionID})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	who, err := personActor(ctx, q, workspaceID, personID)
	if err != nil {
		return err
	}
	if err := logSessionEnded(ctx, q, by, who, workspaceID, sessionID, at); err != nil {
		return err
	}
	return tx.Commit()
}

// EndEverySignIn ends all of a person's sign-ins and browser sessions, and says how
// many of each. Their passkeys stay, so they can sign in again: that is RemovePasskey.
func (s *Store) EndEverySignIn(ctx context.Context, by Actor, workspaceID, personID string, at time.Time) (tokens, sessions int, err error) {
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()
	q := db.New(tx)
	ts, err := q.PersonTokensOfPerson(ctx, db.PersonTokensOfPersonParams{WorkspaceID: workspaceID, PersonID: personID, At: at.UnixMilli()})
	if err != nil {
		return 0, 0, err
	}
	ss, err := q.SessionsOfPerson(ctx, db.SessionsOfPersonParams{WorkspaceID: workspaceID, PersonID: personID, At: at.UnixMilli()})
	if err != nil {
		return 0, 0, err
	}
	who, err := personActor(ctx, q, workspaceID, personID)
	if err != nil {
		return 0, 0, err
	}
	for _, t := range ts {
		if err := endToken(ctx, q, by, who, workspaceID, t.ID, t.Name, t.Scope, at); err != nil {
			return 0, 0, err
		}
	}
	for _, r := range ss {
		if _, err := q.DeleteSessionOfPerson(ctx, db.DeleteSessionOfPersonParams{WorkspaceID: workspaceID, PersonID: personID, ID: r.ID}); err != nil {
			return 0, 0, err
		}
		if err := logSessionEnded(ctx, q, by, who, workspaceID, r.ID, at); err != nil {
			return 0, 0, err
		}
	}
	return len(ts), len(ss), tx.Commit()
}

// RemovePasskey removes a passkey and ends what was approved or opened with it, and
// says how many sign-ins and sessions that was. One of someone else gives ErrNotFound.
func (s *Store) RemovePasskey(ctx context.Context, by Actor, workspaceID, personID, passkeyID string, at time.Time) (tokens, sessions int, err error) {
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()
	q := db.New(tx)
	n, err := q.PasskeyExists(ctx, db.PasskeyExistsParams{WorkspaceID: workspaceID, PersonID: personID, ID: passkeyID})
	if err != nil {
		return 0, 0, err
	}
	if n == 0 {
		return 0, 0, ErrNotFound
	}
	ts, err := q.PersonTokensOfPasskey(ctx, db.PersonTokensOfPasskeyParams{WorkspaceID: workspaceID, PersonID: personID, PasskeyID: passkeyID, At: at.UnixMilli()})
	if err != nil {
		return 0, 0, err
	}
	ss, err := q.SessionIDsOfPasskey(ctx, db.SessionIDsOfPasskeyParams{WorkspaceID: workspaceID, PersonID: personID, PasskeyID: passkeyID, At: at.UnixMilli()})
	if err != nil {
		return 0, 0, err
	}
	who, err := personActor(ctx, q, workspaceID, personID)
	if err != nil {
		return 0, 0, err
	}
	for _, t := range ts {
		if err := endToken(ctx, q, by, who, workspaceID, t.ID, t.Name, t.Scope, at); err != nil {
			return 0, 0, err
		}
	}
	for _, sessionID := range ss {
		if err := logSessionEnded(ctx, q, by, who, workspaceID, sessionID, at); err != nil {
			return 0, 0, err
		}
	}
	if _, err := q.DeletePasskey(ctx, db.DeletePasskeyParams{WorkspaceID: workspaceID, PersonID: personID, ID: passkeyID}); err != nil {
		return 0, 0, err
	}
	if err := writeLog(ctx, q, logEntry{
		WorkspaceID: workspaceID, At: at, Kind: "passkey.removed", AboutKind: "person", AboutID: personID, AboutName: who.Name, By: by, Person: who,
	}); err != nil {
		return 0, 0, err
	}
	return len(ts), len(ss), tx.Commit()
}

// ReadOnly is what a credential with the scope read may do with an access: read as
// a viewer in every project it can see, also for an administrator (R5).
func (a Access) ReadOnly() Access {
	a.Admin = false
	a.Projects = slices.Clone(a.Projects)
	for i := range a.Projects {
		a.Projects[i].Role = Viewer
	}
	return a
}
