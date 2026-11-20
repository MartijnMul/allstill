// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/MartijnMul/allstill/internal/active"
	"github.com/MartijnMul/allstill/internal/api"
	"github.com/MartijnMul/allstill/internal/check"
	"github.com/MartijnMul/allstill/internal/store"
)

// What a credential can change (intent/cli-anywhere/spec.md, R20 to R22). The rules are
// the store's, the same as for the form and the server's commands; what is here reads
// a request, and says what came of it.

// refusalOf turns what the store refused into what to say: 404 for what the caller may
// not see, 403 for what their role forbids (R36). What it does not know is not a
// refusal, but something gone wrong.
func refusalOf(ctx context.Context, err error) (api.Problem, bool) {
	if errors.Is(err, store.ErrNotFound) {
		return problem(http.StatusNotFound, "There is nothing here, or you may not see it.", ""), true
	}
	if errors.Is(err, store.ErrForbidden) {
		return forbidden(ctx), true
	}
	if errors.Is(err, store.ErrChanged) {
		return problem(http.StatusPreconditionFailed, "Someone changed this check since it was read.", "Nothing was saved. Read it again, and try again."), true
	}
	if inv, ok := errors.AsType[*store.InvalidError](err); ok {
		detail := refusalDetail(inv)
		p := problem(http.StatusUnprocessableEntity, "That cannot be saved.", detail)
		if pointer := pointerOf(inv.Field); pointer != "" {
			errs := []struct {
				Detail  string `json:"detail"`
				Pointer string `json:"pointer"`
			}{{detail, pointer}}
			p.Errors = &errs
		}
		return p, true
	}
	return api.Problem{}, false
}

// fieldNames are how a refusal names a field. A form puts its refusal beside the field,
// and here nothing else says which one is meant.
var fieldNames = map[string]string{
	"name": "The name", "slug": "The slug", "description": "The description", "tags": "Tags", "grace": "The grace period",
	"schedule": "The schedule", "schedule.every": "The interval", "urgency": "The urgency", "kind": "The kind", "scope": "The scope",
	"url": "The address", "host": "The host", "port": "The port", "method": "The method", "location": "The location", "timeout": "The timeout",
	"probe": "The probe", "project": "The project", "project_id": "The project", "team_id": "The team",
	"role": "The role", "label": "The label",
}

// refusalDetail says what is wrong, naming the field when the store's words need one:
// they read "must be at least 60 seconds", or say all there is in a sentence of their own.
func refusalDetail(inv *store.InvalidError) string {
	name, ok := fieldNames[inv.Field]
	if !ok {
		name = "The setting " + inv.Field
	}
	first, _, _ := strings.Cut(inv.Problem, " ")
	switch first {
	case "must", "is", "cannot", "holds", "needs", "checks", "does", "follows":
		return sentence(name + " " + inv.Problem)
	case "at", "each":
		return sentence(name + ": " + inv.Problem)
	}
	return sentence(inv.Problem)
}

// forbidden says who could do this, not a status code (R47).
func forbidden(ctx context.Context) api.Problem {
	c, ok := ctx.Value(callerContextKey{}).(caller)
	if ok && c.scope() == store.ScopeRead {
		what := strings.ToUpper(c.what()[:1]) + c.what()[1:]
		return problem(http.StatusForbidden, what+" can only read.", "To change things, it takes one with the manage scope.")
	}
	return problem(http.StatusForbidden, "Your role does not allow this.", "Ask an owner of this project, or an administrator.")
}

// pointerOf says where in a request a field the store refused is, when that does not
// depend on the kind of check.
func pointerOf(field string) string {
	switch field {
	case "name", "slug", "description", "tags", "kind", "grace", "scope", "project_id", "team_id", "role", "label":
		return "/" + field
	case "schedule", "schedule.every":
		return "/" + strings.ReplaceAll(field, ".", "/")
	case "urgency", "critical":
		return "/alerting/" + field
	case "url", "method", "location", "expected_status", "follow_redirects", "json_field":
		return "/http/" + field
	case "host", "port", "tls":
		return "/tcp/" + field
	}
	return ""
}

// etagOf is a check's version, for If-Match. It changes with its settings, not with its
// state.
func etagOf(c store.CheckView) string { return fmt.Sprintf(`"%d"`, c.Version) }

// versionIn reads an ETag. One this server did not make names no version it has, which
// is how it should be taken.
func versionIn(match *string) (int64, error) {
	if match == nil || *match == "" || *match == "*" { // any version, as HTTP says
		return 0, nil
	}
	n, err := strconv.ParseInt(strings.Trim(*match, `"`), 10, 64)
	if err != nil || n <= 0 {
		return 0, store.ErrChanged
	}
	return n, nil
}

// as turns one of the generated answers into another that holds the same fields and more.
func as[T any](v any) (T, error) {
	var out T
	raw, err := json.Marshal(v)
	if err != nil {
		return out, err
	}
	return out, json.Unmarshal(raw, &out)
}

// forChange reads a check for a caller who means to change it: not found when they may
// not see it, forbidden when they may only look.
func (a apiServer) forChange(ctx context.Context, checkID string) (store.CheckView, store.Access, error) {
	access := callerOf(ctx).Access
	c, err := a.s.Store.Check(ctx, access, checkID)
	if err != nil {
		return c, access, err
	}
	if !access.SeesSecrets(c.ProjectID) {
		return c, access, store.ErrForbidden
	}
	return c, access, nil
}

// settingsIn is what a request says a check Allstill runs should run, as the store keeps
// it, with the probe apart.
func settingsIn(kind string, h *api.HttpCheck, t *api.TcpCheck) (map[string]any, string, error) {
	var from any
	var probe *string
	switch {
	case kind == "http" && t != nil, kind == "tcp" && h != nil, !store.Active(kind) && (h != nil || t != nil):
		return nil, "", &store.InvalidError{Field: "kind", Problem: "http goes with an HTTP check and tcp with a TCP check, and a job's check has neither"}
	case kind == "http" && h != nil:
		from, probe = h, h.ProbeId
	case kind == "tcp" && t != nil:
		from, probe = t, t.ProbeId
	default:
		return nil, "", nil
	}
	settings, err := as[map[string]any](from)
	if err != nil {
		return nil, "", err
	}
	delete(settings, "probe_id")
	if probe != nil {
		return settings, *probe, nil
	}
	return settings, "", nil
}

// hostIn is the host a check's settings connect to.
func hostIn(kind string, settings map[string]any) string {
	if kind == "tcp" {
		host, _ := settings["host"].(string)
		return host
	}
	url, _ := settings["url"].(string)
	host, _ := active.HTTP{URL: url}.Host()
	return host
}

// addressField is the field that says where a check of this kind connects to.
func addressField(kind string) string {
	if kind == "tcp" {
		return "host"
	}
	return "url"
}

// maxSeconds is the longest span a request can hold, past which seconds no longer fit a
// duration and could come out as anything, also as none.
const maxSeconds = int64(1<<63-1) / int64(time.Second)

// scheduleIn reads a request's schedule.
func scheduleIn(s *api.Schedule) (interval time.Duration, cron, timeZone string, err error) {
	if s == nil {
		return 0, "", "", nil
	}
	kind, err := s.Discriminator()
	if err != nil {
		return 0, "", "", &store.InvalidError{Field: "schedule", Problem: "must be an interval or a cron expression"}
	}
	switch kind {
	case "interval":
		i, err := s.AsIntervalSchedule()
		if err != nil {
			return 0, "", "", &store.InvalidError{Field: "schedule", Problem: "must be an interval or a cron expression"}
		}
		// Zero would read as no schedule at all, past the store's rule.
		if i.Every <= 0 {
			return 0, "", "", &store.InvalidError{Field: "schedule.every", Problem: "must be at least 60 seconds"}
		}
		if i.Every > maxSeconds {
			return 0, "", "", &store.InvalidError{Field: "schedule.every", Problem: "is too long"}
		}
		return time.Duration(i.Every) * time.Second, "", "", nil
	case "cron":
		c, err := s.AsCronSchedule()
		if err != nil {
			return 0, "", "", &store.InvalidError{Field: "schedule", Problem: "must be an interval or a cron expression"}
		}
		if c.Expression == "" {
			return 0, "", "", &store.InvalidError{Field: "schedule", Problem: "must be an interval or a cron expression"}
		}
		if c.TimeZone == "" {
			return 0, "", "", &store.InvalidError{Field: "schedule", Problem: "needs a time zone, such as Europe/Amsterdam"}
		}
		return 0, c.Expression, c.TimeZone, nil
	}
	return 0, "", "", &store.InvalidError{Field: "schedule", Problem: "must be an interval or a cron expression"}
}

func tryOf(found tryFound, c store.CheckView) *api.CheckTry {
	out := api.CheckTry{Result: api.CheckTryResultNotTried, FirstAttemptAt: optionalTime(found.First)}
	why := func(w api.CheckTryWhyNot) *api.CheckTry {
		out.WhyNot = &w
		return &out
	}
	switch {
	case found.Probe != "":
		return why(api.CheckTryWhyNotProbe)
	case found.Limited:
		return why(api.CheckTryWhyNotLimit)
	case found.Result == nil:
		return why(api.CheckTryWhyNotUnavailable)
	}
	res := found.Result
	switch res.Outcome {
	case check.Passed:
		out.Result = api.CheckTryResultUp
	case check.Warned:
		out.Result = api.CheckTryResultWarning
	default:
		out.Result = api.CheckTryResultFails
		if !c.Status.EverPassed {
			nobody := true
			out.AlertsNobodyYet = &nobody
		}
	}
	out.Reason = optional(res.Reason)
	if status, ok := res.Measured["status"]; ok {
		code := int(status)
		out.HttpStatus = &code
	}
	if ms, ok := res.Measured["ms"]; ok {
		n := int64(ms)
		out.Ms = &n
	}
	if days, ok := res.Measured["cert_days"]; ok {
		out.CertDays = &days
	}
	return &out
}

// logged writes a change to the server's log with who made it, without a URL or a
// setting's value (manage-checks R29).
func (a apiServer) logged(ctx context.Context, checkID, change string) {
	c := callerOf(ctx)
	a.s.Log.Info("check changed", "by", c.id(), "check", checkID, "change", change)
}

// A new job's check that says no more is as the form's: once a day, with an hour's grace.
const (
	defaultJobInterval = 24 * time.Hour
	defaultJobGrace    = time.Hour
)

// CreateCheck adds a check, under the same rules as the form (R20).
func (a apiServer) CreateCheck(ctx context.Context, req api.CreateCheckRequestObject) (api.CreateCheckResponseObject, error) {
	c := callerOf(ctx)
	access := c.Access
	role, ok := access.Role(req.ProjectId)
	if !ok {
		return nil, store.ErrNotFound
	}
	if role == store.Viewer {
		return nil, store.ErrForbidden
	}
	in := req.Body
	kind := string(in.Kind)
	if kind != "ping" && !store.Active(kind) {
		return nil, &store.InvalidError{Field: "kind", Problem: "must be ping, http or tcp"}
	}
	n := store.NewCheck{Name: strings.TrimSpace(in.Name), Kind: kind}
	if in.Slug != nil {
		n.Slug = *in.Slug
	}
	if in.Description != nil {
		n.Description = *in.Description
	}
	if in.Tags != nil {
		n.Tags = *in.Tags
	}
	var err error
	if n.Interval, n.Cron, n.TimeZone, err = scheduleIn(in.Schedule); err != nil {
		return nil, err
	}
	if in.Grace != nil {
		if *in.Grace > maxSeconds {
			return nil, &store.InvalidError{Field: "grace", Problem: "is too long"}
		}
		n.Grace = time.Duration(*in.Grace) * time.Second
	}
	if n.Settings, n.ProbeID, err = settingsIn(kind, in.Http, in.Tcp); err != nil {
		return nil, err
	}
	if store.Active(kind) {
		if n.Settings == nil {
			return nil, &store.InvalidError{Field: addressField(kind), Problem: "is needed"}
		}
		if in.Grace != nil {
			return nil, &store.InvalidError{Field: "grace", Problem: "follows from the interval of a check Allstill runs itself"}
		}
		if n.Interval == 0 && n.Cron == "" {
			n.Interval = time.Minute
		}
		if n.ProbeID == "" {
			if problem := a.s.addressProblem(ctx, c.id(), req.ProjectId, hostIn(kind, n.Settings)); problem != "" {
				return nil, &store.InvalidError{Field: addressField(kind), Problem: problem}
			}
		}
	} else {
		if n.Interval == 0 && n.Cron == "" {
			n.Interval = defaultJobInterval
		}
		if in.Grace == nil {
			n.Grace = defaultJobGrace
		}
	}
	if in.Alerting != nil {
		n.Urgency = "now"
		if in.Alerting.Urgency != nil {
			n.Urgency = string(*in.Alerting.Urgency)
		}
		n.Critical = in.Alerting.Critical != nil && *in.Alerting.Critical
	}
	created, err := a.s.Store.CreateCheck(ctx, access, req.ProjectId, n)
	if err != nil {
		return nil, err
	}
	a.logged(ctx, created.ID, "added")
	view, err := a.s.Store.Check(ctx, access, created.ID)
	if err != nil {
		return nil, err
	}
	read, err := a.checkOf(ctx, access, view)
	if err != nil {
		return nil, err
	}
	out, err := as[api.CheckCreated](read)
	if err != nil {
		return nil, err
	}
	if store.Active(kind) {
		out.Try = tryOf(a.s.tryCheck(ctx, c.id(), view), view)
	} else {
		url := a.s.base.String() + "/ping/" + created.PingUUID
		out.PingUrl = &url
	}
	if a.s.sameName(ctx, access, view) {
		same := true
		out.SameName = &same
	}
	return api.CreateCheck201JSONResponse{Body: out, Headers: api.CreateCheck201ResponseHeaders{
		Location: a.s.base.String() + "/api/v1/checks/" + created.ID,
	}}, nil
}

// UpdateCheck changes what a request says, under the same rules as the form (R20). A
// request that changes nothing saves nothing, so nobody else's read goes out of date.
func (a apiServer) UpdateCheck(ctx context.Context, req api.UpdateCheckRequestObject) (api.UpdateCheckResponseObject, error) {
	c, access, err := a.forChange(ctx, req.CheckId)
	if err != nil {
		return nil, err
	}
	in := req.Body
	if in.Kind != nil && string(*in.Kind) != c.Kind {
		return nil, &store.InvalidError{Field: "kind", Problem: "a check keeps its kind: add a new one instead"}
	}
	var ch store.Change
	if ch.Version, err = versionIn(req.Params.IfMatch); err != nil {
		return nil, err
	}
	if ch.Version != 0 && ch.Version != c.Version {
		return nil, store.ErrChanged
	}
	changed := false
	if in.Name != nil && *in.Name != c.Name {
		ch.Name, changed = in.Name, true
	}
	if in.Slug != nil && *in.Slug != c.Slug {
		ch.Slug, changed = in.Slug, true
	}
	if in.Description != nil && *in.Description != c.Description {
		ch.Description, changed = in.Description, true
	}
	if in.Tags != nil && !slices.Equal(*in.Tags, c.Tags) {
		ch.Tags, changed = in.Tags, true
	}
	interval, cron, timeZone, err := scheduleIn(in.Schedule)
	if err != nil {
		return nil, err
	}
	switch {
	case interval != 0 && (interval != c.Interval || c.Cron != ""):
		ch.Interval, changed = &interval, true
	case cron != "" && (cron != c.Cron || timeZone != c.TimeZone):
		ch.Cron, ch.TimeZone, changed = &cron, &timeZone, true
	}
	if in.Grace != nil {
		if *in.Grace > maxSeconds {
			return nil, &store.InvalidError{Field: "grace", Problem: "is too long"}
		}
		if grace := time.Duration(*in.Grace) * time.Second; grace != c.Grace {
			ch.Grace, changed = &grace, true
		}
	}
	settings, probe, err := settingsIn(c.Kind, in.Http, in.Tcp)
	if err != nil {
		return nil, err
	}
	addressChanged := false
	if settings != nil {
		before := map[string]any{}
		if err := json.Unmarshal(c.Settings, &before); err != nil {
			return nil, err
		}
		now, _ := json.Marshal(settings)
		then, _ := json.Marshal(before)
		// The same settings are not a change, which would make it wait for its next attempt.
		if string(now) != string(then) {
			ch.Settings, changed = settings, true
			addressChanged = !sameAddress(settings, before)
		}
		if probe != c.ProbeID {
			ch.Probe, changed = &probe, true
			addressChanged = true
		}
		if addressChanged && probe == "" {
			if problem := a.s.addressProblem(ctx, callerOf(ctx).id(), c.ProjectID, hostIn(c.Kind, settings)); problem != "" {
				return nil, &store.InvalidError{Field: addressField(c.Kind), Problem: problem}
			}
		}
	}
	if in.Alerting != nil {
		urgency, critical := c.Urgency, c.Critical
		if in.Alerting.Urgency != nil {
			urgency = string(*in.Alerting.Urgency)
		}
		if in.Alerting.Critical != nil {
			critical = *in.Alerting.Critical
		}
		if urgency != c.Urgency || critical != c.Critical {
			ch.Urgency, ch.Critical, changed = &urgency, &critical, true
		}
	}
	after := c
	if changed {
		deadline, err := a.s.Store.UpdateCheck(ctx, access, c.ID, ch, a.s.Now())
		if err != nil {
			return nil, err
		}
		if !deadline.IsZero() && a.s.Nudge != nil {
			a.s.Nudge(deadline)
		}
		a.logged(ctx, c.ID, "changed")
		if after, err = a.s.Store.Check(ctx, access, c.ID); err != nil {
			return nil, err
		}
	}
	read, err := a.checkOf(ctx, access, after)
	if err != nil {
		return nil, err
	}
	out, err := as[api.CheckTried](read)
	if err != nil {
		return nil, err
	}
	if addressChanged {
		out.Try = tryOf(a.s.tryCheck(ctx, callerOf(ctx).id(), after), after)
	}
	if ch.Name != nil && a.s.sameName(ctx, access, after) {
		same := true
		out.SameName = &same
	}
	return api.UpdateCheck200JSONResponse{Body: out, Headers: api.UpdateCheck200ResponseHeaders{ETag: etagOf(after)}}, nil
}

// sameAddress says two settings connect to the same place.
func sameAddress(a, b map[string]any) bool {
	for _, key := range []string{"url", "host", "port", "tls"} {
		x, _ := json.Marshal(a[key])
		y, _ := json.Marshal(b[key])
		if string(x) != string(y) {
			return false
		}
	}
	return true
}

// DeleteCheck deletes a check with its runs.
func (a apiServer) DeleteCheck(ctx context.Context, req api.DeleteCheckRequestObject) (api.DeleteCheckResponseObject, error) {
	if err := a.s.Store.DeleteCheck(ctx, callerOf(ctx).Access, req.CheckId, a.s.Now()); err != nil {
		return nil, err
	}
	a.logged(ctx, req.CheckId, "deleted")
	return api.DeleteCheck204Response{}, nil
}

// currentCheck is a check as it now is, for what answers with one.
func (a apiServer) currentCheck(ctx context.Context, access store.Access, checkID string) (api.Check, error) {
	c, err := a.s.Store.Check(ctx, access, checkID)
	if err != nil {
		return api.Check{}, err
	}
	return a.checkOf(ctx, access, c)
}

// PauseCheck pauses a check until it is resumed or pinged.
func (a apiServer) PauseCheck(ctx context.Context, req api.PauseCheckRequestObject) (api.PauseCheckResponseObject, error) {
	access := callerOf(ctx).Access
	if err := a.s.Store.PauseCheck(ctx, access, req.CheckId, a.s.Now()); err != nil {
		return nil, err
	}
	a.logged(ctx, req.CheckId, "paused")
	out, err := a.currentCheck(ctx, access, req.CheckId)
	if err != nil {
		return nil, err
	}
	return api.PauseCheck200JSONResponse(out), nil
}

// ResumeCheck resumes a paused check. One that is not paused stays as it is: someone else
// was first, or a job's ping resumed it.
func (a apiServer) ResumeCheck(ctx context.Context, req api.ResumeCheckRequestObject) (api.ResumeCheckResponseObject, error) {
	c, access, err := a.forChange(ctx, req.CheckId)
	if err != nil {
		return nil, err
	}
	if c.Status.State == check.Paused {
		if err := a.s.Store.ResumeCheck(ctx, access, req.CheckId, a.s.Now()); err != nil {
			return nil, err
		}
		a.logged(ctx, req.CheckId, "resumed")
	}
	out, err := a.currentCheck(ctx, access, req.CheckId)
	if err != nil {
		return nil, err
	}
	return api.ResumeCheck200JSONResponse(out), nil
}

// MoveCheck moves a check to another project the caller may edit.
func (a apiServer) MoveCheck(ctx context.Context, req api.MoveCheckRequestObject) (api.MoveCheckResponseObject, error) {
	_, access, err := a.forChange(ctx, req.CheckId)
	if err != nil {
		return nil, err
	}
	if req.Body.ProjectId == "" {
		return nil, &store.InvalidError{Field: "project_id", Problem: "is needed: say where it goes"}
	}
	if err := a.s.Store.MoveCheckAs(ctx, access, req.CheckId, req.Body.ProjectId, a.s.Now()); err != nil {
		return nil, err
	}
	a.logged(ctx, req.CheckId, "moved")
	out, err := a.currentCheck(ctx, access, req.CheckId)
	if err != nil {
		return nil, err
	}
	return api.MoveCheck200JSONResponse(out), nil
}

// GetPingUrl says a check's ping URL again, for those who may see it (R21).
func (a apiServer) GetPingUrl(ctx context.Context, req api.GetPingUrlRequestObject) (api.GetPingUrlResponseObject, error) {
	c, _, err := a.forChange(ctx, req.CheckId)
	if err != nil {
		return nil, err
	}
	if store.Active(c.Kind) {
		return nil, &store.InvalidError{Field: "kind", Problem: "Allstill runs this check itself, so nothing pings it"}
	}
	return api.GetPingUrl200JSONResponse{PingUrl: a.s.base.String() + "/ping/" + c.PingUUID}, nil
}

// RotatePingUrl gives a check a new ping URL, and the old one stops working at once.
func (a apiServer) RotatePingUrl(ctx context.Context, req api.RotatePingUrlRequestObject) (api.RotatePingUrlResponseObject, error) {
	access := callerOf(ctx).Access
	uuid, err := a.s.Store.RotatePingURL(ctx, access, req.CheckId, a.s.Now())
	if err != nil {
		return nil, err
	}
	a.logged(ctx, req.CheckId, "ping URL replaced")
	read, err := a.currentCheck(ctx, access, req.CheckId)
	if err != nil {
		return nil, err
	}
	out, err := as[api.CheckWithPingUrl](read)
	if err != nil {
		return nil, err
	}
	out.PingUrl = a.s.base.String() + "/ping/" + uuid
	return api.RotatePingUrl200JSONResponse(out), nil
}

func keyOf(k store.APIKey) api.ApiKey {
	return api.ApiKey{
		Id: k.ID, ProjectId: k.ProjectID, Name: k.Name, Scope: api.Scope(k.Scope),
		MadeBy: logPartyOf(k.MadeBy), MadeVia: optional(k.Via), CreatedAt: k.Created, LastUsedAt: optionalTime(k.LastUsed),
	}
}

// ListKeys lists a project's keys, or those of every project the caller owns.
func (a apiServer) ListKeys(ctx context.Context, req api.ListKeysRequestObject) (api.ListKeysResponseObject, error) {
	project := ""
	if req.Params.ProjectId != nil {
		project = *req.Params.ProjectId
	}
	keys, err := a.s.Store.APIKeys(ctx, callerOf(ctx).Access, project)
	if err != nil {
		return nil, err
	}
	out := api.ListKeys200JSONResponse{Items: make([]api.ApiKey, 0, len(keys))}
	for _, k := range keys {
		out.Items = append(out.Items, keyOf(k))
	}
	return out, nil
}

// CreateKey makes a key, and answers with its value, once.
func (a apiServer) CreateKey(ctx context.Context, req api.CreateKeyRequestObject) (api.CreateKeyResponseObject, error) {
	made, key, err := a.s.Store.CreateAPIKey(ctx, callerOf(ctx).Access, req.ProjectId, req.Body.Name, string(req.Body.Scope), a.s.Now())
	if err != nil {
		return nil, err
	}
	out, err := as[api.ApiKeyMade](keyOf(made))
	if err != nil {
		return nil, err
	}
	out.Key = key
	return api.CreateKey201JSONResponse(out), nil
}

// RevokeKey ends a key, which stops working on its next request.
func (a apiServer) RevokeKey(ctx context.Context, req api.RevokeKeyRequestObject) (api.RevokeKeyResponseObject, error) {
	if err := a.s.Store.RevokeAPIKey(ctx, callerOf(ctx).Access, req.KeyId, a.s.Now()); err != nil {
		return nil, err
	}
	return api.RevokeKey204Response{}, nil
}
