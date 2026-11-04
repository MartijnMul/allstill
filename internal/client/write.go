// SPDX-License-Identifier: AGPL-3.0-only

package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/MartijnMul/allstill/internal/apiclient"
	"github.com/MartijnMul/allstill/internal/logtext"
)

// Clock says a moment as the log's lines do.
func Clock(now, at time.Time, loc *time.Location) string { return logtext.Clock(now, at, loc) }

// Changing things. A write is never repeated by itself: a request that failed may have
// been done all the same, and only the person can tell.

// OneProject finds a project by name or id. With "", it is the one project the caller
// has, and otherwise it says which there are.
func (c *Client) OneProject(ctx context.Context, nameOrID string) (apiclient.Project, error) {
	if nameOrID != "" {
		return c.FindProject(ctx, nameOrID)
	}
	projects, err := c.Projects(ctx)
	if err != nil {
		return apiclient.Project{}, err
	}
	switch len(projects.Items) {
	case 0:
		return apiclient.Project{}, errors.New("you are in no project yet")
	case 1:
		return projects.Items[0], nil
	}
	names := make([]string, len(projects.Items))
	for i, p := range projects.Items {
		names[i] = Clean(p.Name)
	}
	return apiclient.Project{}, fmt.Errorf("give -project: you have %s", strings.Join(names, ", "))
}

// AddCheck adds a check to a project, with what the server said.
func (c *Client) AddCheck(ctx context.Context, projectID string, in apiclient.CheckInput) (*apiclient.CheckCreated, []byte, error) {
	r, err := c.api.CreateCheckWithResponse(ctx, projectID, in)
	if err != nil {
		return nil, nil, c.unreachable(err)
	}
	if r.JSON201 == nil {
		return nil, nil, c.refusal(ctx, r.HTTPResponse, r.Body, true)
	}
	return r.JSON201, r.Body, nil
}

// CheckToChange reads a check with its version, so a change cannot overwrite someone
// else's without saying so.
func (c *Client) CheckToChange(ctx context.Context, id string) (*apiclient.Check, string, error) {
	r, err := c.api.GetCheckWithResponse(ctx, id)
	if err != nil {
		return nil, "", c.unreachable(err)
	}
	if r.JSON200 == nil {
		return nil, "", c.refusal(ctx, r.HTTPResponse, r.Body, true)
	}
	return r.JSON200, r.HTTPResponse.Header.Get("ETag"), nil
}

// ChangeCheck changes a check as it was read, with what the server said.
func (c *Client) ChangeCheck(ctx context.Context, id, etag string, patch apiclient.CheckPatch) (*apiclient.CheckTried, []byte, error) {
	params := &apiclient.UpdateCheckParams{}
	if etag != "" {
		params.IfMatch = &etag
	}
	r, err := c.api.UpdateCheckWithResponse(ctx, id, params, patch)
	if err != nil {
		return nil, nil, c.unreachable(err)
	}
	if r.JSON200 == nil {
		return nil, nil, c.refusal(ctx, r.HTTPResponse, r.Body, true)
	}
	return r.JSON200, r.Body, nil
}

// PauseCheck pauses a check, with what the server said.
func (c *Client) PauseCheck(ctx context.Context, id string) ([]byte, error) {
	r, err := c.api.PauseCheckWithResponse(ctx, id)
	if err != nil {
		return nil, c.unreachable(err)
	}
	if r.JSON200 == nil {
		return nil, c.refusal(ctx, r.HTTPResponse, r.Body, true)
	}
	return r.Body, nil
}

// ResumeCheck resumes a check, with what the server said.
func (c *Client) ResumeCheck(ctx context.Context, id string) ([]byte, error) {
	r, err := c.api.ResumeCheckWithResponse(ctx, id)
	if err != nil {
		return nil, c.unreachable(err)
	}
	if r.JSON200 == nil {
		return nil, c.refusal(ctx, r.HTTPResponse, r.Body, true)
	}
	return r.Body, nil
}

// MoveCheck moves a check to another project, with what the server said.
func (c *Client) MoveCheck(ctx context.Context, id, projectID string) ([]byte, error) {
	r, err := c.api.MoveCheckWithResponse(ctx, id, apiclient.MoveCheckJSONRequestBody{ProjectId: projectID})
	if err != nil {
		return nil, c.unreachable(err)
	}
	if r.JSON200 == nil {
		return nil, c.refusal(ctx, r.HTTPResponse, r.Body, true)
	}
	return r.Body, nil
}

// DeleteCheck deletes a check with its runs.
func (c *Client) DeleteCheck(ctx context.Context, id string) error {
	r, err := c.api.DeleteCheckWithResponse(ctx, id)
	if err != nil {
		return c.unreachable(err)
	}
	if r.StatusCode() != http.StatusNoContent {
		return c.refusal(ctx, r.HTTPResponse, r.Body, true)
	}
	return nil
}

// PingURL says a check's ping URL, with what the server said.
func (c *Client) PingURL(ctx context.Context, id string) (string, []byte, error) {
	r, err := c.api.GetPingUrlWithResponse(ctx, id)
	if err != nil {
		return "", nil, c.unreachable(err)
	}
	if r.JSON200 == nil {
		return "", nil, c.refusal(ctx, r.HTTPResponse, r.Body, true)
	}
	return r.JSON200.PingUrl, r.Body, nil
}

// ReplacePingURL gives a check a new ping URL, with what the server said.
func (c *Client) ReplacePingURL(ctx context.Context, id string) (string, []byte, error) {
	r, err := c.api.RotatePingUrlWithResponse(ctx, id)
	if err != nil {
		return "", nil, c.unreachable(err)
	}
	if r.JSON200 == nil {
		return "", nil, c.refusal(ctx, r.HTTPResponse, r.Body, true)
	}
	return r.JSON200.PingUrl, r.Body, nil
}

// Keys lists a project's keys, or with "" those of every project the caller owns, with
// what the server said.
func (c *Client) Keys(ctx context.Context, projectID string) (*apiclient.ApiKeyList, []byte, error) {
	params := &apiclient.ListKeysParams{}
	if projectID != "" {
		params.ProjectId = &projectID
	}
	r, err := c.api.ListKeysWithResponse(ctx, params)
	if err != nil {
		return nil, nil, c.unreachable(err)
	}
	if r.JSON200 == nil {
		return nil, nil, c.refusal(ctx, r.HTTPResponse, r.Body, true)
	}
	return r.JSON200, r.Body, nil
}

// MakeKey makes a key, with what the server said, which holds its value, once.
func (c *Client) MakeKey(ctx context.Context, projectID, name, scope string) (*apiclient.ApiKeyMade, []byte, error) {
	r, err := c.api.CreateKeyWithResponse(ctx, projectID, apiclient.CreateKeyJSONRequestBody{Name: name, Scope: apiclient.Scope(scope)})
	if err != nil {
		return nil, nil, c.unreachable(err)
	}
	if r.JSON201 == nil {
		return nil, nil, c.refusal(ctx, r.HTTPResponse, r.Body, true)
	}
	return r.JSON201, r.Body, nil
}

// RevokeKey ends a key.
func (c *Client) RevokeKey(ctx context.Context, id string) error {
	r, err := c.api.RevokeKeyWithResponse(ctx, id)
	if err != nil {
		return c.unreachable(err)
	}
	if r.StatusCode() != http.StatusNoContent {
		return c.refusal(ctx, r.HTTPResponse, r.Body, true)
	}
	return nil
}

// PrintTry says what trying an address found, as the form does.
func PrintTry(w io.Writer, try *apiclient.CheckTry, now time.Time, loc *time.Location) {
	first := "it is not tried for real until you resume it"
	if try.FirstAttemptAt != nil {
		first = "its first real attempt comes by " + logtext.Clock(now, *try.FirstAttemptAt, loc)
	}
	if try.Result == apiclient.CheckTryResultNotTried {
		switch {
		case try.WhyNot != nil && *try.WhyNot == apiclient.CheckTryWhyNotProbe:
			fmt.Fprintf(w, "Only its probe can try this address, and %s.\n", first)
		case try.WhyNot != nil && *try.WhyNot == apiclient.CheckTryWhyNotLimit:
			fmt.Fprintf(w, "Not tried now: too many tries this minute. But %s.\n", first)
		default:
			fmt.Fprintf(w, "Not tried now, and %s.\n", first)
		}
		return
	}
	found, known := map[apiclient.CheckTryResult]string{
		apiclient.CheckTryResultUp: "up", apiclient.CheckTryResultWarning: "up, with a warning", apiclient.CheckTryResultFails: "it fails",
	}[try.Result]
	if !known {
		found = Clean(string(try.Result)) // from a newer server, shown as it is
	}
	line := "Tried it: " + found
	if try.Reason != nil {
		line += ", " + Clean(*try.Reason)
	}
	switch {
	case try.HttpStatus != nil && try.Ms != nil:
		line += fmt.Sprintf(" (HTTP %d in %d ms)", *try.HttpStatus, *try.Ms)
	case try.Ms != nil && *try.Ms >= 1:
		line += fmt.Sprintf(" (in %d ms)", *try.Ms)
	}
	fmt.Fprintln(w, line+".")
	if try.CertDays != nil {
		fmt.Fprintf(w, "Its certificate has %.0f days left.\n", *try.CertDays)
	}
	fmt.Fprintf(w, "A try does not count, and %s.\n", first)
	if try.AlertsNobodyYet != nil && *try.AlertsNobodyYet {
		fmt.Fprintln(w, "It alerts nobody until it has worked once.")
	}
}
