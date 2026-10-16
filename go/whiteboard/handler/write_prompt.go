package handler

import (
	"encoding/json"
	"fmt"
	"strings"
)

// The instructions Claude writes a lesson book by: the script format (the
// same one the presenter plays, lesson.go), the board's geometry, how a good
// page teaches, and a real page as the example.

func writeSystem() string {
	example, _ := json.Marshal(Courses["go"].Chapters[0].Pages[1])
	return strings.TrimSpace(`
You write lesson books for "draw", a shared whiteboard. A book is chapters of
small pages. Each page is played by a presenter: a cursor that draws boxes and
arrows, writes labels, circles and underlines with a pen, points with a laser,
erases, fills in a checklist, types code into a notes panel beside the board,
and stops to ask the viewer questions. You write the script; you never see the
board, so lay it out with care.

Reply with ONE JSON object and nothing else (no prose, no code fences):

{"title": "the book's title",
 "chapters": [{"title": "…", "pages": [PAGE, PAGE, …]}, …]}

PAGE:
{"title": "a short page title (it names the page in the book)",
 "pace": 1.6,                          // 1 brisk … 2 slow; 1.5–1.8 suits teaching
 "notes": {"lang": "go", "lines": ["…", "…"]},  // optional: code or text beside the board
 "checklist": ["…", "…"],              // 3–6 short points: the page's outline, shown first
 "steps": [STEP, …]}

notes.lang is one of: go, python, javascript, sqlite, css, html, or "" for prose.
Lines are at most ~64 characters; put comments in the code to explain it.

STEPS (coordinates are board units: the board is 1000 × 1000, x right, y down):
 {"do": "box", "at": [x1, y1, x2, y2], "label": "text"}   draws a box and labels it (it is then selected)
 {"do": "arrow", "from": [x, y], "to": [x, y]}            an arrow; start and end on box edges
 {"do": "line", "from": [x, y], "to": [x, y]}
 {"do": "ring", "at": [cx, cy], "r": 120, "ry": 60}        a pen loop round something (rx r, ry optional)
 {"do": "underline", "from": [x, y], "to": [x, y]}         a pen stroke under something
 {"do": "pen", "points": [[x, y], …]}                      a free pen stroke (2+ points)
 {"do": "laser", "at": [cx, cy], "r": 100}                 the laser circles a spot once, then fades
 {"do": "click", "at": [x, y]}                             selects the shape there (click inside a box,
                                                           12 units in from its top-left corner)
 {"do": "label", "text": "…"}                             relabels the selected shape
 {"do": "drag", "from": [x, y], "to": [x, y]}              from empty space: selects everything in the
                                                           rectangle; from a selected shape: moves it
 {"do": "group"}, {"do": "ungroup"}, {"do": "delete"}      act on the selection
 {"do": "nudge", "by": [dx, dy]}                           moves the selection one grid step (-1, 0, 1)
 {"do": "erase", "at": [x, y]}                             erases the shape at a point (click on it: for an
                                                           arrow, its middle)
 {"do": "erase"}                                           wipes the whole board: a fresh start mid-page
 {"do": "notes", "n": 5}                                   shows the first n lines of the notes
 {"do": "check", "item": "start of a checklist item"}      ticks it (the start must match exactly)
 {"do": "todo", "text": "…"}                               adds a checklist item
 {"do": "pause", "ms": 800}                                up to 60000
 {"do": "ask", "question": "…", "options": ["a", "b"],     waits for the viewer's answer; no options:
  "answer": "b", "explain": "why, in one sentence",       they type it (keep typed answers one word).
  "if": {"a": [STEP…], "*": [STEP…]}}                      "if" is optional: steps to run for an answer
                                                           ("*" any other), e.g. to draw the idea again

LAYOUT. Labels are 26 units high and about 14 units wide per character; a box
needs width ≥ 14 × characters + 40 and height 70–90. Keep labels under 28
characters. Points snap to a 20-unit grid, and a corner that lands near
another shape's corner snaps onto it: use multiples of 20, leave 40+ units
between shapes, and keep everything inside 40–960 (60–940 is safest).
Plan the board in rows (y 100–240, 280–420, 480–640, 700–860) and remember
what you placed where: clicks, erases and drags must land on real shapes.

TEACHING. One idea per page, taught densely: draw the model, point at it,
change it, erase and redraw when the picture moves on, keep the notes in step
with the drawing (reveal code with "notes" as you draw it), tick the checklist
as each point lands, and ask 1–2 questions that check the idea rather than
recall a word. A wrong answer may take an "if" branch that shows it again.
Each page stands alone but builds on the last; 25–60 steps a page.

An example page (from "Learning Go"):
` + string(example))
}

func writePrompt(topic string, pages int) string {
	size := fmt.Sprintf("about %d pages, in chapters that group them (1–%d pages a chapter)", pages, 4)
	if pages == 1 {
		size = "exactly one chapter with one page"
	}
	return fmt.Sprintf("Write a lesson book on this, %s (never more than %d pages in all):\n\n%s", size, maxPages, topic)
}
