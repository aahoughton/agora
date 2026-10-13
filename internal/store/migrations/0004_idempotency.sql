-- A client-chosen key that makes retrying a post safe: a second post with
-- the same key in the same conversation returns the first.
ALTER TABLE messages ADD COLUMN idempotency_key TEXT;
CREATE UNIQUE INDEX messages_idempotency ON messages (conversation_id, idempotency_key)
    WHERE idempotency_key IS NOT NULL;
