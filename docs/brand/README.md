<!-- SPDX-License-Identifier: AGPL-3.0-only -->
# Brand

<img src="lockup.svg" alt="[ OK ] allstill" height="32">

Allstill's logo is a line a machine prints when it starts: `[  OK  ] allstill`. It answers the question in the name, "is it all still running?", the way the people who run servers are used to reading it. And it is live: where Allstill shows its own state, the word in the brackets changes. This page holds the logo, the mark, the app icon, the colours, the type and the voice, so the menu bar app, the dashboard, the website and the alerts look and sound like one product.

## The line

[`lockup.svg`](lockup.svg), and in one colour [`lockup-mono.svg`](lockup-mono.svg).

- **Set in JetBrains Mono**: the brackets and spaces in Medium, the word in the brackets in Bold, `allstill` in Medium. The files hold the letters as outlines, so they look the same without the font installed.
- **A six-wide field**, as at boot: two spaces, `OK`, two spaces, between the brackets, then one space and the name. The field is always six characters wide, so the line never changes length when the word in it does.
- **Colour**: the brackets in grey, the word in its state colour, the name in ink. On a dark ground the lighter variants (see [Colour](#colour)).
- **Lowercase** in the logo, **Allstill** in text: capital A, one word, never AllStill or ALLSTILL.
- **Size**: at least 14 px high. Below that, the mark alone.
- **Don't** change the spacing, set it in another typeface, swap the order, or put anything but a state word in the brackets.

## The mark

[`mark.svg`](mark.svg): `[OK]`, all in Bold, without the spaces. It stands for Allstill where the whole line does not fit: the app icon, a favicon, an avatar. It is always `[OK]`; only the line is live.

## The live line

[`states.svg`](states.svg). The idea: where Allstill shows its own state, the word in the brackets says it. A first proposal:

| Line | When | Colour |
|---|---|---|
| `[  OK  ]` | everything is up | up |
| `[ WARN ]` | something is late or has a warning | warning |
| `[FAILED] 1` | there are open incidents, followed by how many | down |
| `[  ??  ]` | Allstill cannot tell, for example when the app cannot reach the server | unknown |
| `[PAUSED]` | everything you follow is paused | paused |

The line shows the worst state that needs someone, and `OK` only when Allstill knows: green is proven green. `FAILED` is the boot line's own word, so the text after it names what happened: `[FAILED] shop.example.com is down`, `[FAILED] nightly-backup did not start`.

This is meant for the menu bar item of the macOS app, the top of the dashboard, the subject of an alert and the first line of the morning digest. None of these shows the live line yet: the dashboard's header carries the line with `OK` fixed. Before it is built, the mapping has to be played through the states the API knows: `new`, `off_hours` and `maintenance` are not in the table, nor what one unknown or paused check does to the line. Places that show the logo and not the state, such as the website and this page, always show `OK`.

Drawing the line live means setting it in JetBrains Mono in the app and the dashboard. Shipping the font with Allstill is a new dependency under a third licence (the SIL Open Font License), so it needs its own decision record first. Until then, a live line would have to use the system monospace; the fixed line in the dashboard is drawn as outlines and needs no font.

## App icon

[`app-icon.svg`](app-icon.svg), on Apple's 1024 grid: an 824 square with rounded corners and a 100 margin, near-black (`#0f1210`) in light and dark mode alike, like a terminal, with the mark centred at about three fifths of its width, the brackets in `#7e847f` and `OK` in `#7fd197`. The square and the mark are separate parts (`#shape` and `#mark`), so they can become the layers of the app's icon when the macOS app is built. The mark reads down to 64 px; at 32 px and below it is a texture, not text. The dashboard's favicon, web app icon and notification icon, [`icon.svg`](../../internal/web/static/icon.svg), is the same drawing cropped to its square.

## Colour

Allstill is ink on a calm ground. Its only colours are the state colours, and in the logo the word in the brackets carries one: green is "all ok", not a brand colour, so a red `FAILED` in the same place means something.

The tokens live in [`internal/web/static/app.css`](../../internal/web/static/app.css); where this page and that file disagree, the file wins. The line's bracket grey and green are `--logo-bracket` and `--logo-ok` there; the app icon's dark ground is set in its SVG.

| Role | Light | Dark | Use |
|---|---|---|---|
| Ground | `#f4f5f3` | `#111314` | The page behind everything |
| Surface | `#ffffff` | `#1a1d1f` | Panels, cards, the menu |
| Ink | `#16191b` | `#eceeec` | Text, `allstill` in the logo |
| Bracket grey | `#646c71` | `#7e847f` | The brackets in the logo |
| Muted | `#5e676c` | `#9aa29f` | Labels, timestamps |
| Line | `#e3e6e3` | `#2d3133` | Borders and rules |

Each state has a colour and a shape in the product, so it never depends on colour alone; in the logo the word itself says it. The line is text, so its colours reach a contrast of 4.5:1 on the page ground: darker variants on light, lighter on dark. On a dark panel the grey brackets come just under that, which a logo may.

| State | In the product (light / dark) | Shape in lists | In the line (light / dark) | Word |
|---|---|---|---|---|
| Up | `#1e8a5a` / `#4fbf88` | a dot | `#187a4f` / `#7fd197` | `OK` |
| Late, warning | `#b26e0e` / `#e0a243` | half a dot | `#8f5a0b` / `#e0a243` | `WARN` |
| Down, incident | `#cf3f2a` / `#f0715c` | a diamond | `#b23522` / `#f0715c` | `FAILED` |
| Unknown, new | `#8c959b` / `#7d8689` | a ring | `#5e676c` / `#9aa29f` | `??` |
| Paused | `#6a7894` / `#8f9cb8` | a square | `#5d6a86` / `#8f9cb8` | `PAUSED` |

In one colour (print, embossing, a single-colour sticker), everything is ink, or the colour of the ground reversed.

## Type

- **JetBrains Mono** ([JetBrains](https://www.jetbrains.com/lp/mono/), SIL Open Font License 1.1) for the logo and the live line, and outside the product for headings and anything that quotes the machine: commands, check names, the line itself. The SVG files are outlines of version 2.304, Medium and Bold.
- **The system sans** for running text: SF Pro in the macOS app, `system-ui` in the dashboard and on the website, so Allstill reads natively wherever it runs. Machine things in the dashboard (check names, values, URLs, counts) use the system monospace.
- **Weights**: 400 for text, 500 for emphasis, 600 for headings; Bold only for the word in the brackets.
- **Sentence case** for headings, buttons and menu items: "Open dashboard", not "Open Dashboard". Capitals only inside the brackets.

## Voice

Calm, plain and specific, like a colleague who checked everything before you got in.

- **Lead with what it means.** "Every job ran last night. Two need a look today, neither is urgent." The numbers and details come after.
- **Name the thing.** The check, the time, the project: "shop.example.com is down. Since 10:42, in Production." Never "An error occurred".
- **Urgency only when it is real.** No exclamation marks, no capitals for effect outside the brackets, no red for something that can wait. A warning says it is a warning: "The copy finished and is complete, so this is a warning, not a failure."
- **Say what happens next** when Allstill will act: "You get a phone alert if it is still not renewed 3 days before."
- **No blame and no jokes** about something that is down. The live line is the one place Allstill winks, and only when all is well.
- **The same words every time**: a *check* is what Allstill watches, a *run* is one execution of a job, a *ping* is a job reporting in, an *incident* is a problem that needs someone, opened when a check goes down and resolved once it has settled, the *digest* is the morning summary, *quiet hours* are when only critical checks get through.
- **Numbers as digits**, durations short ("2 h 50 min"), times on the 24-hour clock ("10:42").

## The name and the mark

These files are under the AGPL like the rest of the repository ([ADR 0002](../adr/0002-licence-and-contributor-agreement.md)): anyone may copy and change them under its terms. That licence covers the files, not the name. The name Allstill is Martijn Mullié's, and forks are welcome under another name; protecting the name and the logo as marks belongs with the policy on using the name that ADR 0002 calls for.
