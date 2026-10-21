// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/MartijnMul/allstill/internal/store/internal/db"
)

// Retention says how much history each check keeps: its newest Runs runs,
// with their pings, and the output of its newest Output runs. Output is what
// fills the disk: a run may carry up to 100 kB of it. Pings outside any run
// follow the same two numbers.
type Retention struct {
	Runs   int
	Output int
}

// DefaultRetention keeps about three years of a nightly backup, and the
// output of its last hundred nights.
var DefaultRetention = Retention{Runs: 1000, Output: 100}

// Check says whether a retention keeps enough for the dashboard's charts.
func (r Retention) Check() error {
	switch {
	case r.Runs < RecentRuns:
		return fmt.Errorf("keep at least %d runs, for the charts", RecentRuns)
	case r.Output < 0 || r.Output > r.Runs:
		return fmt.Errorf("keep the output of 0 to %d runs, no more runs than are kept", r.Runs)
	}
	return nil
}

// Keeps returns the store's Retention, or DefaultRetention when it has none.
func (s *Store) Keeps() Retention {
	if s.Retention == (Retention{}) {
		return DefaultRetention
	}
	return s.Retention
}

// EventLifetime is how long the live stream's events are kept. The stream is
// still to come; a client away longer than this will reload everything
// instead of catching up.
const EventLifetime = 7 * 24 * time.Hour

const (
	tidyChecks = 20  // checks looked at per call
	tidyBatch  = 500 // rows changed per statement, and so per write lock
)

// Tidy removes history beyond the store's Retention, for some checks at a
// time, from where the last call stopped. It returns true while a round over
// all checks is not finished; at the end of a round it also removes old
// events. Each statement is its own short transaction, so pings never wait
// long for the write lock. A check that cannot be tidied does not stop the
// others; its error is returned with the rest of the call's work done.
func (s *Store) Tidy(ctx context.Context, now time.Time) (more bool, err error) {
	s.tidy.Lock()
	defer s.tidy.Unlock()
	r := s.Keeps()
	w, rd := db.New(s.w), db.New(s.r)
	ids, err := rd.ChecksAfter(ctx, db.ChecksAfterParams{ID: s.tidyAfter, Limit: tidyChecks})
	if err != nil {
		return false, err
	}
	var errs []error
	for _, checkID := range ids {
		if err := tidyCheck(ctx, w, rd, checkID, r); err != nil {
			if ctx.Err() != nil {
				return false, ctx.Err()
			}
			errs = append(errs, fmt.Errorf("tidying check %s: %w", checkID, err))
		}
		s.tidyAfter = checkID
	}
	if len(ids) == tidyChecks {
		return true, errors.Join(errs...)
	}
	s.tidyAfter = ""
	for {
		n, err := w.DeleteEventsBefore(ctx, db.DeleteEventsBeforeParams{Before: now.Add(-EventLifetime).UnixMilli(), Batch: tidyBatch})
		if err != nil || n < tidyBatch {
			return false, errors.Join(append(errs, err)...)
		}
	}
}

// batches repeats a batched statement until it changes fewer rows than a batch.
func batches(do func() (int64, error)) error {
	for {
		n, err := do()
		if err != nil || n < tidyBatch {
			return err
		}
	}
}

// tidyCheck reads with rd and writes with w, so the single writer is never
// held for a read.
func tidyCheck(ctx context.Context, w, rd *db.Queries, checkID string, r Retention) error {
	// The newest run, and loose ping, past each limit; nothing past it is
	// sql.ErrNoRows, and there is nothing to do.
	pastRun := func(keep int) (string, error) {
		return rd.RunPastRetention(ctx, db.RunPastRetentionParams{CheckID: checkID, Offset: int64(keep)})
	}
	pastLoose := func(keep int) (int64, error) {
		return rd.LoosePingPastRetention(ctx, db.LoosePingPastRetentionParams{CheckID: checkID, Offset: int64(keep)})
	}
	if last, err := pastRun(r.Runs); err == nil {
		if err := batches(func() (int64, error) {
			return w.DeleteRunsUpTo(ctx, db.DeleteRunsUpToParams{OfCheck: checkID, UpTo: last, Batch: tidyBatch})
		}); err != nil {
			return err
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if last, err := pastRun(r.Output); err == nil {
		if err := batches(func() (int64, error) {
			return w.DropOutputUpTo(ctx, db.DropOutputUpToParams{OfCheck: checkID, UpTo: last, Batch: tidyBatch})
		}); err != nil {
			return err
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if last, err := pastLoose(r.Runs); err == nil {
		if err := batches(func() (int64, error) {
			return w.DeleteLoosePingsUpTo(ctx, db.DeleteLoosePingsUpToParams{OfCheck: checkID, UpTo: last, Batch: tidyBatch})
		}); err != nil {
			return err
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if last, err := pastLoose(r.Output); err == nil {
		return batches(func() (int64, error) {
			return w.DropLooseOutputUpTo(ctx, db.DropLooseOutputUpToParams{OfCheck: checkID, UpTo: last, Batch: tidyBatch})
		})
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	return nil
}
