<!-- SPDX-License-Identifier: AGPL-3.0-only -->
# Intent: teams with their own projects, on one installation

Author: Claude, confirmed by Martijn Mullié (maintainer)
Status: accepted
Started: 2026-11-19

## Problem

Someone who runs Allstill cannot give someone else a place of their own on it.

- **Whoever runs it sees everything.** An administrator sees every project and gets its alerts. That fits colleagues and customers. It does not fit a friend, a second company or another department, who would use the installation gladly but whose checks are none of the host's business.
- **A guest cannot look after themselves.** Someone added to a project works inside that project and no further. A second project, a colleague, a probe in their own network: each is a request to the host, who does it on the server.
- **A project has no owner but the installation.** There is no "mine" and "ours". Someone trying a check for a job of their own puts it among the company's, or asks an administrator for a project.
- **The answer today is a second installation.** ADR 0001 says that parties who must not see each other run their own, and the menu bar app is planned to hold several side by side. That answer is sound, and it has a price. The guest has to run a server, which is what they hoped to avoid, or the host runs two: a second address, backup, upgrade and outside watch. In the browser and on the command line, someone who belongs to both signs in twice, registers each device twice and sets quiet hours twice.

ADR 0001 weighed this and chose against it twice. Against teams as a way to give access, because in a small team the difference is who is alerted, not who may see. Against organisations as hard walls, because shared infrastructure cannot be modelled across them. This intent does not claim those reasons were wrong. It asks for something between "everyone the host adds is seen by the host" and "run it twice". The maintainer has decided that is worth building (see below). One real installation has the need so far, so the spec plays it through the general cases before a model is chosen.

## Proposed outcome

- **A team is a group of people with projects of its own.** The team decides who is in it and what each may do, without the host.
- **A project is shared one at a time,** with another team or with one person, to look or to change. Sharing can be taken back.
- **The host runs the installation without being shown what is in it.** They make teams, keep the server and its settings, and help when someone is locked out. The dashboard, the command line and the alerts show them a team's projects only when the team shares them.
- **The host can still get in when the server needs it,** as the one who keeps it, not as a member. That is an exception, done on purpose, and the team sees that it happened.
- **One person stays one person.** In two teams, they sign in once, have one set of devices and quiet hours, and hear about one problem once.
- **A small installation notices nothing.** One person, or one group of colleagues, upgrades and finds everything where it was, with no team to set up first.
- **Success:** the host makes a team for someone in one step. That person signs in, adds a colleague, makes a project with a check, and shares one project with the host to look at. The host sees that project and no other of theirs, and nobody went back to the server. This waits on managing people and projects away from the server, which is not built yet.

## Cases it must hold for

Each is a generic case; the spec plays them through before a model is chosen.

1. **A guest who is an equal.** Alice runs the installation. Bob gets a team, runs it himself, and Alice is shown none of it.
2. **One operator for many parties.** Carol watches jobs for three customers. Each customer sees its own project and not the others'.
3. **An outsider with an account.** Today a customer is a viewer of one project among Carol's. After the change they still see that one project, and being let in gives them no view of the rest of Carol's team.
4. **Shared infrastructure.** `web-1` belongs to Alice's team, and Bob's jobs run on it. Today its failure opens an incident per check, gathered into a few messages; one incident with a cause is planned, through dependencies that cross projects. Across teams that is the very thing ADR 0001 said a hard wall cannot do. Bob's team should be told the cause lies elsewhere, and learn no more than Alice lets it.
5. **A person in two teams.** Dave is in his employer's team and in a team of his own. One sign-in, one phone, one digest. When the employer removes him, his own team and his sign-in stay.
6. **Someone leaves.** The team's projects stay with the team. What becomes of a team whose only member leaves, and of the jobs that still ping its checks?
7. **Nobody is left to hear.** A project's last member goes, or nobody in it gets alerts. Today an administrator who is no member still hears. Behind a wall, who does?
8. **The only owner loses their passkey.** The host has to help. What may the host do to get them back in, and what does the team see of it afterwards?
9. **An outsider who only needs a status.** They get a status page or a share link, without an account, as ADR 0001 plans. Teams must not turn that into an account.
10. **One party on its own installation.** The most common case. Nothing gets harder.
11. **A project changes hands,** from one team to another, keeping its checks, history and ping URLs.

## Affected users and systems

- Hosts, who become two things that are one today: the keeper of the server, and a member of their own team.
- People who get a team: they manage members and projects for the first time.
- Members of today's projects, whose access must come through the change exactly as it is.
- Alerts: who is told about a project shared with a team, and what a host who is no member still hears.
- The log, which today lets an administrator read every project, and for which whoever runs the installation answers as a record of what named people did.
- Probes, which belong to the installation today and open one network to every project they serve.
- Keys, which belong to a project so they outlive a person.
- Status pages, planned to belong to the installation and to draw from several projects.
- The command line and the API, whose shapes name projects and no teams, and which `api/` publishes.
- `allstill admin`, the guide, `docs/running.md` and the import from Healthchecks, whose projects map one to one today.

## Constraints

- **It changes accepted decisions, in a new ADR.** ADR 0001 gives an administrator every project and rejects teams as a way to give access, and more follows from those two. The new record says which parts it replaces, and answers what they protected: shared infrastructure that can become one incident, one alert per person, and no permission system to learn for a small team.
- **Nobody loses or gains access by upgrading.** What each person can see and do the day after is what it was the day before. That includes a viewer of a single project.
- **The wall is tested, not assumed.** Every query stays scoped, and tests show that no page, API answer, log line, alert or error tells one team what is in another's.
- **Say plainly what the wall is not.** Whoever holds the server holds the database and its backups. A guest's checks are fetched from the host's server, from its address, into whatever private ranges the host opened for everyone. Sending mail, shadow mode, how long history is kept and the disk are the host's. Teams keep the product from showing one party's checks to another; they do not protect a guest from a host who goes looking, and the documentation says so.
- **What the host does inside a team leaves a trace there.** Help with a lost passkey, or anything else that reaches in, is in that team's log.
- **One team cannot starve another.** Today's limits are per project and per person: a team with four projects can take every attempt the server runs at once. They were set for people who know each other, and need a look for people who do not.
- **A quiet team is noticed.** A wall must not let a project with nobody left to hear go unnoticed.
- **No weaker way in.** A passkey stays how people prove who they are, also when a team adds its own members.
- **It needs what is not built yet.** Managing people and projects away from the server is still to come ([cli-anywhere](../cli-anywhere/spec.md)), and a team that runs itself depends on it.
- Checks and alerts keep running throughout, and ping URLs keep working.

## Out of scope

- A hosted service: signing up without the host, plans, billing.
- Single sign-on and directory sync.
- Teams inside teams, and roles a team defines itself.
- Protecting a team's data from whoever holds the server, such as encryption per team.
- Building status pages. Whose they are, once there are teams, is an open question below.

## Decided by the maintainer

- **It is built,** as teams on one installation, not as a second installation made easy.
- **Today's installation becomes the first team,** with its people and projects as they are. A second team is added next to it.
- **The host is not a member of another team** and is not shown its projects. Getting in as keeper of the server is possible and is the exception.
- **Teams share with each other through permissions,** per project.
- **No separate place for one person's own projects.** Someone who wants that has a team of one.

## Open questions

- Named groups of people, as a shortcut for adding several at once, are already planned in ADR 0001. How much of the need do they cover without changing who may see what?
- How far apart are teams? If people, devices and incidents are shared and projects can be, a team is less a wall than a way of grouping, and the model follows from that answer.
- What can the host do, exactly? See that a team exists, how many checks it has, who is in it? Remove a team? Is the host role one person or several?
- Who reads why a message to a team's member failed, when the host keeps the mail server and may not read the team?
- Who answers for a team's log as personal data, and who may remove someone from it?
- How does the host get in when the server needs it: a command on the server only, for a set time, with a reason? Is the team told at once, or does it only read it in its log?
- Is a customer a team, or an outsider let into one project of the operator's team? Who adds a second person at the customer?
- Who is alerted for a shared project: only the team that owns it, or the other side too, if they ask? May the owning team wake the other side's people with a critical check?
- What roles does a team have, and how do they meet a project's roles: is an owner of the team an owner of each of its projects?
- Can a team share with a person who is in no team with them? How does it find that person without learning who has an account?
- What does the log show to a team a project is shared with?
- Do probes, calendars and shared channels move to the team? Can a team lend a probe to another?
- Whose is a status page that draws from projects of two teams?
- Which settings stay with the installation and which move to the team?
- May anyone make a team, or only the host?
- What names do people see? "Team" may be wrong for a household or a customer, the log already signs a viewer's lines "the team", and "workspace" already means something here.
- In what order can this be built so each step is useful and none leaves access half changed?
