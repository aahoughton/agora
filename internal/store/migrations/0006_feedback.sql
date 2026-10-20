-- Agent feedback on smoot itself: at most one note per agent per
-- conversation, which a later submit replaces. Only server admins read it.
-- seen_seq is the agent's read position when the note was last written.
CREATE TABLE feedback (
    id              INTEGER PRIMARY KEY,
    conversation_id INTEGER NOT NULL,
    agent           TEXT    NOT NULL,
    account_id      INTEGER REFERENCES accounts (id),
    seen_seq        INTEGER NOT NULL,
    body            TEXT    NOT NULL,
    created_at      INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL,
    UNIQUE (conversation_id, agent),
    FOREIGN KEY (conversation_id, agent) REFERENCES participants (conversation_id, agent)
);

CREATE INDEX feedback_recent ON feedback (updated_at, id);
