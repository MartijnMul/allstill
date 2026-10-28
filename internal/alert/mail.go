// SPDX-License-Identifier: AGPL-3.0-only

package alert

import (
	"context"
	"errors"
	"fmt"
	"net/textproto"
	"strings"
	"sync/atomic"
	"time"

	"github.com/MartijnMul/allstill/internal/email"
	"github.com/MartijnMul/allstill/internal/store"
)

// Mailer sends one email; *email.Config is one.
type Mailer interface {
	Send(ctx context.Context, m email.Message, now time.Time) (email.Outcome, error)
}

// mailRetries are the waits before each new try of an email that failed for
// now; after the last it fails (intent/alerts/plan.md, step 4).
var mailRetries = []time.Duration{time.Minute, 5 * time.Minute, 30 * time.Minute}

// MailProblem is the last time email failed for want of the right settings,
// and what the server said; it clears when an email goes out.
type MailProblem struct {
	At    time.Time
	Error string
}

type mailState struct{ problem atomic.Pointer[MailProblem] }

// MailProblem says whether email fails as the installation is set up, for
// administrators to see (R11). Nil when it works, or was not tried.
func (s *Sender) MailProblem() *MailProblem {
	return s.mail.problem.Load()
}

// mailOne sends one delivery by email and records how it went. It says
// whether the mail server could not be reached, so the pass's other emails
// wait.
func (s *Sender) mailOne(ctx context.Context, d store.Pending, token string, now time.Time) (down bool, err error) {
	if s.Mail == nil {
		return false, s.Store.Failed(ctx, d, "email is not set up on this server", false, false, now, s.ways())
	}
	subject, body := s.compose(d, token)
	outcome, sendErr := s.Mail.Send(ctx, email.Message{ID: d.ID, To: d.Address, Subject: subject, Body: body}, now)
	detail := ""
	if sendErr != nil {
		detail = sendErr.Error()
		s.Log.Warn("emailing a message", "delivery", d.ID, "channel", d.ChannelID, "error", detail)
	}
	// What the person sees on Messages says what happened without the
	// server's details, which may name its user and host; administrators see
	// them on the overview and in the log.
	switch outcome {
	case email.Sent:
		s.mail.problem.Store(nil)
		return false, s.Store.Sent(ctx, d.ID, now)
	case email.Retry:
		// A refusal for now names the recipient (greylisting, a full
		// mailbox); anything else means the server is out of reach, and the
		// pass's other emails wait.
		var te *textproto.Error
		down = !errors.As(sendErr, &te) || te.Code == 421 // 421: the service is not available
		if d.Attempts < len(mailRetries) {
			return down, s.Store.Retry(ctx, d.ID, "the mail server did not take it yet", now.Add(mailRetries[d.Attempts]))
		}
		return down, s.Store.Failed(ctx, d, "the mail server did not take it after four tries", false, false, now, s.ways())
	case email.Rejected:
		// The server refused this address for good: the channel fails, not
		// the installation.
		return false, s.Store.Failed(ctx, d, "the mail server refused this address: it does not exist or takes no mail", false, true, now, s.ways())
	}
	// The settings are wrong: every email fails alike, so it shows for
	// administrators instead of marking each person's address.
	s.mail.problem.Store(&MailProblem{At: now, Error: detail})
	return false, s.Store.Failed(ctx, d, "email is not set up correctly on this server; its administrators see why", false, false, now, s.ways())
}

// compose writes an email: the test with the link that confirms the
// address, an alert as its lines, or the whole digest. It carries no job
// output (C8) and no text a person chose for a test.
func (s *Sender) compose(d store.Pending, token string) (subject, body string) {
	var b strings.Builder
	switch d.Kind {
	case "test":
		subject = "Allstill test message"
		b.WriteString("This is a test message from Allstill.\n\n")
		b.WriteString("Open this link to confirm that alerts reach this address:\n")
		b.WriteString(s.Base + "/channels/" + d.ChannelID + "/confirm?token=" + token + "\n\n")
		b.WriteString("It works only when you are signed in to Allstill. If you did not ask for it, ignore it.\n")
		return subject, b.String()
	case "digest":
		subject = "Allstill digest"
		if line := digestLine(d.Items); line != "" {
			subject += ": " + line
		}
		for _, section := range []struct{ what, heading string }{
			{"down", "Down"},
			{"still_open", "Still open"},
			{"recovered", "Resolved"},
			{"resolved_unseen", "Resolved before you heard of it"},
			{"silent_check", "Cannot alert: never pinged, or its schedule cannot be read"},
		} {
			var lines []string
			for _, it := range d.Items {
				if it.What == section.what {
					lines = append(lines, "- "+oneLine(it.Title))
				}
			}
			if len(lines) > 0 {
				fmt.Fprintf(&b, "%s\n%s\n\n", section.heading, strings.Join(lines, "\n"))
			}
		}
	default:
		lines := make([]string, len(d.Items))
		for i, it := range d.Items {
			lines[i] = itemLine(it)
		}
		subject = lines[0]
		if len(lines) > 1 {
			subject = fmt.Sprintf("%d changes: %s", len(lines), lines[0])
		}
		b.WriteString(strings.Join(lines, "\n") + "\n\n")
	}
	b.WriteString("See them in Allstill: " + s.Base + "/messages\n\n")
	b.WriteString("-- \nWhere your alerts go: " + s.Base + "/channels\n")
	return subject, b.String()
}
