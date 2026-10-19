-- System messages not about one agent were written under the project's
-- earlier name. Give them the current system author. Agent messages keep
-- whatever author they had, even one that matches.
UPDATE messages SET author = 'smoot' WHERE kind = 'system' AND author = 'agora';
