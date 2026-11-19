// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/MartijnMul/allstill/internal/active"
	"github.com/MartijnMul/allstill/internal/check"
	"github.com/MartijnMul/allstill/internal/outbound"
	"github.com/MartijnMul/allstill/internal/schedule"
	"github.com/MartijnMul/allstill/internal/store"
)

// Adding and changing checks (intent/manage-checks/spec.md). The rules are the
// store's; what is here is the forms: what was typed stays text until it is saved,
// so a refused form renders again with it, and only what changed on a form goes to
// the store, so a setting the form does not show stays as it is (R8).

const (
	// maxFormBody holds a form with its certificates, twice: as typed and as it was.
	maxFormBody = 256 << 10
	// tryTimeout is how long a try may take, within the server's write limit (R4).
	tryTimeout = 15 * time.Second
	// triesPerMinute is how many tries one person gets, and lookupsPerMinute how
	// many new addresses they may have looked up, as each says whether a name is
	// private (R30).
	triesPerMinute   = 6
	lookupsPerMinute = 30
	// noticeLife is how long a notice waits for the page a post redirects to.
	noticeLife = time.Minute
)

// notice says how something just went, on the page a post redirects to.
type notice struct {
	Class string // a look: up, warn or bad
	Title string
	Lines []string
}

// memory holds what lives for a moment and is stored nowhere: notices, and who
// tried what when.
type memory struct {
	mu      sync.Mutex
	notices map[string]heldNotice
	tries   map[string][]time.Time
}

type heldNotice struct {
	notice
	until time.Time
}

func newMemory() *memory {
	return &memory{notices: map[string]heldNotice{}, tries: map[string][]time.Time{}}
}

// noticeKey keeps a notice to the session it is for, and to a check or, with "",
// to the overview.
func (s *Server) noticeKey(r *http.Request, checkID string) string {
	c, err := r.Cookie(s.cookie("session"))
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%x|%s", sha256.Sum256([]byte(c.Value)), checkID)
}

func (s *Server) say(r *http.Request, checkID string, n notice) {
	now := s.Now()
	s.memory.mu.Lock()
	defer s.memory.mu.Unlock()
	for k, held := range s.memory.notices {
		if now.After(held.until) {
			delete(s.memory.notices, k)
		}
	}
	s.memory.notices[s.noticeKey(r, checkID)] = heldNotice{n, now.Add(noticeLife)}
}

// noticeFor returns the notice waiting for this page. It stays for its minute, as
// a live page fetches itself again.
func (s *Server) noticeFor(r *http.Request, checkID string) *notice {
	s.memory.mu.Lock()
	defer s.memory.mu.Unlock()
	held, ok := s.memory.notices[s.noticeKey(r, checkID)]
	if !ok || s.Now().After(held.until) {
		return nil
	}
	return &held.notice
}

// within says whether something has room left this minute, and counts this one.
func (s *Server) within(key string, limit int) bool {
	now := s.Now()
	s.memory.mu.Lock()
	defer s.memory.mu.Unlock()
	s.memory.forgetTries(now)
	if len(s.memory.tries[key]) >= limit {
		return false
	}
	s.memory.tries[key] = append(s.memory.tries[key], now)
	return true
}

// withinUnless is within for what counts only when it turns out wrong: right is
// asked while the count is held, so a burst cannot get past the limit. At the limit
// it is not asked at all.
func (s *Server) withinUnless(key string, limit int, right func() bool) (room bool) {
	now := s.Now()
	s.memory.mu.Lock()
	defer s.memory.mu.Unlock()
	s.memory.forgetTries(now)
	if len(s.memory.tries[key]) >= limit {
		return false
	}
	if !right() {
		s.memory.tries[key] = append(s.memory.tries[key], now)
	}
	return true
}

// forgetTries drops what is over a minute old. The caller holds the lock.
func (m *memory) forgetTries(now time.Time) {
	for id, tries := range m.tries {
		tries = slices.DeleteFunc(tries, func(t time.Time) bool { return now.Sub(t) >= time.Minute })
		if len(tries) == 0 {
			delete(m.tries, id)
		} else {
			m.tries[id] = tries
		}
	}
}

// changed writes who changed what to the server's log, without a URL or a
// setting's value (R29).
func (s *Server) changed(v viewer, checkID, change string) {
	s.Log.Info("check changed", "person", v.ID, "check", checkID, "change", change)
}

// checkForm is a form as typed.
type checkForm struct {
	Kind, Project, Version            string
	Name, Slug, Description, Tags     string
	Schedule, EveryN, EveryUnit       string
	Cron, TimeZone, GraceN, GraceUnit string
	Urgency                           string
	Critical                          bool
	URL, Method, Expect, Location     string
	JSONPointer, JSONEquals           string
	Follow                            bool
	Host, Port                        string
	TLS                               bool
	Timeout, FailAfter                string
	WarnDays, FailDays, CA            string
	NoVerify                          bool
	Probe                             string
}

// formField is a text field of a form: its name in a post, and what a person calls
// it when it changed under them (R9).
type formField struct {
	Name, Label string
	Value       *string
}

func (f *checkForm) texts() []formField {
	return []formField{
		{"name", "Name", &f.Name}, {"slug", "Slug", &f.Slug}, {"description", "Description", &f.Description}, {"tags", "Tags", &f.Tags},
		{"schedule", "Schedule", &f.Schedule}, {"every_n", "Schedule", &f.EveryN}, {"every_unit", "Schedule", &f.EveryUnit},
		{"cron", "Schedule", &f.Cron}, {"time_zone", "Schedule", &f.TimeZone},
		{"grace_n", "Grace", &f.GraceN}, {"grace_unit", "Grace", &f.GraceUnit},
		{"urgency", "Alerts", &f.Urgency},
		{"url", "Address", &f.URL}, {"method", "Method", &f.Method}, {"expect", "Status codes", &f.Expect}, {"location", "Redirects to", &f.Location},
		{"json_pointer", "JSON field", &f.JSONPointer}, {"json_equals", "JSON field", &f.JSONEquals},
		{"host", "Address", &f.Host}, {"port", "Address", &f.Port},
		{"timeout", "Timeout", &f.Timeout}, {"fail_after", "Down after", &f.FailAfter},
		{"warn_days", "Certificate", &f.WarnDays}, {"fail_days", "Certificate", &f.FailDays}, {"ca", "Certificate", &f.CA},
		{"probe", "Runs from", &f.Probe},
	}
}

// formBox is a tick box. A post does not mention one left unticked, so a form says
// which boxes it showed (R8).
type formBox struct {
	Name, Label string
	Value       *bool
}

func (f *checkForm) boxes() []formBox {
	return []formBox{
		{"critical", "Alerts", &f.Critical}, {"follow", "Redirects", &f.Follow},
		{"tls", "Address", &f.TLS}, {"no_verify", "Certificate", &f.NoVerify},
	}
}

// blankForm is a new check's form: the common case filled in, a nightly job for a
// job that reports in, and each minute for what Allstill runs.
func blankForm(kind string, v viewer) checkForm {
	f := checkForm{Kind: kind, Schedule: "every", TimeZone: v.Loc.String(), Cron: "0 3 * * *", GraceN: "1", GraceUnit: "h", Urgency: "now", Method: "GET",
		Timeout:   strconv.Itoa(int(active.DefaultTimeout / time.Second)),
		FailAfter: strconv.Itoa(store.DefaultFailAfter),
		WarnDays:  strconv.Itoa(active.DefaultWarnDays), FailDays: strconv.Itoa(active.DefaultFailDays),
	}
	f.EveryN, f.EveryUnit = "1", "d"
	if store.Active(kind) {
		f.EveryN, f.EveryUnit = "1", "m"
	}
	return f
}

// formOf is a check as its form shows it.
func formOf(c store.CheckView, v viewer) checkForm {
	f := blankForm(c.Kind, v)
	f.Project, f.Version = c.ProjectID, strconv.FormatInt(c.Version, 10)
	f.Name, f.Slug, f.Description = c.Name, c.Slug, c.Description
	f.Tags = strings.Join(c.Tags, ", ")
	f.Urgency, f.Critical, f.Probe = c.Urgency, c.Critical, c.ProbeID
	f.Cron = ""
	if c.Cron != "" {
		f.Schedule, f.Cron, f.TimeZone = "cron", c.Cron, c.TimeZone
		f.EveryN, f.EveryUnit = "1", "d"
	} else {
		f.EveryN, f.EveryUnit = spanFields(c.Interval)
	}
	f.GraceN, f.GraceUnit = spanFields(c.Grace)
	var timeout, failAfter int
	var warn, fail *int
	switch c.Kind {
	case "http":
		h := settingsOf(c)
		f.URL, f.Location, f.Follow, f.NoVerify, f.CA = h.URL, h.Location, h.Follow, h.NoVerify, h.CA
		if h.Method != "" {
			f.Method = h.Method
		}
		codes := make([]string, len(h.Expect))
		for i, code := range h.Expect {
			codes[i] = strconv.Itoa(code)
		}
		f.Expect = strings.Join(codes, ", ")
		if h.JSON != nil {
			f.JSONPointer, f.JSONEquals = h.JSON.Pointer, h.JSON.Equals
		}
		timeout, failAfter, warn, fail = h.Timeout, h.FailAfter, h.WarnDays, h.FailDays
	case "tcp":
		t := tcpOf(c)
		f.Host, f.Port, f.TLS, f.NoVerify, f.CA = t.Host, strconv.Itoa(t.Port), t.TLS, t.NoVerify, t.CA
		timeout, failAfter, warn, fail = t.Timeout, t.FailAfter, t.WarnDays, t.FailDays
	}
	f.Timeout = strconv.Itoa(int(timeoutOf(timeout) / time.Second))
	f.FailAfter = strconv.Itoa(failAfterOr(failAfter))
	if warn != nil {
		f.WarnDays = strconv.Itoa(*warn)
	}
	if fail != nil {
		f.FailDays = strconv.Itoa(*fail)
	}
	// As a post is read, so that only what someone typed differs: a command or an
	// import may have stored a space at the edge of a value.
	for _, field := range f.texts() {
		*field.Value = normalText(*field.Value)
	}
	return f
}

// normalText is text from a text area as it compares: browsers send lines ending
// in CR and LF.
func normalText(s string) string {
	return strings.TrimSpace(strings.ReplaceAll(s, "\r\n", "\n"))
}

// read fills in what a post holds. What it does not mention stays as it is.
func (f *checkForm) read(r *http.Request) {
	for _, field := range f.texts() {
		if r.PostForm.Has(field.Name) {
			*field.Value = normalText(r.PostFormValue(field.Name))
		}
	}
	shown := r.PostForm["box"]
	for _, box := range f.boxes() {
		if slices.Contains(shown, box.Name) {
			*box.Value = r.PostForm.Has(box.Name)
		}
	}
}

// changedSince names what differs between two forms, once each.
func changedSince(was, now checkForm) []string {
	var names []string
	add := func(label string) {
		if !slices.Contains(names, label) {
			names = append(names, label)
		}
	}
	wasTexts, nowTexts := was.texts(), now.texts()
	for i := range wasTexts {
		if *wasTexts[i].Value != *nowTexts[i].Value {
			add(wasTexts[i].Label)
		}
	}
	wasBoxes, nowBoxes := was.boxes(), now.boxes()
	for i := range wasBoxes {
		if *wasBoxes[i].Value != *nowBoxes[i].Value {
			add(wasBoxes[i].Label)
		}
	}
	return names
}

// spanFields writes a duration as a form shows it: a number and the largest unit
// that takes it whole.
func spanFields(d time.Duration) (n, unit string) {
	for _, u := range []struct {
		unit string
		d    time.Duration
	}{{"d", 24 * time.Hour}, {"h", time.Hour}, {"m", time.Minute}} {
		if d >= u.d && d%u.d == 0 {
			return strconv.FormatInt(int64(d/u.d), 10), u.unit
		}
	}
	return strconv.FormatInt(int64(d/time.Second), 10), "s"
}

// unitChoice is a unit of how often or how long, as a list offers it.
type unitChoice struct {
	Value, One, Many, Label string
	Selected                bool
}

// unitsOf lists the units a form offers, named for the number beside them. Seconds
// are there only for a duration that needs them, as from an import.
func unitsOf(n, unit string) []unitChoice {
	units := []unitChoice{{Value: "m", One: "minute", Many: "minutes"}, {Value: "h", One: "hour", Many: "hours"}, {Value: "d", One: "day", Many: "days"}}
	if unit == "s" {
		units = append(units, unitChoice{Value: "s", One: "second", Many: "seconds"})
	}
	for i := range units {
		units[i].Selected = units[i].Value == unit
		units[i].Label = units[i].Many
		if n == "1" {
			units[i].Label = units[i].One
		}
	}
	return units
}

// fieldErrors holds what is wrong, by the name of the field that says so.
type fieldErrors map[string]string

// readSpan reads a number and a unit from a form.
func readSpan(field, n, unit string, errs fieldErrors) time.Duration {
	per, ok := map[string]time.Duration{"s": time.Second, "m": time.Minute, "h": time.Hour, "d": 24 * time.Hour}[unit]
	count, err := strconv.Atoi(n)
	if !ok || err != nil || count < 1 || count > 100000 {
		errs[field] = "Give a whole number, such as 1."
		return 0
	}
	return time.Duration(count) * per
}

func readTags(text string) []string {
	tags := []string{}
	for _, t := range strings.Split(text, ",") {
		if t = strings.TrimSpace(t); t != "" {
			tags = append(tags, t)
		}
	}
	return tags
}

// settings returns what a check Allstill runs fetches: as it was, with what
// differs on the form changed.
func (f checkForm) settings(current []byte, was checkForm, errs fieldErrors) map[string]any {
	s := map[string]any{}
	if len(current) > 0 {
		json.Unmarshal(current, &s)
	}
	text := func(key, now, then string) {
		switch {
		case now == then:
		case now == "":
			delete(s, key)
		default:
			s[key] = now
		}
	}
	whole := func(key, field, now, then string) {
		if now == then {
			return
		}
		if now == "" {
			delete(s, key)
			return
		}
		n, err := strconv.Atoi(now)
		if err != nil {
			errs[field] = "Give a whole number."
			return
		}
		s[key] = n
	}
	box := func(key string, now, then bool) {
		switch {
		case now == then:
		case now:
			s[key] = true
		default:
			delete(s, key)
		}
	}
	switch f.Kind {
	case "http":
		text("url", f.URL, was.URL)
		if f.Method != was.Method {
			if s["method"] = f.Method; f.Method == "GET" {
				delete(s, "method")
			}
		}
		if f.Expect != was.Expect {
			delete(s, "expected_status")
			var codes []int
			for _, code := range strings.FieldsFunc(f.Expect, func(r rune) bool { return r == ',' || r == ' ' }) {
				n, err := strconv.Atoi(code)
				if err != nil {
					errs["expect"] = "Give status codes, such as 200, 301."
					break
				}
				codes = append(codes, n)
			}
			if len(codes) > 0 {
				s["expected_status"] = codes
			}
		}
		text("location", f.Location, was.Location)
		if f.JSONPointer != was.JSONPointer || f.JSONEquals != was.JSONEquals {
			switch {
			case f.JSONPointer != "":
				s["json_field"] = active.JSONField{Pointer: f.JSONPointer, Equals: f.JSONEquals}
			case f.JSONEquals != "":
				errs["json_pointer"] = "Give the field that must hold this value, such as /status."
			default:
				delete(s, "json_field")
			}
		}
		box("follow_redirects", f.Follow, was.Follow)
	case "tcp":
		text("host", f.Host, was.Host)
		if f.Port == "" {
			delete(s, "port")
		}
		whole("port", "port", f.Port, was.Port)
		box("tls", f.TLS, was.TLS)
	}
	whole("timeout", "timeout", f.Timeout, was.Timeout)
	whole("fail_after", "fail_after", f.FailAfter, was.FailAfter)
	// Without TLS there is no certificate to say anything of.
	if f.Kind == "tcp" && !f.TLS {
		active.WithoutTLS(s)
		return s
	}
	whole("warn_days", "warn_days", f.WarnDays, was.WarnDays)
	whole("fail_days", "fail_days", f.FailDays, was.FailDays)
	text("ca", f.CA, was.CA)
	box("no_verify", f.NoVerify, was.NoVerify)
	return s
}

// schedule reads the schedule from a form, as an interval or a cron expression
// with its time zone.
func (f checkForm) schedule(errs fieldErrors) (interval time.Duration, cron, timeZone string) {
	if f.Schedule == "cron" && !store.Active(f.Kind) {
		if f.Cron == "" {
			errs["cron"] = "Give a cron expression, such as 0 3 * * *."
		}
		if f.TimeZone == "" {
			errs["time_zone"] = "Give a time zone, such as Europe/Amsterdam."
		}
		return 0, f.Cron, f.TimeZone
	}
	return readSpan("every", f.EveryN, f.EveryUnit, errs), "", ""
}

// change is what differs between a check and its form.
func (f checkForm) change(c store.CheckView, was checkForm, errs fieldErrors) store.Change {
	ch := store.Change{}
	if version, err := strconv.ParseInt(f.Version, 10, 64); err == nil {
		ch.Version = version
	}
	if f.Name != was.Name {
		ch.Name = &f.Name
	}
	if f.Slug != was.Slug {
		ch.Slug = &f.Slug
	}
	if f.Description != was.Description {
		ch.Description = &f.Description
	}
	if f.Tags != was.Tags {
		tags := readTags(f.Tags)
		ch.Tags = &tags
	}
	if f.Schedule != was.Schedule || f.EveryN != was.EveryN || f.EveryUnit != was.EveryUnit || f.Cron != was.Cron || f.TimeZone != was.TimeZone {
		interval, cron, timeZone := f.schedule(errs)
		if cron != "" || f.Schedule == "cron" && !store.Active(f.Kind) {
			ch.Cron, ch.TimeZone = &cron, &timeZone
		} else {
			ch.Interval = &interval
		}
	}
	if f.GraceN != was.GraceN || f.GraceUnit != was.GraceUnit {
		grace := readSpan("grace", f.GraceN, f.GraceUnit, errs)
		ch.Grace = &grace
	}
	if store.Active(c.Kind) {
		settings := f.settings(c.Settings, was, errs)
		before := map[string]any{}
		json.Unmarshal(c.Settings, &before)
		// The same settings are not a change, which would make it wait for its next attempt.
		now, _ := json.Marshal(settings)
		then, _ := json.Marshal(before)
		if string(now) != string(then) {
			ch.Settings = settings
		}
		if f.Probe != was.Probe {
			ch.Probe = &f.Probe
		}
	}
	if f.Urgency != was.Urgency || f.Critical != was.Critical {
		ch.Urgency, ch.Critical = &f.Urgency, &f.Critical
	}
	return ch
}

// newCheck is the check a form asks for.
func (f checkForm) newCheck(blank checkForm, errs fieldErrors) store.NewCheck {
	n := store.NewCheck{Name: f.Name, Slug: f.Slug, Description: f.Description, Tags: readTags(f.Tags), Kind: f.Kind, ProbeID: f.Probe}
	n.Interval, n.Cron, n.TimeZone = f.schedule(errs)
	if store.Active(f.Kind) {
		n.Settings = f.settings(nil, blank, errs)
		n.Slug = ""
	} else {
		n.Grace = readSpan("grace", f.GraceN, f.GraceUnit, errs)
		n.ProbeID = ""
	}
	return n
}

// hostOf is the host a form's check connects to.
func (f checkForm) hostOf() string {
	if f.Kind == "tcp" {
		return f.Host
	}
	host, _ := active.HTTP{URL: f.URL}.Host()
	return host
}

func (f checkForm) addressField() string {
	if f.Kind == "tcp" {
		return "host"
	}
	return "url"
}

// reachable refuses an address the server may not reach, on a form.
func (s *Server) reachable(ctx context.Context, v viewer, f checkForm, errs fieldErrors) {
	if problem := s.addressProblem(ctx, v.ID, f.hostOf()); problem != "" {
		errs[f.addressField()] = problem
	}
}

// addressProblem says why the server may not reach a host, for one credential, or ""
// when it may. It says only that: a person is not told what a name resolves to, nor
// that it is the server's own (R27).
func (s *Server) addressProblem(ctx context.Context, credential, host string) string {
	if !s.within("lookup|"+credential, lookupsPerMinute) {
		return "That is many new addresses in a minute. Wait a moment, and save again."
	}
	if _, refused := errors.AsType[*outbound.Unreachable](outbound.CheckHost(ctx, host, s.Allow)); refused {
		return "This address is not on the public internet, and this server may not reach it. Ask your administrator, or use a probe."
	}
	return ""
}

// fieldOf says which field on a form a refusal from the store belongs to, and what
// to say there: "" for one that no field holds.
func (f checkForm) fieldOf(inv *store.InvalidError) (string, string) {
	problem := sentence(inv.Problem)
	switch {
	case inv.Field == "url" && inv.Problem == "is needed":
		return "url", "Give the address, such as https://www.example.com/."
	case inv.Field == "host" && inv.Problem == "is needed":
		return "host", "Give a host name or address, such as mail.example.com."
	}
	switch inv.Field {
	case "name", "slug", "description", "tags", "grace", "urgency", "critical", "url", "method", "location",
		"host", "port", "timeout", "fail_after", "warn_days", "ca", "no_verify", "probe", "project":
		return inv.Field, problem
	case "schedule", "schedule.every":
		switch {
		case f.Schedule != "cron" || store.Active(f.Kind):
			return "every", problem
		case strings.Contains(inv.Problem, "time zone"):
			return "time_zone", problem
		}
		return "cron", problem
	case "fail_days":
		if strings.Contains(inv.Problem, "warn_days") {
			return "fail_days", "Must be fewer than the days for a warning."
		}
		return "fail_days", problem
	case "expected_status":
		return "expect", problem
	case "json_field", "json_field.pointer":
		return "json_pointer", problem
	case "json_field.equals":
		return "json_equals", problem
	}
	return "", problem
}

// sentence makes a store's problem read as a sentence.
func sentence(problem string) string {
	r, size := utf8.DecodeRuneInString(problem)
	problem = string(unicode.ToUpper(r)) + problem[size:]
	if !strings.HasSuffix(problem, ".") {
		problem += "."
	}
	return problem
}

// choice is an option in a list.
type choice struct{ Value, Label string }

// formData is a form's page.
type formData struct {
	F           checkForm
	Errors      fieldErrors
	Problem     string
	More        bool // More settings starts open
	New         bool
	Action      string
	Cancel      string
	Was         string // the form as it was read, to say what changed under it (R9)
	ProjectName string
	Projects    []choice
	Probes      []choice
	Own         bool // a probe's own check: no schedule, and its alerts are fixed (R19)
	ShowAlerts  bool
	CanCritical bool
	C           store.CheckView // the check of an edit form
	CanDelete   bool
	CanMove     bool
}

// title is a form's page title.
func (d formData) title() string {
	if d.New {
		return "Add a check"
	}
	return "Edit " + d.C.Name
}

// moreFields are the fields under "More settings", which opens when one is wrong
// or says something.
var moreFields = []string{"description", "tags", "slug", "method", "expect", "location", "json_pointer", "json_equals", "timeout", "fail_after", "warn_days", "fail_days", "ca", "no_verify", "critical"}

func (d *formData) openMore(blank checkForm) {
	for _, field := range moreFields {
		if d.Errors[field] != "" {
			d.More = true
		}
	}
	f := d.F
	if f.Description != "" || f.Tags != "" || f.Slug != "" || f.Expect != "" || f.Location != "" || f.JSONPointer != "" || f.CA != "" || f.NoVerify || f.Follow ||
		f.Method != blank.Method || f.Timeout != blank.Timeout || f.FailAfter != blank.FailAfter || f.WarnDays != blank.WarnDays || f.FailDays != blank.FailDays {
		d.More = true
	}
}

// editableProjects are the projects a person may change, as a form lists them.
func editableProjects(v viewer) []choice {
	var projects []choice
	for _, p := range v.Access.Projects {
		if p.Role != store.Viewer {
			projects = append(projects, choice{p.ID, v.Access.Label(p.ID)})
		}
	}
	return projects
}

// probeChoices lists the probes a form offers: those of one project, or with ""
// of every project the person may change, each once.
func (s *Server) probeChoices(ctx context.Context, v viewer, project string) ([]choice, error) {
	probes, err := s.Store.ProbesFor(ctx, v.Access)
	if err != nil {
		return nil, err
	}
	var choices []choice
	for _, p := range probes {
		c := choice{p.ID, p.Name}
		if (project == "" || p.ProjectID == project) && !slices.Contains(choices, c) {
			choices = append(choices, c)
		}
	}
	return choices, nil
}

type newCheckData struct {
	Chosen bool
	Form   formData
	Kinds  []kindChoice
}

type kindChoice struct{ Href, Title, Text, Icon string }

// GET /checks/new: what to watch, in plain words, and then the fields for it.
func (s *Server) newCheckPage(w http.ResponseWriter, r *http.Request, v viewer) {
	projects := editableProjects(v)
	if len(projects) == 0 {
		s.fail(w, r, store.ErrForbidden)
		return
	}
	kind, project := r.URL.Query().Get("kind"), r.URL.Query().Get("project")
	if !slices.ContainsFunc(projects, func(c choice) bool { return c.Value == project }) {
		project = projects[0].Value
	}
	f := blankForm(kind, v)
	f.Project = project
	s.renderNew(w, r, v, http.StatusOK, f, nil, "")
}

func (s *Server) renderNew(w http.ResponseWriter, r *http.Request, v viewer, code int, f checkForm, errs fieldErrors, problem string) {
	nav, err := s.nav(r.Context(), v, f.Project)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	crumbs := []crumb{{"All checks", "/"}, {"Add a check", ""}}
	d := newCheckData{}
	if f.Kind != "ping" && !store.Active(f.Kind) {
		q := "/checks/new?project=" + f.Project + "&kind="
		d.Kinds = []kindChoice{
			{q + "ping", "A job that reports in", "A backup, a cron job or a script tells Allstill when it ran.", "clock"},
			{q + "http", "A website or API", "Allstill fetches an address each minute and looks at the answer and its certificate.", "globe"},
			{q + "tcp", "A port", "Allstill connects to a host and port, such as a mail or database server.", "plug"},
		}
		s.render(w, r, code, "check-new", page{Title: "Add a check", Person: v.Name, Crumbs: crumbs, Nav: nav, Data: d})
		return
	}
	d.Chosen = true
	probes, err := s.probeChoices(r.Context(), v, "")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	form := formData{F: f, Errors: errs, Problem: problem, New: true, Action: "/checks/new", Cancel: overviewLink("", f.Project),
		Projects: editableProjects(v), Probes: probes, ShowAlerts: true, CanCritical: anyOwner(v)}
	if len(form.Projects) == 1 {
		form.ProjectName = form.Projects[0].Label
	}
	form.openMore(blankForm(f.Kind, v))
	d.Form = form
	s.render(w, r, code, "check-new", page{Title: form.title(), Person: v.Name, Crumbs: crumbs, Nav: nav, Data: d})
}

// anyOwner says whether a person owns any project: a form that offers several
// projects shows the box, and it is judged for the one chosen.
func anyOwner(v viewer) bool {
	return slices.ContainsFunc(v.Access.Projects, func(p store.ProjectAccess) bool { return p.Role == store.Owner })
}

// POST /checks/new adds a check.
func (s *Server) addCheck(w http.ResponseWriter, r *http.Request, v viewer) {
	if !s.readPost(w, r) {
		return
	}
	kind := r.PostFormValue("kind")
	if kind != "ping" && !store.Active(kind) {
		kind = "ping"
	}
	blank := blankForm(kind, v)
	f := blank
	f.read(r)
	f.Project = r.PostFormValue("project")
	errs := fieldErrors{}
	refuse := func(problem string) {
		s.renderNew(w, r, v, http.StatusUnprocessableEntity, f, errs, problem)
	}
	role, ok := v.Access.Role(f.Project)
	if !ok {
		s.fail(w, r, store.ErrNotFound)
		return
	}
	if role == store.Viewer {
		s.fail(w, r, store.ErrForbidden)
		return
	}
	n := f.newCheck(blank, errs)
	if f.Urgency != "now" && f.Urgency != "digest" && f.Urgency != "none" {
		errs["urgency"] = "Choose how fast its problems reach people."
	}
	if f.Critical && role != store.Owner {
		errs["critical"] = "Only an owner can turn this on: it wakes people during their quiet hours."
	}
	if len(errs) == 0 && store.Active(kind) && f.Probe == "" {
		s.reachable(r.Context(), v, f, errs)
	}
	if len(errs) > 0 {
		refuse("")
		return
	}
	if f.Urgency != blank.Urgency || f.Critical {
		n.Urgency, n.Critical = f.Urgency, f.Critical
	}
	created, err := s.Store.CreateCheck(r.Context(), v.Access, f.Project, n)
	if inv, ok := errors.AsType[*store.InvalidError](err); ok {
		field, problem := f.fieldOf(inv)
		if field == "" {
			refuse(problem)
			return
		}
		errs[field] = problem
		refuse("")
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	now := s.Now()
	s.changed(v, created.ID, "added")
	c, err := s.Store.Check(r.Context(), v.Access, created.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var said notice
	if store.Active(kind) {
		said = s.tried(r, v, c, "Added.")
	} else {
		said = notice{Class: "up", Title: "Added.", Lines: []string{waitsForPing(c, now, v.Loc)}}
	}
	s.warnOfSameName(r.Context(), v, c, &said)
	s.say(r, c.ID, said)
	http.Redirect(w, r, "/checks/"+c.ID, http.StatusSeeOther)
}

// waitsForPing says when a new check is next expected (R6).
func waitsForPing(c store.CheckView, now time.Time, loc *time.Location) string {
	if c.Cron == "" {
		return fmt.Sprintf("It waits for its first ping, and then expects one %s.", every(c.Interval))
	}
	cron, err := schedule.ParseCron(c.Cron, c.TimeZone)
	if err != nil {
		return "It waits for its first ping."
	}
	next, err := cron.Next(now)
	if err != nil {
		return "It waits for its first ping."
	}
	return fmt.Sprintf("It waits for its first ping. By its schedule, the next run is at %s.", clock(now, next, loc))
}

// sameName says another check in the project has the same name. It is not refused,
// which would trap someone retrying a save that timed out (C10).
func (s *Server) sameName(ctx context.Context, access store.Access, c store.CheckView) bool {
	checks, err := s.Store.CheckStates(ctx, access)
	if err != nil {
		s.Log.Error("reading checks", "error", err)
		return false
	}
	return slices.ContainsFunc(checks, func(other store.CheckView) bool {
		return other.ID != c.ID && other.ProjectID == c.ProjectID && other.Name == c.Name
	})
}

func (s *Server) warnOfSameName(ctx context.Context, v viewer, c store.CheckView, n *notice) {
	if s.sameName(ctx, v.Access, c) {
		n.Lines = append(n.Lines, "Another check in this project has the same name.")
	}
}

// tryFound is what trying a check with a new address found. A try is not an attempt:
// nothing of it is stored, and it counts for nothing (R4).
type tryFound struct {
	Probe   string         // only this probe can try the address
	Limited bool           // this credential has made its tries for this minute
	Cannot  bool           // this server has no way to try
	Result  *active.Result // what it found, when it did try
	First   time.Time      // by when its first real attempt comes; zero when paused
	Method  string         // of an HTTP check
}

// tryCheck tries a check with a new address, within what one credential may (R30).
func (s *Server) tryCheck(ctx context.Context, credential string, c store.CheckView) tryFound {
	found := tryFound{}
	if c.Status.State != check.Paused {
		found.First = firstAttempt(c, s.Now())
	}
	if c.ProbeID != "" {
		name, err := s.Store.ProbeName(ctx, s.workspace, c.ProbeID)
		if err != nil {
			s.Log.Error("reading a probe", "error", err)
		}
		found.Probe = name
		return found
	}
	if s.Try == nil {
		found.Cannot = true
		return found
	}
	if !s.within("try|"+credential, triesPerMinute) {
		found.Limited = true
		return found
	}
	var attempt active.Check
	if c.Kind == "tcp" {
		t := tcpOf(c)
		attempt.TCP = &t
	} else {
		attempt.HTTP = settingsOf(c)
	}
	// Its own limit is what the reason names, so it holds within a try's.
	seconds := int(tryTimeout / time.Second)
	if attempt.TCP != nil {
		attempt.TCP.Timeout = min(attempt.TCP.Timeout, seconds)
	}
	attempt.HTTP.Timeout = min(attempt.HTTP.Timeout, seconds)
	found.Method = attempt.HTTP.Method
	ctx, cancel := context.WithTimeout(ctx, tryTimeout+time.Second)
	defer cancel()
	res := s.Try(ctx, attempt)
	found.Result = &res
	return found
}

// tried makes the try of a check with a new address, and says how it went.
func (s *Server) tried(r *http.Request, v viewer, c store.CheckView, done string) notice {
	now := s.Now()
	n := notice{Class: "up", Title: done}
	try := s.tryCheck(r.Context(), v.ID, c)
	first := "it is not tried for real until you resume it"
	if !try.First.IsZero() {
		first = fmt.Sprintf("its first real attempt comes by %s", clock(now, try.First, v.Loc))
	}
	switch {
	case c.ProbeID != "":
		n.Lines = []string{"Only the probe " + try.Probe + " can try this address, and " + first + "."}
		return n
	case try.Cannot:
		n.Lines = []string{sentence(first)}
		return n
	case try.Limited:
		n.Lines = []string{fmt.Sprintf("Not tried now: you get %d tries a minute. But %s.", triesPerMinute, first)}
		return n
	}
	res := *try.Result
	var found string
	switch res.Outcome {
	case check.Passed:
		found = "Tried it: up"
	case check.Warned:
		found, n.Class = "Tried it: up, with a warning", "warn"
	default:
		found, n.Class = "Tried it: it fails", "warn"
	}
	if res.Reason != "" {
		found += ", " + res.Reason
	}
	if code, ok := res.Measured["status"]; ok {
		found += fmt.Sprintf(" (HTTP %d in %d ms)", int(code), int(res.Measured["ms"]))
	} else if ms := res.Measured["ms"]; ms >= 1 {
		found += fmt.Sprintf(" (in %d ms)", int(ms))
	}
	n.Title = done + " " + found + "."
	if days, ok := res.Measured["cert_days"]; ok {
		n.Lines = append(n.Lines, "Its certificate has "+daysLeft(days)+" left.")
	}
	if try.Method == "HEAD" && res.Measured["status"] == http.StatusMethodNotAllowed {
		n.Lines = append(n.Lines, "This site does not take HEAD requests. Change the method to GET.")
	}
	n.Lines = append(n.Lines, "A try does not count, and "+first+".")
	if res.Outcome == check.Failed && !c.Status.EverPassed {
		n.Lines = append(n.Lines, "It alerts nobody until it has worked once.")
	}
	return n
}

// firstAttempt is by when a check's next attempt comes: the loop and the probes
// read new checks at least each minute, and then wait for its place in its interval.
func firstAttempt(c store.CheckView, now time.Time) time.Time {
	return active.Check{ID: c.ID, Every: c.Interval}.Slot(now.Add(time.Minute))
}

// forChange reads a check for someone who means to change it: not found when they
// may not see it, forbidden when they may only look (R25).
func (s *Server) forChange(w http.ResponseWriter, r *http.Request, v viewer) (store.CheckView, bool) {
	c, err := s.Store.Check(r.Context(), v.Access, r.PathValue("id"))
	if err == nil && !v.Access.SeesSecrets(c.ProjectID) {
		err = store.ErrForbidden
	}
	if err != nil {
		s.fail(w, r, err)
		return c, false
	}
	return c, true
}

// readPost reads a form's post, within its limit.
func (s *Server) readPost(w http.ResponseWriter, r *http.Request) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBody)
	err := r.ParseForm()
	if _, tooLarge := errors.AsType[*http.MaxBytesError](err); tooLarge {
		s.render(w, r, http.StatusRequestEntityTooLarge, "error", page{Title: "Too much to save", Data: "This form holds more than a check can have. Go back and shorten what you typed."})
		return false
	}
	if err != nil {
		s.render(w, r, http.StatusBadRequest, "error", page{Title: "This cannot be read", Data: "The form did not arrive whole. Go back and try again."})
		return false
	}
	return true
}

// editForm is a check's form page.
func (s *Server) editForm(ctx context.Context, v viewer, c store.CheckView, f checkForm, errs fieldErrors, problem string) (formData, error) {
	own, err := s.Store.IsProbesOwnCheck(ctx, v.Access, c.ID)
	if err != nil {
		return formData{}, err
	}
	probes, err := s.probeChoices(ctx, v, c.ProjectID)
	if err != nil {
		return formData{}, err
	}
	was, err := json.Marshal(formOf(c, v))
	if err != nil {
		return formData{}, err
	}
	role, _ := v.Access.Role(c.ProjectID)
	d := formData{F: f, Errors: errs, Problem: problem, Action: "/checks/" + c.ID + "/edit", Cancel: "/checks/" + c.ID, Was: string(was),
		ProjectName: c.ProjectName, Probes: probes, Own: own, ShowAlerts: !own || v.Access.OwnsTeamOf(c.ProjectID), CanCritical: role == store.Owner, C: c,
		CanDelete: !own, CanMove: len(otherProjects(v, c)) > 0 && (!own || v.Access.OwnsTeamOf(c.ProjectID))}
	d.openMore(blankForm(c.Kind, v))
	return d, nil
}

func (s *Server) renderEdit(w http.ResponseWriter, r *http.Request, v viewer, code int, c store.CheckView, f checkForm, errs fieldErrors, problem string) {
	d, err := s.editForm(r.Context(), v, c, f, errs, problem)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	nav, err := s.nav(r.Context(), v, c.ProjectID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	crumbs := []crumb{{c.ProjectName, overviewLink("", c.ProjectID)}, {c.Name, "/checks/" + c.ID}, {"Edit", ""}}
	s.render(w, r, code, "check-edit", page{Title: d.title(), Person: v.Name, Crumbs: crumbs, Nav: nav, Data: d})
}

// GET /checks/{id}/edit
func (s *Server) editCheckPage(w http.ResponseWriter, r *http.Request, v viewer) {
	c, ok := s.forChange(w, r, v)
	if !ok {
		return
	}
	s.renderEdit(w, r, v, http.StatusOK, c, formOf(c, v), nil, "")
}

// POST /checks/{id}/edit saves what differs on the form.
func (s *Server) saveCheck(w http.ResponseWriter, r *http.Request, v viewer) {
	c, ok := s.forChange(w, r, v)
	if !ok || !s.readPost(w, r) {
		return
	}
	was := formOf(c, v)
	f := was
	f.read(r)
	f.Kind, f.Project = c.Kind, c.ProjectID // neither changes here (R7)
	errs := fieldErrors{}
	refused := func(problem string) {
		s.renderEdit(w, r, v, http.StatusUnprocessableEntity, c, f, errs, problem)
	}
	outOfDate := func(current store.CheckView) {
		// What it was read as says what changed since. The form holds it only to say so.
		before := was
		json.Unmarshal([]byte(r.PostFormValue("was")), &before)
		names := changedSince(before, formOf(current, v))
		problem := "Someone changed this check while you were editing it. Your changes are not saved yet."
		if len(names) > 0 {
			problem = "Someone changed this check while you were editing it: " + strings.Join(names, ", ") + ". Your changes are not saved yet. Saving again puts what is on this form in their place."
		}
		f.Version = strconv.FormatInt(current.Version, 10)
		s.renderEdit(w, r, v, http.StatusConflict, current, f, nil, problem)
	}
	f.Version = r.PostFormValue("version")
	if f.Version != was.Version {
		outOfDate(c)
		return
	}
	if f == was {
		// Nothing to tell anyone, and nobody else's form goes out of date.
		s.say(r, c.ID, notice{Class: "up", Title: "Nothing changed, so nothing was saved."})
		http.Redirect(w, r, "/checks/"+c.ID, http.StatusSeeOther)
		return
	}
	ch := f.change(c, was, errs)
	addressChanged := store.Active(c.Kind) && (ch.Settings != nil && (f.URL != was.URL || f.Host != was.Host || f.Port != was.Port || f.TLS != was.TLS) || f.Probe != was.Probe)
	if len(errs) == 0 && addressChanged && f.Probe == "" {
		s.reachable(r.Context(), v, f, errs)
	}
	if len(errs) > 0 {
		refused("")
		return
	}
	now := s.Now()
	deadline, err := s.Store.UpdateCheck(r.Context(), v.Access, c.ID, ch, now)
	if errors.Is(err, store.ErrChanged) {
		current, err := s.Store.Check(r.Context(), v.Access, c.ID)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		outOfDate(current)
		return
	}
	if inv, ok := errors.AsType[*store.InvalidError](err); ok {
		field, problem := f.fieldOf(inv)
		if field == "" {
			refused(problem)
			return
		}
		errs[field] = problem
		refused("")
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if !deadline.IsZero() && s.Nudge != nil {
		s.Nudge(deadline)
	}
	s.changed(v, c.ID, "edited")
	after, err := s.Store.Check(r.Context(), v.Access, c.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	said := notice{Class: "up", Title: "Saved."}
	switch {
	case addressChanged:
		said = s.tried(r, v, after, "Saved.")
	case after.Interval != c.Interval || after.Cron != c.Cron || after.TimeZone != c.TimeZone || after.Grace != c.Grace:
		said.Lines = []string{nextAfterChange(after, now, v.Loc)}
	}
	if f.Name != was.Name {
		s.warnOfSameName(r.Context(), v, after, &said)
	}
	s.say(r, c.ID, said)
	http.Redirect(w, r, "/checks/"+c.ID, http.StatusSeeOther)
}

// nextAfterChange says what a change of schedule means for a check (R6).
func nextAfterChange(c store.CheckView, now time.Time, loc *time.Location) string {
	switch c.Status.State {
	case check.New:
		return waitsForPing(c, now, loc)
	case check.Paused:
		return "It is paused, so nothing is expected of it for now."
	case check.Down:
		return "It stays down until a ping."
	}
	if c.Status.NextExpected.IsZero() {
		return "It counts from now."
	}
	return "It counts from now, and is next expected at " + clock(now, c.Status.NextExpected, loc) + "."
}

// confirmData is a page saying what something will do, with a button to do it.
type confirmData struct {
	C        store.CheckView
	Title    string
	Lines    []string
	Warning  string
	Problem  string
	Action   string
	Button   string
	Danger   bool
	Projects []choice // where a check can move to
	NoRoom   string   // why it cannot be done, in place of the button
	Cancel   string
}

// otherProjects are the projects a check can move to: those of its own team that
// the person may change (teams spec, R24).
func otherProjects(v viewer, c store.CheckView) []choice {
	here, _ := v.Access.TeamOf(c.ProjectID)
	return slices.DeleteFunc(editableProjects(v), func(p choice) bool {
		there, _ := v.Access.TeamOf(p.Value)
		return p.Value == c.ProjectID || there.ID != here.ID
	})
}

// confirmOf is what a page says before the action named.
func (s *Server) confirmOf(ctx context.Context, v viewer, c store.CheckView, action string) (confirmData, error) {
	d := confirmData{C: c, Action: "/checks/" + c.ID + "/" + action, Cancel: "/checks/" + c.ID}
	active := store.Active(c.Kind)
	own, err := s.Store.IsProbesOwnCheck(ctx, v.Access, c.ID)
	if err != nil {
		return d, err
	}
	if own && (action == "pause" || action == "delete") {
		d.NoRoom = "This is a probe's own check, which tells when the probe goes quiet, so it stays."
	}
	switch action {
	case "pause":
		d.Title, d.Button = "Pause "+c.Name, "Pause"
		d.Lines = []string{"Nothing is expected of a paused check, and an open incident closes without a recovery message."}
		if active {
			d.Lines = append(d.Lines, "Allstill stops trying it until you resume it.")
		} else {
			d.Lines = append(d.Lines, "A job's next ping resumes it, and a failure then alerts as usual. To keep it quiet, stop the job too.")
		}
		d.Lines = append(d.Lines, "Resuming starts it over, as new. A digest going out anyway lists it, so a forgotten pause gets noticed.")
	case "delete":
		runs, err := s.Store.RunsOfCheck(ctx, v.Access, c.ID)
		if err != nil {
			return d, err
		}
		d.Title, d.Button, d.Danger = "Delete "+c.Name, "Delete check", true
		d.Warning = "This cannot be undone: " + plural(int(runs), "run", "runs") + " with their pings and output go for good."
		d.Lines = []string{"It is gone from every list at once, and an open incident closes without a recovery message. Anyone told it was down reads in the next digest that it was deleted."}
		if !active {
			d.Lines = append(d.Lines, "Its ping URL stops working. A job that still pings its slug with create=1 makes a new check.")
		}
		d.Lines = append(d.Lines, "To stop it and keep its history, pause it instead.")
	case "move":
		d.Title, d.Button = "Move "+c.Name, "Move check"
		d.Warning = "Its runs and output go with it, and that project's viewers can read them."
		d.Lines = []string{"An open incident goes along, and that project's people are told of it."}
		if !active {
			d.Lines = append(d.Lines, "Its ping URL keeps working. A ping by slug needs the new project's ping key, and a job that pings with create=1 makes a new check in this project.")
		}
		d.Projects = otherProjects(v, c)
		if len(d.Projects) == 0 {
			d.NoRoom = "You may change no other project."
		}
	case "ping-url":
		d.Title, d.Button, d.Danger = "Replace the ping URL of "+c.Name, "Replace ping URL", true
		d.Warning = "The old URL stops working at once, so jobs using it stop reporting until they have the new one."
		d.Lines = []string{"Do this when the URL has leaked: anyone who has it can ping this check."}
	}
	if d.NoRoom != "" && action != "move" {
		d.Warning, d.Lines = "", nil
	}
	return d, nil
}

func (s *Server) renderConfirm(w http.ResponseWriter, r *http.Request, v viewer, code int, d confirmData) {
	nav, err := s.nav(r.Context(), v, d.C.ProjectID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	crumbs := []crumb{{d.C.ProjectName, overviewLink("", d.C.ProjectID)}, {d.C.Name, "/checks/" + d.C.ID}, {d.Button, ""}}
	s.render(w, r, code, "check-confirm", page{Title: d.Title, Person: v.Name, Crumbs: crumbs, Nav: nav, Data: d})
}

// confirmPage serves GET /checks/{id}/{action}.
func (s *Server) confirmPage(action string) func(http.ResponseWriter, *http.Request, viewer) {
	return func(w http.ResponseWriter, r *http.Request, v viewer) {
		c, ok := s.forChange(w, r, v)
		if !ok {
			return
		}
		if action == "ping-url" && store.Active(c.Kind) {
			s.fail(w, r, store.ErrNotFound)
			return
		}
		d, err := s.confirmOf(r.Context(), v, c, action)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		s.renderConfirm(w, r, v, http.StatusOK, d)
	}
}

// acted is the end of an action on a check: a refusal says why on its page, and
// otherwise what it did, on the page it redirects to.
func (s *Server) acted(w http.ResponseWriter, r *http.Request, v viewer, c store.CheckView, action string, err error, said notice, to string) {
	if inv, ok := errors.AsType[*store.InvalidError](err); ok && action != "resume" {
		d, cerr := s.confirmOf(r.Context(), v, c, action)
		if cerr != nil {
			s.fail(w, r, cerr)
			return
		}
		d.Problem = sentence(inv.Problem)
		if action == "move" && inv.Field == "probe" {
			d.Problem = "The probe that runs this check does not serve that project."
		}
		s.renderConfirm(w, r, v, http.StatusUnprocessableEntity, d)
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.changed(v, c.ID, action)
	checkID := c.ID
	if action == "delete" {
		checkID = ""
	}
	s.say(r, checkID, said)
	http.Redirect(w, r, to, http.StatusSeeOther)
}

// POST /checks/{id}/pause
func (s *Server) pauseCheck(w http.ResponseWriter, r *http.Request, v viewer) {
	c, ok := s.forChange(w, r, v)
	if !ok {
		return
	}
	err := s.Store.PauseCheck(r.Context(), v.Access, c.ID, s.Now())
	line := "A job's next ping resumes it."
	if store.Active(c.Kind) {
		line = "Allstill does not try it until you resume it."
	}
	s.acted(w, r, v, c, "pause", err, notice{Class: "up", Title: "Paused.", Lines: []string{line}}, "/checks/"+c.ID)
}

// POST /checks/{id}/resume
func (s *Server) resumeCheck(w http.ResponseWriter, r *http.Request, v viewer) {
	c, ok := s.forChange(w, r, v)
	if !ok {
		return
	}
	if c.Status.State != check.Paused {
		// Someone else was first, or a job's ping resumed it.
		s.say(r, c.ID, notice{Class: "up", Title: "It is not paused."})
		http.Redirect(w, r, "/checks/"+c.ID, http.StatusSeeOther)
		return
	}
	err := s.Store.ResumeCheck(r.Context(), v.Access, c.ID, s.Now())
	s.acted(w, r, v, c, "resume", err, notice{Class: "up", Title: "Resumed.", Lines: []string{"It starts over, as new."}}, "/checks/"+c.ID)
}

// POST /checks/{id}/delete
func (s *Server) removeCheck(w http.ResponseWriter, r *http.Request, v viewer) {
	c, ok := s.forChange(w, r, v)
	if !ok {
		return
	}
	err := s.Store.DeleteCheck(r.Context(), v.Access, c.ID, s.Now())
	s.acted(w, r, v, c, "delete", err, notice{Class: "up", Title: "Deleted " + c.Name + "."}, overviewLink("", c.ProjectID))
}

// POST /checks/{id}/move
func (s *Server) moveCheck(w http.ResponseWriter, r *http.Request, v viewer) {
	c, ok := s.forChange(w, r, v)
	if !ok || !s.readPost(w, r) {
		return
	}
	to := r.PostFormValue("project")
	err := s.Store.MoveCheckAs(r.Context(), v.Access, c.ID, to, s.Now())
	s.acted(w, r, v, c, "move", err, notice{Class: "up", Title: "Moved."}, "/checks/"+c.ID)
}

// POST /checks/{id}/ping-url
func (s *Server) replacePingURL(w http.ResponseWriter, r *http.Request, v viewer) {
	c, ok := s.forChange(w, r, v)
	if !ok {
		return
	}
	_, err := s.Store.RotatePingURL(r.Context(), v.Access, c.ID, s.Now())
	s.acted(w, r, v, c, "ping-url", err, notice{Class: "up", Title: "Replaced the ping URL.", Lines: []string{"The old one no longer works. Give your jobs the new one, below."}}, "/checks/"+c.ID)
}

// manageLinks are the buttons in the head of a check's page, for those who may.
type manageLinks struct {
	Edit, Pause, Resume string
	Own                 bool // a probe's own check, which no job pings
}

// manageOf says which buttons a person gets on a check's page: none when they may
// only look, and only Edit on a probe's own check (R19, R25).
func (s *Server) manageOf(ctx context.Context, v viewer, c store.CheckView) (*manageLinks, error) {
	if !v.Access.SeesSecrets(c.ProjectID) {
		return nil, nil
	}
	own, err := s.Store.IsProbesOwnCheck(ctx, v.Access, c.ID)
	if err != nil {
		return nil, err
	}
	m := &manageLinks{Edit: "/checks/" + c.ID + "/edit", Own: own}
	switch {
	case own:
	case c.Status.State == check.Paused:
		m.Resume = "/checks/" + c.ID + "/resume"
	default:
		m.Pause = "/checks/" + c.ID + "/pause"
	}
	return m, nil
}
