<!-- SPDX-License-Identifier: AGPL-3.0-only -->
# Running Allstill

Allstill is one program and one database file. It needs a host your jobs can reach, a host name, and HTTPS in front of it: passkeys only work over HTTPS, and they belong to the host name, so pick the name before anyone signs in.

## Install

Each release has the same program in three forms. Pick one.

- **A Linux package** (`.deb`, `.rpm`, `.apk`) installs `/usr/bin/allstill`, a systemd service and its settings in `/etc/allstill/allstill.env`. It runs as the user `allstill`, with its database in `/var/lib/allstill`. The service does not start by itself.
- **A container image**, `ghcr.io/martijnmul/allstill:1.2.3`, for Linux on amd64, arm64 and armv7, tagged with its version only. It runs as user 65532 and keeps its database in `/home/nonroot`: mount a volume there, or a directory owned by 65532.
- **An archive** with the program alone, for Linux, macOS and Windows, including the Raspberry Pi (armv6 and armv7). A Homebrew formula comes later.

A package upgrade restarts a running service; removing the package stops it and leaves the database.

## Start

1. Set the address people will use, such as `ALLSTILL_BASE_URL=https://allstill.example.com`: in `/etc/allstill/allstill.env`, as `-e` for the container, or as `allstill serve -base-url`.
2. Put a reverse proxy with TLS in front of `127.0.0.1:8000` (in the container, port 8000), and keep the `Host` header as it is.
3. Start it: `systemctl enable --now allstill`, or run the container.
4. Add yourself. The server makes its database when it first starts, and commands refuse to run without one. Allstill prints a link that works once, for a day, where you make a passkey:

   ```sh
   sudo -u allstill allstill admin create-user -db /var/lib/allstill/allstill.db -name "Ada" -email ada@example.com
   docker exec allstill allstill admin create-user -name "Ada" -email ada@example.com   # in the container
   ```

   Someone who lost their devices gets a new link the same way:

   ```sh
   sudo -u allstill allstill admin passkey-link -db /var/lib/allstill/allstill.db -email ada@example.com
   docker exec allstill allstill admin passkey-link -email ada@example.com   # in the container
   ```

`allstill serve -h` lists every setting; each has an environment variable.

## Move from Healthchecks

1. Copy a project's checks: `HEALTHCHECKS_API_KEY=… allstill import healthchecks -project "Backups"` (add `-url` for a self-hosted Healthchecks). Each check keeps its UUID, so a job that pings by UUID only changes the start of its URL: `https://hc-ping.com/…` becomes `https://allstill.example.com/ping/…`. A job that pings by slug needs the project's new ping key as well, printed once when the import creates the project: Healthchecks does not hand out its own.
2. Let jobs report to both for a while: `allstill run --url https://hc-ping.com/… --url https://allstill.example.com/ping/… -- ./backup.sh`, or two space-separated URLs in `ALLSTILL_URL`.
3. Compare: `HEALTHCHECKS_API_KEY=… allstill compare healthchecks -since 2026-10-01` lists, per check, where Allstill saw it go down or come back up differently from Healthchecks: missed, extra, or more than five minutes apart (`-within`). Give `-since` the day the jobs began to ping both. Healthchecks keeps about three months of this history, and counts a check it paused as down until its next ping. It matches checks by UUID, so a project split over several in Allstill compares as one, and lists the checks Allstill never heard from, as they cannot alert, with when Healthchecks last did. One difference is by design: a start that arrives after its end joins that run in Allstill, where Healthchecks waits for another end and reports the check down. With the same `rid` that holds within the grace period; without a `rid`, when the latest run is an end that came without a start, at most ten seconds ago, and that no other start has joined. A job that restarts within seconds of its end should send a `rid`: without one, after an end without a start, its runs are not tracked until it leaves a longer gap, and only the schedule notices one that hangs.
4. Switch a check off at Healthchecks once the comparison has stayed clean for a few weeks.

One Healthchecks project often holds checks that belong to different parties, told apart by tags. Import it per tag with `-tag`, or split it afterwards: `allstill admin move-checks -from Imported -to Customer -tag customer` moves checks by tag or by name (`-check`), and `allstill admin rename-project` renames a project. A moved check keeps its UUID, runs and pings. A job that pings it by slug needs the new project's ping key: with the old key and `create=1`, it would make a new check in the old project.

The ping URLs and answers are those of Healthchecks, with one difference: hc-ping.com answers a ping to a UUID it does not know with 200, Allstill with 404, as self-hosted Healthchecks does. A script that pings with `curl -f` fails on a wrong URL instead of passing quietly.

## Alerts

A new installation starts in shadow mode: when a check goes down, Allstill decides what it would tell whom, and when, and lists that under **Messages** in the dashboard, but sends only test messages. Compare that with the alerts you get elsewhere, from Healthchecks with `allstill compare healthchecks` ([intent/alerts](../intent/alerts/spec.md)).

When the comparison is clean and everyone's devices or addresses are confirmed, switch alerts on: `docker exec allstill allstill admin set-alerts on` (with the Linux package: `sudo -u allstill allstill admin set-alerts -db /var/lib/allstill/allstill.db on`). It says who would be told but has no confirmed device or address; a confirmed address counts as reachable, and when the command cannot see the email settings, as with the Linux package, it says so. Incidents open at that moment began in shadow mode and stay quiet; the digest lists them. `set-alerts shadow` goes back. Then switch the first checks off at Healthchecks, the nightly backups for example.

Get your devices ready meanwhile. Under **Devices**, *Get alerts on this device* subscribes the browser you are in for push notifications; send it a test and open the notification to confirm it, and alerts can reach it. On an iPhone or iPad, add Allstill to the Home Screen first and open it from there. Add an email address there too, once [email](#email) is set up, and open the link in its test message. Test messages are sent even in shadow mode.

- An alert goes to every confirmed device, and by email when none takes it; the daily digest goes by email, or as a short push without one. An alert that reached nobody is decided again ten minutes later; a digest's news waits for the next digest. A message about a check coming back up waits until the message that it went down has gone out, so you never hear only the half of it.
- A push service that took a message has not shown it: a phone that is off never makes the email follow. An address of your own as well is the surer way.

- Push needs Allstill reached over https (`-base-url`): Apple refuses pushes from anything else.
- Each time the dashboard is opened on a device, it renews that device's subscription. A device that shows *Must subscribe again* is put right by opening Allstill on it.
- The overview says when alerts may not reach you: a message to you failed in the last day and no alert or digest has reached you since, a device needs you, Allstill is behind with sending, or nobody gets alerts for a project you see. A device whose pushes failed, say while Allstill or the push service was cut off, is cleared by the next push it takes.
- Serving a copy of the database? Start it with `-shadow` (`ALLSTILL_SHADOW=1`): it then sends no alerts, whatever the copy says.

- Everyone who gets alerts for a project is told once per incident, and again when it is resolved. Owners and editors get them, and so do workspace administrators, for every project unless they are a viewer of it; viewers do not.
- New people have quiet hours from 22:00 to 07:00 in their time zone. An alert waits for the morning unless its check is critical.
- After Allstill was down, or its host slept or its clock jumped forward, checks whose deadline passed meanwhile become unknown, not down, and nobody is alerted for them.
- `allstill admin set-alerting -project Backups -check nightly -urgency digest` sets how fast a check's problems reach people: `now`, `digest` (the daily digest at 08:00) or `none`. `-critical` breaks through quiet hours.

## Email

Allstill sends email through an SMTP server you already have: your mail provider's, a sending service, or a relay of your own. It is the fallback when no device takes an alert, and how digests arrive. People add their addresses under **Devices**.

| Setting | Example | |
| --- | --- | --- |
| `ALLSTILL_SMTP_HOST` | `smtp.example.com` | No email without it. |
| `ALLSTILL_SMTP_PORT` | `587` | The default; 465 with `tls`. 465 and 2465 use TLS from the start. |
| `ALLSTILL_SMTP_SECURITY` | `starttls` | Or `tls` (also written `ssl`), or `none` for a relay you trust, such as a container next to Allstill. With `starttls`, Allstill never sends unencrypted. |
| `ALLSTILL_SMTP_USERNAME` | `allstill@example.com` | |
| `ALLSTILL_SMTP_PASSWORD` | | Or `ALLSTILL_SMTP_PASSWORD_FILE`, a file holding it, such as a container secret. Never a flag. |
| `ALLSTILL_SMTP_FROM` | `Allstill <allstill@example.com>` | On a domain the server may send for. |
| `ALLSTILL_SMTP_CA_FILE` | | Certificates to trust for a relay with its own, such as Proton Bridge. |

Then check it, in the container: `docker exec allstill allstill admin test-email -to you@example.com`, or with the Linux package, whose settings only root may read: `sudo allstill admin test-email -env-file /etc/allstill/allstill.env -to you@example.com`. It says how far it got (connected, TLS, signed in, sender and recipient accepted), or what the server answered, and whether to change the settings or try again later. When a server refuses the message only after taking the recipient, read its answer: it tells whether the address or the settings are wrong. At start, `allstill serve` signs in once without sending and logs whether that worked. When email fails because of the settings, administrators see it on the overview; an address the server refuses shows on its owner's.

What to know:

- **The From address** must be on a domain your provider sends for, or the message lands in spam or is refused: a `gmail.com` From through Postmark fails, for example. With a mailbox of your own, bounces come back to it, so use one someone reads; sending services collect them in their own dashboard.
- **Gmail and Google Workspace:** `smtp.gmail.com`, your address, and an app password, which needs 2-step verification. Where a Workspace administrator turned app passwords off, use `smtp-relay.gmail.com` with the server's address allowed.
- **iCloud:** `smtp.mail.me.com`, an app-specific password, and a From that is your iCloud address or an alias.
- **Fastmail:** `smtp.fastmail.com` with an app password.
- **Proton:** an SMTP token works on paid plans with your own domain; otherwise Proton Bridge, with `ALLSTILL_SMTP_CA_FILE` for its certificate.
- **Microsoft:** Outlook.com accounts only allow OAuth, which Allstill does not do. Microsoft 365 is retiring password sign-in for SMTP and turns it off by default; use a sending service, or your tenant's relay.
- **Sending services** (Postmark, Resend, Mailgun, Amazon SES and the like) work well, and most are free or nearly so for a few messages a day. Postmark's token is both user name and password; Resend's user name is `resend`; SES needs its SMTP credentials, not IAM keys, and in its sandbox delivers only to verified addresses.
- **Blocked ports:** some clouds block port 25, and some (Hetzner Cloud) 465 too. Use 587, or a sending service's 2525 or 2587.

## Changing and removing checks

Anyone who may edit a project does this in the dashboard: **Add check**, and **Edit** and **Pause** on a check's page, with moving, replacing a ping URL and deleting at the bottom of its form. Each change goes in the server's log with who made it and what kind of change, without an address or a setting's value. From a shell:

```sh
allstill admin set-check -project Backups -check nightly -cron "0 5 * * *" -time-zone Europe/Amsterdam -grace 1h
allstill admin set-check -project Backups -check hourly -every 1h
allstill admin pause-check -project Backups -check nightly
allstill admin resume-check -project Backups -check nightly
allstill admin delete-check -project Backups -check old-job
```

- **A new schedule counts from now.** Changing it never makes a check late or down by itself. A check that was late goes up, saying the schedule changed and no ping has come since, and a check that is down stays down until a ping. Only a change of schedule or grace does this: renaming a late check does not postpone it.
- **Pausing.** Nothing is expected of a paused check, and an open incident closes without a recovery message. A digest that goes out anyway lists paused checks, so a forgotten one gets noticed, unless it never alerts; a pause alone sends no message. A job's next ping resumes it, and a failure then alerts as usual, so stop the job too if it should stay quiet. Resuming starts a check over, as new.
- **Deleting is for good.** A check is gone at once, its ping URL stops working, and what it fetched is cleared; its runs, pings and output go with the next tidy, within minutes. Past incidents keep its name, and anyone told it was down reads in the next digest that it was closed this way. A job that still pings its slug with `create=1` makes a new check. A probe's own check cannot be deleted, paused or rescheduled.

## What an editor can reach

Someone who may edit a project can have the server, or a probe serving that project, fetch an address. So they can:

- learn whether a port is open, on the public internet and within `-allow-private`;
- read up to 40 characters of a JSON field that does not hold what they asked for, in a check's reason, which viewers see too;
- send a GET to any address that can be reached, up to once a minute, under this server's name and address. A site they own sees the server's address and version.

They are not told what a host name resolves to, or that an address is the server's own: only that it may not be reached. A project holds at most 200 checks Allstill runs and 1000 ping checks when added this way, of the 20 attempts running at once at most 5 belong to one project, and a person gets 6 tries and 30 new addresses a minute. A shared probe also opens each served project's editors to the others' network, and whoever runs it receives the addresses of its checks, so one probe per party is the safe setup. Choose editors accordingly.

Only an owner or administrator can have a check wake people during their quiet hours.

## People

`allstill admin create-user` without `-project` adds a workspace administrator, who sees every project and gets alerts for all of them. To add someone who should only see their own, such as a customer:

```sh
allstill admin create-user -name "Bob" -email bob@example.com -project Customer -role viewer
```

- **Viewer:** sees the project's checks with their descriptions and tags, last runs and why they failed, what jobs printed, and the scheme, host and port of what a check looks at, so do not add an outsider to a project whose internal host names they should not know. Not a full URL or a ping URL. Gets no alerts.
- **Editor and owner:** also see full URLs and ping URLs, and get alerts for the project, also for what is already open when they join.

`allstill admin set-member -email bob@example.com -project Customer -role editor` changes a role from their next click, and `-role none` takes someone out of a project. `-project` can be repeated when adding someone; `set-member` adds them to one more. Someone in no project can still sign in, and sees no checks.

## Signing in from the command line

From your own machine, `allstill login https://allstill.example.com` prints an address and a short code. Open the address in a browser on any device, also your phone, sign in, enter the code, and approve it with your passkey. The command line then finishes by itself, so the machine needs no browser. A code works once and is gone after 10 minutes, and you get a few wrong tries a minute.

- **It acts as you, with your role as it is at that moment.** So a change of role counts at once. `-read-only` makes one that can only read, as a viewer in every project you can see, also if you are an administrator: use that for an AI assistant. Asking too often gets a sign-in told to wait: each has 120 requests a minute, as a probe's key has.
- **It is kept in a file only you can read,** in your user's configuration folder, per installation. A file is what `gh` falls back to and how ssh keeps keys: anything running as you can read it. If others can read the file, commands refuse it.
- **It ends** after 30 days without use and after a year at most, as a browser session does, or at once with `allstill logout`, or under **Devices** in the dashboard, where you see every sign-in and browser session you have with when it was last used.
- **Never approve a code you did not ask for yourself.** The page says what the machine calls itself and when it asked, but that is only its own claim, and it cannot show where it is, as nothing in Allstill knows a caller's real address behind a proxy. A code is always typed, so a link can never arrive with one filled in. A sign-in you approved by mistake is under **Devices** from that moment: end it there, and whatever was fetched stops working.
- **A lost or stolen device.** From another device, end its sign-in and browser session under **Devices**. A passkey on it stays good until an administrator removes it on the server:

  ```sh
  docker exec allstill allstill admin remove-passkey -email ada@example.com                  # lists theirs
  docker exec allstill allstill admin remove-passkey -email ada@example.com -passkey pky_…   # removes it, and what it approved
  docker exec allstill allstill admin end-sessions -email ada@example.com                    # ends every sign-in and browser session
  ```

  Then give them a new passkey with `allstill admin passkey-link`. Read the log for what was done meanwhile.
- **A proxy in front** must let `/oauth` and `/api` through to clients that are not browsers. Some block those by default, and `allstill login` then fails, saying what it got back. A proxy can also limit by address, which Allstill cannot: asking for codes is limited to 60 a minute over the whole installation, so someone hammering it can keep new sign-ins waiting. Everything already signed in goes on.

## Reading from anywhere

Once signed in, `allstill status`, `allstill projects`, `allstill checks`, `allstill checks show`, `allstill checks runs`, `allstill log` and `allstill probes` read what you may see, as the dashboard shows it to you, in the same words. `-json` prints the API's own answer, for scripts; `allstill status -json` exits 0 however bad things are. Printed text carries no control characters, so a site's reason or a typed name cannot steer your terminal.

For an AI assistant, or anything else you would not hand your rights to, use `allstill login -read-only`: it reads as a viewer in every project you can see, so it gets no whole address, no ping URL, no alert settings, no names in the log and nothing about your other sign-ins, even if you are an administrator.

## Changing checks from anywhere

Someone who may edit a project can add, change, pause, resume, move and delete its checks from their own machine, under the same rules and limits as the form in the dashboard: `allstill checks add`, `change`, `pause`, `resume`, `move` and `delete`. Each takes `-h`, and `-json` for the API's own answer. An address is tried once when it is added or changed, in the 6 tries a minute each sign-in or key gets, and it must be one the server may reach unless a probe runs it. Two people changing one check at the same moment do not overwrite each other: the second is told, and nothing of theirs is saved.

Adding a job's check prints its ping URL with a line for `allstill run`. `allstill checks ping-url` shows it again and `-replace` gives it a new one; nothing else carries it. It takes an editor's role and the `manage` scope, so a read-only sign-in or `read` key cannot read it.

**After a stolen sign-in with such rights,** end it on **Devices** and treat its projects' ping URLs as leaked, as reading one leaves no trace in the log: `allstill checks ping-url -replace` each that matters, and read `allstill log -what done` for addresses changed and for keys made. Revoke any key you did not expect.

## Keys, for scripts

A script gets a key of its project instead of a person's sign-in. An owner of the project or an administrator makes one with `allstill keys make NAME -project P -scope read` (or `manage`), and it is printed once: only its hash is kept. `allstill keys` lists them with who made each, through which sign-in for those who may read that, and when it was last used. `allstill keys revoke NAME` ends one on its next request.

Give it to a command as `ALLSTILL_TOKEN`, with `ALLSTILL_SERVER` for the address. With `read` it acts as a viewer of its one project, and with `manage` as an editor, under the limits a person has: the counts, the tries, and 120 requests a minute. It cannot reach another project, make a key, or turn on `critical`. It belongs to the project and keeps working when whoever made it has gone, so when someone leaves, look through what they made. The log names the key on what it does, and who made it on the line saying it was made.

## What the log records

Allstill writes down what happens as it happens, from the day this version first runs, and shows it under **Log** in the dashboard, with the last few lines on a check's page. Nothing before that can be recovered. You can narrow it to a project, a kind of thing, a check by its exact name (also one that has been deleted), or a person by following their name.

- **What happened to checks:** each change of state with a reason in Allstill's own wording, not the detail a check's page shows, as that can hold what a JSON field said or a probe's own text. A run that changes nothing is not written. A check that changes state more than 30 times in an hour writes one line saying so, and no more of those until it has held for an hour, or for a job that only goes late, until it has had no incident for an hour. A check going late and back is written but shows only when you tick the box for it, as a job that pings a moment after its time does that on every run; such a job writes two lines a run, about 80 MB a year if it runs every 5 minutes.
- **What was done:** checks added, changed, paused, resumed, moved or deleted, a ping URL replaced, alerting changed; projects, people and roles; a project's keys made and revoked; passkey links and passkeys, also one removed; sign-ins, from a browser and from a command line, made and ended; a browser session ended from elsewhere; devices and email addresses confirmed, handed on or removed; probes; alerts turned on or off. By whom: a person's name, with the name of the sign-in if it came from a command line, a probe's name, a job, Allstill itself, or "a command on the server" with the command's name, which says how but not who.
- **Who was reached:** each message that arrives, per check it mentions: who, what it said, at once or in the digest, and the kind of channel. It is written when the message arrives, not when it is queued, so a message taken back because it was no longer true is not there. In shadow mode a line says the message was recorded and not sent. Someone who cannot be reached is one line per incident, with why in Allstill's own words, until a message reaches them. Test messages are not written.
- **Who reads what:** nobody reads a project they are not in, and what you may see is judged as it is today, so someone added now can read back and someone removed cannot.
  - A viewer reads what happened and what was done, signed "the team", with no names and nothing about who was reached. The times still show when your team works.
  - An editor or owner also reads names and who was reached, by name and kind of channel only. An administrator who is no member of the project reads as "an administrator". Owners also read changes to roles.
  - An administrator reads everything, including why a message failed. Others' sign-ins, new projects, people, probes and whether alerts are on are for administrators; everyone reads their own sign-ins and devices.
- **Left out:** URLs, tokens, settings other than a few the check's page shows anyway, output, email addresses, device names, IP addresses and user agents. Names are copied as typed, so a check named after a secret keeps it in the log until the entry is removed. That goes for a sign-in's name too, by default its machine's host name: it stays with what that sign-in did, for those who may read who did it. A failed sign-in is not written, as anyone can cause one; it stays in the server's log.

It is a lasting record of what named people did and when they signed in. If you run Allstill for a company in the EU, that is personal data, and in some places monitoring of staff: you answer for it, and `-keep-log` is how long you keep it. No command removes someone from it yet.

It stops mistakes, and nothing in the dashboard or the commands changes or removes an entry. It is not proof against someone holding the database, and restoring a backup takes the log back with it, unnoticed.

## Sites, ports and certificates

Allstill can also look at an address itself, instead of waiting for a ping: a site, an API's health endpoint, a port that should be open, and the TLS certificate in front of any of them ([intent/http-checks](../intent/http-checks/spec.md)). Each attempt is a run with how long it took, its status code and the certificate's days left where it has them, and from there a check behaves as any other: incidents, calm alerts, the digest.

```sh
allstill admin add-check -project Sites -name shop -http https://shop.example.com/health -json /status=ok
allstill admin add-check -project Sites -name www -http https://www.example.com -expect 301 -location https://example.com/
allstill admin add-check -project Sites -name mail -tcp mail.example.com:993 -tls
allstill admin add-check -project Sites -name database -tcp db.example.com:5432
```

(With the Linux package: `sudo -u allstill allstill admin add-check -db /var/lib/allstill/allstill.db …`; in the container: `docker exec allstill allstill admin add-check …`.) It tries the address once and says what the check will see, and the running server starts it within a minute. `allstill admin set-check -project Sites -check shop -every 5m` changes only what it is given; `allstill admin add-check -h` lists every setting.

- **When it is down.** A check runs every minute by default (`-every`, from 1 minute to a day), with a timeout of 10 seconds (`-timeout`, at most 60). Until it has passed once it stays new, saying why on its page and in the digest, so a mistyped address alerts nobody; a site that is really down when you add it does not alert until it has been up once. After that it goes down after 2 failed attempts in a row (`-fail-after`) and up on the first that passes; the reason says what failed, such as `HTTP 503`, `no answer within 10 s` or `status is degraded instead of ok`.
- **What counts as up.** Any 2xx, or the codes of `-expect`. `-location` expects a redirect to that address, which is how a `www` that should lead to the apex is watched. Redirects are not followed unless `-follow` says so, then at most five. `-json /status=ok` reads at most 1 MB of the answer.
- **Ports.** `-tcp host:port` is up while the port accepts a connection; nothing is sent over it. `-tls` also finishes a TLS handshake. STARTTLS is not covered: on mail ports 25 and 587, which begin in plain text, watch the port without `-tls`.
- **Certificates.** Every attempt over TLS reads the certificate. The check is in warning when fewer than 14 days are left (`-warn-days`), or less than a third of its lifetime when that is shorter, and the morning digest lists it; an attempt fails under 3 days (`-fail-days`), or a tenth of its lifetime. An automatically renewed certificate, even one that lives six days, never warns. `-ca` trusts an internal CA's certificates; `-no-verify` skips verification, for a self-signed certificate, and still watches its expiry. Two checks on one host and port read the same certificate: within a project, `add-check` says so, and `-warn-days 0` on one of them has it warn once.
- **Being a good visitor.** Attempts are spread over their interval, one at a time per host and at most 20 at once, of which at most 5 for one project, so one project's dead hosts cannot blind the others'. Requests are sent as `Allstill/<version>`; `-contact` (`ALLSTILL_CONTACT`) adds an address for site owners. A site behind a bot challenge says so in the reason; let Allstill through there.
- **Private addresses.** The server refuses addresses that are not on the public internet, for every connection and every redirect. `-allow-private 10.0.0.0/8` (`ALLSTILL_ALLOW_PRIVATE`, comma-separated) lets checks reach such ranges: they are then open to the checks of every project, so allow only what you mean to watch. The server's own addresses are never reached.
- **Allstill's own network.** When the server's own network goes down, every site looks down. Give it a canary, addresses that answer independently of the sites you watch, such as `-canary 9.9.9.9:53,1.1.1.1:53` (`ALLSTILL_CANARY`): when a check times out or finds no route and no canary answers either, the check becomes unknown instead of down, nobody is alerted, and the overview and `/health` say the server is offline. Without a canary, Allstill alerts: it cannot tell its own outage from the sites'.
- **Who sees the address.** A check's URL may hold a token: owners and editors of its project and administrators see it whole; viewers see only its scheme and host. It is never in a reason, a message or a log. A TCP check's host and port are shown to everyone who can see the check.
- **Pings.** A ping to such a check is answered `409`: a script that pings with `curl -f` notices that Allstill runs this one itself.

## Probes

A probe watches what the server cannot reach: a service inside a customer's network, a database in a private range. `allstill probe` runs inside that network, connects out to the server, fetches its checks, runs them and sends the runs back. It opens no port, and the same binary is the probe.

```sh
# On the server: make it, and give it checks. The key is shown once.
allstill admin create-probe office -project Customer
allstill admin add-check -project Customer -name intranet -http http://intranet.example.com/health -probe office
allstill admin set-check -project Customer -check database -probe office

# Inside the network:
ALLSTILL_PROBE_KEY=... allstill probe -server https://allstill.example.com -allow 10.0.0.0/8
```

- **What it may reach.** Public addresses, and the private ranges given to it with `-allow` (`ALLSTILL_PROBE_ALLOW`), for every check on it; never its own machine's addresses. The server's `-allow-private` means nothing to it. `-ca` trusts an internal CA's certificates for every check on it.
- **Who may use it.** A probe serves the project it is made in and those in `-serves`. Only their checks can run on it, as anyone who can edit them can make it fetch from its network and read the answers. `set-check -probe ""` brings a check back to the server.
- **When it goes quiet.** It posts at least every 30 seconds. Each post pings a check of its own, in the project it was made in, so its people are alerted once when it has been quiet for 3 minutes. The checks it runs become unknown, not down, and an open incident stays open with no recovery message, until a run judges them.
- **When its own network goes down.** A check that times out asks whether the server can be reached; when it cannot, the attempt judges nothing. Runs wait in memory, at most 5000, until the server can be reached again, and are lost when the probe restarts. Each has an id, so a retry cannot count twice, and says how long ago it ended, so its time is right whatever the clock says. An outage that was already over when Allstill hears of it sends no alert: the next digest lists it. One still going alerts as any other.
- **Its clock.** Certificates are checked against the probe's clock. More than a minute off the server's, its log says so and its checks become unknown until the clock is set right.
- **Versions and keys.** A probe of another protocol version than the server's, or with a key that no longer works, stops with what to do. `allstill admin rekey-probe office` gives a new key and the old one stops working. A probe's key and the settings it fetches, tokens in URLs included, travel to it: use https, and `-key-file` where an environment variable will not do.

## AI assistants

`allstill guide` prints a manual written for people new to Allstill and for AI assistants, for the version it comes with. So that an assistant knows about Allstill in every project, install the skill that points to it, once, on a machine that has the program. For Claude Code:

```sh
mkdir -p ~/.claude/skills/allstill
allstill guide -skill > ~/.claude/skills/allstill/SKILL.md
```

Other assistants that support Agent Skills read the same file from a folder of their own. The skill holds no address or key: a job's ping URL stays in that project's secrets, and the manual tells the assistant so. It stays current by itself as long as the program does, so upgrading Allstill on that machine is all it needs.

## Keep it

- **Backups.** Copy the database while it runs with `sudo -u allstill allstill backup -db /var/lib/allstill/allstill.db /var/lib/allstill/backup-$(date +%F).db` (in the container: `docker exec allstill allstill backup /home/nonroot/backup.db`), then move the copy somewhere else. Copying the file alone is not enough while Allstill runs.
- **The database and every copy are secrets.** They hold every check's ping URL and the URLs Allstill fetches, with any token in them ([ADR 0004](adr/0004-what-a-check-fetches.md)): give a health endpoint a token that can only read its health. With Web Push they also hold the key Allstill signs phone alerts with and where each phone is reached ([ADR 0003](adr/0003-web-push-secrets.md)): whoever has a copy can ping any check, and can send every subscribed phone a notification that looks like Allstill's. `allstill backup` writes a copy only its owner can read; keep copies that way. Never serve a copy with alerts on, or it alerts everyone a second time.
- **Upgrades.** Before a new version changes the database, it copies it next to itself, as `allstill.db.before-0003` for example. Delete the copy once the new version works.
- **History.** Each check keeps its newest 1000 runs, and the output of its newest 100; `-keep-runs` and `-keep-output` change that.
- **The log.** Allstill keeps a record of what happened to checks and what was done, for 400 days; `-keep-log` (`ALLSTILL_KEEP_LOG`) changes that, to at least 30 days. See [What the log records](#what-the-log-records).
- **A changed version.** Every page links to the source of the version that runs, as the AGPL asks: a release links its own tag in the official repository. Running Allstill with changes of your own? Point `-source-url` (`ALLSTILL_SOURCE_URL`) at where your changed source is.
- **Watch Allstill itself** from somewhere else, such as one check at another service that Allstill's host pings. `GET /health` answers 200 while Allstill keeps up with sending and 503 when it has fallen more than two minutes behind, cannot read its database, or is offline for the checks it runs (its canary does not answer), so an outside monitor can watch the sending too; it needs no sign-in and says nothing about any check.

## Check a release

Each release has a `checksums.txt`, signed by the release workflow with cosign, and GitHub attestations for every file and image (releases made before the repository was public have neither):

```sh
cosign verify-blob \
  --certificate-identity 'https://github.com/MartijnMul/allstill/.github/workflows/release.yml@refs/tags/v1.2.3' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --bundle checksums.txt.sigstore.json checksums.txt
sha256sum --check --ignore-missing checksums.txt
gh attestation verify --owner MartijnMul allstill_1.2.3_linux_amd64.tar.gz
gh attestation verify --owner MartijnMul oci://ghcr.io/martijnmul/allstill:1.2.3
cosign verify ghcr.io/martijnmul/allstill:1.2.3 \
  --certificate-identity 'https://github.com/MartijnMul/allstill/.github/workflows/release.yml@refs/tags/v1.2.3' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```
