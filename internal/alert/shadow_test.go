// SPDX-License-Identifier: AGPL-3.0-only

package alert

import (
	"strings"
	"testing"

	"github.com/MartijnMul/allstill/internal/check"
	"github.com/MartijnMul/allstill/internal/store"
)

// Switching alerts on (R10): what began in shadow mode stays quiet, and the
// digest lists it; what begins after is alerted.
func TestSwitchingAlertsOn(t *testing.T) {
	w := newWorld(t)
	d := newDevice(t)
	backup, web := w.check("Shop", "backup"), w.check("Shop", "web")
	w.set("21 11:00")
	w.confirm(d, w.withPush(d, ""))
	// In shadow mode, at night: its down waits for 07:00.
	w.at("22 03:00", backup, check.Fail)
	w.set("22 05:00")
	quiet, err := w.st.SetAlerts(t.Context(), true)
	if err != nil || quiet != 1 {
		t.Fatalf("%d quiet, %v", quiet, err)
	}
	w.at("22 07:00", "", "") // nothing goes out about it
	w.at("22 08:00", "", "") // the digest lists it
	w.at("22 09:00", web, check.Fail)
	if got := strings.Join(d.titles(), "|"); got != "Allstill test message|Allstill digest|web is down" {
		t.Errorf("pushed %s", got)
	}
	w.want("test sent: ", "digest sent: still_open backup is down, silent_check Shop: web", "alert sent: down web is down")
	// Its recovery is not sent either, and the next digest says so.
	w.at("22 10:00", backup, check.Success)
	w.at("22 10:20", "", "")
	w.at("23 08:00", "", "")
	msgs, _ := w.st.Messages(t.Context(), w.admin, 1)
	if len(msgs) != 1 || msgs[0].Kind != "digest" || !strings.Contains(itemsOf(msgs[0].Items), "resolved_unseen backup is down") {
		t.Errorf("the next digest: %+v", msgs)
	}
}

func itemsOf(items []store.Item) string {
	var out []string
	for _, it := range items {
		out = append(out, it.What+" "+it.Title)
	}
	return strings.Join(out, ", ")
}

// A message that finds no channel is tried again, but recorded as failed
// once a day.
func TestNoChannelIsRecordedOnceADay(t *testing.T) {
	w := newWorld(t)
	alertsOn(w)
	backup := w.check("Shop", "backup")
	for _, at := range []string{"12:00", "12:10", "12:20", "13:00"} {
		w.at(at, "", "")
		if at == "12:00" {
			w.at(at, backup, check.Fail)
		}
	}
	w.want("alert failed: down backup is down")
	// A day later it is recorded once more.
	w.at("22 12:30", "", "")
	msgs, _ := w.st.Messages(t.Context(), w.admin, 10)
	failed := 0
	for _, m := range msgs {
		if m.Kind == "alert" && m.State == "failed" && m.Error == store.NoChannel {
			failed++
		}
	}
	if failed != 2 {
		t.Errorf("%d failures recorded over two days, want 2", failed)
	}
}

// Switching on again, or back and on, leaves what people really heard as it
// was: its recovery still comes.
func TestSwitchingOnAgainKeepsWhatWasHeard(t *testing.T) {
	for _, steps := range [][]bool{{true}, {false, true}} {
		w := newWorld(t)
		d := newDevice(t)
		backup := w.check("Shop", "backup")
		w.set("21 11:00")
		w.confirm(d, w.withPush(d, ""))
		alertsOn(w)
		w.at("12:00", backup, check.Fail) // really pushed
		for _, on := range steps {
			if _, err := w.st.SetAlerts(t.Context(), on); err != nil {
				t.Fatal(err)
			}
		}
		w.at("12:30", backup, check.Success)
		w.at("12:41", "", "")
		if got := strings.Join(d.titles(), "|"); got != "Allstill test message|backup is down|Resolved: backup is down" {
			t.Errorf("after switching %v: pushed %s", steps, got)
		}
	}
}

// Someone who follows an incident from shadow mode only later is not
// alerted for it either.
func TestALaterFollowerOfAShadowIncident(t *testing.T) {
	w := newWorld(t)
	backup := w.check("Shop", "backup")
	w.at("12:00", backup, check.Fail) // in shadow mode
	if _, err := w.st.SetAlerts(t.Context(), true); err != nil {
		t.Fatal(err)
	}
	bo, _ := w.st.CreatePerson(t.Context(), w.ws, store.NewPerson{Name: "Bo", TimeZone: "Europe/Amsterdam"})
	lab, _, _ := w.st.CreateProject(t.Context(), w.ws, "Lab")
	w.st.SetMember(t.Context(), w.ws, lab, bo, store.Editor)
	views, _ := w.st.Checks(t.Context(), w.access)
	w.set("12:10")
	if err := w.st.MoveCheck(t.Context(), w.ws, views[0].ID, lab, w.now); err != nil {
		t.Fatal(err)
	}
	w.at("12:10", "", "")
	if msgs, _ := w.st.Messages(t.Context(), bo, 10); len(msgs) != 0 {
		t.Errorf("Bo was told: %+v", msgs)
	}
}
