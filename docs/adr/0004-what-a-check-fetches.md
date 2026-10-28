# 0004. What a check fetches: its URL as a secret, and redirects

- Status: Accepted; amends [0003](0003-web-push-secrets.md) on third-party secrets and redirects, and adds check URLs to the exceptions to hashing in [0001](0001-stack-and-architecture.md)
- Date: 2026-10-28

## Context

Allstill is about to fetch addresses itself: sites, APIs and ports, from the server or from a probe inside another network ([intent/http-checks](../../intent/http-checks/spec.md)). Two things ADR 0003 settled for Web Push meet this:

- **A URL can hold someone else's secret.** A health endpoint may only answer with a token, in its path or its query string. That token belongs to another party: ADR 0003 says encryption at rest "must be decided before the first third-party secret is stored". Allstill cannot tell a token in a URL from any other part of it, so once checks exist, such secrets will be stored.
- **A check may need a redirect followed.** ADR 0003 made the client for addresses others choose follow no redirects, which suits a push endpoint. A site whose health page sits behind a redirect needs it followed, or a check that expects only the redirect.

Played through:

1. **A stolen backup.** It already holds every check UUID, and with them the power to ping any check, and the jobs' output (ADR 0003, case 6). Tokens in check URLs add the power to read those health endpoints.
2. **A token in a path**, such as `https://example.com/health/<token>`, cannot be found by looking for a query string.
3. **A redirect into the network.** A public site redirects to `http://10.0.0.5/`; followed blindly, Allstill would fetch inside its own network.
4. **A viewer of a project**, often someone at a customer who only needs a status, opens a check's page.

## Decision

- **A check's URL is stored as a ping URL is:** in the database, in plain text, protected as the database and its backups are (ADR 0003). Owners and editors of its project and workspace administrators see it whole, in the dashboard and through a token of theirs with the `manage` scope, as does a project's API key with that scope; everyone else, viewers and `read` tokens included, sees only its scheme and host; it is never in logs, reasons or messages. This adds check URLs to ADR 0001's exceptions to storing secrets as hashes, as ping URLs are. This is the decision ADR 0003 asked for, for secrets in URLs: they are not encrypted at rest yet.
- **Encryption at rest is decided with the first secret Allstill asks for by name,** such as a header with a token or a chat bot's token, and covers every stored secret then, URLs included.
- **A check follows redirects only when it says so,** at most 5. Every hop is dialled through the same address checks as the first (ADR 0001, 0003): a redirect into a range the server or probe may not reach is refused. The client for push endpoints still follows none.
- **Checks use a transport of their own** that keeps no connection between attempts, so each attempt resolves, connects and shakes hands as a visitor would.

## Alternatives considered

- **Refuse URLs that look like they hold a token.** A token in a path cannot be told from a page's name (case 2), and refusing a query string would refuse ordinary URLs too.
- **Encrypt check URLs now, with a key from the environment.** It protects a stolen backup only if that key lives elsewhere, and it would protect URLs while ping URLs, which also grant access, stay readable. ADR 0003 rejected it for the same reasons; it is decided once, for all secrets.
- **Never follow redirects for checks.** A check can expect the redirect itself (a 301 with its `Location`), and usually should; but a health page behind a fixed redirect would need two checks for one question.
- **Always follow redirects.** It hides a broken or hijacked redirect, which is exactly what checking `www` against the apex is for.

## Consequences

Easier:

- **Health endpoints with a token** can be checked once checks exist, with the same care as ping URLs.
- **Viewers** see that a check exists and what host it looks at, not how to reach it.

Harder, or ours to look after:

- **A stolen database or backup** now also gives the tokens in check URLs. The change that builds checks adds this to where `docs/running.md` says the database is a secret, with the advice to use a token that can only read health.
- **Encryption at rest** stays owed, and is decided before Allstill asks for any secret by name.
- **Every redirect hop is a new address** to check; the client must apply the same rules at each dial, which its dial-time check does.
