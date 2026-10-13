-- Token auth: server admins, disabled accounts, expiring tokens, browser
-- sessions, and the account behind each agent name, conversation and
-- message. Rows written under auth mode `none` keep NULL accounts.

ALTER TABLE accounts ADD COLUMN server_admin INTEGER NOT NULL DEFAULT 0;
ALTER TABLE accounts ADD COLUMN disabled_at INTEGER;
CREATE UNIQUE INDEX accounts_name ON accounts (name COLLATE NOCASE);

ALTER TABLE tokens ADD COLUMN expires_at INTEGER;
CREATE INDEX tokens_account ON tokens (account_id);

-- role: viewer | observer | member | admin
CREATE INDEX memberships_account ON memberships (account_id);

-- A browser session comes from a token pasted at login and ends with it.
CREATE TABLE browser_sessions (
    id              INTEGER PRIMARY KEY,
    account_id      INTEGER NOT NULL REFERENCES accounts (id),
    source_token_id INTEGER NOT NULL REFERENCES tokens (id),
    hash            BLOB    NOT NULL UNIQUE,
    created_at      INTEGER NOT NULL,
    expires_at      INTEGER NOT NULL,
    revoked_at      INTEGER
);
CREATE INDEX browser_sessions_token ON browser_sessions (source_token_id);

-- The account that owns an agent name in a conversation, for good.
ALTER TABLE participants ADD COLUMN account_id INTEGER REFERENCES accounts (id);
-- The account that started a conversation (its coordinator).
ALTER TABLE conversations ADD COLUMN created_by_account INTEGER REFERENCES accounts (id);
-- The account whose request appended a message.
ALTER TABLE messages ADD COLUMN account_id INTEGER REFERENCES accounts (id);
