<!-- SPDX-License-Identifier: AGPL-3.0-only -->
# Allstill guide

Allstill tells people whether their jobs ran and their sites are up, and keeps it calm: one alert per problem, the rest in a morning digest. It runs on a server of its own. People see it in a dashboard, where they also manage their own devices, and where those who may edit a project add and change its checks. Projects, people and alerting as a whole are managed with the commands below.

There are two ways to watch something:

- **A job reports in.** Cron jobs, backups and scripts ping Allstill when they run. When a ping does not arrive on time, Allstill raises the alarm. Use `allstill run`.
- **Allstill looks for itself.** Sites, APIs, open ports and TLS certificates. Add them under **Add check** in the dashboard, or with `allstill admin add-check`, on the server.

## For AI assistants

- A ping URL is a secret: anyone who has it can report for that check. Keep it in the place the project keeps its secrets. Never put it in the repository, in a log or in a message. If one leaks, someone who may edit the project replaces it on the check's form in the dashboard; the old one stops working at once, so jobs need the new one.
- You usually work somewhere other than the Allstill server. Unless you have been given access there, write the exact command for the person to run. See "On the server" below: these commands do not fail elsewhere, they quietly do the wrong thing. Someone without access to the server who may edit a project can still change its checks: see "In the dashboard" below.
- Do not switch alerts on or off, and do not promise anything from "Not here yet" below.
- Run `allstill <command> -h` for every flag of a command. If this guide and that help disagree, the help is right.

## Report a job

Get the ping URL from the person who runs Allstill, or from the check's page in the dashboard if you are told it is there. It takes one of two forms:

```text
https://allstill.example.com/ping/CHECK-UUID
https://allstill.example.com/ping/PROJECT-PING-KEY/nightly-backup
```

The second form names the check with a slug: lowercase letters, digits, `-` and `_`, starting with a letter or digit. `start`, `fail` and `log` are taken. A project's ping key is shown once, when `allstill admin create-project` makes the project.

Give the URL to the job in `ALLSTILL_URL`, and wrap the job's command:

```sh
ALLSTILL_URL="$BACKUP_PING_URL" allstill run -- ./backup.sh
```

This reports the start, the end, the exit status and the tail of the output, which goes to the server, so the job should not print secrets. The exit status of `allstill run` is the job's own. Each job needs its own URL. Prefer the variable to `allstill run -url`, which shows the secret to anyone who can list processes.

To have the first ping create a check that does not exist yet, end a URL of the second form with `?create=1`. If the slug comes out wrong, `allstill admin delete-check` removes the check, or **Delete check** on its form in the dashboard.

**Check the job against the schedule first.** A check made with `create=1` gets one schedule until someone changes it, on its form in the dashboard or on the server with `allstill admin set-check -project Backups -check nightly -every 1h -grace 30m`, or `-cron "0 3 * * *" -time-zone Europe/Amsterdam` in place of `-every`. A new schedule counts from now. As made, it goes down in two cases:

- No ping has arrived for a day and an hour. That suits a daily job. An hourly job would be missed for a day, and a weekly job would show as down between its runs.
- A run started and did not end within an hour. A job that takes longer would go down every time, although not on its first run, so this is easy to miss when you test it. Such a job should ping only when it ends, with curl as below, without the `/start` line. That line then no longer creates the check, so the last ping has to: `"$BACKUP_PING_URL/$??create=1"`.

Tell the person before setting up a job that does not fit, and what to change. Checks imported from Healthchecks keep their schedules.

### With curl

Without the program on that machine, ping with curl, which also shows the URL to anyone who can list processes. `-f` makes a wrong URL fail instead of passing quietly:

```sh
curl -fsS -m 10 --retry 3 "$BACKUP_PING_URL/start?create=1"
./backup.sh
curl -fsS -m 10 --retry 3 "$BACKUP_PING_URL/$?"
```

Here the variable must hold the URL without a query, and `?create=1` goes after `/start`. If the variable itself ends in `?create=1`, what the script adds lands in the query: a job that fails is then reported as a success, or the ping is not found.

A script that stops at an error never sends its last ping, so Allstill only notices later. `/fail` reports a failure. A job that starts again within seconds of its end should add `rid` to both its pings, with a new UUID for each run (`?rid=`, or `&rid=` after `?create=1`); `allstill run` does this for you.

### Numbers

A job can also report numbers, such as the size of a backup, and the dashboard shows them with the run. Under `allstill run`, it writes one `name=number` per line to the file named in `$ALLSTILL_VALUES_FILE`:

```sh
echo "kilobytes=$(du -sk /backup | cut -f1)" >> "$ALLSTILL_VALUES_FILE"
```

That variable only exists under `allstill run`. With curl, add them to the last ping: `?v=kilobytes:18400`.

## In the dashboard

Someone who may edit a project needs no access to the server to add and change its checks.

- **Add check** asks what to watch. A new job's page shows a line with its ping URL, to run at the end of the job. That URL is a secret, so handle it as above, and see "Report a job" for how to report a failure too.
- A check's page has **Edit**, and **Pause** or **Resume**. Its form also deletes it, moves it to another project the person may edit, and for a job replaces its ping URL, each after saying what will happen.
- A probe's own check, which tells when the probe goes quiet, can be renamed but not deleted or paused, and only an administrator can move it.
- Someone who may only view a project can only look. For more, they ask an administrator, who sets it on the server.

## On the server

These commands read and write the server's database: everything under `allstill admin`, `allstill import healthchecks` and `allstill compare healthchecks`. Run them on the server, in the form that fits how it was installed:

```sh
docker exec allstill allstill admin create-project Backups
sudo -u allstill allstill admin create-project -db /var/lib/allstill/allstill.db Backups
```

The examples below leave that part out. Never run them anywhere else. They would quietly create an empty `allstill.db` in the current directory and succeed: the ping key they print works nowhere, and an import would leave secret ping URLs in that folder.

## Watch a site or a port

On the server, in an existing project:

```sh
allstill admin add-check -project Sites -name shop -http https://shop.example.com/health -json /status=ok
allstill admin add-check -project Sites -name www -http https://www.example.com -expect 301 -location https://example.com/
allstill admin add-check -project Sites -name mail -tcp mail.example.com:993 -tls
allstill admin set-check -project Sites -check shop -every 5m
allstill admin pause-check -project Sites -check shop
allstill admin resume-check -project Sites -check shop
allstill admin delete-check -project Sites -check shop
```

Run from the server, `add-check` tries the address once and says what the check will see. Until a check has passed once it stays new and alerts nobody, saying why on its page and in the digest, so a site that is already down when you add it does not alert until it has been up. A check over TLS also watches its certificate. Use `allstill admin add-check -no-verify` only for a self-signed certificate.

## Watch inside another network

The server cannot reach private networks by itself. A probe runs inside the network and connects out, so no port has to be opened. It is the same program:

```sh
allstill admin create-probe office -project Customer
allstill admin add-check -project Customer -name intranet -http http://intranet.example.com/health -probe office
allstill probe -server https://allstill.example.com -allow 10.0.0.0/8
```

The first two run on the server, the last inside the network, with the probe's key in `ALLSTILL_PROBE_KEY`. That key is a secret and is shown once. `allstill admin rekey-probe` gives a probe a new one.

## Move from Healthchecks

Ping URLs follow Healthchecks, so a job moves by changing the start of its URL: `https://hc-ping.com/CHECK-UUID` becomes `https://allstill.example.com/ping/CHECK-UUID`. A job that pings by slug also needs the project's new ping key, which the import prints once when it creates the project.

1. On the server, copy the checks: `HEALTHCHECKS_API_KEY=$KEY allstill import healthchecks -project Backups`. It needs the project's read-write API key, which the person should set themselves (in a container, with `docker exec -e HEALTHCHECKS_API_KEY`). It only reads from Healthchecks.
2. Let jobs report to both: `ALLSTILL_URL` holds both URLs, separated by a space.
3. On the server, line up what each saw: `HEALTHCHECKS_API_KEY=$KEY allstill compare healthchecks -since 2026-10-01`

## Alerts and people

A new installation starts in shadow mode: it records what it would have sent under **Messages** in the dashboard, and sends only test messages. Someone who wants alerts adds a device or an email address under **Devices**. An administrator then switches them on with `allstill admin set-alerts on`, and `shadow` goes back. No command shows which mode it is in, so ask the person before telling them they will be alerted.

- Allstill keeps a log of what happened to checks and who did what, for 400 days (`-keep-log`), with people's names in it, and no page to read it on yet.
- `allstill admin set-alerting -project Backups -check nightly -urgency digest` sets how fast a check's problems reach people: `now`, `digest` or `none`. `allstill admin set-alerting -critical` breaks through quiet hours. Each call sets both, so give `-urgency` and `-critical` together when you want both.
- `allstill admin create-user` adds a person, with a link to make a passkey. Without `-project` they are an administrator who sees every project, so for an outside party add `-project Customer -role viewer` (or `editor`, `owner`): they then see only those projects. `allstill admin set-member` changes a role later, and `-role none` takes someone out of a project. `allstill admin passkey-link` gives a new link to someone who lost their devices.

## Not here yet

- Seeing a check's status from outside the dashboard. The JSON API described in the project's `api/` directory is not served yet, apart from what probes use.
- Rules that judge a run by its values. They are recorded and shown, and nothing more.
- Changing a project's default schedule and grace period.
- Managing projects, people, probes and who is alerted in the dashboard: for now they are commands, run on the server.
- Alerts other than by push and email, and changing quiet hours or the digest's time.
- Calendars, maintenance windows and status pages.
- The macOS menu bar app.
