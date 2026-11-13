-- The titles of the forum topics the node saw in connected groups (R-CFG-12).
CREATE TABLE group_topics (
    grp     TEXT NOT NULL,              -- the forum's key, e.g. telegram:-1001234567890
    thread  INTEGER NOT NULL,           -- the topic's id
    title   TEXT NOT NULL,              -- outside text, cut
    renamed INTEGER NOT NULL DEFAULT 0, -- the title came from a rename, not from the topic's root
    seen_ms INTEGER NOT NULL,
    PRIMARY KEY (grp, thread)
);
