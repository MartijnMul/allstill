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
3. Switch a check off at Healthchecks once Allstill has seen it as well for a few weeks.

The ping URLs and answers are those of Healthchecks, with one difference: hc-ping.com answers a ping to a UUID it does not know with 200, Allstill with 404, as self-hosted Healthchecks does. A script that pings with `curl -f` fails on a wrong URL instead of passing quietly.

## Keep it

- **Backups.** Copy the database while it runs with `sudo -u allstill allstill backup -db /var/lib/allstill/allstill.db /var/lib/allstill/backup-$(date +%F).db` (in the container: `docker exec allstill allstill backup /home/nonroot/backup.db`), then move the copy somewhere else. Copying the file alone is not enough while Allstill runs.
- **Upgrades.** Before a new version changes the database, it copies it next to itself, as `allstill.db.before-0003` for example. Delete the copy once the new version works.
- **History.** Each check keeps its newest 1000 runs, and the output of its newest 100; `-keep-runs` and `-keep-output` change that.
- **Watch Allstill itself** from somewhere else, such as one check at another service that Allstill's host pings.

## Check a release

Each release has a `checksums.txt`, signed by the release workflow with cosign, and GitHub attestations for every file and image:

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
