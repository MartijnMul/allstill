// SPDX-License-Identifier: AGPL-3.0-only

// Package guide holds the manual `allstill guide` prints, and the skill that
// points AI assistants to it. They are built into the program, so the manual
// always matches the version it describes. cmd/allstill tests them against
// the real commands.
package guide

import (
	_ "embed"
	"strings"
)

//go:embed guide.md
var manual string

// Skill is the file an AI assistant installs. It only says when to use
// Allstill and where to read on, so it does not go out of date with the program.
//
//go:embed SKILL.md
var Skill string

// Manual is the manual, without its licence comment.
func Manual() string {
	_, text, _ := strings.Cut(manual, "\n")
	return text
}
