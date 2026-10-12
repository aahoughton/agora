package web

import (
	"bytes"
	"html/template"

	"github.com/aahoughton/agora/internal/markdown"
)

// renderMarkdown renders a message body to safe HTML.
func renderMarkdown(body string) template.HTML {
	var b bytes.Buffer
	if err := markdown.Parser.Convert([]byte(body), &b); err != nil {
		return template.HTML("<p>" + template.HTMLEscapeString(body) + "</p>")
	}
	return template.HTML(b.String())
}
