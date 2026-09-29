# Changelog

## Unreleased

### Added
- VA pay per month is calculated from your estimated rating and dependents,
  using VA's rate table effective December 1, 2025.
- High-3 is calculated from your pay grade and years of service, month by
  month, on the DoD basic pay tables (2026 enlisted is the official DFAS
  table). Years with no published table use an assumed raise you can change.
- Weather on the dashboard for where you are now and where you plan to
  retire, from Open-Meteo. Works with a US ZIP code or any city worldwide.

### Changed
- The Budget page's live clock is now the Money Countdown. With debt it counts
  to your debt-free date; without debt it counts to retirement day and your
  savings goal.
- The retirement request window is 24 to 12 months before the retirement
  date, per Army Directive 2026-08 (17 April 2026). The old 9-month cutoff and
  Letter of Lateness guidance are gone.
- The timeline card is named Retirement Timeline, and its description explains
  how to change it.
- The weather card sits at the top of the dashboard in a compact layout.
- Money is split into four tabs: Budget (bills, pay, and spending), Debt (the
  payoff countdown, avalanche, and schedule), Savings (the savings goal and
  the countdown to retirement day), and Retired Pay (the pension, High-3, and
  VA pay). The Debt tab now shows the add-a-debt form even with no debts.
- The selected resume no longer uses a one-off thick edge; it matches the rest
  of the app.

## 1.0.0-beta.1 - 2026-09-29

The first public release.

### Added
- Renamed to ARW (Army Retirement Workbench). Downloads for Mac (one
  universal ARW.app with an icon), Windows (x64 and ARM64), and Linux (x64
  and ARM64), built by scripts/build-release.sh, with checksums.
- Quit ARW in Settings; opening ARW while it runs brings up the running copy.
- Housing tab: live for-sale listings around a ZIP code, refreshed every 30
  minutes. Sold, pending, and off-market homes drop out on their own.
- Notes tab: dated notes by topic that open, save, and close in place.
- Money Saved: a savings log with a named goal, on Budget and the Dashboard.
- PAR walkthrough: the Submit Packet steps and checklist as cards you mark
  complete, with your submission window worked out from your retirement date.
- Resume designs: Classic, Modern, Executive, Minimal, and Sidebar, each in
  light or dark, saved per resume.
- Resume PDF download in the chosen design. The app prints it through
  Chrome, Edge, or Brave on your computer, or opens a print-ready page
  where there is none. This replaces the Markdown export.
- Advisor on Claude (Pro or Max) or ChatGPT (Plus or Pro) through each
  company's own command-line app. No API keys.
- Job search across sources: USAJOBS and Adzuna results in one list, with a
  Track It button on each, plus one-click searches on LinkedIn, Indeed,
  ClearanceJobs, Glassdoor, ZipRecruiter, Dice, and Google Jobs. Search near a
  ZIP code within a radius, remote only, or both; saved searches keep all three.
- SkillBridge tab: search official programs and job boards, track leads,
  work the application packet, and download a filled-in request memorandum.
- Dashboard tiles for days to PTDY and days to SkillBridge, and numbers that
  count up on load.
- Retired pay estimate: High-3 or BRS pension, SBP, and VA pay with CRDP.
- Backup and restore of all data and documents in one zip.
- Quick search on every page with Cmd+K or Ctrl+K.
- Resume fonts (six open-licensed families) and color schemes, per resume.
- Notices after saves, and clear errors for bad dollar amounts and dates.
- A container image and compose file with a persistent data volume.
- A test suite, including an end-to-end journey through every page.

### Changed
- New sidebar layout with a clean neutral palette in light and dark.
- Your data now lives in your per-user app data folder by default, never in
  the app's folder. Existing `./data` folders keep working.
- Dollar amounts show thousands separators.
- Dates count calendar days in your own time zone.
- The seeded timeline is a general Army plan, not one person's.

### Removed
- Google Vertex AI and Anthropic API key support.

### Security
- Refuses cross-site requests and DNS rebinding.
- Keeps API keys out of page source.
- Serves uploaded files safely.
- Makes the data folder private to your account.
- Commits a save only after it reaches the disk.
- Erase Everything now deletes uploaded files too.
