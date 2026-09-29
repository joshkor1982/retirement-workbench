# Security

Army Retirement Workbench holds some of the most sensitive records a person has:
medical history, finances, and service documents. Security reports get
priority.

## Reporting a problem

Report privately through **Security > Report a vulnerability** on this
repository. Do not open a public issue. Include the steps to reproduce and the
version (`retirement-workbench -version`). You should hear back within a week.

## How the app protects your data

- **Local only.** The server listens on `127.0.0.1` by default. Nothing on your
  network can reach it.
- **No accounts, no cloud.** Your data lives in one folder on your computer,
  readable only by your user account (folders `0700`, files `0600`).
- **Hostile web pages are refused.** Every change must come from the app's own
  pages (`Sec-Fetch-Site` and `Origin` checks). Requests addressed to any host
  other than localhost are refused, which blocks DNS rebinding.
- **Uploads cannot run as the app.** Only PDFs and images display in the
  browser. Anything else downloads, sandboxed.
- **Outbound traffic is opt-in.** The app contacts the internet only for the
  Advisor (the AI company you choose), USAJOBS searches, and housing listings,
  and only after you turn each one on.
- **The Advisor cannot delete on its own.** It deletes only when your own
  question asks for a deletion, so text inside a document cannot talk it into
  erasing your data.

## Known limits

- **There is no login.** Anyone who can use your computer account can open the
  app. If you run the container, keep the port on `127.0.0.1` or put a login
  proxy in front of it.
- **Advisor questions leave your computer.** They go to Anthropic or OpenAI,
  under that company's terms, along with a summary of your app data.
