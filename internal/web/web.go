// Package web serves the HTML viewer. It renders a theme's templates with
// data from core and contains no markup of its own; see themes/README.md for
// the files a theme provides and the data its templates receive.
package web

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"text/template/parse"
	"time"

	"github.com/aahoughton/agora/internal/auth"
	"github.com/aahoughton/agora/internal/core"
	"github.com/aahoughton/agora/internal/tone"
	"github.com/aahoughton/agora/themes"
)

// TemplateAPI is the version of the data contract between this package and
// theme templates. It changes when a field is removed or changes meaning.
const TemplateAPI = 1

// Page is the data every page template receives.
type Page struct {
	// API is TemplateAPI, for themes that want to check it.
	API     int
	Version string
	Title   string
	// Notice is a message to show above the content, such as an error.
	Notice string

	Namespace       string
	Namespaces      []core.Namespace
	Conversations   []core.Conversation
	IncludeArchived bool

	Conversation *core.Conversation
	Messages     []core.Message
	// Truncated is true when older messages exist that are not shown.
	Truncated bool
	// Observer is the name last used for an observer comment.
	Observer string
	// Draft is the text of a comment that was rejected, to show again.
	Draft string
}

// pages are the page templates a theme must provide. Each defines
// "content"; layout.html defines "layout", message.html "message",
// participants.html "participants" and about.html "about".
var pages = []string{"namespaces", "conversations", "conversation", "error"}

var partials = []string{"layout", "message", "participants", "about"}

// Options configure the viewer.
type Options struct {
	// ThemeDir, if set, holds files that replace the default theme's files
	// of the same path.
	ThemeDir string
	Logger   *slog.Logger
	Version  string
	// ShownMessages is how many recent messages a conversation page shows.
	ShownMessages int
	// Now and Location set the clock and time zone pages show times in;
	// they default to the real clock and the local zone.
	Now      func() time.Time
	Location *time.Location
}

type viewer struct {
	svc    *core.Service
	authn  auth.Authenticator
	opts   Options
	pages  map[string]*template.Template
	static http.Handler
}

const observerCookie = "agora_observer"

// Handler returns the viewer. It fails if the theme is incomplete or its
// templates do not parse.
func Handler(svc *core.Service, authn auth.Authenticator, opts Options) (http.Handler, error) {
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	if opts.ShownMessages == 0 {
		opts.ShownMessages = 300
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Location == nil {
		opts.Location = time.Local
	}
	theme, err := themes.Open(opts.ThemeDir)
	if err != nil {
		return nil, fmt.Errorf("web: %w", err)
	}
	v := &viewer{svc: svc, authn: authn, opts: opts, pages: map[string]*template.Template{}}
	if err := v.load(theme); err != nil {
		return nil, err
	}
	static, err := fs.Sub(theme, "static")
	if err != nil {
		return nil, err
	}
	v.static = http.StripPrefix("/static/", http.FileServerFS(static))

	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", v.root)
	mux.HandleFunc("GET /n/{ns}", v.namespace)
	mux.HandleFunc("GET /n/{ns}/c/{conv}", v.conversation)
	mux.HandleFunc("GET /n/{ns}/c/{conv}/events", v.events)
	mux.HandleFunc("POST /n/{ns}/c/{conv}/observe", v.observe)
	mux.Handle("GET /static/", v.static)
	return mux, nil
}

func (v *viewer) funcs() template.FuncMap {
	return template.FuncMap{
		"markdown": renderMarkdown,
		"ts":       func(t any) string { return timeOf(t).UTC().Format(time.RFC3339) },
		"clock":    func(t any) string { return timeOf(t).In(v.opts.Location).Format("15:04") },
		"ago":      func(t any) string { return ago(v.opts.Now().Sub(timeOf(t))) },
		"until":    func(t any) string { return until(timeOf(t).Sub(v.opts.Now())) },
		"tone":     toneClass,
		"initials": initials,
		"datetime": func(t any) string { return timeOf(t).In(v.opts.Location).Format("Jan 2 15:04") },
	}
}

func timeOf(v any) time.Time {
	switch t := v.(type) {
	case time.Time:
		return t
	case *time.Time:
		if t != nil {
			return *t
		}
	}
	return time.Time{}
}

func ago(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return strconv.Itoa(int(d/time.Minute)) + "m ago"
	case d < 48*time.Hour:
		return strconv.Itoa(int(d/time.Hour)) + "h ago"
	default:
		return strconv.Itoa(int(d/(24*time.Hour))) + "d ago"
	}
}

// toneClass returns a stable color class for a name, so the same participant has
// the same color on every page and in the TUI.
func toneClass(name string) string {
	return "tone-" + strconv.Itoa(tone.Of(name))
}

// initials returns up to two letters for an avatar: the first letter of the
// first two words of a name split on '-', '_', '.' or space.
func initials(name string) string {
	words := strings.FieldsFunc(name, func(r rune) bool { return r == '-' || r == '_' || r == '.' || r == ' ' })
	var b strings.Builder
	for _, w := range words {
		if b.Len() == 2 {
			break
		}
		r := []rune(w)
		b.WriteString(strings.ToUpper(string(r[0])))
	}
	if b.Len() == 0 {
		return "?"
	}
	return b.String()
}

// until describes a time still to come, such as "in 1h 58m".
func until(d time.Duration) string {
	switch {
	case d <= 0:
		return "now"
	case d < time.Minute:
		return "in under a minute"
	case d < time.Hour:
		return "in " + strconv.Itoa(int(d/time.Minute)) + "m"
	case d < 48*time.Hour:
		return "in " + strconv.Itoa(int(d/time.Hour)) + "h " + strconv.Itoa(int(d%time.Hour/time.Minute)) + "m"
	default:
		return "in " + strconv.Itoa(int(d/(24*time.Hour))) + "d"
	}
}

func (v *viewer) load(theme fs.FS) error {
	read := func(name string) (string, error) {
		b, err := fs.ReadFile(theme, "templates/"+name+".html")
		if err != nil {
			return "", fmt.Errorf("web: theme is missing templates/%s.html: %w", name, err)
		}
		return string(b), nil
	}
	shared := template.New("").Funcs(v.funcs())
	for _, name := range partials {
		src, err := read(name)
		if err != nil {
			return err
		}
		// Files are named by path so that only a define can satisfy the
		// check below for a template of the same name.
		if _, err := shared.New("templates/" + name + ".html").Parse(src); err != nil {
			return fmt.Errorf("web: templates/%s.html: %w", name, err)
		}
	}
	for _, name := range pages {
		src, err := read(name)
		if err != nil {
			return err
		}
		t, err := shared.Clone()
		if err != nil {
			return err
		}
		if _, err := t.New("templates/" + name + ".html").Parse(src); err != nil {
			return fmt.Errorf("web: templates/%s.html: %w", name, err)
		}
		for _, def := range []string{"layout", "content", "message", "participants", "about"} {
			if t.Lookup(def) == nil {
				return fmt.Errorf("web: theme does not define %q (needed by %s)", def, name)
			}
		}
		if err := checkReferences(t); err != nil {
			return fmt.Errorf("web: %s page: %w", name, err)
		}
		v.pages[name] = t
	}
	return nil
}

// checkReferences fails if any template in set calls a template that is not
// defined, which html/template would otherwise report only when rendering.
func checkReferences(set *template.Template) error {
	var walk func(n parse.Node) error
	walk = func(n parse.Node) error {
		switch n := n.(type) {
		case *parse.ListNode:
			if n == nil {
				return nil
			}
			for _, c := range n.Nodes {
				if err := walk(c); err != nil {
					return err
				}
			}
		case *parse.IfNode:
			return walkBranch(walk, &n.BranchNode)
		case *parse.RangeNode:
			return walkBranch(walk, &n.BranchNode)
		case *parse.WithNode:
			return walkBranch(walk, &n.BranchNode)
		case *parse.TemplateNode:
			if set.Lookup(n.Name) == nil {
				return fmt.Errorf("template %q is used but not defined", n.Name)
			}
		}
		return nil
	}
	for _, t := range set.Templates() {
		if t.Tree == nil {
			continue
		}
		if err := walk(t.Tree.Root); err != nil {
			return fmt.Errorf("%s: %w", t.Name(), err)
		}
	}
	return nil
}

func walkBranch(walk func(parse.Node) error, b *parse.BranchNode) error {
	if err := walk(b.List); err != nil {
		return err
	}
	if b.ElseList != nil {
		return walk(b.ElseList)
	}
	return nil
}

func (v *viewer) render(w http.ResponseWriter, status int, page string, data Page) {
	data.API = TemplateAPI
	data.Version = v.opts.Version
	var b bytes.Buffer
	if err := v.pages[page].ExecuteTemplate(&b, "layout", data); err != nil {
		v.opts.Logger.Error("render failed", "page", page, "err", err)
		http.Error(w, "template error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(b.Bytes())
}

func statusOf(err error) int {
	switch core.CodeOf(err) {
	case core.CodeNamespaceNotFound, core.CodeConversationNotFound:
		return http.StatusNotFound
	case core.CodeInvalid, core.CodeMessageTooLarge:
		return http.StatusBadRequest
	case core.CodeForbidden:
		return http.StatusForbidden
	case core.CodeConversationConcluded, core.CodeTooManyWatchers:
		return http.StatusConflict
	default:
		return http.StatusInternalServerError
	}
}

// fail renders the error page for err.
func (v *viewer) fail(w http.ResponseWriter, err error, data Page) {
	status := statusOf(err)
	if core.CodeOf(err) == "" {
		v.opts.Logger.Error("viewer request failed", "err", err)
		err = errors.New("something went wrong")
	}
	data.Notice = err.Error()
	if data.Title == "" {
		data.Title = http.StatusText(status)
	}
	v.render(w, status, "error", data)
}

func (v *viewer) principal(w http.ResponseWriter, r *http.Request) (core.Principal, bool) {
	p, err := v.authn.Authenticate(r)
	if err != nil {
		v.fail(w, err, Page{})
		return core.Principal{}, false
	}
	return p, true
}

func (v *viewer) root(w http.ResponseWriter, r *http.Request) {
	p, ok := v.principal(w, r)
	if !ok {
		return
	}
	ns, err := v.svc.ListNamespaces(r.Context(), p)
	if err != nil {
		v.fail(w, err, Page{})
		return
	}
	if len(ns) == 1 {
		http.Redirect(w, r, "/n/"+ns[0].Name, http.StatusFound)
		return
	}
	v.render(w, http.StatusOK, "namespaces", Page{Title: "Namespaces", Namespaces: ns})
}

func (v *viewer) namespace(w http.ResponseWriter, r *http.Request) {
	p, ok := v.principal(w, r)
	if !ok {
		return
	}
	ns := r.PathValue("ns")
	archived := r.URL.Query().Get("archived") == "1"
	cs, err := v.svc.ListConversations(r.Context(), p, ns, core.ListParams{IncludeArchived: archived})
	if err != nil {
		v.fail(w, err, Page{Namespace: ns})
		return
	}
	v.render(w, http.StatusOK, "conversations", Page{Title: ns, Namespace: ns, Conversations: cs, IncludeArchived: archived})
}

func (v *viewer) conversationPage(r *http.Request, p core.Principal) (Page, error) {
	ns, name := r.PathValue("ns"), r.PathValue("conv")
	data := Page{Namespace: ns, Title: name}
	c, err := v.svc.GetConversation(r.Context(), p, ns, name)
	if err != nil {
		return data, err
	}
	after := max(c.LastSeq-int64(v.opts.ShownMessages), 0)
	res, err := v.svc.Read(r.Context(), p, ns, name, core.ReadParams{After: &after, Limit: v.opts.ShownMessages})
	if err != nil {
		return data, err
	}
	data.Conversation, data.Messages, data.Truncated = &c, res.Messages, after > 0
	if ck, err := r.Cookie(observerCookie); err == nil && core.ValidName(ck.Value) {
		data.Observer = ck.Value
	}
	return data, nil
}

func (v *viewer) conversation(w http.ResponseWriter, r *http.Request) {
	p, ok := v.principal(w, r)
	if !ok {
		return
	}
	data, err := v.conversationPage(r, p)
	if err != nil {
		v.fail(w, err, data)
		return
	}
	v.render(w, http.StatusOK, "conversation", data)
}

func (v *viewer) observe(w http.ResponseWriter, r *http.Request) {
	p, ok := v.principal(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := r.ParseForm(); err != nil {
		v.fail(w, &core.Error{Code: core.CodeInvalid, Message: "could not read the form"}, Page{})
		return
	}
	ns, name := r.PathValue("ns"), r.PathValue("conv")
	observer := strings.TrimSpace(r.PostForm.Get("name"))
	_, err := v.svc.Observe(r.Context(), p, ns, name, core.ObserveParams{Name: observer, Body: r.PostForm.Get("body")})
	if err != nil {
		data, perr := v.conversationPage(r, p)
		if perr != nil {
			v.fail(w, perr, data)
			return
		}
		data.Notice = err.Error()
		data.Observer, data.Draft = observer, r.PostForm.Get("body")
		v.render(w, http.StatusBadRequest, "conversation", data)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: observerCookie, Value: observer, Path: "/", MaxAge: 365 * 24 * 3600,
		HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, "/n/"+ns+"/c/"+name+"#messages", http.StatusSeeOther)
}

// followTimeout bounds each server-side wait in the event stream; a comment
// line is sent between waits to keep proxies from closing the connection.
const followTimeout = 25 * time.Second

func (v *viewer) events(w http.ResponseWriter, r *http.Request) {
	p, ok := v.principal(w, r)
	if !ok {
		return
	}
	ns, name := r.PathValue("ns"), r.PathValue("conv")
	after, err := strconv.ParseInt(firstNonEmpty(r.Header.Get("Last-Event-ID"), r.URL.Query().Get("after"), "0"), 10, 64)
	if err != nil || after < 0 {
		http.Error(w, "after must be a non-negative integer", http.StatusBadRequest)
		return
	}
	// Check access, existence and the viewer cap before committing to a
	// 200, so a client that cannot stream gets an error status instead of
	// an empty stream it would reconnect to forever.
	c, err := v.svc.GetConversation(r.Context(), p, ns, name)
	if err != nil {
		http.Error(w, err.Error(), statusOf(err))
		return
	}
	batch, err := v.svc.Follow(r.Context(), p, ns, name, after, 0)
	if err != nil {
		http.Error(w, err.Error(), statusOf(err))
		return
	}

	rc := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	// Start with fresh state, so a reconnecting page catches up on anything
	// it missed between events.
	if v.snapshot(w, c) != nil || rc.Flush() != nil {
		return
	}
	for {
		for _, m := range batch.Messages {
			if err := v.event(w, "message", strconv.FormatInt(m.Seq, 10), m); err != nil {
				return
			}
			after = m.Seq
		}
		if len(batch.Messages) > 0 {
			c, err := v.svc.GetConversation(r.Context(), p, ns, name)
			if err != nil || v.snapshot(w, c) != nil {
				return
			}
		} else if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
			return
		}
		if rc.Flush() != nil {
			return
		}
		batch, err = v.svc.Follow(r.Context(), p, ns, name, after, followTimeout)
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				v.opts.Logger.Info("event stream ended", "err", err)
			}
			return
		}
	}
}

// snapshot sends the conversation's status, participant panel and about
// panel.
func (v *viewer) snapshot(w http.ResponseWriter, c core.Conversation) error {
	if err := writeEvent(w, "status", "", string(c.Status)); err != nil {
		return err
	}
	if err := v.event(w, "participants", "", c); err != nil {
		return err
	}
	return v.event(w, "about", "", c)
}

// event writes one server-sent event whose data is the named template
// rendered with data ("message" for a Message, "participants" and "about"
// for a Conversation).
func (v *viewer) event(w http.ResponseWriter, name, id string, data any) error {
	var b bytes.Buffer
	if err := v.pages["conversation"].ExecuteTemplate(&b, name, data); err != nil {
		v.opts.Logger.Error("render failed", "template", name, "err", err)
		return err
	}
	return writeEvent(w, name, id, b.String())
}

// writeEvent frames data as a server-sent event. CR, LF and CRLF all end a
// line in the event-stream format, so every line break in data becomes its
// own data: line; nothing in data can start a new field.
func writeEvent(w io.Writer, name, id, data string) error {
	data = strings.ReplaceAll(data, "\r\n", "\n")
	data = strings.ReplaceAll(data, "\r", "\n")
	var out strings.Builder
	out.WriteString("event: " + name + "\n")
	if id != "" {
		out.WriteString("id: " + id + "\n")
	}
	for line := range strings.SplitSeq(strings.TrimRight(data, "\n"), "\n") {
		out.WriteString("data: " + line + "\n")
	}
	out.WriteString("\n")
	_, err := io.WriteString(w, out.String())
	return err
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
