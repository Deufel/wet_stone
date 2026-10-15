// Package handler is the HTTP surface. Land-and-stream, pure CQRS:
//
//	GET  /                     the landing page, or your boards when signed in
//	GET  /stream               the front page's view channel: your board list,
//	                           again whenever it changes on any of your devices
//	POST /new/{visibility}     make a board; the answer sends the page to it
//	POST /tabs/close/{name}    drop a board from your boards
//	GET  /b/{name}             a board (land); signed in, and allowed to open it
//	GET  /b/{name}/stream      the one view channel: the whole <svg id="board">
//	                           as a morph plus this viewer's signals on every
//	                           change; the history, share panel and tab strip
//	                           when they change
//	POST /b/{name}/{op}        a drawing or history command: apply, publish,
//	                           answer an SSE stream holding at most an ack. The
//	                           view never comes back on a command; it arrives
//	                           on the stream.
//	POST /b/{name}/share/{op}  the owner's title / invite / uninvite
//	POST /b/{name}/todo/add    a checklist item (payload {text})
//	POST /b/{name}/todo/{id}/{op}  check or uncheck it (toggle), or delete it
//	POST /b/{name}/notes       your private notes on the board (payload {text, lang})
//	GET  /b/{name}/print       the board, its checklist and your notes, for paper
//
// There are no forms. Inputs are signals; a command that stores what an input
// holds answers with ack.Saved, which the input's .saved line shows.
package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/mail"
	"regexp"
	"strconv"
	"strings"
	"time"

	"whiteboard/ack"
	"whiteboard/auth"
	"whiteboard/board"
	"whiteboard/bus"
	"whiteboard/db"
	"whiteboard/hub"
	"whiteboard/view"

	"github.com/go-chi/chi/v5"
	"github.com/starfederation/datastar-go/datastar"
)

type Handler struct {
	Hub  *hub.Hub
	Bus  *bus.Bus
	DB   *db.DB
	Auth *auth.Auth
	// Compress is the stream's encoding: "br" (default), "gzip" or "none".
	// Some proxies (ngrok) hold a compressed stream back instead of passing it on.
	Compress string
	// Instance names this server process. A deploy starts a new one while the
	// old still runs for a while (Coolify's rolling update), and a page could
	// then send its commands to the new process while its stream still hung on
	// the old: the view froze. A page carries the instance it landed on ($inst)
	// and reloads when it reaches another.
	Instance string
}

// reloadJS reloads the page, at most once in 10 s (two processes answering
// side by side during a deploy must not bounce it).
const reloadJS = `try { const t = +sessionStorage.getItem('wb-reload') || 0; if (Date.now() - t > 10000) { sessionStorage.setItem('wb-reload', Date.now()); location.reload() } } catch { location.reload() }`

// moved answers a page that landed on another server process by reloading it.
func (h *Handler) moved(w http.ResponseWriter, r *http.Request, inst string) bool {
	if inst == "" || inst == h.Instance {
		return false
	}
	_ = datastar.NewSSE(w, r).ExecuteScript(reloadJS)
	return true
}

func (h *Handler) sseOptions() []datastar.SSEOption {
	switch h.Compress {
	case "none":
		return nil
	case "gzip":
		return []datastar.SSEOption{datastar.WithCompression(datastar.WithGzip())}
	}
	return []datastar.SSEOption{datastar.WithCompression(datastar.WithBrotli())}
}

var boardName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

var ops = map[string]bool{
	"down": true, "move": true, "up": true, "delete": true, "clear": true, "link": true,
	"label": true, "editsel": true, "nudge": true, "group": true, "ungroup": true, "locktoggle": true,
}

var histOps = map[string]bool{"checkout": true, "switch": true, "delbranch": true, "mainbranch": true}

// Signals as Datastar posts them. Unknown keys (theme, hue, counts) are ignored.
type signals struct {
	UID    uint32 `json:"uid"`
	X      int32  `json:"x"`
	Y      int32  `json:"y"`
	Lx     int32  `json:"lx"`
	Ly     int32  `json:"ly"`
	Tool   uint8  `json:"tool"`
	Sel    int32  `json:"sel"`
	Label  string `json:"label"`
	Ndx    int32  `json:"ndx"`
	Ndy    int32  `json:"ndy"`
	Seq    int64  `json:"seq"`
	DSeq   int64  `json:"dseq"`
	Hrev   int64  `json:"hrev"`
	Hbr    int64  `json:"hbranch"`
	Invite string `json:"invite"`
	Who    string `json:"who"`
	Inst   string `json:"inst"`
	Title  string `json:"title"`
}

func (h *Handler) Routes(r chi.Router) {
	r.Get("/", h.home)
	r.Get("/healthz", h.healthz)
	r.Get("/privacy", func(w http.ResponseWriter, r *http.Request) {
		html(w)
		_ = view.Privacy(auth.User(r.Context())).Render(r.Context(), w)
	})
	r.Get("/terms", func(w http.ResponseWriter, r *http.Request) {
		html(w)
		_ = view.Terms(auth.User(r.Context())).Render(r.Context(), w)
	})
	r.Get("/auth/google", h.Auth.Start)
	r.Get("/auth/google/callback", h.Auth.Callback)
	r.Post("/auth/logout", h.Auth.Logout)
	r.Post("/auth/dev", h.Auth.DevLogin)
	r.Get("/stream", h.homeStream)
	r.Post("/new/{visibility}", h.newBoard)
	r.Post("/demo", h.demo)
	r.Post("/tabs/close/{name}", h.closeTab)
	r.Get("/b/{name}", h.page)
	r.Get("/b/{name}/stream", h.stream)
	r.Post("/b/{name}/share/{op}", h.share)
	r.Post("/b/{name}/todo/add", h.todoAdd)
	r.Post("/b/{name}/todo/{id}/{op}", h.todoOp)
	r.Post("/b/{name}/notes", h.notes)
	r.Get("/b/{name}/print", h.print)
	r.Post("/b/{name}/{op}", h.command)
}

// healthz: 200 "ok" while the database answers, 503 when it does not.
func (h *Handler) healthz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := h.DB.Ping(ctx); err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("db unavailable\n"))
		return
	}
	_, _ = w.Write([]byte("ok\n"))
}

func html(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
}

// home is the landing page for a visitor and the board picker for a user.
func (h *Handler) home(w http.ResponseWriter, r *http.Request) {
	u := auth.User(r.Context())
	html(w)
	if u == nil {
		_ = view.Landing(view.LandingData{Next: r.URL.Query().Get("next"), Google: h.Auth.Google(), Dev: h.Auth.Dev}).Render(r.Context(), w)
		return
	}
	tabs, err := h.tabs(r.Context(), u)
	if err != nil {
		slog.Error("tabs", "err", err)
	}
	_ = view.Home(u, tabs).Render(r.Context(), w)
}

// tabs is the user's tab strip, the global board always first.
func (h *Handler) tabs(ctx context.Context, u *db.User) ([]db.Tab, error) {
	tabs, err := h.DB.Tabs(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	for _, t := range tabs {
		if t.Name == db.GlobalName {
			return tabs, nil
		}
	}
	global := db.Tab{Name: db.GlobalName, Title: "Global board", Visibility: db.Global}
	return append([]db.Tab{global}, tabs...), nil
}

// newBoard makes a board and sends the page there. Every other device's tab
// strip and board list gain it from the user subject.
func (h *Handler) newBoard(w http.ResponseWriter, r *http.Request) {
	u := auth.User(r.Context())
	if u == nil {
		http.Error(w, "sign in first", http.StatusUnauthorized)
		return
	}
	vis := chi.URLParam(r, "visibility")
	if vis != db.Private {
		vis = db.Public
	}
	name, err := h.DB.CreateBoard(r.Context(), u, "Untitled board", vis)
	if err != nil {
		slog.Error("new board", "err", err)
		http.Error(w, "could not make a board", http.StatusInternalServerError)
		return
	}
	h.Hub.PublishUser(u.ID)
	_ = datastar.NewSSE(w, r).Redirect("/b/" + name)
}

func (h *Handler) closeTab(w http.ResponseWriter, r *http.Request) {
	u := auth.User(r.Context())
	if u == nil {
		http.Error(w, "sign in first", http.StatusUnauthorized)
		return
	}
	if err := h.DB.CloseTab(r.Context(), u.ID, chi.URLParam(r, "name")); err != nil {
		slog.Error("close tab", "err", err)
		http.Error(w, "could not save", http.StatusInternalServerError)
		return
	}
	h.Hub.PublishUser(u.ID) // the list re-renders on every device, this one too
	datastar.NewSSE(w, r)
}

// homeStream is the front page's view channel: the user's board list, pushed
// on connect and whenever their boards change on any device.
func (h *Handler) homeStream(w http.ResponseWriter, r *http.Request) {
	u := auth.User(r.Context())
	if u == nil {
		http.Error(w, "sign in first", http.StatusUnauthorized)
		return
	}
	wake, unsub, err := h.wakeOn(bus.UserSubject(u.ID))
	if err != nil {
		http.Error(w, "bus unavailable", http.StatusServiceUnavailable)
		return
	}
	defer unsub()
	sse := datastar.NewSSE(w, r, h.sseOptions()...)
	var buf bytes.Buffer
	push := func() bool {
		tabs, err := h.tabs(r.Context(), u)
		if err != nil {
			slog.Error("tabs", "err", err)
			return true
		}
		buf.Reset()
		if err := view.HomeBoards(tabs).Render(r.Context(), &buf); err != nil {
			return true
		}
		return sse.PatchElements(buf.String()) == nil
	}
	keep := time.NewTicker(Beat) // the heartbeat, as on the board's stream
	defer keep.Stop()
	if !push() {
		return
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case <-wake:
			if !push() {
				return
			}
		case <-keep.C:
			if err := beat(sse); err != nil {
				return
			}
		}
	}
}

// wakeOn subscribes a latest-wins wake to a subject: events that land while a
// frame renders collapse into one.
func (h *Handler) wakeOn(subject string) (<-chan struct{}, func(), error) {
	wake := make(chan struct{}, 1)
	sub, err := h.Bus.Subscribe(subject, func(string, []byte) {
		select {
		case wake <- struct{}{}:
		default:
		}
	})
	if err != nil {
		return nil, nil, err
	}
	return wake, func() { _ = sub.Unsubscribe() }, nil
}

// open resolves a board for this request: the name is valid, the board
// exists, and the signed-in user may open it. On false it has answered.
func (h *Handler) open(w http.ResponseWriter, r *http.Request, page bool) (string, *db.User, *hub.Meta, bool) {
	n := chi.URLParam(r, "name")
	if !boardName.MatchString(n) {
		h.notFound(w, r, page)
		return "", nil, nil, false
	}
	u := auth.User(r.Context())
	if u == nil {
		if page {
			http.Redirect(w, r, auth.SignInURL(r.URL.Path), http.StatusSeeOther)
		} else {
			http.Error(w, "sign in first", http.StatusUnauthorized)
		}
		return "", nil, nil, false
	}
	meta, err := h.Hub.Meta(r.Context(), n)
	if errors.Is(err, db.ErrNoBoard) {
		h.notFound(w, r, page)
		return "", nil, nil, false
	}
	if err != nil {
		slog.Error("board", "board", n, "err", err)
		http.Error(w, "board unavailable", http.StatusServiceUnavailable)
		return "", nil, nil, false
	}
	if !meta.CanOpen(u) {
		if page {
			html(w)
			w.WriteHeader(http.StatusForbidden)
			_ = view.Private(u).Render(r.Context(), w)
		} else {
			http.Error(w, "not yours to open", http.StatusForbidden)
		}
		return "", nil, nil, false
	}
	return n, u, meta, true
}

func (h *Handler) notFound(w http.ResponseWriter, r *http.Request, page bool) {
	if !page {
		http.NotFound(w, r)
		return
	}
	html(w)
	w.WriteHeader(http.StatusNotFound)
	_ = view.NotFound(auth.User(r.Context())).Render(r.Context(), w)
}

func (h *Handler) page(w http.ResponseWriter, r *http.Request) {
	n, u, meta, ok := h.open(w, r, true)
	if !ok {
		return
	}
	_, hist, err := h.Hub.Board(r.Context(), n)
	if err != nil {
		slog.Error("board", "board", n, "err", err)
		http.Error(w, "board unavailable", http.StatusServiceUnavailable)
		return
	}
	if n != db.GlobalName {
		if added, _ := h.DB.OpenTab(r.Context(), u.ID, meta.ID); added {
			h.Hub.PublishUser(u.ID) // your other devices gain the tab
		}
	}
	tabs, err := h.tabs(r.Context(), u)
	if err != nil {
		slog.Error("tabs", "err", err)
	}
	html(w)
	if err := view.Board(view.BoardData{Inst: h.Instance, Name: n, Link: h.Auth.BaseURL + "/b/" + n, User: u, Meta: meta, Hist: hist, Tabs: tabs}).Render(r.Context(), w); err != nil {
		slog.Error("page", "err", err)
	}
}

// stream is the view channel. It joins the viewer, pushes the current frame at
// once (a reconnect lands on truth), then pushes again on every board event.
// One goroutine does every write, so frames and keepalives never interleave.
func (h *Handler) stream(w http.ResponseWriter, r *http.Request) {
	n, u, meta0, ok := h.open(w, r, false)
	if !ok {
		return
	}
	var s signals
	if err := datastar.ReadSignals(r, &s); err != nil {
		http.Error(w, "bad signals", http.StatusBadRequest)
		return
	}
	if h.moved(w, r, s.Inst) {
		return
	}
	b, _, err := h.Hub.Board(r.Context(), n)
	if err != nil {
		slog.Error("board", "board", n, "err", err)
		http.Error(w, "board unavailable", http.StatusServiceUnavailable)
		return
	}

	// the board's events wake the frame; the user's own wake the tab strip
	wake, unsub, err := h.wakeOn(bus.BoardSubject(n))
	if err != nil {
		http.Error(w, "bus unavailable", http.StatusServiceUnavailable)
		return
	}
	defer unsub()
	uwake, uunsub, err := h.wakeOn(bus.UserSubject(u.ID))
	if err != nil {
		http.Error(w, "bus unavailable", http.StatusServiceUnavailable)
		return
	}
	defer uunsub()

	sse := datastar.NewSSE(w, r, h.sseOptions()...)
	b.Join(s.UID, u.First())
	h.Hub.Publish(n) // everyone else sees one more viewer
	defer func() {
		b.Leave(s.UID)
		h.Hub.Publish(n)
	}()

	sent := board.NewSent()
	sentHist, sentMeta := 0, 0
	// the history panel goes at most once a second: it is the heaviest part
	// (a thumbnail per revision), and a burst of drawing re-rendered it per
	// shape. A change inside the second arms a trailing push.
	var histAt time.Time
	histLater := time.NewTimer(time.Hour)
	histLater.Stop()
	defer histLater.Stop()
	histArmed := false
	var buf bytes.Buffer
	// what is the viewer's own: the tab strip, and their notes on this board
	// (pushed as a signal: the editor is never morphed under their caret)
	var sentNote int64 = -1
	tabs := func() bool {
		ctx := r.Context()
		if note, err := h.DB.Note(ctx, u.ID, meta0.ID); err == nil && note.Updated != sentNote {
			b, _ := json.Marshal(map[string]db.Note{"_notes": note})
			if sse.PatchSignals(b) != nil {
				return false
			}
			sentNote = note.Updated
		}
		meta, err := h.Hub.Meta(ctx, n)
		if err != nil {
			return true
		}
		list, err := h.tabs(ctx, u)
		if err != nil {
			slog.Error("tabs", "err", err)
			return true
		}
		buf.Reset()
		if err := view.DrawerBoards(n, meta, list).Render(ctx, &buf); err != nil {
			return true
		}
		return sse.PatchElements(buf.String()) == nil
	}
	history := func() bool {
		hist := h.Hub.History(n)
		if hist == nil || hist.Version == sentHist {
			return true
		}
		if wait := time.Second - time.Since(histAt); wait > 0 {
			if !histArmed {
				histArmed = true
				histLater.Reset(wait)
			}
			return true
		}
		buf.Reset()
		if err := view.History(n, hist).Render(r.Context(), &buf); err == nil {
			if sse.PatchElements(buf.String()) != nil {
				return false
			}
		}
		sentHist, histAt = hist.Version, time.Now()
		return true
	}
	var sentTodo int64
	push := func() bool {
		ctx := r.Context()
		if v := h.Hub.TodoVersion(ctx, n); v != sentTodo {
			list, err := h.DB.Todos(ctx, meta0.ID)
			if err != nil {
				slog.Error("todos", "board", n, "err", err)
			} else {
				buf.Reset()
				if err := view.TodoLists(n, list).Render(ctx, &buf); err == nil {
					if sse.PatchElements(buf.String()) != nil {
						return false
					}
				}
				sentTodo = v
			}
		}
		// the share panel only when it changed: another morph target on the
		// same stream, never a second channel
		if meta, err := h.Hub.Meta(ctx, n); err == nil && meta.Version != sentMeta {
			if !meta.CanOpen(u) { // uninvited, or no longer theirs: sent home
				_ = sse.Redirect("/")
				return false
			}
			buf.Reset()
			if err := view.MetaParts(n, h.Auth.BaseURL+"/b/"+n, u, meta).Render(ctx, &buf); err == nil {
				if sse.PatchElements(buf.String()) != nil {
					return false
				}
			}
			sentMeta = meta.Version
		}
		frame, sig := b.Frame(s.UID, &sent)
		if err := sse.PatchElements(frame); err != nil {
			return false
		}
		if sse.PatchSignals(sig) != nil {
			return false
		}
		return history() // after the drawing, never ahead of it
	}

	// The heartbeat: every Beat a tiny signal ($_beat) goes down, so nothing in
	// the middle culls an idle stream, and the page's sentinel (Datastar, view/js.go aliveCheck) knows
	// the server can still reach it; when the beats stop it greys the page.
	keep := time.NewTicker(Beat)
	defer keep.Stop()

	// the tab strip once on connect, too: a page landed before a change on
	// another device (or a reconnect) catches up
	if !push() || !tabs() {
		return
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case <-wake:
			if !push() {
				return
			}
		case <-uwake:
			if !tabs() {
				return
			}
		case <-histLater.C:
			histArmed = false
			if !history() {
				return
			}
		case <-keep.C:
			if err := beat(sse); err != nil {
				return
			}
		}
	}
}

func (h *Handler) command(w http.ResponseWriter, r *http.Request) {
	n, _, _, ok := h.open(w, r, false)
	if !ok {
		return
	}
	op := chi.URLParam(r, "op")
	if !ops[op] && !histOps[op] {
		http.NotFound(w, r)
		return
	}
	var s signals
	if err := datastar.ReadSignals(r, &s); err != nil {
		http.Error(w, "bad signals", http.StatusBadRequest)
		return
	}
	if h.moved(w, r, s.Inst) {
		return
	}
	if histOps[op] {
		id := s.Hbr
		if op == "checkout" {
			id = s.Hrev
		}
		if err := h.Hub.Hist(r.Context(), n, hub.HistOp{Kind: op, ID: id}); err != nil {
			slog.Error("history", "board", n, "op", op, "err", err)
			http.Error(w, "board unavailable", http.StatusServiceUnavailable)
			return
		}
		datastar.NewSSE(w, r)
		return
	}
	c := board.Cmd{
		Op: op, UID: s.UID, Tool: s.Tool, Seq: s.Seq, DSeq: s.DSeq,
		X: clamp(s.X, 0, board.W), Y: clamp(s.Y, 0, board.H),
		NX: int8(clamp(s.Ndx, -1, 1)), NY: int8(clamp(s.Ndy, -1, 1)),
	}
	switch op {
	case "link":
		c.X, c.Y = clamp(s.Lx, 0, board.W), clamp(s.Ly, 0, board.H) // the double-click's own coordinate
	case "label":
		c.Shape, c.Label = s.Sel, cleanText(s.Label, board.LabelMax)
	}
	if err := h.Hub.Apply(r.Context(), n, c); err != nil {
		slog.Error("command", "board", n, "op", op, "err", err)
		http.Error(w, "board unavailable", http.StatusServiceUnavailable)
		return
	}
	sse := datastar.NewSSE(w, r) // what @post expects: empty, or just the ack
	if op == "label" {
		ack.Saved(sse, "label", "Saved")
	}
}

// share: the owner renames the board or changes who may open it.
func (h *Handler) share(w http.ResponseWriter, r *http.Request) {
	n, u, meta, ok := h.open(w, r, false)
	if !ok {
		return
	}
	if meta.OwnerID != u.ID {
		http.Error(w, "only the owner can change this board", http.StatusForbidden)
		return
	}
	var s signals
	if err := datastar.ReadSignals(r, &s); err != nil {
		http.Error(w, "bad signals", http.StatusBadRequest)
		return
	}
	if h.moved(w, r, s.Inst) {
		return
	}
	op, arg := chi.URLParam(r, "op"), ""
	switch op {
	case "title":
		arg = cleanText(s.Title, 60)
		if arg == "" {
			arg = "Untitled board"
		}
	case "invite":
		if meta.Visibility != db.Private {
			ack.Bad(datastar.NewSSE(w, r), "invite", "Only a private board takes invites")
			return
		}
		addr, err := mail.ParseAddress(strings.TrimSpace(s.Invite))
		if err != nil {
			ack.Bad(datastar.NewSSE(w, r), "invite", "That is not an email address")
			return
		}
		arg = strings.ToLower(addr.Address)
	case "uninvite":
		arg = strings.ToLower(strings.TrimSpace(s.Who))
	default:
		http.NotFound(w, r)
		return
	}
	if err := h.Hub.Share(r.Context(), n, op, arg); err != nil {
		slog.Error("share", "board", n, "op", op, "err", err)
		http.Error(w, "could not save", http.StatusInternalServerError)
		return
	}
	sse := datastar.NewSSE(w, r)
	switch op {
	case "title":
		ack.Saved(sse, "title", "Saved")
	case "invite": // the field empties itself for the next address
		_ = sse.PatchSignals([]byte(`{"invite": ""}`))
		ack.Saved(sse, "invite", "Invited "+arg)
	case "uninvite":
		ack.Saved(sse, "invite", "Removed "+arg)
	}
}

// payload is what the checklist and the notes post: their text, not signals.
type payload struct {
	Text string `json:"text"`
	Lang string `json:"lang"`
	Inst string `json:"inst"`
}

func (h *Handler) todoAdd(w http.ResponseWriter, r *http.Request) {
	n, u, meta, ok := h.open(w, r, false)
	if !ok {
		return
	}
	var p payload
	if err := datastar.ReadSignals(r, &p); err != nil {
		http.Error(w, "bad payload", http.StatusBadRequest)
		return
	}
	if h.moved(w, r, p.Inst) {
		return
	}
	sse := datastar.NewSSE(w, r)
	text := cleanText(p.Text, 200)
	if text == "" {
		return
	}
	if err := h.DB.AddTodo(r.Context(), meta.ID, u.ID, text); err != nil {
		slog.Error("todo", "board", n, "err", err)
		ack.Bad(sse, "todo", "Not saved")
		return
	}
	h.Hub.TodoChanged(r.Context(), n)
}

func (h *Handler) todoOp(w http.ResponseWriter, r *http.Request) {
	n, u, meta, ok := h.open(w, r, false)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	var s signals
	if err := datastar.ReadSignals(r, &s); err == nil && h.moved(w, r, s.Inst) {
		return
	}
	switch chi.URLParam(r, "op") {
	case "toggle":
		err = h.DB.ToggleTodo(r.Context(), meta.ID, id, u.ID)
	case "delete":
		err = h.DB.DeleteTodo(r.Context(), meta.ID, id)
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		slog.Error("todo", "board", n, "err", err)
		http.Error(w, "could not save", http.StatusInternalServerError)
		return
	}
	h.Hub.TodoChanged(r.Context(), n)
	datastar.NewSSE(w, r)
}

// notes saves the viewer's private notes; their other devices get them from
// the user subject.
func (h *Handler) notes(w http.ResponseWriter, r *http.Request) {
	_, u, meta, ok := h.open(w, r, false)
	if !ok {
		return
	}
	var p payload
	if err := datastar.ReadSignals(r, &p); err != nil {
		http.Error(w, "bad payload", http.StatusBadRequest)
		return
	}
	if h.moved(w, r, p.Inst) {
		return
	}
	if len(p.Text) > 64<<10 {
		ack.Bad(datastar.NewSSE(w, r), "notes", "Too long to save")
		return
	}
	if !view.NoteLang(p.Lang) {
		p.Lang = ""
	}
	if err := h.DB.SaveNote(r.Context(), u.ID, meta.ID, p.Text, p.Lang); err != nil {
		slog.Error("notes", "err", err)
		ack.Bad(datastar.NewSSE(w, r), "notes", "Not saved")
		return
	}
	h.Hub.PublishUser(u.ID)
	ack.Saved(datastar.NewSSE(w, r), "notes", "Saved")
}

// print is the board for paper: US Letter, the drawing on the first page,
// the checklist and the viewer's notes after.
func (h *Handler) print(w http.ResponseWriter, r *http.Request) {
	n, u, meta, ok := h.open(w, r, true)
	if !ok {
		return
	}
	b, _, err := h.Hub.Board(r.Context(), n)
	if err != nil {
		http.Error(w, "board unavailable", http.StatusServiceUnavailable)
		return
	}
	todos, _ := h.DB.Todos(r.Context(), meta.ID)
	note, _ := h.DB.Note(r.Context(), u.ID, meta.ID)
	html(w)
	_ = view.Print(view.PrintData{Name: n, Meta: meta, Drawing: b.Print(), Todos: todos, Note: note, Printed: h.DB.Now(r.Context())}).Render(r.Context(), w)
}

// Beat is the heartbeat's period; the page's sentinel calls the line lost after
// a little over two missed beats.
const Beat = 5 * time.Second

func beat(sse *datastar.ServerSentEventGenerator) error {
	return sse.PatchSignals([]byte(`{"_beat":` + strconv.FormatInt(time.Now().UnixMilli(), 10) + `}`))
}

// Control characters dropped, capped at max bytes on a rune boundary.
func cleanText(s string, max int) string {
	s = strings.TrimSpace(s)
	out := make([]rune, 0, len(s))
	n := 0
	for _, c := range s {
		if c < 32 || c == 127 {
			continue
		}
		if n+len(string(c)) > max {
			break
		}
		n += len(string(c))
		out = append(out, c)
	}
	return string(out)
}

func clamp(v, lo, hi int32) int32 { return min(max(v, lo), hi) }
