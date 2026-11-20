<!-- SPDX-License-Identifier: AGPL-3.0-only -->
# Plan: use the command line to create, manage and do things, from anywhere

Intent: [intent.md](intent.md)
Spec: [spec.md](spec.md), accepted 2026-11-02
Status: accepted; steps 1 to 4 running on a real installation; step 5 for a team's owners built by [intent/teams, plan-3](../teams/plan-3.md) part C and running on a real installation since 2026-11-20, while R28 to R30 for an administrator, listing people and removing a person, stay open
Approved: 2026-11-02, by the maintainer; risk: higher risk (a sign-in and credentials of our own, rights opened to the network, the first real shape of the API, and a person removed for the first time)
Codebase: MartijnMul/allstill at 3b635b2

Five steps, each a release candidate that runs on a real installation before the next, and each worth having on its own. After step 1 a command in the wrong place can no longer pretend to work. After step 2 someone can sign in from their own machine and manage that sign-in, with nothing yet to do with it, so the riskiest part is proven alone. After step 3 they can see everything they may see. After step 4 they can manage checks, and scripts have keys. After step 5 they can do the rest, and the spec's success test can run.

## Files that change

Step 1, no database by accident:
- `internal/store/store.go` (changed): `OpenExisting`, which refuses a path holding nothing, an empty file or another program's SQLite file, and creates nothing and adds nothing to it. `Open` stays for `serve`.
- `cmd/allstill/admin.go`, `compare.go` (changed): `openStore`, which every command that uses the database but `serve` goes through, and `backup` use it, with the one message of R34. `serve` keeps `Open`.
- `cmd/allstill/*_test.go`, `internal/store/*_test.go` (changed): fixtures make their database first.
- `internal/guide/guide.md`, `docs/running.md` (changed): the warning about an empty `allstill.db` goes. `README.md` if it says otherwise.

Step 2, signing in:
- `internal/store/migrations/0014_person_tokens.sql` (new): `person_tokens` as the spec's design says, with a foreign key to the passkey it was approved with, cascading as sessions do.
- `internal/store/queries/tokens.sql` (new), `internal/store/internal/db` (generated), `internal/store/tokens.go` (new): making, finding by hash with last use written at most hourly, listing, ending; listing and ending a person's browser sessions by id; `RemovePasskey`. Each writes its log line.
- `internal/store/log.go`, `logread.go` (changed): `token.made`, `token.ended`, `session.ended`, `passkey.removed` in `logOwnKinds`; the sign-in's name in an `Actor` and in `details`, removed in `readLogEntry` where a person is masked. `docs/running.md` lists it among what the log keeps in this step.
- `internal/id/id.go` (changed): prefixes for sign-ins.
- `internal/store/tidy.go` (changed): clearing old sign-ins.
- `internal/web/device.go` (new): `/oauth/device_authorization`, `/oauth/token`, the grants waiting in memory with their limits, `GET` and `POST /device`, and the passkey ceremony of its own, with a purpose bound to the grant and scope, a check that the passkey is the signed-in person's, and a counter that went backwards refused and logged as sign-in does.
- `internal/web/templates/device.html` (new), `channels.html`, `static/app.css` (changed): entering a code, what is asking, and on **Devices** the sign-ins and browser sessions, with a way to end each.
- `internal/web/signin.go`, `web.go` (changed): the sign-in page can return to `/device`, to that address only, so it cannot be used to send someone elsewhere; the routes.
- `internal/web/channels.go` (changed): the new section on **Devices**.
- `internal/web/api.go` (new): the bearer credential turned into an `Access`, ignoring any cookie (R38), one table from each operation to the kinds of credential it takes, `getMe`, `endThisToken`. `internal/web/probe.go` (changed): its methods join one generated server.
- `api/openapi.yaml`, `api/README.md` (changed): the `Account` and `OAuth` tags, `endThisToken`, the next version, `getMe` gains the server's version.
- `internal/api/oapi-codegen.yaml` and generated code (changed): generation moves from tags to a named list of operations (`include-operation-ids`), since `Checks` holds reads and writes that arrive in different steps and a strict server must implement all it includes. Each later step adds its operations to the list. `Probe`'s remain.
- `internal/apiclient/` (new, generated), `Makefile` (changed): the client, with its own `oapi-codegen.yaml`, in a directory of its own under `Apache-2.0` with its own `LICENSE`, as ADR 0002 says of generated clients. `make generate` writes it with that header, and `generated-check` covers it. A test holds the two lists of operations equal, apart from the probe's, which only the server serves.
- `internal/client/` (new): the API client on `internal/apiclient`, the credentials file (R8, R9), refusing redirects, printing without control characters (R19), and the message for different versions (R45).
- `cmd/allstill/remote.go` (new), `main.go` (changed): `login`, `logout`, `whoami`, usage.
- `cmd/allstill/admin.go` (changed): `end-sessions`, `remove-passkey`.
- `docs/running.md`, `internal/guide/guide.md`, `AGENTS.md` (changed): signing in, how to end it, what to do with a stolen device, that a proxy in front must let `/oauth` and `/api` through to clients that are not browsers.

Step 3, reading:
- `api/openapi.yaml`, `api/README.md` (changed): what leaves (`Check.ping_url` on reads, `Idempotency-Key`, `Status`'s `event_id`, `open_incidents` and `upcoming`, `getRun`, incidents, calendars), the fields of features not built, the marking of what stays and is not served, the `Log` and `Probes` tags, `listLog`, `listProbes`, the sentence about the dashboard. The list of operations and generated code (changed).
- `internal/web/api.go` (changed): `getStatus`, `listProjects`, `getProject`, `listChecks`, `getCheck`, `listRuns`, `listLog`, `listProbes`; limits per credential (R42); paging.
- `internal/store/workspace.go` (changed): a `read` credential's `Access`, with `Admin` off and every project at viewer (R5).
- `internal/web/active.go` (changed): which of a check's fields are secret (`whole`) moves to a function the page and the API share.
- `internal/logtext/` (new), `internal/web/log.go` (changed): how an entry reads, moved so that page and command line word it alike; a kind it does not know says so.
- `internal/client/`, `cmd/allstill/remote.go` (changed): `status`, `projects`, `checks`, `checks show`, `checks runs`, `log`, `probes`, `-json`, `-read-only`.
- `internal/active/load_test.go` (changed): the load test also sends API requests (R43).
- `docs/running.md`, `internal/guide/guide.md`, `AGENTS.md` (changed): reading from anywhere; for AI assistants, a read-only sign-in.

Step 4, managing checks, and keys:
- `internal/store/migrations/0015_api_keys.sql` (new): keys as manage-checks designed, plus copies of who made one and the sign-in's name.
- `internal/id/id.go` (changed): a prefix for keys.
- `internal/store/queries/keys.sql` (new), `internal/store/keys.go` (new): a key as an `Access` holding one project, making, listing across projects, revoking, each with its log line, readers set in `logMinRole`; a key's names shown by the log's rules.
- `internal/store/manage.go`, `checks.go` (changed): `viaPerson` tests `Actor.Kind`, so a key is held to what a person is (R37).
- `internal/web/manage.go` (changed): the try and its limits move out for the API to share, per credential.
- `internal/web/api.go` (changed): `createCheck`, `updateCheck`, `deleteCheck`, `pauseCheck`, `resumeCheck`, `moveCheck`, `rotatePingUrl`, `getPingUrl`, `listKeys`, `createKey`, `revokeKey`.
- `api/openapi.yaml`, `api/README.md` (changed): those, the try's result, `moveCheck`, `getPingUrl`, the `Keys` tag; the list of operations and generated code.
- `cmd/allstill/checks.go` (changed): the flag parsing `admin add-check` and `set-check` use, made shareable.
- `internal/client/`, `cmd/allstill/remote.go` (changed): `checks add`, `change`, `pause`, `resume`, `move`, `delete`, `ping-url`, `keys`, and `ALLSTILL_TOKEN`, `ALLSTILL_SERVER`.
- `docs/running.md`, `internal/guide/guide.md`, `AGENTS.md` (changed): managing checks from anywhere, keys for scripts, treating ping URLs as leaked after a stolen sign-in (C2).

Step 5, projects and people:
- `internal/store/workspace.go`, `signin.go` (changed): `SetMember`, `RemoveMember`, `CreateProject`, `RenameProject`, `CreatePerson`, `CreateMember`, `CreatePasskeyLink` gain an `Access` and their rules (R24 to R28, R32); deleting a project on `projects.deleted_at`; removing a person on `people.disabled_at`, clearing the email; what R30 says.
- `internal/store/probe.go` (changed): `CreateProbe` and `RekeyProbe` also test that the caller is a command (R31).
- `internal/store/logread.go` (changed): removed people's ids read once per call, "a former member" (C6); `project.deleted`, `person.removed`.
- `internal/store/queries/` (changed): reads of members and people; any query reading `people` that a removed person must not pass.
- `internal/web/api.go` (changed): `createProject`, `updateProject`, `deleteProject`, `listMembers`, `setMember`, `removeMember`, `listPeople`, `createPerson`, `removePerson`, `createPasskeyLink`, `endSessionsOfPerson`.
- `api/openapi.yaml`, `api/README.md` (changed): those, the tags; the list of operations and generated code. `api/README.md` says `/api/v1` now only grows.
- `internal/client/`, `cmd/allstill/remote.go` (changed): `projects create`, `rename`, `delete`, `members`, `people`.
- `cmd/allstill/admin.go` (changed): `remove-person`.
- `docs/running.md`, `internal/guide/guide.md`, `README.md`, `AGENTS.md` (changed): the whole command line, what stays on the server and why, "Not here yet", the status sentence.
- `.goreleaser.yaml` (checked, changed only if needed): the command line reaches people's own machines with the first release's archives.

## Order of work

1. **No database by accident** (release candidate). No migration.
   1. `OpenExisting`, test-first: a missing path, an empty file and another program's database are refused and left as they were; a database a server has open opens.
   2. Every command, then the fixtures, then the docs.
   3. On a laptop: `admin create-project` refuses and leaves nothing. On the real installation: `admin` commands still work.
2. **Signing in** (release candidate). Migration, so it is tried on a copy of a real database first, and CI is waited for.
   1. The store: sign-ins, sessions, removing a passkey, test-first, including ending counting at once.
   2. The grants in memory and the two `/oauth` endpoints, test-first against RFC 8628's answers, with `synctest` for expiry and polling.
   3. The approval page and its ceremony, seen in a browser, at both widths and in dark mode. Tests hold what the spec's concerns ask for: a passkey of another person refused, an answer for one code refused for another, the code never read from the address.
   4. The bearer `Access`, `getMe`, the table of operations, and the probe's endpoints unchanged on one generated server.
   5. **Devices**: seeing and ending sign-ins and sessions.
   6. The client and `login`, `logout`, `whoami`; `admin end-sessions`, `remove-passkey`.
   7. An independent security pass on this step before its release candidate.
   8. On the real installation: sign in from a laptop, approving on a phone and on the laptop. End it on **Devices**, and the next command gets refused. Read the log lines.
3. **Reading** (release candidate). No migration.
   1. What leaves the description, with the probe still generated and working.
   2. A `read` credential's `Access`, and the shared rule for what is secret, test-first.
   3. The operations one tag at a time, each with its table of who gets what.
   4. Moving the log's wording, with the page's output unchanged.
   5. The commands, `-json`, and the printing.
   6. Limits, and the load test with API requests in it.
4. **Managing checks, and keys** (release candidate). Migration, tried on a copy first.
   1. `viaPerson` and the key's `Access`, test-first: a key is held to the counts and limits.
   2. Each write operation, against the form's rules.
   3. Sharing flag parsing and the try with what exists, without changing `admin` or the form.
   4. The commands, then keys.
   5. On the real installation: add a check for a test job from a laptop, run it with its ping URL, and see it go up. Make a `read` key and read with it. Revoke it.
5. **Projects and people** (release candidate). No migration, as `disabled_at` and `deleted_at` exist from 0001.
   1. The store's rules for each operation, test-first, per role.
   2. Removing a person, starting from a list of every query that reads `people`, deciding for each whether a removed person passes.
   3. "A former member", with a test per role and per field.
   4. The operations and commands.
   5. An independent security pass on the whole API before the release candidate, as `/api/v1` then only grows.
   6. R48: someone who has not used it signs in and connects a job in a few minutes, with a binary built for them, as release candidates build only one image.

## Risks

- **A sign-in of our own (step 2) is the riskiest step.** A mistake opens the installation. It is kept as small as RFC 8628 allows, proven alone with nothing behind it but `whoami`, and gets its own independent pass. Credentials are hashed, and the logic is covered test-first.
- **The ceremony (step 2).** It looks like sign-in and must not behave like it: sign-in's accepts any person's passkey and opens a session. It has its own code path, not a flag on sign-in's, and tests for both differences. Sign-in's own tests run unchanged.
- **Rights opened to the network (steps 3 to 5).** A handler that forgets a check leaks across projects. Rules live in the store, each operation has a table per role and for another project, and a test walks the generated list of operations and fails on one not in the table of credentials, so a new one cannot be open by accident.
- **Secrets on a new path (steps 3, 4).** A ping URL or whole address in a JSON answer is a leak with no page to see it on. A test gives a check a marker in its URL, settings and ping UUID, calls every read as a viewer and with a `read` credential, and asserts the marker is in no answer.
- **Pings slowed (all steps).** Anonymous requests write nothing, a credential's lookup costs reads, and last use is written hourly. `make load` before and after, with API requests from step 3.
- **Removing a person for the first time (step 5).** `disabled_at` has never been set, so anything that misses it fails silently, and it could keep telling someone or stop telling everyone. The list of queries in step 5.2 is written down in the commit. Removal is one transaction. A test removes the only member with alerts on and asserts who is told.
- **Changing workspace methods (step 5).** Adding an `Access` touches everything `admin` does. Every `admin` test runs unchanged, and each command is tried on a copy.
- **The API's shape freezes (step 5).** After this it only grows, so the second independent pass reads the description as a client would. Steps 2 to 4 may still change it.
- **A proxy in front.** One may block clients that are not browsers, or requests to new paths. Step 2's real try finds this, and the docs say what to allow.
- **Files not reaching people's machines.** Release candidates build one image, so the command line on a laptop is built from source until the first release. Nothing about that is new, but R48's test has to allow for it.
- **Size.** This is the largest change so far. Each step ends in something usable, so stopping after any step leaves a better state, not a half-built one.

## Options not taken

- **Keys first, sign-in later.** Smaller at first and the manage-checks order, but an administrator would need a key per project, the log could name nobody, and the success test could not run (spec C1).
- **Commands first, on `admin`, with an API later.** Quick, but server-only, and redone when the API comes.
- **Grants in a table.** Survive a restart, but anyone on the internet could cause writes. A restart costs a new `login`.
- **Refresh tokens, and a client library by hand.** The first has no use while every request reads a row (spec C4). The second could drift from the description.
- **A separate module or binary for the command line.** ADR 0001 wants one program. The generated client does get its own directory and licence (see Step 2).
- **Moving `admin` onto the API.** The server needs a way in with nothing signed in (spec C8).
- **A trusted-proxy setting, to show an address when approving.** One more setting that is dangerous when wrong, for little gain (spec C3).

## Tests that prove it

- **The wrong place (R34):** a table over every command with `-db`, on an empty folder: refused, with no file left behind. `TestServeMakesTheDatabase`: `serve` still creates one, and a command then works beside it.
- **Signing in (R1 to R3):** one test per answer of RFC 8628: pending, slow down, denied, expired, redeemed once, twice at the same moment. Codes ignoring case and dashes. Too many wrong tries.
- **The ceremony (R2):** another person's passkey refused, an answer bound to another code refused, no session opened, a counter gone backwards refused and logged, nothing read from the address.
- **Ending (R6, R7):** a sign-in past its idle time or its year, ended on **Devices**, ended by `logout`, or with its passkey removed gets 401 on the next request. Only `logout` works on a read-only one.
- **Scope (R4, R5):** an administrator's `read` sign-in, per operation, sees what a viewer sees, and gets 403 on every write. A marker in a check's secrets appears in no answer. A role changed or taken away counts on the next request.
- **Only a bearer (R38):** a browser's session cookie, with no bearer, gets 401 on every operation, and a bearer sent with another person's cookie acts as the bearer's.
- **No secret in a log or error (R41):** a marker in each credential, ping URL and address with a token, sent through every `/oauth` answer, every operation and every refusal, with the server's log captured: the marker is in no log line, no entry in `log_entries` and no error.
- **Credentials file and client (R8, R9, R19):** mode 0600 and refusal of a looser one, `http` refused except on this machine, a redirect not followed, control characters gone from every printed field.
- **Who may do what (R20 to R33, R36):** one table per tag, per role: viewer, editor, owner, administrator, member administrator, `read` and `manage` key, `read` sign-in, and someone from another project. 404 where they cannot see, 403 where they may not. `TestEveryOperationHasCredentials` walks the generated list of operations, so one served without a row fails.
- **What stays on the server (R31 to R33):** no operation exists for them, and each that nearly does refuses: making an administrator, a link for someone with a passkey, removing an administrator.
- **A key (R11 to R13, R37):** shown once, stored as a hash, reaches its project alone, held to the counts and tries, 401 when revoked, and working after its maker has been removed.
- **`-json` and output (R18, R21):** every read answer tested against the description, and no ping URL anywhere but the three operations that give one.
- **Names and messages (R45 to R47):** a name two checks share lists both and changes nothing; an id works; an owner gets one answer for someone who does not exist and someone who is not a member; a refusal says who to ask; an unknown operation says the versions differ; exit status 0 only when the command did what it said.
- **`allstill run` and `probe` (R35):** their existing tests run unchanged, with no credentials file and no `ALLSTILL_TOKEN`.
- **A deleted project (R25):** its ping key answers 404, its checks and probes hold it back, and its log entries stay.
- **Removing a person (R29, R30):** one test per thing that goes, then each way of being told: an open incident, a digest, a project they alone followed. Signing in, a link, a session and a sign-in all refused. The last administrator cannot be removed. The email can come back as someone new.
- **The log (R39, R40, C6):** one row per new kind, per way in. Every role reading a former member's line, per field, also in `details`, and no way to narrow to them.
- **The same rules (R36):** a refusal shared between the form and the API for each rule manage-checks names.
- **Limits and load (R42, R43):** 429 with `Retry-After`, a poll told to slow down, made-up credentials not filling the limiter, no write from `/oauth/device_authorization`, `make load` no worse.
- **The guide:** `TestGuideCommandsExist` over every new command, with its flags.
- **On the real installation:** the checks at the end of steps 1, 2 and 4, R10 with a phone in step 2, and R48.
- **Every step:** `make check` clean, the reviewer agent on the staged diff before each commit, a backup before each rollout, and any migration tried on a copy first.

## Departures

- Step 1: until `allstill login` exists in step 2, the message of R34 cannot point to it. It says to run the command on the server, and step 2 adds the pointer.
- Step 1: `import healthchecks` and `compare healthchecks` now open the database before they contact Healthchecks, so that in the wrong place they stop without using its API key.
- Step 2: a sign-in that has ended is gone, as a session is, so `person_tokens` has no ended column: when and by whom is in the log. The sign-in is made when someone approves it, with the passkey's counter, not when a command line fetches it, and whoever asked then has a minute to fetch it: the independent pass found that otherwise someone who got a code approved could wait on purpose with nothing to see or end, for up to ten minutes. So its token waits in memory for that minute at most, and a sign-in nobody fetched stays listed until someone ends it or it runs out, with no holder.
- Step 2: a `read` credential's `Access`, which the plan has in step 3, comes now, as the first sign-ins already need it. `workspace.go` stays as it is, and it lives in `tokens.go`.
- Step 2: clearing old sign-ins is in `prune`, beside sessions, not `tidy.go`. Grants take the time as an argument, so their tests pass it rather than use `synctest`, which the client's waiting does use.
- Step 2: `admin remove-passkey` without `-passkey` lists someone's passkeys, and `allstill login` ends the sign-in it replaces, as this machine could no longer end it later.
- Step 2: the limit on requests per credential comes with reading, in step 3, as the plan has it. `getMe` and `endThisToken` go without until then.
- Step 2: what the approval page says after a passkey is on that page, not at an address, so no link can show "Signed in" to someone who approved nothing.
- Step 2: the command line also takes plain http to a loopback address such as `127.0.0.1`, which R9's "the same rule the server has" does not: the server's own address must be a host name for passkeys, a command line's only has to be on this machine.
- Step 3: `listChecks` loses its `state` and `tag` filters, and a check its `paused` and `usual_run_time`, as nothing would use them yet. A check gains `probe_name`, as its page shows a viewer that name. `Status.overall` follows the checks' states alone, as what it promised about devices and the sender is not built. A check's `ca` comes only with a whole address.
- Step 3: `workspace.go` gains `SeesSecrets`, which moved from `active.go` as planned; a `read` credential's `Access` came in step 2.
- Step 3: reading a list across projects or over several pages with `-json` gives one `items` of what each page said, so a script need not follow cursors. `allstill log` takes `-limit` and `-before`, and `-check` takes a check that can still be seen, saying to use `-name` for one that was deleted.
- Step 3: the load test reads as five sign-ins at their limit, about 10 reads a second over a thousand checks, and logs how long reads took. Pings: median 0.9 ms with and without. Reads: median 12 ms, slowest 23 ms.
- Step 3: "every read answer tested against the description" is done by decoding each into the generated types and refusing a field they do not have, as a validator would be a new dependency. `getCheck` has no `ETag` for now, so `updateCheck`'s `If-Match` is step 4's to settle.
- Step 3: a read that spans several lists waits for the limit on a credential by itself, a few times, as finding a check by name among many projects can reach it; a single request does not, so a loop in a script still hears of it (case 23).
- Step 3: for step 5: a member's line holds their name in `about_name` as well, which "a former member" has to mask.
- Step 4: a key that was revoked is gone, as a sign-in that ended is: what happened to it is in the log. A project's key names are unique within it, so `allstill keys revoke NAME` means one thing.
- Step 4: `updateCheck` is no JSON Merge Patch, which needs to tell `null` from a field left out, and which the generated types cannot. It takes the fields to change, and `http` or `tcp` as a whole, which a client reads first and sends with `If-Match`. An empty `slug`, `description` or `tags` clears it. This also settles the `ETag` that step 3 left: `getCheck` and `updateCheck` carry one.
- Step 4: `-json` means how a command answers everywhere it works from anywhere, so `checks add` and `checks change` call a JSON field `-json-field`. `admin add-check` and `set-check` keep `-json`, and the two share their parsing.
- Step 4: a refusal from the store goes through one place in the API (`refusalOf`), rather than each operation's own response types, so 403, 404, 412 and 422 cannot differ between them.
- Step 4: `listKeys` is not paged, as `listProbes` is not: a project holds few, and a command reads them all to find one by name.
- Step 4: `createCheck` answers 422 for a slug in use, as the store does, so the description no longer has 409. A job's check with no schedule is as the form's: once a day with an hour's grace.
