// SPDX-License-Identifier: AGPL-3.0-only

package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/MartijnMul/allstill/internal/apiclient"
)

// Reading: each call returns what the server said as it said it too, for -json, which
// gives the API's own answer (R18).

// Page is a read that may have gone over several of the server's pages: its items,
// and each as it came.
type Page[T any] struct {
	Items []T
	Raw   []json.RawMessage
}

const (
	// pageLimit is what a list asks for at a time: as much as a page holds, as each
	// request counts against the limit on a credential.
	pageLimit = 200
	// listWaits is how many times one list waits for that limit. A command that reads
	// several lists, as finding a check by name among many projects does, can run into
	// it by itself, and reading can wait. A single request does not, so a loop in a
	// script still hears of it.
	listWaits = 5
	// longestWait is the most a server can make it wait at a time, in seconds.
	longestWait = 60
)

// allPages follows a list to its end.
func allPages[T any](ctx context.Context, c *Client, fetch func(cursor *string) (*http.Response, []byte, error)) (Page[T], error) {
	var out Page[T]
	var cursor *string
	waits := 0
	for {
		resp, body, err := fetch(cursor)
		if err != nil {
			return out, c.unreachable(err)
		}
		if resp.StatusCode == http.StatusTooManyRequests && waits < listWaits {
			waits++
			seconds, err := strconv.Atoi(resp.Header.Get("Retry-After"))
			if err != nil || seconds < 1 || seconds > longestWait {
				seconds = longestWait
			}
			select {
			case <-ctx.Done():
				return out, ctx.Err()
			case <-time.After(time.Duration(seconds) * time.Second):
			}
			continue
		}
		waits = 0
		if resp.StatusCode != http.StatusOK {
			return out, c.refusal(ctx, resp, body, true)
		}
		var page struct {
			Items      []json.RawMessage `json:"items"`
			NextCursor *string           `json:"next_cursor"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return out, fmt.Errorf("%s does not answer as Allstill does. Check the address", c.Server)
		}
		for _, raw := range page.Items {
			var item T
			if err := json.Unmarshal(raw, &item); err != nil {
				return out, fmt.Errorf("%s does not answer as Allstill does. Check the address", c.Server)
			}
			out.Items = append(out.Items, item)
			out.Raw = append(out.Raw, raw)
		}
		if page.NextCursor == nil || *page.NextCursor == "" {
			return out, nil
		}
		cursor = page.NextCursor
	}
}

// Status says how everything the caller can see stands.
func (c *Client) Status(ctx context.Context) (*apiclient.Status, []byte, error) {
	r, err := c.api.GetStatusWithResponse(ctx)
	if err != nil {
		return nil, nil, c.unreachable(err)
	}
	if r.JSON200 == nil {
		return nil, nil, c.refusal(ctx, r.HTTPResponse, r.Body, true)
	}
	return r.JSON200, r.Body, nil
}

// Projects lists the projects the caller can see.
func (c *Client) Projects(ctx context.Context) (Page[apiclient.Project], error) {
	limit := pageLimit
	return allPages[apiclient.Project](ctx, c, func(cursor *string) (*http.Response, []byte, error) {
		r, err := c.api.ListProjectsWithResponse(ctx, &apiclient.ListProjectsParams{Cursor: cursor, Limit: &limit})
		if err != nil {
			return nil, nil, err
		}
		return r.HTTPResponse, r.Body, nil
	})
}

// Checks lists a project's checks.
func (c *Client) Checks(ctx context.Context, projectID string) (Page[apiclient.Check], error) {
	limit := pageLimit
	return allPages[apiclient.Check](ctx, c, func(cursor *string) (*http.Response, []byte, error) {
		r, err := c.api.ListChecksWithResponse(ctx, projectID, &apiclient.ListChecksParams{Cursor: cursor, Limit: &limit})
		if err != nil {
			return nil, nil, err
		}
		return r.HTTPResponse, r.Body, nil
	})
}

// Check returns one check, with what the server said.
func (c *Client) Check(ctx context.Context, id string) (*apiclient.Check, []byte, error) {
	r, err := c.api.GetCheckWithResponse(ctx, id)
	if err != nil {
		return nil, nil, c.unreachable(err)
	}
	if r.JSON200 == nil {
		return nil, nil, c.refusal(ctx, r.HTTPResponse, r.Body, true)
	}
	return r.JSON200, r.Body, nil
}

// Runs returns a check's latest runs, with what the server said.
func (c *Client) Runs(ctx context.Context, checkID string, limit int) (*apiclient.RunPage, []byte, error) {
	r, err := c.api.ListRunsWithResponse(ctx, checkID, &apiclient.ListRunsParams{Limit: &limit})
	if err != nil {
		return nil, nil, c.unreachable(err)
	}
	if r.JSON200 == nil {
		return nil, nil, c.refusal(ctx, r.HTTPResponse, r.Body, true)
	}
	return r.JSON200, r.Body, nil
}

// Log reads a page of the log, with what the server said.
func (c *Client) Log(ctx context.Context, params *apiclient.ListLogParams) (*apiclient.LogPage, []byte, error) {
	r, err := c.api.ListLogWithResponse(ctx, params)
	if err != nil {
		return nil, nil, c.unreachable(err)
	}
	if r.JSON200 == nil {
		return nil, nil, c.refusal(ctx, r.HTTPResponse, r.Body, true)
	}
	return r.JSON200, r.Body, nil
}

// Probes lists the probes serving the projects the caller may edit.
func (c *Client) Probes(ctx context.Context) (*apiclient.ProbeList, []byte, error) {
	r, err := c.api.ListProbesWithResponse(ctx)
	if err != nil {
		return nil, nil, c.unreachable(err)
	}
	if r.JSON200 == nil {
		return nil, nil, c.refusal(ctx, r.HTTPResponse, r.Body, true)
	}
	return r.JSON200, r.Body, nil
}

// looksLikeID says a name given to a command is an id instead.
func looksLikeID(s, prefix string) bool { return strings.HasPrefix(s, prefix+"_") }

// ErrNoSuchThing is a name that is nothing the caller can see, which it does not tell
// from one they may not see.
var ErrNoSuchThing = errors.New("no such thing")

// noSuch says which name that was, in its own words.
type noSuch string

func (e noSuch) Error() string        { return string(e) }
func (e noSuch) Is(target error) bool { return target == ErrNoSuchThing }

// FindProject finds a project by name or id. Two with one name list both, and change
// nothing (R46).
func (c *Client) FindProject(ctx context.Context, nameOrID string) (apiclient.Project, error) {
	projects, err := c.Projects(ctx)
	if err != nil {
		return apiclient.Project{}, err
	}
	var found []apiclient.Project
	for _, p := range projects.Items {
		if p.Id == nameOrID || p.Name == nameOrID {
			found = append(found, p)
		}
	}
	switch len(found) {
	case 0:
		return apiclient.Project{}, noSuch(fmt.Sprintf("you have no project named %q", Clean(nameOrID)))
	case 1:
		return found[0], nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d projects are named %q. Use one of these ids:", len(found), Clean(nameOrID))
	for _, p := range found {
		fmt.Fprintf(&b, "\n  %s", Clean(p.Id))
		if p.Team != nil {
			fmt.Fprintf(&b, "  in %s", Clean(p.Team.Name))
		}
	}
	return apiclient.Project{}, errors.New(b.String())
}

// FindCheck finds a check by name or id, in one project or, with "", in all the caller
// can see, with what the server said of it. Two with one name list both with their
// projects and ids, and change nothing (R46).
func (c *Client) FindCheck(ctx context.Context, project, nameOrID string) (*apiclient.Check, []byte, error) {
	if looksLikeID(nameOrID, "chk") {
		check, raw, err := c.Check(ctx, nameOrID)
		if err != nil && strings.Contains(err.Error(), "nothing here") {
			return nil, nil, noSuch("you have no check " + Clean(nameOrID))
		}
		return check, raw, err
	}
	var projects []apiclient.Project
	if project != "" {
		p, err := c.FindProject(ctx, project)
		if err != nil {
			return nil, nil, err
		}
		projects = []apiclient.Project{p}
	} else {
		all, err := c.Projects(ctx)
		if err != nil {
			return nil, nil, err
		}
		projects = all.Items
	}
	type hit struct {
		check   apiclient.Check
		raw     json.RawMessage
		project string
	}
	var hits []hit
	for _, p := range projects {
		checks, err := c.Checks(ctx, p.Id)
		if err != nil {
			return nil, nil, err
		}
		for i, check := range checks.Items {
			if check.Name == nameOrID {
				hits = append(hits, hit{check, checks.Raw[i], p.Name})
			}
		}
	}
	switch len(hits) {
	case 0:
		return nil, nil, noSuch(fmt.Sprintf("you have no check named %q", Clean(nameOrID)))
	case 1:
		return &hits[0].check, hits[0].raw, nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d checks are named %q. Use -project, or one of these ids:", len(hits), Clean(nameOrID))
	for _, h := range hits {
		fmt.Fprintf(&b, "\n  %s  in %s", Clean(h.check.Id), Clean(h.project))
	}
	return nil, nil, errors.New(b.String())
}
