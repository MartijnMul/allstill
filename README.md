# Allstill

Is it all still running? Allstill watches your cron jobs, backups, scripts and websites, tells you whether they ran and whether they actually worked, and keeps it calm: live status in your macOS menu bar, a digest in the morning, and your phone only when you need to act now.

> **Status: early development.** Not released yet; [docs/running.md](docs/running.md) describes how it will be installed and run. The architecture is in [ADR 0001](docs/adr/0001-stack-and-architecture.md), the API in [`api/`](api/README.md).

## What it does

- **Dead man's switch.** Jobs ping Allstill when they run; when a ping does not arrive on time, you know. The ping URLs follow Healthchecks.io, so moving over means changing one base URL.
- **Did it work, not only did it run.** A ping carries values, such as the size of a backup or the length of a queue, and rules decide: a threshold, compared with the previous run, or how fresh something is.
- **Checks of its own.** HTTP checks on status codes and JSON fields, TCP checks on open ports, and TLS certificate expiry. Inside networks the server cannot reach, a probe runs them, with nothing to open in that network.
- **Calendars.** Working hours, public holidays and maintenance windows decide whether a check counts and how urgent it is.
- **Calm alerts.** One incident per cause, not ten alerts. A phone alert only for what blocks something now, everything else in the morning digest.
- **Always in view.** A native macOS menu bar app, a dashboard and status pages.

## How it runs

One Go binary and one SQLite file. No database server, no cloud account. The same binary is the server, the job wrapper (`allstill run -- ./backup.sh`) and the probe. It comes as a Linux package, a container image or a plain archive; [docs/running.md](docs/running.md) explains how to set it up.

## Licence

Allstill is open source under the [GNU Affero General Public License, version 3](LICENSE). The API description and the client libraries generated from it are under the Apache License 2.0, so you can build on the API freely. Contributions are welcome after signing the [contributor licence agreement](CLA.md) once; [ADR 0002](docs/adr/0002-licence-and-contributor-agreement.md) explains why.

The name Allstill belongs to Martijn Mullié. Forks are welcome under another name.
