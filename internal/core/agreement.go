package core

import (
	"context"
	"regexp"
	"sort"
	"strings"
)

// position is a conclusion in an agreement conversation, read from its
// first line: "AGREED: <ref>" or "BLOCKED: <reason>".
type position struct {
	agent string
	// ref is what was agreed to, "#N" for a message or a lowercase
	// commit sha; empty when blocked or not in this form.
	ref     string
	blocked bool
}

var (
	seqRef = regexp.MustCompile(`^#[1-9][0-9]*$`)
	shaRef = regexp.MustCompile(`^[0-9a-f]{7,64}$`)
)

// errPositionForm is the error for a conclusion an agreement conversation
// cannot compare.
var errPositionForm = errorf(CodeInvalid,
	`in an agreement conversation a conclusion starts with "AGREED: <sha>", "AGREED: #<seq>" or "BLOCKED: <reason>"`)

// parsePosition reads the first line of a conclusion. After AGREED the
// ref is the first word, without quoting or punctuation around it, so
// "AGREED: `#53`, the final text" agrees to #53.
func parsePosition(body string) (position, error) {
	line, _, _ := strings.Cut(strings.TrimLeft(body, " \t\r\n"), "\n")
	line = strings.TrimSpace(line)
	if rest, ok := strings.CutPrefix(line, "BLOCKED:"); ok {
		if strings.TrimSpace(rest) == "" {
			return position{}, errPositionForm
		}
		return position{blocked: true}, nil
	}
	rest, ok := strings.CutPrefix(line, "AGREED:")
	if !ok {
		return position{}, errPositionForm
	}
	words := strings.Fields(rest)
	if len(words) == 0 {
		return position{}, errPositionForm
	}
	ref := strings.ToLower(strings.Trim(words[0], "`'\"()[].,;:!?"))
	if !seqRef.MatchString(ref) && !shaRef.MatchString(ref) {
		return position{}, errPositionForm
	}
	return position{ref: ref}, nil
}

// allBlocked reports whether every position is BLOCKED, which ends the
// conversation as surely as agreeing does.
func allBlocked(ps []position) bool {
	for _, p := range ps {
		if !p.blocked {
			return false
		}
	}
	return len(ps) > 0
}

// agreedRef returns what every position agreed to, or false when one is
// blocked or they differ. Shas agree when each is a prefix of the longest,
// so a short and a full sha of one commit match.
func agreedRef(ps []position) (string, bool) {
	longest := ""
	for _, p := range ps {
		if p.ref == "" {
			return "", false
		}
		if len(p.ref) > len(longest) {
			longest = p.ref
		}
	}
	for _, p := range ps {
		if p.ref != longest && (strings.HasPrefix(p.ref, "#") || !strings.HasPrefix(longest, p.ref)) {
			return "", false
		}
	}
	return longest, longest != ""
}

// positions returns the standing conclusion of each concluded participant:
// their latest conclude message. A participant without one (a hand-edited
// database) has a position that agrees to nothing.
func (s *Service) positions(ctx context.Context, tx *wtx, c *conv) ([]position, error) {
	rows, err := tx.QueryContext(ctx, `SELECT p.agent, coalesce(m.body, '') FROM participants p
		LEFT JOIN messages m ON m.conversation_id = p.conversation_id AND m.seq = (
			SELECT max(seq) FROM messages
			WHERE conversation_id = p.conversation_id AND author = p.agent AND kind = ?)
		WHERE p.conversation_id = ? AND p.state = 'concluded'`,
		KindConclude, c.id)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var ps []position
	for rows.Next() {
		var agent, body string
		if err := rows.Scan(&agent, &body); err != nil {
			return nil, err
		}
		p, err := parsePosition(body)
		if err != nil {
			// A conclusion from before conclusions were compared may not
			// be in the form; it agrees to nothing.
			p = position{}
		}
		p.agent = agent
		ps = append(ps, p)
	}
	sort.Slice(ps, func(i, j int) bool { return ps[i].agent < ps[j].agent })
	return ps, rows.Err()
}

// differ describes conclusions that do not agree, for the system line.
func differ(ps []position) string {
	parts := make([]string, len(ps))
	for i, p := range ps {
		switch {
		case p.blocked:
			parts[i] = p.agent + " blocked"
		case p.ref == "":
			parts[i] = p.agent + " named nothing to agree to"
		default:
			parts[i] = p.agent + " agreed to " + p.ref
		}
	}
	return "conclusions differ: " + strings.Join(parts, ", ") + "; all of them are active again"
}
