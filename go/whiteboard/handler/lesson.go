package handler

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"path"
	"strings"
	"time"

	"whiteboard/board"
	"whiteboard/db"
	"whiteboard/hub"
	"whiteboard/view"
)

// A Lesson is a script the presenter plays on a board: what a demo is, and
// what an AI-written lesson will be. Coordinates are board units (0..1000).
//
//	{
//	  "title": "Go slices, under the hood",
//	  "pace": 1.8,                                  // 1 quick, 2 half speed
//	  "notes": {"lang": "go", "lines": ["…", "…"]}, // typed in as steps reveal them
//	  "checklist": ["…", "…"],                      // the outline, added first
//	  "steps": [
//	    {"do": "box", "at": [x1, y1, x2, y2], "label": "10"},
//	    {"do": "arrow", "from": [x, y], "to": [x, y]},   // also "line"
//	    {"do": "pen", "points": [[x, y], [x, y], …]},
//	    {"do": "ring", "at": [cx, cy], "r": 120, "ry": 60}, // a pen loop round something
//	    {"do": "underline", "from": [x, y], "to": [x, y]},   // a pen stroke under it
//	    {"do": "erase", "at": [x, y]},                     // the shape there; no at: the whole board
//	    {"do": "penarrow", "from": [x, y], "to": [x, y]},  // an arrow by hand
//	    {"do": "say", "text": "…"},                        // a caption over the board
//	    {"do": "code", "at": [x1, y1, x2, y2], "lang": "go", "text": "…"}, // a code block, typed in
//	    {"do": "laser", "at": [cx, cy], "r": 120},
//	    {"do": "click", "at": [x, y]},  {"do": "dblclick", "at": [x, y]},
//	    {"do": "drag", "from": [x, y], "to": [x, y]},     // select tool
//	    {"do": "label", "text": "…"},                      // the selected shape
//	    {"do": "group"}, {"do": "ungroup"}, {"do": "delete"},
//	    {"do": "nudge", "by": [1, 0]},
//	    {"do": "todo", "text": "…"}, {"do": "check", "item": "prefix"},
//	    {"do": "notes", "n": 5},                           // show the first n lines
//	    {"do": "pause", "ms": 800},
//	    {"do": "ask", "question": "…", "options": ["2", "4"], // no options: free text
//	     "answer": "4", "explain": "…",                    // both optional
//	     "if": {"4": [steps…], "*": [steps…]}}              // a branch by the answer
//	  ]
//	}
type Lesson struct {
	Use       string   `json:"use,omitempty"` // a book's page: a built-in lesson by name
	Title     string   `json:"title"`
	Pace      float64  `json:"pace"`
	Notes     *Notes   `json:"notes,omitempty"`
	Checklist []string `json:"checklist,omitempty"`
	Steps     []Step   `json:"steps"`
}

type Notes struct {
	Lang  string   `json:"lang"`
	Lines []string `json:"lines"`
}

type Step struct {
	Do     string     `json:"do"`
	At     []int32    `json:"at,omitempty"`
	From   []int32    `json:"from,omitempty"`
	To     []int32    `json:"to,omitempty"`
	R      int32      `json:"r,omitempty"`
	Ry     int32      `json:"ry,omitempty"`
	Points [][2]int32 `json:"points,omitempty"`
	By     []int8     `json:"by,omitempty"`
	Label  string     `json:"label,omitempty"`
	Text   string     `json:"text,omitempty"`
	Lang   string     `json:"lang,omitempty"` // a code block's language
	Item   string     `json:"item,omitempty"`
	N      int        `json:"n,omitempty"`
	Ms     int        `json:"ms,omitempty"`

	// ask: the lesson waits for the viewer's answer, says how it went, and
	// may take a branch (If, by the answer, "*" for any other)
	Question string            `json:"question,omitempty"`
	Options  []string          `json:"options,omitempty"`
	Answer   string            `json:"answer,omitempty"`
	Explain  string            `json:"explain,omitempty"`
	If       map[string][]Step `json:"if,omitempty"`
}

// Validate checks a lesson before it is played: every step known, with what
// it needs, on the board. An AI-written lesson is checked the same way.
func (l *Lesson) Validate() error {
	if strings.TrimSpace(l.Title) == "" {
		return fmt.Errorf("a lesson needs a title")
	}
	if len(l.Steps) == 0 {
		return fmt.Errorf("a lesson needs steps")
	}
	if l.Pace < 0 || l.Pace > 5 {
		return fmt.Errorf("pace %v: 0 to 5", l.Pace)
	}
	if l.Notes != nil && !view.NoteLang(l.Notes.Lang) {
		return fmt.Errorf("notes lang %q: one of css, html, python, javascript, go, sqlite, odin, mojo, or none", l.Notes.Lang)
	}
	if len(l.Checklist) > 12 {
		return fmt.Errorf("checklist: at most 12 items")
	}
	return l.check(l.Steps, "")
}

// check validates steps, and the steps of every branch (named by its path).
func (l *Lesson) check(steps []Step, at string) error {
	in := func(v []int32, n int) bool {
		if len(v) != n {
			return false
		}
		for _, c := range v {
			if c < 0 || c > board.W {
				return false
			}
		}
		return true
	}
	for i, s := range steps {
		bad := func(why string) error { return fmt.Errorf("step %s%d (%s): %s", at, i+1, s.Do, why) }
		switch s.Do {
		case "box":
			if !in(s.At, 4) {
				return bad("at: [x1, y1, x2, y2] on the board")
			}
		case "arrow", "line", "drag", "underline", "penarrow":
			if !in(s.From, 2) || !in(s.To, 2) {
				return bad("from and to: [x, y] on the board")
			}
		case "pen":
			if len(s.Points) < 2 {
				return bad("points: at least two [x, y]")
			}
			for _, p := range s.Points {
				if !in(p[:], 2) {
					return bad("a point off the board")
				}
			}
		case "laser", "ring":
			if !in(s.At, 2) || s.R <= 0 || s.R > board.W || s.Ry < 0 || s.Ry > board.W {
				return bad("at: [cx, cy] and r")
			}
		case "say":
			if strings.TrimSpace(s.Text) == "" || len(s.Text) > 240 {
				return bad("text: 1 to 240 bytes")
			}
		case "erase":
			if len(s.At) != 0 && !in(s.At, 2) {
				return bad("at: [x, y] on the board, or none for all of it")
			}
		case "click", "dblclick":
			if !in(s.At, 2) {
				return bad("at: [x, y] on the board")
			}
		case "label":
			if max := labelMax(s.Text); len(s.Text) > max {
				return bad(fmt.Sprintf("text: at most %d bytes", max))
			}
		case "code":
			if !in(s.At, 4) {
				return bad("at: [x1, y1, x2, y2] on the board")
			}
			if strings.TrimSpace(s.Text) == "" || len(s.Text)+len(s.Lang)+4 > board.CodeMax {
				return bad(fmt.Sprintf("text: the code, at most %d bytes", board.CodeMax-20))
			}
			if s.Lang != "" && s.Lang != "math" && !view.NoteLang(s.Lang) {
				return bad("lang: one of css, html, python, javascript, go, sqlite, odin, mojo, or math (LaTeX)")
			}
		case "group", "ungroup", "delete":
		case "nudge":
			if len(s.By) != 2 {
				return bad("by: [dx, dy], each -1, 0 or 1")
			}
		case "todo":
			if strings.TrimSpace(s.Text) == "" {
				return bad("text")
			}
		case "check":
			if s.Item == "" {
				return bad("item: the start of a checklist item")
			}
		case "notes":
			if l.Notes == nil || s.N < 0 || s.N > len(l.Notes.Lines) {
				return bad("n: lines of the lesson's notes to show")
			}
		case "pause":
			if s.Ms <= 0 || s.Ms > 60000 {
				return bad("ms: 1 to 60000")
			}
		case "ask":
			if strings.TrimSpace(s.Question) == "" {
				return bad("question")
			}
			if len(s.Options) > 6 {
				return bad("options: at most 6")
			}
			for key, branch := range s.If {
				if err := l.check(branch, fmt.Sprintf("%s%d [%s] ", at, i+1, key)); err != nil {
					return err
				}
			}
		default:
			return bad("unknown step")
		}
	}
	return nil
}

//go:embed lessons/*.json
var lessonFiles embed.FS

// Lessons are the built-in ones, by file name: lessons/go.json is "go".
var Lessons = map[string]*Lesson{}

func init() {
	entries, _ := lessonFiles.ReadDir("lessons")
	for _, e := range entries {
		b, err := lessonFiles.ReadFile("lessons/" + e.Name())
		if err != nil {
			panic(err)
		}
		var l Lesson
		if err := json.Unmarshal(b, &l); err != nil {
			panic(fmt.Sprintf("lesson %s: %v", e.Name(), err))
		}
		if err := l.Validate(); err != nil {
			panic(fmt.Sprintf("lesson %s: %v", e.Name(), err))
		}
		Lessons[strings.TrimSuffix(e.Name(), path.Ext(e.Name()))] = &l
	}
}

// play runs a lesson on a board as the presenter "Demo"; the notes go into
// the viewer's own notebook, the checklist onto the board's. end, when given,
// is the card left on the board after (a book's page: the way to the next).
// Steps before from are done at once (a seek); run is the player's control.
func (h *Handler) play(ctx context.Context, l *Lesson, name string, user db.User, end *hub.Ask, run *lessonRun, from int) {
	b, _, err := h.Hub.Board(ctx, name)
	if err != nil {
		slog.Error("lesson", "board", name, "err", err)
		return
	}
	meta, err := h.Hub.Meta(ctx, name)
	if err != nil {
		return
	}
	b.Join(demoUID, "Demo")
	h.Hub.Settle(name, nil)
	defer func() {
		b.Leave(demoUID)
		if end != nil && ctx.Err() == nil {
			h.Hub.Settle(name, end)
		} else if ctx.Err() == nil {
			h.Hub.Settle(name, nil) // a last caption goes with the presenter
		}
		h.Hub.Publish(name)
	}()
	p := &presenter{h: h, ctx: ctx, name: name, b: b, uid: demoUID, x: 500, y: 950, pace: l.Pace, run: run, fast: from > 0}
	pl := &player{h: h, p: p, l: l, user: user, id: meta.ID}

	p.pause(1200 * time.Millisecond) // the page lands and its stream opens
	for _, item := range l.Checklist {
		p.todo(user, pl.id, item)
		p.pause(300 * time.Millisecond)
	}
	for i, s := range l.Steps {
		if p.err != nil {
			break
		}
		if i == from {
			p.fast = false
			h.Hub.Publish(name)
		}
		if run != nil {
			run.set(func() { run.step = i })
			h.Hub.Publish(name)
		}
		pl.run([]Step{s})
	}
	p.pause(600 * time.Millisecond)
	p.click(980, 980) // deselect, park the cursor, and leave
	if p.err != nil && ctx.Err() == nil {
		slog.Error("lesson", "board", name, "err", p.err)
	}
}

// player is one lesson being played: its presenter, its script, its viewer.
type player struct {
	h    *Handler
	p    *presenter
	l    *Lesson
	user db.User
	id   int64 // the board
}

func (pl *player) run(steps []Step) {
	p := pl.p
	for _, s := range steps {
		if p.err != nil {
			return
		}
		switch s.Do {
		case "box":
			p.box(s.At[0], s.At[1], s.At[2], s.At[3], s.Label)
		case "code":
			p.box(s.At[0], s.At[1], s.At[2], s.At[3], "```"+s.Lang+"\n"+s.Text)
		case "arrow":
			p.stroke(3, s.From[0], s.From[1], s.To[0], s.To[1])
		case "line":
			p.stroke(2, s.From[0], s.From[1], s.To[0], s.To[1])
		case "pen":
			p.pen(s.Points)
		case "ring":
			p.ring(s.At[0], s.At[1], s.R, s.Ry)
		case "underline":
			p.underline(s.From[0], s.From[1], s.To[0], s.To[1])
		case "penarrow":
			p.penarrow(s.From[0], s.From[1], s.To[0], s.To[1])
		case "erase":
			p.erase(s.At)
		case "say":
			p.say(s.Text)
		case "laser":
			p.laser(s.At[0], s.At[1], s.R)
		case "click":
			p.click(s.At[0], s.At[1])
		case "dblclick":
			p.dblclick(s.At[0], s.At[1])
		case "drag":
			p.stroke(0, s.From[0], s.From[1], s.To[0], s.To[1])
		case "label":
			p.label(s.Text)
		case "group", "ungroup", "delete":
			p.act(s.Do, 0, 0)
		case "nudge":
			p.act("nudge", s.By[0], s.By[1])
		case "todo":
			p.todo(pl.user, pl.id, s.Text)
		case "check":
			p.check(pl.user, pl.id, s.Item)
		case "notes":
			pl.notes(s.N)
		case "pause":
			p.pause(time.Duration(s.Ms) * time.Millisecond)
		case "ask":
			pl.run(pl.ask(s))
		}
		p.pause(250 * time.Millisecond) // a beat between steps
	}
}

// notes shows the first n lines of the lesson's notes in the viewer's notebook.
func (pl *player) notes(n int) {
	l, p := pl.l, pl.p
	if p.err != nil || l.Notes == nil {
		return
	}
	if pl.h.DB.SaveNote(p.ctx, pl.user.ID, pl.id, strings.Join(l.Notes.Lines[:n], "\n"), l.Notes.Lang) == nil {
		pl.h.Hub.PublishUser(pl.user.ID)
	}
}

// helpsPerQuestion: how many times "I don't get it" is offered on one question.
const helpsPerQuestion = 2

// ask puts the step's question on the board, waits for the viewer's answer,
// shows how it went, and returns the branch the answer takes (or none). "I
// don't get it" (when the viewer has a key to ask with) sets the page aside,
// has the idea shown another way, puts the page back, and asks again.
func (pl *player) ask(s Step) []Step {
	h, p, user, name := pl.h, pl.p, pl.user, pl.p.name
	if p.err != nil || p.fast {
		return nil // seeking past a question: it is not asked
	}
	ctx, cancel := context.WithTimeout(p.ctx, 10*time.Minute)
	defer cancel()
	var answer string
	for helps := 0; ; helps++ {
		key, ws, _ := h.DB.AnthropicKey(ctx, user.ID)
		q := hub.Ask{Question: s.Question, Options: s.Options, By: user.First(), ByID: user.ID, Help: key != "" && helps < helpsPerQuestion}
		a, err := h.Hub.Ask(ctx, name, q)
		if err != nil {
			p.err = err // nobody answered: the lesson stops here
			return nil
		}
		if a != hub.HelpAnswer {
			answer = a
			break
		}
		pl.explainAgain(s, apiKey{key, ws})
		if p.err != nil {
			return nil
		}
	}
	q := hub.Ask{Question: s.Question, Options: s.Options, By: user.First(), ByID: user.ID, Answered: answer}
	q.Correct = s.Answer == "" || same(answer, s.Answer)
	switch {
	case s.Answer == "":
		q.Result = s.Explain
	case q.Correct:
		q.Result = strings.TrimSpace("Right. " + s.Explain)
	default:
		q.Result = strings.TrimSpace("Not quite: it's " + s.Answer + ". " + s.Explain)
	}
	h.Hub.Settle(name, &q)
	p.pause(time.Duration(2500+40*len(q.Result)) * time.Millisecond) // time to read it
	h.Hub.Settle(name, nil)
	for key, branch := range s.If {
		if key != "*" && same(answer, key) {
			return branch
		}
	}
	return s.If["*"]
}

// explainAgain: Claude writes another way to show the idea behind the
// question (by hand, mostly: the pen), played on a cleared board; then the
// page comes back as it was.
func (pl *player) explainAgain(s Step, key apiKey) {
	h, p := pl.h, pl.p
	h.Hub.Settle(p.name, &hub.Ask{Say: "Let me show that another way…"})
	ctx, cancel := context.WithTimeout(p.ctx, 3*time.Minute)
	steps, err := explainSteps(ctx, key, pl.l, s)
	cancel()
	if err != nil {
		slog.Error("explain again", "board", p.name, "err", err)
		p.say("I couldn't draw it another way just now (" + err.Error() + ").")
		return
	}
	page := p.b.Snapshot()
	p.erase(nil)
	p.pause(400 * time.Millisecond)
	pl.run(steps)
	p.pause(1500 * time.Millisecond)
	if p.err != nil {
		return
	}
	p.say("Back to the page.")
	if err := h.Hub.Redraw(p.ctx, p.name, page); err != nil {
		p.err = err
	}
	p.pause(800 * time.Millisecond)
}

// say puts a caption over the board, for as long as it takes to read.
func (p *presenter) say(text string) {
	if p.err != nil || p.fast {
		return
	}
	p.h.Hub.Settle(p.name, &hub.Ask{Say: text})
	p.pause(time.Duration(1500+70*len(text)) * time.Millisecond) // read, or spoken

}

// penarrow draws an arrow by hand: the shaft, then the head.
func (p *presenter) penarrow(x1, y1, x2, y2 int32) {
	p.underline(x1, y1, x2, y2)
	dx, dy := float64(x2-x1), float64(y2-y1)
	n := math.Hypot(dx, dy)
	if n == 0 {
		return
	}
	ux, uy := dx/n, dy/n
	const head = 28
	wing := func(side float64) [2]int32 {
		return onBoard(x2-int32(head*(ux*0.87-side*uy*0.5)), y2-int32(head*(uy*0.87+side*ux*0.5)))
	}
	p.pen([][2]int32{wing(1), {x2, y2}, wing(-1)})
}

// labelMax: a code block's label may be long; a plain one is a few words.
func labelMax(text string) int {
	if board.IsCode(text) {
		return board.CodeMax
	}
	return board.LabelMax
}

// same compares answers loosely: case and surrounding space aside.
func same(a, b string) bool { return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b)) }

// pen draws a stroke through the points, at a hand's pace. The points a
// script gives are few and far apart; the pen is sampled densely along a
// smooth curve through them (Catmull-Rom), so the board's own smoothing has
// a real line to follow instead of rounding off corners between sparse ones.
func (p *presenter) pen(points [][2]int32) {
	points = densify(points, penStep)
	p.glide(0, points[0][0], points[0][1], 0)
	p.seq++
	down := p.seq
	p.cmd(board.Cmd{Op: "down", Tool: 5, Seq: down, DSeq: down, X: points[0][0], Y: points[0][1]})
	for _, pt := range points[1:] {
		p.seq++
		p.x, p.y = pt[0], pt[1]
		p.cmd(board.Cmd{Op: "move", Tool: 5, Seq: p.seq, DSeq: down, X: pt[0], Y: pt[1]})
		p.pause(penBeat)
	}
	p.seq++
	p.cmd(board.Cmd{Op: "up", Tool: 5, Seq: p.seq, DSeq: down, X: p.x, Y: p.y})
}

// The presenter's pen: a sample every penStep board units, one every penBeat
// (about 330 units a second at pace 1: a steady hand).
const (
	penStep = 6
	penBeat = 18 * time.Millisecond
)

// densify samples a Catmull-Rom curve through pts every ~step units; the
// ends and every given point stay on it.
func densify(pts [][2]int32, step float64) [][2]int32 {
	if len(pts) < 2 {
		return pts
	}
	at := func(i int) (float64, float64) {
		i = min(max(i, 0), len(pts)-1)
		return float64(pts[i][0]), float64(pts[i][1])
	}
	out := [][2]int32{pts[0]}
	for i := 0; i+1 < len(pts); i++ {
		x0, y0 := at(i - 1)
		x1, y1 := at(i)
		x2, y2 := at(i + 1)
		x3, y3 := at(i + 2)
		n := max(1, int(math.Ceil(math.Hypot(x2-x1, y2-y1)/step)))
		for k := 1; k <= n; k++ {
			t := float64(k) / float64(n)
			t2, t3 := t*t, t*t*t
			cr := func(a, b, c, d float64) float64 {
				return 0.5 * (2*b + (-a+c)*t + (2*a-5*b+4*c-d)*t2 + (-a+3*b-3*c+d)*t3)
			}
			pt := onBoard(int32(math.Round(cr(x0, x1, x2, x3))), int32(math.Round(cr(y0, y1, y2, y3))))
			if pt != out[len(out)-1] {
				out = append(out, pt)
			}
		}
	}
	return out
}

// ring loops the pen round (cx, cy): rx across, ry down (ry 0: r*0.6), a
// little past the start, as a hand closes a circle.
func (p *presenter) ring(cx, cy, rx, ry int32) {
	if ry == 0 {
		ry = rx * 6 / 10
	}
	const n = 36
	pts := make([][2]int32, 0, n+4)
	for i := 0; i <= n+3; i++ {
		a := -math.Pi/2 + float64(i)/n*2*math.Pi
		pts = append(pts, onBoard(cx+int32(float64(rx)*math.Cos(a)), cy+int32(float64(ry)*math.Sin(a))))
	}
	p.pen(pts)
}

// underline draws a hand's line from one point to another, gently wavering.
func (p *presenter) underline(x1, y1, x2, y2 int32) {
	const n = 12
	pts := make([][2]int32, 0, n+1)
	for i := 0; i <= n; i++ {
		t := float64(i) / n
		wob := 3 * math.Sin(t*math.Pi*3)
		pts = append(pts, onBoard(x1+int32(float64(x2-x1)*t), y1+int32(float64(y2-y1)*t+wob)))
	}
	p.pen(pts)
}

// erase takes away the shape at a point (a click, then delete), or with no
// point wipes the whole board, as the clear button does.
func (p *presenter) erase(at []int32) {
	if len(at) == 2 {
		p.click(at[0], at[1])
		p.pause(300 * time.Millisecond)
		if p.b.SelectedBy(p.uid) >= 0 {
			p.act("delete", 0, 0)
		}
		return
	}
	p.glide(0, 40, 40, 0)
	p.pause(300 * time.Millisecond)
	p.cmd(board.Cmd{Op: "clear"})
}

func onBoard(x, y int32) [2]int32 {
	return [2]int32{min(max(x, 0), board.W), min(max(y, 0), board.W)}
}

// ─── Lesson books ───────────────────────────────────────────────────────────

// A Course is a lesson book: chapters of pages, each page a lesson played on
// its own board when it is first opened. Small pages, many of them: a page
// teaches one idea, densely (draw, erase, draw again; notes; a question).
//
//	{"title": "Learning Go", "chapters": [
//	  {"title": "Slices", "pages": [{"use": "go", "title": "The header"}, {lesson…}]}]}
//
// "use" takes a built-in lesson (lessons/{use}.json), its title replaced by
// the page's when one is given.
type Course struct {
	Title    string          `json:"title"`
	Chapters []CourseChapter `json:"chapters"`
}

type CourseChapter struct {
	Title string   `json:"title"`
	Pages []Lesson `json:"pages"`
}

// The most a book holds: enough for a short course, little enough to read.
const maxChapters, maxPages = 8, 24

// resolve swaps each "use" page for its built-in lesson, then checks it all.
func (c *Course) resolve() error {
	if strings.TrimSpace(c.Title) == "" {
		return fmt.Errorf("a book needs a title")
	}
	if len(c.Chapters) == 0 || len(c.Chapters) > maxChapters {
		return fmt.Errorf("1 to %d chapters", maxChapters)
	}
	n := 0
	for i := range c.Chapters {
		ch := &c.Chapters[i]
		if strings.TrimSpace(ch.Title) == "" {
			return fmt.Errorf("chapter %d needs a title", i+1)
		}
		if len(ch.Pages) == 0 {
			return fmt.Errorf("chapter %d needs a page", i+1)
		}
		for j := range ch.Pages {
			pg := &ch.Pages[j]
			if pg.Use != "" {
				base, ok := Lessons[pg.Use]
				if !ok {
					return fmt.Errorf("chapter %d page %d: no lesson %q", i+1, j+1, pg.Use)
				}
				title := pg.Title
				*pg = *base
				if title != "" {
					pg.Title = title
				}
			}
			if err := pg.Validate(); err != nil {
				return fmt.Errorf("chapter %d page %d: %w", i+1, j+1, err)
			}
			n++
		}
	}
	if n > maxPages {
		return fmt.Errorf("at most %d pages", maxPages)
	}
	return nil
}

//go:embed courses/*.json
var courseFiles embed.FS

// Courses are the built-in lesson books, by file name.
var Courses = map[string]*Course{}

func init() {
	entries, _ := courseFiles.ReadDir("courses")
	for _, e := range entries {
		b, err := courseFiles.ReadFile("courses/" + e.Name())
		if err != nil {
			panic(err)
		}
		var c Course
		if err := json.Unmarshal(b, &c); err != nil {
			panic(fmt.Sprintf("course %s: %v", e.Name(), err))
		}
		if err := c.resolve(); err != nil {
			panic(fmt.Sprintf("course %s: %v", e.Name(), err))
		}
		Courses[strings.TrimSuffix(e.Name(), path.Ext(e.Name()))] = &c
	}
}
