#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Feeds every hook allowed, blocked and edge cases. `make check` runs it.

A shape a review finds that the guard misjudges becomes a case here."""

import json
import os
import subprocess
import sys
import tempfile

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(os.path.dirname(HERE))
HOOKS = os.path.join(HERE, "hooks.py")
fd, LOG = tempfile.mkstemp()
os.close(fd)
env = dict(os.environ, CLAUDE_PROJECT_DIR=ROOT, HOOK_LOG=LOG)
failed = 0


def run(hook, want, label, tool_input, cwd=None):
    global failed
    data = json.dumps({"tool_name": "x", "session_id": "testsess", "tool_input": tool_input})
    p = subprocess.run([sys.executable, "-I", HOOKS, hook], input=data, capture_output=True, text=True,
                       env=env, cwd=cwd)
    got = {0: "allow", 2: "block"}.get(p.returncode, f"exit {p.returncode}")
    if got == "allow" and '"permissionDecision": "ask"' in p.stdout:
        got = "ask"
    if got == want:
        print(f"ok    {hook:14} {label}")
    else:
        print(f"WRONG {hook:14} {label} -> {got} (wanted {want}): {(p.stderr or p.stdout)[:200]}")
        failed = 1


def cmd(want, label, command):
    run("guard-git", want, label, {"command": command})


def edit(want, label, path):
    run("protect-paths", want, label, {"file_path": path})


# Ordinary work is allowed.
cmd("allow", "plain commit", 'git commit -m "docs: x"')
cmd("allow", "commit from a message file", "git commit -q -F msg.txt")
cmd("allow", "commit -am", 'git commit -am "fix: y"')
cmd("allow", "commit -uno", 'git commit -uno -m "x"')
cmd("allow", "commit -S with a key", 'git commit -SABCDn -m "x"')
cmd("allow", "message mentions -n", 'git commit -m "fix the -n flag"')
cmd("allow", "heredoc body mentions flags", "git commit -q -F - <<'EOF'\nfeat: x\n\nuse -n, --force and --no-verify here\nEOF")
cmd("allow", "commit then log -n", "git commit -m 'x' && git log --oneline -n 3")
cmd("allow", "log --grep commit -n", "git log --grep commit -n 5")
cmd("allow", "a comment between commands", "git status\n# it's all fine\ngit log -n 3")
cmd("allow", "a here-string", 'cat <<< "EOF"\ngit status')
cmd("allow", "plain push", "git push -q origin main")
cmd("allow", "push with upstream", "git push -u origin main")
cmd("allow", "merge -n is --no-stat", "git merge -n feature")
cmd("allow", "a script file", "bash scripts/build.sh")
cmd("allow", "reading a PR", "gh pr view 1")
cmd("allow", "reading a PR in another repo", "gh -R o/r pr view 1")
cmd("allow", "reading through gh api", "gh api repos/o/r/commits/main --jq .sha")
cmd("allow", "make check", "make check")
cmd("allow", "make hooks", "make hooks")
cmd("allow", "a quoted command substitution", 'echo "go: $(go version)"')
cmd("allow", "a substitution in an assignment", 'v=$(go list -m -json "$m" | grep Time)')
cmd("allow", "git log inside a substitution", 'echo "head: $(git log --oneline -n 1)"')
cmd("allow", "backticks around a safe command", "echo `date`")

# Skipping the git hooks is blocked when the guard can read the command.
cmd("block", "commit --no-verify", "git commit --no-verify -m x")
cmd("block", "an abbreviated --no-verify", "git commit --no-verif -m x")
cmd("block", "commit -n", "git commit -nm x")
cmd("block", "second line", "git add -A\ngit commit --no-verify -m x")
cmd("block", "continued line", "git commit -m x \\\n  --no-verify")
cmd("block", "quoted flag", "git commit '--no-verify' -m x")
cmd("block", "inside bash -c", "bash -c 'git commit --no-verify -m x'")
cmd("block", "inside bash -lc", "bash -lc 'git commit -n -m x'")
cmd("block", "inside eval", "eval 'git commit -n -m x'")
cmd("block", "a heredoc fed to bash", "bash <<'EOF'\ngit commit -n -m x\nEOF")
cmd("block", "apostrophes in messages", "git commit -m \"it's\" --no-verify -m \"it's\"")
cmd("block", "a comment with an apostrophe", "# commit what's staged\ngit commit --no-verify -m x")
cmd("block", "after a comment line", "git status\n# it's fine\ngit commit -n -m x")
cmd("block", "in a subshell", "( git commit -n -m x )")
cmd("block", "in braces", "{ git commit -n -m x; }")
cmd("block", "in an if", "if true; then git commit -n -m x; fi")
cmd("block", "as an if condition", "if git commit -n -m x; then :; fi")
cmd("block", "as a while condition", "while git commit -n -m x; do break; done")
cmd("block", "as an until condition", "until git commit -n -m x; do :; done")
cmd("block", "in a for loop", "for f in a; do git commit -n -m x; done")
cmd("block", "after !", "! git commit -n -m x")
cmd("block", "in a command substitution", "echo $(git commit -n -m x)")
cmd("block", "in backticks", "echo `git commit -n -m x`")
cmd("block", "in a quoted command substitution", 'echo "$(git commit -n -m x)"')
cmd("block", "in a nested substitution", 'echo "$(echo "$(git commit -n -m x)")"')
cmd("block", "after a ) in a comment", 'echo "$(echo x # )\ngit commit -n -m x\n)"')
cmd("block", "after a case pattern", 'echo "$(case x in x) git commit -n -m x;; esac)"')
cmd("block", "after a # inside a word", "echo a#; git commit -n -m x # it's")
cmd("block", "in an unquoted heredoc", "cat <<EOF\n$(git commit -n -m x)\nEOF")
cmd("allow", "a quoted heredoc keeps $( literal", "cat <<'EOF'\n$(git commit -n -m x)\nEOF")
cmd("block", "after timeout", "timeout 60 git commit -n -m x")
cmd("block", "after xargs", "echo x | xargs git commit -n -m")
cmd("block", "after env -i", "env -i git commit -n -m x")
cmd("block", "after env and sudo", "FOO=1 sudo git commit --no-verify -m x")
cmd("block", "merge --no-verify", "git merge --no-verify feature")
cmd("block", "hooks path off, lower case", "git -c core.hookspath=/dev/null commit -m x")
cmd("block", "hooks path off", "git -c core.hooksPath=/dev/null commit -m x")
cmd("block", "hooks path elsewhere", "git config core.hooksPath .githooks-off")
cmd("block", "hooks path unset", "git config --unset core.hooksPath")

# What the guard cannot read, or reads only as words, asks the maintainer.
cmd("ask", "a substitution that does not close", 'echo "$(git status"')
cmd("ask", "a backtick that does not close", "echo `git status")
cmd("ask", "an apostrophe after a substitution", 'echo "$(echo a#; git commit -n -m x)" "it\'s"')
cmd("ask", "a force push in a substitution", 'echo "$(git push --force origin main)"')
cmd("ask", "a PR hidden after a case pattern", 'echo "$(case x in x) gh pr create -t t -b b;; esac)"')
cmd("ask", "unbalanced quotes", "git commit -m 'x")
cmd("ask", "commands piped into a shell", "echo 'git commit -n -m x' | bash")
cmd("ask", "--no-verify as a search term", "git grep -n -- '--no-verify'")
cmd("ask", "reading the hooks path", "git config --get core.hooksPath")
cmd("ask", "setting the hooks path by hand", "git config core.hooksPath .githooks")

# Outward-facing actions ask first.
cmd("ask", "force push", "git push --force origin main")
cmd("ask", "force push -f", "git push -f origin main")
cmd("ask", "force with lease", "git push --force-with-lease origin main")
cmd("ask", "plus refspec", "git push origin +main")
cmd("ask", "delete a branch by refspec", "git push origin :main")
cmd("ask", "push --delete", "git push --delete origin main")
cmd("ask", "push --mirror", "git push --mirror")
cmd("ask", "visibility change", "gh repo edit --visibility public")
cmd("ask", "opening a PR", "gh pr create --fill")
cmd("ask", "opening a PR in another repo", "gh -R o/r pr create --fill")
cmd("ask", "repo flag after the group", "gh pr -R o/r merge 1")
cmd("ask", "running a workflow", "gh workflow run ci.yml")
cmd("ask", "merging a PR", "gh --repo o/r pr merge 1")
cmd("ask", "a release", "gh release create v0.1.0")
cmd("ask", "a gist", "gh gist create notes.md")
cmd("ask", "a secret", "gh secret set NAME")
cmd("ask", "gh api PATCH", "gh api -X PATCH repos/o/r -f visibility=public")
cmd("ask", "gh api fields", "gh api repos/o/r/issues -f title=x")

# A broken environment never turns a block into an allow: here the working
# directory is deleted under the hook, and there is no CLAUDE_PROJECT_DIR.
gone = tempfile.mkdtemp()
data = json.dumps({"tool_name": "x", "session_id": "testsess", "tool_input": {"command": "git commit -n -m x"}})
bare = {k: v for k, v in os.environ.items() if k not in ("CLAUDE_PROJECT_DIR", "HOOK_LOG")}
p = subprocess.run(["sh", "-c", 'cd "$1" && rmdir "$1" && exec "$2" -I "$3" guard-git', "sh", gone, sys.executable, HOOKS],
                   input=data, capture_output=True, text=True, env=bare)
if p.returncode == 2:
    print(f"ok    {'guard-git':14} from a deleted directory")
else:
    print(f"WRONG {'guard-git':14} from a deleted directory -> exit {p.returncode}: {p.stderr[:200]}")
    failed = 1

# Licence texts are never edited, wherever they are and however the path is spelled.
edit("block", "AGPL licence", os.path.join(ROOT, "LICENSE"))
edit("block", "Apache licence", os.path.join(ROOT, "api", "LICENSE"))
edit("block", "a client's licence", os.path.join(ROOT, "clients", "go", "LICENSE"))
edit("block", "with ./ in the path", os.path.join(ROOT, ".", "LICENSE"))
edit("block", "in other letter case", os.path.join(ROOT.lower(), "LICENSE"))
edit("block", "British spelling", os.path.join(ROOT, "LICENCE"))
edit("block", "COPYING", os.path.join(ROOT, "COPYING"))
edit("block", "a LICENSES directory", os.path.join(ROOT, "LICENSES", "MIT.txt"))

# Files inside .git are changed through git, and committed migrations not at all.
edit("block", ".git/config", os.path.join(ROOT, ".git", "config"))
edit("allow", "a new migration", os.path.join(ROOT, "internal", "store", "migrations", "9999_test.sql"))

# The guards themselves ask before they change.
edit("ask", "a git hook", os.path.join(ROOT, ".githooks", "pre-commit"))
edit("ask", "a Claude hook", os.path.join(ROOT, ".claude", "hooks", "hooks.py"))
edit("ask", "the hook settings", os.path.join(ROOT, ".claude", "settings.json"))
edit("ask", "the CI workflow", os.path.join(ROOT, ".github", "workflows", "ci.yml"))
edit("allow", "an ordinary file", os.path.join(ROOT, "README.md"))

run("format", "allow", "a file that does not exist", {"file_path": os.path.join(ROOT, "nothing-here.go")})

os.remove(LOG)
sys.exit(failed)
