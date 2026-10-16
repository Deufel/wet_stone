package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
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
//	POST /b/{name}/sketch   payload {text: the request}

const sketchUID = 0xC1A0 // Claude's viewer id on the board

func (h *Handler) sketch(w http.ResponseWriter, r *http.Request) {
	n, u, _, ok := h.open(w, r, false)
	if !ok {
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
	if _, busy := h.sketching.LoadOrStore(n, true); busy {
		ack.Bad(datastar.NewSSE(w, r), "sketch", "Claude is drawing already")
		return
	}
	go func() {
		defer h.sketching.Delete(n)
		h.sketchOn(n, apiKey{key, ws}, ask)
	}()
	ack.Saved(datastar.NewSSE(w, r), "sketch", "Claude is on it…")
}

// sketchOn asks for the steps and plays them as "Claude".
func (h *Handler) sketchOn(name string, key apiKey, ask string) {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
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
	}()
	h.Hub.Settle(name, &hub.Ask{Say: "Claude is thinking about: " + short(ask, 120)})
	steps, err := sketchSteps(ctx, key, b.Snapshot(), ask)
	p := &presenter{h: h, ctx: ctx, name: name, b: b, uid: sketchUID, x: 980, y: 980, pace: 1}
	if err != nil {
		slog.Error("sketch", "board", name, "err", err)
		p.say("Couldn't sketch that: " + err.Error())
		return
	}
	h.Hub.Settle(name, nil)
	pl := &player{h: h, p: p, l: &Lesson{Title: "sketch"}}
	pl.run(steps)
	p.pause(500 * time.Millisecond)
	p.click(980, 980)
}

// sketchSteps asks Claude for the drawing, given what the board holds.
func sketchSteps(ctx context.Context, key apiKey, snap board.Snapshot, ask string) ([]Step, error) {
	msgs := []message{{Role: "user", Content: "The board now:\n" + describeBoard(snap) + "\n\nThe request:\n" + ask}}
	for try := 0; try < 2; try++ {
		text, err := claude(ctx, key, sketchSystem(), msgs)
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
	if len(steps) == 0 || len(steps) > 80 {
		return errors.New("1 to 80 steps")
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

// describeBoard lists the board's shapes for the model: what and where.
func describeBoard(snap board.Snapshot) string {
	if len(snap.Shapes) == 0 {
		return "(empty)"
	}
	kinds := map[board.Kind]string{board.Box: "box", board.Line: "line", board.Arrow: "arrow", board.Pen: "pen stroke"}
	var sb strings.Builder
	for _, s := range snap.Shapes {
		fmt.Fprintf(&sb, "- %s [%d, %d, %d, %d]", kinds[s.Kind], s.X1, s.Y1, s.X2, s.Y2)
		if s.Label != "" {
			fmt.Fprintf(&sb, " %q", short(s.Label, 200))
		}
		sb.WriteByte('\n')
	}
	return sb.String()
}

func sketchSystem() string {
	return strings.TrimSpace(`
You draw on "draw", a shared whiteboard, as the presenter "Claude": the
viewers watch your cursor draw. You get what the board holds now (shapes with
[x1, y1, x2, y2]; for a line or arrow, its two ends) and a request. Draw it:
in the empty space, or adding to and pointing at what is there, as asked.

Draw like a person at a whiteboard: boxes for words, code blocks for code,
arrows to connect, the pen for sketches, emphasis and quick diagrams; a
"say" now and then to tell them what you are showing. Keep it clear and not
crowded: a good sketch is 8–40 steps.

Reply with ONE JSON object and nothing else: {"steps": [STEP, …]}
Allowed steps: box, code, arrow, line, pen, ring, underline, penarrow, laser,
say, pause, click, label, drag, delete, group, ungroup, nudge, erase (with
"at": one shape; never the whole board).

` + stepsSpec)
}
