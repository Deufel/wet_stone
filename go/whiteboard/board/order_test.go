package board

import "testing"

// Pointer commands arriving out of order still make the stroke the user drew.
func TestPointerOrder(t *testing.T) {
	type step struct {
		op        string
		seq, dseq int64
		x, y      int32
	}
	boxes := func(b *Board) int {
		n := 0
		for i := range b.Shapes {
			if b.Shapes[i].Kind == Box {
				n++
			}
		}
		return n
	}
	run := func(steps []step) (*Board, []string) {
		b := New()
		b.Join(1, "t")
		var changes []string
		for _, s := range steps {
			changes = append(changes, b.Apply(Cmd{Op: s.op, UID: 1, Tool: 1, Seq: s.seq, DSeq: s.dseq, X: s.x, Y: s.y}))
		}
		return b, changes
	}

	// the first move overtakes the down: the box is still made
	b, ch := run([]step{{"move", 2, 1, 300, 300}, {"down", 1, 1, 200, 200}, {"move", 3, 1, 400, 400}, {"up", 4, 1, 400, 400}})
	if boxes(b) != 1 || ch[3] != "draw box" {
		t.Fatalf("late down: %d boxes, %q", boxes(b), ch)
	}

	// the up overtakes its down: held, then applied with it
	b, ch = run([]step{{"up", 4, 1, 400, 400}, {"down", 1, 1, 200, 200}})
	if boxes(b) != 1 || ch[1] != "draw box" || b.Users[0].Drag.Kind != DragNone {
		t.Fatalf("early up: %d boxes, %q, drag %v", boxes(b), ch, b.Users[0].Drag.Kind)
	}

	// a hover move overtakes the up: the drag still ends, and the box stays put
	b, _ = run([]step{{"down", 1, 1, 200, 200}, {"move", 2, 1, 400, 400}, {"move", 4, 1, 700, 700}, {"up", 3, 1, 400, 400}})
	if u := &b.Users[0]; u.Drag.Kind != DragNone {
		t.Fatal("drag never ended")
	}
	for i := range b.Shapes {
		if s := &b.Shapes[i]; s.Kind == Box && (s.X2 != 400 || s.Y2 != 400) {
			t.Fatalf("box followed the hover: %d,%d", s.X2, s.Y2)
		}
	}

	// an up lost for good: the next down ends that stroke, then starts its own
	b, ch = run([]step{{"down", 1, 1, 200, 200}, {"move", 2, 1, 400, 400}, {"down", 5, 5, 500, 500}, {"move", 6, 5, 700, 700}, {"up", 7, 5, 700, 700}})
	if boxes(b) != 2 || ch[2] != "draw box" || ch[4] != "draw box" {
		t.Fatalf("lost up: %d boxes, %q", boxes(b), ch)
	}
}
