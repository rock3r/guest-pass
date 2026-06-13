-- migration 0001 — initial schema (ARCHITECTURE.md §6 / AD-17 / AD-25).
-- All tables are STRICT (AD-25): SQLite enforces declared column types instead of
-- coercing. Every column is TEXT/INTEGER, which STRICT permits. IDs are UUIDv4 TEXT;
-- timestamps are INTEGER Unix-seconds UTC (EN-25); token columns hold
-- HMAC(server_secret, token) (EN-5). Schema is kept broadly Postgres-portable; the
-- STRICT keyword itself is the only SQLite-only construct.

CREATE TABLE hosts (
    id          TEXT    PRIMARY KEY,            -- UUIDv4
    google_sub  TEXT    NOT NULL UNIQUE,
    email       TEXT    NOT NULL,
    name        TEXT    NOT NULL,
    picture     TEXT,
    is_admin    INTEGER NOT NULL DEFAULT 0,     -- bool 0/1
    status      TEXT    NOT NULL DEFAULT 'pending'
                CHECK (status IN ('pending','active','suspended')),
    created_at  INTEGER NOT NULL                -- Unix seconds UTC
) STRICT;

CREATE TABLE streams (
    id               TEXT    PRIMARY KEY,
    host_id          TEXT    NOT NULL REFERENCES hosts(id) ON DELETE CASCADE,
    title            TEXT    NOT NULL,
    scheduled_at     INTEGER,
    duration_min     INTEGER,
    status           TEXT    NOT NULL DEFAULT 'draft'
                     CHECK (status IN ('draft','scheduled','live','ended')),
    max_res          INTEGER,                   -- stream-wide quality ceiling (D-19)
    max_fps          INTEGER,
    max_bitrate_kbps INTEGER,
    twitch_yt_channel  TEXT,                     -- linked-channel live-verify (D-29)
    twitch_yt_platform TEXT
                     CHECK (twitch_yt_platform IN ('twitch','youtube') OR twitch_yt_platform IS NULL),
    created_at       INTEGER NOT NULL
) STRICT;
CREATE INDEX idx_streams_host ON streams(host_id);

CREATE TABLE slots (                            -- host-global pool, wired into OBS once (D-20)
    id                TEXT    PRIMARY KEY,
    host_id           TEXT    NOT NULL REFERENCES hosts(id) ON DELETE CASCADE,
    kind              TEXT    NOT NULL CHECK (kind IN ('cam','host','screenshare')),
    idx               INTEGER,                  -- cam slots 1..8; NULL for host/screenshare
    source_token_hash TEXT    NOT NULL,         -- HMAC(secret,token); permanent, host-only (EN-5)
    source_token_last_used_at   INTEGER,        -- leak-detection metadata (EN-5/AD-23)
    source_token_last_source_ip TEXT,           -- leak-detection metadata (EN-5/AD-23)
    epoch             INTEGER NOT NULL DEFAULT 0, -- in-memory authoritative; persisted at lifecycle edges only (RF-6)
    -- Slot shape (D-20): cam slots are addressable 1..8; the optional host slot (D-18)
    -- and the single shared screenshare slot (D-21) carry no idx. The explicit
    -- `idx IS NOT NULL` is load-bearing: without it a cam row with NULL idx makes the
    -- clause evaluate to NULL, which SQLite treats as a passing CHECK.
    CHECK ((kind = 'cam' AND idx IS NOT NULL AND idx BETWEEN 1 AND 8)
        OR (kind IN ('host','screenshare') AND idx IS NULL))
) STRICT;
CREATE INDEX idx_slots_host ON slots(host_id);
CREATE UNIQUE INDEX idx_slots_source_token ON slots(source_token_hash);  -- slot WS auth (/ws?src=) lookup
-- Host-global pool uniqueness (D-20): at most one cam slot per (host, idx),
-- and at most one host slot + one screenshare slot per host.
CREATE UNIQUE INDEX idx_slots_cam ON slots(host_id, idx) WHERE kind = 'cam';
CREATE UNIQUE INDEX idx_slots_singleton ON slots(host_id, kind) WHERE kind IN ('host','screenshare');

CREATE TABLE passes (
    id           TEXT    PRIMARY KEY,
    stream_id    TEXT    NOT NULL REFERENCES streams(id) ON DELETE CASCADE,
    slot_id      TEXT    REFERENCES slots(id) ON DELETE SET NULL,  -- per-pass slot binding (D-20); same-host validated in app (RF-2)
    name         TEXT,                          -- guest PII, purged 24h post-stream (D-37)
    email        TEXT,                          -- guest PII, purged 24h post-stream (D-37)
    role         TEXT    NOT NULL DEFAULT 'guest' CHECK (role IN ('guest','cohost')),
    token_hash   TEXT    NOT NULL,              -- HMAC(secret,token) (EN-5)
    can_screen   INTEGER NOT NULL DEFAULT 0,
    status       TEXT    NOT NULL DEFAULT 'created'
                 CHECK (status IN ('created','sent','opened','accepted','expired','revoked')),
    sent_at      INTEGER,
    expires_at   INTEGER,
    opened_at    INTEGER,
    accepted_at  INTEGER,
    revoked_at   INTEGER
) STRICT;
CREATE INDEX idx_passes_stream ON passes(stream_id);
CREATE UNIQUE INDEX idx_passes_token ON passes(token_hash);   -- magic-link auth lookup (GET /p/{token})
-- at most one active occupant per slot per stream (RF-2)
CREATE UNIQUE INDEX idx_passes_active_slot ON passes(stream_id, slot_id)
    WHERE slot_id IS NOT NULL AND status NOT IN ('revoked','expired');

CREATE TABLE host_source_tokens (               -- D-18 host cam/screen routing, no pass
    id         TEXT NOT NULL PRIMARY KEY,
    stream_id  TEXT NOT NULL REFERENCES streams(id) ON DELETE CASCADE,
    role       TEXT NOT NULL CHECK (role IN ('host','obs','obs_screen')),
    token_hash TEXT NOT NULL,                   -- per-stream, hashed (EN-5)
    token_last_used_at   INTEGER,               -- leak-detection metadata (EN-5/AD-23)
    token_last_source_ip TEXT                   -- leak-detection metadata (EN-5/AD-23)
) STRICT;
CREATE INDEX idx_host_source_tokens_stream ON host_source_tokens(stream_id);
CREATE UNIQUE INDEX idx_host_source_tokens_token ON host_source_tokens(token_hash);  -- source WS auth lookup
-- One active value per role per stream (EN-5): reissuing/rotating replaces the row
-- rather than leaving a second valid token for the same (stream, role).
CREATE UNIQUE INDEX idx_host_source_tokens_stream_role ON host_source_tokens(stream_id, role);

CREATE TABLE sessions (
    id         TEXT    PRIMARY KEY,
    stream_id  TEXT    NOT NULL REFERENCES streams(id) ON DELETE CASCADE,
    host_id    TEXT    NOT NULL REFERENCES hosts(id) ON DELETE CASCADE,  -- denormalized for the one-live invariant (RF-2)
    started_at INTEGER NOT NULL,
    ended_at   INTEGER,
    status     TEXT    NOT NULL DEFAULT 'active' CHECK (status IN ('active','ended'))
) STRICT;
CREATE INDEX idx_sessions_stream ON sessions(stream_id);
-- DB-enforced one-live-session-per-host (EN-2/D-20): at most one active session per host (RF-2).
CREATE UNIQUE INDEX idx_sessions_one_live ON sessions(host_id) WHERE status = 'active';

CREATE TABLE peers (
    id              TEXT    PRIMARY KEY,
    session_id      TEXT    NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    pass_id         TEXT    REFERENCES passes(id) ON DELETE SET NULL,  -- stable key for reconnect (D-40)
    role            TEXT    NOT NULL CHECK (role IN ('host','guest','cohost','obs','obs_screen')),
    connected_at    INTEGER NOT NULL,
    disconnected_at INTEGER,
    used_turn       INTEGER NOT NULL DEFAULT 0  -- NO on_stage (D-11); written once at disconnect (EN-11)
) STRICT;
CREATE INDEX idx_peers_session ON peers(session_id);

-- Suppression locks persisted so moderation survives a server restart (AD-22/D-13/EN-7).
CREATE TABLE pass_locks (
    pass_id            TEXT    NOT NULL REFERENCES passes(id) ON DELETE CASCADE,
    modality           TEXT    NOT NULL CHECK (modality IN ('mic','cam','share')),
    applier_rank_floor TEXT    NOT NULL CHECK (applier_rank_floor IN ('host','cohost')),
    applier_pass_id    TEXT    REFERENCES passes(id) ON DELETE SET NULL,  -- NULL = applied by host (no pass)
    created_at         INTEGER NOT NULL,
    PRIMARY KEY (pass_id, modality)
) STRICT;
