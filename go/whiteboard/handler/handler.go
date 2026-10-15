// Package handler is the HTTP surface. Land-and-stream, pure CQRS:
//
//	GET  /                     the landing page, or your boards when signed in
//	POST /new                  make a board (form + 303 to it: a navigation)
//	POST /tabs/close/{name}    drop a board from your tabs (form + 303)
//	GET  /b/{name}             a board (land); signed in, and allowed to open it
//	GET  /b/{name}/stream      the one view channel: the whole <svg id="board">
//	                           as a morph plus this viewer's signals on every
//	                           change; the history and share panels when they change
//	POST /b/{name}/{op}        a drawing or history command: apply, publish,
//	                           answer an empty SSE stream. The view never comes
//	                           back on a command; it arrives on /stream.
//	POST /b/{name}/share/{op}  the owner's title / invite / uninvite
package handler

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/mail"
	"regexp"
	"strings"
	"time"

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
	"label": true, "nudge": true, "group": true, "ungroup": true, "locktoggle": true,
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
	Hrev   int64  `json:"hrev"`
	Hbr    int64  `json:"hbranch"`
	Invite string `json:"invite"`
	Who    string `json:"who"`
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
	r.Post("/new", h.newBoard)
	r.Post("/tabs/close/{name}", h.closeTab)
	r.Get("/b/{name}", h.page)
	r.Get("/b/{name}/stream", h.stream)
	r.Post("/b/{name}/share/{op}", h.share)
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

func (h *Handler) newBoard(w http.ResponseWriter, r *http.Request) {
	u := auth.User(r.Context())
	if u == nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	vis := r.FormValue("visibility")
	if vis != db.Private {
		vis = db.Public
	}
	title := cleanText(r.FormValue("title"), 60)
	if title == "" {
		title = "Untitled board"
	}
	name, err := h.DB.CreateBoard(r.Context(), u, title, vis)
	if err != nil {
		slog.Error("new board", "err", err)
		http.Error(w, "could not make a board", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/b/"+name, http.StatusSeeOther)
}

func (h *Handler) closeTab(w http.ResponseWriter, r *http.Request) {
	if u := auth.User(r.Context()); u != nil {
		_ = h.DB.CloseTab(r.Context(), u.ID, chi.URLParam(r, "name"))
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
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
		_ = h.DB.OpenTab(r.Context(), u.ID, meta.ID)
	}
	tabs, err := h.tabs(r.Context(), u)
	if err != nil {
		slog.Error("tabs", "err", err)
	}
	html(w)
	if err := view.Board(view.BoardData{Name: n, Link: h.Auth.BaseURL + "/b/" + n, User: u, Meta: meta, Hist: hist, Tabs: tabs}).Render(r.Context(), w); err != nil {
		slog.Error("page", "err", err)
	}
}

// stream is the view channel. It joins the viewer, pushes the current frame at
// once (a reconnect lands on truth), then pushes again on every board event.
// One goroutine does every write, so frames and keepalives never interleave.
func (h *Handler) stream(w http.ResponseWriter, r *http.Request) {
	n, u, _, ok := h.open(w, r, false)
	if !ok {
		return
	}
	var s signals
	if err := datastar.ReadSignals(r, &s); err != nil {
		http.Error(w, "bad signals", http.StatusBadRequest)
		return
	}
	b, _, err := h.Hub.Board(r.Context(), n)
	if err != nil {
		slog.Error("board", "board", n, "err", err)
		http.Error(w, "board unavailable", http.StatusServiceUnavailable)
		return
	}

	// a latest-wins wake: events that land while a frame renders collapse into one
	wake := make(chan struct{}, 1)
	sub, err := h.Bus.Subscribe(bus.BoardSubject(n), func(string, []byte) {
		select {
		case wake <- struct{}{}:
		default:
		}
	})
	if err != nil {
		http.Error(w, "bus unavailable", http.StatusServiceUnavailable)
		return
	}
	defer sub.Unsubscribe()

	sse := datastar.NewSSE(w, r, h.sseOptions()...)
	b.Join(s.UID, u.First())
	h.Hub.Publish(n) // everyone else sees one more viewer
	defer func() {
		b.Leave(s.UID)
		h.Hub.Publish(n)
	}()

	sent := board.NewSent()
	sentHist, sentMeta := 0, 0
	var buf bytes.Buffer
	push := func() bool {
		ctx := r.Context()
		// the history panel and the share panel only when they changed: more
		// morph targets on the same stream, never a second channel
		if hist := h.Hub.History(n); hist != nil && hist.Version != sentHist {
			buf.Reset()
			if err := view.History(n, hist).Render(ctx, &buf); err == nil {
				if sse.PatchElements(buf.String()) != nil {
					return false
				}
			}
			sentHist = hist.Version
		}
		if meta, err := h.Hub.Meta(ctx, n); err == nil && meta.Version != sentMeta {
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
		return sse.PatchSignals(sig) == nil
	}

	// A keepalive, as in eos-go: a data-less event every 15 s so nothing in the
	// middle culls an idle stream. It is the only timed thing on this wire.
	keep := time.NewTicker(15 * time.Second)
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
			if err := sse.Send(datastar.EventType("keepalive"), nil); err != nil {
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
		Op: op, UID: s.UID, Tool: s.Tool, Seq: s.Seq,
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
	datastar.NewSSE(w, r) // an empty stream: what @post expects, and nothing more
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
	op, arg := chi.URLParam(r, "op"), ""
	switch op {
	case "title":
		arg = cleanText(s.Title, 60)
		if arg == "" {
			arg = "Untitled board"
		}
	case "invite":
		addr, err := mail.ParseAddress(strings.TrimSpace(s.Invite))
		if err != nil || meta.Visibility != db.Private {
			datastar.NewSSE(w, r)
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
	if op == "invite" { // the field empties itself for the next address
		_ = sse.PatchSignals([]byte(`{"invite": ""}`))
	}
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
