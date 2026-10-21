// SPDX-License-Identifier: AGPL-3.0-only

// Package schedule works out when a check expects its next ping.
//
// Cron expressions follow Healthchecks' cronsim (BSD-3-Clause, by Pēteris
// Caune), so checks keep their meaning when they move over:
//
//   - Five fields: minute, hour, day of month, month, day of week. Lists,
//     ranges, steps and three-letter names; 0 and 7 are both Sunday.
//   - `L` in the day of month is its last day and `LW` its last weekday; `5L`
//     in the day of week is the last Friday of the month; `5#2` is its second
//     Friday.
//   - A step may be larger than its field; `a-b/n` runs from a to b. Where
//     cronsim reads `5-5/2` as 5 to the end of the field, Allstill reads it as
//     5 alone, as Vixie cron does.
//   - An expression that can never match, such as 30 February, is refused.
//   - When both day fields are restricted, a day matching either counts; when
//     one of them starts with `*`, both must match, as in Vixie cron.
//   - Daylight saving time: a job at a fixed time (neither the minute nor the
//     hour field starts with `*`) that falls in the hour the clocks skip runs
//     when the clocks jump forward; in the hour that repeats, it runs once.
//     Other jobs follow the clock: they do not run in the skipped hour and run
//     in both copies of the repeated one.
package schedule

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Cron is a parsed cron expression in a time zone.
type Cron struct {
	minute, hour, dom, month, dow uint64 // bit n set: value n matches
	lastDom                       bool   // `L` in the day of month
	lastWeekdayDom                bool   // `LW` in the day of month
	lastDow                       uint64 // bit n: the last weekday n of the month
	nthDow                        []nth  // `n#k`
	domStar, dowStar              bool   // the field starts with `*`
	fixed                         bool
	loc                           *time.Location
}

type nth struct{ weekday, k int }

var months = map[string]int{"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6,
	"jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12}

var weekdays = map[string]int{"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6}

// ParseCron parses a five-field expression for the given IANA time zone.
func ParseCron(expr, timeZone string) (*Cron, error) {
	loc, err := time.LoadLocation(timeZone)
	if err != nil || timeZone == "" || strings.EqualFold(timeZone, "local") {
		return nil, fmt.Errorf("unknown time zone %q", timeZone)
	}
	fields := strings.Fields(strings.ToLower(expr))
	if len(fields) != 5 {
		return nil, fmt.Errorf("a cron expression has five fields, not %d", len(fields))
	}
	c := &Cron{loc: loc}
	if c.minute, err = parseField(fields[0], 0, 59, nil); err != nil {
		return nil, fmt.Errorf("minute: %w", err)
	}
	if c.hour, err = parseField(fields[1], 0, 23, nil); err != nil {
		return nil, fmt.Errorf("hour: %w", err)
	}
	if c.month, err = parseField(fields[3], 1, 12, months); err != nil {
		return nil, fmt.Errorf("month: %w", err)
	}
	if err := c.parseDom(fields[2]); err != nil {
		return nil, fmt.Errorf("day of month: %w", err)
	}
	if err := c.parseDow(fields[4]); err != nil {
		return nil, fmt.Errorf("day of week: %w", err)
	}
	c.domStar, c.dowStar = strings.HasPrefix(fields[2], "*"), strings.HasPrefix(fields[4], "*")
	c.fixed = !strings.HasPrefix(fields[0], "*") && !strings.HasPrefix(fields[1], "*")
	if _, err := c.Next(time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Cron) parseDom(f string) error {
	var plain []string
	for _, term := range strings.Split(f, ",") {
		switch term {
		case "l":
			c.lastDom = true
		case "lw":
			c.lastWeekdayDom = true
		default:
			plain = append(plain, term)
		}
	}
	if len(plain) == 0 {
		return nil
	}
	var err error
	c.dom, err = parseField(strings.Join(plain, ","), 1, 31, nil)
	return err
}

func (c *Cron) parseDow(f string) error {
	var plain []string
	for _, term := range strings.Split(f, ",") {
		switch {
		case strings.HasSuffix(term, "l") && len(term) > 1:
			d, err := value(term[:len(term)-1], 0, 7, weekdays)
			if err != nil {
				return err
			}
			c.lastDow |= 1 << (d % 7)
		case strings.Contains(term, "#"):
			ds, ks, _ := strings.Cut(term, "#")
			d, err := value(ds, 0, 7, weekdays)
			if err != nil {
				return err
			}
			k, err := strconv.Atoi(ks)
			if err != nil || k < 1 || k > 5 {
				return fmt.Errorf("%q: the week after # is 1 to 5", term)
			}
			c.nthDow = append(c.nthDow, nth{d % 7, k})
		default:
			plain = append(plain, term)
		}
	}
	if len(plain) == 0 {
		return nil
	}
	bits, err := parseField(strings.Join(plain, ","), 0, 7, weekdays)
	if bits&(1<<7) != 0 {
		bits = bits&^(1<<7) | 1
	}
	c.dow = bits
	return err
}

// parseField parses a comma-separated list of `*`, `n`, `a-b`, each with an
// optional `/step`, into a bit set.
func parseField(f string, lo, hi int, names map[string]int) (uint64, error) {
	var bits uint64
	for _, term := range strings.Split(f, ",") {
		rng, stepStr, hasStep := strings.Cut(term, "/")
		step := 1
		if hasStep {
			var err error
			step, err = strconv.Atoi(stepStr)
			if err != nil || step < 1 || strings.HasPrefix(stepStr, "+") {
				return 0, fmt.Errorf("%q: bad step", term)
			}
		}
		var a, b int
		switch {
		case rng == "*":
			a, b = lo, hi
		case strings.Contains(rng, "-"):
			as, bs, _ := strings.Cut(rng, "-")
			var err error
			if a, err = value(as, lo, hi, names); err != nil {
				return 0, err
			}
			if b, err = value(bs, lo, hi, names); err != nil {
				return 0, err
			}
			if b < a {
				return 0, fmt.Errorf("%q: the range runs backwards", term)
			}
		default:
			var err error
			if a, err = value(rng, lo, hi, names); err != nil {
				return 0, err
			}
			b = a
			if hasStep {
				b = hi
			}
		}
		for v := a; v <= b; v += step {
			bits |= 1 << v
			if step > b-v { // also keeps a huge step from overflowing
				break
			}
		}
	}
	return bits, nil
}

func value(s string, lo, hi int, names map[string]int) (int, error) {
	if v, ok := names[s]; ok {
		return v, nil
	}
	v, err := strconv.Atoi(s)
	if err != nil || v < lo || v > hi || strings.HasPrefix(s, "+") {
		return 0, fmt.Errorf("%q is not a number from %d to %d", s, lo, hi)
	}
	return v, nil
}

func (c *Cron) dayMatches(d time.Time) bool {
	daysInMonth := time.Date(d.Year(), d.Month()+1, 0, 0, 0, 0, 0, time.UTC).Day()
	day, wd := d.Day(), int(d.Weekday())
	domHit := c.dom&(1<<day) != 0 || c.lastDom && day == daysInMonth ||
		c.lastWeekdayDom && day == lastWeekday(d.Year(), d.Month(), daysInMonth)
	dowHit := c.dow&(1<<wd) != 0 || c.lastDow&(1<<wd) != 0 && day+7 > daysInMonth
	for _, n := range c.nthDow {
		if n.weekday == wd && (day-1)/7+1 == n.k {
			dowHit = true
		}
	}
	if c.domStar || c.dowStar {
		return domHit && dowHit
	}
	return domHit || dowHit
}

func lastWeekday(year int, month time.Month, days int) int {
	switch time.Date(year, month, days, 0, 0, 0, 0, time.UTC).Weekday() {
	case time.Saturday:
		return days - 1
	case time.Sunday:
		return days - 2
	}
	return days
}

// ErrNever means an expression never matches, such as 30 February.
var ErrNever = errors.New("the expression never matches")

// Next returns the first time after `after` the expression matches.
func (c *Cron) Next(after time.Time) (time.Time, error) {
	// Wall-clock times are walked as UTC values, then placed in the zone. The
	// walk starts a few hours early, so the second copy of a repeated hour is
	// not skipped.
	_, offset := after.In(c.loc).Zone()
	wall := after.Add(time.Duration(offset) * time.Second).UTC().Truncate(time.Minute).Add(-3 * time.Hour)
	// 29 February on a given weekday can be 40 years apart.
	limit := wall.AddDate(50, 0, 0)
	var best time.Time
	for wall.Before(limit) {
		w, ok := c.nextWall(wall, limit)
		if !ok {
			break
		}
		for _, t := range c.place(w) {
			if t.After(after) && (best.IsZero() || t.Before(best)) {
				best = t
				// Near a change of the clocks a later wall-clock time can be
				// an earlier instant, so look a few hours further.
				_, o1 := t.Add(-4 * time.Hour).Zone()
				_, o2 := t.In(c.loc).Add(4 * time.Hour).Zone()
				if o1 == o2 {
					return best, nil
				}
				limit = w.Add(3 * time.Hour)
			}
		}
		wall = w.Add(time.Minute)
	}
	if best.IsZero() {
		return time.Time{}, ErrNever
	}
	return best, nil
}

// nextWall returns the first wall-clock minute at or after w that matches.
func (c *Cron) nextWall(w, limit time.Time) (time.Time, bool) {
	for w.Before(limit) {
		switch {
		case c.month&(1<<int(w.Month())) == 0:
			w = time.Date(w.Year(), w.Month()+1, 1, 0, 0, 0, 0, time.UTC)
		case !c.dayMatches(w):
			w = time.Date(w.Year(), w.Month(), w.Day()+1, 0, 0, 0, 0, time.UTC)
		case c.hour&(1<<w.Hour()) == 0:
			w = w.Truncate(time.Hour).Add(time.Hour)
		case c.minute&(1<<w.Minute()) == 0:
			w = w.Add(time.Minute)
		default:
			return w, true
		}
	}
	return time.Time{}, false
}

// place returns the instants a wall-clock time stands for in the zone: none
// in a skipped hour (or, for a fixed-time job, the moment the clocks jump),
// two in a repeated hour (one for a fixed-time job), else one.
func (c *Cron) place(w time.Time) []time.Time {
	var out []time.Time
	seen := map[int]bool{}
	for _, probe := range []time.Duration{-26 * time.Hour, 0, 26 * time.Hour} {
		_, off := w.Add(probe).In(c.loc).Zone()
		if seen[off] {
			continue
		}
		seen[off] = true
		t := w.Add(-time.Duration(off) * time.Second)
		if _, o := t.In(c.loc).Zone(); o == off {
			out = append(out, t.In(c.loc))
		}
	}
	if len(out) == 0 {
		if !c.fixed {
			return nil
		}
		// In the skipped hour: run when the clocks jump. Interpreted with the
		// offset from before the jump, w lies after it; the zone that holds
		// there began at the jump.
		_, before := w.Add(-26 * time.Hour).In(c.loc).Zone()
		start, _ := w.Add(-time.Duration(before) * time.Second).In(c.loc).ZoneBounds()
		return []time.Time{start.In(c.loc)}
	}
	if len(out) == 2 {
		if out[1].Before(out[0]) {
			out[0], out[1] = out[1], out[0]
		}
		if c.fixed {
			out = out[:1]
		}
	}
	return out
}
