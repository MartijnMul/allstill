# Allstill API

[`openapi.yaml`](openapi.yaml) describes everything a client can do with Allstill: the pings jobs send, and the JSON API the menu bar app, the CLI, agents and integrations use. It is the source of truth; the Go server and the Swift client are generated from it ([ADR 0001](../docs/adr/0001-stack-and-architecture.md)).

This directory is licensed under the [Apache License 2.0](LICENSE), unlike the rest of Allstill, so anyone can build a client or an integration without the AGPL applying to it ([ADR 0002](../docs/adr/0002-licence-and-contributor-agreement.md)).

## What it covers

**Pings** follow Healthchecks.io, so a job moves over by changing the base URL:

```sh
curl -fsS -m 10 --retry 3 https://ping.example.com/ping/5f3e…/start
./backup.sh
curl -fsS -m 10 --retry 3 "https://ping.example.com/ping/5f3e…/$?"
```

or, with the wrapper that does all of that and more: `allstill run --url https://ping.example.com/ping/5f3e… -- ./backup.sh`.

A job that starts again within seconds of its end should add a `rid` to both pings: without one, Allstill can take its start for one that its previous end overtook, and that run is not tracked. The wrapper always sends one.

Beyond Healthchecks, a ping can carry values for a check's rules: `v=name:number` in the query, repeatable (`/ping/5f3e…?v=size:18400000000&v=rows:312`), or an `Allstill-Values` header holding a Structured Field dictionary (RFC 9651), such as `size=18400000000, rows=312`.

**The JSON API** lives under `/api/v1` and needs a bearer token: an OAuth access token from the CLI or a project API key (a probe has a key of its own, for its two endpoints). The token's scope (`read`, `manage`) and the caller's role in each project (viewer, editor, owner) decide what it may do. It never accepts a browser's cookie.

**Signing in** from a command line is the device grant of RFC 8628, at `/oauth/device_authorization` and `/oauth/token`, for one client only: someone approves a short code with a passkey in any browser. The token that comes back starts with `alt_`, and there is no refresh token.

## Conventions

- OpenAPI 3.1. Both generators handle 3.1; 3.2 is not used until they do.
- JSON field names in `snake_case`; times in RFC 3339, UTC; durations in whole seconds.
- A field without a value is left out, never `null`. The one exception is a JSON Merge Patch (RFC 7396), where `null` returns a setting to its default, which only `setAlertSettings` takes.
- Identifiers are opaque strings and never secrets. A check's ping URL is a secret: nothing reading a check carries it, and only three operations answer with one, for the `manage` scope.
- Numbers are `double` and durations `int64`, so large values such as backup sizes in bytes stay exact.
- Errors are Problem Details (RFC 9457), with `errors[].pointer` for invalid fields.
- Lists are paged with an opaque `cursor` and a `next_cursor`, at most 200 to a page.
- Changes use `If-Match` with the `ETag` that was read. A client does not repeat a write by itself: there is no `Idempotency-Key`.
- Each credential has a limit on requests a minute, answered with 429 and `Retry-After`.
- What the description holds and is not served yet says so in its `summary`, and a test holds that to what is served.
- `/events` is a Server-Sent Events stream. Each event's `data` is a `StreamEvent`. OpenAPI 3.1 cannot attach a schema to the items of a stream, so clients decode the data with that schema themselves, and `redocly.yaml` allows it to be unreferenced.

## Changing the API

- Within `/api/v1`, changes are additive only: new endpoints, new optional fields, new values in an enum that clients are told to expect. Anything else is `/api/v2`.
- While the API has no clients outside this repository, a change that is not additive is allowed when its spec decides so, and noted here. So far: `none` among an incident's urgencies ([intent/alerts](../intent/alerts/spec.md), C9), and in 0.6.0 the unused `certificate` check kind removed, with `CheckKind` now telling clients to expect new kinds, and `offline` among the statuses of `/health` ([intent/http-checks](../intent/http-checks/spec.md), R14); in 0.10.0 `quiet_hours`, `digest_time` and a project's `alerts` left `Me`, as nothing serves them, and its `email` lost `format: email`, as an address is stored as it was typed; in 0.11.0 what was promised and is not built left the description, as [intent/cli-anywhere](../intent/cli-anywhere/spec.md) says: `Check.ping_url` on reads, `Idempotency-Key`, `Status`'s `event_id`, `open_incidents` and `upcoming`, `getRun`, reading and acknowledging incidents, calendars, rules, values and dependencies (its C9). In 0.12.0 `updateCheck` stopped being a JSON Merge Patch: it takes the fields to change, with `http` or `tcp` as a whole, as the generated types cannot tell `null` from a field left out, and `createCheck` lost its 409. In 0.13.0 nothing left: a project gained `team` and `Me` gained `teams`, and `workspace_admin` no longer means that the person sees every project ([intent/teams](../intent/teams/spec.md), R8). In 0.14.0 a message's `kind` gained `notice`. In 0.15.0 nothing left: a team's owners make, rename and delete projects, change and end roles and make and withdraw invitations, a project's owners read who can see it, and everyone sets whether they are told (`alerts` on a project). With `RoleToGive` beside `Role`, Go code generated from it names `Role`'s values `RoleViewer` and the rest.
- Bump `info.version` (semantic versioning) with every change.
- Run `make api-lint` before committing.

## Generating code

The Go server generates the JSON API only (the ping endpoints are written by hand, for speed and Healthchecks compatibility, and so is `/health`, which is served at the root like them), with `oapi-codegen` 2.8 or later and these options:

```yaml
generate:
  std-http-server: true
  strict-server: true
  models: true
output-options:
  exclude-tags: [Ping, Health, OAuth]
  skip-prune: true # keeps StreamEvent, which no path refers to
```

For now it generates what is served, by a named list of operations (`include-operation-ids`, in [`internal/api`](../internal/api/oapi-codegen.yaml)), since one tag can hold operations that are served and others that are not. The command line's client is generated from the same description, into [`internal/apiclient`](../internal/apiclient/oapi-codegen.yaml), under this directory's licence. It lists the same operations, apart from the probe's, which a test holds to. `/oauth/*` is left out of both, as a standard defines it.

The Swift client uses Apple's `swift-openapi-generator`:

```yaml
generate: [types, client]
filter:
  tags: [Account, Status, Projects, Teams, Invitations, Checks, Probes, Log, Events, Alerts, Health]
  schemas: [StreamEvent]
namingStrategy: idiomatic
accessModifier: public
```

## Not in v1 yet

- Reading, acknowledging and snoozing incidents, calendars and maintenance windows come with their own changes. An administrator listing people and removing a person ([intent/cli-anywhere](../intent/cli-anywhere/spec.md), R28 to R30) is not in it yet. Naming, inviting, making less or taking out a team's owner stays in the dashboard, signed in with a passkey ([intent/teams](../intent/teams/plan-3.md)), and accepting an invitation is done in a browser. A person's own devices and messages are in it (tag `Alerts`), and not served yet.
- Status pages.
- The Healthchecks management API (`/api/v3`). Moving over uses `allstill import healthchecks`, which reads it.
