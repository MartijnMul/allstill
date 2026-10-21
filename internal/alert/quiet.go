// SPDX-License-Identifier: AGPL-3.0-only

// Package alert decides what a person is told about an incident, and when,
// and records it: the sender of intent/alerts/spec.md. It has no HTTP code;
// the store holds the incidents and what each person was told.
package alert

import (
	"encoding/json"
	"fmt"
	"slices"
	"time"
)

// QuietHours is when a person may not be disturbed, except by a critical
// check: the API's QuietHours. A period that crosses midnight belongs to the
// day it starts.
type QuietHours struct {
	TimeZone string   `json:"time_zone"`
	Periods  []Period `json:"periods"`
	loc      *time.Location
}

// Period is one stretch of quiet on the days it starts.
type Period struct {
	Days  []string `json:"days"`
	Start string   `json:"start"`
	End   string   `json:"end"`
}

var weekdays = []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}

// ParseQuietHours reads quiet hours as stored. Empty means none. A missing
// time zone is the person's.
func ParseQuietHours(raw []byte, personZone string) (QuietHours, error) {
	var q QuietHours
	if len(raw) == 0 {
		return q, nil
	}
	if err := json.Unmarshal(raw, &q); err != nil {
		return q, fmt.Errorf("quiet hours: %w", err)
	}
	if q.TimeZone == "" {
		q.TimeZone = personZone
	}
	loc, err := time.LoadLocation(q.TimeZone)
	if err != nil {
		return q, fmt.Errorf("quiet hours: %w", err)
	}
	q.loc = loc
	for _, p := range q.Periods {
		if _, err := clock(p.Start); err != nil {
			return q, err
		}
		if _, err := clock(p.End); err != nil {
			return q, err
		}
		for _, d := range p.Days {
			if !slices.Contains(weekdays, d) {
				return q, fmt.Errorf("quiet hours: unknown day %q", d)
			}
		}
	}
	return q, nil
}

// clock reads HH:MM as minutes after midnight.
func clock(s string) (int, error) {
	var h, m int
	if _, err := fmt.Sscanf(s, "%2d:%2d", &h, &m); err != nil || len(s) != 5 || h > 23 || m > 59 {
		return 0, fmt.Errorf("%q is not a time as HH:MM", s)
	}
	return h*60 + m, nil
}

// at is the moment of minutes after midnight on day's date in loc. A time
// that does not exist, in the gap when clocks go forward, moves on by the
// length of the gap, as time.Date does: 02:30 becomes 03:30.
func at(day time.Time, minutes int, loc *time.Location) time.Time {
	y, mo, d := day.Date()
	return time.Date(y, mo, d, minutes/60, minutes%60, 0, 0, loc)
}

// Until returns the end of the quiet period t falls in, and false when t is
// not quiet.
func (q QuietHours) Until(t time.Time) (time.Time, bool) {
	if q.loc == nil {
		return time.Time{}, false
	}
	local := t.In(q.loc)
	var end time.Time
	// A period that started yesterday may still run.
	for _, back := range []int{1, 0} {
		day := local.AddDate(0, 0, -back)
		name := weekdays[day.Weekday()]
		for _, p := range q.Periods {
			if !slices.Contains(p.Days, name) {
				continue
			}
			s, _ := clock(p.Start)
			e, _ := clock(p.End)
			from := at(day, s, q.loc)
			to := at(day, e, q.loc)
			if e <= s {
				to = at(day.AddDate(0, 0, 1), e, q.loc)
			}
			if !t.Before(from) && t.Before(to) && to.After(end) {
				end = to
			}
		}
	}
	return end, !end.IsZero()
}

// NextDigest returns the first digest time after after, at HH:MM in the
// person's time zone.
func NextDigest(after time.Time, digestTime, zone string) (time.Time, error) {
	minutes, err := clock(digestTime)
	if err != nil {
		return time.Time{}, err
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return time.Time{}, err
	}
	local := after.In(loc)
	for back := 0; back < 3; back++ {
		if t := at(local.AddDate(0, 0, back), minutes, loc); t.After(after) {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("no digest time after %s", after)
}
