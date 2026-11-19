// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MartijnMul/allstill/internal/active"
	"github.com/MartijnMul/allstill/internal/alert"
	"github.com/MartijnMul/allstill/internal/api"
	"github.com/MartijnMul/allstill/internal/check"
	"github.com/MartijnMul/allstill/internal/store"
)

// The wall between teams (intent/teams/spec.md, R43): two teams on one
// installation, each with people of every role, a project with checks of every
// kind, a key, a probe, an incident and messages. Everyone walks every page and
// every operation of the API, also asking for the other team's things by id and
// by name, and nothing of the other team may come back, in an answer or in an
// error.

// side is one team and what it holds. Each list is what nobody outside may read.
type side struct {
	team                  string
	owner, editor, viewer string // people, by id
	project               string
	ping, site, port      string // checks, by id
	key, probe            string
	keyValue, probeKey    string
	pingName              string // the name of ping, to search and narrow by
	// What belongs to its owner alone: an address alerts go to, a sign-in from
	// a command line and a browser session.
	channel, signIn, session string

	projects, checks, addresses, keys, probes, people, teams []string
}

type twoTeams struct {
	fixture
	a, b side
	// zed keeps the installation and is in no team; guest has a role of their
	// own in one project of b and is in no team either.
	zed, guest string
	// outside are guest and an editor and an owner like them: each has a row of
	// their own in that one project.
	outside []string
	// rest is what they must not read of b: its other project with its key, its
	// people and its name. Its probe's own check is in the project they see.
	rest side
}

func (s side) everything() []string {
	var all []string
	for _, l := range [][]string{s.projects, s.checks, s.addresses, s.keys, s.probes, s.people, s.teams} {
		all = append(all, l...)
	}
	return all
}

func setupTwoTeams(t *testing.T) twoTeams {
	t.Helper()
	w := twoTeams{fixture: setup(t)}
	ctx := t.Context()
	by := store.Command("test")
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	admin := func() store.Access {
		a, err := w.st.AdminAccess(ctx, w.ws, "test")
		must(err)
		return a
	}
	teams, err := w.st.Teams(ctx, w.ws)
	must(err)
	second, err := w.st.CreateTeam(ctx, by, w.ws, "Harbour Works")
	must(err)

	fill := func(s *side, team, teamName, project string, names [3]string, ping, site, host, key, probe string) {
		s.team = team
		for i, role := range []store.Role{store.Owner, store.Editor, store.Viewer} {
			var id string
			// The first team's owner is the fixture's administrator.
			if i == 0 && team == teams[0].ID {
				id = w.ada
			} else {
				id, err = w.st.CreatePerson(ctx, by, w.ws, store.NewPerson{Name: names[i], Email: strings.ToLower(strings.Fields(names[i])[0]) + "@" + host})
				must(err)
				must(w.st.SetTeamMember(ctx, by, w.ws, team, id, role))
			}
			*[]*string{&s.owner, &s.editor, &s.viewer}[i] = id
			s.people = append(s.people, id, names[i])
		}
		s.project = project
		a := admin()
		c, err := w.st.CreateCheck(ctx, a, project, store.NewCheck{Name: ping, Interval: time.Hour, Grace: time.Hour})
		must(err)
		s.ping, s.pingName = c.ID, ping
		h, err := w.st.CreateCheck(ctx, a, project, store.NewCheck{Name: site, Kind: "http", Interval: time.Minute,
			Settings: map[string]any{"url": "https://" + host + "/private-" + site + "?token=hidden"}})
		must(err)
		s.site = h.ID
		p, err := w.st.CreateCheck(ctx, a, project, store.NewCheck{Name: "port of " + site, Kind: "tcp", Interval: time.Minute,
			Settings: map[string]any{"host": "db." + host, "port": 5432}})
		must(err)
		s.port = p.ID
		s.checks = append(s.checks, c.ID, h.ID, p.ID, c.PingUUID, ping, site)
		s.addresses = append(s.addresses, host)
		k, value, err := w.st.CreateAPIKey(ctx, a, project, key, store.ScopeManage, time.Now())
		must(err)
		s.key, s.keyValue = k.ID, value
		s.keys = append(s.keys, k.ID, key)
		s.probe, s.probeKey, err = w.st.CreateProbe(ctx, a, probe, project, nil)
		must(err)
		s.probes = append(s.probes, s.probe, probe)
		s.teams = append(s.teams, team)
		if teamName != "" {
			s.teams = append(s.teams, teamName)
		}
		// Something happened in it: a failure with an incident, and attempts.
		must(w.st.SetAlerting(ctx, by, w.ws, c.ID, "now", true, time.Now()))
		_, err = w.st.Ping(ctx, store.PingIn{CheckUUID: c.PingUUID, Kind: check.Fail, ExitStatus: 3, Method: "POST", At: time.Now(), Body: []byte("output of " + ping)})
		must(err)
		_, err = w.st.RecordAttempt(ctx, w.ws, h.ID, store.Attempt{Attempt: check.Attempt{Outcome: check.Failed, Reason: "refused by " + host, At: time.Now()}, Started: time.Now().Add(-time.Second)})
		must(err)
		// Its owner's own things, which nobody else may touch.
		ch, err := w.st.SaveEmail(ctx, w.ws, s.owner, "alerts@"+host)
		must(err)
		s.channel = ch.ID
		w.register(t, w.browser(t), s.owner)
		tokenFor(t, w.fixture, s.owner, store.ScopeManage)
		tokens, err := w.st.PersonTokens(ctx, w.ws, s.owner, time.Now())
		must(err)
		sessions, err := w.st.BrowserSessions(ctx, w.ws, s.owner, time.Now())
		must(err)
		s.signIn, s.session = tokens[0].ID, sessions[0].ID
		// A message that reached their editor by no way, which an administrator
		// reads without what it was about. Written as the store writes it for
		// someone who is in the project through its team.
		conn, err := sql.Open("sqlite", w.path)
		must(err)
		defer conn.Close()
		_, err = conn.Exec(`INSERT INTO log_entries (workspace_id, happened_at, recorded_at, project_id, project_name, kind, about_kind, about_id, about_name, by_kind, person_id, person_name, person_outsider, incident_id, details)
			VALUES (?, ?, ?, ?, 'a project', 'message.unreached', 'check', ?, ?, 'allstill', ?, ?, 2, 'inc_never', '{"why":"the person has no way to be reached"}')`,
			w.ws, time.Now().UnixMilli(), time.Now().UnixMilli(), project, c.ID, ping, s.editor, names[1])
		must(err)
	}
	// The first team is named as the installation, which every page says, so its
	// name is no secret; its id is.
	first, _, err := w.st.CreateProjectIn(ctx, by, w.ws, teams[0].ID, "Ledgers")
	must(err)
	fill(&w.a, teams[0].ID, "", first, [3]string{"Ada", "Edna Eastwick", "Vera Vale"}, "ledger-export", "books-site", "books.example.net", "ledger-key", "attic-probe")
	w.a.projects = append(w.a.projects, first, "Ledgers", w.project, "Customer", "nightly-backup", "customer-sync", w.checkID, w.hidden)
	w.a.people = append(w.a.people, w.bob, "bob@example.com", "ada@example.com")

	quay, _, err := w.st.CreateProjectIn(ctx, by, w.ws, second, "Quayside")
	must(err)
	fill(&w.b, second, "Harbour Works", quay, [3]string{"Hal Harrow", "Hedy Holt", "Hector Hale"}, "tide-table", "berth-site", "tides.example.org", "crane-key", "dock-probe")
	w.b.projects = append(w.b.projects, quay, "Quayside")
	// A project of the second team that has a name the first team uses too.
	same, _, err := w.st.CreateProjectIn(ctx, by, w.ws, second, "Backups")
	must(err)
	c, err := w.st.CreateCheck(ctx, admin(), same, store.NewCheck{Name: "harbour-backup", Interval: time.Hour, Grace: time.Hour})
	must(err)
	k, _, err := w.st.CreateAPIKey(ctx, admin(), same, "winch-key", store.ScopeRead, time.Now())
	must(err)
	w.b.projects = append(w.b.projects, same)
	w.b.checks = append(w.b.checks, c.ID, c.PingUUID, "harbour-backup")
	w.b.keys = append(w.b.keys, k.ID, "winch-key")
	w.rest = side{
		project: same, ping: c.ID, site: c.ID, port: c.ID, pingName: "harbour-backup", key: k.ID,
		owner: w.b.owner, editor: w.b.editor, channel: w.b.channel, signIn: w.b.signIn, session: w.b.session,
		projects: []string{same}, checks: []string{c.ID, c.PingUUID, "harbour-backup"}, people: w.b.people, teams: w.b.teams, keys: []string{k.ID, "winch-key"},
	}

	w.zed, err = w.st.CreatePerson(ctx, by, w.ws, store.NewPerson{Name: "Zed Zane", WorkspaceAdmin: true, InNoTeam: true})
	must(err)
	for name, role := range map[string]store.Role{"Gus Guest": store.Viewer, "Enid Outside": store.Editor, "Otto Outside": store.Owner} {
		id, err := w.st.CreatePerson(ctx, by, w.ws, store.NewPerson{Name: name})
		must(err)
		must(w.st.SetMember(ctx, by, w.ws, quay, id, role))
		w.outside = append(w.outside, id)
		if role == store.Viewer {
			w.guest = id
		}
	}
	// Something someone in the team did there, and a choice of theirs, which
	// those outside the team read without a name.
	hedy, err := w.st.AccessOfPerson(ctx, w.ws, w.b.editor)
	must(err)
	must(w.st.PauseCheck(ctx, hedy, w.b.site, time.Now()))
	must(w.st.ResumeCheck(ctx, hedy, w.b.site, time.Now()))
	must(w.st.SetMember(ctx, by, w.ws, quay, w.b.viewer, store.Viewer))
	must(w.st.SetMember(ctx, by, w.ws, quay, w.b.editor, store.Viewer))
	must(w.st.RemoveMember(ctx, by, w.ws, quay, w.b.editor))

	// What each person would be told is worked out and recorded, in shadow mode.
	sender := &alert.Sender{Store: w.st, Log: slog.New(slog.DiscardHandler), Now: time.Now}
	must(sender.Pass(ctx))
	// And a day on, what their digests would have said.
	sender.Now = func() time.Time { return time.Now().Add(26 * time.Hour) }
	must(sender.Pass(ctx))
	return w
}

// wallRequest is one request and what it must not be.
type wallRequest struct {
	method, path string
	form         url.Values
	body         any
	// names is what of the other team it asks for, by id or by name. Its answer
	// must be the answer to asking for something that does not exist.
	names string
}

// twin is the same request for something that does not exist.
func (r wallRequest) twin() wallRequest {
	never := "never-there"
	if kind, _, ok := strings.Cut(r.names, "_"); ok && len(kind) <= 4 {
		never = kind + "_00000000000000000000000000"
	}
	t := r
	t.path = strings.ReplaceAll(r.path, url.QueryEscape(r.names), never)
	t.form = url.Values{}
	for k, v := range r.form {
		t.form[k] = []string{strings.ReplaceAll(v[0], r.names, never)}
	}
	if r.body != nil {
		raw, _ := json.Marshal(r.body)
		t.body = json.RawMessage(strings.ReplaceAll(string(raw), r.names, never))
	}
	t.names = never
	return t
}

// pagesAbout are the dashboard's requests, aimed at the other side where a
// request names anything.
func pagesAbout(o side) []wallRequest {
	reqs := []wallRequest{
		{method: "GET", path: "/"}, {method: "GET", path: "/?show=attention"}, {method: "GET", path: "/?show=paused"},
		{method: "GET", path: "/?q=" + url.QueryEscape(o.pingName), names: o.pingName}, {method: "GET", path: "/?project=" + o.project, names: o.project},
		{method: "GET", path: "/checks/new"}, {method: "GET", path: "/checks/new?project=" + o.project, names: o.project},
		{method: "GET", path: "/messages"}, {method: "GET", path: "/channels"},
		{method: "GET", path: "/log"}, {method: "GET", path: "/log?late=1"}, {method: "GET", path: "/log?before=40"},
		{method: "GET", path: "/log?group=" + o.ping, names: o.ping}, {method: "GET", path: "/log?project=" + o.project, names: o.project},
		{method: "GET", path: "/log?name=" + url.QueryEscape(o.pingName), names: o.pingName}, {method: "GET", path: "/log?person=" + o.editor, names: o.editor},
		{method: "GET", path: "/log?person=" + o.owner, names: o.owner},
	}
	for _, what := range []string{"done", "happened", "reached", "signins"} {
		reqs = append(reqs, wallRequest{method: "GET", path: "/log?what=" + what})
	}
	for _, id := range []string{o.ping, o.site, o.port} {
		reqs = append(reqs, wallRequest{method: "GET", path: "/log?check=" + id, names: id}, wallRequest{method: "GET", path: "/checks/" + id, names: id})
		for _, action := range []string{"edit", "pause", "move", "delete", "ping-url"} {
			reqs = append(reqs, wallRequest{method: "GET", path: "/checks/" + id + "/" + action, names: id})
		}
		for _, action := range []string{"edit", "pause", "resume", "move", "delete", "ping-url"} {
			reqs = append(reqs, wallRequest{method: "POST", path: "/checks/" + id + "/" + action, form: url.Values{"project": {o.project}, "name": {"x"}}, names: id})
		}
	}
	// What belongs to one person: nobody else tests, confirms or removes their
	// address, or ends their sign-ins.
	reqs = append(reqs,
		wallRequest{method: "POST", path: "/channels/" + o.channel + "/test", names: o.channel},
		wallRequest{method: "GET", path: "/channels/" + o.channel + "/confirm?token=x", names: o.channel},
		wallRequest{method: "POST", path: "/channels/" + o.channel + "/delete", names: o.channel},
		wallRequest{method: "POST", path: "/sign-ins/" + o.signIn + "/end", names: o.signIn},
		wallRequest{method: "POST", path: "/sessions/" + o.session + "/end", names: o.session},
	)
	reqs = append(reqs, wallRequest{method: "POST", path: "/checks/new", form: url.Values{"project": {o.project}, "kind": {"ping"}, "name": {"planted"}, "every_n": {"1"}, "every_unit": {"hours"}}, names: o.project})
	return reqs
}

// apiAbout are the API's requests, one or more for every operation a person or
// a key may call, aimed at the other side.
func apiAbout(o side, own side) map[string][]wallRequest {
	check := map[string]any{"name": "planted", "kind": "ping"}
	return map[string][]wallRequest{
		"GetMe":        {{method: "GET", path: "/me"}},
		"EndThisToken": nil, // ends the walker's own sign-in, and names nothing: device_test.go
		"GetStatus":    {{method: "GET", path: "/status"}},
		"ListLog": {
			{method: "GET", path: "/log"}, {method: "GET", path: "/log?late=true"}, {method: "GET", path: "/log?limit=3"}, {method: "GET", path: "/log?limit=3&before=40"},
			{method: "GET", path: "/log?group=" + o.ping, names: o.ping}, {method: "GET", path: "/log?project_id=" + o.project, names: o.project},
			{method: "GET", path: "/log?check_id=" + o.ping, names: o.ping}, {method: "GET", path: "/log?check_id=" + o.site, names: o.site},
			{method: "GET", path: "/log?name=" + url.QueryEscape(o.pingName), names: o.pingName}, {method: "GET", path: "/log?person_id=" + o.editor, names: o.editor},
			{method: "GET", path: "/log?what=reached"}, {method: "GET", path: "/log?what=done"}, {method: "GET", path: "/log?what=signins"},
		},
		"ListProbes":   {{method: "GET", path: "/probes"}},
		"ListProjects": {{method: "GET", path: "/projects"}},
		"GetProject":   {{method: "GET", path: "/projects/" + o.project, names: o.project}},
		"ListChecks":   {{method: "GET", path: "/projects/" + o.project + "/checks", names: o.project}},
		"CreateCheck":  {{method: "POST", path: "/projects/" + o.project + "/checks", body: check, names: o.project}},
		"GetCheck":     {{method: "GET", path: "/checks/" + o.ping, names: o.ping}, {method: "GET", path: "/checks/" + o.site, names: o.site}},
		"UpdateCheck":  {{method: "PATCH", path: "/checks/" + o.ping, body: map[string]any{"name": "planted"}, names: o.ping}},
		"DeleteCheck":  {{method: "DELETE", path: "/checks/" + o.port, names: o.port}},
		"PauseCheck":   {{method: "POST", path: "/checks/" + o.ping + "/pause", names: o.ping}},
		"ResumeCheck":  {{method: "POST", path: "/checks/" + o.ping + "/resume", names: o.ping}},
		"MoveCheck": {
			{method: "POST", path: "/checks/" + o.ping + "/move", body: map[string]any{"project_id": own.project}, names: o.ping},
			// Their own check into the other team's project, which they cannot see.
			{method: "POST", path: "/checks/" + own.ping + "/move", body: map[string]any{"project_id": o.project}, names: o.project},
		},
		"GetPingUrl":    {{method: "GET", path: "/checks/" + o.ping + "/ping-url", names: o.ping}},
		"RotatePingUrl": {{method: "POST", path: "/checks/" + o.ping + "/ping-url", names: o.ping}},
		"ListRuns":      {{method: "GET", path: "/checks/" + o.ping + "/runs", names: o.ping}, {method: "GET", path: "/checks/" + o.site + "/runs", names: o.site}},
		"ListKeys":      {{method: "GET", path: "/keys"}, {method: "GET", path: "/keys?project_id=" + o.project, names: o.project}},
		"CreateKey":     {{method: "POST", path: "/projects/" + o.project + "/keys", body: map[string]any{"name": "planted", "scope": "read"}, names: o.project}},
		"RevokeKey":     {{method: "DELETE", path: "/keys/" + o.key, names: o.key}},
	}
}

func bearerWith(f fixture, method, path, token string, body any) *httptest.ResponseRecorder {
	var raw []byte
	if body != nil {
		raw, _ = json.Marshal(body)
	}
	req := httptest.NewRequest(method, site+"/api/v1"+path, bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Allstill-Probe-Protocol", strconv.Itoa(api.ProbeProtocolVersion))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	return rec
}

// snapshot is what the walk must leave as it was: every check, key and address,
// and whether each sign-in and session still stands.
func (w twoTeams) snapshot(t *testing.T) []string {
	t.Helper()
	conn, err := sql.Open("sqlite", w.path)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var all []string
	for _, q := range []string{
		`SELECT id || ' ' || project_id || ' ' || name || ' ' || state || ' ' || ping_uuid || ' ' || COALESCE(deleted_at, 0) || ' ' || settings || ' ' || urgency || critical FROM checks ORDER BY id`,
		`SELECT project_id || ' ' || person_id || ' ' || role FROM memberships ORDER BY 1`,
		`SELECT project_id || ' ' || person_id || ' ' || alerts FROM alert_choices ORDER BY 1`,
		`SELECT team_id || ' ' || person_id || ' ' || role FROM team_members ORDER BY 1`,
		`SELECT id || ' ' || project_id || ' ' || name FROM api_keys ORDER BY id`,
		`SELECT id || ' ' || COALESCE(person_id, '') FROM channels ORDER BY id`,
		`SELECT count(*) FROM deliveries WHERE kind = 'test'`,
	} {
		rows, err := conn.Query(q)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				t.Fatal(err)
			}
			all = append(all, line)
		}
		rows.Close()
	}
	for _, s := range []side{w.a, w.b} {
		tokens, _ := w.st.PersonTokens(t.Context(), w.ws, s.owner, time.Now())
		sessions, _ := w.st.BrowserSessions(t.Context(), w.ws, s.owner, time.Now())
		all = append(all, fmt.Sprint(len(tokens) > 0 && tokens[len(tokens)-1].ID != "", " ", len(sessions) > 0))
		for _, tk := range tokens {
			if tk.ID == s.signIn {
				all = append(all, "sign-in stands")
			}
		}
		for _, se := range sessions {
			if se.ID == s.session {
				all = append(all, "session stands")
			}
		}
	}
	return all
}

func TestNothingOfAnotherTeamComesBack(t *testing.T) {
	w := setupTwoTeams(t)
	// An administrator is told who has an account, which teams there are and
	// which probes (R10, R11, R40), and nothing of a project or a check.
	forAdmin := func(s side) side {
		s.people, s.teams, s.probes = nil, nil, nil
		return s
	}
	type walker struct {
		name   string
		person string // "" for a key
		token  string
		others []side // what they must not read
		aims   []side // whose things their requests name
		own    side
		admin  bool
		proof  string // something of their own that must come back, or the walk proves nothing
	}
	walkers := []walker{
		{name: "the first team's owner, an administrator", person: w.a.owner, others: []side{forAdmin(w.b)}, aims: []side{w.b}, own: w.a, admin: true, proof: w.a.pingName},
		{name: "the first team's editor", person: w.a.editor, others: []side{w.b}, aims: []side{w.b}, own: w.a, proof: w.a.pingName},
		{name: "the first team's viewer", person: w.a.viewer, others: []side{w.b}, aims: []side{w.b}, own: w.a, proof: w.a.pingName},
		{name: "the second team's owner", person: w.b.owner, others: []side{w.a}, aims: []side{w.a}, own: w.b, proof: w.b.pingName},
		{name: "the second team's editor", person: w.b.editor, others: []side{w.a}, aims: []side{w.a}, own: w.b, proof: w.b.pingName},
		{name: "the second team's viewer", person: w.b.viewer, others: []side{w.a}, aims: []side{w.a}, own: w.b, proof: w.b.pingName},
		{name: "an administrator in no team", person: w.zed, others: []side{forAdmin(w.a), forAdmin(w.b)}, aims: []side{w.a, w.b}, own: w.a, admin: true, proof: "has no way to be reached"},
		{name: "a key of the first team's project", token: w.a.keyValue, others: []side{w.b}, aims: []side{w.b}, own: w.a, proof: w.a.pingName},
		{name: "a key of the second team's project", token: w.b.keyValue, others: []side{w.a}, aims: []side{w.a}, own: w.b, proof: w.b.pingName},
	}
	// Someone with a role of their own in one project sees that project and
	// nothing else of its team: not its other project, its people or its name.
	for _, role := range []string{"viewer", "editor", "owner"} {
		for _, id := range w.outside {
			a, err := w.st.AccessOfPerson(t.Context(), w.ws, id)
			if err != nil {
				t.Fatal(err)
			}
			if string(a.Projects[0].Role) == role {
				walkers = append(walkers, walker{name: "someone in no team, " + role + " of one project of the second", person: id,
					others: []side{w.a, w.rest}, aims: []side{w.a, w.rest}, own: w.b, proof: w.b.pingName})
			}
		}
	}
	clean := func(who, what, body string, others []side) {
		t.Helper()
		for _, o := range others {
			for _, secret := range o.everything() {
				if secret != "" && strings.Contains(body, secret) {
					t.Errorf("%s reads %q of another team at %s", who, secret, what)
				}
			}
		}
	}
	for _, op := range apiOperations {
		if _, ok := apiAbout(w.a, w.b)[op.name]; !ok && op.takes != probeCredential {
			t.Errorf("%s is not walked: add it to apiAbout", op.name)
		}
	}
	before := w.snapshot(t)
	digests := map[string]int{}
	for _, who := range walkers {
		var seen strings.Builder
		proven := func(route string) {
			t.Helper()
			if !strings.Contains(seen.String(), who.proof) {
				t.Errorf("%s never reads %q of their own %s, so reading nothing of another team there proves nothing", who.name, who.proof, route)
			}
			seen.Reset()
		}
		ask := func(what string, do func(wallRequest) (int, string), r wallRequest) {
			t.Helper()
			code, body := do(r)
			seen.WriteString(body)
			if r.names == "" {
				clean(who.name, what, body, who.others)
				return
			}
			// A page says back what was asked for, which is no answer: apart from
			// that, it must be what asking for something that does not exist gives.
			never := r.twin()
			neverCode, neverBody := do(never)
			body = strings.ReplaceAll(strings.ReplaceAll(body, url.QueryEscape(r.names), "ASKED"), r.names, "ASKED")
			neverBody = strings.ReplaceAll(strings.ReplaceAll(neverBody, url.QueryEscape(never.names), "ASKED"), never.names, "ASKED")
			clean(who.name, what, body, who.others)
			// Who has an account is an administrator's to know (R11).
			if who.admin && strings.HasPrefix(r.names, "per_") {
				return
			}
			if code != neverCode || body != neverBody {
				t.Errorf("%s: %s answers %d, and %d for something that does not exist; the answers differ at:\n%s", who.name, what, code, neverCode, firstDifference(body, neverBody))
			}
		}
		tokens := map[string]string{"": who.token}
		if who.person != "" {
			b := w.browser(t)
			w.register(t, b, who.person)
			page := func(r wallRequest) (int, string) {
				var rec *httptest.ResponseRecorder
				if r.method == "POST" {
					rec = b.post(r.path, r.form)
				} else {
					rec = b.get(r.path)
				}
				return rec.Code, rec.Body.String() + rec.Header().Get("Location")
			}
			for _, aim := range who.aims {
				for _, r := range pagesAbout(aim) {
					ask(r.method+" "+r.path, page, r)
				}
			}
			proven("on a page")
			// A sign-in that may change things, and one that may only read.
			tokens = map[string]string{"": tokenFor(t, w.fixture, who.person, store.ScopeManage), " read-only": tokenFor(t, w.fixture, who.person, store.ScopeRead)}
		}
		for scope, token := range tokens {
			call := func(r wallRequest) (int, string) {
				rec := bearerWith(w.fixture, r.method, r.path, token, r.body)
				return rec.Code, rec.Body.String()
			}
			for _, aim := range who.aims {
				for name, reqs := range apiAbout(aim, who.own) {
					for _, r := range reqs {
						ask(name+scope+" "+r.path, call, r)
					}
				}
			}
			// A read-only sign-in is no administrator's, so one who is in no team
			// reads nothing through it at all.
			if who.person == w.zed && scope != "" {
				seen.Reset()
				continue
			}
			proven("over the API" + scope)
		}
		if who.person != "" {
			// What they were or would have been told, digests among it.
			messages, err := w.st.Messages(t.Context(), who.person, 100)
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(messages)
			clean(who.name, "their messages", string(raw), who.others)
			for _, m := range messages {
				if m.Kind == "digest" {
					digests[who.own.team]++
				}
			}
		}
	}
	after := w.snapshot(t)
	if len(before) != len(after) {
		t.Errorf("the walk changed something: %d lines of what there is, now %d", len(before), len(after))
	}
	for i := range min(len(before), len(after)) {
		if before[i] != after[i] {
			t.Errorf("the walk changed something: %q is now %q", before[i], after[i])
		}
	}
	// A probe is given its own team's checks, and what it says of another
	// team's check is not taken.
	for _, s := range []struct{ own, other side }{{w.a, w.b}, {w.b, w.a}} {
		rec := bearerWith(w.fixture, "GET", "/probe/checks", s.own.probeKey, nil)
		clean("a probe", "its checks", rec.Body.String(), []side{s.other})
		if rec.Code != http.StatusOK {
			t.Errorf("a probe's checks: %d %s", rec.Code, rec.Body)
		}
		rec = bearerWith(w.fixture, "POST", "/probe/runs", s.own.probeKey, map[string]any{
			"sent_at": time.Now().UTC().Format(time.RFC3339),
			"runs":    []map[string]any{{"id": "planted", "check_id": s.other.site, "outcome": "passed", "ended_ago": 1}},
		})
		clean("a probe", "its runs", rec.Body.String(), []side{s.other})
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"applied":0`) {
			t.Errorf("a probe's run for another team's check: %d %s", rec.Code, rec.Body)
		}
	}
	// The digests were part of what was read, for someone of each team.
	for _, side := range []side{w.a, w.b} {
		if digests[side.team] == 0 {
			t.Errorf("no digest was read by anyone of team %s", side.team)
		}
	}
}

// An administrator who is in no team keeps the installation and is shown no
// project: every page of one answers as one that does not exist (R8).
func TestAnAdministratorIsShownNoProjectByBeingOne(t *testing.T) {
	w := setupTwoTeams(t)
	b := w.browser(t)
	w.register(t, b, w.zed)
	for _, id := range []string{w.a.ping, w.b.ping, w.checkID} {
		if rec := b.get("/checks/" + id); rec.Code != http.StatusNotFound {
			t.Errorf("/checks/%s: %d", id, rec.Code)
		}
	}
	token := tokenFor(t, w.fixture, w.zed, store.ScopeManage)
	var me struct {
		Projects []any `json:"projects"`
		Teams    []any `json:"teams"`
		Person   struct {
			Admin bool `json:"workspace_admin"`
		} `json:"person"`
	}
	if err := json.Unmarshal(bearerWith(w.fixture, "GET", "/me", token, nil).Body.Bytes(), &me); err != nil {
		t.Fatal(err)
	}
	if len(me.Projects) != 0 || len(me.Teams) != 0 || !me.Person.Admin {
		t.Errorf("%+v", me)
	}
}

// firstDifference is the first line two answers differ in, with the one before.
func firstDifference(a, b string) string {
	as, bs := strings.Split(a, "\n"), strings.Split(b, "\n")
	for i := range as {
		if i >= len(bs) || as[i] != bs[i] {
			other := "(nothing)"
			if i < len(bs) {
				other = bs[i]
			}
			return strings.TrimSpace(as[max(i-1, 0)]) + "\n  theirs: " + strings.TrimSpace(as[i]) + "\n  nothing's: " + strings.TrimSpace(other)
		}
	}
	return "(the second is longer)"
}

// With one team nobody meets the word, beyond the log's "the team" (R6). Its
// owner does where they are told what a command on the server did in it.
func TestOneTeamIsNotSpokenOf(t *testing.T) {
	f := setup(t)
	for _, person := range []string{f.ada, f.bob} {
		b := f.browser(t)
		f.register(t, b, person)
		for _, path := range []string{"/", "/?project=" + f.project, "/checks/" + f.checkID, "/checks/new", "/log", "/messages", "/channels"} {
			page := strings.ToLower(b.get(path).Body.String())
			// The owner is told there what commands on the server did in the team
			// they own, in those words; the rest of the page is held as any other.
			if start := strings.Index(page, `aria-labelledby="notices"`); person == f.ada && start >= 0 {
				page = page[:start] + page[start+strings.Index(page[start:], "</section>"):]
			}
			page = strings.ReplaceAll(page, "the team", "")
			if i := strings.Index(page, "team"); i >= 0 {
				t.Errorf("%s speaks of a team: …%s…", path, page[max(i-60, 0):min(i+60, len(page))])
			}
		}
	}
}

// What a command on the server did in a team is at the top of its owner's
// overview until they have opened Messages, where it stays listed (R29).
func TestANoticeStaysOnTheOverviewUntilRead(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	ada := f.browser(t)
	f.register(t, ada, f.ada)
	const told = "A command on the server (set-member) made Bob editor of Backups."
	if strings.Contains(ada.get("/").Body.String(), "A command on the server") {
		t.Fatal("a notice before there is one")
	}
	if err := f.st.SetMember(ctx, store.Command("set-member"), f.ws, f.project, f.bob, store.Editor); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ada.get("/").Body.String(), told) || !strings.Contains(ada.get("/?show=attention").Body.String(), told) {
		t.Error("the owner's overview does not say it")
	}
	// The person it was done to is not the one who is told.
	bob := f.browser(t)
	f.register(t, bob, f.bob)
	for _, path := range []string{"/", "/messages"} {
		if strings.Contains(bob.get(path).Body.String(), "A command on the server") {
			t.Errorf("%s tells someone who owns no team", path)
		}
	}
	if page := ada.get("/messages").Body.String(); !strings.Contains(page, told) || !strings.Contains(page, "New") {
		t.Errorf("Messages does not list it as new: %s", page)
	}
	if strings.Contains(ada.get("/").Body.String(), told) {
		t.Error("read, it is still on the overview")
	}
	if !strings.Contains(ada.get("/messages").Body.String(), told) {
		t.Error("read, it is no longer listed")
	}
}

// Making a team, naming its owner and making a sign-in link stay commands on
// the server: a stolen sign-in must not be able to do them (R30).
func TestWhatStaysOnTheServer(t *testing.T) {
	for _, op := range apiOperations {
		for _, word := range []string{"Team", "Owner", "Member", "Link", "Passkey", "Person", "People"} {
			if strings.Contains(op.name, word) {
				t.Errorf("%s is served over the API: decide in a spec whether that may leave the server (intent/teams/spec.md, R30)", op.name)
			}
		}
	}
}

// The private ranges the server was given are open to the teams they were
// opened to: the first team after an upgrade, and no new team until said (R13).
func TestPrivateRangesAreOpenedPerTeam(t *testing.T) {
	w := setupTwoTeams(t)
	w.srv.Allow = []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}
	tried := map[bool]int{}
	w.srv.Try = func(_ context.Context, c active.Check) active.Result {
		tried[c.Private]++
		return active.Result{Outcome: check.Passed, Measured: map[string]float64{"status": 200, "ms": 12}}
	}
	form := func(project string) url.Values {
		v := w.typed("http", "url", "http://192.0.2.7/")
		v.Set("project", project)
		return v
	}
	theirs := w.browser(t)
	w.register(t, theirs, w.b.editor)
	if rec := theirs.post("/checks/new", form(w.b.project)); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "not on the public internet") {
		t.Errorf("a team the ranges are not open to saved a check on one: %d", rec.Code)
	}
	ours := w.browser(t)
	w.register(t, ours, w.a.editor)
	if rec := ours.post("/checks/new", form(w.a.project)); rec.Code != http.StatusSeeOther {
		t.Errorf("the first team was refused its own range: %d %s", rec.Code, rec.Body)
	}
	// Opened to the second team, it may too; and what is tried from the form
	// is tried as the team's, never with ranges it was not given.
	yes := true
	if err := w.st.SetTeam(t.Context(), store.Command("set-team"), w.ws, w.b.team, store.TeamChange{Private: &yes}); err != nil {
		t.Fatal(err)
	}
	if rec := theirs.post("/checks/new", form(w.b.project)); rec.Code != http.StatusSeeOther {
		t.Errorf("opened to them, still refused: %d %s", rec.Code, rec.Body)
	}
	if tried[false] != 0 {
		t.Errorf("%d tries were made without the ranges for a team that has them", tried[false])
	}
}

// An administrator is shown the teams they are not in by name, owners and
// counts, and nothing else of them (R10).
func TestAnAdministratorSeesTeamsAsCounts(t *testing.T) {
	w := setupTwoTeams(t)
	for who, person := range map[string]string{"in no team": w.zed, "who owns the first": w.a.owner} {
		b := w.browser(t)
		w.register(t, b, person)
		page := b.get("/").Body.String()
		if !strings.Contains(page, "Harbour Works") || !strings.Contains(page, "Owned by Hal Harrow") || !strings.Contains(page, "3 people, 2 projects, 5 checks") {
			t.Errorf("an administrator %s is not shown the other team", who)
		}
	}
	for who, person := range map[string]string{"another team's owner": w.b.owner, "the first team's editor": w.a.editor} {
		b := w.browser(t)
		w.register(t, b, person)
		if strings.Contains(b.get("/").Body.String(), "Other teams") {
			t.Errorf("%s is shown the teams of the installation", who)
		}
	}
	// With one team there is nothing to list.
	f := setup(t)
	b := f.browser(t)
	f.register(t, b, f.ada)
	if strings.Contains(b.get("/").Body.String(), "Other teams") {
		t.Error("an installation with one team lists teams")
	}
}
