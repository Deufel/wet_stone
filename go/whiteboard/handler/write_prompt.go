package handler

import (
	"encoding/json"
	"fmt"
	"strings"
)

// The instructions Claude writes a lesson book by: the script format (the
// same one the presenter plays, lesson.go), the board's geometry, how a good
// page teaches, and a real page as the example.

func writeSystem(free bool) string {
	art := `Pictures: draw with the board's shapes and the pen; use an "svg" step only for a figure the
shapes cannot make (a curve, a molecule, a chart).`
	if free {
		art = `FREE DRAW is on, and it is how pictures are best made here: draw each figure as one "svg" step
(diagrams, memory layouts, molecules, waves, graphs, timelines), animated where motion teaches, with
buttons in it the viewer presses to run each stage (see TRIGGERS). Build a page up with several
figures as the ideas land; keep words to read in boxes and code in code blocks, and point at the
figures with laser, ring and underline as you "say" them.`
	}
	example, _ := json.Marshal(Courses["go"].Chapters[0].Pages[1])
	return strings.TrimSpace(`
You write lesson books for "draw", a shared whiteboard. A book is chapters of
small pages. Each page is played by a presenter: a cursor that draws boxes and
arrows, writes labels, puts code and math in blocks on the board, circles and
underlines with a pen, points with a laser, erases, says what it is showing (a
caption above the board), and stops to ask the viewer questions. Everything is
on the board: the viewer's checklist and notes are their own, never touched. You write the script; you never see the
board, so lay it out with care.

Reply with ONE JSON object and nothing else (no prose, no code fences):

{"title": "the book's title",
 "chapters": [{"title": "…", "pages": [PAGE, PAGE, …]}, …]}

PAGE:
{"title": "a short page title (it names the page in the book)",
 "pace": 1.6,                          // 1 brisk … 2 slow; 1.5–1.8 suits teaching
 "steps": [STEP, …]}

Code goes on the board in "code" blocks (lang one of go, python, javascript,
sqlite, css, html, odin, mojo; "math" for LaTeX): lines at most ~56 characters,
comments in the code to explain it; grow a block as the lesson goes by
selecting it and labelling it again with more lines.

` + stepsSpec + `
` + shapesSpec + `
` + art + `

` + artSpec + `
TEACHING. One idea per page, taught densely: draw the model, point at it,
change it, erase and redraw when the picture moves on, keep the code beside the
picture it explains, "say" each point as it lands, and ask 1–2 questions that check the idea rather than
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

// shapesSpec: naming what a script draws, and the steps that work with
// shapes by name (shared by the sketcher and the book writer).
const shapesSpec = `NAMES. The ids of shapes you create are not known in advance: name what you draw with "as" on its
step ({"do": "box", …, "as": "store"}) and refer to it later with "refs": ["store"]. A name is
better than a "click" at a point: it cannot miss. Use "ids" only for shapes already on the board
when you were given its list (#n).

WORKING WITH SHAPES (by "ids" [#n] or "refs" [names]):
 {"do": "select", "refs": ["api"]}                        selects shapes (then label, style, turn,
                                                           delete, group act on them)
 {"do": "move", "refs": ["api"], "delta": [0, 120]}        moves shapes by dx, dy (connectors follow)
 {"do": "connect", "refs": ["api", "db"], "label": "calls"}  an arrow from one to the other, side to side,
                                                           linked (kind "line" for no head); ink inline
 {"do": "laser", "at": [cx, cy], "r": 80}                  point at something while you "say" it
 {"do": "turn", "kind": "cw45"}                            animates a turn (cw45, ccw45, cw30, ccw30)
 {"do": "svg", "at": [x1, y1, x2, y2], "svg": "<svg viewBox='0 0 100 60'>…</svg>", "as": "fig"}
                                                           a figure of your own, fitted into the box
                                                           (see ART)
`

// artSpec is how to draw an "svg" figure that suits the board: the theme's
// ink API instead of colours, SMIL for motion, and triggers for buttons.
const artSpec = `ART ("svg" steps). Give the svg a viewBox and draw in its own units; it is fitted into its box,
with no frame. Keep one under ~4000 characters (the limit is 24000). Text inside is fine.

COLOUR: never write a colour (no hex, rgb, or names like "red"): the board is themed, light and
dark, and its ink comes from the theme. Paint with currentColor, and set the ink with the theme's
custom properties in style; they inherit, so set them on a <g>:
 --fg     the ink: -1…0 is neutral (-1 full ink, -0.6 quiet, -0.3 faint), 0…1 is coloured (0.8 vivid,
          1 strongest). Neutral for structure and text, coloured for what matters.
 --hue    the hue, 0–360, for it and everything inside (210 blue, 145 green, 25 red, 75 amber, 290 violet)
 --hue-shift  a turn from the hue around it (style="--hue-shift: 120"), --hue-lock pins a hue absolutely
 class    "suc" green, "inf" blue, "wrn" amber, "dgr" red: a meaning (pair it with --fg: 0.8)
 tints    style="fill: color-mix(in oklch, currentColor 15%, transparent)" for a soft fill
An SVG shape's fill is black unless set: give every shape fill="currentColor", fill="none", or a tint.
 <g style="--hue: 210; --fg: 0.8" fill="none" stroke="currentColor" stroke-width="2">…</g>
 <circle class="dgr" style="--fg: 0.9" fill="currentColor" r="6"/>
 <text style="--fg: -0.8" fill="currentColor" font-size="14">label</text>

MOTION (SMIL): <animate>, <animateTransform>, <animateMotion>, <set>; repeatCount="indefinite"
loops, fill="freeze" holds the end, begin="a.end" chains after the animation with id "a" (make ids
unique: prefix them with the figure's name).

TRIGGERS: a figure can carry its own buttons. Give the animations that wait begin="indefinite" and
data-anim="NAME"; give a button data-trigger="NAME" (a plain lowercase word): a click on it starts
all of them, again on every click. A button is an SVG group or HTML in a foreignObject:
 <g data-trigger="spin"><rect x="4" y="4" width="60" height="22" rx="6" style="--fg: 0.8" fill="none" stroke="currentColor"/><text x="34" y="20" text-anchor="middle" fill="currentColor" font-size="12">▶ Spin</text></g>
 <foreignObject x="4" y="4" width="80" height="30"><button data-trigger="spin" class="inf">Spin</button></foreignObject>
A foreignObject's HTML fills it, its text sized by its height (about 45% of it, at most 14): give a
button height 26–34 and width ≥ 9 × characters + 16, or its words are cut.
Use several triggers for the steps of a process ("push", "pop"), so the viewer drives it.
Allowed: drawing elements, gradients, patterns, masks, filters, SMIL, foreignObject holding only
button, div, span, p, b, i, em, strong, code, small, br, sub, sup (with class, style, title). No
script, no on* handlers, no links, no images, no url() outside the figure.
`

// stepsSpec is the step vocabulary and the board's geometry, shared by the
// book writer and the explainer.
const stepsSpec = `STEPS (coordinates are board units: the board is a letter page, 1000 wide × 1294 tall, x right, y down):
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
 {"do": "penarrow", "from": [x, y], "to": [x, y]}         an arrow drawn by hand (pen)
 {"do": "code", "at": [x1, y1, x2, y2], "lang": "go",     a code block on the board, typed in and
  "text": "s := []int{1, 2}\ns = append(s, 3)"}          highlighted (monospace, 15 units a line,
                                                           ~9 units a character): size the box to fit;
                                                           "lang": "math" typesets LaTeX ("text": "\\frac{a}{b}")
 {"do": "text", "at": [x1, y1, x2, y2], "kind": "heading", words on the paper, no frame, left-aligned, wrapping
  "text": "Why slices grow"}                              in the box: kind "title" (32 units a line, ~17 a
                                                           character), "heading" (24 units, ~13 a character)
                                                           or "text" (a paragraph: 18 units, ~9.5 a character,
                                                           lines 26 apart). Height: title ≥ 50, heading ≥ 40,
                                                           text 26 × lines + 12 (too little and it shrinks
                                                           to fit). Headings over a figure, a
                                                           sentence of explanation beside it, a summary
 {"do": "style", "hue": 210, "fg": 0.8, "sem": "inf"}       inks the selection (click a shape first)
 {"do": "turn", "kind": "cw45"}                            turns the selection (or what was just drawn)
                                                           about its centre: cw45, ccw45, cw30, ccw30
 {"do": "say", "text": "…"}                               the presenter speaks: a caption over the board,
                                                           held as long as it takes to read (≤ 240 bytes)
 {"do": "pause", "ms": 800}                                up to 60000
 {"do": "ask", "question": "…", "options": ["a", "b"],     waits for the viewer's answer; no options:
  "answer": "b", "explain": "why, in one sentence",       they type it (keep typed answers one word).
  "if": {"a": [STEP…], "*": [STEP…]}}                      "if" is optional: steps to run for an answer
                                                           ("*" any other), e.g. to draw the idea again

INK. Any drawing step (box, code, arrow, line, pen, ring, underline,
penarrow) may carry "hue" (1–360, an absolute hue), "fg" (-1–1: the ink's
strength; 0.8 vivid, -0.5 quiet) and "sem" ("inf" blue, "suc" green, "wrn"
amber, "dgr" red: a meaning, its own hue). Use it to mean something: one hue
per idea, "sem": "dgr" for a mistake, "suc" for the right answer, quiet ink for
context; most shapes need none.

LAYOUT. The page's top band (y 0–90) holds its title: start drawing at y 100. Labels are 26 units high and about 14 units wide per character; a box
needs width ≥ 14 × characters + 40 and height 70–90. Keep labels under 28
characters. Points snap to a 20-unit grid, and a corner that lands near
another shape's corner snaps onto it: use multiples of 20, leave 40+ units
between shapes, and keep x inside 40–960 (60–940 is safest) and y inside 100–1250.
Plan the page in rows (y 100–240, 280–420, 480–640, 700–860, 920–1080, 1120–1240) and remember
what you placed where: clicks, erases and drags must land on real shapes.
`

// explainSystem: "I don't get it" — the same idea, shown another way.
func explainSystem() string {
	return strings.TrimSpace(`
You are the presenter of a lesson on "draw", a shared whiteboard. The viewer
was asked a question and pressed "I don't get it". Show the idea behind the
question again, a DIFFERENT way from the lesson's: another picture, another
analogy, a smaller example worked by hand. The board has been cleared for you;
the lesson's page comes back after, and the question is asked again (do not
answer it outright: make the answer something they can now see).

Draw it like a teacher at a whiteboard, by hand: mostly the pen ("pen" with
points, "ring", "underline", "penarrow"), sketching shapes, ticks, crosses,
braces, small diagrams and sums; a few boxes for words that must be read (a
pen cannot write letters). Narrate with "say" as you go: short, plain
sentences, one thought each. Point with "laser". 12–30 steps.

Reply with ONE JSON object and nothing else: {"steps": [STEP, …]}
Allowed steps: box, text, arrow, line, pen, ring, underline, penarrow, laser, say,
pause, click, label, drag, delete, erase. (No ask.)

` + stepsSpec)
}

func explainPrompt(l *Lesson, s Step) string {
	page, _ := json.Marshal(l)
	q, _ := json.Marshal(map[string]any{"question": s.Question, "options": s.Options, "answer": s.Answer, "explain": s.Explain})
	return "The lesson page (its script):\n" + string(page) + "\n\nThe question they did not understand:\n" + string(q)
}
