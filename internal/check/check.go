// SPDX-License-Identifier: AGPL-3.0-only

// Package check decides what a ping or a passing deadline does to a check: its
// state, when the next ping is expected, which run a ping belongs to, and when
// the scheduler must look again. It holds no data and does no I/O.
//
// It follows Healthchecks, so checks behave the same after a move:
//
//   - A success or failure sets the check up or down and expects the next ping
//     one schedule step later. A check that is down stays down until a ping.
//   - Past the expected time a check is late; past the grace period after it,
//     down.
//   - A start opens a run. While a run is open the check is not late; a run
//     that does not end within the grace period times out and the check goes
//     down. When it ends later, its real run time is still recorded. New,
//     paused and down checks do not time out their runs; when the check counts
//     again, runs from such a time that are past their grace period are let
//     go without an alert, and the others count from then on.
//   - Any success or failure resumes a paused check.
//
// Allstill adds one state: when the server itself was not running at a
// deadline, the check becomes unknown instead of down, and gets one grace
// period from the restart to report. And a start that arrives after the end
// with the same rid joins that run, where Healthchecks would open a new run
// that times out.
//
// An active check, one Allstill runs itself (an HTTP or TCP check), is judged
// by its attempts instead of pings (intent/http-checks/spec.md): down after
// FailAfter failed attempts in a row, up on the first that passes. It is never
// late: when no attempt comes in time, or none could judge it, the one who
// runs it could not, so it is unknown, without a deadline, until an attempt
// judges it again.
package check

import (
	"errors"
	"fmt"
	"slices"
	"time"
)

// State is what the last ping or deadline decided.
type State string

const (
	New     State = "new"
	Up      State = "up"
	Warning State = "warning"
	Late    State = "late"
	Down    State = "down"
	Paused  State = "paused"
	Unknown State = "unknown"
)

// Schedule says when the next ping is expected after one at a given time.
type Schedule interface {
	Next(after time.Time) (time.Time, error)
}

// Interval is a schedule of a fixed period.
type Interval time.Duration

// Next returns after plus the interval.
func (i Interval) Next(after time.Time) (time.Time, error) { return after.Add(time.Duration(i)), nil }

// Config is what a check's settings say about timing.
type Config struct {
	Schedule Schedule
	Grace    time.Duration
	// Active is set for a check Allstill runs itself; FailAfter is how many
	// failed attempts in a row make it down (at least 1).
	Active    bool
	FailAfter int
}

// Status is the part of a check that pings and deadlines change. Zero times
// mean none.
type Status struct {
	State        State
	Since        time.Time
	Reason       string
	LastPing     time.Time
	NextExpected time.Time
	Deadline     time.Time
	// FailStreak is an active check's failed attempts in a row.
	FailStreak int
}

// OpenRun is a run that has started and not ended. A run that timed out stays
// open, waiting for its end.
type OpenRun struct {
	ID       string
	RID      string
	Started  time.Time
	TimedOut bool
}

// Kind is what a ping reports.
type Kind string

const (
	Start   Kind = "start"
	Success Kind = "success"
	Fail    Kind = "fail"
	Log     Kind = "log"
	Ignored Kind = "ignored"
)

// Ping is one received ping. ExitStatus is -1 when the ping carried none.
// Ended is, for a start with a rid, the id of a run with that rid that has an
// end and no start and ended within the grace period, if there is one.
type Ping struct {
	Kind       Kind
	ExitStatus int
	RID        string
	At         time.Time
	Ended      string
}

// RunOp is what a ping does to the runs.
type RunOp int

const (
	NoRun     RunOp = iota
	OpenNew         // a start: a new running run
	Restart         // a start with the rid of a run already open: it starts again
	Finish          // an end of an open run
	EndOnly         // an end without a start: a run of its end alone
	AttachLog       // a log ping that belongs to an open run
	LateStart       // a start whose run has ended: the end overtook it
)

// RunAction is what the store must do to the runs, and with which run.
// LetGo lists open runs that will not time out any more: they began while the
// check did not count, it counts again, and their grace period has passed.
// They stay open for a late end.
type RunAction struct {
	Op    RunOp
	RunID string
	LetGo []string
}

// ErrActive is the error for a ping to a check Allstill runs itself.
var ErrActive = errors.New("this check is run by Allstill; pings to it are not applied")

// OnPing applies a ping. It returns the new status and what happens to the
// runs. The status's deadline takes the open runs after the action into
// account.
func OnPing(c Config, s Status, open []OpenRun, p Ping) (Status, RunAction, error) {
	if c.Active {
		return s, RunAction{}, ErrActive
	}
	open = append([]OpenRun(nil), open...)
	switch p.Kind {
	case Ignored:
		return s, RunAction{}, nil
	case Log:
		if r, ok := match(open, p.RID); ok {
			return s, RunAction{Op: AttachLog, RunID: r.ID}, nil
		}
		return s, RunAction{}, nil
	case Start:
		act := RunAction{Op: OpenNew}
		if r, ok := match(open, p.RID); ok {
			act = RunAction{Op: Restart, RunID: r.ID}
			open = without(open, r.ID)
		} else if p.RID != "" && p.Ended != "" {
			return s, RunAction{Op: LateStart, RunID: p.Ended}, nil
		}
		open = append(open, OpenRun{RID: p.RID, Started: p.At})
		s.Deadline = deadline(c, s, open)
		return s, act, nil
	case Success, Fail:
	default:
		return s, RunAction{}, fmt.Errorf("unknown ping kind %q", p.Kind)
	}

	act := RunAction{Op: EndOnly}
	if r, ok := match(open, p.RID); ok {
		act = RunAction{Op: Finish, RunID: r.ID}
		open = without(open, r.ID)
	}
	next, err := c.Schedule.Next(p.At)
	if err != nil {
		return s, act, err
	}
	if !counts(s.State) {
		for i, r := range open {
			if !r.TimedOut && !p.At.Before(r.Started.Add(c.Grace)) {
				act.LetGo = append(act.LetGo, r.ID)
				open[i].TimedOut = true
			}
		}
	}
	s.LastPing, s.NextExpected = p.At, next
	if p.Kind == Success {
		s = become(s, Up, "", p.At)
	} else if p.ExitStatus > 0 {
		s = become(s, Down, fmt.Sprintf("The job failed with exit status %d.", p.ExitStatus), p.At)
	} else {
		s = become(s, Down, "The job reported a failure.", p.At)
	}
	s.Deadline = deadline(c, s, open)
	return s, act, nil
}

// Outcome is how an attempt of an active check went.
type Outcome string

const (
	Passed   Outcome = "passed"
	Warned   Outcome = "warned" // passed, with a warning, such as a certificate running out
	Failed   Outcome = "failed"
	Unjudged Outcome = "unjudged" // the one who ran it could not tell, such as when its own network is down
)

// Attempt is one attempt of an active check, ended at At.
type Attempt struct {
	Outcome Outcome
	Reason  string
	At      time.Time
}

// OnAttempt applies an attempt of an active check. A failed attempt counts
// towards FailAfter; below it, the check stays as it was. An unjudged one
// makes it unknown at once, and neither adds to the failures nor clears them,
// so a check that was down is down again on the first failure after.
func OnAttempt(c Config, s Status, a Attempt) (Status, error) {
	if !c.Active {
		return s, fmt.Errorf("an attempt for a check that is not run by Allstill")
	}
	if s.State == Paused {
		return s, nil
	}
	if a.Outcome == Unjudged {
		s = become(s, Unknown, a.Reason, a.At)
		s.Deadline = time.Time{}
		return s, nil
	}
	next, err := c.Schedule.Next(a.At)
	if err != nil {
		return s, err
	}
	s.LastPing, s.NextExpected = a.At, next
	switch a.Outcome {
	case Passed:
		s.FailStreak = 0
		s = become(s, Up, "", a.At)
	case Warned:
		s.FailStreak = 0
		s = become(s, Warning, a.Reason, a.At)
	case Failed:
		s.FailStreak++
		if s.FailStreak >= max(c.FailAfter, 1) {
			s = become(s, Down, a.Reason, a.At)
		}
	default:
		return s, fmt.Errorf("unknown attempt outcome %q", a.Outcome)
	}
	s.Deadline = next.Add(c.Grace)
	return s, nil
}

// OnDeadline applies the passing of the status's deadline at now. It returns
// the new status and the ids of runs that timed out.
func OnDeadline(c Config, s Status, open []OpenRun, now time.Time) (Status, []string) {
	if s.Deadline.IsZero() || now.Before(s.Deadline) {
		return s, nil
	}
	if c.Active {
		// No attempt came in time: whoever runs it could not.
		s = become(s, Unknown, "No attempt in time: the server or probe that runs it is behind or offline.", now)
		s.Deadline = time.Time{}
		return s, nil
	}
	open = append([]OpenRun(nil), open...)
	var timedOut []string
	if counts(s.State) {
		for i, r := range open {
			if !r.TimedOut && !now.Before(r.Started.Add(c.Grace)) {
				open[i].TimedOut = true
				timedOut = append(timedOut, r.ID)
			}
		}
	}
	running := slices.ContainsFunc(open, func(r OpenRun) bool { return !r.TimedOut })
	switch {
	case len(timedOut) > 0:
		s = become(s, Down, fmt.Sprintf("A run started but did not end within the grace period of %s.", human(c.Grace)), now)
	case s.State == Unknown:
		s = become(s, Down, "No ping since Allstill restarted.", now)
	case running && counts(s.State):
	case (s.State == Up || s.State == Warning) && !now.Before(s.NextExpected):
		s = become(s, Late, "The ping is overdue.", now)
	case s.State == Late && !now.Before(s.NextExpected.Add(c.Grace)):
		s = become(s, Down, fmt.Sprintf("No ping within the grace period of %s.", human(c.Grace)), now)
	}
	s.Deadline = deadline(c, s, open)
	return s, timedOut
}

// OnRestart is called for a check whose deadline passed while the server was
// not running: from lastAlive, the last moment the server knew it ran, until
// now. Such a check becomes unknown and has one grace period to report.
//
// An active check has no grace period to report in: it stays unknown, without
// a deadline, until its next attempt judges it.
func OnRestart(c Config, s Status, lastAlive, now time.Time) Status {
	if s.Deadline.IsZero() || !s.Deadline.After(lastAlive) || s.Deadline.After(now) {
		return s
	}
	if c.Active {
		s = become(s, Unknown, "Allstill was not running when an attempt was due.", now)
		s.Deadline = time.Time{}
		return s
	}
	s = become(s, Unknown, "Allstill was not running when a ping was due.", now)
	s.Deadline = now.Add(c.Grace)
	return s
}

// deadline is the next moment the scheduler must look at a check: when it
// goes late or down, or, while a run is open, when the run times out.
func deadline(c Config, s Status, open []OpenRun) time.Time {
	var d time.Time
	earliest := func(t time.Time) {
		if !t.IsZero() && (d.IsZero() || t.Before(d)) {
			d = t
		}
	}
	if counts(s.State) {
		for _, r := range open {
			if !r.TimedOut {
				earliest(r.Started.Add(c.Grace))
			}
		}
		if !d.IsZero() {
			return d
		}
	}
	switch s.State {
	case Up, Warning:
		earliest(s.NextExpected)
	case Late:
		earliest(s.NextExpected.Add(c.Grace))
	case Unknown:
		earliest(s.Deadline)
	}
	return d
}

// counts says whether a check in this state times out its runs.
func counts(st State) bool {
	return st == Up || st == Warning || st == Late || st == Unknown
}

func become(s Status, st State, reason string, at time.Time) Status {
	if s.State != st {
		s.Since = at
	}
	s.State, s.Reason = st, reason
	return s
}

// match finds the open run a ping belongs to: the one with its rid, or without
// a rid, the latest open run without one.
func match(open []OpenRun, rid string) (OpenRun, bool) {
	var found OpenRun
	ok := false
	for _, r := range open {
		if r.RID == rid && (!ok || r.Started.After(found.Started)) {
			found, ok = r, true
		}
	}
	return found, ok
}

func without(open []OpenRun, id string) []OpenRun {
	out := make([]OpenRun, 0, len(open))
	for _, r := range open {
		if r.ID != id {
			out = append(out, r)
		}
	}
	return out
}

// human writes a duration the way people say it: 90s is "1 min 30 s".
func human(d time.Duration) string {
	d = d.Round(time.Second)
	h, m, sec := int(d/time.Hour), int(d%time.Hour/time.Minute), int(d%time.Minute/time.Second)
	out := ""
	if h > 0 {
		out += fmt.Sprintf("%d h ", h)
	}
	if m > 0 {
		out += fmt.Sprintf("%d min ", m)
	}
	if sec > 0 || out == "" {
		out += fmt.Sprintf("%d s ", sec)
	}
	return out[:len(out)-1]
}
