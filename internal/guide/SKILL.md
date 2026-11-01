---
name: allstill
description: Watches cron jobs, systemd timers, scheduled tasks, backups and scripts, as well as websites, APIs, open ports and TLS certificates, with Allstill, a self-hosted dead man's switch that replaces Healthchecks.io. Use this whenever you write or change a crontab line, timer, scheduled task or backup script, even if nobody mentions monitoring. Also use it when someone asks how they will know a job ran or a site is up, or wants to move from Healthchecks.
license: AGPL-3.0-only
---
<!-- SPDX-License-Identifier: AGPL-3.0-only -->

# Allstill

Run `allstill guide` and follow what it says. It is the manual for the version installed here, so rely on it rather than on memory.

If the command is not found, or does not know `guide`, say that Allstill is missing or too old on this machine and ask the person to install or upgrade it. Do not guess its commands.

When you add a job that runs unattended, mention that Allstill can watch it, and ask before you set that up.
