# Allstill

Is it all still running? Allstill watches your cron jobs, backups, scripts and websites, tells you whether they ran and whether they actually worked, and keeps it calm: live status in your macOS menu bar, a digest in the morning, and your phone only when you need to act now.

> **Status: design.** Nothing to install yet. The architecture is in [ADR 0001](docs/adr/0001-stack-and-architecture.md).

## What it does

- **Dead man's switch.** Jobs ping Allstill when they run; when a ping does not arrive on time, you know. The ping URLs follow Healthchecks.io, so moving over means changing one base URL.
- **Did it work, not only did it run.** A ping carries values, such as the size of a backup or the length of a queue, and rules decide: a threshold, compared with the previous run, or how fresh something is.
- **Checks of its own.** HTTP checks on status codes and JSON fields, and TLS certificate expiry. Inside networks the server cannot reach, a probe runs them.
- **Calendars.** Working hours, public holidays and maintenance windows decide whether a check counts and how urgent it is.
- **Calm alerts.** One incident per cause, not ten alerts. A phone alert only for what blocks something now, everything else in the morning digest.
- **Always in view.** A native macOS menu bar app, a dashboard and status pages.

## How it runs

One Go binary and one SQLite file. No database server, no cloud account. The same binary is the server, the job wrapper (`allstill run -- ./backup.sh`) and the probe.

## Licence

Allstill is meant to be open source under the GNU Affero General Public License. The licence is not in place yet.
