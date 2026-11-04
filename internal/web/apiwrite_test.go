// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MartijnMul/allstill/internal/active"
	"github.com/MartijnMul/allstill/internal/api"
	"github.com/MartijnMul/allstill/internal/apiclient"
	"github.com/MartijnMul/allstill/internal/check"
	"github.com/MartijnMul/allstill/internal/store"
)

// send asks the API with a token and a JSON body, as a command line does.
func send(f fixture, method, path, token string, body any, headers ...string) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(method, site+"/api/v1"+path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, req)
	return w
}

func answer[T any](t *testing.T, w *httptest.ResponseRecorder, code int) T {
	t.Helper()
	if w.Code != code {
		t.Fatalf("answered %d, not %d: %s", w.Code, code, w.Body)
	}
	var out T
	// A field the description does not have fails here.
	d := json.NewDecoder(bytes.NewReader(w.Body.Bytes()))
	d.DisallowUnknownFields()
	if err := d.Decode(&out); err != nil {
		t.Fatalf("%v: %s", err, w.Body)
	}
	return out
}

// keyFor makes a project's key, as an administrator on the server would.
func keyFor(t *testing.T, f fixture, projectID, name, scope string) string {
	t.Helper()
	admins, err := f.st.AdminAccess(t.Context(), f.ws, "test")
	if err != nil {
		t.Fatal(err)
	}
	_, key, err := f.st.CreateAPIKey(t.Context(), admins, projectID, name, scope, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func customer(t *testing.T, f fixture) string {
	t.Helper()
	p, err := f.st.ProjectByName(t.Context(), f.ws, "Customer")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// tryAlways answers each try as up, and counts them.
func tryAlways(f *fixture) *int {
	tries := 0
	f.srv.Try = func(context.Context, active.Check) active.Result {
		tries++
		return active.Result{Outcome: check.Passed, Measured: map[string]float64{"status": 200, "ms": 12, "cert_days": 40}}
	}
	return &tries
}

// One table of who may do what, for each operation and each way of asking (R20, R36).
func TestWhoChangesWhat(t *testing.T) {
	f := setup(t)
	f.srv.Now = time.Now
	tryAlways(&f)
	ctx := t.Context()
	editor := someoneIn(t, f, "Edith", f.project, store.Editor)
	owner := someoneIn(t, f, "Olga", f.project, store.Owner)
	reader := someoneIn(t, f, "Rob", f.project, store.Owner)
	admins, _ := f.st.AdminAccess(ctx, f.ws, "test")

	// A check each operation can work on, and a project it can go to, which everyone
	// who may edit here may edit too.
	fresh := func() store.CreatedCheck {
		t.Helper()
		c, err := f.st.CreateCheck(ctx, admins, f.project, store.NewCheck{Name: "job", Interval: time.Hour, Grace: time.Hour})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	both, _, _ := f.st.CreateProject(ctx, store.Command("test"), f.ws, "Both")
	for _, p := range []string{editor, owner} {
		f.st.SetMember(ctx, store.Command("test"), f.ws, both, p, store.Editor)
	}

	memberAdmin, err := f.st.CreatePerson(ctx, store.Command("test"), f.ws, store.NewPerson{Name: "Meg", WorkspaceAdmin: true})
	if err != nil {
		t.Fatal(err)
	}
	f.st.SetMember(ctx, store.Command("test"), f.ws, f.project, memberAdmin, store.Viewer)

	const ok, notFound, refused = 0, http.StatusNotFound, http.StatusForbidden
	asked := []struct {
		who   string
		token string
		want  int
	}{
		{"a viewer", tokenFor(t, f, f.bob, store.ScopeManage), refused},
		{"an editor", tokenFor(t, f, editor, store.ScopeManage), ok},
		{"an owner", tokenFor(t, f, owner, store.ScopeManage), ok},
		{"an administrator", tokenFor(t, f, f.ada, store.ScopeManage), ok},
		{"an administrator who is a viewer here", tokenFor(t, f, memberAdmin, store.ScopeManage), ok},
		{"an owner's read-only sign-in", tokenFor(t, f, reader, store.ScopeRead), refused},
		{"an administrator's read-only sign-in", tokenFor(t, f, f.ada, store.ScopeRead), refused},
		{"a read key", keyFor(t, f, f.project, "reader", store.ScopeRead), refused},
		{"a manage key", keyFor(t, f, f.project, "writer", store.ScopeManage), ok},
		{"a key of another project", keyFor(t, f, customer(t, f), "elsewhere", store.ScopeManage), notFound},
		{"someone of another project", tokenFor(t, f, someoneIn(t, f, "Stan", customer(t, f), store.Owner), store.ScopeManage), notFound},
	}
	type op struct {
		name, method, path string
		body               any
		code               int
		// The key can't reach both projects, so it is told the other does not exist.
		moving bool
	}
	ops := []op{
		{"create", "POST", "/projects/" + f.project + "/checks", map[string]any{"name": "new", "kind": "ping"}, 201, false},
		{"change", "PATCH", "/checks/{id}", map[string]any{"name": "renamed"}, 200, false},
		{"pause", "POST", "/checks/{id}/pause", nil, 200, false},
		{"resume", "POST", "/checks/{id}/resume", nil, 200, false},
		{"ping URL", "GET", "/checks/{id}/ping-url", nil, 200, false},
		{"new ping URL", "POST", "/checks/{id}/ping-url", nil, 200, false},
		{"move", "POST", "/checks/{id}/move", map[string]any{"project_id": both}, 200, true},
		{"delete", "DELETE", "/checks/{id}", nil, 204, false},
	}
	for _, o := range ops {
		for _, a := range asked {
			t.Run(o.name+" by "+a.who, func(t *testing.T) {
				c := fresh()
				want := a.want
				if want == ok {
					want = o.code
				}
				if o.moving && strings.Contains(a.who, "key") && want == o.code {
					want = notFound
				}
				w := send(f, o.method, strings.ReplaceAll(o.path, "{id}", c.ID), a.token, o.body)
				if w.Code != want {
					t.Errorf("%d, not %d: %s", w.Code, want, w.Body)
				}
				if w.Code == http.StatusForbidden && !strings.Contains(w.Body.String(), "only read") && !strings.Contains(w.Body.String(), "Ask an owner") {
					t.Errorf("does not say what to do: %s", w.Body)
				}
			})
		}
	}
	// What someone may not see answers as what does not exist.
	token := tokenFor(t, f, editor, store.ScopeManage)
	hidden := send(f, "POST", "/checks/"+f.hidden+"/pause", token, nil)
	none := send(f, "POST", "/checks/chk_nothing/pause", token, nil)
	if hidden.Code != http.StatusNotFound || hidden.Body.String() != none.Body.String() {
		t.Errorf("%d %s, and %s", hidden.Code, hidden.Body, none.Body)
	}
	// A move needs the role in both.
	c := fresh()
	f.st.SetMember(ctx, store.Command("test"), f.ws, customer(t, f), editor, store.Viewer)
	if w := send(f, "POST", "/checks/"+c.ID+"/move", token, map[string]any{"project_id": customer(t, f)}); w.Code != http.StatusForbidden {
		t.Errorf("a move to where they only look: %d %s", w.Code, w.Body)
	}
}

// A job's check, from adding to deleting, and where its ping URL may be (R20, R21).
func TestAJobsCheckOverTheAPI(t *testing.T) {
	f := setup(t)
	f.srv.Now = time.Now
	token := tokenFor(t, f, someoneIn(t, f, "Edith", f.project, store.Editor), store.ScopeManage)

	made := answer[api.CheckCreated](t, send(f, "POST", "/projects/"+f.project+"/checks", token, map[string]any{
		"name": "nightly", "kind": "ping", "description": "the backup", "tags": []string{"a"},
		"schedule": map[string]any{"kind": "cron", "expression": "0 3 * * *", "time_zone": "Europe/Amsterdam"},
		"grace":    1800, "slug": "nightly",
	}), 201)
	if made.PingUrl == nil || !strings.HasPrefix(*made.PingUrl, site+"/ping/") || made.Try != nil {
		t.Fatalf("%+v", made)
	}
	if made.Grace == nil || *made.Grace != 1800 || made.Name != "nightly" || made.Slug == nil || *made.Slug != "nightly" {
		t.Errorf("%+v", made)
	}
	uuid := strings.TrimPrefix(*made.PingUrl, site+"/ping/")
	again := answer[struct {
		PingUrl string `json:"ping_url"`
	}](t, send(f, "GET", "/checks/"+made.Id+"/ping-url", token, nil), 200)
	if again.PingUrl != *made.PingUrl {
		t.Errorf("again: %s", again.PingUrl)
	}

	// Defaults, as on the form.
	plain := answer[api.CheckCreated](t, send(f, "POST", "/projects/"+f.project+"/checks", token, map[string]any{"name": "plain", "kind": "ping"}), 201)
	interval, err := plain.Schedule.AsIntervalSchedule()
	if err != nil || interval.Every != 86400 || *plain.Grace != 3600 {
		t.Errorf("the defaults: %+v, %+v, %v", interval, plain.Grace, err)
	}

	// Only what is sent changes, and what it was read as guards it.
	read := send(f, "GET", "/checks/"+made.Id, token, nil)
	etag := read.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag")
	}
	w := send(f, "PATCH", "/checks/"+made.Id, token, map[string]any{"description": "still the backup", "tags": []string{}}, "If-Match", etag)
	changed := answer[api.CheckTried](t, w, 200)
	if *changed.Grace != 1800 || changed.Description == nil || *changed.Description != "still the backup" || changed.Tags != nil {
		t.Errorf("%+v", changed)
	}
	if strings.Contains(w.Body.String(), "ping_url") || strings.Contains(w.Body.String(), uuid) {
		t.Errorf("a change answers with its ping URL: %s", w.Body)
	}
	newer := w.Header().Get("ETag")
	if newer == "" || newer == etag {
		t.Errorf("the ETag was %s and is %s", etag, newer)
	}
	stale := send(f, "PATCH", "/checks/"+made.Id, token, map[string]any{"name": "too late"}, "If-Match", etag)
	if stale.Code != http.StatusPreconditionFailed {
		t.Errorf("out of date: %d %s", stale.Code, stale.Body)
	}
	// What changes nothing saves nothing, so nobody else's read goes out of date.
	same := send(f, "PATCH", "/checks/"+made.Id, token, map[string]any{"description": "still the backup"}, "If-Match", newer)
	if same.Code != 200 || same.Header().Get("ETag") != newer {
		t.Errorf("no change: %d, %s, %s", same.Code, same.Header().Get("ETag"), same.Body)
	}
	if w := send(f, "PATCH", "/checks/"+made.Id, token, map[string]any{"name": "x", "kind": "http"}); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("another kind: %d", w.Code)
	}
	bad := answer[api.Problem](t, send(f, "PATCH", "/checks/"+made.Id, token, map[string]any{"slug": "NOT A SLUG"}), http.StatusUnprocessableEntity)
	if bad.Detail == nil || bad.Errors == nil || (*bad.Errors)[0].Pointer != "/slug" {
		t.Errorf("%+v", bad)
	}

	// Pausing and resuming.
	if got := answer[api.Check](t, send(f, "POST", "/checks/"+made.Id+"/pause", token, nil), 200); got.State != "paused" {
		t.Errorf("paused: %s", got.State)
	}
	if got := answer[api.Check](t, send(f, "POST", "/checks/"+made.Id+"/resume", token, nil), 200); got.State != "new" {
		t.Errorf("resumed: %s", got.State)
	}
	if got := answer[api.Check](t, send(f, "POST", "/checks/"+made.Id+"/resume", token, nil), 200); got.State != "new" {
		t.Errorf("not paused: %s", got.State)
	}

	// A new ping URL, once, and the old one stops.
	rotated := answer[api.CheckWithPingUrl](t, send(f, "POST", "/checks/"+made.Id+"/ping-url", token, nil), 200)
	if rotated.PingUrl == *made.PingUrl {
		t.Errorf("the same ping URL: %s", rotated.PingUrl)
	}
	if body := send(f, "GET", "/checks/"+made.Id, token, nil).Body.String(); strings.Contains(body, "ping_url") || strings.Contains(body, strings.TrimPrefix(rotated.PingUrl, site+"/ping/")) {
		t.Errorf("reading a check gives its ping URL: %s", body)
	}

	// Who did it, in the log.
	if w := send(f, "DELETE", "/checks/"+made.Id, token, nil); w.Code != http.StatusNoContent {
		t.Errorf("deleting: %d", w.Code)
	}
	if w := send(f, "GET", "/checks/"+made.Id, token, nil); w.Code != http.StatusNotFound {
		t.Errorf("after deleting: %d", w.Code)
	}
	log := logAbout(t, f, token, "nightly")
	for _, kind := range []string{"check.created", "check.changed", "check.paused", "check.resumed", "check.ping_url_replaced", "check.deleted"} {
		i := slices.IndexFunc(log, func(e api.LogEntry) bool { return e.Kind == kind })
		if i < 0 {
			t.Errorf("no %s in %+v", kind, log)
			continue
		}
		if log[i].By.Kind != "person" || log[i].By.Name == nil || *log[i].By.Name != "Edith" {
			t.Errorf("%s by %+v", kind, log[i].By)
		}
	}
}

func logAbout(t *testing.T, f fixture, token, name string) []api.LogEntry {
	t.Helper()
	return read[api.LogPage](t, f, "/log?name="+name, token).Items
}

// A site: tried once when added or moved, under the same limits as the form (R22, R30).
func TestASiteOverTheAPI(t *testing.T) {
	f := setup(t)
	f.srv.Now = time.Now
	tries := tryAlways(&f)
	editor := someoneIn(t, f, "Edith", f.project, store.Editor)
	token := tokenFor(t, f, editor, store.ScopeManage)
	var nudged time.Time
	f.srv.Nudge = func(at time.Time) { nudged = at }

	w := send(f, "POST", "/projects/"+f.project+"/checks", token, map[string]any{
		"name": "shop", "kind": "http", "http": map[string]any{"url": "https://shop.example.com/health", "expected_status": []int{200}},
	})
	made := answer[api.CheckCreated](t, w, 201)
	if strings.Contains(w.Body.String(), "ping_url") {
		t.Errorf("a site answers with a ping URL: %s", w.Body)
	}
	if made.Try == nil || made.Try.Result != api.CheckTryResultUp || made.Try.HttpStatus == nil || *made.Try.HttpStatus != 200 || made.Try.CertDays == nil || made.Try.FirstAttemptAt == nil {
		t.Fatalf("%+v", made.Try)
	}
	if *tries != 1 {
		t.Errorf("%d tries", *tries)
	}
	interval, _ := made.Schedule.AsIntervalSchedule()
	if interval.Every != 60 {
		t.Errorf("runs every %d seconds", interval.Every)
	}
	if w := send(f, "GET", "/checks/"+made.Id+"/ping-url", token, nil); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("a site's ping URL: %d", w.Code)
	}
	if w := send(f, "POST", "/checks/"+made.Id+"/ping-url", token, nil); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("a new ping URL for a site: %d", w.Code)
	}

	// What it runs goes as a whole, so reading and sending it back changes nothing.
	whole := read[api.Check](t, f, "/checks/"+made.Id, token).Http
	back := send(f, "PATCH", "/checks/"+made.Id, token, map[string]any{"http": whole})
	if got := answer[api.CheckTried](t, back, 200); got.Try != nil || *tries != 1 {
		t.Errorf("sent back as it was: %+v, %d tries", got.Try, *tries)
	}
	// Judged differently, at the same address: no try.
	whole.ExpectedStatus = &[]int{200, 204}
	got := answer[api.CheckTried](t, send(f, "PATCH", "/checks/"+made.Id, token, map[string]any{"http": whole}), 200)
	if got.Try != nil || got.Http == nil || len(*got.Http.ExpectedStatus) != 2 || *tries != 1 {
		t.Errorf("a new code: %+v, %d tries", got, *tries)
	}
	// A new address: tried.
	whole.Url = "https://new.example.com/health"
	got = answer[api.CheckTried](t, send(f, "PATCH", "/checks/"+made.Id, token, map[string]any{"http": whole}), 200)
	if got.Try == nil || got.Http.Url != whole.Url || *tries != 2 {
		t.Errorf("a new address: %+v, %d tries", got.Try, *tries)
	}
	if got := answer[api.CheckTried](t, send(f, "PATCH", "/checks/"+made.Id, token, map[string]any{"schedule": map[string]any{"kind": "interval", "every": 300}}), 200); got.Try != nil {
		t.Errorf("a new schedule tried it: %+v", got.Try)
	}
	// A job that has run has a deadline, which the scheduler should hear of.
	answer[api.CheckTried](t, send(f, "PATCH", "/checks/"+f.checkID, token, map[string]any{"schedule": map[string]any{"kind": "interval", "every": 300}}), 200)
	if nudged.IsZero() {
		t.Error("the scheduler heard nothing of the change")
	}

	// Each settings field the form can write survives being read and sent back.
	full := answer[api.CheckCreated](t, send(f, "POST", "/projects/"+f.project+"/checks", keyFor(t, f, f.project, "full", store.ScopeManage), map[string]any{"name": "full", "kind": "http", "http": map[string]any{
		"url": "https://full.example.com/", "expected_status": []int{200}, "json_field": map[string]string{"pointer": "/a", "equals": "b"},
		"timeout": 7, "fail_after": 3, "warn_days": 0, "fail_days": 1, "no_verify": true,
	}}), 201)
	fullETag := send(f, "GET", "/checks/"+full.Id, token, nil).Header().Get("ETag")
	whole2 := read[api.Check](t, f, "/checks/"+full.Id, token).Http
	if w := send(f, "PATCH", "/checks/"+full.Id, token, map[string]any{"http": whole2}); w.Header().Get("ETag") != fullETag {
		t.Errorf("read and sent back, it changed: %s", w.Body)
	}

	// A limit per credential: each has its own. It has used two of its tries.
	for i := range triesPerMinute - 2 {
		whole.Url = "https://site-" + strconv.Itoa(i) + ".example.com/"
		send(f, "PATCH", "/checks/"+made.Id, token, map[string]any{"http": whole})
	}
	whole.Url = "https://last.example.com/"
	limited := answer[api.CheckTried](t, send(f, "PATCH", "/checks/"+made.Id, token, map[string]any{"http": whole}), 200)
	if limited.Try == nil || limited.Try.WhyNot == nil || *limited.Try.WhyNot != api.CheckTryWhyNotLimit || limited.Try.FirstAttemptAt == nil {
		t.Errorf("past its tries: %+v", limited.Try)
	}
	// Another sign-in of the same person has its own, and so does a key.
	for who, credential := range map[string]string{
		"a second sign-in": tokenFor(t, f, editor, store.ScopeManage),
		"a key":            keyFor(t, f, f.project, "busy", store.ScopeManage),
	} {
		before := *tries
		whole.Url = "https://" + strings.ReplaceAll(who, " ", "-") + ".example.com/"
		if got := answer[api.CheckTried](t, send(f, "PATCH", "/checks/"+made.Id, credential, map[string]any{"http": whole}), 200); got.Try == nil || got.Try.WhyNot != nil || *tries != before+1 {
			t.Errorf("%s: %+v", who, got.Try)
		}
	}
	// A key runs out of its tries as a person does.
	key := keyFor(t, f, f.project, "out-of-tries", store.ScopeManage)
	var last api.CheckTried
	for i := range triesPerMinute + 1 {
		whole.Url = "https://key-" + strconv.Itoa(i) + ".example.com/"
		last = answer[api.CheckTried](t, send(f, "PATCH", "/checks/"+made.Id, key, map[string]any{"http": whole}), 200)
	}
	if last.Try == nil || last.Try.WhyNot == nil || *last.Try.WhyNot != api.CheckTryWhyNotLimit {
		t.Errorf("a key past its tries: %+v", last.Try)
	}
	other := tokenFor(t, f, someoneIn(t, f, "Otto", f.project, store.Editor), store.ScopeManage)

	// What must be whole, and what the kind does not have.
	for name, body := range map[string]map[string]any{
		"no schedule to speak of": {"name": "x", "kind": "ping", "schedule": map[string]any{"kind": "interval", "every": 0}},
		"no every":                {"name": "x", "kind": "ping", "schedule": map[string]any{"kind": "interval"}},
		"every that wraps round":  {"name": "x", "kind": "ping", "schedule": map[string]any{"kind": "interval", "every": int64(1) << 55}},
		"grace that wraps round":  {"name": "x", "kind": "ping", "grace": int64(1) << 55},
		"no address":              {"name": "x", "kind": "http"},
		"no host":                 {"name": "x", "kind": "tcp"},
		"the wrong one":           {"name": "x", "kind": "http", "tcp": map[string]any{"host": "mail.example.com", "port": 993}},
		"on a job":                {"name": "x", "kind": "ping", "http": map[string]any{"url": "https://example.com/"}},
		"a slug":                  {"name": "x", "kind": "http", "slug": "x", "http": map[string]any{"url": "https://example.com/"}},
		"a grace":                 {"name": "x", "kind": "http", "grace": 600, "http": map[string]any{"url": "https://example.com/"}},
		"a kind":                  {"name": "x", "kind": "dns"},
		"no name":                 {"kind": "ping"},
		"an urgency":              {"name": "x", "kind": "ping", "alerting": map[string]any{"urgency": "soon"}},
	} {
		if w := send(f, "POST", "/projects/"+f.project+"/checks", other, body); w.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: %d %s", name, w.Code, w.Body)
		}
	}
}

// Case 7 of manage-checks: an address the server may not reach, in the same words.
func TestTheAPIRefusesWhatTheFormRefuses(t *testing.T) {
	f := setup(t)
	f.srv.Now = time.Now
	f.srv.Try = func(context.Context, active.Check) active.Result {
		t.Error("tried an address it may not reach")
		return active.Result{}
	}
	editor := someoneIn(t, f, "Edith", f.project, store.Editor)
	token := tokenFor(t, f, editor, store.ScopeManage)
	for _, address := range []string{"http://10.0.0.5/health", "http://127.0.0.1:8000/", "http://[::1]/", "http://169.254.169.254/latest"} {
		w := send(f, "POST", "/projects/"+f.project+"/checks", token, map[string]any{"name": "x", "kind": "http", "http": map[string]any{"url": address}})
		if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "not on the public internet") || strings.Contains(w.Body.String(), "own address") {
			t.Errorf("%s: %d %s", address, w.Code, w.Body)
		}
	}
	w := send(f, "POST", "/projects/"+f.project+"/checks", token, map[string]any{"name": "x", "kind": "tcp", "tcp": map[string]any{"host": "127.0.0.1", "port": 22}})
	if got := answer[api.Problem](t, w, http.StatusUnprocessableEntity); got.Errors == nil || (*got.Errors)[0].Pointer != "/tcp/host" {
		t.Errorf("%+v", got)
	}
	// Changing to such an address is refused too, and leaves it as it was.
	f.srv.Allow = []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}
	f.srv.Try = func(context.Context, active.Check) active.Result {
		return active.Result{Outcome: check.Failed, Reason: "the request failed"}
	}
	made := answer[api.CheckCreated](t, send(f, "POST", "/projects/"+f.project+"/checks", token, map[string]any{"name": "lan", "kind": "http", "http": map[string]any{"url": "http://192.0.2.7/"}}), 201)
	if made.Try == nil || made.Try.Result != api.CheckTryResultFails || made.Try.AlertsNobodyYet == nil || !*made.Try.AlertsNobodyYet {
		t.Errorf("%+v", made.Try)
	}
	if w := send(f, "PATCH", "/checks/"+made.Id, token, map[string]any{"http": map[string]any{"url": "http://10.0.0.5/"}}); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("changed to such an address: %d %s", w.Code, w.Body)
	}
	if got := read[api.Check](t, f, "/checks/"+made.Id, token); got.Http.Url != "http://192.0.2.7/" {
		t.Errorf("after being refused: %+v", got.Http)
	}
	// What only a job has, on either path.
	if w := send(f, "PATCH", "/checks/"+made.Id, token, map[string]any{"slug": "lan"}); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("a slug on a site: %d %s", w.Code, w.Body)
	}
	// A field it does not know is refused by name, and nothing changes: misspelled beside
	// a whole `http`, it would otherwise lose what it meant to set.
	for name, tt := range map[string]struct {
		method, path string
		body         map[string]any
		field        string
	}{
		"on a change":  {"PATCH", "/checks/" + made.Id, map[string]any{"descriptoin": "x"}, "descriptoin"},
		"within http":  {"PATCH", "/checks/" + made.Id, map[string]any{"http": map[string]any{"url": "http://192.0.2.8/", "expected_stauts": []int{200}}}, "expected_stauts"},
		"on a new one": {"POST", "/projects/" + f.project + "/checks", map[string]any{"name": "typo", "kind": "ping", "grase": 600}, "grase"},
		"on a move":    {"POST", "/checks/" + made.Id + "/move", map[string]any{"project_id": f.project, "to": "x"}, "to"},
	} {
		w := send(f, tt.method, tt.path, token, tt.body)
		if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), tt.field) {
			t.Errorf("%s: %d %s", name, w.Code, w.Body)
		}
	}
	if got := read[api.Check](t, f, "/checks/"+made.Id, token); got.Http.Url != "http://192.0.2.7/" {
		t.Errorf("after an unknown field: %+v", got.Http)
	}
	if w := send(f, "POST", "/projects/"+f.project+"/keys", tokenFor(t, f, f.ada, store.ScopeManage), map[string]any{"name": "x", "scope": "read", "expires": 1}); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("a key with an unknown field: %d %s", w.Code, w.Body)
	}

	// Critical takes an owner (manage-checks C4), and a key is none.
	if w := send(f, "POST", "/projects/"+f.project+"/checks", token, map[string]any{"name": "x", "kind": "ping", "alerting": map[string]any{"critical": true}}); w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "owner") {
		t.Errorf("critical by an editor: %d %s", w.Code, w.Body)
	}
	key := keyFor(t, f, f.project, "writer", store.ScopeManage)
	if w := send(f, "PATCH", "/checks/"+f.checkID, key, map[string]any{"alerting": map[string]any{"critical": true}}); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("critical by a key: %d %s", w.Code, w.Body)
	}
	owner := tokenFor(t, f, someoneIn(t, f, "Olga", f.project, store.Owner), store.ScopeManage)
	got := answer[api.CheckTried](t, send(f, "PATCH", "/checks/"+f.checkID, owner, map[string]any{"alerting": map[string]any{"critical": true}}), 200)
	if got.Alerting == nil || !*got.Alerting.Critical || *got.Alerting.Urgency != "now" {
		t.Errorf("critical by an owner: %+v", got.Alerting)
	}

	// A probe's own check stays, and a check on a probe that does not serve it is refused.
	admins, _ := f.st.AdminAccess(t.Context(), f.ws, "test")
	probeID, _, err := f.st.CreateProbe(t.Context(), admins, "office", f.project, []string{f.project})
	if err != nil {
		t.Fatal(err)
	}
	checks := read[api.CheckPage](t, f, "/projects/"+f.project+"/checks", token).Items
	i := slices.IndexFunc(checks, func(c api.Check) bool { return strings.Contains(c.Name, "office") })
	if i < 0 {
		t.Fatalf("no probe's own check in %+v", checks)
	}
	own := checks[i].Id
	for _, path := range []string{"/pause"} {
		if w := send(f, "POST", "/checks/"+own+path, token, nil); w.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s of a probe's own check: %d %s", path, w.Code, w.Body)
		}
	}
	if w := send(f, "DELETE", "/checks/"+own, token, nil); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("deleting a probe's own check: %d", w.Code)
	}
	onProbe := answer[api.CheckCreated](t, send(f, "POST", "/projects/"+f.project+"/checks", token, map[string]any{"name": "inside", "kind": "http", "http": map[string]any{"url": "http://10.0.0.5/", "probe_id": probeID}}), 201)
	if onProbe.Try == nil || onProbe.Try.WhyNot == nil || *onProbe.Try.WhyNot != api.CheckTryWhyNotProbe || onProbe.ProbeName == nil || *onProbe.ProbeName != "office" {
		t.Errorf("on a probe: %+v", onProbe)
	}
	elsewhere := keyFor(t, f, customer(t, f), "there", store.ScopeManage)
	if w := send(f, "POST", "/projects/"+customer(t, f)+"/checks", elsewhere, map[string]any{"name": "inside", "kind": "http", "http": map[string]any{"url": "http://10.0.0.5/", "probe_id": probeID}}); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("on a probe that does not serve it: %d %s", w.Code, w.Body)
	}
}

// Every operation that takes a body is held to the fields it knows, so a new one cannot
// silently ignore what it is sent.
func TestEveryBodyRefusesWhatItDoesNotKnow(t *testing.T) {
	var withBody []string
	inClient := reflect.TypeFor[apiclient.ClientInterface]()
	for i := range inClient.NumMethod() {
		if name, ok := strings.CutSuffix(inClient.Method(i).Name, "WithBody"); ok {
			withBody = append(withBody, name)
		}
	}
	for _, op := range apiOperations {
		if slices.Contains(withBody, op.name) {
			if _, ok := apiBodies[op.route]; !ok {
				t.Errorf("%s takes a body, and is not in apiBodies", op.name)
			}
		}
	}
	if len(withBody) != len(apiBodies) {
		t.Errorf("%d operations take a body, and apiBodies has %d", len(withBody), len(apiBodies))
	}
}

// A key: what it is, what it is held to, and how it ends (R11 to R13, R37).
func TestAKeyOverTheAPI(t *testing.T) {
	f := setup(t)
	f.srv.Now = time.Now
	owner := tokenFor(t, f, someoneIn(t, f, "Olga", f.project, store.Owner), store.ScopeManage)

	w := send(f, "POST", "/projects/"+f.project+"/keys", owner, map[string]any{"name": "deploy", "scope": "manage"})
	made := answer[api.ApiKeyMade](t, w, 201)
	if !strings.HasPrefix(made.Key, "alk_") || made.Scope != api.Manage || made.MadeBy.Kind != "person" {
		t.Fatalf("%+v", made)
	}
	me := read[api.Me](t, f, "/me", made.Key)
	if me.Kind != api.MeKindApiKey || me.Key == nil || me.Key.Name != "deploy" || me.Person != nil || me.SignIn != nil || len(me.Projects) != 1 || me.Projects[0].Role != api.Editor {
		t.Errorf("%+v", me)
	}
	// It reads and changes its project, and nothing else.
	if got := read[api.CheckPage](t, f, "/projects/"+f.project+"/checks", made.Key); len(got.Items) != 1 {
		t.Errorf("its checks: %+v", got.Items)
	}
	if w := bearer(f, "GET", "/checks/"+f.hidden, made.Key); w.Code != http.StatusNotFound {
		t.Errorf("a check elsewhere: %d", w.Code)
	}
	created := answer[api.CheckCreated](t, send(f, "POST", "/projects/"+f.project+"/checks", made.Key, map[string]any{"name": "by-key", "kind": "ping"}), 201)
	log := logAbout(t, f, owner, "by-key")
	if len(log) != 1 || log[0].By.Kind != "key" || *log[0].By.Name != "deploy" {
		t.Errorf("the log says %+v", log)
	}
	// It can make no key, nor end what is a person's.
	for _, tt := range []struct {
		method, path string
		body         any
	}{
		{"POST", "/projects/" + f.project + "/keys", map[string]any{"name": "more", "scope": "manage"}},
		{"GET", "/keys", nil},
		{"DELETE", "/keys/" + made.Id, nil},
		{"DELETE", "/me/token", nil},
	} {
		if w := send(f, tt.method, tt.path, made.Key, tt.body); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "person") {
			t.Errorf("%s %s by a key: %d %s", tt.method, tt.path, w.Code, w.Body)
		}
	}
	// A key's value is in no list.
	list := bearer(f, "GET", "/keys", owner)
	if got := answer[api.ApiKeyList](t, list, 200); len(got.Items) != 1 || got.Items[0].Name != "deploy" || got.Items[0].LastUsedAt == nil {
		t.Errorf("%+v", got)
	}
	if strings.Contains(list.Body.String(), made.Key) || strings.Contains(list.Body.String(), `"key"`) {
		t.Errorf("a list holds a key: %s", list.Body)
	}
	byProject := read[api.ApiKeyList](t, f, "/keys?project_id="+f.project, owner)
	if len(byProject.Items) != 1 {
		t.Errorf("%+v", byProject)
	}
	if w := bearer(f, "GET", "/keys?project_id="+customer(t, f), owner); w.Code != http.StatusNotFound {
		t.Errorf("another project's keys: %d", w.Code)
	}
	if w := send(f, "POST", "/projects/"+f.project+"/keys", owner, map[string]any{"name": "deploy", "scope": "read"}); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("the same name: %d", w.Code)
	}
	editor := tokenFor(t, f, someoneIn(t, f, "Edith", f.project, store.Editor), store.ScopeManage)
	for _, tt := range []struct {
		method, path string
		body         any
	}{
		{"POST", "/projects/" + f.project + "/keys", map[string]any{"name": "x", "scope": "read"}},
		{"DELETE", "/keys/" + made.Id, nil},
		{"GET", "/keys?project_id=" + f.project, nil},
	} {
		if w := send(f, tt.method, tt.path, editor, tt.body); w.Code != http.StatusForbidden {
			t.Errorf("%s %s by an editor: %d", tt.method, tt.path, w.Code)
		}
	}
	// Revoked, it stops on its next request.
	if w := send(f, "DELETE", "/keys/"+made.Id, owner, nil); w.Code != http.StatusNoContent {
		t.Fatalf("revoking: %d %s", w.Code, w.Body)
	}
	if w := bearer(f, "GET", "/me", made.Key); w.Code != http.StatusUnauthorized {
		t.Errorf("revoked: %d", w.Code)
	}
	if w := send(f, "DELETE", "/keys/"+made.Id, owner, nil); w.Code != http.StatusNotFound {
		t.Errorf("revoked twice: %d", w.Code)
	}
	if _, err := f.st.Check(t.Context(), mustAdmin(t, f), created.Id); err != nil {
		t.Errorf("what it made goes with it: %v", err)
	}
	// A key has its own limit, and made-up ones fill nothing.
	key := keyFor(t, f, f.project, "busy", store.ScopeRead)
	for range apiRequestsPerMinute {
		bearer(f, "GET", "/me", key)
	}
	if w := bearer(f, "GET", "/me", key); w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") == "" || !strings.Contains(w.Body.String(), "this key") {
		t.Errorf("past its limit: %d %s", w.Code, w.Body)
	}
	if w := bearer(f, "GET", "/me", "alk_madeup"); w.Code != http.StatusUnauthorized {
		t.Errorf("a made-up key: %d", w.Code)
	}
}

func mustAdmin(t *testing.T, f fixture) store.Access {
	t.Helper()
	a, err := f.st.AdminAccess(t.Context(), f.ws, "test")
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// No secret in a log line or an error (R41): a marker in the address, the ping URL and
// each credential, through what changes things and what is refused.
func TestNoSecretInAnErrorOrALog(t *testing.T) {
	f := setup(t)
	f.srv.Now = time.Now
	var logged bytes.Buffer
	f.srv.Log = slog.New(slog.NewTextHandler(&logged, nil))
	tryAlways(&f)
	const marker = "s3cretmark3r"
	token := tokenFor(t, f, someoneIn(t, f, "Edith", f.project, store.Editor), store.ScopeManage)
	owner := tokenFor(t, f, someoneIn(t, f, "Olga", f.project, store.Owner), store.ScopeManage)
	made := answer[api.ApiKeyMade](t, send(f, "POST", "/projects/"+f.project+"/keys", owner, map[string]any{"name": "deploy", "scope": "manage"}), 201)
	withMarker := "https://shop.example.com/health?token=" + marker
	shop := answer[api.CheckCreated](t, send(f, "POST", "/projects/"+f.project+"/checks", token, map[string]any{"name": "shop", "kind": "http", "http": map[string]any{"url": withMarker}}), 201)
	job := answer[api.CheckCreated](t, send(f, "POST", "/projects/"+f.project+"/checks", made.Key, map[string]any{"name": "job", "kind": "ping"}), 201)
	uuid := strings.TrimPrefix(*job.PingUrl, site+"/ping/")
	var answers []string
	for _, tt := range []struct {
		method, path string
		body         any
		code         int
	}{
		{"PATCH", "/checks/" + shop.Id, map[string]any{"http": map[string]any{"url": withMarker + "x", "method": "POST"}}, http.StatusUnprocessableEntity},
		{"PATCH", "/checks/" + shop.Id, map[string]any{"http": map[string]any{"url": "ftp://example.com/" + marker}}, http.StatusUnprocessableEntity},
		{"PATCH", "/checks/" + shop.Id, map[string]any{"http": map[string]any{"url": "https://user:" + marker + "@example.com/"}}, http.StatusUnprocessableEntity},
		{"POST", "/projects/" + f.project + "/checks", map[string]any{"name": "x", "kind": "http", "http": map[string]any{"url": "http://10.0.0.5/" + marker}}, http.StatusUnprocessableEntity},
		{"POST", "/checks/" + job.Id + "/ping-url", nil, http.StatusOK},
	} {
		w := send(f, tt.method, tt.path, token, tt.body)
		if w.Code != tt.code {
			t.Errorf("%s %s: %d, not %d: %s", tt.method, tt.path, w.Code, tt.code, w.Body)
		}
		answers = append(answers, w.Body.String())
	}
	// A change that goes through writes its line to the log and the server's log, and
	// neither holds the address.
	answer[api.CheckTried](t, send(f, "PATCH", "/checks/"+shop.Id, token, map[string]any{"http": map[string]any{"url": withMarker + "y"}}), 200)
	if w := send(f, "DELETE", "/checks/"+shop.Id, token, nil); w.Code != http.StatusNoContent {
		t.Errorf("deleting: %d", w.Code)
	}
	answers = append(answers, bearer(f, "GET", "/me", "alk_"+marker).Body.String(), bearer(f, "GET", "/me", "alt_"+marker).Body.String())
	for _, a := range answers {
		if strings.Contains(a, marker) {
			t.Errorf("an answer holds a secret: %s", a)
		}
	}
	for _, secret := range []string{marker, made.Key, token, owner, uuid} {
		if strings.Contains(logged.String(), secret) {
			t.Errorf("the server's log holds %q: %s", secret, logged.String())
		}
	}
	log := read[api.LogPage](t, f, "/log", owner)
	raw, _ := json.Marshal(log)
	for _, secret := range []string{marker, made.Key, token, owner, uuid} {
		if strings.Contains(string(raw), secret) {
			t.Errorf("the log holds %q: %s", secret, raw)
		}
	}
}
