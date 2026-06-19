-- Winnow initial schema.

-- Live operational settings (key/value). The env seeds these on first boot;
-- afterwards the DB is authoritative and the dashboard edits them.
CREATE TABLE settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

-- Categories: how mail is sorted. Seeded with editable presets.
CREATE TABLE categories (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    name              TEXT NOT NULL UNIQUE,
    destination_folder TEXT NOT NULL DEFAULT '',
    keep_in_inbox     INTEGER NOT NULL DEFAULT 0,
    flag              INTEGER NOT NULL DEFAULT 0,
    mark_read         INTEGER NOT NULL DEFAULT 0,
    is_builtin        INTEGER NOT NULL DEFAULT 0,
    sort_order        INTEGER NOT NULL DEFAULT 0
);

-- Per-email decision log (append-only). Powers the dashboard + digest.
CREATE TABLE decisions (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    email_id        TEXT NOT NULL,
    thread_id       TEXT NOT NULL DEFAULT '',
    sender          TEXT NOT NULL DEFAULT '',
    subject         TEXT NOT NULL DEFAULT '',
    category        TEXT NOT NULL DEFAULT '',
    confidence      REAL NOT NULL DEFAULT 0,
    reason          TEXT NOT NULL DEFAULT '',
    summary         TEXT NOT NULL DEFAULT '',
    action          TEXT NOT NULL DEFAULT '',   -- moved | kept | flagged | dry_run | error
    low_confidence  INTEGER NOT NULL DEFAULT 0,
    used_llm        INTEGER NOT NULL DEFAULT 0,
    created_at      TEXT NOT NULL
);
CREATE INDEX idx_decisions_created ON decisions(created_at);
CREATE INDEX idx_decisions_email ON decisions(email_id);

-- Idempotency: email ids Winnow has already acted on.
CREATE TABLE processed (
    email_id   TEXT PRIMARY KEY,
    created_at TEXT NOT NULL
);

-- Sender → category observation stats, driving rule proposals + unsubscribe
-- ranking. Tracked per full sender address and rolled up by domain in queries.
CREATE TABLE sender_stats (
    sender     TEXT NOT NULL,
    domain     TEXT NOT NULL,
    category   TEXT NOT NULL,
    count      INTEGER NOT NULL DEFAULT 0,
    last_seen  TEXT NOT NULL,
    PRIMARY KEY (sender, category)
);
CREATE INDEX idx_sender_stats_domain ON sender_stats(domain);

-- Allow/deny overrides (always-important / always-bulk).
CREATE TABLE sender_rules (
    pattern    TEXT NOT NULL,          -- full address or @domain
    kind       TEXT NOT NULL,          -- allow_important | deny_bulk
    category   TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    PRIMARY KEY (pattern, kind)
);

-- Proposed/approved Sieve rule candidates (domain → category).
CREATE TABLE sieve_candidates (
    domain       TEXT NOT NULL,
    category     TEXT NOT NULL,
    observations INTEGER NOT NULL DEFAULT 0,
    status       TEXT NOT NULL DEFAULT 'proposed', -- proposed | approved | rejected
    updated_at   TEXT NOT NULL,
    PRIMARY KEY (domain, category)
);

-- Backups of the active Sieve script taken before each managed-block write.
CREATE TABLE sieve_backups (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    content    TEXT NOT NULL,
    created_at TEXT NOT NULL
);

-- Per-sender unsubscribe state. Metadata is persisted so a later unsubscribe
-- works without a fresh message in hand.
CREATE TABLE unsubscribe (
    sender     TEXT PRIMARY KEY,
    method     TEXT NOT NULL DEFAULT '',   -- one_click | mailto | http_manual
    target     TEXT NOT NULL DEFAULT '',   -- URL or mailto target
    status     TEXT NOT NULL DEFAULT 'needs_decision', -- needs_decision | kept | unsubscribed
    count      INTEGER NOT NULL DEFAULT 0,
    last_seen  TEXT NOT NULL,
    acted_at   TEXT,
    verified   INTEGER NOT NULL DEFAULT 0
);

-- Recent error state for the dashboard banner / digest errors line.
CREATE TABLE errors (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    kind       TEXT NOT NULL,
    message    TEXT NOT NULL,
    created_at TEXT NOT NULL,
    resolved   INTEGER NOT NULL DEFAULT 0
);

-- Daily LLM-call counter for the spend cap.
CREATE TABLE spend (
    day       TEXT PRIMARY KEY,   -- YYYY-MM-DD
    llm_calls INTEGER NOT NULL DEFAULT 0
);
