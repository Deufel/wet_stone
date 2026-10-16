package handler

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"log/slog"
	"path"
	"strings"
	"time"

	"whiteboard/board"
	"whiteboard/db"
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
//	    {"do": "laser", "at": [cx, cy], "r": 120},
//	    {"do": "click", "at": [x, y]},  {"do": "dblclick", "at": [x, y]},
//	    {"do": "drag", "from": [x, y], "to": [x, y]},     // select tool
//	    {"do": "label", "text": "…"},                      // the selected shape
//	    {"do": "group"}, {"do": "ungroup"}, {"do": "delete"},
//	    {"do": "nudge", "by": [1, 0]},
//	    {"do": "todo", "text": "…"}, {"do": "check", "item": "prefix"},
//	    {"do": "notes", "n": 5},                           // show the first n lines
//	    {"do": "pause", "ms": 800}
//	  ]
//	}
type Lesson struct {
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
	Points [][2]int32 `json:"points,omitempty"`
	By     []int8     `json:"by,omitempty"`
	Label  string     `json:"label,omitempty"`
	Text   string     `json:"text,omitempty"`
	Item   string     `json:"item,omitempty"`
	N      int        `json:"n,omitempty"`
	Ms     int        `json:"ms,omitempty"`
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
	for i, s := range l.Steps {
		bad := func(why string) error { return fmt.Errorf("step %d (%s): %s", i+1, s.Do, why) }
		switch s.Do {
		case "box":
			if !in(s.At, 4) {
				return bad("at: [x1, y1, x2, y2] on the board")
			}
		case "arrow", "line", "drag":
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
		case "laser":
			if !in(s.At, 2) || s.R <= 0 {
				return bad("at: [cx, cy] and r")
			}
		case "click", "dblclick":
			if !in(s.At, 2) {
				return bad("at: [x, y] on the board")
			}
		case "label":
			if len(s.Text) > board.LabelMax {
				return bad(fmt.Sprintf("text: at most %d bytes", board.LabelMax))
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
// the viewer's own notebook, the checklist onto the board's.
func (h *Handler) play(l *Lesson, name string, user db.User) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
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
	h.Hub.Publish(name)
	defer func() {
		b.Leave(demoUID)
		h.Hub.Publish(name)
	}()
	p := &presenter{h: h, ctx: ctx, name: name, b: b, x: 500, y: 950, pace: l.Pace}
	id := meta.ID
	notes := func(n int) {
		if p.err != nil || l.Notes == nil {
			return
		}
		if h.DB.SaveNote(ctx, user.ID, id, strings.Join(l.Notes.Lines[:n], "\n"), l.Notes.Lang) == nil {
			h.Hub.PublishUser(user.ID)
		}
	}

	p.pause(2000 * time.Millisecond) // the page lands and its stream opens
	for _, item := range l.Checklist {
		p.todo(user, id, item)
		p.pause(300 * time.Millisecond)
	}
	for _, s := range l.Steps {
		if p.err != nil {
			break
		}
		switch s.Do {
		case "box":
			p.box(s.At[0], s.At[1], s.At[2], s.At[3], s.Label)
		case "arrow":
			p.stroke(3, s.From[0], s.From[1], s.To[0], s.To[1])
		case "line":
			p.stroke(2, s.From[0], s.From[1], s.To[0], s.To[1])
		case "pen":
			p.pen(s.Points)
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
			p.todo(user, id, s.Text)
		case "check":
			p.check(user, id, s.Item)
		case "notes":
			notes(s.N)
		case "pause":
			p.pause(time.Duration(s.Ms) * time.Millisecond)
		}
		p.pause(250 * time.Millisecond) // a beat between steps
	}
	p.pause(600 * time.Millisecond)
	p.click(980, 980) // deselect, park the cursor, and leave
	if p.err != nil {
		slog.Error("lesson", "board", name, "err", p.err)
	}
}

// pen draws a stroke through the points, at a hand's pace.
func (p *presenter) pen(points [][2]int32) {
	p.glide(0, points[0][0], points[0][1], 0)
	p.seq++
	down := p.seq
	p.cmd(board.Cmd{Op: "down", Tool: 5, Seq: down, DSeq: down, X: points[0][0], Y: points[0][1]})
	for _, pt := range points[1:] {
		p.seq++
		p.x, p.y = pt[0], pt[1]
		p.cmd(board.Cmd{Op: "move", Tool: 5, Seq: p.seq, DSeq: down, X: pt[0], Y: pt[1]})
		p.pause(24 * time.Millisecond)
	}
	p.seq++
	p.cmd(board.Cmd{Op: "up", Tool: 5, Seq: p.seq, DSeq: down, X: p.x, Y: p.y})
}
