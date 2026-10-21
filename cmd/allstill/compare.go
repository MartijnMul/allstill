// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/MartijnMul/allstill/internal/alert"
	"github.com/MartijnMul/allstill/internal/check"
	"github.com/MartijnMul/allstill/internal/compare"
	"github.com/MartijnMul/allstill/internal/importer"
	"github.com/MartijnMul/allstill/internal/store"
)

func compareCommand(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] != "healthchecks" {
		fmt.Fprintln(stderr, "Usage: allstill compare healthchecks [flags]")
		return errors.New("unknown service to compare with")
	}
	fs := flag.NewFlagSet("compare healthchecks", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, `Usage: allstill compare healthchecks -since YYYY-MM-DD [flags]

Shows where Allstill saw checks go down or come back up differently from
Healthchecks: what it missed, what it saw that Healthchecks did not, and what
it saw more than a few minutes apart. Checks are matched by their UUID, so a
Healthchecks project split over several Allstill projects compares as one.
It compares when each would alert; when people would have been told, after
quiet hours and the digest, is on the Messages page.

Give -since the day the jobs began to ping Allstill too: before that,
everything Healthchecks saw counts as missed. Healthchecks keeps status
changes for the current month and the two before it, and counts a check it
paused as down until its next ping. The project's API key is read from
HEALTHCHECKS_API_KEY; the comparison only reads, here and there.

Flags:
`)
		fs.PrintDefaults()
	}
	dbPath := fs.String("db", envOr("ALLSTILL_DB", "allstill.db"), "the database file (ALLSTILL_DB)")
	base := fs.String("url", "https://healthchecks.io", "the Healthchecks site")
	project := fs.String("project", "", "compare only the checks of this Allstill project")
	since := fs.String("since", "", "compare from this day, YYYY-MM-DD: when the jobs began to ping both")
	margin := fs.Duration("within", 5*time.Minute, "how far apart two times may be and still count as the same")
	zone := fs.String("tz", "", "the time zone to show times in (default: this machine's)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	key := os.Getenv("HEALTHCHECKS_API_KEY")
	if key == "" || *since == "" || fs.NArg() != 0 {
		fs.Usage()
		return errors.New("give -since and set HEALTHCHECKS_API_KEY")
	}
	loc := time.Local
	if *zone != "" {
		var err error
		if loc, err = time.LoadLocation(*zone); err != nil {
			return fmt.Errorf("-tz: %w", err)
		}
	}
	from, err := time.ParseInLocation(time.DateOnly, *since, loc)
	if err != nil {
		return errors.New("-since is a day, such as 2026-10-01")
	}

	hcChecks, err := importer.Fetch(ctx, http.DefaultClient, *base, key)
	if err != nil {
		return err
	}
	st, ws, err := openStore(ctx, *dbPath)
	if err != nil {
		return err
	}
	defer st.Close()
	admin, err := st.AdminAccess(ctx, ws)
	if err != nil {
		return err
	}
	checks, err := st.Checks(ctx, admin)
	if err != nil {
		return err
	}
	ours := map[string]store.CheckView{}
	for _, c := range checks {
		ours[c.PingUUID] = c
	}
	var pairs []pair
	var notHere []string
	for _, hc := range hcChecks {
		c, ok := ours[strings.ToLower(hc.UUID)]
		switch {
		case !ok && *project == "":
			notHere = append(notHere, hc.Name)
		case ok && (*project == "" || c.ProjectName == *project):
			pairs = append(pairs, pair{hc, c})
		}
	}
	if len(pairs) == 0 && *project != "" {
		return fmt.Errorf("no check of %s is at Healthchecks", *project)
	}
	if len(pairs) == 0 {
		return errors.New("no check at Healthchecks is in Allstill")
	}
	slices.SortFunc(pairs, func(a, b pair) int {
		return cmp.Or(cmp.Compare(a.ours.ProjectName, b.ours.ProjectName), cmp.Compare(a.ours.Name, b.ours.Name))
	})

	now := time.Now()
	fmt.Fprintf(stdout, "Where Allstill saw checks go down or come back up differently from Healthchecks, since %s.\n", *since)
	fmt.Fprintf(stdout, "Times are in %s; up to %s apart counts as the same.\n", zoneName(*zone, now.In(loc)), short(*margin))
	// Healthchecks keeps the current month and the two before it.
	here := now.In(loc)
	kept := time.Date(here.Year(), here.Month()-2, 1, 0, 0, 0, 0, loc)
	if from.Before(kept) {
		fmt.Fprintf(stdout, "Healthchecks no longer has what happened before %s, so Allstill's problems before then show as extra.\n", kept.Format(time.DateOnly))
	}
	fmt.Fprintln(stdout)
	same, differ, silent, failed := 0, 0, 0, 0
	for _, p := range pairs {
		name := p.ours.ProjectName + " / " + p.ours.Name
		if p.hc.Status == "paused" {
			name += " (paused at Healthchecks)"
		}
		if p.ours.Status.State == check.New {
			fmt.Fprintf(stdout, "%s: never pinged Allstill, so it cannot alert\n", name)
			silent++
			continue
		}
		start := from
		if p.ours.Created.After(start) {
			start = p.ours.Created
		}
		flips, err := importer.FetchFlips(ctx, http.DefaultClient, *base, key, p.hc.UUID, start)
		if err != nil {
			fmt.Fprintf(stdout, "%s: not compared, %v\n", name, err)
			failed++
			continue
		}
		incidents, err := st.IncidentsOfCheck(ctx, p.ours.ID, start)
		if err != nil {
			return err
		}
		r := compare.Periods(compare.FromFlips(flips), compare.FromIncidents(incidents, alert.Settle), start, now, *margin)
		if len(r.Differences) == 0 {
			same++
			fmt.Fprintf(stdout, "%s: the same%s\n", name, times(r.Same))
			continue
		}
		differ++
		fmt.Fprintf(stdout, "%s:\n", name)
		for _, d := range r.Differences {
			fmt.Fprintf(stdout, "  %s  %s\n", d.At.In(loc).Format("Mon 2 Jan 15:04"), says(d))
		}
	}
	fmt.Fprintf(stdout, "\n%d the same, %d different, %d never pinged Allstill.\n", same, differ, silent)
	if len(notHere) > 0 {
		fmt.Fprintf(stdout, "Not in Allstill: %s.\n", strings.Join(notHere, ", "))
	}
	if failed > 0 {
		return fmt.Errorf("%d checks could not be compared", failed)
	}
	return nil
}

// zoneName names the zone times are shown in, also when it is this
// machine's.
func zoneName(given string, t time.Time) string {
	abbr, _ := t.Zone()
	if given == "" {
		return "this machine's time (" + abbr + ")"
	}
	return given + " (" + abbr + ")"
}

type pair struct {
	hc   importer.HCCheck
	ours store.CheckView
}

// says puts a difference into words.
func says(d compare.Difference) string {
	lasted := func() string {
		if d.For == 0 {
			return "and still is"
		}
		return "for " + short(d.For)
	}
	apart := func() string {
		if d.Apart > 0 {
			return short(d.Apart) + " later"
		}
		return short(-d.Apart) + " earlier"
	}
	switch d.Kind {
	case compare.Missed:
		return "missed: down at Healthchecks " + lasted() + ", never at Allstill"
	case compare.Extra:
		return "extra: down at Allstill " + lasted() + ", never at Healthchecks"
	case compare.Down:
		return "went down: Allstill saw it " + apart()
	case compare.Up:
		return "came back up: Allstill saw it " + apart()
	case compare.Fewer:
		return fmt.Sprintf("Healthchecks saw it go down %d times, Allstill once, as one incident", d.Count)
	case compare.StillDown:
		return "came back up at Healthchecks; Allstill still has it down"
	case compare.Again:
		return "went down again at Allstill, after it came back up there; Healthchecks saw one longer down"
	case compare.OtherStillDown:
		return "came back up at Allstill; Healthchecks still has it down"
	}
	return string(d.Kind)
}

// times says how often both saw a check go down. None may also mean it was
// down throughout, or too briefly to tell.
func times(n int) string {
	switch n {
	case 0:
		return ""
	case 1:
		return ", down once at both"
	}
	return fmt.Sprintf(", down %d times at both", n)
}

// short writes a duration to the minute: 1h4m, 20m.
func short(d time.Duration) string {
	if d < time.Minute {
		return d.Round(time.Second).String()
	}
	return strings.TrimSuffix(d.Round(time.Minute).String(), "0s")
}
