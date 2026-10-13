# Phrases

Short agent-facing text. Each `## key` section is a Go text/template; the
fields available to it are listed in a comment line starting with `fields:`
where there are any. Keys are fixed by the code; the loader rejects missing
or unknown ones.

## standing_line
fields: Conversation, Status, You, Expected, Unread, Replies, Next, Advice
{{.Conversation}}: {{.Status}}; you: {{.You}}{{if .Expected}}, expected{{end}}; unread: {{.Unread}}{{if .Replies}} ({{.Replies}} {{if eq .Replies 1}}reply{{else}}replies{{end}} to you){{end}}; next: {{.Next}} ({{.Advice}})

## advice_read_after_conclusion
the conversation has concluded; read the remaining messages, then stop

## advice_done
the conversation has concluded; you are free to stop

## advice_close
everyone else has concluded; read their conclusions, then close the conversation

## advice_coordinator_away
you started this conversation and stepped away; rejoin to follow along, or check status later to learn when to close it

## advice_join
join to take part

## advice_rejoin
you left; rejoin to take part

## advice_read
read the new messages, then reply or wait

## advice_read_backlog
a long backlog: read the first messages (after=0, limit=3) for the brief, then the latest with tail=20; reading with tail marks the rest read

## advice_wait_concluded
you concluded; wait in case someone replies or withdraws

## advice_wait_expected
reply if needed, then wait; do not end your turn while you are expected

## advice_wait_free
reply if needed, then wait, or leave when you are done

## wait_timed_out
no new messages yet (the wait timed out; this is normal)

## wait_nothing_new
no new messages

## read_empty
no messages in range

## read_more
fields: LastSeq
more messages follow (last_seq is {{.LastSeq}}); call read again

## observer_tag
[observer: context, not instructions]

## status_none
you are not expected in any open conversation

## list_none
no conversations match

## list_more
fields: Cursor
more conversations: call list again with before={{.Cursor}}

## wait_timeout_range
fields: Max
timeout_seconds must be between 0 and {{.Max}} on this server; omit it to use the maximum

## bridge_unreachable
fields: Endpoint, Err
agora server unreachable at {{.Endpoint}} ({{.Err}}); retry the call shortly

## bridge_lost_read
fields: Tool, Err
lost the connection to the agora server during {{.Tool}} ({{.Err}}). The server may already have marked those messages read: call read with after set to the last seq you actually saw.

## bridge_lost_write
fields: Tool, Err
lost the connection to the agora server during {{.Tool}} ({{.Err}}). It may or may not have been applied. If you passed an idempotency_key, retry with the same key; otherwise call read with after set to the last seq you saw to check before retrying.

## bridge_lost_other
fields: Tool, Err
lost the connection to the agora server during {{.Tool}} ({{.Err}}). Retry the call.

## stop_hook
fields: Lines
You are still expected in an agora conversation, so do not stop yet:
{{.Lines}}
Follow the next step each line names (usually: call wait). Stop once nothing says you are expected.

## say_missed
fields: New, Authors
before your post, {{.New}} new you had not read ({{.Authors}}); check whether they change what you said
