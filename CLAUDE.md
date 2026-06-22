# CLAUDE.md

Guidance for working in this repository.

## What this is

**Winnow** is a self-hosted AI assistant that triages a Fastmail inbox over JMAP:
free header heuristics catch obvious bulk, ambiguous mail goes to Claude, and
mail is filed into category→folder mappings. It never deletes, biases toward
keeping mail in the inbox when unsure, and can graduate recurring senders into
the user's own Fastmail Sieve rules. Single Go binary, distroless container,
self-hosted via Docker.

## Layout

- `cmd/winnow/` — entrypoint; subcommands include `hashpw` (bcrypt the dashboard
  password) and `sweep`.
- `internal/jmap/` — JMAP client (session, `Email/query|get|set|changes`,
  `Mailbox`, submission, `SieveScript`). Header properties use the `:asRaw` form
  (Fastmail rejects `:asText` for `List-Unsubscribe` etc).
- `internal/store/` — SQLite + embedded migrations: decisions log, categories,
  sender stats, allow/deny rules, Sieve candidates/backups, unsubscribe state,
  settings (DB overrides env seeds).
- `internal/classify/` — heuristics + Claude classifier (HTTP, injected client).
- `internal/actions/` — applies plans over JMAP (move/flag/mark-read); honors
  dry-run; retry with backoff.
- `internal/schedule/` — triage (`Email/changes` + high-water mark), one-time
  sweep, daily digest + maintenance, single-flight run lock. `Refile` re-files a
  single email on demand; `ApplyReviewed` files previewed mail from the decision
  log (no re-classification).
- `internal/web/` — dashboard (server-rendered `html/template`, no SPA): Review,
  Categories, Senders, Rules, Unsubscribe, Settings; password auth + session;
  Cloudflare Access JWT middleware; `/healthz`.
- `internal/digest/`, `internal/sieve/`, `internal/unsubscribe/`, `internal/retry/`.
- `docs/` — GitHub Pages site (`index.html` + `guide.html`, the user guide).

## Key behaviors to preserve

- **Safety first.** Never delete mail. Below the confidence threshold, keep mail
  in the inbox unmoved. Dry-run and sweep *preview* must not mutate mail.
- **Dry-run scope.** `DryRun` is honored in exactly one place — `actions.Apply`
  (the automatic triage/sweep-preview pipeline). It does NOT gate explicit user
  actions, which act regardless: sweep *apply*, `ApplyReviewed` (apply reviewed
  decisions — files previewed mail from the log with no new LLM calls), `Refile`
  (Move & teach), Sieve
  rule apply (writes to Fastmail; then Fastmail files server-side, fully outside
  Winnow), unsubscribe execution, Teach corrections, and the daily digest (gated
  by `DigestEnabled`). Keep it that way — anything the user clicks is deliberate.
  If you add a new explicit action, don't gate it on dry-run; if you add to the
  autonomous pipeline, it must honor dry-run.
- **Preview is a true dry read.** Learning side-effects (sender stats, Sieve
  candidates, unsubscribe metadata) and the processed-mark fire only when an
  action actually sticks — i.e. live triage or sweep *apply*, never preview /
  dry-run. `RecordDecision` keeps at most one `dry_run` row per email so previews
  are re-runnable without duplicating the log. See `record()` in
  `internal/schedule/triage.go`.
- **Sweep** is Inbox-only, newest-first, up to 10,000 messages, in chunks of 75,
  skipping already-processed mail. Sweep *apply* and `Refile` move mail even when
  the global dry-run toggle is on (explicit user actions).
- **Sieve safety.** Only ever edit the delimited managed block; validate before
  activating; back up the prior script.
- **Corrections are soft.** Review's Teach / Move & teach record a sender
  *observation* only (`teach()` in `internal/web/handlers.go`) — never a blanket
  allow/deny rule. The classifier auto-applies a category only once a
  sender/domain is consistently it (`dominantCategory`: ≥5 obs, ≥80%), so a
  mixed sender keeps per-email AI judgment. Hard always-on overrides are an
  explicit user action in the Senders tab. Don't make corrections write
  `sender_rules`.

## Build / test / run

Use the Makefile (Go 1.23+; if `go` isn't on `PATH`, invoke the local toolchain
binary directly):

- `make build` — compile.
- `make race` — `go test -race ./...`.
- `make cover` — tests + **coverage gate, fails below 80%** (`./internal/...`).
  CI enforces this, so keep it green; add tests when adding code.
- `make vet` / `make fmtcheck` — CI also runs `go vet` and `gofmt`; run
  `gofmt -w` before committing.
- `make all` — fmtcheck + vet + race + cover (what to run before pushing).
- **Browser E2E** (`e2e/`, build tag `e2e`, Playwright via `playwright-go`):
  `go test -tags e2e ./e2e/...` after `playwright install --with-deps chromium`.
  Drives the dashboard headless against an in-process server (real SQLite, fake
  scheduler). Excluded from the default suite/coverage; runs in its own CI job.
  To run it without a local browser, use a container, e.g.:
  `docker run --rm -v "$PWD":/src -w /src golang:1.26-bookworm bash -c \
   'go run github.com/playwright-community/playwright-go/cmd/playwright install --with-deps chromium && go test -tags e2e ./e2e/...'`

No Go is required to *run* Winnow — builds happen inside Docker
(`docker compose up -d --build`). The dashboard binds `0.0.0.0:8080` in the
container.

## Secrets / config

- Real secrets live in **`winnow.env`** (gitignored), never `.env` — Docker
  Compose auto-reads a file literally named `.env` for YAML substitution and
  would mangle the `$` signs in the bcrypt hash. The compose `env_file` uses
  `format: raw`, so paste `APP_PASSWORD_HASH` raw with **no quotes and no inline
  `#` comment** (they'd become part of the value).
- DB settings override env seeds at runtime (live-editable in Settings); env
  values are only defaults.

## Deploy

`deploy.sh` rsyncs the source and runs `docker compose up -d --build` on a remote
host. It reads host/user/path from a **gitignored `.env.deploy`** — server
details must never be committed.

## Conventions

- **Commit as the repository owner's own git identity** (the global git config is
  authoritative). Never commit under a tool/bot identity.
- This repo is intended to be public: **no personal or secret data in any commit
  or in `docs/`** — no real hostnames, emails, tokens, or real-inbox screenshots.
  Examples use placeholders (`winnow.example.com`, `<owner>`); the GHCR owner is
  derived from `github.repository_owner` in CI.
- Match the surrounding style; keep handlers server-rendered; whitelist any
  user-supplied SQL sort/column inputs and parameterize values.
- When changing dashboard behavior, update `docs/guide.html` and `README.md`.
