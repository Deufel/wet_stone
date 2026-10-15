// Package handler is the HTTP surface. Land-and-stream, pure CQRS:
//
//	GET  /b/{name}          the page (land)
//	GET  /b/{name}/stream   the one view channel: the whole <svg id="board"> as
//	                        a morph, plus this viewer's signals, on every change
//	POST /b/{name}/{op}     a command: read signals, apply, publish, answer an
//	                        empty SSE stream. The view never comes back on a
//	                        command, the caller's included; it arrives on /stream.
//	                        History commands (checkout, switch, delbranch,
//	                        mainbranch) go to the board's keeper the same way.
package handler

import (
	"bytes"
	"log/slog"
	"net/http"
	"regexp"
	"time"

	"whiteboard/board"
	"whiteboard/bus"
	"whiteboard/hub"
	"whiteboard/view"

	"github.com/go-chi/chi/v5"
	"github.com/starfederation/datastar-go/datastar"
)

type Handler struct {
	Hub *hub.Hub
	Bus *bus.Bus
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
	UID   uint32 `json:"uid"`
	X     int32  `json:"x"`
	Y     int32  `json:"y"`
	Lx    int32  `json:"lx"`
	Ly    int32  `json:"ly"`
	Tool  uint8  `json:"tool"`
	Sel   int32  `json:"sel"`
	Label string `json:"label"`
	Ndx   int32  `json:"ndx"`
	Ndy   int32  `json:"ndy"`
	Seq   int64  `json:"seq"`
	Hrev  int64  `json:"hrev"`
	Hbr   int64  `json:"hbranch"`
}

func (h *Handler) Routes(r chi.Router) {
	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/b/main", http.StatusFound)
	})
	r.Get("/b/{name}", h.page)
	r.Get("/b/{name}/stream", h.stream)
	r.Post("/b/{name}/{op}", h.command)
}

func name(w http.ResponseWriter, r *http.Request) (string, bool) {
	n := chi.URLParam(r, "name")
	if !boardName.MatchString(n) {
		http.NotFound(w, r)
		return "", false
	}
	return n, true
}

func (h *Handler) page(w http.ResponseWriter, r *http.Request) {
	n, ok := name(w, r)
	if !ok {
		return
	}
	_, hist, err := h.Hub.Board(r.Context(), n)
	if err != nil {
		slog.Error("board", "board", n, "err", err)
		http.Error(w, "board unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := view.Page(n, hist).Render(r.Context(), w); err != nil {
		slog.Error("page", "err", err)
	}
}

// stream is the view channel. It joins the viewer, pushes the current frame at
// once (a reconnect lands on truth), then pushes again on every board event.
// One goroutine does every write, so frames and keepalives never interleave.
func (h *Handler) stream(w http.ResponseWriter, r *http.Request) {
	n, ok := name(w, r)
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
	b.Join(s.UID)
	h.Hub.Publish(n) // everyone else sees one more viewer
	defer func() {
		b.Leave(s.UID)
		h.Hub.Publish(n)
	}()

	sent := board.NewSent()
	sentHist := 0
	var buf bytes.Buffer
	push := func() bool {
		// the history panel only when it changed: a second morph target, same stream
		if hist := h.Hub.History(n); hist != nil && hist.Version != sentHist {
			buf.Reset()
			if err := view.History(n, hist).Render(r.Context(), &buf); err != nil {
				slog.Error("history render", "err", err)
			} else if err := sse.PatchElements(buf.String()); err != nil {
				return false
			}
			sentHist = hist.Version
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
	n, ok := name(w, r)
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
		c.Shape, c.Label = s.Sel, cleanLabel(s.Label)
	}
	if err := h.Hub.Apply(r.Context(), n, c); err != nil {
		slog.Error("command", "board", n, "op", op, "err", err)
		http.Error(w, "board unavailable", http.StatusServiceUnavailable)
		return
	}
	datastar.NewSSE(w, r) // an empty stream: what @post expects, and nothing more
}

// Control characters dropped, capped at LabelMax bytes on a rune boundary.
func cleanLabel(s string) string {
	out := make([]rune, 0, len(s))
	n := 0
	for _, c := range s {
		if c < 32 || c == 127 {
			continue
		}
		if n+len(string(c)) > board.LabelMax {
			break
		}
		n += len(string(c))
		out = append(out, c)
	}
	return string(out)
}

func clamp(v, lo, hi int32) int32 { return min(max(v, lo), hi) }
