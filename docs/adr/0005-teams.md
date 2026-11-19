# 0005. Teams own projects, and the administrator is not shown them

- Status: Accepted; replaces parts of the section on workspace, projects, people and keys in [0001](0001-stack-and-architecture.md), listed under the decision
- Date: 2026-11-19

## Context

ADR 0001 made an installation one workspace with projects as the unit of access, and gave workspace administrators every project. Parties who must not see each other were to run their own installation. That fits colleagues and customers. It does not fit a guest who is the host's equal: a friend, a second company, another department. They would use the installation gladly, but their checks are none of the host's business, and they cannot look after themselves on it ([intent/teams](../../intent/teams/intent.md)).

A second installation stays a sound answer and has a price: the guest runs a server after all, or the host runs two, and someone who belongs to both signs in, registers devices and sets quiet hours twice.

ADR 0001 rejected two things that touch this, and its reasons hold:

- **Organisations as hard walls**, because shared infrastructure cannot be modelled across them: a host serving two organisations gives two incidents, and a person's alerts split.
- **Teams as an access concept**, because in a small team the difference is who is alerted, not who may see.

Played through in the [spec](../../intent/teams/spec.md): a guest who is an equal, one operator for many parties, an outsider with one project, an outsider who only needs a status, shared infrastructure, a person in two teams, a team left without an owner, a project nobody is alerted for, a lost passkey, an installation with one party, an administrator who needs to look, a server command that reaches in, a stolen administrator's sign-in, and a share an editor could abuse.

## Decision

- **A team owns projects.** Every project belongs to one team. A team's member has one role, owner, editor or viewer, in all its projects. A project can still give a role to a single person, and the highest role a person has counts. No role is new.
- **A team is not a workspace.** People, passkeys, devices, quiet hours, incidents and the push key stay with the installation, which stays one workspace. So a person in two teams is one person and is told once, and a project can be shared across teams.
- **An administrator keeps the installation and has no role in a project by being one.** They make and remove teams, see each team's name, owners and counts, see people and whether messages to them fail, and keep mail, retention and the alerts switch. They are not shown a project's or a check's name, state, address or history.
- **Reaching into a team is possible and tells on itself.** The server's commands still work on every team, as whoever holds the shell holds the database. Each is written to that team's log, and its owners are told at once when one lets someone in, when an administrator enters to look, and when a sign-in link is made for a member who has a passkey. Those notices are sent whatever the alerts switch says. Entering, naming an owner and such a sign-in link take the server.
- **What people hand each other across a team is a link.** A place in a team, a role in a project and a share are each handed over as a link that works once and is accepted after signing in. Nothing is looked up by name or address, so nobody learns who has an account or which teams exist.
- **A share gives another team viewer or editor in one project.** It counts once an owner there accepts. It alerts nobody on the other side until they switch it on for themselves, never breaks their quiet hours, never names one team's people to the other, and never lets a check leave its team.
- **Whether a person is alerted is their own choice per project,** apart from their role, starting from what the role suggests.
- **Access is decided in one place and who is told in one other,** each a single definition every page, API operation, message and log reader uses.
- **What the installation opens is opened per team:** private ranges checks may reach, and teams take turns when attempts wait for room. A probe belongs to one team.
- **An upgrade changes nothing anyone can see.** Today's installation becomes one team, its administrators become owners of it, and everyone else keeps the roles they have. With one team, nobody meets the word unless they manage its people.

What this replaces in ADR 0001:

- "Workspace administrators manage people, shared resources and settings, and see every project." They no longer see every project.
- "Projects are the unit of access." A team now gives access to its projects, next to a project's own members.
- "Teams are routing, not access", and the rejected alternative "Teams as an access concept".
- Probes as shared in the workspace, running "the checks of any project assigned to it". A probe belongs to one team.
- Private ranges allowed "for an installation". They are allowed per team.
- "A project's routing says who is alerted for it." It starts from a person's role and is then their own choice.
- The consequence "without a permission system to learn" holds less: there are more ways into a project.

What stands: one workspace per installation, alerts that belong to people, the rejection of organisations as hard walls, and everything outside that section.

## Alternatives considered

- **A second installation, made easy.** Sound, and still what parties who share nothing should do. Rejected as the only answer for the price above, and because a project could never be shared between the two.
- **A workspace per team,** which every table already carries. A person would be one person per workspace, with their own passkeys, devices and quiet hours, and a shared project would cross the line every foreign key holds. The workspace stays reserved for a hosted service whose customers share nothing.
- **Hard walls with no sharing.** ADR 0001's objection in full: shared infrastructure becomes two incidents. A share is the door that lets a check depend on another team's, once dependencies are built.
- **Refusing the server's commands for other teams.** It would look like a wall and move a determined administrator to the database file, where nothing is logged. Telling on every command is the stronger promise.
- **Looking people and teams up by address or name.** Simpler to use, but each lookup answers whether they exist, and an address nobody confirmed would decide who gets a share.
- **A switch that lets an administrator see everything again.** Today's behaviour is what an installation with one team already has, as its administrators own it.

## Consequences

Easier:

- **Hosting someone else.** One server, one address, one backup, and a guest who runs their own checks and people.
- **Trusting the host.** Not because the host cannot look, but because looking is announced.
- **Adding colleagues.** One role in a team instead of one membership per project.

Harder, or ours to look after:

- **More ways into a project.** Team, own role, share, entry. Held by the same three words everywhere, by the highest role counting, and by a page that shows who can see a project and why.
- **The wall must be tested.** A two-team fixture walks every page, API operation, message and log reader. A new one that forgets its team is a leak.
- **The administrator's word changes meaning.** `workspace_admin` no longer means "sees every project", in the API and in every place the code reads it.
- **Quiet teams.** An administrator no longer hears when nobody in a team does. They are told that a team is quiet, not what went down.
- **Limits between strangers.** Counts and turns per team, where there were only limits per project and per person.

## Not decided here

- Moving a project between teams, an import from Healthchecks run from one's own machine, alerts on or in shadow per team, and letting members make teams. The spec leaves each out and says why.
- What each side sees of the other's checks in an incident joined by a dependency, which belongs to dependencies.
