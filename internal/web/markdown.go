package web

import (
	"bytes"
	"html/template"

	"github.com/yuin/goldmark/parser"

	"github.com/aahoughton/agora/internal/core"
	"github.com/aahoughton/agora/internal/markdown"
)

// Message is a message as the message template sees it: the record's
// fields, and the conversation's People for marking mentions.
type Message struct {
	core.Message
	People markdown.People
}

// peopleOf returns the names c's messages can mention.
func peopleOf(c core.Conversation) markdown.People {
	ps := make([]string, len(c.Participants))
	for i, p := range c.Participants {
		ps[i] = p.Agent
	}
	os := make([]string, len(c.Observers))
	for i, o := range c.Observers {
		os[i] = o.Name
	}
	return markdown.NewPeople(ps, os)
}

// renderMarkdown renders a message body to safe HTML, marking mentions of
// people when given.
func renderMarkdown(body string, people ...markdown.People) template.HTML {
	var b bytes.Buffer
	body = markdown.StripBidi(body)
	var opts []parser.ParseOption
	if len(people) > 0 {
		opts = append(opts, markdown.WithPeople(people[0]))
	}
	if err := markdown.Parser.Convert([]byte(body), &b, opts...); err != nil {
		return template.HTML("<p>" + template.HTMLEscapeString(body) + "</p>")
	}
	return template.HTML(b.String())
}
