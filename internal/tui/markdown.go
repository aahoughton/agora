package tui

import (
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/yuin/goldmark/ast"
	east "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"

	"github.com/aahoughton/agora/internal/markdown"
)

// minWidth is the narrowest column a body is wrapped to.
const minWidth = 10

// sanitize makes a body safe to print: it drops control characters (escape
// sequences above all) and bidirectional overrides, keeps newlines, and
// expands tabs.
func sanitize(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\n':
			b.WriteRune(r)
		case r == '\t':
			b.WriteString("    ")
		case r < 0x20, r >= 0x7f && r < 0xa0:
		case markdown.IsBidiControl(r):
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// renderBody renders a message body as lines at most width cells wide. The
// body is Markdown in the dialect of internal/markdown; links show their
// target in parentheses, code keeps its line breaks, and each word carries
// its own style so a wrapped line never leaks a style into the next.
// Mentions of people are bold in that person's color.
func renderBody(body string, width int, st styles, people markdown.People) []string {
	width = max(width, minWidth)
	src := []byte(sanitize(body))
	doc := markdown.Parser.Parser().Parse(text.NewReader(src), markdown.WithPeople(people))
	r := mdRenderer{src: src, st: st}
	lines := r.blocks(doc, width)
	if len(lines) == 0 {
		return []string{""}
	}
	// Deep nesting can indent past the column; cut what does not fit.
	for i, l := range lines {
		lines[i] = ansi.Truncate(l, width, st.g.more)
	}
	return lines
}

type mdRenderer struct {
	src []byte
	st  styles
}

// blocks renders the block children of n, separated by blank lines.
func (r mdRenderer) blocks(n ast.Node, width int) []string {
	var out []string
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		lines := r.block(c, width)
		if len(lines) == 0 {
			continue
		}
		if len(out) > 0 && !tightItem(c) {
			out = append(out, "")
		}
		out = append(out, lines...)
	}
	return out
}

// tightItem reports whether block c sits in a tight list item, where blocks
// follow each other without a blank line.
func tightItem(c ast.Node) bool {
	if _, ok := c.Parent().(*ast.ListItem); !ok {
		return false
	}
	l, ok := c.Parent().Parent().(*ast.List)
	return ok && l.IsTight
}

func (r mdRenderer) block(n ast.Node, width int) []string {
	// Each level of quote or list takes cells from its children; past the
	// edge they render one cell wide and renderBody cuts the overflow.
	width = max(width, 1)
	switch n := n.(type) {
	case *ast.Paragraph, *ast.TextBlock:
		return wrap(r.inline(n, lipgloss.NewStyle()), width)
	case *ast.Heading:
		base := lipgloss.NewStyle().Bold(true)
		if n.Level <= 2 {
			base = base.Foreground(r.st.accent.GetForeground())
		}
		return wrap(r.inline(n, base), width)
	case *ast.FencedCodeBlock, *ast.CodeBlock, *ast.HTMLBlock:
		return r.code(n, width)
	case *ast.Blockquote:
		bar := r.st.faint.Render(r.st.g.column + " ")
		lines := r.blocks(n, width-2)
		for i, l := range lines {
			lines[i] = bar + l
		}
		return lines
	case *ast.List:
		return r.list(n, width)
	case *ast.ThematicBreak:
		return []string{r.st.faint.Render(strings.Repeat(r.st.g.rule, min(width, 40)))}
	case *east.Table:
		return r.table(n, width)
	default:
		return r.blocks(n, width)
	}
}

// code prints the lines of a code or raw HTML block as they are, wrapping
// only lines wider than the column.
func (r mdRenderer) code(n ast.Node, width int) []string {
	var out []string
	segs := n.Lines()
	for i := range segs.Len() {
		seg := segs.At(i)
		line := strings.TrimRight(string(seg.Value(r.src)), "\n")
		for part := range strings.SplitSeq(ansi.Hardwrap(line, max(width-2, 1), true), "\n") {
			out = append(out, "  "+r.st.muted.Render(part))
		}
	}
	return out
}

func (r mdRenderer) list(l *ast.List, width int) []string {
	var out []string
	num := l.Start
	for item := l.FirstChild(); item != nil; item = item.NextSibling() {
		marker := r.st.g.bullet + " "
		if l.IsOrdered() {
			marker = strconv.Itoa(num) + ". "
			num++
		}
		pad := ansi.StringWidth(marker)
		lines := r.blocks(item, width-pad)
		if len(lines) == 0 {
			lines = []string{""}
		}
		if len(out) > 0 && !l.IsTight {
			out = append(out, "")
		}
		for i, line := range lines {
			if i == 0 {
				out = append(out, r.st.muted.Render(marker)+line)
			} else {
				out = append(out, strings.Repeat(" ", pad)+line)
			}
		}
	}
	return out
}

// table prints one line per row with cells separated by a bar; it does not
// align columns.
func (r mdRenderer) table(t *east.Table, width int) []string {
	var out []string
	sep := r.st.faint.Render(" " + r.st.g.column + " ")
	for row := t.FirstChild(); row != nil; row = row.NextSibling() {
		base := lipgloss.NewStyle()
		if _, ok := row.(*east.TableHeader); ok {
			base = base.Bold(true)
		}
		var cells []string
		for cell := row.FirstChild(); cell != nil; cell = cell.NextSibling() {
			cells = append(cells, r.inline(cell, base))
		}
		out = append(out, wrap(strings.Join(cells, sep), width)...)
	}
	return out
}

// inline renders the inline children of n with base as the starting style.
func (r mdRenderer) inline(n ast.Node, base lipgloss.Style) string {
	var b strings.Builder
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		switch c := c.(type) {
		case *ast.Text:
			b.WriteString(words(string(c.Value(r.src)), base))
			switch {
			case c.HardLineBreak():
				b.WriteByte('\n')
			case c.SoftLineBreak():
				b.WriteByte(' ')
			}
		case *ast.String:
			b.WriteString(words(string(c.Value), base))
		case *ast.CodeSpan:
			b.WriteString(r.inline(c, base.Foreground(r.st.accent.GetForeground())))
		case *markdown.Mention:
			color := r.st.toneOf(c.Name)
			if c.Observer {
				color = r.st.observer
			}
			b.WriteString(r.inline(c, base.Foreground(color.GetForeground()).Bold(true)))
		case *ast.Emphasis:
			if c.Level >= 2 {
				b.WriteString(r.inline(c, base.Bold(true)))
			} else {
				b.WriteString(r.inline(c, base.Italic(true)))
			}
		case *east.Strikethrough:
			b.WriteString(r.inline(c, base.Strikethrough(true)))
		case *east.TaskCheckBox:
			box := "[ ] "
			if c.IsChecked {
				box = "[x] "
			}
			b.WriteString(r.st.muted.Render(box))
		case *ast.Link:
			label := r.inline(c, base.Underline(true))
			b.WriteString(label)
			if dest := string(c.Destination); dest != ansi.Strip(label) {
				b.WriteByte(' ')
				b.WriteString(words("("+dest+")", r.st.faint))
			}
		case *ast.AutoLink:
			b.WriteString(words(string(c.URL(r.src)), base.Underline(true)))
		case *ast.RawHTML:
			for i := range c.Segments.Len() {
				seg := c.Segments.At(i)
				b.WriteString(words(string(seg.Value(r.src)), base))
			}
		default:
			b.WriteString(r.inline(c, base))
		}
	}
	return b.String()
}

// words styles each space-separated word on its own, so wrapping between
// words never splits a styled run.
func words(s string, st lipgloss.Style) string {
	parts := strings.Split(s, " ")
	for i, p := range parts {
		if p != "" {
			parts[i] = st.Render(p)
		}
	}
	return strings.Join(parts, " ")
}

// wrap breaks s into lines at most width cells wide, at spaces where it can.
func wrap(s string, width int) []string {
	return strings.Split(ansi.Wrap(s, max(width, 1), ""), "\n")
}
