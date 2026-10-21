// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"testing"
	"time"
)

func TestSpans(t *testing.T) {
	for d, want := range map[time.Duration]string{
		12 * time.Second:                "12 s",
		3*time.Minute + 5*time.Second:   "3 min 5 s",
		5 * time.Minute:                 "5 min",
		39*time.Minute + 20*time.Second: "39 min",
		2*time.Hour + 50*time.Minute:    "2 h 50 min",
		time.Hour:                       "1 h",
		3*24*time.Hour + 2*time.Hour:    "3 days 2 h",
		1500 * time.Millisecond:         "2 s",
		47*time.Hour + 59*time.Minute:   "47 h 59 min",
		48 * time.Hour:                  "2 days",
	} {
		if got := span(d); got != want {
			t.Errorf("span(%v) = %q, want %q", d, got, want)
		}
	}
	for d, want := range map[time.Duration]string{time.Minute: "every 1 min", time.Hour: "every hour", 24 * time.Hour: "every day", 6 * time.Hour: "every 6 h"} {
		if got := every(d); got != want {
			t.Errorf("every(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestMoments(t *testing.T) {
	ams, _ := time.LoadLocation("Europe/Amsterdam")
	now := time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC) // a Wednesday, 12:00 in Amsterdam
	for t0, want := range map[time.Time]string{
		now.Add(-2 * time.Hour):       "10:00",
		now.Add(-24 * time.Hour):      "Tue 12:00",
		now.Add(-10 * 24 * time.Hour): "Sun 11 Oct 12:00",
		now.AddDate(-1, 0, 0):         "21 Oct 2025 12:00",
		{}:                            "—",
	} {
		if got := clock(now, t0, ams); got != want {
			t.Errorf("clock(%v) = %q, want %q", t0, got, want)
		}
	}
	for t0, want := range map[time.Time]string{
		now.Add(-2 * time.Second):                   "just now",
		now.Add(-90 * time.Second):                  "1 min 30 s ago",
		now.Add(-(19*time.Minute + 30*time.Second)): "19 min ago",
		now.Add(-6 * time.Hour):                     "6 h ago",
		now.Add(-50 * time.Hour):                    "2 days ago",
		{}:                                          "never",
	} {
		if got := ago(now, t0); got != want {
			t.Errorf("ago(%v) = %q, want %q", t0, got, want)
		}
	}
	if got := until(now, now.Add(48*time.Second), ams); got != "in 48 s" {
		t.Errorf("until: %q", got)
	}
	if got := until(now, now.Add(-time.Second), ams); got != "overdue" {
		t.Errorf("until a past moment: %q", got)
	}
}

func TestNumbers(t *testing.T) {
	for v, want := range map[float64]string{18.4e9: "18.4 G", 312: "312", -0.25: "-0.25", 12345: "12.3 k", 1.23456: "1.235", 2e12: "2 T", 0: "0"} {
		if got := number(v); got != want {
			t.Errorf("number(%v) = %q, want %q", v, got, want)
		}
	}
	if got := values(map[string]float64{"size": 18.4e9, "rows": 312}); got != "rows 312 · size 18.4 G" {
		t.Errorf("values: %q", got)
	}
}
