package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"whiteboard/ack"
	"whiteboard/board"
	"whiteboard/hub"

	"github.com/starfederation/datastar-go/datastar"
)

// AI draw: ask, and Claude sketches it onto the board, as a presenter of its
// own ("Claude"), around what is already there. It writes steps in the
// lesson's vocabulary (no questions, no checklist, no notes), checked like a
// lesson's, played like one.
//
//	POST /b/{name}/sketch        payload {text: the request}
//	POST /b/{name}/sketch/stop   Claude stops (thinking or drawing)

const sketchUID = 0xC1A0 // Claude's viewer id on the board

func (h *Handler) sketch(w http.ResponseWriter, r *http.Request) {
	n, u, meta, ok := h.open(w, r, false)
	if !ok {
		return
	}
	if h.student(meta, u) {
		ack.Bad(datastar.NewSSE(w, r), "sketch", "Only the teacher draws on this whiteboard")
		return
	}
	var p payload
	_ = datastar.ReadSignals(r, &p)
	if h.moved(w, r, p.Inst) {
		return
	}
	ask := strings.TrimSpace(p.Text)
	if len(ask) > 800 {
		ask = ask[:800]
	}
	if ask == "" {
		ack.Bad(datastar.NewSSE(w, r), "sketch", "What should Claude draw?")
		return
	}
	key, ws, err := h.DB.AnthropicKey(r.Context(), u.ID)
	if err != nil || key == "" {
		ack.Bad(datastar.NewSSE(w, r), "sketch", "Keep your Anthropic key first (Learn, Write a lesson)")
		return
	}
	if h.running(n) {
		ack.Bad(datastar.NewSSE(w, r), "sketch", "A lesson is playing here")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	if _, busy := h.sketching.LoadOrStore(n, cancel); busy {
		cancel()
		ack.Bad(datastar.NewSSE(w, r), "sketch", "Claude is drawing already")
		return
	}
	sketchVersion.Add(1)
	go func() {
		defer func() {
			cancel()
			h.sketching.Delete(n)
			sketchVersion.Add(1)
			h.Hub.Publish(n) // the Stop button goes
		}()
		h.sketchOn(ctx, n, apiKey{key, ws}, ask, p.Free)
	}()
	ack.Saved(datastar.NewSSE(w, r), "sketch", "Claude is on it…")
}

// sketchStop: Claude stops, wherever it is.
func (h *Handler) sketchStop(w http.ResponseWriter, r *http.Request) {
	n, _, _, ok := h.open(w, r, false)
	if !ok {
		return
	}
	if v, ok := h.sketching.Load(n); ok {
		v.(context.CancelFunc)()
	}
	datastar.NewSSE(w, r)
}

// sketchVersion counts Claude starting and stopping anywhere: a stream
// re-sends the Stop button when it moves.
var sketchVersion atomic.Int64

// sketching: is Claude drawing on the board?
func (h *Handler) sketchingOn(name string) bool {
	_, ok := h.sketching.Load(name)
	return ok
}

// sketchOn asks for the steps and plays them as "Claude", until ctx ends.
func (h *Handler) sketchOn(ctx context.Context, name string, key apiKey, ask string, free bool) {
	b, _, err := h.Hub.Board(ctx, name)
	if err != nil {
		return
	}
	b.Join(sketchUID, "Claude")
	h.Hub.Publish(name)
	defer func() {
		b.Leave(sketchUID)
		h.Hub.Settle(name, nil)
		h.Hub.Publish(name)
		if ctx.Err() == context.Canceled {
			slog.Info("sketch stopped", "board", name)
		}
	}()
	h.Hub.Settle(name, &hub.Ask{Say: "Claude is thinking about: " + short(ask, 120)})
	steps, err := sketchSteps(ctx, key, b.Snapshot(), b.Height, ask, free)
	p := &presenter{h: h, ctx: ctx, name: name, b: b, uid: sketchUID, x: 980, y: 980, pace: 1}
	if ctx.Err() != nil {
		return // stopped while thinking
	}
	if err != nil {
		slog.Error("sketch", "board", name, "err", err)
		p.say("Couldn't sketch that: " + err.Error())
		return
	}
	h.Hub.Settle(name, nil)
	pl := &player{h: h, p: p, l: &Lesson{Title: "sketch"}, notesID: -1, named: map[string]int32{}}
	pl.run(steps)
	p.pause(500 * time.Millisecond)
	p.click(980, 980)
}

// sketchSteps asks Claude for the drawing, given what the board holds.
func sketchSteps(ctx context.Context, key apiKey, snap board.Snapshot, height int32, ask string, free bool) ([]Step, error) {
	msgs := []message{{Role: "user", Content: "The board now:\n" + describeBoard(snap, height) + "\nThe request:\n" + ask}}
	for try := 0; try < 2; try++ {
		text, err := claude(ctx, key, sketchSystem(free), msgs)
		if err != nil {
			return nil, err
		}
		var out struct{ Steps []Step }
		err = json.Unmarshal([]byte(jsonOf(text)), &out)
		if err == nil {
			err = checkSketch(out.Steps)
		}
		if err == nil {
			return out.Steps, nil
		}
		msgs = append(msgs, message{Role: "assistant", Content: text},
			message{Role: "user", Content: "That does not pass the checker: " + err.Error() + "\nSend the whole corrected JSON, and nothing else."})
	}
	return nil, errors.New("the drawing did not come out right")
}

// checkSketch: a sketch draws and speaks; it does not ask, nor touch the
// checklist or the notes, nor wipe the board.
func checkSketch(steps []Step) error {
	if len(steps) == 0 || len(steps) > 120 {
		return errors.New("1 to 120 steps")
	}
	for i, s := range steps {
		switch s.Do {
		case "ask", "todo", "check", "notes", "dblclick":
			return fmt.Errorf("step %d (%s): not in a sketch", i+1, s.Do)
		case "erase":
			if len(s.At) == 0 {
				return fmt.Errorf("step %d: a sketch erases shapes, never the whole board", i+1)
			}
		}
	}
	l := Lesson{Title: "sketch", Steps: steps}
	return l.Validate()
}

// describeBoard is the board for the model: its size, every shape by id
// (what, where, what it says, its ink and turn, what a connector joins), and
// the empty bands where new work fits.
func describeBoard(snap board.Snapshot, height int32) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "The page is 1000 wide × %d tall (x right, y down).\n", height)
	if len(snap.Shapes) == 0 {
		sb.WriteString("It is empty.\n")
		return sb.String()
	}
	jointBox := map[int32]int32{}
	for _, j := range snap.Joints {
		jointBox[j.Idx] = j.Box
	}
	kinds := map[board.Kind]string{board.Box: "box", board.Line: "line", board.Arrow: "arrow", board.Pen: "pen stroke"}
	sb.WriteString("Shapes (id, kind, extent [x1, y1, x2, y2], then what it holds):\n")
	type band struct{ y1, y2 int32 }
	var used []band
	for _, s := range snap.Shapes {
		x1, y1, x2, y2 := min(s.X1, s.X2), min(s.Y1, s.Y2), max(s.X1, s.X2), max(s.Y1, s.Y2)
		used = append(used, band{y1, y2})
		fmt.Fprintf(&sb, "- #%d %s [%d, %d, %d, %d]", s.Idx, kinds[s.Kind], x1, y1, x2, y2)
		switch {
		case board.IsArt(s.Label):
			sb.WriteString(" a free drawing (svg)")
		case strings.HasPrefix(s.Label, "```math"):
			_, tex := board.CodeOf(s.Label)
			fmt.Fprintf(&sb, " math %q", short(tex, 120))
		case board.IsCode(s.Label):
			lang, code := board.CodeOf(s.Label)
			fmt.Fprintf(&sb, " code (%s) %q", lang, short(code, 160))
		case s.Label != "":
			fmt.Fprintf(&sb, " %q", s.Label)
		}
		if s.Kind == board.Line || s.Kind == board.Arrow {
			for end, j := range s.Joint {
				if b, ok := jointBox[j]; ok && j >= 0 && b >= 0 {
					fmt.Fprintf(&sb, " %s #%d", [...]string{"from", "to"}[end], b)
				}
			}
		}
		if s.Group >= 0 {
			fmt.Fprintf(&sb, " group %d", s.Group)
		}
		if s.Ink.Sem > 0 {
			fmt.Fprintf(&sb, " sem %s", board.Semantics[s.Ink.Sem])
		}
		if s.Ink.Hue > 0 {
			fmt.Fprintf(&sb, " hue %d", s.Ink.Hue-1)
		}
		if s.Turn.Rot%360 != 0 {
			fmt.Fprintf(&sb, " turned %d°", ((s.Turn.Rot%360)+360)%360)
		}
		sb.WriteByte('\n')
	}
	// the empty bands, top to bottom (at least 100 tall)
	sb.WriteString("Empty bands (y ranges with nothing in them, the whole width free):")
	free := 0
	for y := int32(40); y < height-40; {
		blocked := false
		for _, u := range used {
			if y >= u.y1-10 && y <= u.y2+10 {
				blocked, y = true, u.y2+11
				break
			}
		}
		if blocked {
			continue
		}
		end := height - 40
		for _, u := range used {
			if u.y1-10 > y && u.y1-10 < end {
				end = u.y1 - 10
			}
		}
		if end-y >= 100 {
			fmt.Fprintf(&sb, " %d–%d", y, end)
			free++
		}
		y = end + 1
	}
	if free == 0 {
		sb.WriteString(" none (fit new work beside what is there, or erase)")
	}
	sb.WriteByte('\n')
	return sb.String()
}

// sketchSystem is the manual Claude draws by: the page, what each step
// does, how the board behaves; free, it may also draw any SVG it likes.
func sketchSystem(free bool) string {
	mode := `Draw with the board's own shapes (boxes, arrows, code and math blocks, the pen) wherever they say it;
use an "svg" step only for a figure the shapes cannot make (a curve, a molecule, a chart).

` + artSpec
	if free {
		mode = `FREE DRAW is on: you may draw anything as SVG ("svg" steps), animated if motion helps:
diagrams, molecules, waves, orbits, charts, icons, a figure with its own play button. Still use
boxes and code blocks for words to be read, and "say" to narrate.

` + artSpec
	}
	return strings.TrimSpace(`
You draw on "draw", a shared whiteboard shaped like a letter page, as the presenter "Claude": the
viewers watch your cursor draw. You get the board (every shape by id, where it is, what it says,
what each connector joins, and the empty bands) and a request. Do it: in the empty space, or
working with what is there: point at it, connect to it, move it, ink it, turn it, as asked.

` + mode + `

Reply with ONE JSON object and nothing else: {"steps": [STEP, …]}

THE BOARD. Shapes snap to a 20-unit grid. A box's label is centred, 26 units high, ~14 units a
character, and wraps inside the box. A box whose label starts with a fence is a block: code
(15 units a line, monospace, highlighted) or math (LaTeX, typeset). Lines and arrows link to the
8 ports of a box (corners and side midpoints) and follow it when it moves. Selecting a shape
selects its whole group. A shape you have just drawn is the one "style" and "turn" act on if
nothing is selected.

` + shapesSpec + `
` + stepsSpec + `

Draw like a teacher: say what you are showing, build it up in order, point at what matters, keep
it clear and uncrowded (8–40 steps). Never wipe the board ("erase" with "at" removes one shape).`)
}
