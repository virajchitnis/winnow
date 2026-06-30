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
- **Morning briefing** — a polished daily HTML email (default 6am) with what was
  filed, what needs your attention, approvals waiting, stats, and cost/health.
  Doubles as a heartbeat. Optional, opt-in: a **synthesized digest of your
  newsletters** — Claude reads everything in your Newsletters folder and composes
  one themed read (the only feature that sends bodies to Claude).
- **A small web dashboard** to review/correct decisions and tune everything,
  reachable privately over Tailscale and (optionally) via a Cloudflare Tunnel.
  Server-rendered with a tiny self-hosted [htmx](https://htmx.org) for snappy
  in-place navigation (no SPA, no JSON API, no external calls). The Review tab is
  searchable and sortable, shows whether each decision came from a free heuristic
  or from Claude, and lets you **Teach** (learn a sender) or **Move & teach**
  (also re-file that email) per row.
- **One-time inbox sweep.** Preview classifications for your whole existing inbox
  (a side-effect-free dry read you can re-run), then either **apply the reviewed
  decisions** (file them using the categories you already saw — no new API calls)
  or re-sweep to re-classify. Separate from ongoing triage, which only touches
  new mail.

See the [full user guide](https://virajchitnis.github.io/winnow/guide.html) for
day-to-day usage, the go-live sequence, and troubleshooting.

## Quickstart

> You need only **Docker** on your server. Go is not required.

1. **Create a Fastmail API token** — *Settings → Privacy & Security → Manage API
   tokens*. Grant **Mail**, **Submission**, and **Sieve** scopes.

2. **Generate a dashboard password hash** (build the image first, or use a
   published release):

   ```sh
   docker build -t winnow .
   docker run --rm -it winnow hashpw
   # enter a password when prompted; copy the printed bcrypt hash
   ```

3. **Configure** — copy `.env.example` to `winnow.env` and fill in `FASTMAIL_TOKEN`,
   `ANTHROPIC_API_KEY`, `APP_PASSWORD_HASH`, and `SESSION_SECRET` (a random
   string, e.g. `openssl rand -hex 32`).

   > **Paste the bcrypt hash raw — do not wrap it in quotes and do not add an
   > inline `#` comment after it.** The hash contains `$` signs; the compose
   > file loads `winnow.env` with `format: raw` so those are preserved verbatim,
   > which means any surrounding quotes or trailing comment would become part of
   > the value and break login. Just `APP_PASSWORD_HASH=$2a$12$...`.
   >
   > The file is also named **`winnow.env`** (not `.env`) so Docker Compose
   > doesn't separately read it for YAML variable substitution.

4. **Run:**

   ```sh
   docker compose up -d
   ```

   The first run seeds the preset categories and starts in **dry-run** mode
   (nothing is moved). Open the dashboard at `http://localhost:8080`, review how
   Winnow *would* classify your mail, then turn off dry-run in **Settings** when
   you're happy.

5. **(Optional) expose the dashboard publicly** via the Cloudflare Tunnel +
   Access setup described below — otherwise it's reachable over your Tailnet
   only.

## Deploy to a remote server

`deploy.sh` syncs the source over SSH and builds + starts the container
remotely. Your server needs only Docker; Go is not needed there either.

1. Copy `.env.deploy.example` to `.env.deploy` (gitignored) and fill in your
   server details:

   ```sh
   cp .env.deploy.example .env.deploy
   # edit: DEPLOY_HOST, DEPLOY_USER, DEPLOY_PATH
   ```

2. Make sure your `winnow.env` with real secrets exists at `DEPLOY_PATH/winnow.env`
   on the server (copy it there once out-of-band — it is never synced by `deploy.sh`).

3. Run the deploy:

   ```sh
   bash deploy.sh
   ```

   This rsyncs the source (excluding secrets and local state), then runs
   `docker compose up -d --build` on the server.

## Cloudflare Tunnel + Access (optional)

This exposes the dashboard at a public hostname while keeping auth enforced at
Cloudflare's edge. The tunnel runs as a **separate container** alongside the app.

1. Create a tunnel in the [Cloudflare Zero Trust dashboard](https://one.dash.cloudflare.com),
   point it at `http://winnow:8080`, and copy the tunnel token.

2. Add to your `.env`:

   ```env
   TUNNEL_TOKEN=<your-cloudflare-tunnel-token>
   CF_ACCESS_TEAM_DOMAIN=<yourteam>.cloudflareaccess.com
   CF_ACCESS_AUD=<your-cloudflare-access-audience-tag>
   ```

3. Start with the tunnel profile:

   ```sh
   docker compose --profile tunnel up -d
   ```

4. In the Cloudflare Zero Trust dashboard, add an **Access Application** for
   your hostname and configure the identity provider (email OTP, Google SSO,
   etc.). Winnow will verify the `Cf-Access-Jwt-Assertion` header on every
   request — unauthenticated requests are rejected at both layers.

> Without these env vars set, `CF_ACCESS_*` verification is skipped — the tunnel
> container simply won't start if `TUNNEL_TOKEN` is absent.

## Privacy

Your Fastmail token and all mail storage stay on your server. For *ambiguous*
mail only, Winnow sends the **subject, sender, and a short snippet** (not full
bodies) to Anthropic's API to classify it. You can tighten this to
subject+sender-only in Settings. Heuristics and graduated Sieve rules keep most
mail off the API entirely.

The **one exception is opt-in**: if you turn on *newsletter summaries*, the
morning briefing sends the **bodies of your Newsletters-category mail** to Claude
to summarize them. It's **off by default**; leave it off to keep full bodies off
the API.

## Backup

All learned state (categories, sender stats, rule backups, decision log) lives
in the SQLite database on the `winnow-data` volume. Back it up with a volume
copy or `sqlite3 winnow.db ".backup backup.db"`.

## License

[MIT](LICENSE).
