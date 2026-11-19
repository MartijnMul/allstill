// SPDX-License-Identifier: AGPL-3.0-only

package alert

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/MartijnMul/allstill/internal/store"
)

// A notice about a command on the server goes out whatever the alerts switch
// says and whatever the person's quiet hours, and stays in when the server itself is told to
// send nothing, which is how a copy of a database is served (teams spec, R29).
func TestANoticeGoesOutInShadowModeAndQuietHours(t *testing.T) {
	w := newWorld(t)
	ctx := t.Context()
	d := newDevice(t)
	w.check("Shop", "backup")
	w.set("11:00")
	w.confirm(d, w.withPush(d, ""))
	project, err := w.st.ProjectByName(ctx, w.ws, "Shop")
	if err != nil {
		t.Fatal(err)
	}
	// In shadow mode, and with quiet hours that cover the whole day: the owner
	// is told at once all the same.
	conn, err := sql.Open("sqlite", w.path)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Exec(`UPDATE people SET quiet_hours = ?`, `{"time_zone":"UTC","periods":[{"days":["mon","tue","wed","thu","fri","sat","sun"],"start":"00:00","end":"23:59"}]}`); err != nil {
		t.Fatal(err)
	}
	w.now = time.Now().Add(time.Minute)
	if _, err := w.st.CreateMember(ctx, store.Command("create-user"), w.ws, store.NewPerson{Name: "Carol"}, []string{project}, store.Viewer); err != nil {
		t.Fatal(err)
	}
	if err := w.sender.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	d.mu.Lock()
	last := d.got[len(d.got)-1]
	d.mu.Unlock()
	if d.requests() != 2 || !strings.Contains(last.Title, "a command on the server") || !strings.Contains(last.Body, "made Carol viewer of Shop") {
		t.Fatalf("%d pushes, the last %+v", d.requests(), last)
	}
	states := func() string {
		t.Helper()
		msgs, err := w.st.Messages(ctx, w.admin, 20)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, m := range msgs {
			if m.Kind == store.NoticeKind {
				out = append(out, m.State+": "+m.Items[0].Title)
			}
		}
		return strings.Join(out, "\n")
	}
	if got := states(); !strings.HasPrefix(got, "sent: ") || !strings.Contains(got, "Carol") {
		t.Errorf("recorded as %q", got)
	}
	// A server kept in shadow mode sends nothing, a notice neither.
	w.sender.Shadow = true
	w.now = time.Now().Add(2 * time.Minute)
	if _, err := w.st.CreateMember(ctx, store.Command("create-user"), w.ws, store.NewPerson{Name: "Dave"}, []string{project}, store.Viewer); err != nil {
		t.Fatal(err)
	}
	w.now = time.Now().Add(3 * time.Minute)
	if err := w.sender.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	if d.requests() != 2 {
		t.Errorf("%d pushes, the last from a server told to send nothing", d.requests())
	}
	if got := states(); !strings.Contains(got, "shadow: A command on the server (create-user) made Dave") {
		t.Errorf("held back, it is recorded as %q", got)
	}
}

// A notice that cannot be pushed fails as it is, with no other way tried: it
// went to every way the owner has at once.
func TestAFailedNoticeDoesNotFallBack(t *testing.T) {
	w := newWorld(t)
	ctx := t.Context()
	d := newDevice(t)
	w.check("Shop", "backup")
	w.set("11:00")
	w.confirm(d, w.withPush(d, ""))
	project, _ := w.st.ProjectByName(ctx, w.ws, "Shop")
	d.answer(410) // the device is gone
	if _, err := w.st.CreateMember(ctx, store.Command("create-user"), w.ws, store.NewPerson{Name: "Carol"}, []string{project}, store.Viewer); err != nil {
		t.Fatal(err)
	}
	w.now = time.Now().Add(time.Minute)
	if err := w.sender.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	msgs, _ := w.st.Messages(ctx, w.admin, 10)
	notices := 0
	for _, m := range msgs {
		if m.Kind == store.NoticeKind {
			notices++
			if m.State != "failed" {
				t.Errorf("%+v", m)
			}
		}
	}
	if notices != 1 {
		t.Errorf("%d deliveries of one notice to one device", notices)
	}
}
