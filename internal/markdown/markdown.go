// Package markdown holds the Markdown dialect and link policy shared by the
// HTML viewer and the TUI.
package markdown

import (
	"net/url"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// Parser parses message bodies for every viewer: GitHub-flavored Markdown
// whose links keep their target only for allowed schemes, and whose images
// never load (each becomes a link to its source, or plain text). Its HTML
// renderer drops raw HTML (goldmark's default without WithUnsafe).
var Parser = goldmark.New(
	goldmark.WithExtensions(extension.GFM),
	goldmark.WithParserOptions(parser.WithASTTransformers(util.Prioritized(linkPolicy{}, 100))),
)

var allowedSchemes = map[string]bool{"http": true, "https": true, "mailto": true}

// AllowedURL reports whether a link target may be followed.
func AllowedURL(dest []byte) bool {
	u, err := url.Parse(strings.TrimSpace(string(dest)))
	return err == nil && allowedSchemes[strings.ToLower(u.Scheme)]
}

// linkPolicy rewrites links and images after parsing.
type linkPolicy struct{}

func (linkPolicy) Transform(doc *ast.Document, reader text.Reader, _ parser.Context) {
	var fix []ast.Node
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n := n.(type) {
		case *ast.Link:
			if !AllowedURL(n.Destination) {
				fix = append(fix, n)
			}
		case *ast.AutoLink:
			if !AllowedURL(n.URL(reader.Source())) {
				fix = append(fix, n)
			}
		case *ast.Image:
			fix = append(fix, n)
		}
		return ast.WalkContinue, nil
	})
	for _, n := range fix {
		parent := n.Parent()
		switch n := n.(type) {
		case *ast.Link:
			// Keep the link text, drop the link.
			for c := n.FirstChild(); c != nil; {
				next := c.NextSibling()
				parent.InsertBefore(parent, n, c)
				c = next
			}
			parent.RemoveChild(parent, n)
		case *ast.AutoLink:
			parent.ReplaceChild(parent, n, ast.NewString(n.URL(reader.Source())))
		case *ast.Image:
			alt := PlainText(n, reader.Source())
			if alt == "" {
				alt = "image"
			}
			if AllowedURL(n.Destination) {
				link := ast.NewLink()
				link.Destination = n.Destination
				link.AppendChild(link, ast.NewString([]byte("[image: "+alt+"]")))
				parent.ReplaceChild(parent, n, link)
			} else {
				parent.ReplaceChild(parent, n, ast.NewString([]byte("[image: "+alt+"]")))
			}
		}
	}
}

// PlainText concatenates the text under n.
func PlainText(n ast.Node, src []byte) string {
	var b strings.Builder
	_ = ast.Walk(n, func(c ast.Node, entering bool) (ast.WalkStatus, error) {
		if t, ok := c.(*ast.Text); ok && entering {
			b.Write(t.Segment.Value(src))
		}
		return ast.WalkContinue, nil
	})
	return b.String()
}
