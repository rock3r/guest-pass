-- Host-level defaults are intentionally separate from identity fields. They prefill new
-- streams and, when a host opts in, carry an encrypted BYO-TURN shared secret. The secret is
-- never returned from the host-facing settings read model or GDPR export.
CREATE TABLE host_preferences (
    host_id                        TEXT    PRIMARY KEY REFERENCES hosts(id) ON DELETE CASCADE,
    timezone                       TEXT    NOT NULL DEFAULT 'UTC',
    youtube_channel                TEXT,
    twitch_channel                 TEXT,
    default_channel_platform       TEXT    NOT NULL DEFAULT ''
                                        CHECK (default_channel_platform IN ('', 'youtube', 'twitch')),
    max_res                        INTEGER NOT NULL DEFAULT 720 CHECK (max_res > 0),
    max_fps                        INTEGER NOT NULL DEFAULT 30 CHECK (max_fps > 0),
    max_bitrate_kbps               INTEGER NOT NULL DEFAULT 2500 CHECK (max_bitrate_kbps > 0),
    custom_turn_enabled            INTEGER NOT NULL DEFAULT 0 CHECK (custom_turn_enabled IN (0, 1)),
    custom_turn_url                TEXT    NOT NULL DEFAULT '',
    custom_turn_secret_encrypted   TEXT    NOT NULL DEFAULT ''
) STRICT;
