// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"html"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/MartijnMul/allstill/internal/active"
	"github.com/MartijnMul/allstill/internal/check"
	"github.com/MartijnMul/allstill/internal/store"
)

// post sends a form as a browser does.
func (b *browser) post(path string, form url.Values) *httptest.ResponseRecorder {
	b.t.Helper()
	req := httptest.NewRequest("POST", site+path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for k, v := range b.header {
		req.Header[k] = v
	}
	for _, c := range b.cookies {
		req.AddCookie(c)
	}
	w := httptest.NewRecorder()
	b.mux.ServeHTTP(w, req)
	return w
}

func (b *browser) get(path string) *httptest.ResponseRecorder {
	b.t.Helper()
	return b.do("GET", path, nil)
}

// member signs in a new person with a role in the fixture's project.
func (f fixture) member(t *testing.T, role store.Role) *browser {
	t.Helper()
	id, err := f.st.CreateMember(t.Context(), store.Command("test"), f.ws, store.NewPerson{Name: "Member " + string(role), TimeZone: "Asia/Tokyo"}, []string{f.project}, role)
	if err != nil {
		t.Fatal(err)
	}
	b := f.browser(t)
	f.register(t, b, id)
	return b
}

// typed is a form with what a person fills in for a new check.
func (f fixture) typed(kind string, more ...string) url.Values {
	form := url.Values{"kind": {kind}, "project": {f.project}, "name": {"new-check"}, "urgency": {"now"}, "every_n": {"1"}, "schedule": {"every"}}
	if kind == "ping" {
		form.Set("every_unit", "d")
		form.Set("grace_n", "1")
		form.Set("grace_unit", "h")
	} else {
		form.Set("every_unit", "m")
	}
	for i := 0; i+1 < len(more); i += 2 {
		form.Set(more[i], more[i+1])
	}
	return form
}

// created adds a check through the form and returns it.
func (f fixture) created(t *testing.T, b *browser, form url.Values) store.CheckView {
	t.Helper()
	w := b.post("/checks/new", form)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("adding a check: %d\n%s", w.Code, w.Body)
	}
	return f.view(t, strings.TrimPrefix(w.Header().Get("Location"), "/checks/"))
}

func (f fixture) view(t *testing.T, id string) store.CheckView {
	t.Helper()
	admin, _ := f.st.AdminAccess(t.Context(), f.ws, "test")
	c, err := f.st.Check(t.Context(), admin, id)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// fieldsOf reads a form's page as a browser would post it, so a test changes only
// what a person would.
func fieldsOf(t *testing.T, page string) url.Values {
	t.Helper()
	form := url.Values{}
	for _, part := range strings.Split(page, "<")[1:] {
		tag, rest, _ := strings.Cut(part, ">")
		attr := func(name string) (string, bool) {
			_, after, ok := strings.Cut(" "+tag, " "+name+`="`)
			if !ok {
				return "", false
			}
			v, _, _ := strings.Cut(after, `"`)
			return html.UnescapeString(v), true
		}
		name, named := attr("name")
		switch {
		case !named:
		case strings.HasPrefix(tag, "input"):
			value, _ := attr("value")
			switch typ, _ := attr("type"); typ {
			case "checkbox":
				if strings.Contains(tag, " checked") {
					form.Set(name, "on")
				}
			case "radio":
				if strings.Contains(tag, " checked") {
					form.Set(name, value)
				}
			case "search":
			default:
				form.Add(name, value)
			}
		case strings.HasPrefix(tag, "textarea"):
			form.Set(name, html.UnescapeString(rest))
		case strings.HasPrefix(tag, "select"):
			form.Set(name, selected(page, name))
		}
	}
	return form
}

// selected is the option chosen in a list.
func selected(page, name string) string {
	_, list, _ := strings.Cut(page, `name="`+name+`"`)
	list, _, _ = strings.Cut(list, "</select>")
	first := ""
	for i, option := range strings.Split(list, "<option")[1:] {
		tag, text, _ := strings.Cut(option, ">")
		value := strings.TrimSpace(strings.Split(text, "<")[0])
		if _, after, ok := strings.Cut(tag, `value="`); ok {
			value, _, _ = strings.Cut(after, `"`)
		}
		if i == 0 {
			first = value
		}
		if strings.Contains(tag, " selected") {
			return value
		}
	}
	return first
}

// Case 1: someone with a passkey link and no shell adds a check and connects a job.
func TestANewPersonAddsACheck(t *testing.T) {
	f := setup(t)
	id, err := f.st.CreateMember(t.Context(), store.Command("test"), f.ws, store.NewPerson{Name: "Ada", TimeZone: "Asia/Tokyo"}, nil, store.Viewer)
	if err != nil {
		t.Fatal(err)
	}
	empty, _, _ := f.st.CreateProject(t.Context(), store.Command("test"), f.ws, "Empty")
	if err := f.st.SetMember(t.Context(), store.Command("test"), f.ws, empty, id, store.Editor); err != nil {
		t.Fatal(err)
	}
	b := f.browser(t)
	f.register(t, b, id)

	overview := string(b.ok("GET", "/", nil))
	if !strings.Contains(overview, "Add your first check") || !strings.Contains(overview, "/checks/new") {
		t.Errorf("an empty overview does not invite: %s", overview)
	}
	if strings.Contains(overview, "allstill import") {
		t.Error("an empty overview tells an editor of a command")
	}
	choose := string(b.ok("GET", "/checks/new?project="+empty, nil))
	for _, words := range []string{"A job that reports in", "A website or API", "A port"} {
		if !strings.Contains(choose, words) {
			t.Errorf("no %q to choose", words)
		}
	}
	form := string(b.ok("GET", "/checks/new?project="+empty+"&kind=ping", nil))
	if !strings.Contains(form, `value="Asia/Tokyo"`) {
		t.Error("the time zone is not the person's")
	}
	if strings.Contains(form, `id="live"`) {
		t.Error("a form on a page that refreshes itself would lose what was typed")
	}
	fields := fieldsOf(t, form)
	fields.Set("name", "nightly-backup")
	c := f.created(t, b, fields)
	if c.Status.State != check.New || c.Grace != time.Hour || c.Interval != 24*time.Hour {
		t.Errorf("%+v", c)
	}
	page := string(b.ok("GET", "/checks/"+c.ID, nil))
	curl, run := strings.Index(page, "curl -fsS"), strings.Index(page, "allstill run --")
	if curl < 0 || run < curl {
		t.Errorf("the curl line does not come first: %s", page)
	}
	if !strings.Contains(page, "waits for its first ping") {
		t.Error("the page does not say what it waits for")
	}
	if strings.Contains(string(b.ok("GET", "/checks/new?kind=ping&project="+f.project, nil)), "Backups") {
		t.Error("the form offers a project the person may only look at")
	}
}

// A viewer sees no way to change anything, and a hand-made post is refused;
// anywhere else it is as if nothing is there (R25).
func TestAViewerCannotChangeAnything(t *testing.T) {
	f := setup(t)
	b := f.member(t, store.Viewer)
	for _, path := range []string{"/", "/checks/" + f.checkID} {
		page := string(b.ok("GET", path, nil))
		for _, words := range []string{"Add check", ">Edit<", ">Pause<", "/edit", "/pause"} {
			if strings.Contains(page, words) {
				t.Errorf("%s shows a viewer %q", path, words)
			}
		}
	}
	if !strings.Contains(string(b.ok("GET", "/", nil)), "attention") {
		t.Error("a viewer is told nothing needs attention")
	}
	if code := b.get("/checks/new").Code; code != http.StatusForbidden {
		t.Errorf("the new form: %d", code)
	}
	if code := b.post("/checks/new", f.typed("ping")).Code; code != http.StatusForbidden {
		t.Errorf("adding a check: %d", code)
	}
	for _, action := range []string{"edit", "pause", "move", "delete", "ping-url"} {
		if code := b.get("/checks/" + f.checkID + "/" + action).Code; code != http.StatusForbidden {
			t.Errorf("the %s page: %d", action, code)
		}
		if code := b.get("/checks/" + f.hidden + "/" + action).Code; code != http.StatusNotFound {
			t.Errorf("the %s page of a check it may not see: %d", action, code)
		}
	}
	for _, action := range []string{"edit", "pause", "resume", "move", "delete", "ping-url"} {
		form := url.Values{"name": {"mine"}, "project": {f.project}}
		if code := b.post("/checks/"+f.checkID+"/"+action, form).Code; code != http.StatusForbidden {
			t.Errorf("%s: %d", action, code)
		}
		if code := b.post("/checks/"+f.hidden+"/"+action, form).Code; code != http.StatusNotFound {
			t.Errorf("%s of a check it may not see: %d", action, code)
		}
	}
	if c := f.view(t, f.checkID); c.Name != "nightly-backup" || c.Status.State == check.Paused {
		t.Errorf("a viewer changed a check: %+v", c)
	}
}

// A post from elsewhere changes nothing (R26).
func TestAFormPostsOnlyFromTheDashboard(t *testing.T) {
	f := setup(t)
	b := f.member(t, store.Editor)
	b.header.Set("Sec-Fetch-Site", "cross-site")
	if code := b.post("/checks/"+f.checkID+"/delete", nil).Code; code != http.StatusForbidden {
		t.Errorf("%d", code)
	}
	b.header.Del("Sec-Fetch-Site")
	if c := f.view(t, f.checkID); c.ID == "" {
		t.Error("deleted from elsewhere")
	}
}

// A refused form keeps what was typed, and says at each field what is wrong (R5).
func TestARefusedFormKeepsWhatWasTyped(t *testing.T) {
	f := setup(t)
	b := f.member(t, store.Editor)
	form := f.typed("ping", "name", "my job", "description", "keeps <this>", "every_n", "soon", "grace_n", "many")
	w := b.post("/checks/new", form)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("%d", w.Code)
	}
	page := w.Body.String()
	for _, words := range []string{`value="my job"`, "keeps &lt;this&gt;", `value="soon"`, `value="many"`, "Give a whole number"} {
		if !strings.Contains(page, words) {
			t.Errorf("no %q in %s", words, page)
		}
	}
	w = b.post("/checks/new", f.typed("ping", "slug", "Not A Slug"))
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "Must be lower-case letters") || !strings.Contains(w.Body.String(), `value="Not A Slug"`) {
		t.Errorf("%d %s", w.Code, w.Body)
	}
	w = b.post("/checks/new", f.typed("ping", "schedule", "cron", "cron", "not cron", "time_zone", "Europe/Amsterdam"))
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), `value="not cron"`) {
		t.Errorf("%d", w.Code)
	}
	w = b.post("/checks/new", f.typed("http", "url", "ftp://example.com/x"))
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "Must start with http") {
		t.Errorf("%d %s", w.Code, w.Body)
	}
	w = b.post("/checks/new", f.typed("http", "url", "https://example.com/", "warn_days", "5", "fail_days", "9"))
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "fewer than the days for a warning") {
		t.Errorf("%d %s", w.Code, w.Body)
	}
}

// Case 7: nobody is told what a name resolves to, or that it is the server's own.
func TestAnAddressTheServerMayNotReach(t *testing.T) {
	f := setup(t)
	b := f.member(t, store.Editor)
	f.srv.Try = func(context.Context, active.Check) active.Result {
		t.Error("tried an address it may not reach")
		return active.Result{}
	}
	for _, address := range []string{"http://10.0.0.5/health", "http://127.0.0.1:8000/", "http://[::1]/", "http://169.254.169.254/latest"} {
		w := b.post("/checks/new", f.typed("http", "url", address))
		page := w.Body.String()
		if w.Code != http.StatusUnprocessableEntity || !strings.Contains(page, "not on the public internet") || !strings.Contains(page, "use a probe") {
			t.Errorf("%s: %d %s", address, w.Code, page)
		}
		if strings.Contains(page, "own address") || strings.Contains(page, "allow-private") {
			t.Errorf("%s: says too much", address)
		}
	}
	w := b.post("/checks/new", f.typed("tcp", "host", "127.0.0.1", "port", "22"))
	if !strings.Contains(w.Body.String(), "not on the public internet") {
		t.Errorf("%d", w.Code)
	}
	// The server's ranges are the administrator's to open.
	f.srv.Allow = []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}
	f.srv.Try = func(context.Context, active.Check) active.Result {
		return active.Result{Outcome: check.Passed, Measured: map[string]float64{"status": 200, "ms": 12}}
	}
	f.created(t, b, f.typed("http", "url", "http://192.0.2.7/"))
}

// Trying a new address says what it found, and counts for nothing (R4, case 6).
func TestTheTry(t *testing.T) {
	f := setup(t)
	b := f.member(t, store.Editor)
	tries := 0
	f.srv.Try = func(ctx context.Context, c active.Check) active.Result {
		tries++
		if dl, ok := ctx.Deadline(); !ok || time.Until(dl) > tryTimeout+time.Second {
			t.Errorf("a try without its limit: %v", dl)
		}
		if c.HTTP.Timeout > int(tryTimeout/time.Second) {
			t.Errorf("a try that may take %d s", c.HTTP.Timeout)
		}
		return active.Result{Outcome: check.Failed, Reason: "the host name does not exist", Measured: map[string]float64{}}
	}
	c := f.created(t, b, f.typed("http", "url", "https://www.example.com/", "timeout", "60"))
	if tries != 1 {
		t.Errorf("%d tries", tries)
	}
	page := string(b.ok("GET", "/checks/"+c.ID, nil))
	for _, words := range []string{"the host name does not exist", "first real attempt comes by", "alerts nobody until it has worked once"} {
		if !strings.Contains(page, words) {
			t.Errorf("no %q in %s", words, page)
		}
	}
	if c := f.view(t, c.ID); len(c.Recent) != 0 || c.Status.State != check.New {
		t.Errorf("the try counted: %+v", c.Status)
	}
	// Another session does not get the result.
	other := f.member(t, store.Editor)
	if strings.Contains(string(other.ok("GET", "/checks/"+c.ID, nil)), "the host name does not exist") {
		t.Error("the result reached another session")
	}
	// Fixing the address tries again; renaming does not.
	fields := fieldsOf(t, string(b.ok("GET", "/checks/"+c.ID+"/edit", nil)))
	fields.Set("url", "https://example.com/")
	if w := b.post("/checks/"+c.ID+"/edit", fields); w.Code != http.StatusSeeOther {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if tries != 2 {
		t.Errorf("%d tries after fixing it", tries)
	}
	fields = fieldsOf(t, string(b.ok("GET", "/checks/"+c.ID+"/edit", nil)))
	fields.Set("name", "renamed")
	if w := b.post("/checks/"+c.ID+"/edit", fields); w.Code != http.StatusSeeOther {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if tries != 2 {
		t.Errorf("%d tries after a rename", tries)
	}
	// A person gets a few tries a minute, and the check is saved all the same.
	for range triesPerMinute {
		f.created(t, b, f.typed("http", "url", "https://www.example.com/"))
	}
	if tries > triesPerMinute {
		t.Errorf("%d tries in a minute", tries)
	}
	last := f.created(t, b, f.typed("http", "url", "https://www.example.com/"))
	if page := string(b.ok("GET", "/checks/"+last.ID, nil)); !strings.Contains(page, "Not tried now") {
		t.Errorf("%s", page)
	}
}

// A form saves what it shows: what it does not show stays, a box left unticked
// turns off, and a rename leaves a check's schedule alone (R8, R10, case 16).
func TestAChangeSavesOnlyWhatItsFormShows(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	admin, _ := f.st.AdminAccess(ctx, f.ws, "test")
	created, err := f.st.CreateCheck(ctx, admin, f.project, store.NewCheck{Name: "imported", Kind: "http", Interval: time.Hour, Settings: map[string]any{
		"url": "https://example.com/secret-token", "follow_redirects": true, "kept_from_an_import": "yes", "warn_days": 14,
	}})
	if err != nil {
		t.Fatal(err)
	}
	b := f.member(t, store.Editor)
	before := f.view(t, created.ID)
	fields := fieldsOf(t, string(b.ok("GET", "/checks/"+created.ID+"/edit", nil)))
	fields.Set("name", "renamed")
	if w := b.post("/checks/"+created.ID+"/edit", fields); w.Code != http.StatusSeeOther {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	after := f.view(t, created.ID)
	if after.Name != "renamed" || string(after.Settings) != string(before.Settings) {
		t.Errorf("a rename changed its settings: %s", after.Settings)
	}
	if !after.Status.Deadline.Equal(before.Status.Deadline) {
		t.Error("a rename moved its deadline")
	}
	fields = fieldsOf(t, string(b.ok("GET", "/checks/"+created.ID+"/edit", nil)))
	fields.Del("follow")
	if w := b.post("/checks/"+created.ID+"/edit", fields); w.Code != http.StatusSeeOther {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var settings map[string]any
	json.Unmarshal(f.view(t, created.ID).Settings, &settings)
	if _, on := settings["follow_redirects"]; on || settings["kept_from_an_import"] != "yes" {
		t.Errorf("%v", settings)
	}
}

// A check's kind stays what it is, and a post cannot make it otherwise (R7).
func TestAKindCannotChange(t *testing.T) {
	f := setup(t)
	b := f.member(t, store.Editor)
	fields := fieldsOf(t, string(b.ok("GET", "/checks/"+f.checkID+"/edit", nil)))
	fields.Set("kind", "http")
	fields.Set("url", "https://example.com/")
	if w := b.post("/checks/"+f.checkID+"/edit", fields); w.Code != http.StatusSeeOther {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if c := f.view(t, f.checkID); c.Kind != "ping" {
		t.Errorf("%s", c.Kind)
	}
}

// Case 8: the second to save is told what changed, and keeps what they typed. A
// ping does not make a page out of date (R9).
func TestTwoEditorsAtOnce(t *testing.T) {
	f := setup(t)
	first, second := f.member(t, store.Editor), f.member(t, store.Editor)
	path := "/checks/" + f.checkID + "/edit"
	fromFirst := fieldsOf(t, string(first.ok("GET", path, nil)))
	fromSecond := fieldsOf(t, string(second.ok("GET", path, nil)))

	uuid := strings.TrimPrefix(f.pingURL, site+"/ping/")
	if _, err := f.st.Ping(t.Context(), store.PingIn{CheckUUID: uuid, Kind: check.Success, ExitStatus: 0, Method: "GET", At: time.Now()}); err != nil {
		t.Fatal(err)
	}
	fromFirst.Set("name", "first-name")
	fromFirst.Set("tags", "one")
	if w := first.post(path, fromFirst); w.Code != http.StatusSeeOther {
		t.Fatalf("a ping made a page out of date: %d", w.Code)
	}
	fromSecond.Set("description", "typed by the second")
	w := second.post(path, fromSecond)
	page := w.Body.String()
	if w.Code != http.StatusConflict || !strings.Contains(page, "Name, Tags") || !strings.Contains(page, "typed by the second") {
		t.Fatalf("%d %s", w.Code, page)
	}
	if c := f.view(t, f.checkID); c.Description != "" || c.Name != "first-name" {
		t.Errorf("an out-of-date form saved: %+v", c)
	}
	// Saved again from that page, it is theirs.
	again := fieldsOf(t, page)
	if w := second.post(path, again); w.Code != http.StatusSeeOther {
		t.Fatalf("%d", w.Code)
	}
	if c := f.view(t, f.checkID); c.Description != "typed by the second" {
		t.Errorf("%+v", c)
	}
}

// Moving a nightly job says when it is next expected (case 3, R6), and tells the
// scheduler of the new deadline.
func TestChangingASchedule(t *testing.T) {
	f := setup(t)
	b := f.member(t, store.Editor)
	var nudged time.Time
	f.srv.Nudge = func(d time.Time) { nudged = d }
	fields := fieldsOf(t, string(b.ok("GET", "/checks/"+f.checkID+"/edit", nil)))
	fields.Set("schedule", "every")
	fields.Set("every_n", "2")
	fields.Set("every_unit", "h")
	fields.Set("grace_n", "30")
	fields.Set("grace_unit", "m")
	if w := b.post("/checks/"+f.checkID+"/edit", fields); w.Code != http.StatusSeeOther {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	c := f.view(t, f.checkID)
	if c.Interval != 2*time.Hour || c.Grace != 30*time.Minute || c.Cron != "" {
		t.Errorf("%+v", c)
	}
	if nudged.IsZero() {
		t.Error("the scheduler was not told")
	}
	if page := string(b.ok("GET", "/checks/"+f.checkID, nil)); !strings.Contains(page, "next expected at") {
		t.Errorf("%s", page)
	}
}

// Only an owner turns critical on; an editor can turn it off (C4).
func TestWhoMayWakePeople(t *testing.T) {
	f := setup(t)
	editor := f.member(t, store.Editor)
	path := "/checks/" + f.checkID + "/edit"
	page := string(editor.ok("GET", path, nil))
	if strings.Contains(page, `name="critical"`) {
		t.Error("an editor is offered the box")
	}
	fields := fieldsOf(t, page)
	fields.Add("box", "critical")
	fields.Set("critical", "on")
	w := editor.post(path, fields)
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "Only an owner can") {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if w := editor.post("/checks/new", f.typed("ping", "box", "critical", "critical", "on")); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("%d", w.Code)
	}
	owner := f.member(t, store.Owner)
	page = string(owner.ok("GET", path, nil))
	fields = fieldsOf(t, page)
	fields.Set("critical", "on")
	if w := owner.post(path, fields); w.Code != http.StatusSeeOther {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if !f.view(t, f.checkID).Critical {
		t.Error("not critical")
	}
	fields = fieldsOf(t, string(editor.ok("GET", path, nil)))
	if fields.Get("critical") == "" {
		t.Error("an editor does not see it is on")
	}
	fields.Del("critical")
	if w := editor.post(path, fields); w.Code != http.StatusSeeOther {
		t.Fatalf("%d", w.Code)
	}
	if f.view(t, f.checkID).Critical {
		t.Error("still critical")
	}
}

// Pausing, resuming, replacing a ping URL and deleting, each after its page.
func TestTheActionsOnACheck(t *testing.T) {
	f := setup(t)
	b := f.member(t, store.Editor)
	id := "/checks/" + f.checkID
	if page := string(b.ok("GET", id, nil)); !strings.Contains(page, ">Edit<") || !strings.Contains(page, ">Pause<") {
		t.Errorf("%s", page)
	}
	if page := string(b.ok("GET", id+"/pause", nil)); !strings.Contains(page, "next ping resumes it") {
		t.Errorf("%s", page)
	}
	if f.view(t, f.checkID).Status.State == check.Paused {
		t.Error("the page itself paused it")
	}
	if w := b.post(id+"/pause", nil); w.Code != http.StatusSeeOther {
		t.Fatalf("%d", w.Code)
	}
	if f.view(t, f.checkID).Status.State != check.Paused {
		t.Error("not paused")
	}
	if page := string(b.ok("GET", id, nil)); !strings.Contains(page, ">Resume<") || strings.Contains(page, ">Pause<") {
		t.Errorf("%s", page)
	}
	if w := b.post(id+"/resume", nil); w.Code != http.StatusSeeOther {
		t.Fatalf("%d", w.Code)
	}
	if f.view(t, f.checkID).Status.State != check.New {
		t.Error("not new")
	}
	if w := b.post(id+"/resume", nil); w.Code != http.StatusSeeOther {
		t.Fatalf("%d", w.Code)
	}
	if page := string(b.ok("GET", id, nil)); !strings.Contains(page, "It is not paused") {
		t.Errorf("%s", page)
	}

	before := f.view(t, f.checkID).PingUUID
	if page := string(b.ok("GET", id+"/ping-url", nil)); !strings.Contains(page, "stops working at once") {
		t.Errorf("%s", page)
	}
	if w := b.post(id+"/ping-url", nil); w.Code != http.StatusSeeOther {
		t.Fatalf("%d", w.Code)
	}
	if f.view(t, f.checkID).PingUUID == before {
		t.Error("the same URL")
	}

	page := string(b.ok("GET", id+"/delete", nil))
	if !strings.Contains(page, "1 run") || !strings.Contains(page, "create=1") {
		t.Errorf("%s", page)
	}
	if w := b.post(id+"/delete", nil); w.Code != http.StatusSeeOther {
		t.Fatalf("%d", w.Code)
	}
	if code := b.get(id).Code; code != http.StatusNotFound {
		t.Errorf("a deleted check: %d", code)
	}
	if page := string(b.ok("GET", "/?project="+f.project, nil)); !strings.Contains(page, "Deleted nightly-backup") {
		t.Errorf("%s", page)
	}
}

// Moving asks to confirm what goes with it, and needs rights on both sides (R13).
func TestMovingACheck(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	id, _ := f.st.CreateMember(ctx, store.Command("test"), f.ws, store.NewPerson{Name: "Both"}, []string{f.project}, store.Editor)
	other, _, _ := f.st.CreateProject(ctx, store.Command("test"), f.ws, "Other")
	looks, _, _ := f.st.CreateProject(ctx, store.Command("test"), f.ws, "Looks")
	f.st.SetMember(ctx, store.Command("test"), f.ws, other, id, store.Editor)
	f.st.SetMember(ctx, store.Command("test"), f.ws, looks, id, store.Viewer)
	b := f.browser(t)
	f.register(t, b, id)

	path := "/checks/" + f.checkID + "/move"
	page := string(b.ok("GET", path, nil))
	if !strings.Contains(page, "viewers can read them") || !strings.Contains(page, "create=1") || !strings.Contains(page, ">Other</option>") || strings.Contains(page, ">Looks</option>") {
		t.Errorf("%s", page)
	}
	if code := b.post(path, url.Values{"project": {looks}}).Code; code != http.StatusForbidden {
		t.Errorf("to a project it only looks at: %d", code)
	}
	if code := b.post(path, url.Values{"project": {"nowhere"}}).Code; code != http.StatusNotFound {
		t.Errorf("to a project it cannot see: %d", code)
	}
	if w := b.post(path, url.Values{"project": {other}}); w.Code != http.StatusSeeOther {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if c := f.view(t, f.checkID); c.ProjectID != other {
		t.Errorf("%+v", c)
	}
}

// Case 13 and case 9: a probe's own check has its rules, and a probe is offered
// only for the projects it serves.
func TestProbesInTheForms(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	admin, _ := f.st.AdminAccess(ctx, f.ws, "test")
	probeID, _, err := f.st.CreateProbe(ctx, admin, "web-1", f.project, nil)
	if err != nil {
		t.Fatal(err)
	}
	elsewhere, _, _ := f.st.CreateProject(ctx, store.Command("test"), f.ws, "Elsewhere")
	if _, _, err := f.st.CreateProbe(ctx, admin, "far-1", elsewhere, nil); err != nil {
		t.Fatal(err)
	}
	var own string
	checks, _ := f.st.Checks(ctx, admin)
	for _, c := range checks {
		if c.Name == "Probe web-1" {
			own = c.ID
		}
	}
	b := f.member(t, store.Editor)
	f.srv.Try = func(context.Context, active.Check) active.Result {
		t.Error("tried on the server what a probe runs")
		return active.Result{}
	}

	page := string(b.ok("GET", "/checks/"+own, nil))
	if !strings.Contains(page, ">Edit<") || strings.Contains(page, ">Pause<") {
		t.Errorf("%s", page)
	}
	page = string(b.ok("GET", "/checks/"+own+"/edit", nil))
	for _, words := range []string{`name="every_n"`, `name="urgency"`, "/delete", "/move"} {
		if strings.Contains(page, words) {
			t.Errorf("a probe's own check offers %q", words)
		}
	}
	fields := fieldsOf(t, page)
	fields.Set("name", "Probe web-1, renamed")
	if w := b.post("/checks/"+own+"/edit", fields); w.Code != http.StatusSeeOther {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	for action, why := range map[string]string{"pause": "cannot be paused", "delete": "cannot be deleted"} {
		w := b.post("/checks/"+own+"/"+action, nil)
		if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), why) {
			t.Errorf("%s: %d %s", action, w.Code, w.Body)
		}
	}
	fields = fieldsOf(t, string(b.ok("GET", "/checks/"+own+"/edit", nil)))
	fields.Set("urgency", "none")
	if w := b.post("/checks/"+own+"/edit", fields); w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "only an owner of its team can change this") {
		t.Errorf("%d %s", w.Code, w.Body)
	}
	fields.Del("urgency")
	fields.Set("every_n", "5")
	fields.Set("every_unit", "h")
	if w := b.post("/checks/"+own+"/edit", fields); w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "so its schedule stays.</p>\n  <p class=\"field-error\">") {
		t.Errorf("%d %s", w.Code, w.Body)
	}

	page = string(b.ok("GET", "/checks/new?kind=http&project="+f.project, nil))
	if !strings.Contains(page, "The probe web-1") || strings.Contains(page, "far-1") {
		t.Errorf("%s", page)
	}
	// A private address is its to reach, and only it can try it.
	c := f.created(t, b, f.typed("http", "url", "http://10.0.0.5/", "probe", probeID))
	if c.ProbeID != probeID {
		t.Errorf("%+v", c)
	}
	if page := string(b.ok("GET", "/checks/"+c.ID, nil)); !strings.Contains(page, "Only the probe web-1 can try") {
		t.Errorf("%s", page)
	}
	far, _ := f.st.ProbeIDByName(ctx, f.ws, f.project, "far-1")
	w := b.post("/checks/new", f.typed("http", "url", "http://10.0.0.5/", "probe", far))
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "Does not serve") {
		t.Errorf("%d %s", w.Code, w.Body)
	}
}

// A TCP check's certificate settings go with TLS.
func TestATCPForm(t *testing.T) {
	f := setup(t)
	b := f.member(t, store.Editor)
	f.srv.Try = func(context.Context, active.Check) active.Result {
		return active.Result{Outcome: check.Passed, Measured: map[string]float64{"ms": 4}}
	}
	c := f.created(t, b, f.typed("tcp", "host", "mail.example.com", "port", "993", "box", "tls", "tls", "on", "warn_days", "20"))
	var settings map[string]any
	json.Unmarshal(c.Settings, &settings)
	if settings["host"] != "mail.example.com" || settings["port"] != 993.0 || settings["tls"] != true || settings["warn_days"] != 20.0 {
		t.Errorf("%v", settings)
	}
	fields := fieldsOf(t, string(b.ok("GET", "/checks/"+c.ID+"/edit", nil)))
	if fields.Get("warn_days") != "20" || fields.Get("tls") == "" {
		t.Errorf("%v", fields)
	}
	fields.Del("tls")
	if w := b.post("/checks/"+c.ID+"/edit", fields); w.Code != http.StatusSeeOther {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	settings = nil
	json.Unmarshal(f.view(t, c.ID).Settings, &settings)
	if _, on := settings["warn_days"]; on || settings["tls"] != nil {
		t.Errorf("%v", settings)
	}
	w := b.post("/checks/new", f.typed("tcp", "host", "mail.example.com", "port", "many"))
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), `value="many"`) {
		t.Errorf("%d", w.Code)
	}
}

// A secret never travels in a page's address (R12 of the checks spec).
func TestASecretStaysOutOfAddresses(t *testing.T) {
	f := setup(t)
	b := f.member(t, store.Editor)
	secret := "https://example.com/hook/secret-token"
	w := b.post("/checks/new", f.typed("http", "url", secret))
	if strings.Contains(w.Header().Get("Location"), "secret-token") {
		t.Error("a token in a redirect")
	}
	viewer := f.member(t, store.Viewer)
	c := strings.TrimPrefix(w.Header().Get("Location"), "/checks/")
	if strings.Contains(string(viewer.ok("GET", "/checks/"+c, nil)), "secret-token") {
		t.Error("a viewer sees its token")
	}
}

// Each change is in the log with who made it, without an address (R29).
func TestChangesAreLogged(t *testing.T) {
	f := setup(t)
	var log strings.Builder
	f.srv.Log = slog.New(slog.NewTextHandler(&log, nil))
	b := f.member(t, store.Editor)
	c := f.created(t, b, f.typed("http", "url", "https://example.com/hook/secret-token"))
	b.post("/checks/"+c.ID+"/pause", nil)
	for _, words := range []string{"change=added", "change=pause", "check=" + c.ID, "person="} {
		if !strings.Contains(log.String(), words) {
			t.Errorf("no %q in %s", words, &log)
		}
	}
	if strings.Contains(log.String(), "secret-token") {
		t.Error("a token in the log")
	}
}

// The overview's words fit the kind of check and the person.
func TestTheOverviewsWords(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	alone, _, _ := f.st.CreateProject(ctx, store.Command("test"), f.ws, "Sites")
	admin, _ := f.st.AdminAccess(ctx, f.ws, "test")
	if _, err := f.st.CreateCheck(ctx, admin, alone, store.NewCheck{Name: "shop", Kind: "http", Interval: time.Minute, Settings: map[string]any{"url": "https://shop.example.com/"}}); err != nil {
		t.Fatal(err)
	}
	id, _ := f.st.CreateMember(ctx, store.Command("test"), f.ws, store.NewPerson{Name: "Sites only"}, []string{alone}, store.Viewer)
	b := f.browser(t)
	f.register(t, b, id)
	page := string(b.ok("GET", "/", nil))
	if !strings.Contains(page, "1 check is not checked yet") || strings.Contains(page, "pinged") {
		t.Errorf("%s", page)
	}
	// A viewer of a failing check is not told it needs them.
	if err := f.st.SetMember(ctx, store.Command("test"), f.ws, f.project, id, store.Viewer); err != nil {
		t.Fatal(err)
	}
	uuid := strings.TrimPrefix(f.pingURL, site+"/ping/")
	if _, err := f.st.Ping(ctx, store.PingIn{CheckUUID: uuid, Kind: check.Fail, ExitStatus: 1, Method: "GET", At: time.Now()}); err != nil {
		t.Fatal(err)
	}
	page = string(b.ok("GET", "/", nil))
	if !strings.Contains(page, "1 check needs attention") || strings.Contains(page, "Needs you") {
		t.Errorf("%s", page)
	}
}

// Saving without touching anything changes nothing, whatever a command or an import
// stored: a space at the edge of a value, a line ending, an interval in seconds
// (R8, R10).
func TestAnUntouchedFormChangesNothing(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	admin, _ := f.st.AdminAccess(ctx, f.ws, "test")
	made := map[string]store.NewCheck{
		"cron": {Name: "spaced", Cron: "0 5 * * * ", TimeZone: "Europe/Amsterdam", Grace: time.Hour, Description: "two\r\nlines "},
		"secs": {Name: "seconds", Interval: 90 * time.Second, Grace: 90 * time.Second},
		"json": {Name: "json", Kind: "http", Interval: time.Hour, Settings: map[string]any{
			"url": "https://example.com/ ", "json_field": map[string]any{"pointer": "/status", "equals": "ok "},
		}},
	}
	b := f.member(t, store.Editor)
	for name, n := range made {
		created, err := f.st.CreateCheck(ctx, admin, f.project, n)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		before := f.view(t, created.ID)
		path := "/checks/" + created.ID + "/edit"
		page := string(b.ok("GET", path, nil))
		fields := fieldsOf(t, page)
		if fields.Get("description") != "" {
			// A text area comes back from a browser with CR and LF.
			fields.Set("description", strings.ReplaceAll(fields.Get("description"), "\n", "\r\n"))
		}
		if w := b.post(path, fields); w.Code != http.StatusSeeOther {
			t.Fatalf("%s: %d %s", name, w.Code, w.Body)
		}
		after := f.view(t, created.ID)
		if after.Version != before.Version || string(after.Settings) != string(before.Settings) || after.Cron != before.Cron || after.Interval != before.Interval {
			t.Errorf("%s: an untouched form changed it: %+v", name, after)
		}
		// A rename leaves the rest as it was too.
		fields = fieldsOf(t, string(b.ok("GET", path, nil)))
		fields.Set("name", name+"-renamed")
		if w := b.post(path, fields); w.Code != http.StatusSeeOther {
			t.Fatalf("%s: %d %s", name, w.Code, w.Body)
		}
		after = f.view(t, created.ID)
		if string(after.Settings) != string(before.Settings) || after.Cron != before.Cron || after.Interval != before.Interval || after.Grace != before.Grace || !after.Status.Deadline.Equal(before.Status.Deadline) {
			t.Errorf("%s: a rename changed it: %+v", name, after)
		}
	}
}

// An editor of one project has no part in another: it is as if nothing is there.
func TestAnEditorOfAnotherProject(t *testing.T) {
	f := setup(t)
	b := f.member(t, store.Editor)
	for _, action := range []string{"edit", "pause", "resume", "move", "delete", "ping-url"} {
		if action != "resume" {
			if code := b.get("/checks/" + f.hidden + "/" + action).Code; code != http.StatusNotFound {
				t.Errorf("the %s page: %d", action, code)
			}
		}
		if code := b.post("/checks/"+f.hidden+"/"+action, url.Values{"name": {"mine"}, "project": {f.project}}).Code; code != http.StatusNotFound {
			t.Errorf("%s: %d", action, code)
		}
	}
	admin, _ := f.st.AdminAccess(t.Context(), f.ws, "test")
	hidden, _ := f.st.Check(t.Context(), admin, f.hidden)
	if code := b.post("/checks/new", f.typed("ping", "project", hidden.ProjectID)).Code; code != http.StatusNotFound {
		t.Errorf("adding to it: %d", code)
	}
	if code := b.post("/checks/"+f.checkID+"/move", url.Values{"project": {hidden.ProjectID}}).Code; code != http.StatusNotFound {
		t.Errorf("moving to it: %d", code)
	}
}

// The first administrator of a new installation is told what to do, not to ask
// their administrator.
func TestANewInstallation(t *testing.T) {
	ctx := t.Context()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "allstill.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ws, _ := st.Workspace(ctx)
	ada, _ := st.CreatePerson(ctx, store.Command("test"), ws, store.NewPerson{Name: "Ada", WorkspaceAdmin: true})
	base, _ := ParseBaseURL(site)
	srv, err := New(st, ws, base, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	f := fixture{st: st, srv: srv, mux: http.NewServeMux(), ws: ws}
	srv.Register(f.mux)
	b := f.browser(t)
	f.register(t, b, ada)
	page := string(b.ok("GET", "/", nil))
	if !strings.Contains(page, `href="/projects/new"`) || strings.Contains(page, "Your administrator") {
		t.Errorf("%s", page)
	}
}

// A person gets a limited number of new addresses looked up, which each say whether
// a name is private (R30).
func TestLookupsAreLimited(t *testing.T) {
	f := setup(t)
	b := f.member(t, store.Editor)
	var last *httptest.ResponseRecorder
	for range lookupsPerMinute + 1 {
		last = b.post("/checks/new", f.typed("http", "url", "http://10.0.0.5/"))
	}
	if page := last.Body.String(); !strings.Contains(page, "many new addresses") || strings.Contains(page, "not on the public internet") {
		t.Errorf("%s", page)
	}
}

// A probe's own check is not offered what it cannot have, even by address.
func TestAProbesOwnCheckPages(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	admin, _ := f.st.AdminAccess(ctx, f.ws, "test")
	if _, _, err := f.st.CreateProbe(ctx, admin, "web-1", f.project, nil); err != nil {
		t.Fatal(err)
	}
	var own string
	checks, _ := f.st.Checks(ctx, admin)
	for _, c := range checks {
		if c.Name == "Probe web-1" {
			own = c.ID
		}
	}
	b := f.member(t, store.Editor)
	if page := string(b.ok("GET", "/checks/"+own, nil)); strings.Contains(page, "Connect your job") {
		t.Error("a probe's own check is offered to a job")
	}
	for _, action := range []string{"pause", "delete"} {
		if page := string(b.ok("GET", "/checks/"+own+"/"+action, nil)); strings.Contains(page, `class="primary"`) || strings.Contains(page, `class="danger"`) || !strings.Contains(page, "so it stays") {
			t.Errorf("%s: %s", action, page)
		}
	}
}

// Saving a form nobody changed saves nothing, so nobody else's form goes out of date.
func TestSavingNothing(t *testing.T) {
	f := setup(t)
	b := f.member(t, store.Editor)
	path := "/checks/" + f.checkID + "/edit"
	before := f.view(t, f.checkID)
	if w := b.post(path, fieldsOf(t, string(b.ok("GET", path, nil)))); w.Code != http.StatusSeeOther {
		t.Fatalf("%d", w.Code)
	}
	if after := f.view(t, f.checkID); after.Version != before.Version {
		t.Errorf("version %d, was %d", after.Version, before.Version)
	}
	if page := string(b.ok("GET", "/checks/"+f.checkID, nil)); !strings.Contains(page, "Nothing changed") {
		t.Errorf("%s", page)
	}
}

// logLines reads what people did from the log, in order, for the tests here.
func (f fixture) logLines(t *testing.T) [][2]string {
	t.Helper()
	conn, err := sql.Open("sqlite", f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	rows, err := conn.QueryContext(t.Context(), `SELECT kind, by_name FROM log_entries WHERE by_kind = 'person' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out [][2]string
	for rows.Next() {
		var l [2]string
		if err := rows.Scan(&l[0], &l[1]); err != nil {
			t.Fatal(err)
		}
		out = append(out, l)
	}
	return out
}

// Every route that changes a check is in the log by the person who used it, and one
// form is one entry (intent/activity-log/spec.md, R6, R15).
func TestAChangeInTheDashboardIsInTheLog(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	// Someone who may edit both projects can move a check between them.
	person, err := f.st.CreateMember(ctx, store.Command("test"), f.ws, store.NewPerson{Name: "Grace"}, []string{f.project}, store.Editor)
	if err != nil {
		t.Fatal(err)
	}
	other, _, err := f.st.CreateProject(ctx, store.Command("test"), f.ws, "Elsewhere")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.st.SetMember(ctx, store.Command("test"), f.ws, other, person, store.Editor); err != nil {
		t.Fatal(err)
	}
	b := f.browser(t)
	f.register(t, b, person)
	before := len(f.logLines(t))
	id := "/checks/" + f.checkID
	post := func(path string, form url.Values) {
		t.Helper()
		if w := b.post(path, form); w.Code != http.StatusSeeOther {
			t.Fatalf("%s: %d: %s", path, w.Code, w.Body)
		}
	}
	fields := fieldsOf(t, string(b.ok("GET", id+"/edit", nil)))
	fields.Set("name", "renamed")
	post(id+"/edit", fields)
	post(id+"/pause", nil)
	post(id+"/resume", nil)
	post(id+"/ping-url", nil)
	post("/checks/new", f.typed("ping", "name", "added with its alerting", "urgency", "digest"))
	post(id+"/move", url.Values{"project": {other}})
	post(id+"/delete", nil)
	var got []string
	for _, l := range f.logLines(t)[before:] {
		if l[1] != "Grace" {
			t.Errorf("%v", l)
		}
		got = append(got, l[0])
	}
	want := []string{
		"check.changed", "check.paused", "check.resumed", "check.ping_url_replaced", "check.created",
		"check.moved_out", "check.moved_in", "check.deleted",
	}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}
