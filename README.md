# Winnow

**Winnow** is a self-hosted AI assistant for [Fastmail](https://www.fastmail.com)
that triages your inbox — it files low-value promotional mail into folders,
surfaces what actually matters, and learns to do more of the work server-side
over time. The name comes from *winnowing*: separating the grain from the chaff.

> Status: early development. Builds run inside Docker — you do **not** need Go
> installed to run Winnow, only Docker.

## What it does

- **Connects to Fastmail over JMAP** (Fastmail's native API — no fragile IMAP).
- **Cheap-first classification.** Free header heuristics catch the obvious bulk;
  only genuinely ambiguous mail is sent to Claude (Haiku by default), so API
  cost stays tiny.
- **Sorts into configurable categories → folders** (Promotional, Social,
  Newsletters, …). Important mail is flagged and kept in the inbox. **Nothing is
  ever deleted.**
- **Safety bias.** When the classifier isn't confident, mail is left in the
  inbox — so a stray promo is the worst case, never a hidden important email.
- **Graduates recurring senders into Fastmail's own Sieve rules** (with your
  approval) so they're filed server-side at delivery — free and always-on, even
  when Winnow is offline. Your existing hand-made rules are never touched.
- **Assisted unsubscribe** (opt-in, per-sender) using the safe standardized
  methods (RFC 8058 One-Click, `mailto:`).
- **Daily digest** of what was filed and what needs you (doubles as a heartbeat).
- **A small web dashboard** to review/correct decisions and tune everything,
  reachable privately over Tailscale and (optionally) via a Cloudflare Tunnel.

## Quickstart

1. **Create a Fastmail API token** — *Settings → Privacy & Security → Manage API
   tokens*. Grant **Mail**, **Submission**, and **Sieve** scopes.

2. **Generate a dashboard password hash:**

   ```sh
   docker run --rm ghcr.io/<owner>/winnow hashpw
   # enter a password when prompted; copy the printed bcrypt hash
   ```

3. **Configure** — copy `.env.example` to `.env` and fill in `FASTMAIL_TOKEN`,
   `ANTHROPIC_API_KEY`, `APP_PASSWORD_HASH`, and `SESSION_SECRET`.

4. **Run:**

   ```sh
   docker compose up -d
   ```

   The first run seeds the preset categories and starts in **dry-run** mode
   (nothing is moved). Open the dashboard, review how Winnow *would* classify
   your mail, then turn off dry-run in **Settings** when you're happy.

5. **(Optional) expose the dashboard publicly** via the Cloudflare Tunnel +
   Access setup described in [docs](docs/) — otherwise it's reachable over your
   Tailnet only.

## Privacy

Your Fastmail token and all mail storage stay on your server. For *ambiguous*
mail only, Winnow sends the **subject, sender, and a short snippet** (not full
bodies) to Anthropic's API to classify it. You can tighten this to
subject+sender-only in Settings. Heuristics and graduated Sieve rules keep most
mail off the API entirely.

## Backup

All learned state (categories, sender stats, rule backups, decision log) lives
in the SQLite database on the `winnow-data` volume. Back it up with a volume
copy or `sqlite3 winnow.db ".backup backup.db"`.

## License

[MIT](LICENSE).
