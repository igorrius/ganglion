-- The open escalation episodes of the local modules (owner ruling of
-- 2026-11-10): a failing module escalates each kind once per active version
-- until a successful run clears it. The node keeps them here, not in the
-- module's own state, which the module can write, so a restart in the middle
-- of an episode does not escalate the kind again. opened_ms is Unix
-- milliseconds UTC.

CREATE TABLE module_escalations (
    module    TEXT    NOT NULL,
    kind      TEXT    NOT NULL,
    sha       TEXT    NOT NULL,
    opened_ms INTEGER NOT NULL,
    PRIMARY KEY (module, kind)
);
