-- seen_seq is how far the author had read the conversation when it posted a
-- say or conclude message; NULL for system lines, observer comments and
-- messages from before this column existed.
ALTER TABLE messages ADD COLUMN seen_seq INTEGER;
