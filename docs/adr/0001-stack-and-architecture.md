# 0001. Stack and architecture

- Status: Accepted; licence decided by [0002](0002-licence-and-contributor-agreement.md)
- Date: 2026-10-20

## Context

Allstill tells you whether your cron jobs, backups, scripts and websites are still running, and whether they actually worked. It shows that calmly: live in the macOS menu bar, once a morning in a digest, and on your phone only when you need to act now. It replaces Healthchecks.io and does more: values with rules, checks it runs itself, calendars, run-time anomalies and incidents that group checks by cause.

The forces behind this decision:

- **Self-hosted, for a long life.** One binary and one data file that run for months on a small VPS, without a database server or a cloud account.
- **Runs everywhere a job runs.** The CLI that wraps jobs must run on Linux servers, macOS and small ARM boards, and must never block or change the job it wraps.
- **Trustworthy green.** Green must be proven green. When Allstill does not know, it says so; when Allstill itself was down, checks become unknown, not down.
- **Calm.** Alerts are only worth something if they are rare enough to be taken seriously. Noise is a defect.
- **One operator, many parties.** A typical user runs checks for their own company, for customers and for a home lab, often on shared infrastructure: one host or one private network serves several of them. When that host fails, they want one alert, not one per party. Other people need narrower access: a colleague manages one customer's checks, someone at a customer only wants to see that their service works.
- **One person, one stream.** Phone alerts, quiet hours and the morning digest belong to a person, across everything they watch.
- **Compatible.** Moving from Healthchecks must not require rewriting scripts.
- **Open source.** Every dependency needs a licence compatible with the AGPL, and the supply chain must be small enough to review.
- **A small team, building with AI assistants.** Few moving parts, a strict toolchain, and everything testable from the terminal.

## Decision

### Components

| Part | Choice |
| --- | --- |
| Language | Go, current stable release, pinned in `go.mod` |
| Binary | One static binary: `allstill serve`, `allstill run`, `allstill probe` and the admin commands |
| Data | SQLite in WAL mode via `modernc.org/sqlite` (pure Go, so the binary stays static); queries with `sqlc`; numbered SQL migrations of our own |
| Backups | `allstill backup` writes a consistent snapshot with SQLite's online backup; continuous replication with Litestream is documented as an option, not built in |
| Scheduling | Every deadline is a row in SQLite with an index; one timer waits for the earliest. No timer or goroutine per check |
| Cron | Our own evaluator behind an interface, with the semantics and daylight-saving test cases of Healthchecks' `cronsim` (BSD-3, credited) |
| HTTP | Go standard library: `net/http` with its routing patterns, `http.CrossOriginProtection` against CSRF |
| Dashboard | Server-rendered with `html/template`; a small amount of plain JavaScript as ES modules for live updates and Web Push; no npm and no build step |
| Live updates | Server-Sent Events with `Last-Event-ID`, replayed from an event log in SQLite; a gap too old to replay sends a reset that makes the client fetch a fresh snapshot |
| API | A versioned JSON API under `/api/v1`, described by one OpenAPI document that is the source of truth. The Go server uses types generated from it with `oapi-codegen`; the macOS app uses Apple's `swift-openapi-generator`. The OpenAPI version is the newest both generators support; CI checks the generated code is current |
| Phone alerts | Web Push (RFC 8030), with message encryption (RFC 8291) and VAPID (RFC 8292) implemented on the Go standard library (`crypto/ecdh`, `crypto/hkdf`, AES-GCM, ES256), tested against the RFCs' test vectors. Declarative Web Push where the browser supports it, a service worker elsewhere |
| Other channels | SMTP for email; Telegram, Pushover, ntfy and signed webhooks later, each behind one `Channel` interface. A channel is either a person's own (their phone, their email) or shared (a team chat), see below |
| Human login | Passkeys (`go-webauthn`) with recovery codes; server-side sessions in a `__Host-` cookie |
| Native clients | A minimal OAuth 2.1 authorisation server built in, for Allstill's own clients only: authorisation code with PKCE for the menu bar app (`ASWebAuthenticationSession`), the device authorisation grant (RFC 8628) for the CLI. No other grants, no third-party clients, no dynamic registration. Opaque tokens, stored hashed, revocable per device |
| macOS app | Swift 6 and SwiftUI, AppKit where SwiftUI falls short; logic in a Swift package tested with Swift Testing; project generated with XcodeGen; macOS 26 or later; sandboxed, notarised, updated with Sparkle. Signs in to several installations side by side, as a mail app holds several accounts |
| Delivery | GoReleaser: binaries for Linux (amd64, arm64, armv7, armv6), macOS and Windows, a container image on a distroless base, Linux packages and a Homebrew tap; signed with cosign, with SBOMs and GitHub artifact attestations |
| Quality | `gofmt`, `go vet`, `staticcheck`, tests with the race detector, `testing/synctest` for everything that waits, fuzzing of every parser of untrusted input, `govulncheck`, `gitleaks`, `zizmor` on the workflows |

Go tools are `tool` directives in `go.mod`, not global installs. GitHub Actions are pinned by commit SHA. Dependencies are proposed by Dependabot only after they have been out for two weeks.

### One core, thin edges

The core (checks, pings, rules, calendars, incidents, alerting) has no HTTP code. The dashboard, the JSON API, the ping endpoints, the SSE stream and the CLI are thin layers over it. `cmd/allstill` is only wiring; logic lives in `internal/` packages.

### Pings and checks

- The ping endpoints follow Healthchecks: `/ping/{uuid}`, `/start`, `/fail`, `/{exit-code}`, `/log`, `?rid=` to pair start and finish, and slug pings per project with `?create=1`. The body is stored as the run's log, up to a size limit.
- A ping may carry values (`?v.size=4.2GB`, or a JSON body); rules on a check judge values, run time and counts.
- Check UUIDs are version 4: the ping URL is the secret, and it can only ping.
- Ping ingest can be served on its own host name or path. Cookies are never sent to it.
- `allstill run -- cmd` reports start, exit code, run time and the tail of the output. It passes signals through, never changes the job's exit code, and gives up on Allstill after a short timeout, so an unreachable server never blocks a job. During a move from Healthchecks it can report to both.
- `allstill probe` runs inside a network Allstill cannot reach. It connects out to the server, fetches the HTTP checks assigned to it, runs them and pushes the results; it opens no port.
- Everything the server fetches itself (HTTP checks, webhooks) goes through one client that refuses private, loopback and link-local addresses. An administrator can allow chosen private ranges for an installation that runs inside its own network; checks in networks the server cannot reach run on a probe.

### Workspace, projects, people and keys

An installation is one workspace. Projects are the unit of access, as in Healthchecks; what several projects share lives in the workspace. Organisations that must not see each other at all run separate installations, which costs one binary and one file.

```
Installation (one workspace)
├── People      accounts, each with their own devices, channels and quiet hours
├── Shared      calendars, probes, shared channels, status pages
└── Project     members with a role, keys, alert routing
    └── Check   pings, runs, rules; may depend on checks in other projects
```

- **Projects carry access.** A person is a member of any number of projects, as owner (members, keys, deleting), editor (checks, rules, routing) or viewer (read only). Workspace administrators manage people, shared resources and settings, and see every project.
- **Dependencies cross projects.** A check may depend on a check in another project, such as a host that serves several customers. When the host fails, that is one incident. A member who cannot see the cause is told the cause lies outside their projects, without its details.
- **Alerts belong to people.** A person's own channels (Web Push to their devices, their email), their quiet hours and their digest time apply across every project they follow. A project's routing says who is alerted for it, how urgently and when; it can also alert a shared channel, such as a team chat. Teams are routing, not access; named groups of people may come later as a shortcut for adding several people at once.
- **Two clocks.** A check's calendar (working hours, holidays, maintenance) says when it counts and when it is urgent. A person's quiet hours say when they may be disturbed. Only a check marked critical breaks through quiet hours.
- **Machine credentials belong to a project or the workspace, never to a person,** so a job keeps working when someone leaves:
  - check UUIDs, which can only ping;
  - a ping key per project, for slug pings;
  - API keys per project, with a scope (ping, read, manage);
  - a key per probe, in the workspace; a probe runs the checks of any project assigned to it.
- **User credentials are per device:** a browser session, or an OAuth token for the menu bar app or the CLI, each listed and revocable.
- **People without an account** see a status page through a public URL or a share link that is read only, expiring and revocable. A status page is built from services, each a group of checks, and may draw from several projects.
- **Every query is scoped.** The store hands out a handle scoped to what the caller may see: the projects of a person or a key, and the workspace. Tests check that no handler returns data from a project the caller is not in.
- **A workspace key in every table.** There is one workspace per installation, but every table carries its id from the first migration, so a shared hosted service would not mean touching every query.
- **The first user** is created from the command line (`allstill admin create-user`), which prints a link to register a passkey, and becomes workspace administrator.

All keys and tokens are stored as hashes, except check UUIDs, which the dashboard shows so they can be copied.

The same authorisation server can later serve MCP clients, which need OAuth 2.1, so an agent can read a project's status with a token the user grants and can revoke.

## Alternatives considered

- **TypeScript on Node or Bun.** Closer to other web projects, but npm brings the most dependency churn and supply-chain risk, and a long-running service with thousands of deadlines is a better fit for Go's runtime and compatibility promise. Rust: a strong runtime, but slower to build ordinary web screens, and a smaller pool of contributors.
- **PostgreSQL.** Better for several instances and very high volumes. Rejected: a separate server to run for a self-hosted, single-instance service. Reconsider if a hosted, shared installation is built.
- **A React single-page dashboard** (Vite, TanStack, shadcn/ui). It would bring npm and a build step into a project that otherwise has neither, for a dashboard whose main jobs are lists, forms and live status. The menu bar app is the primary interface. Revisit when the dashboard becomes app-like, such as a drag-and-drop editor.
- **WebSockets.** Two-way, which nothing needs. SSE reconnects by itself and replays from `Last-Event-ID`.
- **A Web Push library.** The Go libraries are small and lightly maintained; the protocol is three RFCs with test vectors and fits in the standard library. Owning it keeps the dependency list short.
- **A native iOS app with APNs.** The richest notifications, but Apple ties push to one key per app, so every self-hosted installation would depend on a push relay run by the project. Web Push needs no relay and no developer account. Reconsider if Web Push proves unreliable for urgent alerts.
- **Native passkeys in the macOS app.** They need an associated domain fixed in the app, which self-hosted installations with their own domains cannot have. A browser sign-in through `ASWebAuthenticationSession` works for every installation.
- **JWTs.** Self-contained, but hard to revoke. Opaque tokens checked against a hashed row cost one indexed lookup.
- **One user per installation.** Simpler at first, but a colleague or a customer who needs access is common from the start, and adding access control later means touching every query.
- **Organisations as hard boundaries inside one installation**, as in Grafana. Shared infrastructure then cannot be modelled: a host that serves two organisations gives two incidents, a probe needs a key per organisation, and a person's alerts split per organisation. Grafana's users often avoid its organisations for this reason. Parties that must be fully separate run separate installations.
- **Teams as an access concept.** The real difference between people in a small team is who gets alerted, not what they may see. Routing per project covers that without a permission system.
- **Channels per project only.** Web Push is tied to a person's devices, and quiet hours are a person's; making each project hold its own phone channel would alert one person once per project.
- **An existing cron library.** Go's cron libraries differ in how they handle daylight-saving gaps and overlaps, and Healthchecks compatibility needs its exact behaviour. An evaluator of our own, tested against `cronsim`'s cases, is small.
- **Litestream built in.** Valuable, but an external process with its own storage credentials. A built-in snapshot covers the common case; Litestream stays a documented option.

## Consequences

Easier:

- **Running it.** One binary, one database file, no database server, no external accounts. A container, a Linux package or a Homebrew formula.
- **Moving from Healthchecks.** Same URLs, an importer, and `allstill run` reporting to both during the switch.
- **Reviewing it.** No npm, few Go dependencies, contextual escaping by default, no passwords.
- **Testing it.** A real database in memory, a fake clock through `testing/synctest`, and the RFCs' and `cronsim`'s test vectors.
- **Sharing with others.** Colleagues get a project, customers a status page or a share link, without a permission system to learn.
- **Moving from Healthchecks.** Its projects map one to one onto Allstill's.
- **A hosted service later.** Scoped queries and a workspace key in every table are there from the start.

Harder, or ours to look after:

- **Our own implementations** of Web Push encryption, a cron evaluator and an OAuth authorisation server must be right. Each is small, specified and tested against published vectors, but each is security- or correctness-critical.
- **A single instance.** SQLite means one writer. Availability depends on a fast restore and on watching Allstill from outside, not on failover.
- **Web Push on the iPhone** needs the dashboard added to the home screen once, and cannot break through every Focus mode the way a native critical alert could.
- **Two languages.** Go for the server and Swift for the macOS app; the OpenAPI document keeps them in step.
- **SSRF.** A server that fetches URLs on request needs strict address rules from the first HTTP check.
- **The ping URL is a secret.** Anyone who has it can ping that check. Docs must say so, and the dashboard must make rotating it easy.
- **Incidents across projects** must show each person only what they may see. The incident view, the digest and every alert are rendered per person, never once for all.
- **Several installations in one app.** The menu bar app keeps a separate sign-in, token and status per installation, and merges them only in what it shows.

## Not decided here

- The licence and the contributor licence agreement: a separate ADR. The intent is AGPL-3.0-only for the server, the CLI and the app, and Apache-2.0 for the OpenAPI document and generated client libraries, so others can integrate freely.
- Named groups of people.
- A hosted service, and whether it is one instance per customer or a shared installation.
- The exact retention of pings, runs and logs.
