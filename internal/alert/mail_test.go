// SPDX-License-Identifier: AGPL-3.0-only

package alert

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/MartijnMul/allstill/internal/check"
	"github.com/MartijnMul/allstill/internal/email"
	"github.com/MartijnMul/allstill/internal/store"
)

// mailer is a fake mail server: it answers with outcome, and keeps what it
// was sent.
type mailer struct {
	outcome email.Outcome
	sent    []email.Message
	tries   int
}

func (m *mailer) Send(_ context.Context, msg email.Message, _ time.Time) (email.Outcome, error) {
	m.tries++
	if m.outcome != email.Sent {
		return m.outcome, errors.New("the server said no")
	}
	m.sent = append(m.sent, msg)
	return email.Sent, nil
}

func (m *mailer) subjects() string {
	var out []string
	for _, msg := range m.sent {
		out = append(out, msg.Subject)
	}
	return strings.Join(out, "|")
}

var confirmLink = regexp.MustCompile(`https://allstill\.example\.com/channels/(\S+)/confirm\?token=(\S+)`)

// withEmail gives the administrator a confirmed email address.
func (w *world) withEmail(m *mailer) string {
	w.t.Helper()
	ctx := w.t.Context()
	w.sender.Mail, w.sender.Base = m, "https://allstill.example.com"
	c, err := w.st.SaveEmail(ctx, w.ws, w.admin, "ada@example.com")
	if err != nil {
		w.t.Fatal(err)
	}
	if err := w.st.Test(ctx, w.admin, c.ID, w.now); err != nil {
		w.t.Fatal(err)
	}
	if err := w.sender.Pass(ctx); err != nil {
		w.t.Fatal(err)
	}
	link := confirmLink.FindStringSubmatch(m.sent[len(m.sent)-1].Body)
	if link == nil || link[1] != c.ID {
		w.t.Fatalf("no confirm link in %q", m.sent[len(m.sent)-1].Body)
	}
	token, _ := url.QueryUnescape(link[2])
	if err := w.st.Confirm(ctx, w.admin, c.ID, token, w.now); err != nil {
		w.t.Fatal(err)
	}
	return c.ID
}

func TestAnAlertByEmailWithoutADevice(t *testing.T) {
	w := newWorld(t)
	alertsOn(w)
	m := &mailer{}
	backup := w.check("Shop", "backup")
	w.set("11:00")
	w.withEmail(m)
	w.at("12:00", backup, check.Fail)
	w.at("12:30", backup, check.Success)
	w.at("12:41", "", "")
	if got := m.subjects(); got != "Allstill test message|backup is down|Resolved: backup is down" {
		t.Fatalf("emailed %s", got)
	}
	down := m.sent[1]
	if down.To != "ada@example.com" || !strings.Contains(down.Body, "https://allstill.example.com/messages") || down.ID == "" {
		t.Errorf("the alert: %+v", down)
	}
	w.want("test sent: ", "alert sent: down backup is down", "alert sent: recovered backup is down")
}

// A push that no device took goes by email, as the same message.
func TestAPushThatFailsGoesByEmail(t *testing.T) {
	w := newWorld(t)
	alertsOn(w)
	d, m := newDevice(t), &mailer{}
	backup := w.check("Shop", "backup")
	w.set("11:00")
	w.confirm(d, w.withPush(d, ""))
	w.withEmail(m)
	d.answer(http.StatusForbidden) // refused for good
	w.at("12:00", backup, check.Fail)
	w.at("12:00", "", "") // the email follows at the next pass
	if got := m.subjects(); got != "Allstill test message|backup is down" {
		t.Fatalf("emailed %s", got)
	}
	// One message: its time is the alert's, so the gather period holds.
	last, _ := w.st.LastMessageAt(t.Context(), w.admin)
	if !last.Equal(w.now) {
		t.Errorf("last message at %v, want %v", last, w.now)
	}
	w.want("test sent: ", "test sent: ", "alert failed: down backup is down", "alert sent: down backup is down")
}

// The digest goes by email, whole; when the address is refused, as a short
// push.
func TestTheDigestByEmailAndBack(t *testing.T) {
	w := newWorld(t)
	alertsOn(w)
	d, m := newDevice(t), &mailer{}
	backup := w.check("Shop", "backup")
	w.set("11:00")
	w.confirm(d, w.withPush(d, ""))
	channel := w.withEmail(m)
	w.at("12:00", backup, check.Fail) // pushed
	w.at("22 08:00", "", "")
	digest := m.sent[len(m.sent)-1]
	if digest.Subject != "Allstill digest: 1 still open" || !strings.Contains(digest.Body, "Still open\n- backup is down\n") {
		t.Fatalf("digest %q\n%s", digest.Subject, digest.Body)
	}
	// The next day the address is refused for good.
	m.outcome = email.Rejected
	w.at("23 08:00", "", "")
	w.at("23 08:00", "", "") // the push follows at the next pass
	if got := strings.Join(d.titles(), "|"); got != "Allstill test message|backup is down|Allstill digest" {
		t.Errorf("pushed %s", got)
	}
	cs, _ := w.st.Channels(t.Context(), w.admin)
	for _, c := range cs {
		if c.ID == channel && c.FailedAt.IsZero() {
			t.Error("a refused address is not marked as failing")
		}
	}
}

// A message that reached no channel leaves its down unheard, and it is
// decided again later.
func TestAMessageThatReachedNobody(t *testing.T) {
	w := newWorld(t)
	alertsOn(w)
	m := &mailer{}
	backup := w.check("Shop", "backup")
	w.set("11:00")
	w.withEmail(m)
	m.outcome = email.Failed
	w.at("12:00", backup, check.Fail)
	if p := w.sender.MailProblem(); p == nil || !strings.Contains(p.Error, "said no") {
		t.Fatalf("the settings problem is not kept: %+v", p)
	}
	// The address itself is not to blame.
	if cs, _ := w.st.Channels(t.Context(), w.admin); !cs[0].FailedAt.IsZero() {
		t.Error("the address was marked for a problem of the settings")
	}
	// Mended within ten minutes: the down goes out then, and its recovery
	// follows it.
	m.outcome = email.Sent
	w.at("12:05", "", "")
	w.at("12:10", "", "")
	w.at("12:30", backup, check.Success)
	w.at("12:41", "", "")
	if got := m.subjects(); got != "Allstill test message|backup is down|Resolved: backup is down" {
		t.Fatalf("emailed %s", got)
	}
	if w.sender.MailProblem() != nil {
		t.Error("the settings problem did not clear once email worked")
	}
}

// An email that fails for now is tried after 1, 5 and 30 minutes; while the
// server is out of reach, the others wait.
func TestAnEmailThatFailsForNow(t *testing.T) {
	w := newWorld(t)
	alertsOn(w)
	m := &mailer{}
	a, b := w.check("Shop", "a"), w.check("Shop", "b")
	w.set("11:00")
	w.withEmail(m)
	m.outcome = email.Retry
	w.at("12:00", a, check.Fail)
	tries := m.tries // the test, and one try
	w.at("12:03", b, check.Fail)
	if m.tries != tries+1 {
		t.Errorf("%d tries at 12:03, want one more: the other waits", m.tries-tries)
	}
	for _, at := range []string{"12:06", "12:30", "13:10", "13:50", "14:30"} {
		w.at(at, "", "")
	}
	msgs, _ := w.st.Messages(t.Context(), w.admin, 10)
	failed := 0
	for _, msg := range msgs {
		if msg.State == "failed" && msg.Kind == "alert" {
			failed++
		}
	}
	if failed == 0 {
		t.Errorf("no alert failed after its last try: %+v", msgs)
	}
}

func TestEmailLimits(t *testing.T) {
	w := newWorld(t)
	ctx := t.Context()
	var ids []string
	for _, a := range []string{"a@example.com", "b@example.com", "c@example.com"} {
		c, err := w.st.SaveEmail(ctx, w.ws, w.admin, a)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, c.ID)
	}
	if c, err := w.st.SaveEmail(ctx, w.ws, w.admin, "A@example.com"); err != nil || c.ID != ids[0] {
		t.Errorf("the same address again: %v %+v", err, c)
	}
	var limit store.LimitError
	if _, err := w.st.SaveEmail(ctx, w.ws, w.admin, "d@example.com"); !errors.As(err, &limit) {
		t.Errorf("a fourth address: %v", err)
	}
	w.set("11:00")
	if err := w.st.Test(ctx, w.admin, ids[0], w.now); err != nil {
		t.Fatal(err)
	}
	if err := w.st.Test(ctx, w.admin, ids[0], w.now.Add(30*time.Second)); !errors.As(err, &limit) {
		t.Errorf("a second test within a minute: %v", err)
	}
	at := w.now
	for i := 1; i < store.TestsPerDay; i++ {
		at = at.Add(time.Minute)
		if err := w.st.Test(ctx, w.admin, ids[i%3], at); err != nil {
			t.Fatalf("test %d: %v", i+1, err)
		}
	}
	if err := w.st.Test(ctx, w.admin, ids[1], at.Add(time.Hour)); !errors.As(err, &limit) {
		t.Errorf("an eleventh test in a day: %v", err)
	}
}

// A recovery waits while its down is on its way, also through the email's
// tries; a down that never went out has no recovery (R6).
func TestARecoveryWaitsForItsDown(t *testing.T) {
	w := newWorld(t)
	alertsOn(w)
	m := &mailer{}
	backup := w.check("Shop", "backup")
	w.set("11:00")
	w.withEmail(m)
	m.outcome = email.Retry
	w.at("12:00", backup, check.Fail) // first try; the next at 12:01
	w.at("12:01", "", "")             // second; the next at 12:06
	w.at("12:02", backup, check.Success)
	w.at("12:06", "", "") // third; the next at 12:36
	// Resolved at 12:12, while the down waits for 12:36: the recovery
	// waits too.
	for _, at := range []string{"12:13", "12:20", "12:30"} {
		w.at(at, "", "")
	}
	m.outcome = email.Sent
	w.at("12:36", "", "") // the down is no longer true, so never heard
	for _, at := range []string{"13:00", "13:30", "14:00"} {
		w.at(at, "", "")
	}
	if got := m.subjects(); got != "Allstill test message" {
		t.Errorf("emailed %s: no recovery of a down that never went out, and no stale down", got)
	}
}

// A delivery whose channel was removed while it waited falls back as any
// other that failed.
func TestARemovedChannelFallsBack(t *testing.T) {
	w := newWorld(t)
	alertsOn(w)
	d, m := newDevice(t), &mailer{}
	backup := w.check("Shop", "backup")
	w.set("11:00")
	device := w.withPush(d, "")
	w.confirm(d, device)
	w.withEmail(m)
	d.answer(http.StatusServiceUnavailable)
	w.at("12:00", backup, check.Fail) // waits for its second try
	if err := w.st.DeleteChannel(t.Context(), w.admin, device); err != nil {
		t.Fatal(err)
	}
	w.at("12:01", "", "")
	w.at("12:01", "", "")
	if got := m.subjects(); got != "Allstill test message|backup is down" {
		t.Errorf("emailed %s", got)
	}
}

// What a person sees of a failure of the server's settings names no user
// or host; administrators see that on the overview.
func TestASettingsFailureIsNotShownToEveryone(t *testing.T) {
	w := newWorld(t)
	alertsOn(w)
	m := &mailer{}
	backup := w.check("Shop", "backup")
	w.set("11:00")
	w.withEmail(m)
	m.outcome = email.Failed
	w.at("12:00", backup, check.Fail)
	msgs, _ := w.st.Messages(t.Context(), w.admin, 10)
	for _, msg := range msgs {
		if strings.Contains(msg.Error, "said no") {
			t.Errorf("the server's answer reached Messages: %q", msg.Error)
		}
	}
}

func TestALongAddress(t *testing.T) {
	w := newWorld(t)
	long := strings.Repeat("a", 150) + "@example.com"
	c, err := w.st.SaveEmail(t.Context(), w.ws, w.admin, long)
	if err != nil || c.Address != long {
		t.Fatalf("%v %+v", err, c)
	}
}

// A push that fails while the server cannot email queues nothing; its down
// is unheard and decided again later.
func TestAFallbackOnlyGoesAWayThatWorks(t *testing.T) {
	w := newWorld(t)
	alertsOn(w)
	d, m := newDevice(t), &mailer{}
	backup := w.check("Shop", "backup")
	w.set("11:00")
	w.confirm(d, w.withPush(d, ""))
	w.withEmail(m)
	w.sender.Mail = nil // email was switched off since the address was added
	d.answer(http.StatusForbidden)
	w.at("12:00", backup, check.Fail)
	w.at("12:00", "", "")
	w.want("test sent: ", "test sent: ", "alert failed: down backup is down")
	if m.tries != 1 {
		t.Errorf("%d emails tried, want only the earlier test", m.tries)
	}
	pending, _ := w.st.PendingDowns(t.Context(), w.admin)
	if len(pending) != 0 {
		t.Errorf("a down still waits: %v", pending)
	}
}

// The digest lists checks in warning, such as a certificate running out
// (intent/http-checks/spec.md, R9), and tells nothing for them.
func TestTheDigestListsWarnings(t *testing.T) {
	items, told := DigestItems(nil, []store.ListedCheck{
		{Name: "web", Project: "Shop", State: "new"},
		{Name: "shop", Project: "Sites", State: "warning", Reason: "certificate expires in 12 days"},
	}, false)
	if len(items) != 2 || items[1].What != "warning" || items[1].Title != "Sites: shop (certificate expires in 12 days)" || len(told) != 0 {
		t.Fatalf("%+v, told %+v", items, told)
	}
	subject, body := (&Sender{Base: "https://allstill.example.com"}).compose(store.Pending{Kind: "digest", Items: items}, "")
	if subject != "Allstill digest: 1 check that cannot alert, 1 warning" || !strings.Contains(body, "Warnings\n- Sites: shop (certificate expires in 12 days)\n") {
		t.Errorf("%q\n%s", subject, body)
	}
}
