# Changelog

All notable changes are recorded here. Release notes are generated from
conventional-commit messages by GoReleaser on each tagged release; this file is
a human-maintained mirror.

## Unreleased

Initial complete implementation of Winnow:

- JMAP client for Fastmail (mail, submission, RFC 9661 Sieve).
- SQLite store with embedded migrations and all persistence.
- Cheap-first classifier (header heuristics + Claude) with a confidence gate
  that keeps low-confidence mail in the inbox.
- Action applier (move/flag/mark-read) with retry/backoff and dry-run.
- Triage scheduler with `Email/changes`, single-flight run lock, spend cap,
  graceful shutdown, and a one-time checkpointed inbox sweep.
- Managed-block Sieve rule generation that never touches the user's rules.
- Assisted unsubscribe (One-Click / mailto; bare HTTPS shown for manual use).
- Daily digest email (also a heartbeat).
- Responsive web dashboard with app-password + Cloudflare Access auth and all
  tabs (Review, Categories, Senders, Rules, Unsubscribe, Settings).
- Docker/compose/deploy, GitHub Actions CI (gofmt, vet, -race, 80% coverage
  gate, docker build, gitleaks, browser-e2e), GoReleaser releases to GHCR, and
  a GitHub Pages site.
