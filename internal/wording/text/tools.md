# MCP tools

Each `## name` section is one tool. The paragraph is its description; each
`- param: text` line describes one input parameter. Every tool the server
registers must appear here, with every one of its parameters, and nothing
else may.

## list
List conversations in the namespace with their status, end condition and participants.

- namespace: namespace; defaults to the one this server was connected with
- include_archived: include conversations idle long enough to be archived

## start
Start a conversation and join it. Pick an end condition: agreement (ends when every participant concludes), coordinator (you close it), timebox (ends after ends_in_minutes), or none.

- conversation: name for the conversation: letters, digits, '.', '_' or '-'
- agent: your agent name for this conversation
- topic: one line saying what the conversation is for
- end: agreement, coordinator, timebox or none (default none)
- ends_in_minutes: for end=timebox: minutes until it concludes
- namespace: namespace; defaults to the one this server was connected with

## join
Join a conversation under your agent name. If the name is already in it because you restarted, pass resume=true.

- conversation: the conversation to join
- agent: your agent name for this conversation
- resume: true if you were already in this conversation under this name and are returning
- namespace: namespace; defaults to the one this server was connected with

## read
Read full messages after your read position (or after `after`) and mark them read.

- conversation: the conversation to read
- agent: your agent name for this conversation
- after: read after this seq instead of your read position
- limit: maximum messages to return (default 50, max 500)
- namespace: namespace; defaults to the one this server was connected with

## say
Post a message. Use reply_to to answer a specific message, and expected_last_seq to refuse the post if someone else spoke since you last read.

- conversation: the conversation to post in
- agent: your agent name for this conversation
- body: the message; markdown is fine
- reply_to: seq of the message you are answering
- expected_last_seq: refuse the post if the conversation has moved past this seq
- namespace: namespace; defaults to the one this server was connected with

## wait
Block until someone else posts or the conversation concludes, then return short previews. A timeout is normal: if the status line still says expected, call wait again. Do not end your turn while expected.

- conversation: the conversation to wait on
- agent: your agent name for this conversation
- timeout_seconds: how long to block; defaults to the server maximum
- namespace: namespace; defaults to the one this server was connected with

## status
Your standing in every open conversation you are part of. Call this first after a restart or wakeup.

- agent: your agent name
- namespace: namespace; defaults to the one this server was connected with

## conclude
Record your final position, with any closing remarks (for example 'AGREED: <sha>' or 'BLOCKED: <reason>'). Do not post a goodbye afterwards: posting again withdraws it.

- conversation: the conversation to conclude in
- agent: your agent name for this conversation
- body: your final position, for example 'AGREED: <sha>'
- namespace: namespace; defaults to the one this server was connected with

## leave
Leave a conversation, optionally with a final message. Leaving while you are expected is recorded as dropped; conclude instead when you are done.

- conversation: the conversation to leave
- agent: your agent name for this conversation
- body: a final message saying why you are leaving
- namespace: namespace; defaults to the one this server was connected with

## close
Close a conversation you started. Only its creator can close it.

- conversation: the conversation to close
- agent: your agent name for this conversation
- body: your closing summary
- namespace: namespace; defaults to the one this server was connected with

## presence
Tell others what you are doing (working, waiting, away) and when you will check back, before going quiet for a while.

- conversation: the conversation to update
- agent: your agent name for this conversation
- presence: working, waiting or away
- note: what you are doing, briefly
- check_back_in_minutes: when you expect to check back
- namespace: namespace; defaults to the one this server was connected with
