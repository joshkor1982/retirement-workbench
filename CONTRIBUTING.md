# Contributing

Thanks for helping make the transition easier for the next soldier.

## Ground rules

- **Never commit real personal data.** Your data lives outside the repo by
  default, and `data/` is git-ignored as a second guard. Check `git status`
  before every commit, and use **Load Example Data** for screenshots and bug
  reports. A feature that stores data stores it in the data folder
  (`s.data`), never in the repo.
- **Standard library only.** No third-party Go modules. The whole point is a
  single binary anyone can build and trust. If you reach for a dependency, find
  a stdlib way instead.
- **Keep it local-first.** The app binds to loopback. Any feature that sends
  data off the machine must be opt-in, clearly labeled, and off by default.

## How it fits together

- `main.go` holds the HTTP handlers, the JSON store (`mutate` and `snapshot`),
  the timeline engine, the budget engine, and the Advisor tools. Newer
  features live in their own files: `housing.go`, `savings.go`, `leave.go`,
  `notes.go`, `par.go`, `resume_style.go`, and `ai.go` for the Advisor.
- `mutate` changes a copy of the state and keeps it only after it is safely on
  disk. Always change state through it.
- Each page is one file in `templates/`, wrapped by the shared `head` and
  `foot` templates in `layout.html`. A new page needs an entry in `sideNav`
  (`nav.go`) and a title in the `page()` titles map.
- Styles use the tokens in `static/design-system.css`. Page styles go in
  `static/app.css`; the sidebar and top bar live in `static/shell.css`.

## Making a change

1. Fork and branch.
2. Run `gofmt -l .` (it must print nothing), `go vet ./...`, and
   `go test ./...`. CI runs the same checks.
3. Run it, and click through the pages you touched in light and dark mode.
4. Open a pull request describing the problem, the change, and how you checked
   it.

## Especially wanted

- Service-specific corrections (Navy, Air Force, Marines, Space Force, Coast
  Guard) - the timeline and forms lean Army today.
- Accessibility and plain-language improvements.
- Anything a Veterans Service Officer or transition counselor would fix.

## What to leave alone

This app organizes information; it does not give advice. Keep VA, medical, and
financial content descriptive and point users to the real authority (a VSO, the
transition office, a financial counselor). Don't add anything that would read as
official guidance.
