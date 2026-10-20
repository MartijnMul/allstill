#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Claude Code hooks for this repository.

    hooks.py guard-git      PreToolUse on Bash
    hooks.py protect-paths  PreToolUse on Edit and Write
    hooks.py format         PostToolUse on Edit and Write

Each reads the hook input as JSON on stdin and decides: allow (exit 0),
block (exit 2, the reason on stderr for Claude) or ask (a JSON decision on
stdout that pauses for the maintainer). Every decision is logged to
.claude/hooks.log.

These hooks are a convenience layer, not a security boundary: a shell
command can always be written in a shape no parser foresees. The boundary is
the git hooks in .githooks/ for what is committed, and CI, which checks the
whole history for secrets, private terms and commit identities. So the guard
stays coarse: what looks like skipping the git hooks asks the maintainer,
whatever the quoting, and a command it cannot read asks too.
"""

import json
import os
import re
import shlex
import subprocess
import sys
import time

ORDER = {"allow": 0, "ask": 1, "block": 2}
OPERATOR = re.compile(r"^[;&|()\n]+$")
GRAMMAR = {"if", "while", "until", "then", "do", "else", "elif", "!", "{", "}",
           "time", "nohup", "exec", "command", "sudo"}
SHELLS = {"bash", "sh", "zsh", "dash"}

# Substrings that only appear when skipping or moving the git hooks. git
# accepts any unambiguous prefix of a long option, and `--no-veri` is the
# shortest one for --no-verify (`--no-ver` could be --no-verbose).
RAW_ASK = (
    ("--no-veri", "this looks like skipping the git hooks (--no-verify)"),
    ("hookspath", "this touches core.hooksPath, which stays .githooks (make hooks)"),
)


def project_dir():
    d = os.environ.get("CLAUDE_PROJECT_DIR")
    if d:
        return d
    try:
        return os.getcwd()
    except OSError:
        return "."


def decide(hook, verdict, detail, message="", session=""):
    log = os.environ.get("HOOK_LOG") or os.path.join(project_dir(), ".claude", "hooks.log")
    line = " ".join(detail.split())[:200]
    try:
        with open(log, "a", encoding="utf-8") as f:
            f.write(f"{time.strftime('%Y-%m-%dT%H:%M:%S')}\t{hook}\t{verdict}\t{line}\t{session[:8]}\n")
    except OSError:
        pass
    if verdict == "block":
        print(message, file=sys.stderr)
        sys.exit(2)
    if verdict == "ask":
        print(json.dumps({"hookSpecificOutput": {
            "hookEventName": "PreToolUse",
            "permissionDecision": "ask",
            "permissionDecisionReason": message,
        }}))
    sys.exit(0)


# guard-git

HEREDOC = re.compile(r"(?<!<)<<-?\s*(['\"]?)([A-Za-z_][A-Za-z0-9_]*)\1")


def split_heredocs(text):
    """Return the command text without heredoc bodies, and the bodies fed to a shell."""
    out, shell_bodies, end, body, to_shell = [], [], None, [], False
    for line in text.replace("\\\n", " ").split("\n"):
        if end is not None:
            if line.strip() == end:
                if to_shell:
                    shell_bodies.append("\n".join(body))
                end, body = None, []
            else:
                body.append(line)
            continue
        out.append(line)
        m = HEREDOC.search(line)
        if m:
            end = m.group(2)
            words = line[:m.start()].split()
            to_shell = bool(words) and os.path.basename(words[-1]) in SHELLS
    return "\n".join(out), shell_bodies


def tokenize(text, comments):
    lex = shlex.shlex(text, posix=True, punctuation_chars=";&|()\n")
    lex.whitespace = " \t\r"
    lex.whitespace_split = True
    lex.commenters = "#" if comments else ""
    return list(lex)


def segments(text):
    """Split a command line into simple commands, the way the shell does.

    Read as a whole first, so quoted text may span lines; a comment's words
    then become harmless words of their own. When that fails (an apostrophe
    in a comment), read line by line with comments dropped. Returns None when
    a line still cannot be read."""
    try:
        tokens = tokenize(text, comments=False)
    except ValueError:
        tokens = []
        for line in text.split("\n"):
            try:
                tokens += tokenize(line, comments=True) + ["\n"]
            except ValueError:
                return None
    segs, seg = [], []
    for tok in tokens:
        if OPERATOR.match(tok):
            if seg:
                segs.append(seg)
            seg = []
        else:
            seg.append(tok)
    if seg:
        segs.append(seg)
    return segs


def judge_git(args):
    i = 0
    while i < len(args) and args[i].startswith("-"):
        if args[i] == "-c" and i + 1 < len(args):
            key, _, value = args[i + 1].partition("=")
            if key.lower() == "core.hookspath" and value != ".githooks":
                return "block", "the git hooks path stays .githooks (make hooks)"
        i += 2 if args[i] in ("-C", "-c", "--git-dir", "--work-tree", "--namespace") else 1
    if i >= len(args):
        return "allow", ""
    sub, rest = args[i], args[i + 1:]

    if sub != "grep" and sub != "log" and any(a.startswith("--no-veri") for a in rest):
        return "block", "git never skips its hooks (--no-verify)"

    if sub == "config":
        keys = [a for a in rest if a.lower() == "core.hookspath"]
        if keys:
            if {"--unset", "--unset-all", "unset", "--remove-section"} & set(rest):
                return "block", "the git hooks path stays .githooks (make hooks)"
            k = rest.index(keys[0])
            value = rest[k + 1] if k + 1 < len(rest) else None
            reads = {"--get", "--get-all", "--get-regexp", "--list", "-l", "get", "list"}
            if value is not None and not (reads & set(rest)) and value != ".githooks":
                return "block", "the git hooks path stays .githooks (make hooks)"
        if "--remove-section" in rest and any(a.lower() == "core" for a in rest):
            return "block", "the git hooks path stays .githooks (make hooks)"

    if sub == "commit":
        skip = False
        for a in rest:
            if skip:
                skip = False
                continue
            if a == "--":
                break
            if a.startswith("-") and not a.startswith("--"):
                for ch in a[1:]:
                    if ch == "n":
                        return "block", "git never skips its hooks (commit -n)"
                    if ch in "mFCct":       # takes a value, attached or as the next word
                        skip = a.endswith(ch)
                        break
                    if ch in "uS":          # an optional value, attached only
                        break
        return "allow", ""

    if sub == "push":
        for a in rest:
            short = a.startswith("-") and not a.startswith("--")
            if (a.startswith("--force") or a in ("--mirror", "--delete", "--prune")
                    or (short and ("f" in a or "d" in a))
                    or a.startswith("+") or (a.startswith(":") and len(a) > 1)):
                return "ask", "this push rewrites or deletes published history"
    return "allow", ""


GH_WRITES = {
    "repo": {"create", "edit", "delete", "rename", "archive", "unarchive", "fork"},
    "pr": {"create", "merge", "comment", "edit", "close", "reopen", "review", "ready"},
    "issue": {"create", "comment", "edit", "close", "reopen", "delete", "transfer"},
    "release": {"create", "edit", "delete", "upload"},
    "gist": {"create", "edit", "delete"},
    "secret": {"set", "delete"},
    "variable": {"set", "delete"},
    "workflow": {"run", "enable", "disable"},
    "label": {"create", "edit", "delete", "clone"},
}


def judge_gh(args):
    i = 0
    while i < len(args) and args[i].startswith("-"):
        i += 2 if args[i] in ("-R", "--repo", "--hostname") else 1
    args = args[i:]
    if not args:
        return "allow", ""
    group, rest = args[0], args[1:]
    j = 0
    while j < len(rest) and rest[j].startswith("-"):  # flags may sit between group and action
        j += 2 if rest[j] in ("-R", "--repo", "--hostname") else 1
    action = rest[j] if j < len(rest) else ""
    if action in GH_WRITES.get(group, set()):
        return "ask", f"gh {group} {action} is outward-facing"
    if group == "api":
        for k, a in enumerate(args):
            if a in ("-X", "--method") and k + 1 < len(args) and args[k + 1].upper() != "GET":
                return "ask", "gh api with a write method is outward-facing"
            if a.startswith(("-X", "--method=")) and a not in ("-X", "--method") and not a.upper().endswith("GET"):
                return "ask", "gh api with a write method is outward-facing"
            if a in ("-f", "-F", "--field", "--raw-field", "--input"):
                return "ask", "gh api with fields writes by default"
    return "allow", ""


def judge(tokens, depth):
    if any("`" in t or "$(" in t for t in tokens):
        return "ask", "a command substitution inside a word cannot be judged"
    while tokens:
        t = tokens[0]
        if t in GRAMMAR or re.match(r"^[A-Za-z_][A-Za-z0-9_]*=", t):
            tokens = tokens[1:]
        elif t in ("env", "xargs", "nice"):
            tokens = tokens[1:]
            while tokens and (tokens[0].startswith("-") or "=" in tokens[0]):
                tokens = tokens[1:]
        elif t == "timeout":
            tokens = tokens[1:]
            while tokens and tokens[0].startswith("-"):
                tokens = tokens[1:]
            tokens = tokens[1:]  # the duration
        else:
            break
    if not tokens:
        return "allow", ""
    prog = os.path.basename(tokens[0])
    if prog == "eval":
        return judge_command(" ".join(tokens[1:]), depth + 1)
    if prog in SHELLS:
        for k, a in enumerate(tokens[1:], 1):
            if a.startswith("-") and not a.startswith("--") and "c" in a:
                return judge_command(tokens[k + 1], depth + 1) if k + 1 < len(tokens) else ("allow", "")
        if not [a for a in tokens[1:] if not a.startswith("-")]:
            return "ask", "a shell reading its commands from input cannot be judged"
        return "allow", ""
    if prog == "git":
        return judge_git(tokens[1:])
    if prog == "gh":
        return judge_gh(tokens[1:])
    return "allow", ""


def worst(a, b):
    return b if ORDER[b[0]] > ORDER[a[0]] else a


def judge_command(text, depth=0):
    if depth > 4:
        return "ask", "this command nests shells too deeply to read"
    command, shell_bodies = split_heredocs(text)
    verdict = ("allow", "")
    low = (command + "\n" + "\n".join(shell_bodies)).lower()
    for needle, reason in RAW_ASK:
        if needle in low:
            verdict = worst(verdict, ("ask", reason))
    segs = segments(command)
    if segs is None:
        return worst(verdict, ("ask", "the guard cannot read this command (unbalanced quotes?)"))
    for seg in segs:
        verdict = worst(verdict, judge(seg, depth))
    for body in shell_bodies:
        verdict = worst(verdict, judge_command(body, depth + 1))
    return verdict


def guard_git(data):
    cmd = (data.get("tool_input") or {}).get("command") or ""
    session = data.get("session_id") or ""
    verdict, reason = judge_command(cmd)
    if verdict == "block":
        decide("guard-git", "block", cmd,
               f"Blocked: {reason}. Fix what the hook reports; a false positive is fixed in "
               f".claude/hooks/hooks.py, after asking the maintainer.", session)
    if verdict == "ask":
        decide("guard-git", "ask", cmd, f"{reason[0].upper()}{reason[1:]}. The maintainer approves it first.", session)
    decide("guard-git", "allow", cmd, session=session)


# protect-paths

LICENCE_NAMES = {"license", "licence", "copying", "unlicense"}
GUARDS = (".githooks/", ".claude/hooks/", ".claude/settings.json", ".github/workflows/")


def git_location(path):
    """The repository top and the path relative to it, as git spells them."""
    d = os.path.dirname(os.path.abspath(path))
    while d and not os.path.isdir(d):
        d = os.path.dirname(d)
    try:
        top = subprocess.run(["git", "-C", d, "rev-parse", "--show-toplevel"],
                             capture_output=True, text=True, check=True).stdout.strip()
        prefix = subprocess.run(["git", "-C", d, "rev-parse", "--show-prefix"],
                                capture_output=True, text=True, check=True).stdout.strip()
    except (OSError, subprocess.CalledProcessError):
        return None, None
    below = os.path.relpath(os.path.abspath(path), os.path.abspath(d))
    return top, os.path.normpath(os.path.join(prefix, below))


def protect_paths(data):
    path = (data.get("tool_input") or {}).get("file_path") or ""
    session = data.get("session_id") or ""
    if not path:
        decide("protect-paths", "allow", "no file", session=session)
    parts = [p.lower() for p in os.path.normpath(path).split(os.sep)]
    name = os.path.splitext(parts[-1])[0]
    if name in LICENCE_NAMES or "licenses" in parts[:-1] or "licences" in parts[:-1]:
        decide("protect-paths", "block", path,
               f"Blocked: {os.path.basename(path)} holds a licence text, which is never edited (ADR 0002).", session)
    if ".git" in parts[:-1]:
        decide("protect-paths", "block", path,
               "Blocked: files inside .git are changed through git commands, not edited.", session)
    top, rel = git_location(path)
    if rel:
        low = rel.lower()
        if low.startswith("internal/store/migrations/"):
            exists = subprocess.run(["git", "-C", top, "cat-file", "-e", f"HEAD:{rel}"],
                                    capture_output=True).returncode == 0
            if exists:
                decide("protect-paths", "block", rel,
                       f"Blocked: {rel} is a committed migration. Add a new numbered migration instead.", session)
        if any(low == g.rstrip("/") or low.startswith(g) for g in GUARDS):
            decide("protect-paths", "ask", rel,
                   f"{rel} is one of the checks that guard this repository. The maintainer approves changes to it.", session)
    decide("protect-paths", "allow", rel or path, session=session)


# format

def format_file(data):
    path = (data.get("tool_input") or {}).get("file_path") or ""
    session = data.get("session_id") or ""
    try:
        if path.endswith(".go"):
            subprocess.run(["gofmt", "-w", path], capture_output=True)
        elif path.endswith(".swift"):
            subprocess.run(["swift", "format", "--in-place", path], capture_output=True)
    except OSError:  # gofmt or swift missing: formatting is best effort
        pass
    decide("format", "allow", path, session=session)


def main():
    hooks = {"guard-git": guard_git, "protect-paths": protect_paths, "format": format_file}
    if len(sys.argv) != 2 or sys.argv[1] not in hooks:
        print("usage: hooks.py guard-git|protect-paths|format", file=sys.stderr)
        sys.exit(2)
    try:
        data = json.load(sys.stdin)
    except ValueError:
        data = {}
    hooks[sys.argv[1]](data)


if __name__ == "__main__":
    main()
