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
4. Add yourself. Allstill prints a link that works once, for a day, where you make a passkey:

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

1. Copy a project's checks: `HEALTHCHECKS_API_KEY=… allstill import healthchecks -project "Backups"` (add `-url` for a self-hosted Healthchecks). Each check keeps its UUID, so a job that pings by UUID only changes the host. A job that pings by slug needs the project's new ping key as well, printed once when the import creates the project: Healthchecks does not hand out its own.
2. Let jobs report to both for a while: `allstill run --url https://hc-ping.com/… --url https://allstill.example.com/ping/… -- ./backup.sh`, or two space-separated URLs in `ALLSTILL_URL`.
3. Compare: `HEALTHCHECKS_API_KEY=… allstill compare healthchecks -since 2026-10-01` lists, per check, where Allstill saw it go down or come back up differently from Healthchecks: missed, extra, or more than five minutes apart (`-within`). Give `-since` the day the jobs began to ping both. Healthchecks keeps about three months of this history, and counts a check it paused as down until its next ping. It matches checks by UUID, so a project split over several in Allstill compares as one, and lists the checks Allstill never heard from, as they cannot alert, with when Healthchecks last did. One difference is by design: a start that arrives after its end with the same `rid` joins that run in Allstill, where Healthchecks waits for another end and reports the check down.
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
- The overview says when alerts may not reach you: a message to you failed in the last day, a device needs you, Allstill is behind with sending, or nobody gets alerts for a project you see.
- Serving a copy of the database? Start it with `-shadow` (`ALLSTILL_SHADOW=1`): it then sends no alerts, whatever the copy says.

- Everyone who gets alerts for a project is told once per incident, and again when it is resolved. Owners and editors get them, and so do workspace administrators, for every project.
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

## Keep it

- **Backups.** Copy the database while it runs with `sudo -u allstill allstill backup -db /var/lib/allstill/allstill.db /var/lib/allstill/backup-$(date +%F).db` (in the container: `docker exec allstill allstill backup /home/nonroot/backup.db`), then move the copy somewhere else. Copying the file alone is not enough while Allstill runs.
- **The database and every copy are secrets.** They hold every check's ping URL, and with Web Push the key Allstill signs phone alerts with and where each phone is reached ([ADR 0003](adr/0003-web-push-secrets.md)): whoever has a copy can ping any check, and can send every subscribed phone a notification that looks like Allstill's. `allstill backup` writes a copy only its owner can read; keep copies that way. Never serve a copy with alerts on, or it alerts everyone a second time.
- **Upgrades.** Before a new version changes the database, it copies it next to itself, as `allstill.db.before-0003` for example. Delete the copy once the new version works.
- **History.** Each check keeps its newest 1000 runs, and the output of its newest 100; `-keep-runs` and `-keep-output` change that.
- **Watch Allstill itself** from somewhere else, such as one check at another service that Allstill's host pings. `GET /health` answers 200 while Allstill keeps up with sending and 503 when it has fallen more than two minutes behind or cannot read its database, so an outside monitor can watch the sending too; it needs no sign-in and says nothing about checks.

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
