# Allstill API

[`openapi.yaml`](openapi.yaml) describes everything a client can do with Allstill: the pings jobs send, and the JSON API the menu bar app, the CLI, agents and integrations use. It is the source of truth; the Go server and the Swift client are generated from it ([ADR 0001](../docs/adr/0001-stack-and-architecture.md)).

This directory is licensed under the [Apache License 2.0](LICENSE), unlike the rest of Allstill, so anyone can build a client or an integration without the AGPL applying to it ([ADR 0002](../docs/adr/0002-licence-and-contributor-agreement.md)).

## Two surfaces

**Pings** follow Healthchecks.io, so a job moves over by changing the base URL:

```sh
curl -fsS -m 10 --retry 3 https://ping.example.com/ping/5f3e…/start
./backup.sh
curl -fsS -m 10 --retry 3 "https://ping.example.com/ping/5f3e…/$?"
```

or, with the wrapper that does all of that and more: `allstill run --url https://ping.example.com/ping/5f3e… -- ./backup.sh`.

A job that starts again within seconds of its end should add a `rid` to both pings: without one, Allstill can take its start for one that its previous end overtook, and that run is not tracked. The wrapper always sends one.

Beyond Healthchecks, a ping can carry values for a check's rules: `v=name:number` in the query, repeatable (`/ping/5f3e…?v=size:18400000000&v=rows:312`), or an `Allstill-Values` header holding a Structured Field dictionary (RFC 9651), such as `size=18400000000, rows=312`.

**The JSON API** lives under `/api/v1` and needs a bearer token: a project API key, or an OAuth access token from the menu bar app or the CLI. The token's scope (`read`, `manage`) and the caller's role in each project (viewer, editor, owner) decide what it may do.

## Conventions

- OpenAPI 3.1. Both generators handle 3.1; 3.2 is not used until they do.
- JSON field names in `snake_case`; times in RFC 3339, UTC; durations in whole seconds.
- A field without a value is left out, never `null`. The one exception is a JSON Merge Patch (RFC 7396), where `null` returns a setting to its default.
- Identifiers are opaque strings and never secrets. A check's ping URL is a secret, and only the `manage` scope sees it.
- Numbers are `double` and durations `int64`, so large values such as backup sizes in bytes stay exact.
- Errors are Problem Details (RFC 9457), with `errors[].pointer` for invalid fields.
- Lists are paged with an opaque `cursor` and a `next_cursor`.
- Changes use `If-Match` with the `ETag` that was read; creating accepts an `Idempotency-Key`.
- `/events` is a Server-Sent Events stream. Each event's `data` is a `StreamEvent`. OpenAPI 3.1 cannot attach a schema to the items of a stream, so clients decode the data with that schema themselves, and `redocly.yaml` allows it to be unreferenced.

## Changing the API

- Within `/api/v1`, changes are additive only: new endpoints, new optional fields, new values in an enum that clients are told to expect. Anything else is `/api/v2`.
- While the API has no clients outside this repository, a change that is not additive is allowed when its spec decides so, and noted here. So far: `none` among an incident's urgencies ([intent/alerts](../intent/alerts/spec.md), C9), and in 0.6.0 the unused `certificate` check kind removed, with `CheckKind` now telling clients to expect new kinds, and `offline` among the statuses of `/health` ([intent/http-checks](../intent/http-checks/spec.md), R14).
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
  exclude-tags: [Ping, Health]
  skip-prune: true # keeps StreamEvent, which no path refers to
```

The Swift client uses Apple's `swift-openapi-generator`:

```yaml
generate: [types, client]
filter:
  tags: [Account, Status, Projects, Checks, Runs, Incidents, Calendars, Events, Alerts, Health]
  schemas: [StreamEvent]
namingStrategy: idiomatic
accessModifier: public
```

## Not in v1 yet

- Managing projects, members, calendars, maintenance windows and keys through the API; the dashboard does that. A person's own devices and messages are in it (tag `Alerts`).
- Status pages and probes.
- The Healthchecks management API (`/api/v3`). Moving over uses `allstill import healthchecks`, which reads it.
