// SPDX-License-Identifier: AGPL-3.0-only

package schedule

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func utc(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

// In Europe/Amsterdam in 2026 the clocks jump from 02:00 to 03:00 on 29 March
// (01:00 UTC) and fall back from 03:00 to 02:00 on 25 October (01:00 UTC).
func TestNext(t *testing.T) {
	cases := []struct {
		name, expr, zone, after string
		want                    []string // successive runs
	}{
		{"daily", "0 3 * * *", "UTC", "2026-10-21T10:00:00Z",
			[]string{"2026-10-22T03:00:00Z", "2026-10-23T03:00:00Z"}},
		{"every minute", "* * * * *", "UTC", "2026-10-21T10:00:30Z",
			[]string{"2026-10-21T10:01:00Z", "2026-10-21T10:02:00Z"}},
		{"in a time zone", "0 9 * * mon-fri", "Europe/Amsterdam", "2026-10-23T08:00:00Z",
			[]string{"2026-10-26T08:00:00Z"}},
		{"names and 7 for Sunday", "0 9 * jan-mar 7", "UTC", "2026-10-21T00:00:00Z",
			[]string{"2027-01-03T09:00:00Z"}},
		{"either day field", "0 0 1 * mon", "UTC", "2026-10-21T00:00:00Z",
			[]string{"2026-10-26T00:00:00Z", "2026-11-01T00:00:00Z", "2026-11-02T00:00:00Z"}},
		{"both day fields when one starts with *", "0 0 */10 * mon", "UTC", "2026-10-21T00:00:00Z",
			[]string{"2026-12-21T00:00:00Z"}},
		{"last day of the month", "0 0 L * *", "UTC", "2028-02-01T00:00:00Z",
			[]string{"2028-02-29T00:00:00Z", "2028-03-31T00:00:00Z"}},
		{"last Friday", "0 0 * * 5L", "UTC", "2026-10-21T00:00:00Z",
			[]string{"2026-10-30T00:00:00Z", "2026-11-27T00:00:00Z"}},
		{"second Friday", "0 0 * * fri#2", "UTC", "2026-10-21T00:00:00Z",
			[]string{"2026-11-13T00:00:00Z"}},
		{"a step larger than its field", "*/90 * * * *", "UTC", "2026-10-21T10:00:30Z",
			[]string{"2026-10-21T11:00:00Z", "2026-10-21T12:00:00Z"}},
		{"last weekday of the month", "0 0 LW * *", "UTC", "2026-10-01T00:00:00Z",
			[]string{"2026-10-30T00:00:00Z", "2026-11-30T00:00:00Z", "2026-12-31T00:00:00Z", "2027-01-29T00:00:00Z"}},
		{"a range with a step", "5-5/2 * * * *", "UTC", "2026-10-21T10:00:00Z",
			[]string{"2026-10-21T10:05:00Z", "2026-10-21T11:05:00Z"}},
		{"midnight skipped in Santiago", "0 0 * * *", "America/Santiago", "2026-09-05T12:00:00Z",
			[]string{"2026-09-06T04:00:00Z", "2026-09-07T03:00:00Z"}},
		{"a step from a value", "10/20 * * * *", "UTC", "2026-10-21T10:00:00Z",
			[]string{"2026-10-21T10:10:00Z", "2026-10-21T10:30:00Z", "2026-10-21T10:50:00Z", "2026-10-21T11:10:00Z"}},

		{"fixed time in the skipped hour runs at the jump", "30 2 * * *", "Europe/Amsterdam", "2026-03-28T12:00:00Z",
			[]string{"2026-03-29T01:00:00Z", "2026-03-30T00:30:00Z"}},
		{"the clock skips the hour for other jobs", "*/30 * * * *", "Europe/Amsterdam", "2026-03-29T00:20:00Z",
			[]string{"2026-03-29T00:30:00Z", "2026-03-29T01:00:00Z", "2026-03-29T01:30:00Z"}},
		{"fixed time in the repeated hour runs once", "30 2 * * *", "Europe/Amsterdam", "2026-10-24T12:00:00Z",
			[]string{"2026-10-25T00:30:00Z", "2026-10-26T01:30:00Z"}},
		{"other jobs run in both copies of the repeated hour", "*/30 * * * *", "Europe/Amsterdam", "2026-10-24T23:50:00Z",
			[]string{"2026-10-25T00:00:00Z", "2026-10-25T00:30:00Z", "2026-10-25T01:00:00Z",
				"2026-10-25T01:30:00Z", "2026-10-25T02:00:00Z"}},
		{"from inside the first copy", "*/30 * * * *", "Europe/Amsterdam", "2026-10-25T00:20:00Z",
			[]string{"2026-10-25T00:30:00Z"}},
		{"an hourly job in the repeated hour", "15 * * * *", "Europe/Amsterdam", "2026-10-24T23:30:00Z",
			[]string{"2026-10-25T00:15:00Z", "2026-10-25T01:15:00Z", "2026-10-25T02:15:00Z"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, err := ParseCron(tc.expr, tc.zone)
			if err != nil {
				t.Fatal(err)
			}
			at := utc(tc.after)
			for i, w := range tc.want {
				got, err := c.Next(at)
				if err != nil {
					t.Fatalf("run %d: %v", i, err)
				}
				if !got.Equal(utc(w)) {
					t.Fatalf("run %d after %s: got %s, want %s", i, at.UTC().Format(time.RFC3339), got.UTC().Format(time.RFC3339), w)
				}
				at = got
			}
		})
	}
}

func TestNever(t *testing.T) {
	if _, err := ParseCron("0 0 30 2 *", "UTC"); !errors.Is(err, ErrNever) {
		t.Fatalf("30 February: %v", err)
	}
	// 29 February on a Sunday: 2088, then 2128.
	c, err := ParseCron("0 0 29 2 */7", "UTC")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := c.Next(utc("2088-03-01T00:00:00Z")); err != nil || !got.Equal(utc("2128-02-29T00:00:00Z")) {
		t.Fatalf("%s, %v", got, err)
	}
}

func TestParseErrors(t *testing.T) {
	for _, tc := range []struct{ expr, zone string }{
		{"60 * * * *", "UTC"},
		{"* 24 * * *", "UTC"},
		{"* * 0 * *", "UTC"},
		{"* * * 13 *", "UTC"},
		{"* * * * 8", "UTC"},
		{"* * * *", "UTC"},
		{"* * * * * *", "UTC"},
		{"5-1 * * * *", "UTC"},
		{"*/0 * * * *", "UTC"},
		{"+5 * * * *", "UTC"},
		{"* * * * fri#6", "UTC"},
		{"* * * * *", "Mars/Olympus"},
		{"* * * * *", ""},
		{"* * * * *", "Local"},
	} {
		if _, err := ParseCron(tc.expr, tc.zone); err == nil {
			t.Errorf("ParseCron(%q, %q) accepted", tc.expr, tc.zone)
		}
	}
}

func FuzzParseCron(f *testing.F) {
	for _, s := range []string{"0 3 * * *", "*/15 * * * mon-fri", "0 0 L * 5L", "1-5/2 2 * jan fri#2"} {
		f.Add(s)
	}
	after := utc("2026-10-25T00:20:00Z")
	f.Fuzz(func(t *testing.T, expr string) {
		c, err := ParseCron(expr, "Europe/Amsterdam")
		if err != nil {
			return
		}
		next, err := c.Next(after)
		if err == nil && !next.After(after) {
			t.Fatalf("%q: next %s is not after %s", expr, next, after)
		}
	})
}

// cronsim's own answers, from testdata/gen_cronsim.py. Where it differs, it is
// cronsim that is wrong, and the case says how.
var cronsimDiffers = map[string]string{
	// The start is the very instant the clocks fall back; cronsim answers with
	// a time before the start.
	"0 3 * * *|Pacific/Chatham|2026-04-04T14:00:00Z":       "answers before the start",
	"0 2-3 * * *|Pacific/Chatham|2026-04-04T14:00:00Z":     "answers before the start",
	"0 1,2,3 * * *|Pacific/Chatham|2026-04-04T14:00:00Z":   "answers before the start",
	"1-59/15 2 * * *|Pacific/Chatham|2026-04-04T14:00:00Z": "answers before the start",
	// Lord Howe falls back by half an hour; cronsim skips 02:00 to 02:29,
	// which happen once.
	"0 */2 * * *|Australia/Lord_Howe|2026-04-04T14:00:00Z": "skips 02:00 after a half-hour change",
	"*/7 2 * * *|Australia/Lord_Howe|2026-04-04T14:00:00Z": "skips 02:00 after a half-hour change",
	"* 2 * * *|Australia/Lord_Howe|2026-04-04T14:00:00Z":   "skips 02:00 after a half-hour change",
}

func TestAgreesWithCronsim(t *testing.T) {
	b, err := os.ReadFile("testdata/cronsim.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Expr, Zone, After string
		Want              []string
	}
	if err := json.Unmarshal(b, &cases); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, tc := range cases {
		key := tc.Expr + "|" + tc.Zone + "|" + tc.After
		c, err := ParseCron(tc.Expr, tc.Zone)
		if err != nil {
			t.Errorf("%s: %v", key, err)
			continue
		}
		at, mismatch := utc(tc.After), ""
		for i, w := range tc.Want {
			got, err := c.Next(at)
			if strings.HasPrefix(w, "ERR") {
				if err == nil {
					mismatch = fmt.Sprintf("run %d: got %s, cronsim never matches", i, got.UTC().Format(time.RFC3339))
				}
				break
			}
			if err != nil || !got.Equal(utc(w)) {
				mismatch = fmt.Sprintf("run %d: got %s (%v), cronsim %s", i, got.UTC().Format(time.RFC3339), err, w)
				break
			}
			at = got
		}
		if _, known := cronsimDiffers[key]; known {
			seen[key] = true
			if mismatch == "" {
				t.Errorf("%s: listed as a difference, but agrees", key)
			}
		} else if mismatch != "" {
			t.Errorf("%s: %s", key, mismatch)
		}
	}
	for key := range cronsimDiffers {
		if !seen[key] {
			t.Errorf("%s: listed as a difference, but not in the data", key)
		}
	}
}
