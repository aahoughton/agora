package markdown

import (
	"strconv"

	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// Refs says which "#N" in a body refer to messages: N from 1 to below
// Below, each linked to Link(N). The zero Refs marks none.
type Refs struct {
	Below int64
	Link  func(seq int64) string
}

var refsKey = parser.NewContextKey()

// KindRef is the node kind of a Ref.
var KindRef = ast.NewNodeKind("Ref")

// Ref is "#N" referring to message N. Its child is the text "#N".
type Ref struct {
	ast.BaseInline
	Seq  int64
	Href string
}

// Kind implements ast.Node.
func (n *Ref) Kind() ast.NodeKind { return KindRef }

// Dump implements ast.Node.
func (n *Ref) Dump(src []byte, level int) {
	ast.DumpHelper(n, src, level, map[string]string{"Seq": strconv.FormatInt(n.Seq, 10)}, nil)
}

// refs marks "#N" in plain text. Code, links, autolinks and mentions are
// left alone. The "#" must start the text or follow a character that
// cannot be in a name or a path, so "repo#12" and "a/b#12" are not
// references, and the number must not run on into a letter. The text is
// still source here, so "&#39;" (an entity) and "\#5" (an escape) are
// skipped by the character before the "#".
type refs struct{}

func (refs) Transform(doc *ast.Document, reader text.Reader, pc parser.Context) {
	r, _ := pc.Get(refsKey).(Refs)
	if r.Link == nil || r.Below <= 1 {
		return
	}
	src := reader.Source()
	var texts []*ast.Text
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n := n.(type) {
		case *ast.CodeSpan, *ast.Link, *ast.AutoLink, *Mention:
			return ast.WalkSkipChildren, nil
		case *ast.Text:
			texts = append(texts, n)
		}
		return ast.WalkContinue, nil
	})
	for _, t := range texts {
		splitRefs(t, src, r)
	}
}

func digit(c byte) bool { return c >= '0' && c <= '9' }

// splitRefs replaces t with text and Ref nodes.
func splitRefs(t *ast.Text, src []byte, r Refs) {
	seg := t.Segment
	var nodes []ast.Node
	from := seg.Start
	for i := seg.Start; i < seg.Stop; i++ {
		if src[i] != '#' || (i > 0 && (nameByte(src[i-1]) || src[i-1] == '/' || src[i-1] == '&' || src[i-1] == '\\')) {
			continue
		}
		end := i + 1
		for end < seg.Stop && digit(src[end]) {
			end++
		}
		if end == i+1 || src[i+1] == '0' || (end < seg.Stop && nameByte(src[end]) && src[end] != '.' && src[end] != '-') {
			continue
		}
		seq, err := strconv.ParseInt(string(src[i+1:end]), 10, 64)
		if err != nil || seq >= r.Below {
			continue
		}
		if from < i {
			nodes = append(nodes, ast.NewTextSegment(text.NewSegment(from, i)))
		}
		ref := &Ref{Seq: seq, Href: r.Link(seq)}
		ref.AppendChild(ref, ast.NewTextSegment(text.NewSegment(i, end)))
		nodes = append(nodes, ref)
		from, i = end, end-1
	}
	splice(t, nodes, from)
}

// refHTML renders a Ref as <a class="ref" href="...">.
type refHTML struct{}

func (refHTML) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(KindRef, func(w util.BufWriter, _ []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			_, _ = w.WriteString("</a>")
			return ast.WalkContinue, nil
		}
		_, _ = w.WriteString(`<a class="ref" href="`)
		_, _ = w.Write(util.EscapeHTML([]byte(n.(*Ref).Href)))
		_, _ = w.WriteString(`">`)
		return ast.WalkContinue, nil
	})
}
