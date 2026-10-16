package handler

import (
	"context"
	"encoding/json"
	"log/slog"
	"math"
	"net/http"
	"strings"
	"time"

	"whiteboard/auth"
	"whiteboard/board"
	"whiteboard/db"

	"github.com/go-chi/chi/v5"
	"github.com/starfederation/datastar-go/datastar"
)

// The demos: a new public board, and on it a presenter ("Demo") who plays a
// lesson script (lesson.go, lessons/*.json) the way a person would, through
// the same commands a page sends. Anyone with the link can watch it happen.

const demoUID = 0xDE30 // the presenter's viewer id on the board

// demo makes a board for a built-in lesson (/demo/{kind}: lessons/{kind}.json),
// sends the page to it, and starts the presenter.
func (h *Handler) demo(w http.ResponseWriter, r *http.Request) {
	u := auth.User(r.Context())
	if u == nil {
		http.Error(w, "sign in first", http.StatusUnauthorized)
		return
	}
	l, ok := Lessons[chi.URLParam(r, "kind")]
	if !ok {
		http.NotFound(w, r)
		return
	}
	title := l.Title
	if now := h.DB.Now(r.Context()); len(now) >= 16 {
		title += " · " + now[11:16] // SQLite's clock: each run its own name
	}
	name, err := h.DB.CreateBoard(r.Context(), u, title, db.Public)
	if err != nil {
		slog.Error("demo", "err", err)
		http.Error(w, "could not make the demo board", http.StatusInternalServerError)
		return
	}
	script, _ := json.Marshal(l)
	if err := h.DB.AttachLesson(r.Context(), name, string(script)); err != nil {
		slog.Error("demo", "err", err)
	}
	h.Hub.PublishUser(u.ID)
	_ = datastar.NewSSE(w, r).Redirect("/b/" + name) // it plays as the page lands
}

// presenter drives one viewer on a board, at a human pace (pace stretches
// every pause: 1 is a quick presenter, 2 half that speed; 0 counts as 1).
type presenter struct {
	h    *Handler
	ctx  context.Context
	name string
	b    *board.Board
	uid  uint32 // its viewer id on the board
	seq  int64
	x, y int32
	pace float64
	err  error

	run  *lessonRun // the player's control (pause), or nil
	fast bool       // seeking: draw at once, no pauses, no laser, no captions
	ink  board.Ink  // the step's ink: each shape it draws takes it
}

func (p *presenter) cmd(c board.Cmd) {
	if p.err != nil {
		return
	}
	c.UID = p.uid
	p.err = p.h.Hub.Apply(p.ctx, p.name, c)
}

func (p *presenter) pause(d time.Duration) {
	if p.err != nil {
		return
	}
	if p.run != nil {
		if err := p.run.hold(p.ctx); err != nil {
			p.err = err
			return
		}
	}
	if p.fast {
		return
	}
	if p.run != nil {
		d = p.run.over(d)
	}
	if p.pace > 0 {
		d = time.Duration(float64(d) * p.pace)
	}
	select {
	case <-time.After(d):
	case <-p.ctx.Done():
		p.err = p.ctx.Err()
	}
}

// glide moves the cursor to (x, y) in small steps, as a hand would.
func (p *presenter) glide(tool uint8, x, y int32, dseq int64) {
	steps := 14
	if p.fast {
		steps = 1
	}
	x0, y0 := p.x, p.y
	for i := 1; i <= steps; i++ {
		t := float64(i) / float64(steps)
		t = t * t * (3 - 2*t) // ease in and out
		p.seq++
		p.cmd(board.Cmd{Op: "move", Tool: tool, Seq: p.seq, DSeq: dseq,
			X: x0 + int32(float64(x-x0)*t), Y: y0 + int32(float64(y-y0)*t)})
		p.pause(28 * time.Millisecond)
	}
	p.x, p.y = x, y
}

// stroke presses at (x1, y1), drags to (x2, y2), and lets go.
func (p *presenter) stroke(tool uint8, x1, y1, x2, y2 int32) {
	p.glide(0, x1, y1, 0)
	p.seq++
	down := p.seq
	p.cmd(board.Cmd{Op: "down", Tool: tool, Seq: down, DSeq: down, X: x1, Y: y1})
	p.glide(tool, x2, y2, down)
	p.seq++
	p.cmd(board.Cmd{Op: "up", Tool: tool, Seq: p.seq, DSeq: down, X: x2, Y: y2})
	if tool != 0 && tool != 4 { // a shape drawn (not a click, a drag, the laser)
		p.style()
	}
}

// box draws a box and labels it.
func (p *presenter) box(x1, y1, x2, y2 int32, label string) {
	p.stroke(1, x1, y1, x2, y2)
	p.pause(250 * time.Millisecond)
	p.label(label)
}

func (p *presenter) label(text string) {
	sel := p.b.SelectedBy(p.uid)
	if sel < 0 {
		return
	}
	if p.fast {
		p.cmd(board.Cmd{Op: "label", Shape: sel, Label: text})
		return
	}
	// typed a few letters at a time, as the label field sends them (code a
	// line's worth)
	step := 3
	if board.IsCode(text) {
		step = 14
	}
	for i := step; i < len(text); i += step {
		p.cmd(board.Cmd{Op: "label", Shape: sel, Label: text[:i]})
		p.pause(45 * time.Millisecond)
	}
	p.cmd(board.Cmd{Op: "label", Shape: sel, Label: text})
}

// laser circles (cx, cy) once.
func (p *presenter) laser(cx, cy, r int32) {
	if p.fast {
		return
	}
	p.glide(4, cx+r, cy, 0)
	p.seq++
	down := p.seq
	p.cmd(board.Cmd{Op: "down", Tool: 4, Seq: down, DSeq: down, X: cx + r, Y: cy})
	for i := 1; i <= 40; i++ {
		a := float64(i) / 40 * 2 * math.Pi
		p.seq++
		p.x, p.y = cx+int32(float64(r)*math.Cos(a)), cy+int32(float64(r)*0.8*math.Sin(a))
		p.cmd(board.Cmd{Op: "move", Tool: 4, Seq: p.seq, DSeq: down, X: p.x, Y: p.y})
		p.pause(22 * time.Millisecond)
	}
	p.seq++
	p.cmd(board.Cmd{Op: "up", Tool: 4, Seq: p.seq, DSeq: down, X: p.x, Y: p.y})
}

func (p *presenter) todo(user db.User, boardID int64, text string) {
	if p.err != nil {
		return
	}
	if err := p.h.DB.AddTodo(p.ctx, boardID, user.ID, text); err == nil {
		p.h.Hub.TodoChanged(p.ctx, p.name)
	}
}

// ─── More of the presenter's moves ──────────────────────────────────────────

// click presses and lets go at (x, y) in the select tool.
func (p *presenter) click(x, y int32) { p.stroke(0, x, y, x, y) }

// dblclick is the page's double-click: link or unlink a meeting point, or
// open a shape's label.
func (p *presenter) dblclick(x, y int32) {
	p.glide(0, x, y, 0)
	p.cmd(board.Cmd{Op: "link", X: x, Y: y})
}

// act is a key or a button on the selection: group, ungroup, nudge, delete.
func (p *presenter) act(op string, nx, ny int8) {
	p.cmd(board.Cmd{Op: op, NX: nx, NY: ny})
}

// check ticks the checklist item that starts with prefix.
func (p *presenter) check(user db.User, boardID int64, prefix string) {
	if p.err != nil {
		return
	}
	todos, err := p.h.DB.Todos(p.ctx, boardID)
	if err != nil {
		return
	}
	for _, t := range todos {
		if strings.HasPrefix(t.Text, prefix) && !t.Done {
			if p.h.DB.ToggleTodo(p.ctx, boardID, t.ID, user.ID) == nil {
				p.h.Hub.TodoChanged(p.ctx, p.name)
			}
			return
		}
	}
}
