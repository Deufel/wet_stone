package handler

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"

	"whiteboard/board"
	"whiteboard/bus"
	"whiteboard/db"
	"whiteboard/hub"
	"whiteboard/view"

	"github.com/starfederation/datastar-go/datastar"
)

// A student's own pencil. On a book's whiteboard, only the teacher (its
// owner) changes the board; anyone else draws with a pencil only they see:
// comments, questions, notes for themselves. Their pointer still moves their
// cursor on the board; a press draws a mark instead of a shape. A mark is
// kept when the stroke ends, shown only on their own stream, and goes with
// the page the teacher sends next.
//
//	POST /b/{name}/marks/clear   a student clears their marks on the whiteboard

type markKey struct {
	board string
	user  int64
}

// stroke is a mark being drawn.
type stroke struct {
	mu     sync.Mutex
	points []int32
}

// student: is this viewer drawing in their own layer here (a book's
// whiteboard that is not theirs)?
func (h *Handler) student(meta *hub.Meta, u *db.User) bool {
	if u == nil || meta.OwnerID == u.ID {
		return false
	}
	return h.isWhiteboard(meta.ID)
}

// isWhiteboard: is the board a book's whiteboard? Asked on every pointer
// move, so remembered (a board becomes one once, and stays one).
func (h *Handler) isWhiteboard(boardID int64) bool {
	if v, ok := h.whiteboards.Load(boardID); ok {
		return v.(bool)
	}
	is := h.DB.WhiteboardBook(context.Background(), boardID) != ""
	if is {
		h.whiteboards.Store(boardID, true)
	}
	return is
}

// markCommand is a student's pointer: the cursor on the board, the stroke
// in their own layer.
func (h *Handler) markCommand(w http.ResponseWriter, r *http.Request, n string, u *db.User, meta *hub.Meta, op string, s signals) {
	defer datastar.NewSSE(w, r)
	x, y := clamp(s.X, 0, board.W), clamp(s.Y, 0, height(meta))
	if op == "down" || op == "move" || op == "up" {
		// their cursor moves on the board for everyone, drawing nothing
		_ = h.Hub.Apply(r.Context(), n, board.Cmd{Op: "move", UID: s.UID, Seq: s.Seq, X: x, Y: y})
	}
	key := markKey{n, u.ID}
	switch op {
	case "down":
		h.marking.Store(key, &stroke{points: []int32{x, y}})
		h.marksChanged(n, u.ID)
	case "move":
		if v, ok := h.marking.Load(key); ok {
			st := v.(*stroke)
			st.mu.Lock()
			p := st.points
			if k := len(p); k < 2 || (x-p[k-2])*(x-p[k-2])+(y-p[k-1])*(y-p[k-1]) >= board.LaserStep*board.LaserStep {
				st.points = append(p, x, y)
			}
			st.mu.Unlock()
			h.marksChanged(n, u.ID)
		}
	case "up":
		v, ok := h.marking.LoadAndDelete(key)
		if !ok {
			return
		}
		st := v.(*stroke)
		st.mu.Lock()
		p := append(st.points, x, y)
		st.mu.Unlock()
		if len(p) >= 6 && len(p) <= 2*board.PenPointsMax {
			if err := h.DB.AddMark(r.Context(), meta.ID, u.ID, p); err != nil {
				slog.Error("mark", "board", n, "err", err)
			}
		}
		h.marksChanged(n, u.ID)
	}
}

func (h *Handler) clearMarks(w http.ResponseWriter, r *http.Request) {
	n, u, meta, ok := h.open(w, r, false)
	if !ok {
		return
	}
	if err := h.DB.ClearMarks(r.Context(), meta.ID, u.ID); err != nil {
		slog.Error("clear marks", "board", n, "err", err)
	}
	h.marksChanged(n, u.ID)
	datastar.NewSSE(w, r)
}

// marksVersion counts each student's marks changing on each board.
var marksVersion sync.Map // markKey → *atomic.Int64

func marksAt(k markKey) *atomic.Int64 {
	v, _ := marksVersion.LoadOrStore(k, new(atomic.Int64))
	return v.(*atomic.Int64)
}

// marksChanged wakes that student's streams on that board.
func (h *Handler) marksChanged(name string, userID int64) {
	marksAt(markKey{name, userID}).Add(1)
	_ = h.Bus.Publish(bus.MarksSubject(name, userID), nil)
}

// marksSVG is a student's layer: their kept marks and the stroke underway.
func (h *Handler) marksSVG(r *http.Request, n string, boardID int64, u *db.User, height int32) string {
	marks, _ := h.DB.Marks(r.Context(), boardID, u.ID, 0)
	if v, ok := h.marking.Load(markKey{n, u.ID}); ok {
		st := v.(*stroke)
		st.mu.Lock()
		marks = append(marks, append([]int32(nil), st.points...))
		st.mu.Unlock()
	}
	return renderString(r, view.Marks(marks, height))
}

// marksOver are a reader's marks on a page, for its drawing.
func marksOver(marks [][]int32) string {
	if len(marks) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(`<g class="marks">`)
	for _, m := range marks {
		if len(m) >= 4 {
			sb.WriteString(`<path class="mark" d="` + board.PenPath(m) + `"/>`)
		}
	}
	sb.WriteString(`</g>`)
	return sb.String()
}
