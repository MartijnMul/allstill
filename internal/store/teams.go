// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/MartijnMul/allstill/internal/store/internal/db"
)

// ErrSeveralTeams means an installation has more than one team, so a command
// that did not say which cannot know.
var ErrSeveralTeams = errors.New("several teams")

// onlyTeam returns the installation's team. A team owns projects and probes,
// and its members have one role in all of them (intent/teams/spec.md). Until a
// second one can be made, everything new belongs to this one.
func onlyTeam(ctx context.Context, q *db.Queries, workspaceID string) (string, error) {
	teams, err := q.Teams(ctx, workspaceID)
	switch {
	case err != nil:
		return "", err
	case len(teams) == 0:
		return "", fmt.Errorf("the installation has no team: %w", ErrNotFound)
	case len(teams) > 1:
		return "", fmt.Errorf("the installation has %d teams, so say which with -team: %w", len(teams), ErrSeveralTeams)
	}
	return teams[0].ID, nil
}
