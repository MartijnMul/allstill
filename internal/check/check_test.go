// SPDX-License-Identifier: AGPL-3.0-only

package check

import (
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
