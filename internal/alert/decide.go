// SPDX-License-Identifier: AGPL-3.0-only

package alert

import "time"

// Told is what a person has been told about an incident.
type Told string

const (
	Nothing   Told = "nothing"
	Down      Told = "down"
	Recovered Told = "recovered"
)

// How is the way a person heard: a message at once, or the digest.
const (
	Now    = "now"
	Digest = "digest"
)

// Situation is what is true for one person and one incident at the moment
// the sender looks: the incident and the check as they are now, the person's
// settings as they are now, and what the person was told before.
type Situation struct {
	Open             bool   // open or acknowledged, not resolved
	Reason           string // why it resolved: recovered, paused or deleted
	Urgency          string // now, digest or none
	Critical         bool
	Follows          bool // the person gets alerts for a project of the incident
	RecoveryMessages bool // the project of its cause wants them
	OverWhenHeard    bool // its check came back before Allstill heard it went down, as with a probe's late runs
	Told             Told
	ToldHow          string // Now or Digest
	ToldInShadow     bool   // what they were told was only recorded
	Shadow           bool   // the installation is in shadow mode now
	Quiet            QuietHours
}

// Decision is what to send at once, if anything, and when to look again: the
// zero time when there is nothing left to do.
type Decision struct {
	Tell  Told
	Again time.Time
}

// Decide works out what a person must be told at once about an incident.
// What goes by digest is left to the digest. It compares what should be true
// for them with what they were told, so a message that waited is still right:
// a down that recovered before it was sent is never sent, settings changed
// while it waited are followed, and nobody hears "recovered" about a down
// they never heard. Nor does anyone hear "down" of an outage that was over when
// Allstill heard of it: if it settles, the digest says it happened; if the check
// goes down again, that is told.
func Decide(s Situation, now time.Time) Decision {
	if !s.Follows {
		return Decision{}
	}
	// What was told only in shadow mode was never heard: nothing follows up
	// on it once messages are really sent.
	if s.ToldInShadow && !s.Shadow {
		return Decision{}
	}
	if s.Open {
		if s.Told != Nothing || s.Urgency != Now {
			return Decision{}
		}
		if s.OverWhenHeard {
			return Decision{Again: now.Add(time.Minute)}
		}
		if end, quiet := s.Quiet.Until(now); quiet && !s.Critical {
			return Decision{Again: end}
		}
		return Decision{Tell: Down}
	}
	if s.Told != Down || s.ToldHow != Now || s.Reason != "recovered" || !s.RecoveryMessages {
		return Decision{}
	}
	// Good news can wait for the morning, even for a critical check.
	if end, quiet := s.Quiet.Until(now); quiet {
		return Decision{Again: end}
	}
	return Decision{Tell: Recovered}
}
