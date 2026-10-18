package markdown

import (
	"strconv"

	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"

	"github.com/aahoughton/agora/internal/tone"
)

// People are the names a message body can mention, each mapped to whether
// it is an observer's. A body parsed without people has no mentions.
type People map[string]bool

// NewPeople returns the people of a conversation. A name that is both a
// participant's and an observer's counts as the participant's.
func NewPeople(participants, observers []string) People {
	p := People{}
	for _, n := range observers {
		p[n] = true
	}
	for _, n := range participants {
		p[n] = false
	}
	return p
}

var peopleKey = parser.NewContextKey()

// WithPeople makes a parse mark "@name" as a Mention for each of people.
func WithPeople(p People) parser.ParseOption { return With(p, Refs{}) }

// With makes a parse mark mentions of people and the references refs
// allows.
func With(p People, refs Refs) parser.ParseOption {
	ctx := parser.NewContext()
	ctx.Set(peopleKey, p)
	ctx.Set(refsKey, refs)
	return parser.WithContext(ctx)
}

// KindMention is the node kind of a Mention.
var KindMention = ast.NewNodeKind("Mention")

// Mention is "@name" for one of the people given to the parse. Its child
// is the text "@name".
type Mention struct {
	ast.BaseInline
	Name     string
	Observer bool
}

// Kind implements ast.Node.
func (n *Mention) Kind() ast.NodeKind { return KindMention }

// Dump implements ast.Node.
func (n *Mention) Dump(src []byte, level int) {
	ast.DumpHelper(n, src, level, map[string]string{"Name": n.Name}, nil)
}

// mentions marks "@name" in plain text. Code, links and autolinks are left
// alone. The "@" must start the text or follow a character that cannot be
// in a name, so an email address is not a mention, and "\@name" (an
// escape) is not one either. Trailing '.', '_' or
// '-' may end the sentence rather than the name, so "@ca-one." mentions
// ca-one.
type mentions struct{}

func (mentions) Transform(doc *ast.Document, reader text.Reader, pc parser.Context) {
	people, _ := pc.Get(peopleKey).(People)
	if len(people) == 0 {
		return
	}
	src := reader.Source()
	var texts []*ast.Text
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n := n.(type) {
		case *ast.CodeSpan, *ast.Link, *ast.AutoLink:
			return ast.WalkSkipChildren, nil
		case *ast.Text:
			texts = append(texts, n)
		}
		return ast.WalkContinue, nil
	})
	for _, t := range texts {
		splitMentions(t, src, people)
	}
}

func nameByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-'
}

// splitMentions replaces t with text and Mention nodes. Line breaks after
// t stay on the last node.
func splitMentions(t *ast.Text, src []byte, people People) {
	seg := t.Segment
	var nodes []ast.Node
	from := seg.Start
	for i := seg.Start; i < seg.Stop; i++ {
		if src[i] != '@' || (i > 0 && (nameByte(src[i-1]) || src[i-1] == '\\')) {
			continue
		}
		end := i + 1
		for end < seg.Stop && nameByte(src[end]) {
			end++
		}
		for end > i+1 && !people.has(src[i+1:end]) && (src[end-1] == '.' || src[end-1] == '_' || src[end-1] == '-') {
			end--
		}
		name := string(src[i+1 : end])
		observer, ok := people[name]
		if end == i+1 || !ok {
			continue
		}
		if from < i {
			nodes = append(nodes, ast.NewTextSegment(text.NewSegment(from, i)))
		}
		m := &Mention{Name: name, Observer: observer}
		m.AppendChild(m, ast.NewTextSegment(text.NewSegment(i, end)))
		nodes = append(nodes, m)
		from, i = end, end-1
	}
	splice(t, nodes, from)
}

// splice replaces t with nodes followed by the text of t from from on,
// which keeps t's line breaks. Without nodes, t stays.
func splice(t *ast.Text, nodes []ast.Node, from int) {
	if len(nodes) == 0 {
		return
	}
	last := ast.NewTextSegment(text.NewSegment(from, t.Segment.Stop))
	last.SetSoftLineBreak(t.SoftLineBreak())
	last.SetHardLineBreak(t.HardLineBreak())
	nodes = append(nodes, last)
	parent := t.Parent()
	for _, n := range nodes {
		parent.InsertBefore(parent, t, n)
	}
	parent.RemoveChild(parent, t)
}

func (p People) has(name []byte) bool {
	_, ok := p[string(name)]
	return ok
}

// mentionHTML renders a Mention as <strong class="mention tone-N">, or
// class "mention observer" for an observer, N being the participant's
// color (internal/tone).
type mentionHTML struct{}

func (mentionHTML) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(KindMention, func(w util.BufWriter, _ []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			_, _ = w.WriteString("</strong>")
			return ast.WalkContinue, nil
		}
		m := n.(*Mention)
		class := "tone-" + strconv.Itoa(tone.Of(m.Name))
		if m.Observer {
			class = "observer"
		}
		_, _ = w.WriteString(`<strong class="mention ` + class + `">`)
		return ast.WalkContinue, nil
	})
}
