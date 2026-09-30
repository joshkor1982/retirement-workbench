# Changelog

## Unreleased

### Added
- Retired Pay: Home, RV, and Deductions. Check "I'll own a home" and "I'll
  own an RV" to see the monthly payment (principal and interest, property
  tax, insurance, HOA, and the RV loan), what is left of take-home after
  housing, cash to close against your savings, and whether itemizing the
  mortgage and RV interest, state and local taxes (the RV's sales tax
  counts), and gifts beats the standard deduction, with the federal tax it
  saves. The rate and closing costs come from your lowest lender quote.
- Neighborhood Map on Housing: homes for sale as pins, FEMA flood zones,
  2020 Census population density by tract, NCES public schools, FBI crime
  rates by police agency against the state and national rates, and
  OpenStreetMap places (grocery, shopping, dining, hospitals, pharmacies,
  parks, fitness, gas, libraries, fire and police, childcare, places of
  worship, military). Layers load only when switched on, for the area in
  view, through the app's own cached endpoints. `?layers=` bookmarks a view.
  An optional free api.data.gov key in Settings lifts the crime data's demo
  limit.
- Agents & Lenders (under Next Home): searches for agents near your Housing
  ZIP code and VA's own home loan pages (COE, funding fee, lender
  statistics), NMLS license lookup, and CFPB's Loan Estimate guide; a tracker
  for the agents you interview (MRP certification, VA buyers closed, status)
  and the lenders who quote you, sorted by APR with the lowest marked; and a
  VA funding fee estimate from VA's published table, $0 with a VA rating.
- Medical / VA has a Claim Evidence card near the top: upload DBQs, medical
  records, and other evidence (buddy statements, photos, letters), several
  files at a time, and see and delete them in three lists. Analyze the Claim
  and Find Conditions in My Records read all three.
- Topic and company pages. Every skill on Learning opens its own page with
  a handbook (a long-form guide with contents, labs, interview questions,
  and a cheat sheet) and a references list you can add to or trim. Every
  company opens a page with its profile, recommended books, references, and
  its topics with progress. Generate Handbook and Generate Profile have the
  Advisor write one where none exists. Handbooks live in the docs folder, so
  backup and restore include them, and the Documents page lists them all.
- Generate buttons. On Learning, Generate Topics has the Advisor add what a
  company expects for your target role that you have not listed, reading the
  job posting, and notes what you already bring. On a resume, Generate
  Headline, Generate Section, and Generate on any section (rewrite it for
  the posting). Every prompt uses only facts from your own data.
- Companies and resumes are linked: adding a company starts a tailored
  resume, and starting a resume adds its company to Learning. The job posting
  is shared between them, and Add Mastered Skills writes a Technical Skills
  section from what you have mastered for that company.
- Learning (under Career): list the companies you want to work for and the
  skills, protocols, and tools to master at each, grouped by area with a
  link to where you will learn them. Click a skill's status to move it To
  Learn, Learning, Mastered; each company shows its progress. Skills show up
  in quick search, and the Advisor can add them for you.
- Find Conditions in My Records on Medical / VA: the Advisor reads your
  uploaded medical records and DBQs and lists conditions they document that
  are not on your claim list, each with the document and date it came from.
  Check the ones that fit and Add Selected puts them on the list, marked as
  in the record, with the source as the note. Nothing is added on its own.
- Everything you can add, you can edit. Conditions, medications, symptom
  entries, job prospects and contacts, saved job searches, SkillBridge leads,
  Who to Call offices, resource sites, savings entries, and recorded money all
  have an Edit button that opens the item's fields prefilled. Prospects and
  leads can move to any status, including back a step.
- Retired Pay counts health coverage: the 2026 TRICARE Prime or Select
  enrollment fee for your group (A or B, worked out from when you joined) and
  family size, Medicare Part B for TRICARE For Life, typical FEDVIP dental and
  vision premiums, and any other premium you enter. The top tile is now
  Take-Home Each Month, after tax and health coverage.
- Retired Pay takes an estimated civilian salary. Federal and state tax are
  worked out on retired pay and the job together, Social Security and
  Medicare come out of the job only, and Each Month After Tax becomes your
  full take-home: retired pay, job, and VA pay.
- Retired Pay estimates federal and state income tax on your retired pay,
  using the 2026 federal brackets, every state's 2026 brackets, and each
  state's rule for military retired pay (no tax, fully exempt, partial, or
  fully taxed). The state comes from where you plan to retire, or you pick it.
  Each Month now shows pay after tax, and the comparison with today's
  take-home uses it.
- Appointments have a Done button that turns green when you finish one (click
  again to reopen), and an Edit button to change the title, time, place, or
  notes. Finished appointments drop off the dashboard.
- VA pay per month is calculated from your estimated rating and dependents,
  using VA's rate table effective December 1, 2025.
- High-3 is calculated from your pay grade and years of service, month by
  month, on the DoD basic pay tables (2026 enlisted is the official DFAS
  table). Years with no published table use an assumed raise you can change.
- Weather on the dashboard for where you are now and where you plan to
  retire, from Open-Meteo. Works with a US ZIP code or any city worldwide.

### Changed
- Retired Pay shows TRICARE and dental costs from the first visit. Before
  years of service were entered, the health section showed a blank plan and
  the wrong TRICARE group.
- Resumes can be deleted again. The Delete button sat inside the Save form,
  which browsers do not allow, so it saved instead of deleting. It is now its
  own form, and each resume in the list has a delete as well.
- Bills can be renamed in place and saved with the amount. Debts have an
  Edit button that opens the account name, APR, balance, and minimum in one
  form, replacing the crowded Update column that pushed Delete off the edge.
- The interface font is Barlow, with Barlow Semi Condensed for headings and
  big numbers, and the small text is a size larger.
- Every card uses the same spacing between its parts, so the How This Works
  panels, tiles, and forms line up.
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
