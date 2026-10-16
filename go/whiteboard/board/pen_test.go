package board

import "testing"

// A pen stroke is drawn, kept, selected by touch or enclosure, moved whole,
// saved and restored.
func TestPen(t *testing.T) {
	b := New()
	b.Join(1, "t")
	seq := int64(1)
	b.Apply(Cmd{Op: "down", UID: 1, Tool: 5, X: 100, Y: 100, Seq: seq, DSeq: seq})
	d := seq
	for i := int32(1); i <= 20; i++ {
		seq++
		b.Apply(Cmd{Op: "move", UID: 1, Tool: 5, X: 100 + i*10, Y: 100 + (i%3)*10, Seq: seq, DSeq: d})
	}
	seq++
	if got := b.Apply(Cmd{Op: "up", UID: 1, Tool: 5, X: 310, Y: 100, Seq: seq, DSeq: d}); got != "draw pen" {
		t.Fatalf("up: %q", got)
	}
	s := &b.Shapes[0]
	if s.Kind != Pen || len(s.Points) < 20 || s.X1 != 100 || s.X2 != 310 {
		t.Fatalf("pen %v points %d box %d..%d", s.Kind, len(s.Points)/2, s.X1, s.X2)
	}
	// grab it on the stroke and move it 50 right
	seq++
	d = seq
	b.Apply(Cmd{Op: "down", UID: 1, Tool: 0, X: 190, Y: 100, Seq: seq, DSeq: d})
	seq++
	b.Apply(Cmd{Op: "up", UID: 1, Tool: 0, X: 240, Y: 100, Seq: seq, DSeq: d})
	if s.X1 != 150 || s.Points[0] != 150 {
		t.Fatalf("moved to %d (first point %d)", s.X1, s.Points[0])
	}
	// a click (no stroke) with the pen leaves nothing
	seq++
	b.Apply(Cmd{Op: "down", UID: 1, Tool: 5, X: 600, Y: 600, Seq: seq, DSeq: seq})
	d = seq
	seq++
	b.Apply(Cmd{Op: "up", UID: 1, Tool: 5, X: 600, Y: 600, Seq: seq, DSeq: d})
	if b.Shapes[1].Kind != None {
		t.Fatal("a pen click left a shape")
	}
	snap := b.Snapshot()
	c := New()
	c.Restore(snap)
	if c.Shapes[0].Kind != Pen || len(c.Shapes[0].Points) != len(s.Points) {
		t.Fatal("not restored")
	}
	if th := Thumb(snap); len(th) < 40 {
		t.Fatalf("thumb %q", th)
	}
}
