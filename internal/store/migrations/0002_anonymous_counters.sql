-- Anonymous long-term aggregates (D-37 / M6). These tables intentionally have
-- NO foreign keys or entity dimensions: host erasure and guest-PII purge must
-- never remove or alter a contribution that was anonymized at event time.
CREATE TABLE counters (
    key   TEXT PRIMARY KEY,
    value INTEGER NOT NULL DEFAULT 0
) STRICT;

CREATE TABLE counters_daily (
    key   TEXT    NOT NULL,
    day   TEXT    NOT NULL, -- UTC YYYY-MM-DD
    value INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (key, day)
) STRICT;
