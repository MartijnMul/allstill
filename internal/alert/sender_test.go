// SPDX-License-Identifier: AGPL-3.0-only

package alert

import (
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MartijnMul/allstill/internal/check"
	"github.com/MartijnMul/allstill/internal/store"
)

// world is one installation with an administrator in Amsterdam, who has the
// default quiet hours (22:00 to 07:00) and gets alerts for every project.
type world struct {
	t      *testing.T
	path   string
	st     *store.Store
	ws     string
	admin  string
	access store.Access
	sender *Sender
	now    time.Time
}

func newWorld(t *testing.T) *world {
	t.Helper()
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "allstill.db")
	st, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ws, _ := st.Workspace(ctx)
	admin, err := st.CreatePerson(ctx, store.Command("test"), ws, store.NewPerson{Name: "Ada", TimeZone: "Europe/Amsterdam", WorkspaceAdmin: true})
	if err != nil {
		t.Fatal(err)
	}
	// The last digest was at noon on the 21st, whatever the real clock says:
	// the next one is due at 08:00 on the 22nd.
	if err := st.RecordDigest(ctx, admin, time.Date(2026, 10, 21, 12, 0, 0, 0, ams), nil, nil); err != nil {
		t.Fatal(err)
	}
	a, _ := st.AdminAccess(ctx, ws, "test")
	w := &world{t: t, path: path, st: st, ws: ws, admin: admin, access: a}
	w.sender = &Sender{Store: st, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: func() time.Time { return w.now }}
	return w
}

func (w *world) check(project, name string) string {
	w.t.Helper()
	ctx := w.t.Context()
	p, err := w.st.ProjectByName(ctx, w.ws, project)
	if err != nil {
		p, _, err = w.st.CreateProject(ctx, store.Command("test"), w.ws, project)
		if err != nil {
			w.t.Fatal(err)
		}
		w.access, _ = w.st.AdminAccess(ctx, w.ws, "test")
	}
	c, err := w.st.CreateCheck(ctx, w.access, p, store.NewCheck{Name: name, Interval: 24 * time.Hour, Grace: time.Hour})
	if err != nil {
		w.t.Fatal(err)
	}
	return c.PingUUID
}

// at pings a check at a time in Amsterdam, then lets the sender work.
func (w *world) at(hhmm string, uuid string, kind check.Kind) {
	w.t.Helper()
	w.set(hhmm)
	if uuid != "" {
		_, err := w.st.Ping(w.t.Context(), store.PingIn{CheckUUID: uuid, Kind: kind, ExitStatus: -1, Method: "POST", At: w.now})
		if err != nil {
			w.t.Fatal(err)
		}
	}
	if err := w.sender.Pass(w.t.Context()); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) set(hhmm string) {
	w.t.Helper()
	day, clock, ok := strings.Cut(hhmm, " ")
	if !ok {
		day, clock = "21", hhmm
	}
	when, err := time.ParseInLocation("2006-01-02 15:04", "2026-10-"+day+" "+clock, ams)
	if err != nil {
		w.t.Fatal(err)
	}
	w.now = when
}

// messages lists what the administrator was sent, oldest first, as
// "what title" lines per message.
func (w *world) messages() []string {
	w.t.Helper()
	msgs, err := w.st.Messages(w.t.Context(), w.admin, 100)
	if err != nil {
		w.t.Fatal(err)
	}
	var out []string
	for i := len(msgs) - 1; i >= 0; i-- {
		var lines []string
		for _, it := range msgs[i].Items {
			lines = append(lines, it.What+" "+it.Title)
		}
		out = append(out, msgs[i].Kind+" "+msgs[i].State+": "+strings.Join(lines, ", "))
	}
	return out
}

func (w *world) want(want ...string) {
	w.t.Helper()
	got := w.messages()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		w.t.Fatalf("messages:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

func TestADownByDayAndItsRecovery(t *testing.T) {
	w := newWorld(t)
	backup := w.check("Shop", "backup")
	w.at("12:00", backup, check.Fail)
	w.want("alert shadow: down backup is down")
	w.at("12:30", backup, check.Success)
	w.at("12:39", "", "") // recovering
	w.want("alert shadow: down backup is down")
	w.at("12:41", "", "") // settled
	w.want("alert shadow: down backup is down", "alert shadow: recovered backup is down")
}

func TestADownAtNightThatRecoversIsNeverSent(t *testing.T) {
	w := newWorld(t)
	backup := w.check("Shop", "backup")
	w.at("22 04:00", backup, check.Fail)
	w.at("22 05:00", backup, check.Success)
	w.at("22 05:11", "", "")
	w.at("22 07:00", "", "")
	w.want()
	// The digest says it happened.
	w.at("22 08:00", "", "")
	w.want("digest shadow: resolved_unseen backup is down")
}

func TestADownAtNightIsSentInTheMorning(t *testing.T) {
	w := newWorld(t)
	backup := w.check("Shop", "backup")
	w.at("22 04:00", backup, check.Fail)
	w.at("22 06:55", backup, check.Success)
	w.at("22 07:00", backup, check.Fail) // down again before it settled: still the same incident
	w.want("alert shadow: down backup is down")
	w.at("22 08:00", "", "")
	w.want("alert shadow: down backup is down", "digest shadow: still_open backup is down")
}

func TestAFlapSendsTwoMessages(t *testing.T) {
	w := newWorld(t)
	web := w.check("Shop", "web")
	w.at("13:00", web, check.Fail)
	w.at("13:03", web, check.Success)
	w.at("13:06", web, check.Fail)
	w.at("13:09", web, check.Success)
	w.at("13:15", "", "")
	w.at("13:20", "", "")
	w.want("alert shadow: down web is down", "alert shadow: recovered web is down")
}

func TestABurstIsGathered(t *testing.T) {
	w := newWorld(t)
	a, b, c := w.check("Shop", "a"), w.check("Lab", "b"), w.check("Lab", "c")
	w.at("14:00", a, check.Fail)
	w.at("14:01", b, check.Fail)
	w.at("14:02", c, check.Fail)
	w.want("alert shadow: down a is down")
	w.at("14:03", "", "")
	w.want("alert shadow: down a is down", "alert shadow: down b is down, down c is down")
}

func TestOutOfShadowThereIsNoChannelYet(t *testing.T) {
	w := newWorld(t)
	backup := w.check("Shop", "backup")
	if err := w.st.SetSetting(t.Context(), "alerts", "on"); err != nil {
		t.Fatal(err)
	}
	w.at("12:00", backup, check.Fail)
	w.at("12:05", "", "") // not again before the retry
	w.want("alert failed: down backup is down")
}

func TestACheckMovedDuringAnIncident(t *testing.T) {
	w := newWorld(t)
	ctx := t.Context()
	// A colleague follows only the customer's project.
	colleague, _ := w.st.CreatePerson(ctx, store.Command("test"), w.ws, store.NewPerson{Name: "Bo", TimeZone: "Europe/Amsterdam"})
	printer := w.check("Imported", "printer")
	customer, _, _ := w.st.CreateProject(ctx, store.Command("test"), w.ws, "Customer")
	w.st.SetMember(ctx, store.Command("test"), w.ws, customer, colleague, store.Editor)
	w.at("12:00", printer, check.Fail)

	views, _ := w.st.Checks(ctx, w.access)
	if err := w.st.MoveCheck(ctx, store.Command("test"), w.ws, views[0].ID, customer, w.now); err != nil {
		t.Fatal(err)
	}
	w.at("12:01", "", "")
	msgs, _ := w.st.Messages(ctx, colleague, 10)
	if len(msgs) != 1 || msgs[0].Items[0].What != "down" {
		t.Fatalf("the colleague was sent %+v", msgs)
	}
}

// Case 9 of the spec: urgency lowered while an alert waits for the morning.
func TestASettingChangedWhileWaitingIsFollowed(t *testing.T) {
	w := newWorld(t)
	backup := w.check("Shop", "backup")
	w.at("22 03:00", backup, check.Fail)
	views, _ := w.st.Checks(t.Context(), w.access)
	if err := w.st.SetAlerting(t.Context(), store.Command("test"), w.ws, views[0].ID, "digest", false, w.now); err != nil {
		t.Fatal(err)
	}
	w.at("22 07:00", "", "")
	w.want()
	w.at("22 08:00", "", "")
	w.want("digest shadow: down backup is down")
}

// From none to now during an incident: the people who follow it are told.
func TestFromNoneToNowDuringAnIncident(t *testing.T) {
	w := newWorld(t)
	laptop := w.check("Lab", "laptop")
	views, _ := w.st.Checks(t.Context(), w.access)
	w.st.SetAlerting(t.Context(), store.Command("test"), w.ws, views[0].ID, "none", false, w.now)
	w.at("12:00", laptop, check.Fail)
	w.want()
	w.st.SetAlerting(t.Context(), store.Command("test"), w.ws, views[0].ID, "now", false, w.now)
	w.at("12:01", "", "")
	w.want("alert shadow: down laptop is down")
}

// An alert and the digest are not sent within the gather period of each other.
func TestTheDigestKeepsItsDistance(t *testing.T) {
	w := newWorld(t)
	backup := w.check("Shop", "backup")
	w.at("22 07:59", backup, check.Fail) // by day, so at once
	w.at("22 08:00", "", "")
	w.want("alert shadow: down backup is down")
	w.at("22 08:01", "", "")
	w.want("alert shadow: down backup is down")
	w.at("22 08:02", "", "")
	w.want("alert shadow: down backup is down", "digest shadow: still_open backup is down")
}

// Allstill was down when the incident settled. It resolves when its settle
// period ended, before the digest time, and the digest still lists it; the
// down that waited for the morning is never sent.
func TestASettleWhileAllstillWasDownReachesTheDigest(t *testing.T) {
	w := newWorld(t)
	backup := w.check("Shop", "backup")
	w.at("22 06:00", backup, check.Fail) // quiet hours: waits until 07:00
	w.at("22 06:50", backup, check.Success)
	// Down from 06:55 until 09:00.
	w.at("22 09:00", "", "")
	w.want("digest shadow: resolved_unseen backup is down")
}

// probeOf is a probe with a check on it, in a project the world's person follows.
func probeOf(t *testing.T, w *world, critical bool) (store.Probe, string) {
	t.Helper()
	ctx := t.Context()
	w.check("Sites", "other")
	project, err := w.st.ProjectByName(ctx, w.ws, "Sites")
	if err != nil {
		t.Fatal(err)
	}
	_, key, err := w.st.CreateProbe(ctx, w.access, "inside", project, nil)
	if err != nil {
		t.Fatal(err)
	}
	p, err := w.st.ProbeByKey(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	c, err := w.st.CreateCheck(ctx, w.access, project, store.NewCheck{Name: "intranet", Kind: "http", Interval: time.Minute, ProbeID: p.ID,
		Settings: map[string]any{"url": "http://10.0.0.5/", "fail_after": 1}})
	if err != nil {
		t.Fatal(err)
	}
	if critical {
		if err := w.st.SetAlerting(ctx, store.Command("test"), w.ws, c.ID, "now", true, w.now); err != nil {
			t.Fatal(err)
		}
	}
	return p, c.ID
}

// A probe that was cut off reports an outage that ended hours ago. It is untrue
// to say it is down, so it wakes nobody, and the digest says it happened.
func TestAnOutageOverWhenHeardSendsNoAlert(t *testing.T) {
	for _, critical := range []bool{false, true} {
		w := newWorld(t)
		p, id := probeOf(t, w, critical)
		post := func(runs ...store.ProbeRun) {
			t.Helper()
			if _, err := w.st.ApplyProbeRuns(t.Context(), p, runs, w.now, w.now); err != nil {
				t.Fatal(err)
			}
			if err := w.sender.Pass(t.Context()); err != nil {
				t.Fatal(err)
			}
		}
		run := func(rid string, o check.Outcome, ago time.Duration) store.ProbeRun {
			return store.ProbeRun{ID: rid, CheckID: id, Outcome: o, Reason: "HTTP 503", EndedAgo: ago}
		}
		w.set("10:00")
		post(run("up", check.Passed, 0))
		w.set("22 03:00")
		post(run("a", check.Failed, 6*time.Hour), run("b", check.Passed, 5*time.Hour))
		w.want()
		// Still none while it settles, and no "recovered" of what nobody heard.
		w.set("22 03:30")
		post()
		w.want()
		w.set("22 08:05")
		post()
		got := w.messages()
		if len(got) == 0 || !strings.Contains(strings.Join(got, "\n"), "resolved_unseen intranet is down") {
			t.Errorf("critical %v: the digest says %q", critical, got)
		}
	}
}

// Held back while it seemed over, it is told when it turns out not to be.
func TestAnOutageOverWhenHeardThatReturnsIsTold(t *testing.T) {
	w := newWorld(t)
	p, id := probeOf(t, w, false)
	post := func(runs ...store.ProbeRun) {
		t.Helper()
		if _, err := w.st.ApplyProbeRuns(t.Context(), p, runs, w.now, w.now); err != nil {
			t.Fatal(err)
		}
		if err := w.sender.Pass(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	run := func(rid string, o check.Outcome, ago time.Duration) store.ProbeRun {
		return store.ProbeRun{ID: rid, CheckID: id, Outcome: o, Reason: "HTTP 503", EndedAgo: ago}
	}
	w.set("10:00")
	post(run("up", check.Passed, 0))
	w.set("12:00")
	post(run("a", check.Failed, time.Hour), run("b", check.Passed, 30*time.Minute))
	w.want()
	w.set("12:02")
	post(run("c", check.Failed, 0))
	w.want("alert shadow: down intranet is down")
}
