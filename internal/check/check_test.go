// SPDX-License-Identifier: AGPL-3.0-only

package check

import (
	"errors"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 21, 3, 0, 0, 0, time.UTC)

func at(d time.Duration) time.Time { return t0.Add(d) }

// hourly is a check pinged every hour with ten minutes' grace.
var hourly = Config{Schedule: Interval(time.Hour), Grace: 10 * time.Minute}

// world holds a check's status and open runs, as the store would.
type world struct {
	t    *testing.T
	c    Config
	s    Status
	open []OpenRun
	n    int
}

func newWorld(t *testing.T, c Config) *world {
	return &world{t: t, c: c, s: Status{State: New, Since: t0}}
}

func (w *world) ping(k Kind, rid string, d time.Duration) RunAction {
	w.t.Helper()
	s, act, err := OnPing(w.c, w.s, w.open, Ping{Kind: k, ExitStatus: -1, RID: rid, At: at(d)})
	if err != nil {
		w.t.Fatal(err)
	}
	w.s = s
	switch act.Op {
	case OpenNew:
		w.n++
		w.open = append(w.open, OpenRun{ID: string(rune('a' + w.n - 1)), RID: rid, Started: at(d)})
	case Restart:
		for i := range w.open {
			if w.open[i].ID == act.RunID {
				w.open[i].Started = at(d)
			}
		}
	case Finish:
		w.open = without(w.open, act.RunID)
	}
	for _, id := range act.LetGo {
		for i := range w.open {
			if w.open[i].ID == id {
				w.open[i].TimedOut = true
			}
		}
	}
	return act
}

// tick runs the scheduler at d, as often as deadlines have passed.
func (w *world) tick(d time.Duration) []string {
	var all []string
	for range 10 {
		if w.s.Deadline.IsZero() || at(d).Before(w.s.Deadline) {
			break
		}
		s, timedOut := OnDeadline(w.c, w.s, w.open, at(d))
		for _, id := range timedOut {
			for i := range w.open {
				if w.open[i].ID == id {
					w.open[i].TimedOut = true
				}
			}
		}
		w.s, all = s, append(all, timedOut...)
	}
	return all
}

func (w *world) want(st State, deadline time.Duration) {
	w.t.Helper()
	if w.s.State != st {
		w.t.Fatalf("state %s (%s), want %s", w.s.State, w.s.Reason, st)
	}
	if deadline < 0 {
		if !w.s.Deadline.IsZero() {
			w.t.Fatalf("deadline %s, want none", w.s.Deadline.Sub(t0))
		}
	} else if !w.s.Deadline.Equal(at(deadline)) {
		w.t.Fatalf("deadline %s, want %s", w.s.Deadline.Sub(t0), deadline)
	}
}

const none = -1

func is(act RunAction, op RunOp, runID string) bool { return act.Op == op && act.RunID == runID }

func TestLateThenDownThenRecovered(t *testing.T) {
	w := newWorld(t, hourly)
	w.want(New, none)
	w.tick(10 * time.Hour)
	w.want(New, none) // a check never pinged is never late

	w.ping(Success, "", 0)
	w.want(Up, time.Hour)
	w.tick(59 * time.Minute)
	w.want(Up, time.Hour)
	w.tick(time.Hour)
	w.want(Late, 70*time.Minute)
	w.tick(70 * time.Minute)
	w.want(Down, none)
	if w.s.Since != at(70*time.Minute) {
		t.Fatalf("down since %s", w.s.Since.Sub(t0))
	}
	w.tick(48 * time.Hour)
	w.want(Down, none) // down stays down

	w.ping(Success, "", 3*time.Hour)
	w.want(Up, 4*time.Hour)
}

func TestSchedulerRunningBehindGoesStraightToDown(t *testing.T) {
	w := newWorld(t, hourly)
	w.ping(Success, "", 0)
	w.tick(5 * time.Hour)
	w.want(Down, none)
	if w.s.Since != at(5*time.Hour) {
		t.Fatalf("since %s", w.s.Since.Sub(t0))
	}
}

func TestFailures(t *testing.T) {
	w := newWorld(t, hourly)
	w.ping(Fail, "", 0)
	w.want(Down, none)
	if w.s.Reason != "The job reported a failure." {
		t.Fatalf("reason %q", w.s.Reason)
	}
	s, _, _ := OnPing(hourly, w.s, nil, Ping{Kind: Fail, ExitStatus: 3, At: at(time.Minute)})
	if s.Reason != "The job failed with exit status 3." || s.Since != t0 {
		t.Fatalf("%+v", s)
	}
}

// A nightly backup through `allstill run`: start and end share a rid.
func TestRunsPairByRid(t *testing.T) {
	w := newWorld(t, hourly)
	w.ping(Success, "", 0)
	if act := w.ping(Start, "r1", time.Hour); act.Op != OpenNew {
		t.Fatalf("start: %+v", act)
	}
	w.want(Up, 70*time.Minute) // the run times out before the check goes late
	if act := w.ping(Start, "r2", 61*time.Minute); act.Op != OpenNew {
		t.Fatalf("an overlapping start: %+v", act)
	}
	if act := w.ping(Log, "r1", 62*time.Minute); !is(act, AttachLog, "a") {
		t.Fatalf("log: %+v", act)
	}
	if act := w.ping(Success, "r1", 65*time.Minute); !is(act, Finish, "a") {
		t.Fatalf("end of r1: %+v", act)
	}
	w.want(Up, 71*time.Minute) // r2 still open
	if act := w.ping(Success, "r2", 66*time.Minute); !is(act, Finish, "b") {
		t.Fatalf("end of r2: %+v", act)
	}
	w.want(Up, 126*time.Minute)
	if act := w.ping(Success, "r9", 67*time.Minute); act.Op != EndOnly {
		t.Fatalf("an end without its start: %+v", act)
	}
	if act := w.ping(Log, "r9", 68*time.Minute); act.Op != NoRun {
		t.Fatalf("a log without a run: %+v", act)
	}
}

func TestAStartOvertakenByItsEndJoinsThatRun(t *testing.T) {
	w := newWorld(t, hourly)
	if act := w.ping(Success, "r1", 0); act.Op != EndOnly {
		t.Fatalf("the end that arrived first: %+v", act)
	}
	s, act, err := OnPing(w.c, w.s, w.open, Ping{Kind: Start, ExitStatus: -1, RID: "r1", At: at(time.Second), Ended: "z"})
	if err != nil {
		t.Fatal(err)
	}
	if !is(act, LateStart, "z") || s != w.s {
		t.Fatalf("the late start: %+v, status %+v", act, s)
	}
	// Without a rid it joins all the same, when the store found a run for it.
	if _, act, _ := OnPing(w.c, w.s, w.open, Ping{Kind: Start, ExitStatus: -1, At: at(time.Second), Ended: "z"}); !is(act, LateStart, "z") {
		t.Fatalf("a start without rid: %+v", act)
	}
	if _, act, _ := OnPing(w.c, w.s, w.open, Ping{Kind: Start, ExitStatus: -1, At: at(time.Second)}); act.Op != OpenNew {
		t.Fatalf("a start with no run to join: %+v", act)
	}
}

func TestARepeatedStartRestartsTheRun(t *testing.T) {
	w := newWorld(t, hourly)
	w.ping(Success, "", 0)
	w.ping(Start, "r1", time.Hour)
	if act := w.ping(Start, "r1", 65*time.Minute); !is(act, Restart, "a") {
		t.Fatalf("%+v", act)
	}
	w.want(Up, 75*time.Minute)
	w.ping(Start, "", 66*time.Minute)
	if act := w.ping(Start, "", 67*time.Minute); !is(act, Restart, "b") {
		t.Fatalf("a repeated start without rid: %+v", act)
	}
}

func TestATimedOutRunStillEnds(t *testing.T) {
	w := newWorld(t, hourly)
	w.ping(Success, "", 0)
	w.ping(Start, "r1", time.Hour)
	if got := w.tick(70 * time.Minute); len(got) != 1 || got[0] != "a" {
		t.Fatalf("timed out %v", got)
	}
	w.want(Down, none)
	if w.s.Reason != "A run started but did not end within the grace period of 10 min." {
		t.Fatalf("reason %q", w.s.Reason)
	}
	if act := w.ping(Success, "r1", 3*time.Hour); !is(act, Finish, "a") {
		t.Fatalf("the late end: %+v", act)
	}
	w.want(Up, 4*time.Hour)
}

func TestNewAndPausedChecksDoNotTimeOutRuns(t *testing.T) {
	for _, st := range []State{New, Paused} {
		w := newWorld(t, hourly)
		w.s.State = st
		w.ping(Start, "", 0)
		w.want(st, none)
		w.tick(5 * time.Hour)
		w.want(st, none)
		w.ping(Success, "", 6*time.Hour)
		w.want(Up, 7*time.Hour) // a success resumes a paused check
	}
}

func TestIgnoredAndLogPingsChangeNothing(t *testing.T) {
	w := newWorld(t, hourly)
	w.ping(Success, "", 0)
	before := w.s
	w.ping(Ignored, "", time.Minute)
	w.ping(Log, "", 2*time.Minute)
	if w.s != before {
		t.Fatalf("%+v, want %+v", w.s, before)
	}
}

func TestRestartAfterDowntime(t *testing.T) {
	w := newWorld(t, hourly)
	w.ping(Success, "", 0)
	// The server stopped at 0:30 and came back at 3:00, past the deadline.
	s := OnRestart(hourly, w.s, at(30*time.Minute), at(3*time.Hour))
	if s.State != Unknown || !s.Deadline.Equal(at(3*time.Hour+10*time.Minute)) {
		t.Fatalf("%+v", s)
	}
	w.s = s
	w.tick(3*time.Hour + 10*time.Minute)
	w.want(Down, none)
	if w.s.Reason != "No ping since Allstill restarted." {
		t.Fatalf("reason %q", w.s.Reason)
	}

	// A ping during the restart's grace makes it up again.
	w = newWorld(t, hourly)
	w.ping(Success, "", 0)
	w.s = OnRestart(hourly, w.s, at(30*time.Minute), at(3*time.Hour))
	w.ping(Success, "", 3*time.Hour+time.Minute)
	w.want(Up, 4*time.Hour+time.Minute)

	// A deadline before the server stopped, or still ahead, is not touched.
	w = newWorld(t, hourly)
	w.ping(Success, "", 0)
	if s := OnRestart(hourly, w.s, at(2*time.Hour), at(3*time.Hour)); s != w.s {
		t.Fatalf("a deadline the server saw pass: %+v", s)
	}
	if s := OnRestart(hourly, w.s, at(10*time.Minute), at(30*time.Minute)); s != w.s {
		t.Fatalf("a deadline still ahead: %+v", s)
	}
}

func TestDoesNotChangeTheCallersRuns(t *testing.T) {
	open := make([]OpenRun, 1, 4)
	open[0] = OpenRun{ID: "a", Started: t0}
	s := Status{State: Up, NextExpected: at(time.Hour), Deadline: at(10 * time.Minute)}
	OnPing(hourly, s, open, Ping{Kind: Start, ExitStatus: -1, At: at(time.Minute)})
	OnDeadline(hourly, s, open, at(10*time.Minute))
	if open[0].TimedOut || open[:2][1] != (OpenRun{}) {
		t.Fatalf("the caller's runs changed: %+v", open[:2])
	}
}

func TestHuman(t *testing.T) {
	for d, want := range map[time.Duration]string{
		0: "0 s", 90 * time.Second: "1 min 30 s", time.Hour: "1 h", 26*time.Hour + 5*time.Minute: "26 h 5 min",
	} {
		if got := human(d); got != want {
			t.Errorf("human(%s) = %q, want %q", d, got, want)
		}
	}
}

func TestARunningJobIsNotLate(t *testing.T) {
	w := newWorld(t, hourly)
	w.ping(Success, "", 0)
	w.ping(Start, "r1", 55*time.Minute)
	w.tick(64 * time.Minute)
	w.want(Up, 65*time.Minute) // past the expected time, but running
	w.ping(Success, "r1", 64*time.Minute)
	w.want(Up, 124*time.Minute)

	w.ping(Start, "r2", 2*time.Hour)
	w.tick(2*time.Hour + 10*time.Minute)
	w.want(Down, none)
}

// A run that began while the check did not count never brings it down later.
func TestARunLeftOpenBeforeTheCheckCountedIsLetGo(t *testing.T) {
	w := newWorld(t, hourly)
	w.ping(Start, "a", 0) // new; it never ends
	w.ping(Start, "b", 24*time.Hour)
	act := w.ping(Success, "b", 24*time.Hour+time.Minute)
	if len(act.LetGo) != 1 || act.LetGo[0] != "a" {
		t.Fatalf("let go %v", act.LetGo)
	}
	w.want(Up, 25*time.Hour+time.Minute)
	w.tick(25*time.Hour + time.Minute)
	if w.s.State != Late {
		t.Fatalf("state %s (%s)", w.s.State, w.s.Reason)
	}
	if act := w.ping(Success, "a", 26*time.Hour); act.Op != Finish || act.RunID != "a" {
		t.Fatalf("the late end of a: %+v", act)
	}
}

func TestARunStillInItsGraceKeepsCounting(t *testing.T) {
	w := newWorld(t, hourly)
	w.s.State = Down
	w.ping(Start, "a", 0)
	act := w.ping(Success, "b", 5*time.Minute)
	if len(act.LetGo) != 0 {
		t.Fatalf("let go %v", act.LetGo)
	}
	w.want(Up, 10*time.Minute) // a times out at the end of its grace
	w.tick(10 * time.Minute)
	w.want(Down, none)
}

// An HTTP or TCP check every minute, down after 2 failures in a row.
var minutely = Config{Schedule: Interval(time.Minute), Grace: 2 * time.Minute, Active: true, FailAfter: 2}

func TestOnAttempt(t *testing.T) {
	t0 := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	at := func(m int) time.Time { return t0.Add(time.Duration(m) * time.Minute) }
	attempt := func(s Status, o Outcome, m int) Status {
		t.Helper()
		s, err := OnAttempt(minutely, s, Attempt{Outcome: o, Reason: "HTTP 503", At: at(m)})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	s := attempt(Status{State: New, Since: t0}, Passed, 0)
	if s.State != Up || s.FailStreak != 0 || !s.Deadline.Equal(at(3)) {
		t.Fatalf("a pass: %+v", s)
	}
	s = attempt(s, Failed, 1)
	if s.State != Up || s.FailStreak != 1 || !s.Since.Equal(t0) {
		t.Fatalf("one failure stays up: %+v", s)
	}
	s = attempt(s, Failed, 2)
	if s.State != Down || s.Reason != "HTTP 503" || !s.Since.Equal(at(2)) {
		t.Fatalf("the second failure: %+v", s)
	}
	// Allstill's own network drops: unknown at once, no deadline, the
	// failures kept, so the first failure after is down again.
	s = attempt(s, Unjudged, 3)
	if s.State != Unknown || !s.Deadline.IsZero() || s.FailStreak != 2 {
		t.Fatalf("unjudged: %+v", s)
	}
	if s = attempt(s, Failed, 4); s.State != Down {
		t.Fatalf("a failure after an unjudged attempt: %+v", s)
	}
	s = attempt(s, Warned, 5)
	if s.State != Warning || s.FailStreak != 0 {
		t.Fatalf("a warning: %+v", s)
	}
	if s = attempt(s, Passed, 6); s.State != Up || s.Reason != "" {
		t.Fatalf("a pass after a warning: %+v", s)
	}
	// A new check that fails once stays new.
	if s := attempt(Status{State: New, Since: t0}, Failed, 0); s.State != New || s.FailStreak != 1 {
		t.Fatalf("a new check's first failure: %+v", s)
	}
	// A paused check is not judged.
	if s := attempt(Status{State: Paused, Since: t0}, Failed, 0); s.State != Paused || s.FailStreak != 0 {
		t.Fatalf("paused: %+v", s)
	}
	if _, err := OnAttempt(hourly, Status{State: Up}, Attempt{Outcome: Passed, At: t0}); err == nil {
		t.Error("an attempt for a ping check was taken")
	}
}

// An active check is never late: a missed deadline, or a restart, makes it
// unknown without a deadline, and a ping to it is refused.
func TestAnActiveCheckIsNeverLate(t *testing.T) {
	t0 := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	s, _ := OnAttempt(minutely, Status{State: New, Since: t0}, Attempt{Outcome: Passed, At: t0})
	for _, now := range []time.Time{t0.Add(time.Minute), t0.Add(2 * time.Minute)} {
		if got, _ := OnDeadline(minutely, s, nil, now); got.State != Up {
			t.Errorf("at %v, before its deadline: %s", now, got.State)
		}
	}
	got, _ := OnDeadline(minutely, s, nil, s.Deadline)
	if got.State != Unknown || !got.Deadline.IsZero() {
		t.Errorf("past its deadline: %+v", got)
	}
	// Down, and Allstill not running when its deadline passed: unknown, not
	// down, and no grace period that would make it down again.
	down := Status{State: Down, Since: t0, Deadline: t0.Add(3 * time.Minute), FailStreak: 2}
	got = OnRestart(minutely, down, t0, t0.Add(time.Hour))
	if got.State != Unknown || !got.Deadline.IsZero() {
		t.Errorf("after a restart: %+v", got)
	}
	if again, _ := OnDeadline(minutely, got, nil, t0.Add(2*time.Hour)); again.State != Unknown {
		t.Errorf("an hour after the restart: %+v", again)
	}
	// A ping check after a restart still has its grace period.
	if got := OnRestart(hourly, Status{State: Up, Deadline: t0}, t0.Add(-time.Minute), t0.Add(time.Hour)); got.State != Unknown || got.Deadline.IsZero() {
		t.Errorf("a ping check after a restart: %+v", got)
	}
	if _, _, err := OnPing(minutely, s, nil, Ping{Kind: Success, ExitStatus: -1, At: t0}); !errors.Is(err, ErrActive) {
		t.Errorf("a ping to an active check: %v", err)
	}
}

func activeConfig() Config {
	return Config{Schedule: Interval(time.Minute), Grace: 2 * time.Minute, Active: true, FailAfter: 2}
}

// R35 of intent/manage-checks/spec.md: a check that has never passed opens no
// incident, so a typo costs nobody their sleep. It keeps saying why.
func TestAnActiveCheckThatHasNeverPassed(t *testing.T) {
	c := activeConfig()
	s := Status{State: New}
	var err error
	failed := Attempt{Outcome: Failed, Reason: "the host name does not exist", At: at(0)}
	for i := range 5 {
		failed.At = at(time.Duration(i) * time.Minute)
		if s, err = OnAttempt(c, s, failed); err != nil {
			t.Fatal(err)
		}
		if s.State != New || s.Reason != "the host name does not exist" {
			t.Fatalf("after %d failures: %s, %q", i+1, s.State, s.Reason)
		}
	}
	// Unknown does not change that: it has still never worked.
	s, _ = OnAttempt(c, s, Attempt{Outcome: Unjudged, Reason: "not judged", At: at(6 * time.Minute)})
	if s.State != Unknown {
		t.Fatalf("not judged: %s", s.State)
	}
	s, _ = OnAttempt(c, s, Attempt{Outcome: Failed, Reason: "HTTP 503", At: at(7 * time.Minute)})
	if s.State != New || s.Reason != "HTTP 503" {
		t.Fatalf("after unknown: %s, %q", s.State, s.Reason)
	}
	// Once it has passed, it behaves as any check, also after a resume.
	s, _ = OnAttempt(c, s, Attempt{Outcome: Passed, At: at(8 * time.Minute)})
	if s.State != Up || !s.EverPassed || s.Reason != "" {
		t.Fatalf("passed: %+v", s)
	}
	s = OnPause(s, at(9*time.Minute))
	s = OnResume(s, at(10*time.Minute))
	if s.State != New || !s.EverPassed {
		t.Fatalf("resumed: %+v", s)
	}
	s, _ = OnAttempt(c, s, Attempt{Outcome: Failed, Reason: "HTTP 503", At: at(11 * time.Minute)})
	s, _ = OnAttempt(c, s, Attempt{Outcome: Failed, Reason: "HTTP 503", At: at(12 * time.Minute)})
	if s.State != Down {
		t.Fatalf("a check that worked before, after two failures: %s", s.State)
	}
}

func TestPauseAndResume(t *testing.T) {
	s := Status{State: Down, Reason: "HTTP 503", Since: at(0), LastPing: at(0), NextExpected: at(time.Hour), Deadline: at(2 * time.Hour), FailStreak: 3, EverPassed: true}
	p := OnPause(s, at(time.Minute))
	if p.State != Paused || p.Reason != "" || !p.Deadline.IsZero() || p.FailStreak != 0 || !p.Since.Equal(at(time.Minute)) || !p.LastPing.Equal(at(0)) {
		t.Fatalf("paused: %+v", p)
	}
	if again := OnPause(p, at(2*time.Minute)); !again.Since.Equal(at(time.Minute)) {
		t.Errorf("paused twice: %+v", again)
	}
	r := OnResume(p, at(3*time.Minute))
	if r.State != New || !r.Deadline.IsZero() || !r.NextExpected.IsZero() || !r.Since.Equal(at(3*time.Minute)) || !r.EverPassed {
		t.Fatalf("resumed: %+v", r)
	}
	// Only a paused check resumes.
	up := Status{State: Up, Since: at(0)}
	if got := OnResume(up, at(time.Minute)); got != up {
		t.Errorf("resuming one that is not paused: %+v", got)
	}
}

// The table of intent/manage-checks/spec.md: a change of schedule or grace never
// itself moves a check to late or down.
func TestAScheduleChange(t *testing.T) {
	now := at(10 * time.Hour)
	hourly := Config{Schedule: Interval(time.Hour), Grace: 30 * time.Minute}
	next := now.Add(time.Hour)
	for _, c := range []struct {
		name   string
		s      Status
		state  State
		reason string
		next   time.Time
		dl     time.Time
	}{
		{name: "new", s: Status{State: New}, state: New},
		{name: "paused", s: Status{State: Paused, LastPing: at(0)}, state: Paused},
		{name: "up", s: Status{State: Up, LastPing: at(9 * time.Hour), NextExpected: at(10 * time.Hour), Deadline: at(10 * time.Hour)}, state: Up, next: next, dl: next},
		{name: "warning", s: Status{State: Warning, Reason: "low", LastPing: at(9 * time.Hour), Deadline: at(10 * time.Hour)}, state: Warning, reason: "low", next: next, dl: next},
		{name: "late", s: Status{State: Late, Reason: "The ping is overdue.", LastPing: at(8 * time.Hour), NextExpected: at(9 * time.Hour), Deadline: at(10 * time.Hour)}, state: Up, reason: ScheduleChanged, next: next, dl: next},
		{name: "down", s: Status{State: Down, Reason: "No ping.", LastPing: at(5 * time.Hour), NextExpected: at(6 * time.Hour)}, state: Down, reason: "No ping.", next: at(6 * time.Hour)},
		{name: "unknown after a restart", s: Status{State: Unknown, Reason: "Allstill was not running.", LastPing: at(8 * time.Hour), Deadline: at(10 * time.Hour)}, state: Unknown, reason: "Allstill was not running.", dl: now.Add(30 * time.Minute)},
		{name: "unreadable, never pinged", s: Status{State: Unknown, Reason: "cannot read"}, state: New},
		{name: "unreadable, pinged", s: Status{State: Unknown, Reason: "cannot read", LastPing: at(8 * time.Hour)}, state: Up, reason: ScheduleChanged, next: next, dl: next},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, letGo, err := OnSchedule(hourly, c.s, nil, false, now)
			if err != nil {
				t.Fatal(err)
			}
			if got.State != c.state || got.Reason != c.reason || !got.NextExpected.Equal(c.next) || !got.Deadline.Equal(c.dl) || len(letGo) != 0 {
				t.Errorf("%+v, let go %v", got, letGo)
			}
			if got.State == Down && c.s.State != Down || got.State == Late {
				t.Errorf("the change itself made it %s", got.State)
			}
			// Nothing is due at once, so the scheduler has nothing to do.
			after, _ := OnDeadline(hourly, got, nil, now)
			if after.State != got.State {
				t.Errorf("at the same moment it goes %s", after.State)
			}
		})
	}
}

// A shorter grace must not time a run out at once, which would alert; a run within
// it goes on guarding the check.
func TestAScheduleChangeWithARunOpen(t *testing.T) {
	now := at(10 * time.Hour)
	c := Config{Schedule: Interval(time.Hour), Grace: 30 * time.Minute}
	s := Status{State: Up, LastPing: at(9 * time.Hour), NextExpected: at(10 * time.Hour), Deadline: at(10 * time.Hour)}
	open := []OpenRun{
		{ID: "old", Started: now.Add(-40 * time.Minute)},
		{ID: "recent", Started: now.Add(-10 * time.Minute)},
		{ID: "gone", Started: now.Add(-5 * time.Hour), TimedOut: true},
	}
	got, letGo, err := OnSchedule(c, s, open, false, now)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != Up || len(letGo) != 1 || letGo[0] != "old" {
		t.Fatalf("%+v, let go %v", got, letGo)
	}
	// With an incident open, a fixed schedule is no recovery.
	if got, _, _ := OnSchedule(c, Status{State: Unknown, Reason: "cannot read", LastPing: at(time.Hour)}, nil, true, now); got.State != Down || got.Reason != StillDown || !got.Deadline.IsZero() {
		t.Errorf("unreadable, with an incident: %+v", got)
	}
	// The run within the new grace still times out, at its own time.
	if want := now.Add(20 * time.Minute); !got.Deadline.Equal(want) {
		t.Errorf("deadline %v, want the recent run's %v", got.Deadline, want)
	}
	after, timedOut := OnDeadline(c, got, []OpenRun{{ID: "old", Started: open[0].Started, TimedOut: true}, open[1], open[2]}, got.Deadline)
	if after.State != Down || len(timedOut) != 1 || timedOut[0] != "recent" {
		t.Errorf("%+v, timed out %v", after, timedOut)
	}
}

func TestAScheduleChangeOfAnActiveCheck(t *testing.T) {
	now := at(10 * time.Hour)
	c := Config{Schedule: Interval(5 * time.Minute), Grace: 5 * time.Minute, Active: true, FailAfter: 2}
	for _, st := range []State{Up, Warning, Down} {
		got, letGo, err := OnSchedule(c, Status{State: st, Deadline: at(9 * time.Hour), FailStreak: 1}, nil, false, now)
		if err != nil {
			t.Fatal(err)
		}
		if got.State != st || !got.Deadline.Equal(now.Add(10*time.Minute)) || len(letGo) != 0 {
			t.Errorf("%s: %+v", st, got)
		}
	}
	// Unknown without a deadline waits for an attempt, however the schedule goes.
	if got, _, _ := OnSchedule(c, Status{State: Unknown}, nil, false, now); got.State != Unknown || !got.Deadline.IsZero() {
		t.Errorf("unknown: %+v", got)
	}
	if got, _, _ := OnSchedule(c, Status{State: New}, nil, false, now); got.State != New || !got.Deadline.IsZero() {
		t.Errorf("new: %+v", got)
	}
}
