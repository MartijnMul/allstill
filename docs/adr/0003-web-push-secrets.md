# 0003. Web Push keys and subscriptions live in the database

- Status: Accepted; adds to the exceptions to hashing in [0001](0001-stack-and-architecture.md); amended by [0004](0004-what-a-check-fetches.md) on third-party secrets in URLs and on redirects for checks
- Date: 2026-10-27

## Context

Allstill alerts people on their phones through Web Push ([ADR 0001](0001-stack-and-architecture.md)). Before it can, it needs two kinds of secret that it must read back, so it cannot keep them as hashes:

- **The VAPID key pair** of the installation. Every push carries a short token signed with its private key. A browser's subscription is made for the matching public key, and push services refuse a push signed with any other: Apple and Google with an error that says so, Mozilla with a 404, which reads like a subscription that is gone ([web.dev: common issues](https://web.dev/articles/push-notifications-common-issues-and-reporting-bugs)). Nothing on the phone shows that its subscription no longer works.
- **A subscription** per device: the push service's address for that device (the endpoint, itself a secret URL), and the two keys that encrypt messages to it. Whoever holds a subscription and the private key can send that device a notification that looks like Allstill's, with any text and link.

ADR 0001 says "all keys and tokens are stored as hashes, except check UUIDs". The [alerts spec](../../intent/alerts/spec.md) (C6) asks for a record of a second exception before Web Push is built.

Played through:

1. **A restart, an upgrade, a new container.** The key must come back unchanged, or every phone stops getting alerts without anyone noticing.
2. **A restore from backup onto a new host.** The key and the subscriptions must come back together; one without the other is useless.
3. **A copy of the production database, made to look into a problem.** If `allstill serve` were started on it with alerts on, it would push real alerts to real phones, as a second sender.
4. **A person leaves.** Their devices must stop getting alerts, and their subscriptions must go.
5. **A move to another domain.** Push services do not know the dashboard's address, so old subscriptions keep working; the dashboard on the home screen still belongs to the old address.
6. **A stolen backup.** It already holds every check UUID in plain text, and with them the power to ping any check, plus the jobs' output.
7. **A person registers a subscription by hand** with an endpoint inside the server's own network. Allstill would send a request there on their behalf.

## Decision

Keep both in the database, in plain text, protected as the database and its backups are.

- **One VAPID key pair per workspace,** made at first start and never replaced by Allstill on its own. The token's subject, which Apple requires to be an `https` or `mailto` address, is the installation's `-base-url`. A workspace with its own key would also have its own subject.
- **A subscription is a `webpush` channel** of its person, and records the public key it was made for. Allstill only pushes to a subscription made for its current key; any other shows as needing to be made again on that device, listed under `attention`. Checking first matters: pushed to anyway, Mozilla's 404 would read as a device that is gone, and the subscription would be deleted instead of repaired.
- **The dashboard renews the subscription by itself.** Each time it is opened on a subscribed device, it compares the device's subscription with the server's key and sends it to the server again; on a mismatch it unsubscribes and subscribes once more. That also picks up an endpoint the push service has changed, and makes case 1's lost key cost every person one visit to the dashboard per device, not a new setup.
- **An endpoint must be `https`,** and every push goes through the client ADR 0001 requires for everything the server fetches, which refuses private, loopback and link-local addresses. This record adds two rules to that client: addresses are checked when connecting, so a host name cannot later point inside the network, and redirects are not followed. The private ranges an administrator may allow for HTTP checks never apply to push endpoints (case 7).
- **Endpoints and keys stay out of logs and error messages,** as ping URLs do, and the API and the dashboard never return them.
- **Deleting a person deletes their channels** (the foreign key in migration 0003 cascades, case 4).
- **Encryption at rest comes with the first third-party secret,** such as a chat bot token in phase 2, as one decision for every stored secret. The SMTP password stays in the environment, as the spec says.

This adds the VAPID private key and Web Push subscriptions to ADR 0001's exceptions to hashing.

## Alternatives considered

- **The key in a file or an environment variable,** as Mastodon and ntfy do. It keeps the key out of database backups, but a restore then needs two things kept in step, and a lost or mismatched key cuts off every phone (cases 1 and 2). One binary and one data file is what ADR 0001 promises.
- **Encrypting the key and the subscriptions with a key from the environment.** It protects a stolen backup (case 6) only if that key is kept elsewhere, and makes a lost key as bad as a lost file. A stolen backup already gives away every ping URL, so it would protect little today. It is worth doing once for all secrets when a third-party one arrives.
- **A key made at every start,** like the one that signs sign-in challenges. It needs nothing stored, but cuts off every device at every restart.
- **One key for the whole installation instead of per workspace.** The same today, since an installation is one workspace. A key per workspace keeps a later shared, hosted installation from letting one customer's key reach another's devices, at the cost of one column.
- **Pushing only to subscriptions made at the current dashboard address,** to keep a copy served elsewhere from pushing (case 3). It blocks old subscriptions that still work after a move to another domain (case 5), does nothing for a copy started with the same settings, and depends on how an address is written. A copy is kept from pushing by serving it in shadow mode instead.
- **A list of allowed push services.** Browsers do not share a fixed set of push services, and the address rules already keep requests out of the server's network.

## Consequences

Easier:

- **Backups and restores** stay one file: `allstill backup` copies the key and the subscriptions with everything else.
- **No silent loss of alerts:** a subscription Allstill cannot use shows up as such, and the next visit to the dashboard on that device repairs it.

Harder, or ours to look after:

- **The database and its backups are secrets,** more so than before. Who has a copy can send notifications that look like Allstill's, with any text and link, to every subscribed device, until each device unsubscribes. Allstill cannot revoke a subscription at the push service, and replacing its key does not stop the thief, who keeps the old one. After a stolen backup, a new key and a visit to the dashboard on each device do: the dashboard then drops the old subscription. [Running Allstill](../running.md) says so, and that backups are readable by their owner only.
- **A copy of the database is a second sender,** by email as much as by push. It is served only in shadow mode; `allstill serve` gets a way to force that, whatever the database says.
- **Phones need an `https` address:** with the default `http://localhost`, Apple refuses every push.
- **Replacing the key** is not offered. If it is ever needed, it is a command that makes a new key; each device then subscribes again at its next visit to the dashboard, and one that never visits is no longer reached.
- **Encryption at rest** is still owed, and must be decided before the first third-party secret is stored.
