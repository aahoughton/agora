-- Times are unix milliseconds (UTC).

CREATE TABLE namespaces (
    id         INTEGER PRIMARY KEY,
    name       TEXT    NOT NULL UNIQUE,
    created_at INTEGER NOT NULL
);

INSERT INTO namespaces (name, created_at)
VALUES ('default', CAST(unixepoch('subsec') * 1000 AS INTEGER));

-- end_condition: none | agreement | coordinator | timebox
-- status:        open | concluded
CREATE TABLE conversations (
    id            INTEGER PRIMARY KEY,
    namespace_id  INTEGER NOT NULL REFERENCES namespaces (id),
    name          TEXT    NOT NULL,
    topic         TEXT    NOT NULL DEFAULT '',
    end_condition TEXT    NOT NULL,
    ends_at       INTEGER,
    status        TEXT    NOT NULL DEFAULT 'open',
    created_by    TEXT    NOT NULL,
    created_at    INTEGER NOT NULL,
    concluded_at  INTEGER,
    last_seq      INTEGER NOT NULL DEFAULT 0,
    last_at       INTEGER NOT NULL,
    UNIQUE (namespace_id, name)
);

-- Listings page through a namespace by recent activity.
CREATE INDEX conversations_recent ON conversations (namespace_id, last_at, id);

-- The message log is append-only. kind: say | conclude | observer | system
CREATE TABLE messages (
    conversation_id INTEGER NOT NULL REFERENCES conversations (id),
    seq             INTEGER NOT NULL,
    kind            TEXT    NOT NULL,
    author          TEXT    NOT NULL,
    body            TEXT    NOT NULL,
    reply_to        INTEGER,
    created_at      INTEGER NOT NULL,
    PRIMARY KEY (conversation_id, seq)
) WITHOUT ROWID;

-- Derived from the log, updated in the same transaction as each append.
-- state:    active | concluded | left | dropped
-- presence: working | waiting | away
CREATE TABLE participants (
    conversation_id INTEGER NOT NULL REFERENCES conversations (id),
    agent           TEXT    NOT NULL,
    state           TEXT    NOT NULL,
    joined_at       INTEGER NOT NULL,
    read_seq        INTEGER NOT NULL DEFAULT 0,
    presence        TEXT    NOT NULL DEFAULT 'working',
    note            TEXT    NOT NULL DEFAULT '',
    check_back_by   INTEGER,
    last_seen_at    INTEGER NOT NULL,
    PRIMARY KEY (conversation_id, agent)
) WITHOUT ROWID;

-- Auth tables. Unused while auth mode is `none`.
CREATE TABLE accounts (
    id         INTEGER PRIMARY KEY,
    email      TEXT    UNIQUE,
    name       TEXT    NOT NULL,
    created_at INTEGER NOT NULL
);

CREATE TABLE tokens (
    id           INTEGER PRIMARY KEY,
    account_id   INTEGER NOT NULL REFERENCES accounts (id),
    hash         BLOB    NOT NULL UNIQUE,
    label        TEXT    NOT NULL DEFAULT '',
    created_at   INTEGER NOT NULL,
    last_used_at INTEGER,
    revoked_at   INTEGER
);

-- role: admin | member | viewer
CREATE TABLE memberships (
    namespace_id INTEGER NOT NULL REFERENCES namespaces (id),
    account_id   INTEGER NOT NULL REFERENCES accounts (id),
    role         TEXT    NOT NULL,
    PRIMARY KEY (namespace_id, account_id)
) WITHOUT ROWID;
