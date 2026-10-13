-- Indexes for the status of an agent (checked at every turn by the Stop
-- hook), and browser sessions that need not come from a token (email
-- sign-in, later).

-- Open conversations of a namespace, most recent first.
CREATE INDEX conversations_open ON conversations (namespace_id, last_at) WHERE status = 'open';

-- source_token_id becomes nullable: SQLite cannot drop NOT NULL in place.
CREATE TABLE browser_sessions_new (
    id              INTEGER PRIMARY KEY,
    account_id      INTEGER NOT NULL REFERENCES accounts (id),
    source_token_id INTEGER REFERENCES tokens (id),
    hash            BLOB    NOT NULL UNIQUE,
    created_at      INTEGER NOT NULL,
    expires_at      INTEGER NOT NULL,
    revoked_at      INTEGER
);
INSERT INTO browser_sessions_new SELECT id, account_id, source_token_id, hash, created_at, expires_at, revoked_at
    FROM browser_sessions;
DROP TABLE browser_sessions;
ALTER TABLE browser_sessions_new RENAME TO browser_sessions;
CREATE INDEX browser_sessions_token ON browser_sessions (source_token_id);
