// SPDX-License-Identifier: AGPL-3.0-only

// Package importer brings checks over from Healthchecks.io or a self-hosted
// Healthchecks through its API. Each check keeps its UUID, so a job that pings
// it moves over by changing only the host in its ping URL; until then it can
// ping both, and their status changes can be compared.
package importer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/MartijnMul/allstill/internal/id"
	"github.com/MartijnMul/allstill/internal/schedule"
	"github.com/MartijnMul/allstill/internal/store"
)

// HCCheck is a check as Healthchecks' API (v3) describes it. Fields Allstill
// does not use are kept in the check's settings, so nothing is lost.
type HCCheck struct {
	Name          string `json:"name"`
	Status        string `json:"status"`
	Slug          string `json:"slug"`
	Tags          string `json:"tags"`
	Desc          string `json:"desc"`
	Grace         int64  `json:"grace"`
	Timeout       int64  `json:"timeout"`
	Schedule      string `json:"schedule"`
	TZ            string `json:"tz"`
	UUID          string `json:"uuid"`
	ManualResume  bool   `json:"manual_resume"`
	Methods       string `json:"methods"`
	Subject       string `json:"subject"`
	SubjectFail   string `json:"subject_fail"`
	StartKw       string `json:"start_kw"`
	SuccessKw     string `json:"success_kw"`
	FailureKw     string `json:"failure_kw"`
	FilterSubject bool   `json:"filter_subject"`
	FilterBody    bool   `json:"filter_body"`
}

// Fetch lists the checks of the Healthchecks project the API key belongs to.
// The key must be a read-write key: only that shows the checks' UUIDs.
func Fetch(ctx context.Context, client *http.Client, baseURL, apiKey string) ([]HCCheck, error) {
	var body struct {
		Checks []HCCheck `json:"checks"`
	}
	if err := get(ctx, client, baseURL, apiKey, "/api/v3/checks/", nil, &body); err != nil {
		return nil, fmt.Errorf("reading the checks: %w", err)
	}
	for _, c := range body.Checks {
		if c.UUID == "" {
			return nil, errors.New("the checks have no UUIDs: use the project's read-write API key, not the read-only one")
		}
	}
	return body.Checks, nil
}

// Flip is a change of a Healthchecks check's status: to up, or away from it.
type Flip struct {
	At time.Time
	Up bool
}

// FetchFlips lists a check's status changes since a time, oldest first.
// Healthchecks keeps them for the current month and the two before it.
func FetchFlips(ctx context.Context, client *http.Client, baseURL, apiKey, uuid string, since time.Time) ([]Flip, error) {
	var raw json.RawMessage
	path := "/api/v3/checks/" + url.PathEscape(uuid) + "/flips/"
	query := url.Values{"start": {strconv.FormatInt(since.Unix(), 10)}}
	if err := get(ctx, client, baseURL, apiKey, path, query, &raw); err != nil {
		return nil, fmt.Errorf("reading its status changes: %w", err)
	}
	flips, err := readFlips(raw)
	if err != nil {
		return nil, fmt.Errorf("reading its status changes: %w", err)
	}
	slices.SortStableFunc(flips, func(a, b Flip) int { return a.At.Compare(b.At) })
	return flips, nil
}

// readFlips reads Healthchecks' answer, {"flips": [...]}, or the bare list
// its documentation shows. An answer without the list is refused, so it is
// never taken for no changes.
func readFlips(raw json.RawMessage) ([]Flip, error) {
	var list []struct {
		Timestamp time.Time `json:"timestamp"`
		Up        *int      `json:"up"`
	}
	if trimmed := bytes.TrimSpace(raw); len(trimmed) == 0 || trimmed[0] != '[' {
		var body struct {
			Flips *json.RawMessage `json:"flips"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			return nil, fmt.Errorf("the answer is not a list of changes: %w", err)
		}
		if body.Flips == nil {
			return nil, errors.New("the answer holds no list of changes")
		}
		raw = *body.Flips
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("the answer is not a list of changes: %w", err)
	}
	if list == nil {
		return nil, errors.New("the answer holds no list of changes")
	}
	flips := make([]Flip, len(list))
	for i, f := range list {
		if f.Timestamp.IsZero() || f.Up == nil || (*f.Up != 0 && *f.Up != 1) {
			return nil, errors.New("a status change has no time, or is neither up nor down")
		}
		flips[i] = Flip{At: f.Timestamp.UTC(), Up: *f.Up == 1}
	}
	return flips, nil
}

// get reads one answer of Healthchecks' API into out. It only reads. The key
// travels only over HTTPS (plain HTTP to this machine aside), and never
// follows a redirect to another host.
func get(ctx context.Context, client *http.Client, baseURL, apiKey, path string, query url.Values, out any) error {
	u, err := url.Parse(strings.TrimRight(baseURL, "/") + path)
	if err != nil || u.Host == "" {
		return fmt.Errorf("%q is not a URL", baseURL)
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && isLoopback(u.Hostname())) {
		return errors.New("the Healthchecks URL must use https, so the API key is not sent in the clear")
	}
	u.RawQuery = query.Encode()
	c := *client
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if req.URL.Host != u.Host || req.URL.Scheme != u.Scheme || len(via) > 5 {
			return errors.New("refused a redirect elsewhere, which would carry the API key")
		}
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Api-Key", apiKey)
	resp, err := c.Do(req)
	var ue *url.Error
	if errors.As(err, &ue) {
		// Without the URL, which holds a check's UUID: its ping credential.
		return fmt.Errorf("%s %s: %w", ue.Op, u.Host, ue.Err)
	}
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("the Healthchecks API answered %s", resp.Status)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(out)
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Result says what became of one check.
type Result struct {
	Name    string
	CheckID string // empty when not imported
	Note    string // why it was skipped, or what changed
}

// Import creates the checks in a project. A check whose UUID is already in
// Allstill is skipped, so an import can run again. A schedule Allstill cannot
// read, such as systemd's OnCalendar, skips the check. A slug that a check
// before it already took is left out, so the later check keeps its UUID pings
// but not its slug pings.
func Import(ctx context.Context, st *store.Store, a store.Access, projectID string, checks []HCCheck) ([]Result, error) {
	taken := map[string]bool{}
	var results []Result
	for _, hc := range checks {
		r := Result{Name: hc.Name}
		n, why := convert(hc)
		if n == nil {
			r.Note = why
			results = append(results, r)
			continue
		}
		var notes []string
		if n.Settings != nil {
			notes = append(notes, "kept settings Allstill does not use yet: "+strings.Join(keys(n.Settings["healthchecks"]), ", "))
		}
		if n.Slug != "" && taken[n.Slug] {
			notes = append(notes, fmt.Sprintf("its slug %q is used by an earlier check, so it was left out", n.Slug))
			n.Slug = ""
		}
		c, err := st.CreateCheck(ctx, a, projectID, *n)
		var inv *store.InvalidError
		if errors.As(err, &inv) && inv.Field == "slug" && n.Slug != "" {
			notes = append(notes, fmt.Sprintf("its slug %q is already used in the project, so it was left out", n.Slug))
			n.Slug = ""
			c, err = st.CreateCheck(ctx, a, projectID, *n)
		}
		switch {
		case errors.Is(err, store.ErrExists):
			notes = []string{"already in Allstill"}
		case errors.As(err, &inv):
			notes = []string{inv.Error()}
		case err != nil:
			return results, fmt.Errorf("%s: %w", hc.Name, err)
		default:
			r.CheckID = c.ID
			if n.Slug != "" {
				taken[n.Slug] = true
			}
		}
		r.Note = strings.Join(notes, "; ")
		results = append(results, r)
	}
	return results, nil
}

func keys(v any) []string {
	m, _ := v.(map[string]any)
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// WithTags keeps the checks that have at least one of the tags.
func WithTags(checks []HCCheck, want ...string) []HCCheck {
	return slices.DeleteFunc(slices.Clone(checks), func(c HCCheck) bool {
		return !slices.ContainsFunc(strings.Fields(c.Tags), func(t string) bool { return slices.Contains(want, t) })
	})
}

// tags splits Healthchecks' space-separated tags, cut to what Allstill keeps:
// 20 tags of at most 50 characters.
func tags(s string) []string {
	var out []string
	for _, t := range strings.Fields(s) {
		if len(out) == 20 {
			break
		}
		if len(t) > 50 {
			t = strings.ToValidUTF8(t[:50], "")
		}
		out = append(out, t)
	}
	return out
}

// convert maps a Healthchecks check onto a new Allstill check, or says why it
// cannot.
func convert(hc HCCheck) (*store.NewCheck, string) {
	u, ok := id.ParseUUID(hc.UUID)
	if !ok {
		return nil, fmt.Sprintf("its UUID %q is not valid", hc.UUID)
	}
	name := strings.TrimSpace(hc.Name)
	if name == "" {
		name = hc.Slug
	}
	if name == "" {
		name = u
	}
	n := &store.NewCheck{
		Name:        name,
		Slug:        hc.Slug,
		Description: hc.Desc,
		Tags:        tags(hc.Tags),
		Paused:      hc.Status == "paused",
		Grace:       time.Duration(hc.Grace) * time.Second,
		PingUUID:    u,
	}
	if hc.Schedule != "" && hc.Timeout == 0 {
		if _, err := schedule.ParseCron(hc.Schedule, hc.TZ); err != nil {
			return nil, fmt.Sprintf("its schedule %q cannot be read: %v", hc.Schedule, err)
		}
		n.Cron, n.TimeZone = hc.Schedule, hc.TZ
	} else {
		n.Interval = time.Duration(hc.Timeout) * time.Second
	}

	// What Allstill does not use yet, kept for later and for the record.
	kept := map[string]any{}
	for k, v := range map[string]any{
		"manual_resume": hc.ManualResume, "methods": hc.Methods, "subject": hc.Subject,
		"subject_fail": hc.SubjectFail, "start_kw": hc.StartKw, "success_kw": hc.SuccessKw,
		"failure_kw": hc.FailureKw, "filter_subject": hc.FilterSubject, "filter_body": hc.FilterBody,
	} {
		if v != "" && v != false {
			kept[k] = v
		}
	}
	if len(kept) > 0 {
		n.Settings = map[string]any{"healthchecks": kept}
	}
	return n, ""
}
