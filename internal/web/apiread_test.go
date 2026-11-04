// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MartijnMul/allstill/internal/api"
	"github.com/MartijnMul/allstill/internal/check"
	"github.com/MartijnMul/allstill/internal/store"
)

// tokenFor gives someone a sign-in, without the ceremony, which device_test.go covers.
func tokenFor(t *testing.T, f fixture, personID, scope string) string {
	t.Helper()
	ctx := t.Context()
	now := time.Now()
	keys, err := f.st.Passkeys(ctx, f.ws, personID)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) == 0 {
		link, err := f.st.CreatePasskeyLink(ctx, store.Command("test"), f.ws, personID, now)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.st.AddPasskey(ctx, link, store.NewPasskey{CredentialID: []byte(personID), RPID: "allstill.example.com", Name: "k", Record: []byte(`{"id":"x"}`)}, "", now); err != nil {
			t.Fatal(err)
		}
		keys, _ = f.st.Passkeys(ctx, f.ws, personID)
	}
	token, err := f.st.CreatePersonToken(ctx, store.NewPersonToken{WorkspaceID: f.ws, PersonID: personID, PasskeyID: keys[0].ID, Record: keys[0].Record, Name: "laptop", Scope: scope}, now)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func read[T any](t *testing.T, f fixture, path, token string) T {
	t.Helper()
	var out T
	w := bearer(f, "GET", path, token)
	if w.Code != http.StatusOK {
		t.Fatalf("GET %s: %d %s", path, w.Code, w.Body)
	}
	// A field the description does not have fails here.
	d := json.NewDecoder(bytes.NewReader(w.Body.Bytes()))
	d.DisallowUnknownFields()
	if err := d.Decode(&out); err != nil {
		t.Fatalf("GET %s: %v: %s", path, err, w.Body)
	}
	return out
}

// someoneIn adds a person with a role in one project.
func someoneIn(t *testing.T, f fixture, name, projectID string, role store.Role) string {
	t.Helper()
	person, err := f.st.CreatePerson(t.Context(), store.Command("test"), f.ws, store.NewPerson{Name: name})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.st.SetMember(t.Context(), store.Command("test"), f.ws, projectID, person, role); err != nil {
		t.Fatal(err)
	}
	return person
}

// Who gets what, for each kind of reader (spec, R5, R15, R36).
func TestWhoReadsWhat(t *testing.T) {
	f := setup(t)
	f.srv.Now = time.Now
	viewerT := tokenFor(t, f, f.bob, store.ScopeManage)
	editor := tokenFor(t, f, someoneIn(t, f, "Edith", f.project, store.Editor), store.ScopeManage)
	owner := tokenFor(t, f, someoneIn(t, f, "Olga", f.project, store.Owner), store.ScopeManage)
	admin := tokenFor(t, f, f.ada, store.ScopeManage)
	readOnly := tokenFor(t, f, f.ada, store.ScopeRead)
	limitedRead := tokenFor(t, f, someoneIn(t, f, "Rob", f.project, store.Owner), store.ScopeRead)
	admins, _ := f.st.AdminAccess(t.Context(), f.ws, "test")
	elsewhere, _ := f.st.ProjectByName(t.Context(), f.ws, "Customer")
	if _, _, err := f.st.CreateProbe(t.Context(), admins, "office", elsewhere, []string{f.project}); err != nil {
		t.Fatal(err)
	}

	for _, tt := range []struct {
		who      string
		token    string
		projects int
		role     api.Role
		other    int // what asking for the other project answers
		probes   bool
	}{
		{"a viewer", viewerT, 1, api.Viewer, http.StatusNotFound, false},
		{"an editor", editor, 1, api.Editor, http.StatusNotFound, true},
		{"an owner", owner, 1, api.Owner, http.StatusNotFound, true},
		{"an administrator", admin, 2, api.Owner, http.StatusOK, true},
		{"an administrator's read-only sign-in", readOnly, 2, api.Viewer, http.StatusOK, false},
		{"an owner's read-only sign-in", limitedRead, 1, api.Viewer, http.StatusNotFound, false},
	} {
		t.Run(tt.who, func(t *testing.T) {
			projects := read[api.ProjectPage](t, f, "/projects", tt.token)
			if len(projects.Items) != tt.projects {
				t.Fatalf("projects: %+v", projects.Items)
			}
			i := slices.IndexFunc(projects.Items, func(p api.Project) bool { return p.Id == f.project })
			if projects.Items[i].Role != tt.role || projects.Items[i].Counts["up"] != 1 || projects.Items[i].WorstState == nil {
				t.Errorf("the project: %+v", projects.Items[i])
			}
			if got := read[api.Project](t, f, "/projects/"+f.project, tt.token); got.Name != "Backups" {
				t.Errorf("one project: %+v", got)
			}
			if got := read[api.CheckPage](t, f, "/projects/"+f.project+"/checks", tt.token); len(got.Items) != 1 || got.Items[0].Id != f.checkID {
				t.Errorf("its checks: %+v", got.Items)
			}
			if got := read[api.Check](t, f, "/checks/"+f.checkID, tt.token); got.Name != "nightly-backup" || got.LastRun == nil {
				t.Errorf("one check: %+v", got)
			}
			if got := read[api.RunPage](t, f, "/checks/"+f.checkID+"/runs", tt.token); len(got.Items) != 1 || got.Items[0].Outcome != api.RunOutcomeSuccess {
				t.Errorf("its runs: %+v", got.Items)
			}
			status := read[api.Status](t, f, "/status", tt.token)
			if len(status.Projects) != tt.projects {
				t.Errorf("status: %+v", status)
			}

			// What they cannot see answers as what does not exist.
			hiddenProject, _ := f.st.ProjectByName(t.Context(), f.ws, "Customer")
			for _, path := range []string{"/projects/" + hiddenProject, "/projects/" + hiddenProject + "/checks", "/checks/" + f.hidden, "/checks/" + f.hidden + "/runs"} {
				w := bearer(f, "GET", path, tt.token)
				if w.Code != tt.other {
					t.Errorf("%s: %d", path, w.Code)
				}
				if tt.other == http.StatusNotFound {
					none := bearer(f, "GET", "/checks/chk_nothing", tt.token)
					if w.Body.String() != none.Body.String() {
						t.Errorf("%s tells itself apart: %s", path, w.Body)
					}
				}
			}
			probes := read[api.ProbeList](t, f, "/probes", tt.token)
			if tt.probes != (len(probes.Items) > 0) {
				t.Errorf("probes: %+v", probes.Items)
			}
		})
	}

	// A role changed counts on the next request.
	bobs := read[api.Project](t, f, "/projects/"+f.project, viewerT)
	if bobs.Role != api.Viewer {
		t.Fatalf("before: %+v", bobs)
	}
	f.st.SetMember(t.Context(), store.Command("test"), f.ws, f.project, f.bob, store.Editor)
	if got := read[api.Project](t, f, "/projects/"+f.project, viewerT); got.Role != api.Editor {
		t.Errorf("after: %+v", got)
	}
	f.st.RemoveMember(t.Context(), store.Command("test"), f.ws, f.project, f.bob)
	if w := bearer(f, "GET", "/projects/"+f.project, viewerT); w.Code != http.StatusNotFound {
		t.Errorf("taken out: %d", w.Code)
	}
}

// A marker in each secret, called through every read, as those who may not see it.
func TestNoSecretInAReadersAnswers(t *testing.T) {
	f := setup(t)
	f.srv.Now = time.Now
	ctx := t.Context()
	admins, _ := f.st.AdminAccess(ctx, f.ws, "test")
	const marker = "s3cretmark3r"
	site, err := f.st.CreateCheck(ctx, admins, f.project, store.NewCheck{
		Name: "shop", Kind: "http", Interval: time.Minute,
		Settings: map[string]any{"url": "https://shop.example.com/health?token=" + marker, "location": "https://shop.example.com/" + marker, "ca": aCertificate(t)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.RecordAttempts(ctx, []store.AttemptOf{{WorkspaceID: f.ws, CheckID: site.ID, Attempt: store.Attempt{
		Attempt: check.Attempt{Outcome: check.Failed, Reason: "HTTP 503", At: time.Now()}, Started: time.Now().Add(-time.Second), Measured: map[string]float64{"status": 503},
	}}}); err != nil {
		t.Fatal(err)
	}
	job, _ := f.st.Check(ctx, admins, f.checkID)
	if job.PingUUID == "" {
		t.Fatal("no ping UUID")
	}
	paths := []string{
		"/me", "/status", "/projects", "/projects/" + f.project, "/projects/" + f.project + "/checks",
		"/checks/" + site.ID, "/checks/" + site.ID + "/runs", "/checks/" + f.checkID, "/checks/" + f.checkID + "/runs",
		"/log", "/log?check_id=" + site.ID, "/log?name=shop", "/probes", "/keys",
	}
	for _, op := range apiOperations {
		method, path, _ := strings.Cut(op.route, " ")
		// Its answer is a ping URL, for those who may have one: apiwrite_test.go.
		if method != "GET" || op.takes == probeCredential || op.name == "GetPingUrl" {
			continue
		}
		path = strings.TrimPrefix(path, "/api/v1")
		path = strings.ReplaceAll(path, "{project_id}", f.project)
		path = strings.ReplaceAll(path, "{check_id}", site.ID)
		if !slices.Contains(paths, path) {
			t.Errorf("%s is not read here: add it", path)
		}
	}
	for who, token := range map[string]string{
		"a viewer":                             tokenFor(t, f, f.bob, store.ScopeManage),
		"an administrator's read-only sign-in": tokenFor(t, f, f.ada, store.ScopeRead),
		"an owner's read-only sign-in":         tokenFor(t, f, someoneIn(t, f, "Rob", f.project, store.Owner), store.ScopeRead),
	} {
		for _, path := range paths {
			body := bearer(f, "GET", path, token).Body.String()
			if strings.Contains(body, marker) {
				t.Errorf("%s reads a secret at %s: %s", who, path, body)
			}
			if strings.Contains(body, job.PingUUID) || strings.Contains(body, site.PingUUID) {
				t.Errorf("%s reads a ping URL at %s: %s", who, path, body)
			}
		}
		got := read[api.Check](t, f, "/checks/"+site.ID, token)
		if got.Http == nil || got.Http.Url != "https://shop.example.com" || got.Http.Location == nil || *got.Http.Location != "https://shop.example.com" || got.Http.Ca != nil {
			t.Errorf("%s reads %+v", who, got.Http)
		}
		// How fast it tells people is for those who may change that, as on the dashboard.
		if got.Alerting != nil {
			t.Errorf("%s reads its alerting: %+v", who, got.Alerting)
		}
	}
	// A read-only sign-in cannot have one, whoever holds it.
	for who, token := range map[string]string{
		"a viewer":                             tokenFor(t, f, f.bob, store.ScopeManage),
		"an administrator's read-only sign-in": tokenFor(t, f, f.ada, store.ScopeRead),
		"a read key":                           keyFor(t, f, f.project, "reader", store.ScopeRead),
	} {
		if w := bearer(f, "GET", "/checks/"+f.checkID+"/ping-url", token); w.Code != http.StatusForbidden || strings.Contains(w.Body.String(), job.PingUUID) {
			t.Errorf("%s asks for a ping URL: %d %s", who, w.Code, w.Body)
		}
	}
	// Someone who may see an address whole reads it whole, and no ping URL even so.
	for who, token := range map[string]string{
		"an editor":        tokenFor(t, f, someoneIn(t, f, "Edith", f.project, store.Editor), store.ScopeManage),
		"an administrator": tokenFor(t, f, f.ada, store.ScopeManage),
	} {
		got := read[api.Check](t, f, "/checks/"+site.ID, token)
		if got.Http == nil || !strings.Contains(got.Http.Url, marker) || got.Http.Ca == nil || got.Alerting == nil {
			t.Errorf("%s reads %+v, %+v", who, got.Http, got.Alerting)
		}
		for _, path := range paths {
			body := bearer(f, "GET", path, token).Body.String()
			if strings.Contains(body, job.PingUUID) || strings.Contains(body, site.PingUUID) || strings.Contains(body, "ping_url") {
				t.Errorf("%s reads a ping URL at %s: %s", who, path, body)
			}
		}
	}
}

func TestStatusSaysHowThingsStand(t *testing.T) {
	f := setup(t)
	f.srv.Now = time.Now
	ctx := t.Context()
	token := tokenFor(t, f, f.ada, store.ScopeManage)
	got := read[api.Status](t, f, "/status", token)
	// One up, and one that has never pinged.
	if got.Overall != api.StatusOverallAllStill || got.Counts["up"] != 1 || got.Counts["new"] != 1 || len(got.Attention) != 0 {
		t.Fatalf("all still: %+v", got)
	}
	admins, _ := f.st.AdminAccess(ctx, f.ws, "test")
	job, _ := f.st.Check(ctx, admins, f.checkID)
	f.st.Ping(ctx, store.PingIn{CheckUUID: job.PingUUID, Kind: check.Fail, ExitStatus: 1, Method: "POST", At: time.Now()})
	got = read[api.Status](t, f, "/status", token)
	if got.Overall != api.StatusOverallIncident || len(got.Attention) != 1 || got.Counts["down"] != 1 {
		t.Fatalf("down: %+v", got)
	}
	a := got.Attention[0]
	if a.Id != f.checkID || a.State != api.CheckStateDown || a.ProjectName != "Backups" || a.StateReason == nil || *a.StateReason == "" {
		t.Errorf("what needs attention: %+v", a)
	}
	worst := slices.IndexFunc(got.Projects, func(p api.Project) bool { return p.Id == f.project })
	if s := got.Projects[worst].WorstState; s == nil || *s != api.CheckStateDown {
		t.Errorf("its project: %+v", got.Projects[worst])
	}
}

func TestListsArePaged(t *testing.T) {
	f := setup(t)
	f.srv.Now = time.Now
	ctx := t.Context()
	admins, _ := f.st.AdminAccess(ctx, f.ws, "test")
	token := tokenFor(t, f, f.ada, store.ScopeManage)
	for i := range 4 {
		if _, err := f.st.CreateCheck(ctx, admins, f.project, store.NewCheck{Name: "job-" + strconv.Itoa(i), Interval: time.Hour, Grace: time.Hour}); err != nil {
			t.Fatal(err)
		}
	}
	var names []string
	path := "/projects/" + f.project + "/checks?limit=2"
	for pages := 0; ; pages++ {
		page := read[api.CheckPage](t, f, path, token)
		if len(page.Items) > 2 || pages > 3 {
			t.Fatalf("page %d: %d items", pages, len(page.Items))
		}
		for _, c := range page.Items {
			names = append(names, c.Name)
		}
		if page.NextCursor == nil {
			break
		}
		path = "/projects/" + f.project + "/checks?limit=2&cursor=" + url.QueryEscape(*page.NextCursor)
	}
	if !slices.Equal(names, []string{"job-0", "job-1", "job-2", "job-3", "nightly-backup"}) {
		t.Errorf("names: %v", names)
	}

	job, _ := f.st.Check(ctx, admins, f.checkID)
	for range 3 {
		f.st.Ping(ctx, store.PingIn{CheckUUID: job.PingUUID, Kind: check.Success, ExitStatus: 0, Method: "POST", At: time.Now()})
	}
	first := read[api.RunPage](t, f, "/checks/"+f.checkID+"/runs?limit=3", token)
	if len(first.Items) != 3 || first.NextCursor == nil {
		t.Fatalf("runs: %+v", first)
	}
	second := read[api.RunPage](t, f, "/checks/"+f.checkID+"/runs?limit=3&cursor="+url.QueryEscape(*first.NextCursor), token)
	if len(second.Items) != 1 || second.NextCursor != nil || second.Items[0].Id == first.Items[2].Id {
		t.Errorf("the rest of the runs: %+v", second)
	}
	projects := read[api.ProjectPage](t, f, "/projects?limit=1", token)
	if len(projects.Items) != 1 || projects.NextCursor == nil {
		t.Errorf("projects: %+v", projects)
	}

	// Asking for more than a page holds, or for nonsense, keeps within what is sensible.
	if size := pageSize(ptr(5000)); size != maxPage {
		t.Errorf("a large limit: %d", size)
	}
	if size := pageSize(ptr(-1)); size != defaultPage {
		t.Errorf("a negative limit: %d", size)
	}
	for _, path := range []string{"/projects?cursor=!!!", "/projects/" + f.project + "/checks?cursor=!!!", "/checks/" + f.checkID + "/runs?cursor=!!!", "/log?cursor=!!!", "/log?cursor=bm90YW51bWJlcg", "/log?what=everything"} {
		if w := bearer(f, "GET", path, token); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", path, w.Code, w.Body)
		}
	}
}

func ptr[T any](v T) *T { return &v }

// aCertificate is a certificate in PEM, for a check's own CA.
func aCertificate(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test"}, NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

// The same record as the page, under the same rules for each reader (R16).
func TestTheLogOverTheAPI(t *testing.T) {
	f := setup(t)
	f.srv.Now = time.Now
	ctx := t.Context()
	token := tokenFor(t, f, f.ada, store.ScopeManage)
	access, _ := f.st.AccessOfPerson(ctx, f.ws, f.ada)
	gone, err := f.st.CreateCheck(ctx, access, f.project, store.NewCheck{Name: "soon-gone", Interval: time.Hour, Grace: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.st.DeleteCheck(ctx, access, gone.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	page := read[api.LogPage](t, f, "/log?name=soon-gone", token)
	kinds := []string{}
	for _, e := range page.Items {
		kinds = append(kinds, e.Kind)
		if e.AboutName == nil || *e.AboutName != "soon-gone" || e.By.Kind != "person" || e.By.Name == nil || *e.By.Name != "Ada" {
			t.Errorf("an entry: %+v", e)
		}
	}
	if !slices.Contains(kinds, "check.deleted") || !slices.Contains(kinds, "check.created") || page.KeptFor == 0 {
		t.Errorf("a deleted check, by name: %v, %+v", kinds, page)
	}

	// Someone who only reads, whoever they are, reads what a viewer reads.
	for who, tok := range map[string]string{"a viewer": tokenFor(t, f, f.bob, store.ScopeManage), "a read-only administrator": tokenFor(t, f, f.ada, store.ScopeRead)} {
		for _, e := range read[api.LogPage](t, f, "/log?name=soon-gone", tok).Items {
			if e.By.Kind != "team" || e.By.Name != nil || e.By.Id != nil || e.Person != nil {
				t.Errorf("%s reads %+v", who, e)
			}
		}
	}
	// A viewer reads their own sign-ins, and a read-only sign-in nobody's, as those are
	// listed on Devices alone: not even the others of its own person.
	if got := read[api.LogPage](t, f, "/log?what=signins", tokenFor(t, f, f.ada, store.ScopeRead)); len(got.Items) != 0 {
		t.Errorf("a read-only sign-in reads sign-ins: %+v", got.Items)
	}
	if got := read[api.LogPage](t, f, "/log?person_id="+f.ada, tokenFor(t, f, f.ada, store.ScopeRead)); len(got.Items) != 0 {
		t.Errorf("a read-only sign-in narrows to its person: %+v", got.Items)
	}
	for _, e := range read[api.LogPage](t, f, "/log?what=signins", tokenFor(t, f, f.bob, store.ScopeManage)).Items {
		if e.Person == nil || e.Person.Id == nil || *e.Person.Id != f.bob {
			t.Errorf("a viewer reads someone else's sign-in: %+v", e)
		}
	}
	// A project they cannot see answers as if nothing is there.
	hidden, _ := f.st.ProjectByName(ctx, f.ws, "Customer")
	if got := read[api.LogPage](t, f, "/log?project_id="+hidden, tokenFor(t, f, f.bob, store.ScopeManage)); len(got.Items) != 0 {
		t.Errorf("another project: %+v", got.Items)
	}
}

// A credential's limit, and made-up ones do not fill it (R42).
func TestACredentialHasALimit(t *testing.T) {
	f := setup(t)
	advance := clockOf(f)
	one, two := tokenFor(t, f, f.ada, store.ScopeManage), tokenFor(t, f, f.ada, store.ScopeRead)
	for i := range apiRequestsPerMinute {
		if w := bearer(f, "GET", "/me", one); w.Code != http.StatusOK {
			t.Fatalf("request %d: %d", i, w.Code)
		}
	}
	w := bearer(f, "GET", "/status", one)
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") == "" || !strings.Contains(w.Body.String(), "Too many") {
		t.Fatalf("past the limit: %d %v %s", w.Code, w.Header(), w.Body)
	}
	if w := bearer(f, "GET", "/me", two); w.Code != http.StatusOK {
		t.Errorf("another sign-in: %d", w.Code)
	}
	wait, _ := strconv.Atoi(w.Header().Get("Retry-After"))
	advance(time.Duration(wait+1) * time.Second)
	if w := bearer(f, "GET", "/me", one); w.Code != http.StatusOK {
		t.Errorf("after waiting: %d", w.Code)
	}
	for range 3 * apiRequestsPerMinute {
		if w := bearer(f, "GET", "/me", "alt_madeup"); w.Code != http.StatusUnauthorized {
			t.Fatalf("a made-up value: %d", w.Code)
		}
	}
	if n := len(f.srv.apiLimit.buckets); n != 2 {
		t.Errorf("%d buckets, with 2 credentials", n)
	}

	// A probe's key has one too, of its own.
	key, _, _ := aProbeOf(t, f)
	for i := range apiRequestsPerMinute {
		if w := probeRequest(f, "GET", "/api/v1/probe/checks", key, nil, nil); w.Code != http.StatusOK {
			t.Fatalf("a probe's request %d: %d", i, w.Code)
		}
	}
	if w := probeRequest(f, "GET", "/api/v1/probe/checks", key, nil, nil); w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") == "" {
		t.Errorf("a probe past the limit: %d", w.Code)
	}
	if w := bearer(f, "GET", "/me", two); w.Code != http.StatusOK {
		t.Errorf("a sign-in, with a probe past its limit: %d", w.Code)
	}
}

// What the description holds and is not served says so (R44).
func TestTheDescriptionSaysWhatIsNotServed(t *testing.T) {
	raw, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	served := map[string]bool{"getHealth": true, "requestDeviceCode": true, "requestToken": true}
	for _, op := range apiOperations {
		served[strings.ToLower(op.name[:1])+op.name[1:]] = true
	}
	found := 0
	for _, m := range regexp.MustCompile(`operationId: (\w+)\n\s+summary: ([^\n]*)`).FindAllStringSubmatch(string(raw), -1) {
		op, summary := m[1], m[2]
		if strings.HasPrefix(op, "ping") {
			continue // handwritten, and served
		}
		found++
		says := strings.Contains(summary, "Not served yet")
		if served[op] == says {
			t.Errorf("%s: served %v, summary %q", op, served[op], summary)
		}
	}
	if found < 20 {
		t.Fatalf("found only %d operations", found)
	}
}
