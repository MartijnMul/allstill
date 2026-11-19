// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/MartijnMul/allstill/internal/check"
	"github.com/MartijnMul/allstill/internal/store/internal/db"
	"github.com/MartijnMul/allstill/internal/webpush"
)

// entries reads the log as it stands, oldest first.
func entries(t *testing.T, s *Store) []db.LogEntry {
	t.Helper()
	rows, err := s.r.QueryContext(t.Context(), `SELECT id, workspace_id, happened_at, recorded_at, project_id, project_name, kind,
		about_kind, about_id, about_name, by_kind, by_id, by_name, by_outsider, person_id, person_name, person_outsider,
		incident_id, hidden, group_id, details FROM log_entries ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []db.LogEntry
	for rows.Next() {
		var e db.LogEntry
		if err := rows.Scan(&e.ID, &e.WorkspaceID, &e.HappenedAt, &e.RecordedAt, &e.ProjectID, &e.ProjectName, &e.Kind,
			&e.AboutKind, &e.AboutID, &e.AboutName, &e.ByKind, &e.ByID, &e.ByName, &e.ByOutsider, &e.PersonID, &e.PersonName, &e.PersonOutsider,
			&e.IncidentID, &e.Hidden, &e.GroupID, &e.Details); err != nil {
			t.Fatal(err)
		}
		out = append(out, e)
	}
	return out
}

// since runs do, and returns the entries it wrote.
func since(t *testing.T, s *Store, do func()) []db.LogEntry {
	t.Helper()
	n := len(entries(t, s))
	do()
	return entries(t, s)[n:]
}

func kinds(es []db.LogEntry) []string {
	out := make([]string, len(es))
	for i, e := range es {
		out[i] = e.Kind
	}
	return out
}

func detailsOfEntry(t *testing.T, e db.LogEntry) map[string]any {
	t.Helper()
	var d map[string]any
	if err := json.Unmarshal([]byte(e.Details), &d); err != nil {
		t.Fatal(err)
	}
	return d
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// Every action writes its one entry, saying who did it (R5 to R7, R15).
func TestEveryActionIsWritten(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	s := f.s
	at := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	admin, err := s.AdminAccess(ctx, f.ws, "test-command")
	must(t, err)
	byCommand := Command("test-command")
	other, _, err := s.CreateProject(ctx, byCommand, f.ws, "Other")
	must(t, err)
	must(t, s.SetMember(ctx, byCommand, f.ws, other, f.editor.PersonID, Editor))
	editor, err := s.AccessOfPerson(ctx, f.ws, f.editor.PersonID)
	must(t, err)
	checkID, _ := f.pingCheck(t)
	ada := newPerson(t, s, f.ws, "Ada")
	guest := newPersonLimited(t, s, f.ws, "Guest")

	const editorKind, commandKind = "person", "command"
	cases := []struct {
		name, kind, by string
		do             func() error
	}{
		{"add a check", "check.created", editorKind, func() error {
			_, err := s.CreateCheck(ctx, editor, f.project, NewCheck{Name: "added", Interval: time.Hour, Grace: time.Hour})
			return err
		}},
		{"add a check with its alerting", "check.created", editorKind, func() error {
			_, err := s.CreateCheck(ctx, editor, f.project, NewCheck{Name: "added 2", Interval: time.Hour, Grace: time.Hour, Urgency: "digest"})
			return err
		}},
		{"change it", "check.changed", editorKind, func() error {
			_, err := s.UpdateCheck(ctx, editor, checkID, Change{Name: ptr("renamed")}, at)
			return err
		}},
		{"change its schedule", "check.changed", editorKind, func() error {
			_, err := s.UpdateCheck(ctx, editor, checkID, Change{Interval: ptr(2 * time.Hour)}, at)
			return err
		}},
		{"change its alerting", "check.changed", editorKind, func() error {
			return s.SetAlertingAs(ctx, editor, checkID, "digest", false, at)
		}},
		{"change its alerting by command", "check.changed", commandKind, func() error {
			return s.SetAlerting(ctx, byCommand, f.ws, checkID, "none", false, at)
		}},
		{"pause it", "check.paused", editorKind, func() error { return s.PauseCheck(ctx, editor, checkID, at) }},
		{"resume it", "check.resumed", editorKind, func() error { return s.ResumeCheck(ctx, editor, checkID, at) }},
		{"replace its ping URL", "check.ping_url_replaced", editorKind, func() error {
			_, err := s.RotatePingURL(ctx, editor, checkID, at)
			return err
		}},
		{"make a project", "project.created", commandKind, func() error {
			_, _, err := s.CreateProject(ctx, byCommand, f.ws, "New")
			return err
		}},
		{"rename a project", "project.renamed", commandKind, func() error {
			return s.RenameProject(ctx, byCommand, f.ws, other, "Renamed")
		}},
		{"add a person", "person.added", commandKind, func() error {
			_, err := s.CreatePerson(ctx, byCommand, f.ws, NewPerson{Name: "Bob"})
			return err
		}},
		{"give a role", "member.set", commandKind, func() error { return s.SetMember(ctx, byCommand, f.ws, f.project, guest, Viewer) }},
		{"change a role", "member.set", commandKind, func() error { return s.SetMember(ctx, byCommand, f.ws, f.project, guest, Editor) }},
		{"take a role away", "member.removed", commandKind, func() error { return s.RemoveMember(ctx, byCommand, f.ws, f.project, guest) }},
		// Someone in the project's team keeps no row of their own: viewer only says
		// not to tell them.
		{"stop telling someone in the team", "alerts.chosen", commandKind, func() error { return s.SetMember(ctx, byCommand, f.ws, f.project, ada, Viewer) }},
		{"tell them again", "alerts.chosen", commandKind, func() error { return s.RemoveMember(ctx, byCommand, f.ws, f.project, ada) }},
		{"make a passkey link", "passkey.link_made", commandKind, func() error {
			_, err := s.CreatePasskeyLink(ctx, byCommand, f.ws, ada, at)
			return err
		}},
		{"turn alerts on", "alerts.mode", commandKind, func() error {
			_, err := s.SetAlerts(ctx, byCommand, true)
			return err
		}},
		{"turn alerts back to shadow mode", "alerts.mode", commandKind, func() error {
			_, err := s.SetAlerts(ctx, byCommand, false)
			return err
		}},
		{"rekey a probe", "probe.rekeyed", commandKind, nil},
		{"delete a check", "check.deleted", editorKind, func() error { return s.DeleteCheck(ctx, editor, checkID, at) }},
	}
	_, _, err = s.CreateProbe(ctx, admin, "p", f.project, nil)
	must(t, err)
	for _, c := range cases {
		do := c.do
		if do == nil {
			do = func() error {
				_, err := s.RekeyProbe(ctx, admin, "", "p")
				return err
			}
		}
		t.Run(c.name, func(t *testing.T) {
			got := since(t, s, func() { must(t, do()) })
			if len(got) != 1 || got[0].Kind != c.kind || got[0].ByKind != c.by {
				t.Fatalf("%v by %v, want one %s by %s", kinds(got), got, c.kind, c.by)
			}
		})
	}
	t.Run("the names stay when the check goes", func(t *testing.T) {
		es := entries(t, s)
		last := es[len(es)-1]
		if last.Kind != "check.deleted" {
			last = es[slices.IndexFunc(es, func(e db.LogEntry) bool { return e.Kind == "check.deleted" })]
		}
		if last.AboutName != "renamed" || last.AboutID != checkID || last.ProjectName != "Backups" || last.ByName != "Editor" {
			t.Errorf("%+v", last)
		}
	})
}

func TestMovingWritesOneEntryInEachProject(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	at := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	other, _, err := f.s.CreateProject(ctx, Command("test"), f.ws, "Other")
	must(t, err)
	checkID, uuid := f.pingCheck(t)
	_, err = f.s.Ping(ctx, ping(uuid, check.Fail, "", at))
	must(t, err)
	got := since(t, f.s, func() { must(t, f.s.MoveCheck(ctx, Command("move-checks"), f.ws, checkID, other, at)) })
	if want := []string{"check.moved_out", "check.moved_in"}; !slices.Equal(kinds(got), want) {
		t.Fatalf("%v", kinds(got))
	}
	if got[0].ProjectID != f.project || got[1].ProjectID != other || got[1].ProjectName != "Other" {
		t.Errorf("%+v", got)
	}
	arrived := detailsOfEntry(t, got[1])
	if arrived["state"] != "down" || arrived["incident_open"] != true {
		t.Errorf("%v", arrived)
	}
	// Moving nowhere writes nothing.
	got = since(t, f.s, func() { must(t, f.s.MoveCheck(ctx, Command("move-checks"), f.ws, checkID, other, at)) })
	if len(got) != 0 {
		t.Errorf("%v", kinds(got))
	}
}

func TestSigningInAndDevicesAreWritten(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	s := f.s
	at := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	ada := newPerson(t, s, f.ws, "Ada")
	bob := newPerson(t, s, f.ws, "Bob")
	link, err := s.CreatePasskeyLink(ctx, Command("test"), f.ws, ada, at)
	must(t, err)

	got := since(t, s, func() {
		_, err := s.AddPasskey(ctx, link, key("k1"), "a browser", at)
		must(t, err)
	})
	if !slices.Equal(kinds(got), []string{"passkey.added"}) || got[0].ByName != "Ada" || got[0].PersonID != ada {
		t.Fatalf("%+v", got)
	}
	keys, err := s.Passkeys(ctx, f.ws, ada)
	must(t, err)
	got = since(t, s, func() {
		_, err := s.SignIn(ctx, f.ws, ada, keys[0].ID, []byte(`{}`), "a browser", at)
		must(t, err)
	})
	if !slices.Equal(kinds(got), []string{"signin"}) || got[0].ByName != "Ada" {
		t.Fatalf("%+v", got)
	}
	got = since(t, s, func() { must(t, s.PasskeyLooksCopied(ctx, f.ws, ada, at)) })
	if !slices.Equal(kinds(got), []string{"passkey.looks_copied"}) {
		t.Fatalf("%v", kinds(got))
	}

	// A device: nothing until it is confirmed, then who holds it, never the address.
	const address = "ada@example.com"
	var c Channel
	got = since(t, s, func() {
		c, err = s.SaveEmail(ctx, f.ws, ada, address)
		must(t, err)
	})
	if len(got) != 0 {
		t.Errorf("adding: %v", kinds(got))
	}
	must(t, s.SetConfirm(ctx, Pending{ChannelID: c.ID, PersonID: ada, ChannelKind: "email", Address: address}, "token"))
	got = since(t, s, func() { must(t, s.Confirm(ctx, ada, c.ID, "token", at)) })
	if !slices.Equal(kinds(got), []string{"channel.confirmed"}) || got[0].PersonName != "Ada" {
		t.Fatalf("%+v", got)
	}
	got = since(t, s, func() { must(t, s.Confirm(ctx, ada, c.ID, "token", at)) })
	if len(got) != 0 {
		t.Errorf("confirming again: %v", kinds(got))
	}
	got = since(t, s, func() { must(t, s.DeleteChannel(ctx, ada, c.ID)) })
	if !slices.Equal(kinds(got), []string{"channel.removed"}) || got[0].ByName != "Ada" {
		t.Fatalf("%+v", got)
	}
	for _, e := range entries(t, s) {
		if strings.Contains(fmt.Sprintf("%+v", e), address) {
			t.Errorf("the address is in %+v", e)
		}
	}

	// A device handed on says so; the same device sent again says nothing.
	sub := webpush.Subscription{Endpoint: "https://push.example.com/1", P256dh: []byte("a"), Auth: []byte("b")}
	p, err := s.SavePush(ctx, f.ws, ada, "Phone", sub, "key")
	must(t, err)
	got = since(t, s, func() {
		_, err := s.SavePush(ctx, f.ws, ada, "Phone", sub, "key")
		must(t, err)
	})
	if len(got) != 0 {
		t.Errorf("sent again: %v", kinds(got))
	}
	got = since(t, s, func() {
		_, err := s.SavePush(ctx, f.ws, bob, "Phone", sub, "key")
		must(t, err)
	})
	if !slices.Equal(kinds(got), []string{"channel.handed_on", "channel.handed_away"}) || got[0].PersonID != bob || got[1].PersonID != ada {
		t.Fatalf("%+v", got)
	}

	// A device the push service says is gone was removed by Allstill.
	got = since(t, s, func() {
		must(t, s.Failed(ctx, Pending{ID: "none", WorkspaceID: f.ws, PersonID: bob, Kind: "test", ChannelID: p.ID, ChannelKind: "webpush"}, "gone", true, false, at, Ways{}))
	})
	if !slices.Equal(kinds(got), []string{"channel.removed"}) || got[0].ByKind != "allstill" {
		t.Fatalf("%+v", got)
	}
	// A second message from the same batch, finding it gone, writes nothing.
	got = since(t, s, func() {
		must(t, s.Failed(ctx, Pending{ID: "none", WorkspaceID: f.ws, PersonID: bob, Kind: "test", ChannelID: p.ID, ChannelKind: "webpush"}, "gone", true, false, at, Ways{}))
	})
	if len(got) != 0 {
		t.Errorf("%v", kinds(got))
	}
}

// An action that changed nothing writes nothing (R8).
func TestAnActionThatChangedNothingWritesNothing(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	at := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	checkID, _ := f.pingCheck(t)
	ada := newPerson(t, f.s, f.ws, "Ada")
	must(t, f.s.SetMember(ctx, Command("test"), f.ws, f.project, ada, Viewer))
	cases := map[string]func() error{
		"saving unchanged": func() error {
			_, err := f.s.UpdateCheck(ctx, f.editor, checkID, Change{Name: ptr("nightly")}, at)
			return err
		},
		"the same alerting":           func() error { return f.s.SetAlertingAs(ctx, f.editor, checkID, "now", false, at) },
		"the same role":               func() error { return f.s.SetMember(ctx, Command("test"), f.ws, f.project, ada, Viewer) },
		"the same name":               func() error { return f.s.RenameProject(ctx, Command("test"), f.ws, f.project, "Backups") },
		"resuming what is not paused": func() error { return f.s.ResumeCheck(ctx, f.editor, checkID, at) },
		"the same alert mode": func() error {
			_, err := f.s.SetAlerts(ctx, Command("test"), false)
			return err
		},
	}
	for name, do := range cases {
		t.Run(name, func(t *testing.T) {
			if got := since(t, f.s, func() { must(t, do()) }); len(got) != 0 {
				t.Errorf("%v", kinds(got))
			}
		})
	}
}

// A change names its fields, with values only for those on the list (R12).
func TestAChangeShowsOnlyListedValues(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	at := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	id := f.httpCheck(t)
	changesIn := func(es []db.LogEntry) map[string]fieldChange {
		t.Helper()
		if len(es) != 1 {
			t.Fatalf("%v", kinds(es))
		}
		var d struct{ Changes []fieldChange }
		must(t, json.Unmarshal([]byte(es[0].Details), &d))
		out := map[string]fieldChange{}
		for _, c := range d.Changes {
			out[c.Field] = c
		}
		return out
	}
	got := changesIn(since(t, f.s, func() {
		_, err := f.s.UpdateCheck(ctx, f.editor, id, Change{
			Name: ptr("shop front"), Description: ptr("about the shop"), Tags: ptr([]string{"web"}),
			Settings: map[string]any{"url": "https://shop.example.com/other", "fail_after": 3},
			Urgency:  ptr("digest"),
		}, at)
		must(t, err)
	}))
	if c := got["name"]; c.From != "shop" || c.To != "shop front" {
		t.Errorf("name: %+v", c)
	}
	if c := got["fail_after"]; c.To != float64(3) {
		t.Errorf("fail_after: %+v", c)
	}
	if c := got["tags"]; c.To == nil {
		t.Errorf("tags: %+v", c)
	}
	for _, field := range []string{"url", "description", "urgency"} {
		c, ok := got[field]
		if !ok {
			t.Errorf("%s is not named: %v", field, got)
		}
		if c.From != nil || c.To != nil {
			t.Errorf("%s has a value: %+v", field, c)
		}
	}

	// A check that jobs ping: the schedule, its slug and its alerting.
	admin, err := f.s.AdminAccess(ctx, f.ws, "test")
	must(t, err)
	pingID, _ := f.pingCheck(t)
	got = changesIn(since(t, f.s, func() {
		_, err := f.s.UpdateCheck(ctx, admin, pingID, Change{
			Slug: ptr("nightly"), Cron: ptr("0 3 * * *"), TimeZone: ptr("Europe/Amsterdam"), Grace: ptr(2 * time.Hour),
			Urgency: ptr("now"), Critical: ptr(true),
		}, at)
		must(t, err)
	}))
	if c := got["slug"]; c.To != "nightly" {
		t.Errorf("slug: %+v", c)
	}
	if c := got["every"]; c.From != float64(3600) && c.From != int64(3600) {
		t.Errorf("every: %+v", c)
	}
	if c := got["cron"]; c.To != "0 3 * * *" {
		t.Errorf("cron: %+v", c)
	}
	if c := got["time_zone"]; c.To != "Europe/Amsterdam" {
		t.Errorf("time_zone: %+v", c)
	}
	if c := got["grace"]; c.To != float64(7200) && c.To != int64(7200) {
		t.Errorf("grace: %+v", c)
	}
	if c, ok := got["critical"]; !ok || c.To != nil {
		t.Errorf("critical: %+v", c)
	}
	if _, ok := got["urgency"]; ok {
		t.Errorf("the urgency did not change: %v", got)
	}

	// Who runs it is named, without saying which.
	p, probeCheck := aProbe(t, f)
	second, _, err := f.s.CreateProbe(ctx, admin, "second", f.project, nil)
	must(t, err)
	got = changesIn(since(t, f.s, func() {
		_, err := f.s.UpdateCheck(ctx, f.editor, probeCheck, Change{Probe: ptr(second)}, at)
		must(t, err)
	}))
	if c, ok := got["probe"]; !ok || c.To != nil || strings.Contains(fmt.Sprint(c), p.ID) {
		t.Errorf("probe: %+v", c)
	}
}

// Nothing Allstill does itself puts a secret in the log, whatever the way in (R13).
func TestNoSecretReachesTheLog(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	const marker = "sekret-marker"
	at := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	c, err := f.s.CreateCheck(ctx, f.editor, f.project, NewCheck{Name: "shop", Kind: "http", Interval: time.Minute, Settings: map[string]any{
		"url": "https://shop.example.com/?token=" + marker,
	}})
	must(t, err)
	_, err = f.s.UpdateCheck(ctx, f.editor, c.ID, Change{Settings: map[string]any{
		"url": "https://shop.example.com/a?token=" + marker, "location": "https://shop.example.com/" + marker,
		"expected_status": []int{302}, "ca": "-----BEGIN " + marker,
	}}, at)
	if err != nil {
		_, err = f.s.UpdateCheck(ctx, f.editor, c.ID, Change{Settings: map[string]any{
			"url": "https://shop.example.com/a?token=" + marker, "location": "https://shop.example.com/" + marker, "expected_status": []int{302},
		}}, at)
	}
	must(t, err)
	_, err = f.s.UpdateCheck(ctx, f.editor, c.ID, Change{Settings: map[string]any{
		"url": "https://shop.example.com/b?token=" + marker, "json_field": map[string]any{"pointer": "/status", "equals": marker},
	}}, at)
	must(t, err)
	// Reasons a check Allstill runs, or a probe, can give.
	for i, reason := range []string{
		"status is " + marker + " instead of ok",
		"no " + marker + " in the answer",
		marker,
		"HTTP " + marker,
		"Allstill cannot read its schedule or settings: " + marker,
	} {
		_, err := f.s.RecordAttempt(ctx, f.ws, c.ID, Attempt{
			Attempt: check.Attempt{Outcome: check.Failed, Reason: reason, At: at.Add(time.Duration(i) * time.Minute)},
		})
		must(t, err)
		_, err = f.s.RecordAttempt(ctx, f.ws, c.ID, Attempt{
			Attempt: check.Attempt{Outcome: check.Failed, Reason: reason, At: at.Add(time.Duration(i)*time.Minute + time.Second)},
		})
		must(t, err)
		_, err = f.s.RecordAttempt(ctx, f.ws, c.ID, Attempt{Attempt: check.Attempt{Outcome: check.Passed, At: at.Add(time.Duration(i)*time.Minute + 2*time.Second)}})
		must(t, err)
	}
	must(t, f.s.DeleteCheck(ctx, f.editor, c.ID, at.Add(time.Hour)))
	es := entries(t, f.s)
	if !slices.ContainsFunc(es, func(e db.LogEntry) bool { return e.Kind == "check.state" && detailsOfEntry(t, e)["to"] == "down" }) {
		t.Fatal("nothing went down, so reasons were not tried")
	}
	for _, e := range es {
		if strings.Contains(fmt.Sprintf("%+v", e), marker) {
			t.Errorf("the marker is in %+v", e)
		}
	}
}

// A reason is kept only in Allstill's own wording (C4).
func TestLogReason(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"", ""},
		{"The job reported a failure.", "The job reported a failure."},
		{"The job failed with exit status 3.", "The job failed with an exit status."},
		{"The ping is overdue.", "The ping is overdue."},
		{"No ping within the grace period of 1 hour.", "No ping within the grace period."},
		{"A run started but did not end within the grace period of 1 hour.", "A run started but did not end in time."},
		{check.ScheduleChanged, check.ScheduleChanged},
		{"HTTP 503", "The server answered with another HTTP status."},
		{"HTTP 503 instead of 200 or 204", "The server answered with another HTTP status."},
		{"HTTP 200 instead of a redirect", "The server answered with another HTTP status."},
		{"rate limited (HTTP 429)", "rate limited (HTTP 429)"},
		{"more than 10 redirects", "too many redirects"},
		{"certificate expires in 3 days", "the certificate is running out"},
		{"no answer within 10 s", "no answer in time"},
		{"no route to the address", "no route to the address"},
		{"status is broken instead of ok", "a JSON field was not as expected"},
		{"no status in the answer", "a JSON field was not as expected"},
		{ClockOffReason, ClockOffReason},
		{"something a probe made up", ""},
		{"Allstill cannot read its schedule or settings: unknown time zone Mars/Base", "Allstill cannot read its schedule or settings."},
	} {
		if got := logReason(tc.in); got != tc.want {
			t.Errorf("logReason(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// A state change says what caused it, and what it left out of the list (R1, R10, R17).
func TestAStateChangeIsWritten(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	at := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	checkID, uuid := f.pingCheck(t)

	got := since(t, f.s, func() {
		_, err := f.s.Ping(ctx, ping(uuid, check.Success, "", at))
		must(t, err)
	})
	if !slices.Equal(kinds(got), []string{"check.state"}) || got[0].ByKind != "job" || got[0].AboutID != checkID {
		t.Fatalf("%+v", got)
	}
	// A ping that changes nothing is no entry (R2).
	if got := since(t, f.s, func() {
		_, err := f.s.Ping(ctx, ping(uuid, check.Success, "", at.Add(time.Minute)))
		must(t, err)
	}); len(got) != 0 {
		t.Errorf("%v", kinds(got))
	}
	got = since(t, f.s, func() {
		_, err := f.s.Ping(ctx, ping(uuid, check.Fail, "", at.Add(2*time.Minute)))
		must(t, err)
	})
	d := detailsOfEntry(t, got[0])
	if d["from"] != "up" || d["to"] != "down" || d["reason"] != "The job reported a failure." || got[0].IncidentID == "" || got[0].Hidden != 0 {
		t.Errorf("%+v, %v", got[0], d)
	}

	// A deadline passing is by Allstill.
	f2, uuid2 := f.pingCheckNamed(t, "hourly")
	_, err := f.s.Ping(ctx, ping(uuid2, check.Success, "", at))
	must(t, err)
	got = since(t, f.s, func() {
		_, err := f.s.ProcessDue(ctx, at.Add(90*time.Minute))
		must(t, err)
	})
	i := slices.IndexFunc(got, func(e db.LogEntry) bool { return e.AboutID == f2 })
	if i < 0 || got[i].ByKind != "allstill" {
		t.Errorf("%+v", got)
	}

	// A ping that makes a check is by a job.
	got = since(t, f.s, func() {
		_, err := f.s.Ping(ctx, PingIn{PingKey: f.pingKey, Slug: "made", Create: true, Kind: check.Fail, ExitStatus: -1, Method: "POST", At: at})
		must(t, err)
	})
	// One entry, holding both what it did and what the ping said (R6).
	if len(got) != 1 || got[0].Kind != "check.created" || got[0].ByKind != "job" || got[0].AboutName != "made" ||
		detailsOfEntry(t, got[0])["state"] != "down" || got[0].IncidentID == "" {
		t.Errorf("%+v", got)
	}
}

func (f fixture) pingCheckNamed(t *testing.T, name string) (string, string) {
	t.Helper()
	c, err := f.s.CreateCheck(t.Context(), f.editor, f.project, NewCheck{Name: name, Interval: time.Hour, Grace: time.Minute})
	must(t, err)
	return c.ID, c.PingUUID
}

// A ping that resumes a paused check says so, by a job, and a person's pause is one
// entry holding both the action and the change of state (R6).
// What closes or carries an incident names it, as that cannot be added later.
func TestActionsNameTheirIncident(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	at := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	other, _, err := f.s.CreateProject(ctx, Command("test"), f.ws, "Other")
	must(t, err)
	must(t, f.s.SetMember(ctx, Command("test"), f.ws, other, f.editor.PersonID, Editor))
	editor, err := f.s.AccessOfPerson(ctx, f.ws, f.editor.PersonID)
	must(t, err)
	checkID, uuid := f.pingCheck(t)
	down := func(minute int) {
		t.Helper()
		_, err := f.s.Ping(ctx, ping(uuid, check.Fail, "", at.Add(time.Duration(minute)*time.Minute)))
		must(t, err)
	}
	down(0)
	got := since(t, f.s, func() {
		must(t, f.s.MoveCheck(ctx, Command("test"), f.ws, checkID, other, at.Add(time.Minute)))
		_, err := f.s.UpdateCheck(ctx, editor, checkID, Change{Name: ptr("renamed")}, at.Add(time.Minute))
		must(t, err)
		must(t, f.s.PauseCheck(ctx, editor, checkID, at.Add(2*time.Minute)))
	})
	for _, e := range got {
		if e.IncidentID == "" {
			t.Errorf("%s names no incident", e.Kind)
		}
	}
	must(t, f.s.ResumeCheck(ctx, editor, checkID, at.Add(3*time.Minute)))
	down(4)
	got = since(t, f.s, func() { must(t, f.s.DeleteCheck(ctx, editor, checkID, at.Add(5*time.Minute))) })
	if got[0].IncidentID == "" {
		t.Errorf("%+v", got[0])
	}
}

func TestPausingIsOneEntryAndAPingResumes(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	at := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	_, uuid := f.pingCheck(t)
	_, err := f.s.Ping(ctx, ping(uuid, check.Success, "", at))
	must(t, err)
	got := since(t, f.s, func() {
		cs, err := f.s.Checks(ctx, f.editor)
		must(t, err)
		must(t, f.s.PauseCheck(ctx, f.editor, cs[0].ID, at.Add(time.Minute)))
	})
	if !slices.Equal(kinds(got), []string{"check.paused"}) || detailsOfEntry(t, got[0])["from"] != "up" {
		t.Fatalf("%+v", got)
	}
	got = since(t, f.s, func() {
		_, err := f.s.Ping(ctx, ping(uuid, check.Success, "", at.Add(2*time.Minute)))
		must(t, err)
	})
	if !slices.Equal(kinds(got), []string{"check.resumed"}) || got[0].ByKind != "job" {
		t.Fatalf("%+v", got)
	}
}

// A check run by a probe says which, and so does the probe's own check.
func TestAProbesChangesAreByTheProbe(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	p, checkID := aProbe(t, f)
	at := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	got := since(t, f.s, func() {
		_, err := f.s.ApplyProbeRuns(ctx, p, []ProbeRun{{ID: "r1", CheckID: checkID, Outcome: check.Passed, EndedAgo: 2 * time.Minute}}, at, at)
		must(t, err)
	})
	if len(got) != 2 {
		t.Fatalf("%+v", got)
	}
	for _, e := range got {
		if e.ByKind != "probe" || e.ByName != "inside" || e.ByID != p.ID {
			t.Errorf("%+v", e)
		}
	}
	// It happened two minutes before Allstill learned of it (R14).
	run := got[slices.IndexFunc(got, func(e db.LogEntry) bool { return e.AboutID == checkID })]
	if time.UnixMilli(run.RecordedAt).Sub(time.UnixMilli(run.HappenedAt)) != 2*time.Minute {
		t.Errorf("recorded %d, happened %d", run.RecordedAt, run.HappenedAt)
	}
}

func TestQuietChange(t *testing.T) {
	for _, tc := range []struct {
		from, to check.State
		incident bool
		want     bool
	}{
		{check.Up, check.Unknown, false, true},
		{check.Down, check.Unknown, true, true},
		{check.Unknown, check.Up, false, true},
		{check.Unknown, check.New, false, true},
		{check.Unknown, check.Up, true, false}, // a real recovery
		{check.Unknown, check.Down, false, false},
		{check.Unknown, check.Down, true, false},
		{check.Unknown, check.Warning, false, false},
		{check.Unknown, check.Late, false, false},
		{check.Up, check.Down, false, false},
		{check.Down, check.Up, true, false},
		{check.Up, check.Late, false, false},
	} {
		// Going late has its own rule, in briefLate.
		if got := quietChange(tc.from, tc.to, tc.incident); got != tc.want {
			t.Errorf("%s to %s, incident %v: %v", tc.from, tc.to, tc.incident, got)
		}
	}
}

// After a restart a project sees one line, and the checks' own changes only when
// narrowed to them (R17, R18).
func TestARestartIsOneLinePerProject(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	at := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	other, _, err := f.s.CreateProject(ctx, Command("test"), f.ws, "Other")
	must(t, err)
	must(t, f.s.SetMember(ctx, Command("test"), f.ws, other, f.editor.PersonID, Editor))
	editor, err := f.s.AccessOfPerson(ctx, f.ws, f.editor.PersonID)
	must(t, err)
	for _, project := range []string{f.project, f.project, other} {
		c, err := f.s.CreateCheck(ctx, editor, project, NewCheck{Name: "hourly", Interval: time.Hour, Grace: time.Minute})
		must(t, err)
		_, err = f.s.Ping(ctx, ping(c.PingUUID, check.Success, "", at))
		must(t, err)
	}
	must(t, f.s.Alive(ctx, at.Add(10*time.Minute)))
	after := at.Add(3 * time.Hour)
	got := since(t, f.s, func() { must(t, f.s.Recover(ctx, after)) })
	var shown []db.LogEntry
	for _, e := range got {
		if e.Hidden == 0 {
			shown = append(shown, e)
		}
	}
	if len(got) != 5 || len(shown) != 2 {
		t.Fatalf("%v, of which shown %v", kinds(got), kinds(shown))
	}
	for _, line := range shown {
		d := detailsOfEntry(t, line)
		want := float64(1)
		if line.ProjectID == f.project {
			want = 2
		}
		if line.Kind != "checks.unjudged" || d["count"] != want || d["from"] != float64(at.Add(10*time.Minute).UnixMilli()) || d["to"] != float64(after.UnixMilli()) {
			t.Errorf("%+v, %v", line, d)
		}
		n := 0
		for _, e := range got {
			if e.GroupID == line.GroupID && e.Hidden == 1 {
				n++
			}
		}
		if float64(n) != want {
			t.Errorf("%d checks in the group of %+v", n, line)
		}
	}
}

// A check past its ceiling writes one line, and one when it settles (R16).
func TestAFlappingCheckHasACeiling(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	at := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	checkID, uuid := f.pingCheck(t)
	flips := func(from time.Time, n int) time.Time {
		for i := range n {
			kind := check.Fail
			if i%2 == 1 {
				kind = check.Success
			}
			from = from.Add(time.Minute)
			_, err := f.s.Ping(ctx, ping(uuid, kind, "", from))
			must(t, err)
		}
		return from
	}
	end := flips(at, 50)
	count := func(kind string) int {
		n := 0
		for _, e := range entries(t, f.s) {
			if e.Kind == kind && e.AboutID == checkID {
				n++
			}
		}
		return n
	}
	if count("check.state") != flappingChanges || count("check.flapping") != 1 {
		t.Fatalf("%d states, %d lines", count("check.state"), count("check.flapping"))
	}
	// What people do still shows.
	got := since(t, f.s, func() { must(t, f.s.PauseCheck(ctx, f.editor, checkID, end.Add(time.Second))) })
	if !slices.Equal(kinds(got), []string{"check.paused"}) {
		t.Errorf("%v", kinds(got))
	}
	// Nor does a ping that resumes it stay out, by being past the ceiling.
	got = since(t, f.s, func() {
		_, err := f.s.Ping(ctx, ping(uuid, check.Success, "", end.Add(2*time.Second)))
		must(t, err)
	})
	if !slices.Equal(kinds(got), []string{"check.resumed"}) || got[0].ByKind != "job" {
		t.Errorf("%+v", got)
	}
	must(t, f.s.PauseCheck(ctx, f.editor, checkID, end.Add(3*time.Second)))
	must(t, f.s.ResumeCheck(ctx, f.editor, checkID, end.Add(4*time.Second)))
	// Not steady yet.
	if got := since(t, f.s, func() { must(t, f.s.closeFlapping(ctx, end.Add(30*time.Minute))) }); len(got) != 0 {
		t.Errorf("%v", kinds(got))
	}
	got = since(t, f.s, func() { must(t, f.s.closeFlapping(ctx, end.Add(2*time.Hour))) })
	if !slices.Equal(kinds(got), []string{"check.steady"}) || detailsOfEntry(t, got[0])["state"] != "new" {
		t.Fatalf("%+v", got)
	}
	// Once only.
	if got := since(t, f.s, func() { must(t, f.s.closeFlapping(ctx, end.Add(3*time.Hour))) }); len(got) != 0 {
		t.Errorf("%v", kinds(got))
	}
	// And it is written again.
	got = since(t, f.s, func() {
		_, err := f.s.Ping(ctx, ping(uuid, check.Success, "", end.Add(4*time.Hour)))
		must(t, err)
	})
	if !slices.Equal(kinds(got), []string{"check.state"}) {
		t.Errorf("%v", kinds(got))
	}
}

// A check that moved while flapping settles in the project it is in now, and the
// one it left does not read its state (R25).
func TestAMovedFlappingCheckSettlesWhereItIs(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	at := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	other, _, err := f.s.CreateProject(ctx, Command("test"), f.ws, "Other")
	must(t, err)
	checkID, uuid := f.pingCheck(t)
	for i := range 40 {
		kind := check.Fail
		if i%2 == 1 {
			kind = check.Success
		}
		_, err := f.s.Ping(ctx, ping(uuid, kind, "", at.Add(time.Duration(i)*time.Second)))
		must(t, err)
	}
	must(t, f.s.MoveCheck(ctx, Command("test"), f.ws, checkID, other, at.Add(time.Minute)))
	got := since(t, f.s, func() { must(t, f.s.closeFlapping(ctx, at.Add(3*time.Hour))) })
	if !slices.Equal(kinds(got), []string{"check.steady"}) || got[0].ProjectID != other || got[0].ProjectName != "Other" {
		t.Fatalf("%+v", got)
	}
}

func TestADeletedFlappingCheckIsCalledSteady(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	at := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	checkID, uuid := f.pingCheck(t)
	for i := range 40 {
		kind := check.Fail
		if i%2 == 1 {
			kind = check.Success
		}
		_, err := f.s.Ping(ctx, ping(uuid, kind, "", at.Add(time.Duration(i)*time.Second)))
		must(t, err)
	}
	must(t, f.s.DeleteCheck(ctx, f.editor, checkID, at.Add(time.Minute)))
	got := since(t, f.s, func() { must(t, f.s.closeFlapping(ctx, at.Add(2*time.Minute))) })
	if !slices.Equal(kinds(got), []string{"check.steady"}) || got[0].AboutName != "nightly" {
		t.Fatalf("%+v", got)
	}
}

// R22 needs to know who was an outsider then, which cannot be worked out later.
func TestAnAdministratorWithNoMembershipIsMarked(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	at := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	dan, err := f.s.CreatePerson(ctx, Command("test"), f.ws, NewPerson{Name: "Dan", WorkspaceAdmin: true})
	must(t, err)
	access, err := f.s.AccessOfPerson(ctx, f.ws, dan)
	must(t, err)
	checkID, _ := f.pingCheck(t)
	got := since(t, f.s, func() { must(t, f.s.PauseCheck(ctx, access, checkID, at)) })
	if got[0].ByName != "Dan" || got[0].ByOutsider != 1 {
		t.Errorf("no member: %+v", got[0])
	}
	// A role in the project gives an owner of its team no row, so nothing changes.
	must(t, f.s.SetMember(ctx, Command("test"), f.ws, f.project, dan, Editor))
	got = since(t, f.s, func() { must(t, f.s.ResumeCheck(ctx, access, checkID, at)) })
	if got[0].ByOutsider != 1 {
		t.Errorf("an owner of the team, given a role: %+v", got[0])
	}
	// Someone with a row of their own is a member, and named.
	eve, err := f.s.CreatePerson(ctx, Command("test"), f.ws, NewPerson{Name: "Eve"})
	must(t, err)
	must(t, f.s.SetMember(ctx, Command("test"), f.ws, f.project, eve, Editor))
	asEve, err := f.s.AccessOfPerson(ctx, f.ws, eve)
	must(t, err)
	got = since(t, f.s, func() { must(t, f.s.PauseCheck(ctx, asEve, checkID, at)) })
	if got[0].ByOutsider != 0 {
		t.Errorf("a member: %+v", got[0])
	}
	// Taken out, they read as a member still: they were one until then.
	got = since(t, f.s, func() { must(t, f.s.RemoveMember(ctx, Command("test"), f.ws, f.project, eve)) })
	if got[0].PersonOutsider != 0 || got[0].PersonName != "Eve" {
		t.Errorf("removed: %+v", got[0])
	}
	// Someone in through the team who is no administrator is marked as that.
	team := teamOf(t, f.s, f.project)
	must(t, f.s.SetTeamMember(ctx, Command("test"), f.ws, team, eve, Editor))
	asEve, err = f.s.AccessOfPerson(ctx, f.ws, eve)
	must(t, err)
	got = since(t, f.s, func() { must(t, f.s.ResumeCheck(ctx, asEve, checkID, at)) })
	if got[0].ByOutsider != 2 {
		t.Errorf("in through the team: %+v", got[0])
	}
	// What was written stays, whatever membership becomes.
	if e := entries(t, f.s); e[slices.IndexFunc(e, func(e db.LogEntry) bool { return e.Kind == "check.paused" })].ByOutsider != 1 {
		t.Error("the earlier entry changed")
	}
}

// An entry is never changed, and an action is not done unless it is recorded (R33, R34).
func TestTheRecordHolds(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	at := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	checkID, _ := f.pingCheck(t)
	must(t, f.s.PauseCheck(ctx, f.editor, checkID, at))
	if _, err := f.s.w.ExecContext(ctx, `UPDATE log_entries SET by_name = 'someone else'`); err == nil || !strings.Contains(err.Error(), "not changed") {
		t.Errorf("an update went through: %v", err)
	}
	// A later migration can still add a column.
	if _, err := f.s.w.ExecContext(ctx, `ALTER TABLE log_entries ADD COLUMN later TEXT NOT NULL DEFAULT ''`); err != nil {
		t.Error(err)
	}
	if _, err := f.s.w.ExecContext(ctx, `ALTER TABLE log_entries DROP COLUMN later`); err != nil {
		t.Error(err)
	}
	// With no way to record it, the action does not happen.
	if _, err := f.s.w.ExecContext(ctx, `DROP TABLE log_entries`); err != nil {
		t.Fatal(err)
	}
	if err := f.s.ResumeCheck(ctx, f.editor, checkID, at); err == nil {
		t.Fatal("resumed with no way to record it")
	}
	if c := f.get(t, checkID); c.State != string(check.Paused) {
		t.Errorf("%s", c.State)
	}
}

// Only the age limit removes entries, and it cannot be set too short (R35).
func TestTidyRemovesOnlyWhatIsPastItsAge(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	checkID, _ := f.pingCheck(t)
	now := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	must(t, f.s.PauseCheck(ctx, f.editor, checkID, now.Add(-DefaultLogAge-time.Hour)))
	must(t, f.s.ResumeCheck(ctx, f.editor, checkID, now.Add(-DefaultLogAge+time.Hour)))
	for {
		more, err := f.s.Tidy(ctx, now)
		must(t, err)
		if !more {
			break
		}
	}
	var got []string
	for _, e := range entries(t, f.s) {
		if e.AboutID == checkID && e.Kind != "check.created" {
			got = append(got, e.Kind)
		}
	}
	if !slices.Equal(got, []string{"check.resumed"}) {
		t.Errorf("%v", got)
	}

	for _, tc := range []struct {
		log time.Duration
		ok  bool
	}{{0, true}, {DefaultLogAge, true}, {MinLogAge, true}, {MinLogAge - time.Hour, false}, {time.Hour, false}} {
		err := Retention{Runs: DefaultRetention.Runs, Output: DefaultRetention.Output, Log: tc.log}.Check()
		if (err == nil) != tc.ok {
			t.Errorf("%v: %v", tc.log, err)
		}
	}
}

// Where the log starts is known, whatever tidying has removed since (R30).
func TestTheLogKnowsWhereItStarts(t *testing.T) {
	s := open(t)
	v, err := s.Setting(t.Context(), "log_started_at")
	if err != nil || v == "" {
		t.Fatalf("%q, %v", v, err)
	}
}

// A message queued before the log knew of checks' ids still says the person was
// reached, without tying it to a check it cannot name (plan, step 2).
func TestAMessageWithoutChecksIsOneLine(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	at := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	ada := newPerson(t, f.s, f.ws, "Ada")
	got := since(t, f.s, func() {
		must(t, f.s.Sent(ctx, Pending{ID: "none", WorkspaceID: f.ws, PersonID: ada, Kind: "digest", ChannelKind: "email", Items: []Item{
			{What: "warning", Title: "Shop: web (certificate expires in 3 days)"}, {What: "silent_check", Title: "Shop: backup"},
		}}, at))
	})
	if len(got) != 1 || got[0].Kind != "message.reached" || got[0].PersonID != ada || got[0].ProjectID != "" || strings.Contains(got[0].Details, "certificate") {
		t.Fatalf("%+v", got)
	}
	if d := detailsOfEntry(t, got[0]); d["count"] != float64(2) {
		t.Errorf("%v", d)
	}
	// A test message is no part of the log.
	if got := since(t, f.s, func() {
		must(t, f.s.Sent(ctx, Pending{ID: "none", WorkspaceID: f.ws, PersonID: ada, Kind: "test", ChannelKind: "email"}, at))
	}); len(got) != 0 {
		t.Errorf("%v", kinds(got))
	}
}

// Who is reached is marked as an outsider when they are an administrator with no
// membership, for R22, and the line names the incident.
func TestAMessageMarksAnOutsider(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	at := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	dan, err := f.s.CreatePerson(ctx, Command("test"), f.ws, NewPerson{Name: "Dan", WorkspaceAdmin: true})
	must(t, err)
	_, uuid := f.pingCheck(t)
	_, err = f.s.Ping(ctx, ping(uuid, check.Fail, "", at))
	must(t, err)
	incident := f.incidents(t)[0].ID
	got := since(t, f.s, func() {
		must(t, f.s.Sent(ctx, Pending{ID: "none", WorkspaceID: f.ws, PersonID: dan, Kind: "alert", ChannelKind: "webpush", Items: []Item{{IncidentID: incident, What: "down", Title: "nightly is down"}}}, at))
		must(t, f.s.Sent(ctx, Pending{ID: "none", WorkspaceID: f.ws, PersonID: f.editor.PersonID, Kind: "alert", ChannelKind: "webpush", Items: []Item{{IncidentID: incident, What: "down", Title: "nightly is down"}}}, at))
	})
	if got[0].PersonName != "Dan" || got[0].PersonOutsider != 1 || got[0].IncidentID != incident || got[0].AboutName != "nightly" || got[0].ProjectID != f.project {
		t.Errorf("%+v", got[0])
	}
	if got[1].PersonOutsider != 0 {
		t.Errorf("%+v", got[1])
	}
}

// Why a message failed is kept in Allstill's own words: an error can hold a device's
// push address, and a server's answer anything.
func TestLogFailure(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{NoChannel, "the person has no way to be reached"},
		{`Post "https://push.example.com/send/sekret": dial tcp: lookup failed`, "it could not be sent"},
		{"the push service answered 503 Service Unavailable", "the push service refused it"},
		{"the mail server refused this address: it does not exist or takes no mail", "the mail server did not take it"},
		{"the mail server did not take it after four tries", "the mail server did not take it"},
		{"the device no longer subscribes; it was removed", "the device no longer subscribes"},
		{"email is not set up correctly on this server; its administrators see why", "this server is not set up to send it"},
		{"push is not set up on this server", "this server is not set up to send it"},
		{"the channel was removed", "the device or address changed"},
		{"the channel changed hands or must be confirmed again", "the device or address changed"},
		{"the address was refused; send it a test once it works again", "the address was refused"},
		{"the device subscribed for another key and must subscribe again", "the device must subscribe again"},
		{"anything at all", "it could not be sent"},
	} {
		if got := logFailure(tc.in); got != tc.want {
			t.Errorf("logFailure(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// A message about a check that moved after it was queued does not name its person to
// the people of the project it moved to (R25).
func TestAMessageAboutAMovedCheckNamesNobodyThere(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	at := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	other, _, err := f.s.CreateProject(ctx, Command("test"), f.ws, "Other")
	must(t, err)
	checkID, uuid := f.pingCheck(t)
	_, err = f.s.Ping(ctx, ping(uuid, check.Fail, "", at))
	must(t, err)
	incident := f.incidents(t)[0].ID
	must(t, f.s.MoveCheck(ctx, Command("test"), f.ws, checkID, other, at))
	got := since(t, f.s, func() {
		must(t, f.s.Sent(ctx, Pending{ID: "none", WorkspaceID: f.ws, PersonID: f.editor.PersonID, Kind: "digest", ChannelKind: "email", Items: []Item{{IncidentID: incident, What: "still_open", Title: "nightly is down"}}}, at))
	})
	if got[0].ProjectID != other || got[0].PersonOutsider != 1 || detailsOfEntry(t, got[0])["person_elsewhere"] != true {
		t.Errorf("%+v", got[0])
	}
}

func TestBriefLate(t *testing.T) {
	for _, tc := range []struct {
		from, to check.State
		want     bool
	}{
		{check.Up, check.Late, true},
		{check.Warning, check.Late, true},
		{check.Late, check.Up, true},
		{check.Late, check.Down, false}, // an outage
		{check.Up, check.Down, false},
		{check.Down, check.Up, false}, // a recovery
		{check.Unknown, check.Up, false},
	} {
		if got := briefLate(tc.from, tc.to); got != tc.want {
			t.Errorf("%s to %s: %v", tc.from, tc.to, got)
		}
	}
}

// A job that pings a moment after its time goes late and back on every run. That is
// written and hidden, uses up nothing of the ceiling, and a real outage still shows.
func TestAJobThatIsAlwaysALittleLate(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	at := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	c, err := f.s.CreateCheck(ctx, f.editor, f.project, NewCheck{Name: "often", Interval: time.Minute, Grace: time.Hour})
	must(t, err)
	_, err = f.s.Ping(ctx, ping(c.PingUUID, check.Success, "", at))
	must(t, err)
	last := at
	for range 100 {
		_, err := f.s.ProcessDue(ctx, last.Add(time.Minute+time.Second))
		must(t, err)
		last = last.Add(time.Minute + 2*time.Second)
		_, err = f.s.Ping(ctx, ping(c.PingUUID, check.Success, "", last))
		must(t, err)
	}
	var late, state int
	for _, e := range entries(t, f.s) {
		switch {
		case e.AboutID != c.ID:
		case e.Kind == "check.late":
			late++
			if e.Hidden != 1 {
				t.Errorf("%+v", e)
			}
		case e.Kind == "check.flapping":
			t.Errorf("late runs reached the ceiling: %+v", e)
		case e.Kind == "check.state":
			state++
		}
	}
	if late != 200 || state != 1 {
		t.Errorf("%d late, %d state", late, state)
	}
	// A real outage is written, and shows.
	got := since(t, f.s, func() {
		_, err := f.s.Ping(ctx, ping(c.PingUUID, check.Fail, "", last.Add(time.Second)))
		must(t, err)
	})
	if !slices.Equal(kinds(got), []string{"check.state"}) || got[0].Hidden != 0 {
		t.Errorf("%+v", got)
	}
}

// A check past its ceiling that then only goes a little late on every run has settled,
// though no state of it lasts an hour; one that keeps failing has not (R16, R17a).
func TestAFlappingCheckThatIsOnlyLateSettles(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	at := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	checkID, uuid := f.pingCheck(t)
	for i := range 40 {
		kind := check.Fail
		if i%2 == 1 {
			kind = check.Success
		}
		_, err := f.s.Ping(ctx, ping(uuid, kind, "", at.Add(time.Duration(i)*time.Second)))
		must(t, err)
	}
	// Still with its incident open, it has not settled, whatever its state.
	got := since(t, f.s, func() { must(t, f.s.closeFlapping(ctx, at.Add(5*time.Minute))) })
	if len(got) != 0 {
		t.Fatalf("%v", kinds(got))
	}
	// Its incident closes, then the job only goes late and back.
	for {
		n, err := f.s.SettleIncidents(ctx, at.Add(20*time.Minute), 10*time.Minute)
		must(t, err)
		if n == 0 {
			break
		}
	}
	if len(f.incidents(t)) == 0 || f.incidents(t)[len(f.incidents(t))-1].State != "resolved" {
		t.Fatalf("%+v", f.incidents(t))
	}
	last := at.Add(20 * time.Minute)
	_, err := f.s.Ping(ctx, ping(uuid, check.Success, "", last))
	must(t, err)
	// An hour after it closed, with a change of state moments ago.
	_, err = f.s.w.ExecContext(ctx, `UPDATE checks SET state = 'late', state_since = ? WHERE id = ?`, at.Add(90*time.Minute).UnixMilli(), checkID)
	must(t, err)
	got = since(t, f.s, func() { must(t, f.s.closeFlapping(ctx, at.Add(91*time.Minute))) })
	if !slices.Equal(kinds(got), []string{"check.steady"}) || detailsOfEntry(t, got[0])["state"] != "late" {
		t.Fatalf("%+v", got)
	}
	// So a real outage is written again.
	got = since(t, f.s, func() {
		_, err := f.s.Ping(ctx, ping(uuid, check.Fail, "", at.Add(92*time.Minute)))
		must(t, err)
	})
	if !slices.Equal(kinds(got), []string{"check.state"}) {
		t.Errorf("%v", kinds(got))
	}
}

// A check Allstill runs can flip between up and a warning with no incident at all. Being
// up with no incident does not make it steady: it must hold its state (R16).
func TestAFlappingCheckWithoutIncidentsHasNotSettled(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	at := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	id := f.httpCheck(t)
	var when time.Time
	for i := range 40 {
		when = at.Add(time.Duration(i) * 30 * time.Second)
		outcome := check.Warned
		if i%2 == 1 {
			outcome = check.Passed
		}
		_, err := f.s.RecordAttempt(ctx, f.ws, id, Attempt{Attempt: check.Attempt{Outcome: outcome, Reason: "certificate expires in 3 days", At: when}})
		must(t, err)
	}
	if c := f.get(t, id); c.State != string(check.Up) {
		t.Fatalf("%s", c.State)
	}
	if got := since(t, f.s, func() { must(t, f.s.closeFlapping(ctx, when.Add(time.Minute))) }); len(got) != 0 {
		t.Errorf("settled while flipping: %v", kinds(got))
	}
	got := since(t, f.s, func() { must(t, f.s.closeFlapping(ctx, when.Add(2*time.Hour))) })
	if !slices.Equal(kinds(got), []string{"check.steady"}) {
		t.Errorf("%v", kinds(got))
	}
}

// The ceiling's lookup reads only what it needs, however many late lines a check has.
func TestTheCeilingUsesItsIndex(t *testing.T) {
	s := open(t)
	rows, err := s.r.QueryContext(t.Context(), `EXPLAIN QUERY PLAN SELECT recorded_at FROM log_entries
		WHERE about_id = 'x' AND kind <> 'check.late' AND +kind = 'check.state' ORDER BY id DESC LIMIT 1 OFFSET 29`)
	must(t, err)
	defer rows.Close()
	var text string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		must(t, rows.Scan(&id, &parent, &unused, &detail))
		text += detail
	}
	if !strings.Contains(text, "log_entries_about") || strings.Contains(text, "TEMP B-TREE") {
		t.Errorf("%s", text)
	}
}
