-- Full-text search over message bodies. The search index reads its text
-- from messages by id, so messages becomes a rowid table with an explicit
-- id (VACUUM may renumber an implicit rowid, which would break the index).
-- (conversation_id, seq) stays unique and is how every read finds a
-- message.
CREATE TABLE messages_new (
    id              INTEGER PRIMARY KEY,
    conversation_id INTEGER NOT NULL REFERENCES conversations (id),
    seq             INTEGER NOT NULL,
    kind            TEXT    NOT NULL,
    author          TEXT    NOT NULL,
    body            TEXT    NOT NULL,
    reply_to        INTEGER,
    created_at      INTEGER NOT NULL,
    account_id      INTEGER REFERENCES accounts (id),
    idempotency_key TEXT,
    seen_seq        INTEGER,
    UNIQUE (conversation_id, seq)
);

INSERT INTO messages_new (conversation_id, seq, kind, author, body, reply_to, created_at, account_id, idempotency_key, seen_seq)
SELECT conversation_id, seq, kind, author, body, reply_to, created_at, account_id, idempotency_key, seen_seq
FROM messages ORDER BY conversation_id, seq;

DROP TABLE messages;
ALTER TABLE messages_new RENAME TO messages;

CREATE INDEX messages_observers ON messages (conversation_id, author, created_at)
    WHERE kind = 'observer';
CREATE UNIQUE INDEX messages_idempotency ON messages (conversation_id, idempotency_key)
    WHERE idempotency_key IS NOT NULL;

-- The index covers messages_searchable: every message but system lines.
-- Its tokens are whole words (unicode61, diacritics folded), with no
-- stemming. Because the index's content is that view, FTS5's rebuild and
-- integrity-check see exactly the indexed rows. Messages are only ever
-- added, so one trigger keeps the index current.
CREATE VIEW messages_searchable AS
    SELECT id, body FROM messages WHERE kind != 'system';

CREATE VIRTUAL TABLE messages_fts USING fts5(
    body,
    content = 'messages_searchable',
    content_rowid = 'id',
    tokenize = 'unicode61 remove_diacritics 2'
);

INSERT INTO messages_fts (messages_fts) VALUES ('rebuild');

CREATE TRIGGER messages_fts_insert AFTER INSERT ON messages
WHEN new.kind != 'system'
BEGIN
    INSERT INTO messages_fts (rowid, body) VALUES (new.id, new.body);
END;
