// SPDX-License-Identifier: AGPL-3.0-only

package compare

import (
	"slices"
	"testing"
	"time"

	"github.com/MartijnMul/allstill/internal/importer"
	"github.com/MartijnMul/allstill/internal/store"
)

var t0 = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

func at(m int) time.Time { return t0.Add(time.Duration(m) * time.Minute) }

func TestFromFlips(t *testing.T) {
	got := FromFlips([]importer.Flip{
		{At: at(0), Up: true}, // ends a period from before
		{At: at(10)}, {At: at(11)}, {At: at(20), Up: true}, {At: at(21), Up: true},
		{At: at(30)},
	})
	want := []Period{{Down: at(10), Up: at(20)}, {Down: at(30)}}
	if !slices.Equal(got, want) {
		t.Fatalf("%+v", got)
	}
}

func TestFromIncidents(t *testing.T) {
	got := FromIncidents([]store.CheckIncident{
		{Down: at(0), Resolved: at(15), Reason: "recovered"},
		{Down: at(20), Resolved: at(25), Reason: "paused"},
		{Down: at(30), RecoveringSince: at(40)},
		{Down: at(50)},
	}, 10*time.Minute)
	want := []Period{{at(0), at(5)}, {at(20), at(25)}, {at(30), at(40)}, {Down: at(50)}}
	if !slices.Equal(got, want) {
		t.Fatalf("%+v", got)
	}
}

func TestPeriods(t *testing.T) {
	const margin = 5 * time.Minute
	from, to := at(0), at(1000)
	for _, c := range []struct {
		name            string
		other, allstill []Period
		same            int
		want            []Difference
	}{
		{name: "the same", same: 1,
			other: []Period{{at(10), at(20)}}, allstill: []Period{{at(12), at(21)}}},
		{name: "never down"},
		{name: "missed",
			other: []Period{{at(10), at(40)}},
			want:  []Difference{{Kind: Missed, At: at(10), For: 30 * time.Minute}}},
		{name: "extra, still down",
			allstill: []Period{{Down: at(10)}},
			want:     []Difference{{Kind: Extra, At: at(10)}}},
		{name: "later and up apart",
			other: []Period{{at(10), at(40)}}, allstill: []Period{{at(30), at(60)}},
			want: []Difference{{Kind: Down, At: at(10), Apart: 20 * time.Minute}, {Kind: Up, At: at(40), Apart: 20 * time.Minute}}},
		{name: "flaps the other saw as several", same: 1,
			other: []Period{{at(10), at(12)}, {at(14), at(16)}, {at(18), at(20)}}, allstill: []Period{{at(10), at(20)}},
			want: []Difference{{Kind: Fewer, At: at(10), Count: 3}}},
		{name: "Allstill still down", same: 1,
			other: []Period{{at(10), at(20)}}, allstill: []Period{{Down: at(10)}},
			want: []Difference{{Kind: StillDown, At: at(20)}}},
		{name: "the other still down", same: 1,
			other: []Period{{Down: at(10)}}, allstill: []Period{{at(10), at(20)}},
			want: []Difference{{Kind: OtherStillDown, At: at(20)}}},
		{name: "before it was watched, and too recent to judge",
			other: []Period{{at(-30), at(-20)}, {Down: at(998)}}, allstill: []Period{{at(-60), at(-50)}}},
		{name: "Allstill saw it up in between", same: 1,
			other: []Period{{at(0), at(40)}}, allstill: []Period{{at(0), at(5)}, {at(25), at(40)}},
			want: []Difference{{Kind: Again, At: at(25)}, {Kind: Up, At: at(40), Apart: -35 * time.Minute}}},
		{name: "in order of time",
			other: []Period{{at(100), at(110)}}, allstill: []Period{{at(10), at(20)}},
			want: []Difference{{Kind: Extra, At: at(10), For: 10 * time.Minute}, {Kind: Missed, At: at(100), For: 10 * time.Minute}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := Periods(c.other, c.allstill, from, to, margin)
			if got.Same != c.same || !slices.Equal(got.Differences, c.want) {
				t.Fatalf("got %d same, %+v\nwant %d same, %+v", got.Same, got.Differences, c.same, c.want)
			}
		})
	}
}
