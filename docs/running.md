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

Allstill does not send alerts yet. It runs in shadow mode: when a check goes down, it decides what it would tell whom, and when, and lists that under **Messages** in the dashboard. Compare that with the alerts you get elsewhere, from Healthchecks with `allstill compare healthchecks`; Web Push to your phone and email follow ([intent/alerts](../intent/alerts/spec.md)).

- Everyone who gets alerts for a project is told once per incident, and again when it is resolved. Owners and editors get them, and so do workspace administrators, for every project.
- New people have quiet hours from 22:00 to 07:00 in their time zone. An alert waits for the morning unless its check is critical.
- After Allstill was down, or its host slept or its clock jumped forward, checks whose deadline passed meanwhile become unknown, not down, and nobody is alerted for them.
- `allstill admin set-alerting -project Backups -check nightly -urgency digest` sets how fast a check's problems reach people: `now`, `digest` (the daily digest at 08:00) or `none`. `-critical` breaks through quiet hours.

## Keep it

- **Backups.** Copy the database while it runs with `sudo -u allstill allstill backup -db /var/lib/allstill/allstill.db /var/lib/allstill/backup-$(date +%F).db` (in the container: `docker exec allstill allstill backup /home/nonroot/backup.db`), then move the copy somewhere else. Copying the file alone is not enough while Allstill runs.
- **Upgrades.** Before a new version changes the database, it copies it next to itself, as `allstill.db.before-0003` for example. Delete the copy once the new version works.
- **History.** Each check keeps its newest 1000 runs, and the output of its newest 100; `-keep-runs` and `-keep-output` change that.
- **Watch Allstill itself** from somewhere else, such as one check at another service that Allstill's host pings.

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
