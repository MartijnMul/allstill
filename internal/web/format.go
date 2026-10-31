// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"fmt"
	"html/template"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/MartijnMul/allstill/internal/check"
)

var funcs = template.FuncMap{
	// initial is the first letter of a name, for the person's avatar.
	"initial": func(name string) string {
		for _, r := range strings.TrimSpace(name) {
			return strings.ToUpper(string(r))
		}
		return "?"
	},
}

// look is how a state shows: a label, and a class that gives it a colour and
// a shape.
type look struct{ Label, Class string }

func lookOf(st check.State) look {
	switch st {
	case check.Up:
		return look{"Up", "up"}
	case check.Late:
		return look{"Late", "warn"}
	case check.Warning:
		return look{"Warning", "warn"}
	case check.Down:
		return look{"Down", "bad"}
	case check.Paused:
		return look{"Paused", "paused"}
	case check.Unknown:
		return look{"Unknown", "quiet"}
	}
	return look{"New", "quiet"}
}

// needsAttention says whether a state asks someone to act.
func needsAttention(st check.State) bool {
	return st == check.Down || st == check.Late || st == check.Warning
}

func outcomeLook(outcome string) look {
	switch outcome {
	case "success":
		return look{"OK", "up"}
	case "warning":
		return look{"Warning", "warn"}
	case "fail":
		return look{"Failed", "bad"}
	case "timeout":
		return look{"Timed out", "bad"}
	}
	return look{"Running", "quiet"}
}

// span says how long a duration is, to the precision a person needs.
func span(d time.Duration) string {
	d = d.Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d s", int(d.Seconds()))
	case d < 10*time.Minute:
		m, s := int(d.Minutes()), int(d.Seconds())%60
		if s == 0 {
			return fmt.Sprintf("%d min", m)
		}
		return fmt.Sprintf("%d min %d s", m, s)
	case d < time.Hour:
		return fmt.Sprintf("%d min", int(d.Minutes()))
	case d < 48*time.Hour:
		h, m := int(d.Hours()), int(d.Minutes())%60
		if m == 0 {
			return fmt.Sprintf("%d h", h)
		}
		return fmt.Sprintf("%d h %d min", h, m)
	}
	days, h := int(d.Hours())/24, int(d.Hours())%24
	if h == 0 {
		return fmt.Sprintf("%d days", days)
	}
	return fmt.Sprintf("%d days %d h", days, h)
}

// every says how often an interval is.
func every(d time.Duration) string {
	switch {
	case d == time.Hour:
		return "every hour"
	case d == 24*time.Hour:
		return "every day"
	case d == 7*24*time.Hour:
		return "every week"
	}
	return "every " + span(d)
}

// ago says how long ago t was.
func ago(now, t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := now.Sub(t)
	if d < 5*time.Second {
		return "just now"
	}
	if d >= 48*time.Hour {
		return fmt.Sprintf("%d days ago", int(d.Hours())/24)
	}
	d = d.Truncate(time.Second)
	if d >= time.Hour {
		d = d.Truncate(time.Minute)
	}
	if d >= 10*time.Minute {
		d = d.Truncate(time.Minute)
	}
	return span(d) + " ago"
}

// clock says when t is, as briefly as is clear from now: a time today, a
// weekday this week, a date otherwise.
func clock(now, t time.Time, loc *time.Location) string {
	if t.IsZero() {
		return "—"
	}
	t, now = t.In(loc), now.In(loc)
	y1, m1, d1 := t.Date()
	y2, m2, d2 := now.Date()
	switch {
	case y1 == y2 && m1 == m2 && d1 == d2:
		return t.Format("15:04")
	case t.After(now.AddDate(0, 0, -6)) && t.Before(now.AddDate(0, 0, 6)):
		return t.Format("Mon 15:04")
	case y1 == y2:
		return t.Format("Mon 2 Jan 15:04")
	}
	return t.Format("2 Jan 2006 15:04")
}

// until says when a future moment comes, or that it has passed.
func until(now, t time.Time, loc *time.Location) string {
	switch {
	case t.IsZero():
		return "—"
	case !t.After(now):
		return "overdue"
	case t.Sub(now) < time.Hour:
		return "in " + span(t.Sub(now).Truncate(time.Second))
	}
	return clock(now, t, loc)
}

// number shows a measured value briefly: 18400000000 as 18.4 G. Below ten
// thousand it is shown whole, as a count of rows reads best.
func number(v float64) string {
	abs := math.Abs(v)
	for _, u := range []struct {
		from, size float64
		unit       string
	}{{1e12, 1e12, " T"}, {1e9, 1e9, " G"}, {1e6, 1e6, " M"}, {1e4, 1e3, " k"}} {
		if abs >= u.from {
			return strconv.FormatFloat(math.Round(v/u.size*10)/10, 'f', -1, 64) + u.unit
		}
	}
	return strconv.FormatFloat(math.Round(v*1000)/1000, 'f', -1, 64)
}

// values shows a run's values, in order of name.
func values(m map[string]float64) string {
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	sort.Strings(names)
	parts := make([]string, len(names))
	for i, k := range names {
		parts[i] = k + " " + number(m[k])
	}
	return strings.Join(parts, " · ")
}
