<!-- SPDX-License-Identifier: AGPL-3.0-only -->
# Changelog

What changes in each release of Allstill, newest first. Changes that are merged but not released yet are under **Unreleased**.

## Unreleased

The first release is still to come. What it holds so far:

- **Pings and deadlines.** Jobs ping on URLs that follow Healthchecks.io; a check that misses its period, grace time or cron schedule goes down. Pings are limited per check, and how many runs and how much output a check keeps is configurable.
- **`allstill run`** wraps a job: it reports the start, the end, the exit code and the output, and numbers such as the size of a backup, which the dashboard shows with the run.
- **Moving from Healthchecks.** `allstill import healthchecks` copies checks over with their schedules, and `allstill compare healthchecks` lines up what both saw.
- **Checks Allstill runs itself.** HTTP and TCP checks and certificate expiry, from the server or from a probe inside another network (`allstill probe`).
- **People and projects.** An administrator gives someone their first passkey link with `allstill admin create-user`, as an administrator or for chosen projects with a role. A team's owners let people in themselves, with an invitation that works once and makes an account with a passkey, and change or take away their roles under People in the dashboard. They make, rename and delete the team's projects there too; a project's page says who can see it and who is told, and everyone chooses for themselves whether they are told about it. All of that but naming, inviting, making less or taking out an owner also works from any machine, with `allstill projects` and `allstill teams` over the API (0.15.0).
- **Dashboard.** Sign in with a passkey; see checks by project, their runs and output, and a log of what happened, when and by whom. Those who may edit a project add and change its checks there.
- **Alerts, in shadow mode.** Incidents, what each person would be told, quiet hours and the morning digest, recorded under Messages and not sent until an administrator turns them on. People add devices (Web Push) and email addresses and get test messages.
- **From any machine.** `allstill login` signs in by approving a code with a passkey; `allstill status`, `checks`, `log` and the rest read and change what you may see over the JSON API, and `allstill keys` gives a script its project's key.
- **Teams, begun.** Every project belongs to the installation's team, which its administrators own; being an administrator gives no project by itself. The API's projects say their `team` to its members and `me` lists `teams`. `allstill whoami` and `allstill admin set-member` word an administrator's access accordingly. A command on the server that gives someone a role, makes a sign-in link, takes away a passkey or ends sign-ins tells the owners of the team concerned, at once and whatever the alerts switch says. `allstill admin create-team` makes a second team beside the first, for someone who runs their own checks on the installation without its administrator being shown them; a team has its own limits, takes turns with the others when attempts wait, and reaches the server's private ranges only when an administrator says so.
- **The source on every page.** Every page links to the source of the running version; `-source-url` points a changed version at its own.
- **Packaging.** Linux packages, a container image and plain archives, built by GoReleaser from a tag.
