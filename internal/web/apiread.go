// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/MartijnMul/allstill/internal/active"
	"github.com/MartijnMul/allstill/internal/api"
	"github.com/MartijnMul/allstill/internal/check"
	"github.com/MartijnMul/allstill/internal/store"
)

// What a credential can read. What it may read is decided where its access was made
// and by the store, so nothing here has to remember the scope: with read, a project
// holds a viewer's role. What secrets it reads goes through checkOf.

const (
	defaultPage = 50
	maxPage     = 200
)

func callerOf(ctx context.Context) caller { return ctx.Value(callerContextKey{}).(caller) }

// pageSize keeps what a client asks for to what a page can hold.
func pageSize(limit *api.Limit) int {
	switch {
	case limit == nil || *limit <= 0:
		return defaultPage
	case *limit > maxPage:
		return maxPage
	}
	return *limit
}

// A cursor is opaque to a client, so what it holds can change.
func encodeCursor(s string) *string {
	c := base64.RawURLEncoding.EncodeToString([]byte(s))
	return &c
}

func decodeCursor(c *api.Cursor) (string, bool) {
	if c == nil || *c == "" {
		return "", true
	}
	b, err := base64.RawURLEncoding.DecodeString(*c)
	return string(b), err == nil
}

// offsetOf reads a cursor that holds how far a list has been read.
func offsetOf(c *api.Cursor) (int, bool) {
	s, ok := decodeCursor(c)
	if !ok {
		return 0, false
	}
	if s == "" {
		return 0, true
	}
	n, err := strconv.Atoi(s)
	return n, err == nil && n >= 0
}

var badCursor = problem(http.StatusBadRequest, "That cursor cannot be read.", "Use the next_cursor of the page before.")

func optional[T comparable](v T) *T {
	var zero T
	if v == zero {
		return nil
	}
	return &v
}

func optionalTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	t = t.UTC()
	return &t
}

// stateRank orders states from what most needs someone to what least does.
var stateRank = map[check.State]int{
	check.Down: 6, check.Late: 5, check.Warning: 4, check.Unknown: 3, check.New: 2, check.Up: 1, check.Paused: 0,
}

// projectOf says how a project stands, from the states of its checks.
func projectOf(p store.ProjectAccess, checks []store.CheckView) api.Project {
	out := api.Project{Id: p.ID, Name: p.Name, Role: api.Role(p.Role), Counts: api.StateCounts{}}
	worst := check.State("")
	for _, c := range checks {
		if c.ProjectID != p.ID {
			continue
		}
		out.Counts[string(c.Status.State)]++
		if worst == "" || stateRank[c.Status.State] > stateRank[worst] {
			worst = c.Status.State
		}
	}
	if worst != "" {
		state := api.CheckState(worst)
		out.WorstState = &state
	}
	return out
}

// GetStatus says how everything the caller can see stands.
func (a apiServer) GetStatus(ctx context.Context, _ api.GetStatusRequestObject) (api.GetStatusResponseObject, error) {
	access := callerOf(ctx).Access
	checks, err := a.s.Store.CheckStates(ctx, access)
	if err != nil {
		return nil, err
	}
	out := api.GetStatus200JSONResponse{
		GeneratedAt: a.s.Now().UTC(), Overall: api.StatusOverallAllStill, Counts: api.StateCounts{},
		Projects: []api.Project{}, Attention: []api.AttentionCheck{},
	}
	for _, p := range access.Projects {
		out.Projects = append(out.Projects, projectOf(p, checks))
	}
	worst := check.State("")
	for _, c := range checks {
		st := c.Status.State
		out.Counts[string(st)]++
		if worst == "" || stateRank[st] > stateRank[worst] {
			worst = st
		}
		if st.NeedsAttention() {
			out.Attention = append(out.Attention, api.AttentionCheck{
				Id: c.ID, Name: c.Name, ProjectId: c.ProjectID, ProjectName: c.ProjectName,
				State: api.CheckState(st), StateSince: c.Status.Since, StateReason: optional(c.Status.Reason),
			})
		}
	}
	switch worst {
	case check.Down:
		out.Overall = api.StatusOverallIncident
	case check.Late, check.Warning:
		out.Overall = api.StatusOverallAttention
	case check.Unknown:
		out.Overall = api.StatusOverallUnknown
	}
	return out, nil
}

// ListProjects lists the projects the caller can see.
func (a apiServer) ListProjects(ctx context.Context, req api.ListProjectsRequestObject) (api.ListProjectsResponseObject, error) {
	access := callerOf(ctx).Access
	offset, ok := offsetOf(req.Params.Cursor)
	if !ok {
		return api.ListProjects400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: api.BadRequestApplicationProblemPlusJSONResponse(badCursor)}, nil
	}
	checks, err := a.s.Store.CheckStates(ctx, access)
	if err != nil {
		return nil, err
	}
	size := pageSize(req.Params.Limit)
	out := api.ListProjects200JSONResponse{Items: []api.Project{}}
	projects := access.Projects[min(offset, len(access.Projects)):]
	if len(projects) > size {
		projects = projects[:size]
		out.NextCursor = encodeCursor(strconv.Itoa(offset + size))
	}
	for _, p := range projects {
		out.Items = append(out.Items, projectOf(p, checks))
	}
	return out, nil
}

// GetProject returns one project, as one that does not exist if the caller cannot see it.
func (a apiServer) GetProject(ctx context.Context, req api.GetProjectRequestObject) (api.GetProjectResponseObject, error) {
	access := callerOf(ctx).Access
	for _, p := range access.Projects {
		if p.ID != req.ProjectId {
			continue
		}
		checks, err := a.s.Store.CheckStates(ctx, access)
		if err != nil {
			return nil, err
		}
		return api.GetProject200JSONResponse(projectOf(p, checks)), nil
	}
	return api.GetProject404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse: notFound()}, nil
}

func notFound() api.NotFoundApplicationProblemPlusJSONResponse {
	return api.NotFoundApplicationProblemPlusJSONResponse(problem(http.StatusNotFound, "There is nothing here, or you may not see it.", ""))
}

// checkOf is a check as this caller may read it. An HTTP check's URL and where it must
// redirect are whole only for someone who may see them, and a ping URL is never here,
// as only three operations answer with one (spec, R21).
func (a apiServer) checkOf(ctx context.Context, access store.Access, c store.CheckView) (api.Check, error) {
	out := api.Check{
		Id: c.ID, ProjectId: c.ProjectID, Name: c.Name, Kind: api.CheckKind(c.Kind),
		Slug: optional(c.Slug), Description: optional(c.Description),
		State: api.CheckState(c.Status.State), StateSince: c.Status.Since, StateReason: optional(c.Status.Reason),
		LastPingAt: optionalTime(c.Status.LastPing), NextExpectedAt: optionalTime(c.Status.NextExpected),
		CreatedAt: c.Created,
	}
	if len(c.Tags) > 0 {
		out.Tags = &c.Tags
	}
	if grace := int64(c.Grace / time.Second); grace > 0 {
		out.Grace = &grace
	}
	var schedule api.Schedule
	var err error
	if c.Cron != "" {
		err = schedule.FromCronSchedule(api.CronSchedule{Kind: "cron", Expression: c.Cron, TimeZone: cmp(c.TimeZone, "UTC")})
	} else {
		err = schedule.FromIntervalSchedule(api.IntervalSchedule{Kind: "interval", Every: int64(c.Interval / time.Second)})
	}
	if err != nil {
		return out, err
	}
	out.Schedule = &schedule
	whole := access.SeesSecrets(c.ProjectID)
	// How fast it tells people is for those who may change it, as on the dashboard: a
	// customer does not learn that a check tells nobody.
	if whole {
		urgency, critical := api.AlertingUrgency(c.Urgency), c.Critical
		out.Alerting = &api.Alerting{Urgency: &urgency, Critical: &critical}
	}
	switch c.Kind {
	case "http":
		var h api.HttpCheck
		if err := json.Unmarshal(c.Settings, &h); err != nil {
			return out, err
		}
		if !whole {
			h.Url = active.Shown(h.Url)
			if h.Location != nil {
				shown := active.Shown(*h.Location)
				h.Location = &shown
			}
			h.Ca = nil
		}
		h.ProbeId = optional(c.ProbeID)
		out.Http = &h
	case "tcp":
		var t api.TcpCheck
		if err := json.Unmarshal(c.Settings, &t); err != nil {
			return out, err
		}
		if !whole {
			t.Ca = nil
		}
		t.ProbeId = optional(c.ProbeID)
		out.Tcp = &t
	}
	if c.ProbeID != "" {
		name, err := a.s.Store.ProbeName(ctx, access.WorkspaceID, c.ProbeID)
		if err != nil {
			return out, err
		}
		out.ProbeName = &name
	}
	if len(c.Recent) > 0 {
		last := runOf(c.ID, c.Recent[0])
		out.LastRun = &last
	}
	return out, nil
}

func runOf(checkID string, r store.RunView) api.Run {
	out := api.Run{
		Id: r.ID, CheckId: checkID, Outcome: api.RunOutcome(r.Outcome),
		StartedAt: optionalTime(r.Started), EndedAt: optionalTime(r.Ended), Reason: optional(r.Reason),
	}
	if out.EndedAt == nil {
		out.EndedAt = optionalTime(r.TimedOut)
	}
	if d := r.Duration(); d > 0 {
		seconds := int64(d / time.Second)
		out.RunTime = &seconds
	}
	if r.ExitStatus >= 0 {
		out.ExitStatus = &r.ExitStatus
	}
	if len(r.Values) > 0 {
		out.Values = &r.Values
	}
	return out
}

// ListChecks lists a project's checks.
func (a apiServer) ListChecks(ctx context.Context, req api.ListChecksRequestObject) (api.ListChecksResponseObject, error) {
	access := callerOf(ctx).Access
	offset, ok := offsetOf(req.Params.Cursor)
	if !ok {
		return api.ListChecks400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: api.BadRequestApplicationProblemPlusJSONResponse(badCursor)}, nil
	}
	size := pageSize(req.Params.Limit)
	checks, more, err := a.s.Store.ChecksOfProject(ctx, access, req.ProjectId, offset, size)
	if errors.Is(err, store.ErrNotFound) {
		return api.ListChecks404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse: notFound()}, nil
	}
	if err != nil {
		return nil, err
	}
	out := api.ListChecks200JSONResponse{Items: make([]api.Check, 0, len(checks))}
	for _, c := range checks {
		item, err := a.checkOf(ctx, access, c)
		if err != nil {
			return nil, err
		}
		out.Items = append(out.Items, item)
	}
	if more {
		out.NextCursor = encodeCursor(strconv.Itoa(offset + size))
	}
	return out, nil
}

// GetCheck returns one check, as one that does not exist if the caller cannot see it.
func (a apiServer) GetCheck(ctx context.Context, req api.GetCheckRequestObject) (api.GetCheckResponseObject, error) {
	access := callerOf(ctx).Access
	c, err := a.s.Store.Check(ctx, access, req.CheckId)
	if errors.Is(err, store.ErrNotFound) {
		return api.GetCheck404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse: notFound()}, nil
	}
	if err != nil {
		return nil, err
	}
	out, err := a.checkOf(ctx, access, c)
	if err != nil {
		return nil, err
	}
	return api.GetCheck200JSONResponse(out), nil
}

// ListRuns lists a check's runs, newest first.
func (a apiServer) ListRuns(ctx context.Context, req api.ListRunsRequestObject) (api.ListRunsResponseObject, error) {
	before, ok := decodeCursor(req.Params.Cursor)
	if !ok {
		return api.ListRuns400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: api.BadRequestApplicationProblemPlusJSONResponse(badCursor)}, nil
	}
	runs, more, err := a.s.Store.Runs(ctx, callerOf(ctx).Access, req.CheckId, before, pageSize(req.Params.Limit))
	if errors.Is(err, store.ErrNotFound) {
		return api.ListRuns404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse: notFound()}, nil
	}
	if err != nil {
		return nil, err
	}
	out := api.ListRuns200JSONResponse{Items: make([]api.Run, 0, len(runs))}
	for _, r := range runs {
		out.Items = append(out.Items, runOf(req.CheckId, r))
	}
	if more {
		out.NextCursor = encodeCursor(runs[len(runs)-1].ID)
	}
	return out, nil
}

func logPartyOf(p store.LogParty) api.LogParty {
	return api.LogParty{Kind: p.Kind, Id: optional(p.ID), Name: optional(p.Name)}
}

// ListLog reads the log under the same rules for each reader as the page, which the
// store holds.
func (a apiServer) ListLog(ctx context.Context, req api.ListLogRequestObject) (api.ListLogResponseObject, error) {
	bad := func(p api.Problem) (api.ListLogResponseObject, error) {
		return api.ListLog400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: api.BadRequestApplicationProblemPlusJSONResponse(p)}, nil
	}
	in := req.Params
	f := store.LogFilter{Limit: pageSize(in.Limit)}
	cursor, ok := decodeCursor(in.Cursor)
	if !ok {
		return bad(badCursor)
	}
	if cursor != "" {
		before, err := strconv.ParseInt(cursor, 10, 64)
		if err != nil {
			return bad(badCursor)
		}
		f.Before = before
	}
	if in.What != nil {
		if !in.What.Valid() {
			return bad(problem(http.StatusBadRequest, "That is not something the log can narrow to.", "Use happened, done, reached or signins."))
		}
		f.What = string(*in.What)
	}
	if in.ProjectId != nil {
		f.ProjectID = *in.ProjectId
	}
	if in.CheckId != nil {
		f.CheckID = *in.CheckId
	}
	if in.PersonId != nil {
		f.PersonID = *in.PersonId
	}
	if in.Name != nil {
		f.Name = strings.TrimSpace(*in.Name)
	}
	if in.Group != nil {
		f.Group = *in.Group
	}
	f.Late = in.Late != nil && *in.Late
	page, err := a.s.Store.ReadLog(ctx, callerOf(ctx).Access, f)
	if err != nil {
		return nil, err
	}
	out := api.ListLog200JSONResponse{
		Items: make([]api.LogEntry, 0, len(page.Entries)), KeptFor: int64(page.KeptFor / time.Second), StartedAt: optionalTime(page.Started),
	}
	if page.Older != 0 {
		out.NextCursor = encodeCursor(strconv.FormatInt(page.Older, 10))
	}
	for _, e := range page.Entries {
		item := api.LogEntry{
			Id: e.ID, At: e.At, RecordedAt: e.Recorded, Kind: e.Kind,
			ProjectId: optional(e.ProjectID), ProjectName: optional(e.ProjectName),
			AboutKind: optional(e.AboutKind), AboutId: optional(e.AboutID), AboutName: optional(e.AboutName),
			Group: optional(e.Group), By: logPartyOf(e.By),
		}
		if e.Person.Kind != "" {
			person := logPartyOf(e.Person)
			item.Person = &person
		}
		if len(e.Details) > 0 {
			item.Details = &e.Details
		}
		out.Items = append(out.Items, item)
	}
	return out, nil
}

// ListProbes lists the probes serving the projects the caller may edit.
func (a apiServer) ListProbes(ctx context.Context, _ api.ListProbesRequestObject) (api.ListProbesResponseObject, error) {
	probes, err := a.s.Store.ProbesFor(ctx, callerOf(ctx).Access)
	if err != nil {
		return nil, err
	}
	out := api.ListProbes200JSONResponse{Items: make([]api.Probe, 0, len(probes))}
	for _, p := range probes {
		out.Items = append(out.Items, api.Probe{Id: p.ID, Name: p.Name, ProjectId: p.ProjectID})
	}
	return out, nil
}
