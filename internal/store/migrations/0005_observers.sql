-- Observer comments, for listing a conversation's observers without
-- reading its whole log.
CREATE INDEX messages_observers ON messages (conversation_id, author, created_at)
    WHERE kind = 'observer';
