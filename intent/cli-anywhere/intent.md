<!-- SPDX-License-Identifier: AGPL-3.0-only -->
# Intent: use the command line to create, manage and do things, from anywhere

Author: Claude, confirmed by Martijn Mullié (maintainer)
Status: accepted
Started: 2026-11-02

## Problem

Allstill can be set up and changed with commands, but only from the server, and those commands cannot tell you what exists.

- **The commands only work on the server.** Nearly everything under `allstill admin` opens the database file directly, so it needs a shell on the server, and in a container a `docker exec` as well. Someone at their own laptop has to connect to the server first for every small thing.
- **Elsewhere they can quietly do the wrong thing.** Run on a laptop, `allstill admin create-project Backups` creates a new `allstill.db` in the current folder, reports success and prints a ping key that works nowhere. Some others succeed the same way, such as `set-alerts on`. Most stop with an error, but leave the new file behind. Beyond those errors, there is only a warning in the guide.
- **You cannot ask what exists.** There is no command that lists projects, checks, people or probes, and none that shows the status of anything. Changing a check takes its exact name and its project's name, and the command line will not tell you either. Today the answer is to open the dashboard, or to read the database by hand.
- **Common tasks have no command.** A check that a job reports to, which is what Allstill is first of all for, cannot be added with a command other than by importing it from Healthchecks, and its ping URL cannot be shown. The first ping can make a check, but then it has a default schedule until someone changes it. The log cannot be read. An incident cannot be acknowledged or snoozed, here or in the dashboard. A project or a probe cannot be deleted, and a person cannot be removed, here or in the dashboard.
- **A command line is out of reach for most people who may act.** An editor of one project can only use the dashboard. Giving them a shell on the server is not an answer: the database holds every project's secrets.
- **Commands have no name behind them.** The log says "a command on the server", which says how something was done but not who did it. That is the most it can say today.
- **Scripts and AI assistants have no way in.** A script cannot read the status of a project. An AI assistant working on a job, usually somewhere other than the server, can only write out a command for a person to run. The JSON API that would serve them is described in `api/`, but apart from what probes use, it is not served. The description also stops short of this intent: it has no way to create a project, to manage people or probes, or to read the log.

The maintainer misses this on a real installation. Anyone else who sets up Allstill meets the same limits, as setting it up now means working on the server.

## Proposed outcome

- From their own machine, someone signs in once and then works with Allstill from the command line, with no shell on the server.
- They can see what exists and how it is doing, and can create, change and remove what they are allowed to.
- What they can do is what their role allows. An editor of one project can manage that project's checks and sees nothing of another. An administrator can also do most of what today takes the server.
- What they do is recorded in the log as done by them, not by "a command on the server".
- Running a command against nothing, or against the wrong installation, fails with a clear message and never pretends to have worked.
- The output is easy to read for a person and easy to use in a script.
- The way in is the one ADR 0001 and `api/README.md` already name: the command line uses the JSON API, after someone signs in with something like `allstill login`. The menu bar app, scripts and AI assistants then have the same way in. How it works is for the spec.
- Success: from a laptop with no shell on the server, someone signs in, creates a project, adds a check for a nightly job, gets its ping URL and sees the check turn up, in a few minutes and without opening the dashboard. And the maintainer stops connecting to the server for daily work.

## Affected users and systems

- Administrators, who today need the server for projects, people, probes and switching alerts on or off.
- Owners and editors of a project, who get a command line for the first time.
- Viewers, who could look things up from a command line, if they should.
- One operator running checks for many parties: each party reaches its own project and nothing of another's.
- People who install Allstill for the first time, who still need a way to make the first administrator.
- Scripts and AI assistants, which act without a person at the keyboard.
- The menu bar app, which is planned to use the same API, and its generated client.
- The server, which starts answering a new kind of request and keeping a new kind of credential.
- The log, the guide, `docs/running.md` and the API description, which all describe how this works today.

## Constraints

- Nobody can do more through the command line than their role allows, and the same rules are applied in the same place, whichever way a request comes in. A check refused in the form is refused here. What today takes the server goes to whom ADR 0001 gives it, such as an owner for a project's members and an administrator for people. ADR 0001 does not say who creates a project; this intent takes that to be an administrator, as it is today. The dashboard offers none of that yet.
- Decisions already accepted stand, unless the spec changes them on purpose and says so. ADR 0001 gives machines keys that belong to a project or the workspace, never to a person, and gives a person a token per device. The manage-checks spec (R20 to R22, C1) already chose which check operations the API serves and gave projects `read` and `manage` keys that an owner makes, to be built once something is about to use them, and left sign-in for people until the menu bar app. This intent is that something, and it asks for sign-in sooner.
- A ping URL, a project's ping key, a probe's key, a URL holding a token and any new credential stay secret: shown only to those who may see them, never in the log.
- A credential can be taken away, and when it is, it stops working at once. Losing a laptop must not mean losing control of the installation.
- Every action goes through the same record as the dashboard's, with who did it. The log's intent already expects the API to do so.
- `allstill run` keeps working as it does: it needs only a ping URL, and it never blocks or changes the job it wraps. Nothing here makes a job depend on someone being signed in.
- Stays within ADR 0001, which already names how the command line signs in (the device grant) and how tokens are kept (hashed, revocable per device). A change to that is a new ADR.
- The API description in `api/` is the source of truth and is published under Apache-2.0: what is served follows it, and where the description turns out wrong, it changes first.
- A passkey stays the way people prove who they are. Opening the server to the command line must not add a weaker way in.
- It works when server and command line are of different versions, or says plainly that they do not match.
- Checks and alerts keep running as they do throughout.

## Out of scope

- Dashboard pages for managing projects, people and probes. These are wanted too, and get their own intent.
- The menu bar app itself, and an MCP tool for AI assistants, though both may use what this builds.
- Team chat and other new alert channels.
- Being able to do everything the API description lists: calendars and maintenance windows, for instance, do not exist yet.

## Open questions

- Which commands come first? A first version could read everything and manage checks, or could cover projects and people from the start.
- What stays on the server only? `serve` and `backup` surely. What about making the first administrator, switching alerts on or off, importing from Healthchecks, a passkey link for someone who lost their device?
- Should an administrator's most dangerous actions need something more from a laptop than from the server, such as a second confirmation?
- What becomes of `allstill admin`: does it stay as it is for use on the server, or does it become the same commands pointed at the local installation? Do the same commands get two forms, or one?
- Should `allstill admin` refuse to create a new database by accident? This may be worth doing straight away, whatever else is decided.
- A script uses a project's key, as already decided, and real setups have asked for a read-only one that shows a project's status. Is that enough, or does a script working across projects, such as an administrator's, need a workspace key as well?
- What does it mean for a key to be in the log's "by whom" when it was made by someone who has since left?
- Can someone sign in where there is no browser, such as on a server, using their phone? ADR 0001 names the device grant for the command line; does that hold with passkeys?
- How long does a sign-in last, and where is the credential kept on the machine?
- Can one person work with several installations, as the menu bar app is meant to?
- Where do people see and revoke what is signed in? The dashboard has no page for this yet, and recovery codes and managing passkeys were already due in this phase.
- Are deleting a project and removing a person part of this? They need decisions of their own: what becomes of its checks and their history, and what the log keeps.
- What must the API description gain and lose? It has kept up with alerts, but leaves out the log and has no way to create a project or to manage people or probes. `api/README.md` leaves projects and members to the dashboard, which cannot do them. The manage-checks plan already names what should leave it. Beyond the probe, no real client has tried it.
- How does the command line find the installation: a flag, a setting, whatever was given at sign-in?
- How much can the API be asked before the server suffers, on the small machines it is meant to run on?
