// Package tui is the terminal viewer, `agora tui`. It talks to the server
// over the JSON API (internal/client) and never opens the database. Its
// text and colors come from the theme's tui/ directory (see Skin).
package tui

import (
	"context"
	"errors"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/aahoughton/agora/internal/client"
	"github.com/aahoughton/agora/internal/core"
)

// Defaults for Options.
const (
	DefaultFollowTimeout = 25 * time.Second
	DefaultRefreshEvery  = 5 * time.Second
	DefaultRetryAfter    = 2 * time.Second
	DefaultShownMessages = 300
)

// Layout constants, in cells.
const (
	panelWidth    = 32
	wideEnough    = 100
	composerLines = 3
)

// requestMargin is how long past its wait a follow may take before it is
// abandoned and retried.
const requestMargin = 5 * time.Second

// requestTimeout bounds every other request.
const requestTimeout = 30 * time.Second

// Options configure the TUI. Client and Skin are required; zero values
// elsewhere select the defaults.
type Options struct {
	Client *client.Client
	Skin   *Skin
	// Namespace and Conversation pick the first screen: both open the
	// conversation, Namespace alone its conversation list, neither the
	// namespace list.
	Namespace    string
	Conversation string
	// Observer is the name comments are posted under. If empty, the TUI
	// asks on the first comment.
	Observer      string
	Now           func() time.Time
	Location      *time.Location
	FollowTimeout time.Duration
	RefreshEvery  time.Duration
	RetryAfter    time.Duration
	ShownMessages int
}

type screen int

const (
	screenNamespaces screen = iota
	screenConversations
	screenConversation
)

// Model is the Bubble Tea model for the whole TUI.
type Model struct {
	ctx  context.Context
	opts Options
	// margin is how long past its wait a follow may take; see
	// requestMargin.
	margin        time.Duration
	width, height int
	dark          bool
	st            styles
	screen        screen
	help          bool
	notice        string
	// gen changes whenever a screen opens; replies to requests made for an
	// earlier screen are dropped.
	gen int
	// screenCtx ends the current screen's requests when it closes.
	screenCtx    context.Context
	screenCancel context.CancelFunc

	ns            string
	namespaces    []core.Namespace
	conversations []core.Conversation
	archived      bool
	// next continues the conversation list; empty when all is loaded.
	next        string
	loadingMore bool
	cursor      int
	filter      string
	filtering   bool

	t *thread
}

// thread is the state of the conversation screen.
type thread struct {
	name      string
	conv      core.Conversation
	loaded    bool
	messages  []core.Message
	lines     [][]string
	after     int64
	truncated bool
	// start is the seq the loaded messages follow: earlier ones exist
	// when it is above 0, and load on scrolling past the top.
	start          int64
	loadingEarlier bool
	vp             viewport.Model
	newBelow       int
	// panel shows participants beside the thread on a wide screen;
	// overlay shows them in place of the thread on a narrow one.
	panel    bool
	overlay  bool
	retrying string

	composer   textarea.Model
	composing  bool
	nameInput  textinput.Model
	askingName bool
	sending    bool
}

// Messages from commands.
type (
	namespacesMsg struct {
		gen  int
		list []core.Namespace
		err  error
	}
	conversationsMsg struct {
		gen  int
		page core.ListPage
		// more is true for a following page, appended to the list.
		more bool
		err  error
	}
	refreshMsg      struct{ gen int }
	conversationMsg struct {
		gen  int
		conv core.Conversation
		err  error
	}
	followMsg struct {
		gen int
		res client.FollowResult
		err error
	}
	retryMsg   struct{ gen int }
	earlierMsg struct {
		gen  int
		from int64
		res  client.FollowResult
		err  error
	}
	observedMsg struct {
		gen int
		err error
	}
)

// New returns the model. ctx bounds every request it makes.
func New(ctx context.Context, opts Options) *Model {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Location == nil {
		opts.Location = time.Local
	}
	if opts.FollowTimeout == 0 {
		opts.FollowTimeout = DefaultFollowTimeout
	}
	if opts.RefreshEvery == 0 {
		opts.RefreshEvery = DefaultRefreshEvery
	}
	if opts.RetryAfter == 0 {
		opts.RetryAfter = DefaultRetryAfter
	}
	if opts.ShownMessages == 0 {
		opts.ShownMessages = DefaultShownMessages
	}
	m := &Model{ctx: ctx, opts: opts, margin: requestMargin, dark: true, width: 80, height: 24}
	m.st = opts.Skin.styles(m.dark)
	return m
}

// Run starts the TUI and blocks until the user quits or ctx is done.
func Run(ctx context.Context, opts Options) error {
	// Requests still in flight end with the program.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// A signal cancels ctx and kills the program: a normal way to quit.
	if _, err := tea.NewProgram(New(ctx, opts), tea.WithContext(ctx)).Run(); err != nil && ctx.Err() == nil {
		return err
	}
	return nil
}

// Init implements tea.Model.
func (m *Model) Init() tea.Cmd {
	var open tea.Cmd
	switch {
	case m.opts.Namespace != "" && m.opts.Conversation != "":
		m.ns = m.opts.Namespace
		open = m.openConversation(m.opts.Conversation)
	case m.opts.Namespace != "":
		open = m.openConversations(m.opts.Namespace)
	default:
		open = m.openNamespaces()
	}
	return tea.Batch(tea.RequestBackgroundColor, open)
}

func (m *Model) render() renderer {
	return renderer{sk: m.opts.Skin, st: m.st, now: m.opts.Now(), loc: m.opts.Location}
}

func (m *Model) say(key string, data any) string { return m.opts.Skin.say(key, data) }

// leave tears down the current screen before another opens.
func (m *Model) leave() {
	m.gen++
	m.notice, m.help, m.filter, m.filtering, m.cursor = "", false, "", false, 0
	if m.screenCancel != nil {
		m.screenCancel()
	}
	m.screenCtx, m.screenCancel = context.WithCancel(m.ctx)
	m.t = nil
}

func (m *Model) openNamespaces() tea.Cmd {
	m.leave()
	m.screen = screenNamespaces
	return m.loadNamespaces()
}

func (m *Model) loadNamespaces() tea.Cmd {
	gen, c, ctx := m.gen, m.opts.Client, m.screenCtx
	return func() tea.Msg {
		ctx, done := context.WithTimeout(ctx, requestTimeout)
		defer done()
		list, err := c.ListNamespaces(ctx)
		return namespacesMsg{gen: gen, list: list, err: err}
	}
}

func (m *Model) openConversations(ns string) tea.Cmd {
	m.leave()
	m.screen, m.ns, m.conversations, m.next, m.loadingMore = screenConversations, ns, nil, "", false
	return m.loadConversations()
}

func (m *Model) loadConversations() tea.Cmd {
	// A refresh reloads as many as are already shown, so paging further
	// is not undone every few seconds.
	limit := min(max(listPage, len(m.conversations)), core.MaxListLimit)
	return m.listRequest(core.ListParams{IncludeArchived: m.archived, Limit: limit}, false)
}

// reloadList drops responses still in flight, including a pending next
// page, and loads the list again.
func (m *Model) reloadList() tea.Cmd {
	m.gen++
	m.loadingMore = false
	return m.loadConversations()
}

// listPage is how many conversations the list loads at a time.
const listPage = 100

// loadMore fetches the page after the loaded ones.
func (m *Model) loadMore() tea.Cmd {
	if m.next == "" || m.loadingMore {
		return nil
	}
	m.loadingMore = true
	return m.listRequest(core.ListParams{IncludeArchived: m.archived, Limit: listPage, Before: m.next}, true)
}

func (m *Model) listRequest(lp core.ListParams, more bool) tea.Cmd {
	gen, c, ctx, ns := m.gen, m.opts.Client, m.screenCtx, m.ns
	return func() tea.Msg {
		ctx, done := context.WithTimeout(ctx, requestTimeout)
		defer done()
		page, err := c.ListConversations(ctx, ns, lp)
		return conversationsMsg{gen: gen, page: page, more: more, err: err}
	}
}

func (m *Model) refreshLater() tea.Cmd {
	gen := m.gen
	return tea.Tick(m.opts.RefreshEvery, func(time.Time) tea.Msg { return refreshMsg{gen: gen} })
}

func (m *Model) openConversation(name string) tea.Cmd {
	m.leave()
	m.screen = screenConversation
	t := &thread{name: name, vp: viewport.New(), panel: true}
	t.composer = textarea.New()
	t.composer.ShowLineNumbers = false
	t.composer.Prompt = ""
	t.composer.Placeholder = m.say("composer_placeholder", nil)
	t.composer.SetHeight(composerLines)
	t.composer.KeyMap.InsertNewline = key.NewBinding(key.WithKeys("ctrl+j"))
	t.composer.SetStyles(textarea.DefaultStyles(m.dark))
	t.nameInput = textinput.New()
	t.nameInput.Prompt = ""
	t.nameInput.CharLimit = 64
	m.t = t
	m.layout()
	gen, c, ctx, ns := m.gen, m.opts.Client, m.screenCtx, m.ns
	return func() tea.Msg {
		ctx, done := context.WithTimeout(ctx, requestTimeout)
		defer done()
		conv, err := c.GetConversation(ctx, ns, name)
		return conversationMsg{gen: gen, conv: conv, err: err}
	}
}

// follow asks the server for messages past t.after, waiting up to timeout.
func (m *Model) follow(timeout time.Duration) tea.Cmd {
	t := m.t
	gen, c, ctx, ns, name, after, margin := m.gen, m.opts.Client, m.screenCtx, m.ns, t.name, t.after, m.margin
	return func() tea.Msg {
		rctx, done := context.WithTimeout(ctx, timeout+margin)
		defer done()
		res, err := c.Follow(rctx, ns, name, after, timeout)
		return followMsg{gen: gen, res: res, err: err}
	}
}

// Update implements tea.Model.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		return m, nil
	case tea.BackgroundColorMsg:
		m.dark = msg.IsDark()
		m.st = m.opts.Skin.styles(m.dark)
		if m.t != nil {
			m.t.composer.SetStyles(textarea.DefaultStyles(m.dark))
			m.rerender()
		}
		return m, nil
	case tea.KeyPressMsg:
		return m, m.key(msg)

	case namespacesMsg:
		if msg.gen != m.gen {
			return m, nil
		}
		m.setNotice(msg.err)
		if msg.err == nil {
			m.namespaces = msg.list
			m.clampCursor()
		}
		return m, nil
	case conversationsMsg:
		if msg.gen != m.gen {
			return m, nil
		}
		m.setNotice(msg.err)
		if msg.more {
			m.loadingMore = false
			if msg.err == nil {
				m.appendConversations(msg.page)
			}
			return m, nil
		}
		if msg.err == nil {
			m.replaceConversations(msg.page)
			m.clampCursor()
		}
		return m, m.refreshLater()
	case refreshMsg:
		if msg.gen != m.gen || m.screen != screenConversations {
			return m, nil
		}
		return m, m.loadConversations()

	case conversationMsg:
		if msg.gen != m.gen {
			return m, nil
		}
		if msg.err != nil {
			m.setNotice(msg.err)
			return m, nil
		}
		t := m.t
		t.conv = msg.conv
		t.after = max(0, msg.conv.LastSeq-int64(m.opts.ShownMessages))
		t.start, t.truncated = t.after, t.after > 0
		return m, m.follow(0)
	case earlierMsg:
		if msg.gen != m.gen {
			return m, nil
		}
		m.t.loadingEarlier = false
		if msg.err != nil {
			m.setNotice(msg.err)
			return m, nil
		}
		m.prependEarlier(msg.from, msg.res.Messages)
		return m, nil
	case followMsg:
		if msg.gen != m.gen {
			return m, nil
		}
		return m, m.followed(msg)
	case retryMsg:
		if msg.gen != m.gen {
			return m, nil
		}
		return m, m.follow(0)
	case observedMsg:
		if msg.gen != m.gen {
			return m, nil
		}
		m.t.sending = false
		if msg.err != nil {
			m.setNotice(msg.err)
			return m, nil
		}
		m.notice = ""
		m.t.composer.Reset()
		return m, nil
	}

	if m.t != nil {
		var cmd tea.Cmd
		switch {
		case m.t.askingName:
			m.t.nameInput, cmd = m.t.nameInput.Update(msg)
		case m.t.composing:
			m.t.composer, cmd = m.t.composer.Update(msg)
		}
		return m, cmd
	}
	return m, nil
}

func (m *Model) followed(msg followMsg) tea.Cmd {
	t := m.t
	if msg.err != nil {
		if m.screenCtx.Err() != nil {
			return nil
		}
		// A request that outlived its deadline is a stalled server or
		// connection: try again, like any other transient failure.
		if client.IsTemporary(msg.err) || errors.Is(msg.err, context.DeadlineExceeded) {
			t.retrying = m.say("retrying", map[string]any{"Err": msg.err.Error()})
			gen := m.gen
			return tea.Tick(m.opts.RetryAfter, func(time.Time) tea.Msg { return retryMsg{gen: gen} })
		}
		m.setNotice(msg.err)
		return nil
	}
	t.retrying = ""
	t.conv = msg.res.Conversation
	wasBottom := !t.loaded || t.vp.AtBottom()
	added := 0
	for _, mm := range msg.res.Messages {
		if mm.Seq <= t.after {
			continue
		}
		t.messages = append(t.messages, mm)
		t.after = mm.Seq
		added++
	}
	t.loaded = true
	if added > 0 && gutterWidth(t.after) != gutterWidth(t.after-int64(added)) {
		m.rerender()
	} else {
		m.renderNew(added)
	}
	if wasBottom {
		t.vp.GotoBottom()
		t.newBelow = 0
	} else {
		t.newBelow += added
	}
	if t.conv.Status == core.StatusConcluded && (t.composing || t.askingName) {
		t.composing, t.askingName = false, false
		t.composer.Blur()
		t.nameInput.Blur()
	}
	// The composer's rows go to the thread once the conversation concludes.
	m.layout()
	if msg.res.More {
		return m.follow(0)
	}
	return m.follow(m.opts.FollowTimeout)
}

func (m *Model) setNotice(err error) {
	if err == nil {
		m.notice = ""
		return
	}
	m.notice = m.say("error", map[string]any{"Err": errorText(err)})
}

// errorText is the server's message for an API error, or the error.
func errorText(err error) string {
	if e, ok := errors.AsType[*client.Error](err); ok {
		return e.Message
	}
	return err.Error()
}

// threadWidth is the width of the message column.
func (m *Model) threadWidth() int {
	if m.t != nil && m.t.panel && m.width >= wideEnough {
		return m.width - panelWidth - 3
	}
	return m.width
}

// bodyHeight is the height between the sticky header and the composer.
func (m *Model) bodyHeight() int {
	// header (2) + rule, rule + composer, footer
	used := 3 + 1 + 1 + 1
	if m.t != nil && m.t.conv.Status != core.StatusConcluded {
		used += composerLines - 1
	}
	return max(m.height-used, 1)
}

// layout sizes the viewport and composer to the window.
func (m *Model) layout() {
	t := m.t
	if t == nil {
		return
	}
	w := m.threadWidth()
	resized := t.vp.Width() != w
	bottom := t.vp.AtBottom()
	t.vp.SetWidth(w)
	t.vp.SetHeight(m.bodyHeight())
	t.composer.SetWidth(max(m.width-2, minWidth))
	t.nameInput.SetWidth(max(m.width-2, minWidth))
	if resized {
		m.rerender()
	}
	// A view at the bottom keeps following through a resize.
	if bottom {
		t.vp.GotoBottom()
	}
}

// rerender renders every message again, after a width or color change.
func (m *Model) rerender() {
	t := m.t
	if t == nil {
		return
	}
	t.lines = nil
	m.renderNew(len(t.messages))
}

// renderNew renders the last n messages and updates the viewport.
func (m *Model) renderNew(n int) {
	t := m.t
	r := m.render()
	gutter := gutterWidth(t.after)
	w := t.vp.Width()
	for _, mm := range t.messages[len(t.messages)-n:] {
		t.lines = append(t.lines, r.message(mm, gutter, w))
	}
	var all []string
	if t.truncated {
		all = append(all, m.st.faint.Render(m.say("truncated", nil)), "")
	}
	for i, l := range t.lines {
		if i > 0 {
			all = append(all, "")
		}
		all = append(all, l...)
	}
	// A view at the bottom stays there; otherwise the offset holds.
	off, bottom := t.vp.YOffset(), t.vp.AtBottom()
	t.vp.SetContentLines(all)
	if bottom {
		t.vp.GotoBottom()
	} else {
		t.vp.SetYOffset(off)
	}
}

func (m *Model) clampCursor() {
	n := len(m.visible())
	m.cursor = max(0, min(m.cursor, n-1))
}

// visible returns the names on the current list screen that match the
// filter.
func (m *Model) visible() []string {
	var names []string
	switch m.screen {
	case screenNamespaces:
		for _, n := range m.namespaces {
			names = append(names, n.Name)
		}
	case screenConversations:
		for _, c := range m.conversations {
			names = append(names, c.Name)
		}
	}
	if m.filter == "" {
		return names
	}
	var out []string
	for _, n := range names {
		if strings.Contains(strings.ToLower(n), strings.ToLower(m.filter)) {
			out = append(out, n)
		}
	}
	return out
}

func (m *Model) key(k tea.KeyPressMsg) tea.Cmd {
	s := k.String()
	if s == "ctrl+c" {
		return tea.Quit
	}
	if m.t != nil && m.t.askingName {
		return m.nameKey(k)
	}
	if m.t != nil && m.t.composing {
		return m.composerKey(k)
	}
	if m.filtering {
		m.filterKey(k)
		return nil
	}
	if m.help {
		if s == "?" || s == "esc" || s == "q" {
			m.help = false
		}
		return nil
	}
	switch s {
	case "?":
		m.help = true
		return nil
	case "q":
		return tea.Quit
	}
	if m.screen == screenConversation {
		return m.threadKey(k)
	}
	return m.listKey(k)
}

// appendConversations adds a following page, skipping any a refresh has
// already brought in.
// replaceConversations applies a refresh. A refresh loads at most
// core.MaxListLimit, so conversations loaded beyond that are kept after the
// refreshed ones, and paging continues from where it was.
func (m *Model) replaceConversations(page core.ListPage) {
	if page.Next == "" || len(m.conversations) <= len(page.Conversations) {
		m.conversations, m.next = page.Conversations, page.Next
		return
	}
	seen := map[string]bool{}
	for _, c := range page.Conversations {
		seen[c.Name] = true
	}
	list := page.Conversations
	for _, c := range m.conversations[len(page.Conversations):] {
		if !seen[c.Name] {
			list = append(list, c)
		}
	}
	m.conversations = list
}

func (m *Model) appendConversations(page core.ListPage) {
	seen := map[string]bool{}
	for _, c := range m.conversations {
		seen[c.Name] = true
	}
	for _, c := range page.Conversations {
		if !seen[c.Name] {
			m.conversations = append(m.conversations, c)
		}
	}
	m.next = page.Next
}

func (m *Model) listKey(k tea.KeyPressMsg) tea.Cmd {
	cmd := m.moveInList(k)
	// Reaching the end of what is loaded fetches the next page.
	if m.screen == screenConversations && m.filter == "" && m.cursor >= len(m.visible())-1 {
		return tea.Batch(cmd, m.loadMore())
	}
	return cmd
}

func (m *Model) moveInList(k tea.KeyPressMsg) tea.Cmd {
	names := m.visible()
	switch k.String() {
	case "up", "k":
		m.cursor = max(m.cursor-1, 0)
	case "down", "j":
		m.cursor = min(m.cursor+1, max(len(names)-1, 0))
	case "home", "g":
		m.cursor = 0
	case "end", "G":
		m.cursor = max(len(names)-1, 0)
	case "/":
		m.filtering = true
	case "r":
		if m.screen == screenNamespaces {
			return m.loadNamespaces()
		}
		return m.reloadList()
	case "a":
		if m.screen == screenConversations {
			m.archived = !m.archived
			return m.reloadList()
		}
	case "esc":
		if m.filter != "" {
			m.filter = ""
			m.clampCursor()
			return nil
		}
		if m.screen == screenConversations {
			return m.openNamespaces()
		}
	case "enter":
		if m.cursor >= len(names) {
			return nil
		}
		if m.screen == screenNamespaces {
			return m.openConversations(names[m.cursor])
		}
		return m.openConversation(names[m.cursor])
	}
	return nil
}

func (m *Model) filterKey(k tea.KeyPressMsg) {
	switch k.String() {
	case "enter":
		m.filtering = false
	case "esc":
		m.filtering, m.filter = false, ""
	case "backspace":
		if r := []rune(m.filter); len(r) > 0 {
			m.filter = string(r[:len(r)-1])
		}
	default:
		if k.Text != "" {
			m.filter += k.Text
		}
	}
	m.clampCursor()
}

func (m *Model) threadKey(k tea.KeyPressMsg) tea.Cmd {
	t := m.t
	switch k.String() {
	case "esc":
		ns := m.ns
		return m.openConversations(ns)
	case "p":
		if m.width >= wideEnough {
			t.panel = !t.panel
		} else {
			t.overlay = !t.overlay
		}
		m.layout()
	case "i", "tab":
		if t.loaded && t.conv.Status != core.StatusConcluded {
			t.composing = true
			return t.composer.Focus()
		}
	case "g", "home":
		t.vp.GotoTop()
		return m.loadEarlier()
	case "G", "end":
		t.vp.GotoBottom()
		t.newBelow = 0
	default:
		var cmd tea.Cmd
		t.vp, cmd = t.vp.Update(k)
		if t.vp.AtBottom() {
			t.newBelow = 0
		}
		if t.vp.AtTop() {
			return tea.Batch(cmd, m.loadEarlier())
		}
		return cmd
	}
	return nil
}

// earlierPage is how many earlier messages load at a time; a follow
// returns at most core.FollowLimit.
const earlierPage = core.FollowLimit

// loadEarlier fetches the messages before the loaded ones, if any.
func (m *Model) loadEarlier() tea.Cmd {
	t := m.t
	if t.start == 0 || t.loadingEarlier {
		return nil
	}
	t.loadingEarlier = true
	from := max(0, t.start-earlierPage)
	gen, c, ctx, ns, name := m.gen, m.opts.Client, m.screenCtx, m.ns, t.name
	return func() tea.Msg {
		ctx, done := context.WithTimeout(ctx, requestTimeout)
		defer done()
		res, err := c.Follow(ctx, ns, name, from, 0)
		return earlierMsg{gen: gen, from: from, res: res, err: err}
	}
}

// prependEarlier adds messages before the loaded ones and keeps the same
// lines on screen.
func (m *Model) prependEarlier(from int64, msgs []core.Message) {
	t := m.t
	var earlier []core.Message
	for _, mm := range msgs {
		if mm.Seq <= t.start {
			earlier = append(earlier, mm)
		}
	}
	t.messages = append(earlier, t.messages...)
	t.start = from
	t.truncated = from > 0
	total, off := t.vp.TotalLineCount(), t.vp.YOffset()
	m.rerender()
	t.vp.SetYOffset(off + t.vp.TotalLineCount() - total)
}

func (m *Model) composerKey(k tea.KeyPressMsg) tea.Cmd {
	t := m.t
	if k.String() == "esc" {
		t.composing = false
		t.composer.Blur()
		return nil
	}
	// The text being sent stays put until the server answers: it is
	// cleared on success and kept for another try on failure.
	if t.sending {
		return nil
	}
	if k.String() == "enter" {
		if strings.TrimSpace(t.composer.Value()) == "" {
			return nil
		}
		if m.opts.Observer == "" {
			t.askingName = true
			t.composer.Blur()
			return t.nameInput.Focus()
		}
		return m.send()
	}
	var cmd tea.Cmd
	t.composer, cmd = t.composer.Update(k)
	return cmd
}

func (m *Model) nameKey(k tea.KeyPressMsg) tea.Cmd {
	t := m.t
	switch k.String() {
	case "esc":
		t.askingName = false
		t.nameInput.Blur()
		m.notice = ""
		return t.composer.Focus()
	case "enter":
		name := strings.TrimSpace(t.nameInput.Value())
		if !core.ValidName(name) {
			m.notice = m.say("composer_name_invalid", nil)
			return nil
		}
		m.opts.Observer = name
		m.notice = ""
		t.askingName = false
		t.nameInput.Blur()
		return tea.Batch(t.composer.Focus(), m.send())
	}
	var cmd tea.Cmd
	t.nameInput, cmd = t.nameInput.Update(k)
	return cmd
}

// send posts the composer's text as an observer comment. The post is not
// tied to the screen: leaving right after sending does not abort it.
func (m *Model) send() tea.Cmd {
	t := m.t
	t.sending = true
	gen, c, ctx, ns, name, as, body := m.gen, m.opts.Client, m.ctx, m.ns, t.name, m.opts.Observer, t.composer.Value()
	return func() tea.Msg {
		ctx, done := context.WithTimeout(ctx, requestTimeout)
		defer done()
		_, err := c.Observe(ctx, ns, name, as, body)
		return observedMsg{gen: gen, err: err}
	}
}

// View implements tea.Model.
func (m *Model) View() tea.View {
	var lines []string
	switch m.screen {
	case screenConversation:
		lines = m.threadView()
	default:
		lines = m.listView()
	}
	v := tea.NewView(strings.Join(fit(lines, m.width, m.height), "\n"))
	v.AltScreen = true
	v.WindowTitle = m.say("brand", nil)
	return v
}

func (m *Model) footer(hints string) string {
	switch {
	case m.notice != "":
		return m.st.accent.Render(ansi.Truncate(m.notice, m.width, m.st.g.more))
	case m.t != nil && m.t.retrying != "":
		return m.st.observer.Render(ansi.Truncate(m.t.retrying, m.width, m.st.g.more))
	}
	return m.st.faint.Render(ansi.Truncate(hints, m.width, m.st.g.more))
}

func (m *Model) helpLines() []string {
	out := []string{m.st.fg.Bold(true).Render(m.say("help_title", nil)), ""}
	for l := range strings.SplitSeq(m.say("help", nil), "\n") {
		out = append(out, m.st.muted.Render(l))
	}
	return out
}

func (m *Model) listView() []string {
	r := m.render()
	var title, hints, empty string
	if m.screen == screenNamespaces {
		title = m.say("namespaces_title", nil)
		hints = m.say("hints_namespaces", nil)
		empty = m.say("namespaces_empty", nil)
	} else {
		title = m.say("conversations_title", map[string]any{"Namespace": m.ns})
		hints = m.say("hints_conversations", nil)
		empty = m.say("conversations_empty", map[string]any{"Archived": m.archived})
	}
	head := m.st.accent.Bold(true).Render(m.say("brand", nil)) + "  " + m.st.fg.Bold(true).Render(title)
	if m.filtering || m.filter != "" {
		head += "  " + m.st.faint.Render(m.say("filter", nil)+": ") + m.filter
		if m.filtering {
			head += m.st.faint.Render(m.st.g.caret)
		}
	}
	out := []string{head, r.rule(m.width)}
	bodyH := max(m.height-3, 1)
	if m.help {
		return append(append(out, fit(m.helpLines(), m.width, bodyH)...), m.footer(hints))
	}

	var rows []string
	names := m.visible()
	byName := map[string]core.Conversation{}
	for _, c := range m.conversations {
		byName[c.Name] = c
	}
	for i, n := range names {
		mark := "  "
		if i == m.cursor {
			mark = m.st.accent.Render(m.st.g.cursor + " ")
		}
		row := m.st.fg.Bold(true).Render(n)
		if m.screen == screenConversations {
			row = r.conversationRow(byName[n], m.width-2)
		}
		rows = append(rows, mark+row)
	}
	loaded := m.namespaces != nil
	if m.screen == screenConversations {
		loaded = m.conversations != nil
	}
	switch {
	case len(rows) > 0:
	case loaded:
		rows = []string{"  " + m.st.muted.Render(empty)}
	case m.notice == "":
		rows = []string{"  " + m.st.faint.Render(m.say("loading", nil))}
	}
	start := 0
	if m.cursor >= bodyH {
		start = m.cursor - bodyH + 1
	}
	out = append(out, fit(rows[min(start, len(rows)):], m.width, bodyH)...)
	return append(out, m.footer(hints))
}

func (m *Model) threadView() []string {
	t := m.t
	r := m.render()
	var out []string
	if t.loaded {
		out = r.header(t.conv, m.width)
	} else {
		out = []string{m.st.fg.Bold(true).Render(t.name), m.st.faint.Render(m.say("loading", nil))}
	}
	out = append(out, r.rule(m.width))

	bodyH := m.bodyHeight()
	var body []string
	switch {
	case m.help:
		body = fit(m.helpLines(), m.width, bodyH)
	case m.width < wideEnough && t.overlay:
		body = fit(r.participants(t.conv, m.width), m.width, bodyH)
	case m.width >= wideEnough && t.panel:
		thread := fit(strings.Split(t.vp.View(), "\n"), t.vp.Width(), bodyH)
		panel := fit(r.participants(t.conv, panelWidth), panelWidth, bodyH)
		body = sideBySide(thread, t.vp.Width(), m.st.line.Render(" "+m.st.g.column+" "), panel)
	default:
		body = fit(strings.Split(t.vp.View(), "\n"), m.width, bodyH)
	}
	out = append(out, body...)

	// The rule above the composer carries the follow state on its right.
	state := m.say("following", nil)
	if t.newBelow > 0 {
		state = m.say("new_below", map[string]any{"N": t.newBelow})
	}
	state = " " + state + " "
	rule := r.rule(m.width - ansi.StringWidth(state) - 1)
	if t.newBelow > 0 {
		out = append(out, rule+m.st.accent.Render(state)+r.rule(1))
	} else {
		out = append(out, rule+m.st.faint.Render(state)+r.rule(1))
	}

	hints := m.say("hints_conversation", nil)
	switch {
	case t.loaded && t.conv.Status == core.StatusConcluded:
		out = append(out, m.st.muted.Render(m.say("composer_closed", nil)))
		hints = m.say("hints_concluded", nil)
	case t.askingName:
		out = append(out, m.st.muted.Render(m.say("composer_name_prompt", nil)), t.nameInput.View(), "")
		hints = m.say("hints_composer", nil)
	default:
		out = append(out, fit(strings.Split(t.composer.View(), "\n"), m.width, composerLines)...)
		if t.composing {
			hints = m.say("hints_composer", nil)
		}
		if t.sending {
			hints = m.say("composer_sending", nil) + " " + m.st.g.sep + " " + hints
		}
		if m.opts.Observer != "" {
			hints = m.say("composer_as", map[string]any{"Name": m.opts.Observer}) + " " + m.st.g.sep + " " + hints
		}
	}
	return append(out, m.footer(hints))
}
