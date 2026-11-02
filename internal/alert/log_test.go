// SPDX-License-Identifier: AGPL-3.0-only

package alert

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/MartijnMul/allstill/internal/check"
	"github.com/MartijnMul/allstill/internal/email"
	"github.com/MartijnMul/allstill/internal/store"
)

// reachLines reads what the log says of messages, oldest first, as
// "kind about what how channel", with "(hidden)" for what the list leaves out.
func (w *world) reachLines() []string {
	w.t.Helper()
	conn, err := sql.Open("sqlite", w.path)
	if err != nil {
		w.t.Fatal(err)
	}
	defer conn.Close()
	rows, err := conn.QueryContext(w.t.Context(), `SELECT kind, about_name, person_name, hidden, details FROM log_entries
		WHERE kind LIKE 'message.%' ORDER BY id`)
	if err != nil {
		w.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var kind, about, person, details string
		var hidden int
		if err := rows.Scan(&kind, &about, &person, &hidden, &details); err != nil {
			w.t.Fatal(err)
		}
		var d map[string]any
		if err := json.Unmarshal([]byte(details), &d); err != nil {
			w.t.Fatal(err)
		}
		line := strings.TrimPrefix(kind, "message.") + " " + person
		for _, part := range []any{about, d["what"], d["how"], d["channel"], d["count"], d["why"]} {
			if part != nil && part != "" {
				line += " " + fmt.Sprint(part)
			}
		}
		if hidden == 1 {
			line += " (hidden)"
		}
		out = append(out, line)
	}
	return out
}

func (w *world) wantReach(want ...string) {
	w.t.Helper()
	got := w.reachLines()
	if !slices.Equal(got, want) {
		w.t.Fatalf("the log says:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

func (w *world) pause(name string) {
	w.t.Helper()
	ctx := w.t.Context()
	checks, err := w.st.Checks(ctx, w.access)
	if err != nil {
		w.t.Fatal(err)
	}
	i := slices.IndexFunc(checks, func(c store.CheckView) bool { return c.Name == name })
	if err := w.st.PauseCheck(ctx, w.access, checks[i].ID, w.now); err != nil {
		w.t.Fatal(err)
	}
}

// A message is written when it arrives, once, with how it went (R3).
func TestAMessageIsWrittenWhenItArrives(t *testing.T) {
	t.Run("at once", func(t *testing.T) {
		w := newWorld(t)
		alertsOn(w)
		d := newDevice(t)
		backup := w.check("Shop", "backup")
		w.set("11:00")
		w.confirm(d, w.withPush(d, ""))
		w.wantReach() // a test message is no part of it
		w.at("12:00", backup, check.Fail)
		w.wantReach("reached Ada backup down now webpush")
		w.at("12:30", backup, check.Success)
		w.at("12:41", "", "")
		w.wantReach("reached Ada backup down now webpush", "reached Ada backup recovered now webpush")
	})
	t.Run("after a retry", func(t *testing.T) {
		w := newWorld(t)
		alertsOn(w)
		d := newDevice(t)
		backup := w.check("Shop", "backup")
		w.set("11:00")
		w.confirm(d, w.withPush(d, ""))
		d.answer(http.StatusServiceUnavailable)
		w.at("12:00", backup, check.Fail)
		w.wantReach() // queued and tried, but it has not arrived
		d.answer(http.StatusCreated)
		w.at("12:01", "", "")
		w.wantReach("reached Ada backup down now webpush")
	})
	t.Run("on two devices, once", func(t *testing.T) {
		w := newWorld(t)
		alertsOn(w)
		phone, laptop := newDevice(t), newDevice(t)
		backup := w.check("Shop", "backup")
		w.set("11:00")
		w.confirm(phone, w.withPush(phone, ""))
		k, _ := w.st.VAPIDKey(t.Context(), w.ws)
		c, err := w.st.SavePush(t.Context(), w.ws, w.admin, "Laptop", laptop.subscription(), k.PublicKey())
		if err != nil {
			t.Fatal(err)
		}
		w.sender.Push.Client = trusting(phone, laptop)
		w.confirm(laptop, c.ID)
		w.at("12:00", backup, check.Fail)
		w.wantReach("reached Ada backup down now webpush")
	})
	t.Run("by email when the push fails", func(t *testing.T) {
		w := newWorld(t)
		alertsOn(w)
		d, m := newDevice(t), &mailer{}
		backup := w.check("Shop", "backup")
		w.set("11:00")
		w.confirm(d, w.withPush(d, ""))
		w.withEmail(m)
		d.answer(http.StatusForbidden)
		w.at("12:00", backup, check.Fail)
		w.at("12:00", "", "")
		w.wantReach("reached Ada backup down now email")
	})
	t.Run("holding only what it still said", func(t *testing.T) {
		w := newWorld(t)
		alertsOn(w)
		d := newDevice(t)
		backup, web := w.check("Shop", "backup"), w.check("Shop", "web")
		w.set("11:00")
		w.confirm(d, w.withPush(d, ""))
		d.answer(http.StatusServiceUnavailable)
		w.set("12:00")
		for _, uuid := range []string{backup, web} {
			if _, err := w.st.Ping(t.Context(), store.PingIn{CheckUUID: uuid, Kind: check.Fail, ExitStatus: -1, Method: "POST", At: w.now}); err != nil {
				t.Fatal(err)
			}
		}
		w.at("12:00", "", "")
		w.pause("backup")
		d.answer(http.StatusCreated)
		w.at("12:01", "", "")
		w.wantReach("reached Ada web down now webpush")
	})
}

// One digest can say two things of one check, which is still one check.
func TestTwoLinesOfOneCheckAreNotSeveralChecks(t *testing.T) {
	w := newWorld(t)
	alertsOn(w)
	m := &mailer{}
	backup := w.check("Shop", "backup")
	w.set("11:00")
	w.withEmail(m)
	w.at("12:00", backup, check.Fail)
	w.set("12:05")
	w.pause("backup")
	w.at("22 08:00", "", "")
	w.wantReach(
		"reached Ada backup down now email",
		"reached Ada backup closed_paused digest email",
		"reached Ada backup paused_check digest email",
	)
}

// A digest says what it says of incidents too, each with its check (spec case 15), and
// with nowhere to go it says who could not be reached.
func TestADigestAboutIncidents(t *testing.T) {
	t.Run("reached", func(t *testing.T) {
		w := newWorld(t)
		alertsOn(w)
		m := &mailer{}
		backup, web := w.check("Shop", "backup"), w.check("Shop", "web")
		w.set("11:00")
		w.withEmail(m)
		w.at("12:00", backup, check.Fail)
		w.at("12:04", web, check.Success)
		w.at("22 08:00", "", "")
		w.wantReach(
			"reached Ada backup down now email",
			"reached Ada backup still_open digest email",
		)
	})
	t.Run("nowhere to go", func(t *testing.T) {
		w := newWorld(t)
		alertsOn(w)
		backup, web := w.check("Shop", "backup"), w.check("Shop", "web")
		w.at("12:00", backup, check.Success)
		ctx := t.Context()
		checks, err := w.st.Checks(ctx, w.access)
		if err != nil {
			t.Fatal(err)
		}
		i := slices.IndexFunc(checks, func(c store.CheckView) bool { return c.Name == "web" })
		if err := w.st.SetAlerting(ctx, store.Command("test"), w.ws, checks[i].ID, "digest", false, w.now); err != nil {
			t.Fatal(err)
		}
		w.at("12:10", web, check.Fail)
		w.wantReach()
		w.at("22 08:00", "", "")
		w.wantReach("unreached Ada web the person has no way to be reached")
	})
	t.Run("falling back", func(t *testing.T) {
		w := newWorld(t)
		alertsOn(w)
		d, m := newDevice(t), &mailer{}
		backup := w.check("Shop", "backup")
		w.set("11:00")
		w.confirm(d, w.withPush(d, ""))
		w.withEmail(m)
		w.at("12:00", backup, check.Fail)
		m.outcome = email.Rejected
		w.at("22 08:00", "", "")
		w.at("22 08:00", "", "")
		w.wantReach("reached Ada backup down now webpush", "reached Ada backup still_open digest webpush")
	})
}

// A message one device got and another did not is one line that it arrived, and
// shadow mode coming on before the second try does not take that back (R4).
func TestAMessageOneDeviceGot(t *testing.T) {
	w := newWorld(t)
	alertsOn(w)
	phone, laptop := newDevice(t), newDevice(t)
	backup := w.check("Shop", "backup")
	w.set("11:00")
	w.confirm(phone, w.withPush(phone, ""))
	k, _ := w.st.VAPIDKey(t.Context(), w.ws)
	c, err := w.st.SavePush(t.Context(), w.ws, w.admin, "Laptop", laptop.subscription(), k.PublicKey())
	if err != nil {
		t.Fatal(err)
	}
	w.sender.Push.Client = trusting(phone, laptop)
	w.confirm(laptop, c.ID)
	laptop.answer(http.StatusServiceUnavailable)
	w.at("12:00", backup, check.Fail)
	w.wantReach("reached Ada backup down now webpush")
	w.sender.Shadow = true
	w.at("12:01", "", "")
	w.wantReach("reached Ada backup down now webpush")
}

// A message that was taken back says nothing was sent, and nobody was unreachable
// (R3, R5; spec case 12).
func TestAMessageNoLongerTrueIsNotWritten(t *testing.T) {
	w := newWorld(t)
	alertsOn(w)
	d := newDevice(t)
	backup := w.check("Shop", "backup")
	w.set("11:00")
	w.confirm(d, w.withPush(d, ""))
	d.answer(http.StatusServiceUnavailable)
	w.at("12:00", backup, check.Fail)
	w.pause("backup")
	d.answer(http.StatusCreated)
	w.at("12:01", "", "")
	w.wantReach()
}

// In shadow mode a line says it was recorded, not sent (R4).
func TestShadowModeSaysSo(t *testing.T) {
	t.Run("from the start", func(t *testing.T) {
		w := newWorld(t)
		backup := w.check("Shop", "backup")
		w.at("12:00", backup, check.Fail)
		w.wantReach("shadow Ada backup down now")
	})
	t.Run("when its time came", func(t *testing.T) {
		w := newWorld(t)
		alertsOn(w)
		d := newDevice(t)
		backup := w.check("Shop", "backup")
		w.set("11:00")
		w.confirm(d, w.withPush(d, ""))
		d.answer(http.StatusServiceUnavailable)
		w.at("12:00", backup, check.Fail)
		d.answer(http.StatusCreated)
		w.sender.Shadow = true
		w.at("12:01", "", "")
		w.wantReach("shadow Ada backup down now")
	})
}

// Someone who cannot be reached is one line per incident, not six an hour, and a
// second incident the same day has its own (R5).
func TestSomeoneWhoCannotBeReached(t *testing.T) {
	t.Run("with no channel", func(t *testing.T) {
		w := newWorld(t)
		alertsOn(w)
		backup, web := w.check("Shop", "backup"), w.check("Shop", "web")
		w.at("12:00", backup, check.Fail)
		const noWay = "the person has no way to be reached"
		w.wantReach("unreached Ada backup " + noWay)
		w.at("12:10", "", "")
		w.at("12:20", "", "")
		w.wantReach("unreached Ada backup " + noWay)
		// No failed message is recorded for the rest of the day, yet this is another
		// incident.
		w.at("12:30", web, check.Fail)
		w.wantReach("unreached Ada backup "+noWay, "unreached Ada web "+noWay)
	})
	t.Run("with a device that always fails", func(t *testing.T) {
		w := newWorld(t)
		alertsOn(w)
		d := newDevice(t)
		backup := w.check("Shop", "backup")
		w.set("11:00")
		w.confirm(d, w.withPush(d, ""))
		d.answer(http.StatusServiceUnavailable)
		w.at("12:00", backup, check.Fail)
		w.at("12:01", "", "")
		const refused = "the push service refused it"
		w.wantReach("unreached Ada backup " + refused)
		for _, at := range []string{"12:12", "12:13", "12:24", "12:25"} {
			w.at(at, "", "")
		}
		w.wantReach("unreached Ada backup " + refused)
		// Reached at last, it says so.
		d.answer(http.StatusCreated)
		w.at("12:36", "", "")
		w.wantReach("unreached Ada backup "+refused, "reached Ada backup down now webpush")
	})
	t.Run("with an email address refused", func(t *testing.T) {
		w := newWorld(t)
		alertsOn(w)
		m := &mailer{}
		backup := w.check("Shop", "backup")
		w.set("11:00")
		w.withEmail(m)
		m.outcome = email.Rejected
		w.at("12:00", backup, check.Fail)
		w.wantReach("unreached Ada backup the mail server did not take it")
	})
}

// A digest is one line per project that opens to its checks, naming them without
// copying the titles, which can hold a reason (R3, R19).
func TestADigestIsOneLinePerProject(t *testing.T) {
	w := newWorld(t)
	alertsOn(w)
	m := &mailer{}
	w.set("11:00")
	w.withEmail(m)
	const marker = "sekret-marker"
	ctx := t.Context()
	w.check("Shop", "backup")
	w.check("Shop", "web")
	w.check("Other", "alone")
	p, err := w.st.ProjectByName(ctx, w.ws, "Shop")
	if err != nil {
		t.Fatal(err)
	}
	c, err := w.st.CreateCheck(ctx, w.access, p, store.NewCheck{Name: "site", Kind: "http", Interval: time.Minute, Settings: map[string]any{"url": "https://shop.example.com/"}})
	if err != nil {
		t.Fatal(err)
	}
	w.set("12:00")
	if _, err := w.st.RecordAttempt(ctx, w.ws, c.ID, store.Attempt{Attempt: check.Attempt{Outcome: check.Warned, Reason: "certificate expires in " + marker, At: w.now}}); err != nil {
		t.Fatal(err)
	}
	w.at("22 08:00", "", "")
	if !strings.Contains(m.sent[len(m.sent)-1].Body, marker) {
		t.Fatal("the digest does not hold the reason, so this tests nothing")
	}
	got := w.reachLines()
	slices.Sort(got)
	var lines string
	for _, l := range got {
		lines += l + "\n"
	}
	for _, line := range []string{
		"reached Ada alone silent_check digest email\n",
		"reached Ada backup silent_check digest email (hidden)\n",
		"reached Ada web silent_check digest email (hidden)\n",
		"reached Ada site warning digest email (hidden)\n",
		"reached_several Ada digest email 3\n",
	} {
		if !strings.Contains(lines, line) {
			t.Errorf("no %q in\n%s", line, lines)
		}
	}
	if len(got) != 5 {
		t.Errorf("%d lines:\n%s", len(got), lines)
	}
	if strings.Contains(lines, marker) {
		t.Errorf("the reason reached the log:\n%s", lines)
	}
}
