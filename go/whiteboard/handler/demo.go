package handler

import (
	"context"
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

// The demo: a new public board, and on it a scripted presenter ("Demo") who
// runs a short product review the way a person would, through the same
// commands a page sends: draws the checkout flow and links it, points at the
// problem with the laser, proposes a fix, keeps the checklist, and types the
// meeting notes into the viewer's own notebook. Anyone with the link can
// watch it happen; it is also a walk through every part of the app.

const demoUID = 0xDE30 // the presenter's viewer id on the board

// The demos, by the name in /demo/{kind}: a title and the session to play.
var demos = map[string]struct {
	title string
	play  func(h *Handler, name string, user db.User)
}{
	"review": {"Demo · Checkout redesign", (*Handler).playDemo},
	"tour":   {"Demo · Controls tour", (*Handler).playTour},
	"go":     {"Demo · Go slices, under the hood", (*Handler).playSlices},
}

// demo makes the board, sends the page to it, and starts the presenter.
func (h *Handler) demo(w http.ResponseWriter, r *http.Request) {
	u := auth.User(r.Context())
	if u == nil {
		http.Error(w, "sign in first", http.StatusUnauthorized)
		return
	}
	d, ok := demos[chi.URLParam(r, "kind")]
	if !ok {
		http.NotFound(w, r)
		return
	}
	title := d.title
	if now := h.DB.Now(r.Context()); len(now) >= 16 {
		title += " · " + now[11:16] // SQLite's clock: each run its own name
	}
	name, err := h.DB.CreateBoard(r.Context(), u, title, db.Public)
	if err != nil {
		slog.Error("demo", "err", err)
		http.Error(w, "could not make the demo board", http.StatusInternalServerError)
		return
	}
	h.Hub.PublishUser(u.ID)
	go d.play(h, name, *u)
	_ = datastar.NewSSE(w, r).Redirect("/b/" + name)
}

// presenter drives one viewer on a board, at a human pace (pace stretches
// every pause: 1 is a quick presenter, 2 half that speed; 0 counts as 1).
type presenter struct {
	h    *Handler
	ctx  context.Context
	name string
	b    *board.Board
	seq  int64
	x, y int32
	pace float64
	err  error
}

func (p *presenter) cmd(c board.Cmd) {
	if p.err != nil {
		return
	}
	c.UID = demoUID
	p.err = p.h.Hub.Apply(p.ctx, p.name, c)
}

func (p *presenter) pause(d time.Duration) {
	if p.err != nil {
		return
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
	const steps = 14
	x0, y0 := p.x, p.y
	for i := 1; i <= steps; i++ {
		t := float64(i) / steps
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
}

// box draws a box and labels it.
func (p *presenter) box(x1, y1, x2, y2 int32, label string) {
	p.stroke(1, x1, y1, x2, y2)
	p.pause(250 * time.Millisecond)
	p.label(label)
}

func (p *presenter) label(text string) {
	sel := p.b.SelectedBy(demoUID)
	if sel < 0 {
		return
	}
	// typed a few letters at a time, as the label field sends them
	for i := 3; i < len(text); i += 3 {
		p.cmd(board.Cmd{Op: "label", Shape: sel, Label: text[:i]})
		p.pause(45 * time.Millisecond)
	}
	p.cmd(board.Cmd{Op: "label", Shape: sel, Label: text})
}

// laser circles (cx, cy) once.
func (p *presenter) laser(cx, cy, r int32) {
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

// The meeting notes, as the presenter types them into the viewer's notebook.
var demoNotes = []string{
	"Product review · checkout redesign",
	"Presenting: Demo · watching: the team",
	"",
	"Flow today: Cart → Shipping → Payment → Confirm",
	"- 38% of mobile users leave at Payment",
	"- the card form is the slow part",
	"",
	"Proposal",
	"- Apple Pay on Payment: skips the card form",
	"",
	"Decisions",
	"- spike Apple Pay this sprint",
	"- keep Shipping as one step",
	"",
	"Follow-ups are on the checklist →",
}

func (p *presenter) notes(user db.User, boardID int64, lines int) {
	if p.err != nil {
		return
	}
	if err := p.h.DB.SaveNote(p.ctx, user.ID, boardID, strings.Join(demoNotes[:lines], "\n"), ""); err == nil {
		p.h.Hub.PublishUser(user.ID)
	}
}

func (p *presenter) todo(user db.User, boardID int64, text string) {
	if p.err != nil {
		return
	}
	if err := p.h.DB.AddTodo(p.ctx, boardID, user.ID, text); err == nil {
		p.h.Hub.TodoChanged(p.ctx, p.name)
	}
}

// playDemo is the session, start to finish (about twenty seconds of drawing,
// at a quick presenter's pace).
func (h *Handler) playDemo(name string, user db.User) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	b, _, err := h.Hub.Board(ctx, name)
	if err != nil {
		slog.Error("demo", "board", name, "err", err)
		return
	}
	meta, err := h.Hub.Meta(ctx, name)
	if err != nil {
		return
	}
	b.Join(demoUID, "Demo")
	h.Hub.Publish(name)
	defer func() {
		b.Leave(demoUID)
		h.Hub.Publish(name)
	}()
	p := &presenter{h: h, ctx: ctx, name: name, b: b, x: 500, y: 900}
	id := meta.ID

	p.pause(2500 * time.Millisecond) // the page lands and its stream opens
	p.notes(user, id, 2)

	// 1. the flow as it is today
	steps := []struct {
		x1, x2 int32
		label  string
	}{{60, 220, "Cart"}, {290, 450, "Shipping"}, {520, 680, "Payment"}, {750, 910, "Confirm"}}
	for _, s := range steps {
		p.box(s.x1, 200, s.x2, 320, s.label)
		p.pause(350 * time.Millisecond)
	}
	// linked arrows, port to port: they follow the boxes from now on
	for i := 0; i+1 < len(steps); i++ {
		p.stroke(3, steps[i].x2, 260, steps[i+1].x1, 260)
		p.pause(250 * time.Millisecond)
	}
	p.notes(user, id, 4)
	p.pause(600 * time.Millisecond)

	// 2. where it hurts: the laser, then the question written down
	p.laser(600, 260, 120)
	p.notes(user, id, 6)
	p.pause(500 * time.Millisecond)
	p.box(290, 700, 910, 800, "38% of mobile users leave at Payment: why?")
	p.pause(700 * time.Millisecond)

	// 3. the proposal, branching off Payment
	p.box(520, 450, 680, 560, "Apple Pay")
	p.pause(250 * time.Millisecond)
	p.stroke(3, 600, 320, 600, 450)
	p.notes(user, id, 9)
	p.pause(600 * time.Millisecond)

	// 4. what comes out of the meeting
	p.todo(user, id, "Measure drop-off at Payment by device")
	p.pause(500 * time.Millisecond)
	p.todo(user, id, "Spike Apple Pay on the Payment step")
	p.pause(500 * time.Millisecond)
	p.todo(user, id, "Review the Confirm copy with design")
	p.notes(user, id, 13)
	p.pause(900 * time.Millisecond)
	// the first is already answered in the meeting: checked, with its time
	if todos, err := h.DB.Todos(ctx, id); err == nil {
		for _, t := range todos {
			if strings.HasPrefix(t.Text, "Measure") && p.err == nil {
				if h.DB.ToggleTodo(ctx, id, t.ID, user.ID) == nil {
					h.Hub.TodoChanged(ctx, name)
				}
			}
		}
	}
	p.notes(user, id, len(demoNotes))

	// park the cursor, deselect, and leave
	p.pause(400 * time.Millisecond)
	p.stroke(0, 960, 960, 960, 960)
	p.glide(0, 980, 980, 0)
	if p.err != nil {
		slog.Error("demo", "board", name, "err", p.err)
	}
}

// ─── The controls tour ──────────────────────────────────────────────────────

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

var tourSteps = []string{
	"Draw: R for a box, A for an arrow",
	"Arrows link to a box's ports",
	"Move a box: its arrows follow",
	"Drag down to select what it touches",
	"Drag up to select only what it encloses",
	"Group (G) moves as one; ungroup (⇧G)",
	"Nudge the selection with the arrow keys",
	"Double-click where lines meet: unlink, link",
}

var tourNotes = []string{
	"Controls tour",
	"",
	"Tools: Esc select · R box · L line · A arrow · 5 laser",
	"Arrows and lines snap to a box's ports (corners,",
	"edge midpoints) and link there: move the box and",
	"they follow.",
	"",
	"Selecting: drag on empty paper.",
	"- downward: everything the rectangle touches",
	"- upward: only what it fully encloses",
	"",
	"G groups the selection (one colour, moves as one);",
	"⇧G ungroups. Arrows nudge it a grid step.",
	"",
	"Double-click a meeting point to unlink it, again to",
	"link. Double-click a shape (or click its label) to",
	"type its label in place.",
}

// playTour shows the drawing controls, one checklist item at a time.
func (h *Handler) playTour(name string, user db.User) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	b, _, err := h.Hub.Board(ctx, name)
	if err != nil {
		slog.Error("demo", "board", name, "err", err)
		return
	}
	meta, err := h.Hub.Meta(ctx, name)
	if err != nil {
		return
	}
	b.Join(demoUID, "Demo")
	h.Hub.Publish(name)
	defer func() {
		b.Leave(demoUID)
		h.Hub.Publish(name)
	}()
	p := &presenter{h: h, ctx: ctx, name: name, b: b, x: 500, y: 900}
	id := meta.ID
	notes := func(n int) {
		if p.err == nil && h.DB.SaveNote(ctx, user.ID, id, strings.Join(tourNotes[:n], "\n"), "") == nil {
			h.Hub.PublishUser(user.ID)
		}
	}

	p.pause(2500 * time.Millisecond)
	// the plan first: everything the tour will show, on the checklist
	for _, s := range tourSteps {
		p.todo(user, id, s)
		p.pause(250 * time.Millisecond)
	}
	notes(3)
	p.pause(800 * time.Millisecond)

	// 1. draw three boxes, and label them
	p.box(100, 150, 300, 300, "A")
	p.pause(250 * time.Millisecond)
	p.box(500, 150, 700, 300, "B")
	p.pause(250 * time.Millisecond)
	p.box(300, 500, 500, 650, "C")
	p.check(user, id, "Draw")
	p.pause(600 * time.Millisecond)

	// 2. arrows, port to port: A's right to B's left, B's bottom to C's right
	p.stroke(3, 300, 225, 500, 225)
	p.pause(300 * time.Millisecond)
	p.stroke(3, 600, 300, 500, 575)
	notes(6)
	p.check(user, id, "Arrows link")
	p.pause(800 * time.Millisecond)

	// 3. move B (grabbed off its label): both arrows come along
	p.stroke(0, 660, 170, 720, 350)
	p.check(user, id, "Move a box")
	p.pause(900 * time.Millisecond)

	// 4. drag down from empty paper: what the rectangle touches (C)
	p.click(900, 900) // clear the selection
	p.stroke(0, 80, 450, 330, 700)
	notes(9)
	p.check(user, id, "Drag down")
	p.pause(1200 * time.Millisecond)

	// 5. drag up: only what it encloses (A and C, not B or the arrows)
	p.click(900, 900)
	p.stroke(0, 520, 720, 80, 100)
	notes(11)
	p.check(user, id, "Drag up")
	p.pause(1200 * time.Millisecond)

	// 6. group them, move the group as one, ungroup
	p.act("group", 0, 0)
	p.pause(700 * time.Millisecond)
	p.stroke(0, 130, 170, 130, 230) // A, off its label: the group moves
	notes(14)
	p.pause(700 * time.Millisecond)
	p.act("ungroup", 0, 0)
	p.check(user, id, "Group")
	p.pause(900 * time.Millisecond)

	// 7. select B and nudge it, a step at a time
	p.click(900, 900)
	p.click(580, 360) // B, moved above (snapped): (560,340)-(760,480)
	for i := 0; i < 3; i++ {
		p.pause(350 * time.Millisecond)
		p.act("nudge", 1, 0)
	}
	p.check(user, id, "Nudge")
	p.pause(900 * time.Millisecond)

	// 8. where A's arrow meets B: unlink, then link again
	bx := int32(560 + 3*20) // B's left edge after the nudges (a grid step is 20)
	p.dblclick(bx, 410)
	p.pause(1200 * time.Millisecond)
	p.dblclick(bx, 410)
	notes(len(tourNotes))
	p.check(user, id, "Double-click")

	// park the cursor, deselect, and leave
	p.pause(600 * time.Millisecond)
	p.click(960, 960)
	p.glide(0, 980, 980, 0)
	if p.err != nil {
		slog.Error("demo", "board", name, "err", p.err)
	}
}

// ─── Go: slices, under the hood ─────────────────────────────────────────────

var sliceSteps = []string{
	"A slice is a header: pointer, length, capacity",
	"The header points into a backing array",
	"Slicing shares the array: nothing is copied",
	"append within capacity writes in place",
	"append past capacity moves to a new array",
	"copy when you need a slice of your own",
}

var sliceCode = []string{
	"// Slices, under the hood",
	"package main",
	"",
	"func main() {",
	"\tarr := [5]int{10, 20, 30, 40, 50} // the backing array",
	"\ts := arr[1:3]                     // header: ptr → arr[1], len 2, cap 4",
	"\t// s is [20 30]: it looks into arr; it owns nothing",
	"",
	"\ts = append(s, 99) // fits in cap: writes into arr[3]",
	"\t// arr is now [10 20 30 99 50]",
	"",
	"\ts = append(s, 1, 2) // past cap: a new array, copied, then appended",
	"\ts[0] = -1           // writes the new array; arr[1] is still 20",
	"",
	"\t// the old array lives on only while something points at it",
	"\tc := make([]int, len(s))",
	"\tcopy(c, s) // c has its own backing array",
	"}",
}

// playSlices teaches what a Go slice is, slowly: the code in the notes
// (highlighted as Go), the picture on the board, the laser on what each line
// touches, and the checklist as the lesson's outline.
func (h *Handler) playSlices(name string, user db.User) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	b, _, err := h.Hub.Board(ctx, name)
	if err != nil {
		slog.Error("demo", "board", name, "err", err)
		return
	}
	meta, err := h.Hub.Meta(ctx, name)
	if err != nil {
		return
	}
	b.Join(demoUID, "Demo")
	h.Hub.Publish(name)
	defer func() {
		b.Leave(demoUID)
		h.Hub.Publish(name)
	}()
	p := &presenter{h: h, ctx: ctx, name: name, b: b, x: 500, y: 950, pace: 1.8}
	id := meta.ID
	code := func(n int) {
		if p.err == nil && h.DB.SaveNote(ctx, user.ID, id, strings.Join(sliceCode[:n], "\n"), "go") == nil {
			h.Hub.PublishUser(user.ID)
		}
	}
	// the array's cells: x from 100, 140 wide, 20 apart; row at y 140..240
	cellX := func(i int) int32 { return int32(100 + i*160) }

	p.pause(2000 * time.Millisecond)
	for _, s := range sliceSteps { // the outline first
		p.todo(user, id, s)
		p.pause(300 * time.Millisecond)
	}
	code(5)
	p.pause(1000 * time.Millisecond)

	// the backing array: five cells
	for i, v := range []string{"10", "20", "30", "40", "50"} {
		p.box(cellX(i), 140, cellX(i)+140, 240, v)
		p.pause(200 * time.Millisecond)
	}
	p.pause(600 * time.Millisecond)
	p.laser(490, 190, 430) // "this whole row is arr"

	// the slice header, and its pointer into the array (at arr[1])
	code(6)
	p.box(140, 460, 440, 560, "s · ptr · len 2 · cap 4")
	p.check(user, id, "A slice is a header")
	p.pause(800 * time.Millisecond)
	p.stroke(3, 290, 460, 330, 240) // header's top to arr[1]'s bottom: linked
	p.check(user, id, "The header points")
	p.pause(800 * time.Millisecond)

	// s is [20 30]: arr[1] and arr[2], no copy
	code(7)
	p.laser(410, 190, 180)
	p.check(user, id, "Slicing shares")
	p.pause(1200 * time.Millisecond)

	// append within cap: the 99 lands in arr[3] itself
	code(10)
	p.laser(650, 190, 90)
	p.click(600, 150) // arr[3], off its label
	p.label("99")
	p.check(user, id, "append within")
	p.pause(1400 * time.Millisecond)

	// append past cap: a new array (cap 8), the header moves to it
	code(12)
	for i, v := range []string{"20", "30", "99", "1", "2"} {
		p.box(cellX(i), 700, cellX(i)+140, 800, v)
		p.pause(150 * time.Millisecond)
	}
	p.pause(500 * time.Millisecond)
	p.dblclick(330, 240)             // unlink the pointer from the old array…
	p.pause(700 * time.Millisecond)
	p.stroke(0, 330, 240, 170, 700) // …and drag it onto the new one (it links there)
	p.pause(500 * time.Millisecond)
	p.dblclick(290, 460)            // and its other end: off the header's top…
	p.pause(500 * time.Millisecond)
	p.stroke(0, 290, 460, 290, 560) // …onto its bottom, out of the label's way
	p.pause(500 * time.Millisecond)
	p.click(160, 545) // the header, off its label
	p.label("s · ptr · len 5 · cap 8")
	p.pause(600 * time.Millisecond)
	code(13)
	p.click(120, 715) // s[0] = -1 writes the NEW array
	p.label("-1")
	p.laser(330, 190, 90) // the old arr[1]: still 20
	p.check(user, id, "append past")
	p.pause(1400 * time.Millisecond)

	// the old array is on its own now: group it and set it aside
	code(15)
	p.click(960, 960)
	p.stroke(0, 900, 260, 80, 120) // drag up: only the old row, enclosed
	p.act("group", 0, 0)
	p.pause(700 * time.Millisecond)
	p.stroke(0, 110, 150, 110, 90) // the group moves as one
	p.pause(700 * time.Millisecond)
	p.laser(490, 130, 430)
	p.pause(800 * time.Millisecond)

	// independence: copy
	code(len(sliceCode))
	p.check(user, id, "copy when")

	p.pause(800 * time.Millisecond)
	p.click(960, 960)
	p.glide(0, 980, 980, 0)
	if p.err != nil {
		slog.Error("demo", "board", name, "err", p.err)
	}
}
