// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bytes"
	"errors"
	"flag"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/MartijnMul/allstill/internal/guide"
)

// Commands the guide leaves out on purpose: the guide itself, and those for
// running the server, which docs/running.md covers. A new command goes in the
// guide or here.
var notInGuide = []string{
	"guide",
	"serve",
	"backup",
	"version",
	"admin rename-project",
	"admin move-checks",
	"admin test-email",
}

// twoWords are the commands whose second word is a command too.
var twoWords = []string{"admin", "import", "compare"}

// commandName is how a command is called, from the words after `allstill`.
func commandName(words []string) string {
	if len(words) > 1 && slices.Contains(twoWords, words[0]) {
		return words[0] + " " + words[1]
	}
	return words[0]
}

// guideCommands finds the calls of allstill in the guide's code: every line of
// a fenced block and every inline snippet, as the words after `allstill`.
func guideCommands(text string) [][]string {
	var snippets []string
	inFence := false
	for line := range strings.Lines(text) {
		switch {
		case strings.HasPrefix(line, "```"):
			inFence = !inFence
		case inFence:
			snippets = append(snippets, line)
		default:
			for _, m := range regexp.MustCompile("`([^`]+)`").FindAllStringSubmatch(line, -1) {
				snippets = append(snippets, m[1])
			}
		}
	}
	var calls [][]string
	for _, s := range snippets {
		s, _, _ = strings.Cut(s, " -- ") // what follows is the job's
		words := strings.Fields(s)
		at := len(words) - 1 // the last, after `docker exec allstill` or `sudo -u allstill`
		for at >= 0 && words[at] != "allstill" {
			at--
		}
		if at < 0 || at == len(words)-1 || strings.HasPrefix(words[at+1], "<") {
			continue
		}
		call := words[at+1:]
		if len(call) == 1 && slices.Contains(twoWords, call[0]) {
			continue // the group of commands, named in a sentence
		}
		calls = append(calls, call)
	}
	return calls
}

// Every command in the guide exists, with every flag it shows: a renamed or
// removed one fails here, against the program's own help.
func TestGuideCommandsExist(t *testing.T) {
	calls := guideCommands(guide.Manual())
	if len(calls) < 10 {
		t.Fatalf("found only %d commands in the guide: %q", len(calls), calls)
	}
	// As flag.PrintDefaults writes them, so only real flags count, not a
	// command's hand-written help.
	flagInHelp := regexp.MustCompile(`(?m)^  -([a-z-]+)(?: .*)?\n    \t`)
	for _, words := range calls {
		name := commandName(words)
		var out, errOut bytes.Buffer
		err := run(t.Context(), append(strings.Fields(name), "-h"), &out, &errOut)
		if !errors.Is(err, flag.ErrHelp) {
			t.Errorf("guide calls `allstill %s`, which has no help: %v", name, err)
			continue
		}
		var known []string
		for _, m := range flagInHelp.FindAllStringSubmatch(errOut.String(), -1) {
			known = append(known, m[1])
		}
		for _, w := range words {
			if !strings.HasPrefix(w, "-") {
				continue
			}
			f, _, _ := strings.Cut(strings.TrimLeft(w, "-"), "=")
			if !slices.Contains(known, f) {
				t.Errorf("guide calls `allstill %s` with -%s, which it does not have", name, f)
			}
		}
	}
}

// Every command is in the guide or left out on purpose, so a new one cannot be
// forgotten. An exception for a command that is gone fails too.
func TestGuideCoversEveryCommand(t *testing.T) {
	inGuide := map[string]bool{}
	for _, words := range guideCommands(guide.Manual()) {
		inGuide[commandName(words)] = true
	}
	inUsage := map[string]bool{}
	for line := range strings.Lines(usage) {
		if rest, ok := strings.CutPrefix(line, "  allstill "); ok {
			inUsage[commandName(strings.Fields(rest))] = true
		}
	}
	if len(inUsage) < 10 {
		t.Fatalf("found only %d commands in the usage: %v", len(inUsage), inUsage)
	}
	for name := range inUsage {
		if !inGuide[name] && !slices.Contains(notInGuide, name) {
			t.Errorf("`allstill %s` is not in internal/guide/guide.md: add it, or to notInGuide with a reason", name)
		}
	}
	for _, name := range notInGuide {
		if !inUsage[name] {
			t.Errorf("notInGuide names `allstill %s`, which is not a command", name)
		}
		if inGuide[name] {
			t.Errorf("notInGuide names `allstill %s`, which the guide shows", name)
		}
	}
}

// The skill stays a pointer: what goes out of date belongs in the guide, which
// ships with the program. Its limits are those of the Agent Skills format.
func TestSkillIsAPointer(t *testing.T) {
	front, body, ok := strings.Cut(strings.TrimPrefix(guide.Skill, "---\n"), "\n---\n")
	if !ok || !strings.HasPrefix(guide.Skill, "---\n") {
		t.Fatalf("no frontmatter in the skill:\n%s", guide.Skill)
	}
	if !strings.Contains(front, "name: allstill\n") {
		t.Errorf("the skill's name is not allstill:\n%s", front)
	}
	_, description, _ := strings.Cut(front, "description: ")
	description, _, _ = strings.Cut(description, "\n")
	if description == "" || len(description) > 1024 {
		t.Errorf("the skill's description has %d characters, not 1 to 1024", len(description))
	}
	if !strings.Contains(body, "`allstill guide`") {
		t.Errorf("the skill does not point to the guide:\n%s", body)
	}
	if len(body) > 800 {
		t.Errorf("the skill's body has %d bytes; what it says at length belongs in the guide", len(body))
	}
	if calls := guideCommands(body); len(calls) != 1 || calls[0][0] != "guide" {
		t.Errorf("the skill should call only `allstill guide`: %q", calls)
	}
}

// The guide says which version it describes, and the licence comment is gone.
func TestGuideCommand(t *testing.T) {
	var out, errOut bytes.Buffer
	if err := run(t.Context(), []string{"guide"}, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.String(), "This guide describes allstill ") || strings.Contains(out.String(), "SPDX") {
		t.Errorf("guide: %.120q", out.String())
	}
	out.Reset()
	if err := run(t.Context(), []string{"guide", "-skill"}, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if out.String() != guide.Skill {
		t.Errorf("guide -skill: %.120q", out.String())
	}
}
