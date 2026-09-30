# Architecture

This document explains how Army Retirement Workbench is built and why. It is
written for engineers who want to contribute, review, or reuse the design.

## Context and scope

Army Retirement Workbench helps a soldier plan the last two years of service:
the retirement timeline, VA claim evidence, the budget, the job search, and
the move. It holds some of the most sensitive records a person has: medical
history, debts, service documents, and identity details.

**It holds a soldier's most sensitive records, and the person running it is
not an engineer.** Every decision below falls out of that sentence. The data
must never leave the person's control by default, and the app must install,
run, back up, and recover without anyone who can read a log file.

## Goals

- **Private by default.** Nothing leaves the computer until the person turns
  on a feature that needs the internet, and each one says what it sends.
- **Zero setup.** Download one file, open it, and it works. No installer, no
  database, no account.
- **Recoverable by the user.** One-click backup and restore. A failed save
  never loses data it claimed to keep.
- **Free to run forever.** No servers to pay for and no API bills. The AI
  features ride subscriptions people already have.
- **Works on Mac, Windows, and Linux** from the same source.

## Non-goals

These are reasonable things to want, and each one is refused on purpose.

| Not a goal | Why not |
|---|---|
| Multi-user or cloud hosting | A hosted copy concentrates thousands of soldiers' medical and financial records in one breach target. One person, one computer is the security model. |
| A login screen | On a single-user loopback app, a password protects against someone who already has your computer account, and it adds a lockout risk for the person who forgets it. The operating system account is the boundary. |
| Official guidance | Regulations change and differ by branch and installation. The app plans and organizes; counselors, S-1, and accredited VSOs decide. |
| Scraping LinkedIn or Indeed | Both forbid it and block it within days. The app opens their own search pages with your keywords instead. |
| Kubernetes deployment | The audience runs laptops. A container exists for home servers, and nothing more is maintained. |

## System context

```
 Browser (127.0.0.1 only)
    │  HTML forms, a little JavaScript
    ▼
 guard ── refuses foreign Host headers and cross-site writes
    ▼
 net/http mux ── one handler per page and action
    │
    ├── Store ── state.json (copy, change, fsync, rename, then commit)
    ├── docs/ ── uploaded files, paths relative to the data folder
    │
    └── opt-in, outbound only
         ├── claude or codex CLI ── the Advisor, on the user's own plan
         ├── USAJOBS, Adzuna ────── job search APIs, user's free keys
         ├── realtor.com ────────── housing listings, every 30 minutes
         └── Chrome or Edge ─────── prints resume PDFs, headless
```

| Component | What it is | When it changes |
|---|---|---|
| Binary | One Go program, templates and styles embedded | Every release |
| `state.json` | Everything the person entered | Every save |
| `docs/` | Uploaded PDFs and images | On upload or delete |
| `homes.json` | Housing listing cache | Every 30 minutes, disposable |
| Settings keys | USAJOBS and Adzuna keys | When the person sets them |

## Degree of constraint

**Inherited:** the Army retirement process (the PAR, the DA 2339, the BDD
claim window), VA rating math under 38 CFR Part 4, the fact that LinkedIn
and Indeed publish no search API, and that consumer AI plans have no API
key. **Chosen:** everything else in this document.

## Decisions

### One binary, standard library only

**Decision.** Ship a single Go binary with no third-party modules.

**Why.** Zero setup. A soldier downloads one file per platform, and a
reviewer can audit every line the program runs without auditing a
dependency tree.

**Alternatives considered.**

| Option | Why it lost |
|---|---|
| Electron | Hundreds of megabytes, and a Node dependency tree nobody audits |
| Python or Node web app | Needs a runtime installed first, which loses most of the audience |
| Hosted web app | Violates the privacy goal; see non-goals |

**Cost accepted.** Hand-written things a framework would give for free: form
handling, a small PDF merger per platform, and no component library.

### A JSON file, committed only after it reaches the disk

**Decision.** All state is one JSON document. Each change runs on a deep
copy; the copy is written to a temporary file, synced to disk, and renamed
over `state.json`. Only then does the in-memory state become the copy.

**Why.** A person who sees a change must be able to trust it survived. The
earlier version changed memory first and wrote second, so a full disk showed
an edit that vanished on restart.

**Alternatives considered.** SQLite is the obvious choice. It lost because it
needs cgo or a large pure-Go port, and the whole data set is small enough
(kilobytes, plus files) that a document rewrite per save is free.

**Cost accepted.** Every save rewrites the whole file. At a few hundred
kilobytes this costs nothing measurable; at hundreds of megabytes it would
need to change.

### The data folder lives outside the app

**Decision.** Data defaults to the operating system's per-user app data
folder, readable only by that user (folders `0700`, files `0600`). Document
paths are stored relative to that folder.

**Why.** The app's own folder gets deleted, synced, and zipped when people
share it. Keeping data out of it means an upgrade or a shared copy never
takes records with it. Relative paths let a person move the folder or
restore it on another computer.

**Near miss.** An earlier build stored document paths as `data/docs/...`,
tied to the folder's name. Moving the folder would have broken every
document silently. `docPath` now re-anchors those legacy paths, and a test
proves a moved folder still serves its files.

### Loopback plus a request guard

**Decision.** Listen on `127.0.0.1`, refuse any request whose `Host` is not a
loopback name, and refuse any state change that a browser marks as
cross-site (`Sec-Fetch-Site`, falling back to `Origin`).

**Why.** Loopback keeps the network out but not the browser: any website the
person visits can make the browser send a form to `127.0.0.1:5252`. Without
the guard, a hostile page could erase everything with one hidden form. DNS
rebinding is the second path in, and the `Host` check closes it.

**Cost accepted.** Scripts on the same computer can still post, by design;
they already run as the person.

### The Advisor runs through the vendor's own CLI

**Decision.** The Advisor calls the `claude` or `codex` command-line tool
with one prompt on stdin, all of the tool's own tools switched off, in an
empty temporary folder. The model replies with JSON:
`{"answer": ..., "actions": [...]}`, and the app applies the actions through
the same functions the pages use.

**Why.** Soldiers have Claude Pro or ChatGPT Plus. They do not have API keys
or business accounts. Each vendor's CLI signs in with the consumer plan.

**Alternatives considered.**

| Option | Why it lost |
|---|---|
| Vendor API keys | Pay-per-token billing that most soldiers will not set up |
| Cloud AI platforms such as Vertex AI | Need a cloud project and an organization account |
| Letting the CLI edit files directly | The model would hold write access to medical records |

**Cost accepted.** A few seconds of CLI startup per question, and the reply
format depends on the model following instructions. A reply that is not JSON
is shown as plain text and changes nothing.

**Prompt injection.** Document names, job postings, and notes flow into the
prompt, so text inside them could tell the model to delete things. The app
applies a `delete_*` action only when the person's own question contains
"delete" or "remove", and never more than 12 actions per question.

### Resume PDFs through a headless browser

**Decision.** The resume renders as an HTML page at US Letter size. Chrome,
Edge, or Brave on the person's computer prints it to PDF in the background.

**Why.** The preview and the PDF use the same HTML and CSS, so they match
exactly, fonts and colors included, with no PDF library.

**Near miss.** Headless Chrome writes the PDF and then does not exit, and its
crash reporter keeps the stderr pipe open, so `cmd.Run` waited out a
60-second timeout on every download. The app now sends stderr to a file,
watches for the finished file's `%%EOF` marker, and kills the process
group. Downloads take about half a second.

## Cross-cutting concerns

**Security.** See [SECURITY.md](../SECURITY.md). Uploaded HTML and SVG never
render as the app: only PDFs and images display, and everything else
downloads under a sandbox policy. Keys never appear in page source.

**Privacy.** Outbound traffic is opt-in and listed in the README. The app
has no telemetry and no update check.

**Time.** Due dates are calendar days in the person's own time zone.
`time.Parse` would place them at UTC midnight, which made "due today" read
as overdue after 7 PM in the United States.

**Recovery.** Backup is one zip. Restore validates the whole zip first,
refuses path tricks and stray files, and keeps the previous data beside the
new data, so a mistaken restore is reversible by hand.

## Testing

| Layer | What it proves |
|---|---|
| Unit tests | Money parsing and formatting, VA combined-rating math, retired pay, federal and state tax on retired pay, leave projection, avalanche payoff, savings, quick search, date and path handling |
| Journey tests | A full app against a temp folder: first run, demo data, daily use on every page, uploads, backup and restore, erase, and hostile requests |
| Guard tests | DNS rebinding, cross-site forms, `null` origins, and health probes |
| CI | `gofmt`, `go vet`, `go test -race`, builds for five platforms, and a container health check |

A journey test found a real crash: the Budget page failed on a brand-new
install because a template read a value that only existed once a goal was
set. Unit tests of the budget math had all passed.

## Open questions

- **Signing.** Unsigned binaries trigger Gatekeeper and SmartScreen warnings.
  Signing needs paid developer accounts.
- **Housing data.** The Housing tab reads realtor.com's public search, which
  is unofficial and can change without notice.
- **Other branches.** The timeline and PAR steps follow the Army process.
  Branch-specific templates would need contributors from those branches.
