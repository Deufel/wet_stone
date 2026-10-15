package board

import "testing"

// A selected box's edge midpoint, dragged away, draws an arrow linked there.
func TestArrowFromPort(t *testing.T) {
	b := New()
	b.Join(1, "t")
	seq := int64(0)
	do := func(op string, tool uint8, x, y int32) string {
		seq++
		return b.Apply(Cmd{Op: op, UID: 1, Tool: tool, X: x, Y: y, Seq: seq})
	}
	do("down", 1, 200, 200) // draw a box 200..400; it stays selected
	do("move", 1, 400, 400)
	if got := do("up", 1, 400, 400); got != "draw box" {
		t.Fatalf("box: %q", got)
	}
	do("down", 0, 400, 300) // the right midpoint, in the select tool
	do("move", 0, 600, 300)
	if got := do("up", 0, 600, 300); got != "draw arrow" {
		t.Fatalf("arrow: %q", got)
	}
	var arrow *Shape
	for i := range b.Shapes {
		if b.Shapes[i].Kind == Arrow {
			arrow = &b.Shapes[i]
		}
	}
	if arrow == nil {
		t.Fatal("no arrow")
	}
	if j := arrow.Joint[0]; j < 0 || b.Joints[j].Box < 0 || b.Joints[j].Anchor != 7 {
		t.Fatalf("start not linked to the right midpoint: joint %d", arrow.Joint[0])
	}
	if x, y := b.endPoint(arrow, 1); x != 600 || y != 300 {
		t.Fatalf("end at %d,%d", x, y)
	}

	// a corner of the selected box still resizes
	do("down", 0, 300, 300) // select the box again
	do("up", 0, 300, 300)
	do("down", 0, 400, 400)
	do("move", 0, 500, 500)
	if got := do("up", 0, 500, 500); got != "reshape" {
		t.Fatalf("corner: %q", got)
	}
}

// A double-click at a box's very centre edits its label (it is not a link node).
func TestDblclickCentreEdits(t *testing.T) {
	b := New()
	b.Join(1, "t")
	b.Apply(Cmd{Op: "down", UID: 1, Tool: 1, X: 200, Y: 200, Seq: 1, DSeq: 1})
	b.Apply(Cmd{Op: "up", UID: 1, Tool: 1, X: 400, Y: 400, Seq: 2, DSeq: 1})
	before := b.Users[0].EditSeq
	b.Apply(Cmd{Op: "link", UID: 1, X: 300, Y: 300})
	if b.Users[0].EditSeq != before+1 {
		t.Fatal("a double-click at the centre did not open the label editor")
	}
}

// A click (no movement) on a shape's label text opens its editor; a drag does not.
func TestClickLabelEdits(t *testing.T) {
	b := New()
	b.Join(1, "t")
	seq := int64(0)
	do := func(op string, tool uint8, x, y int32, dseq int64) {
		seq++
		b.Apply(Cmd{Op: op, UID: 1, Tool: tool, X: x, Y: y, Seq: seq, DSeq: dseq})
	}
	do("down", 1, 200, 200, seq+1)
	do("up", 1, 600, 400, seq)
	b.Apply(Cmd{Op: "label", UID: 1, Shape: 0, Label: "Sign up"})
	before := b.Users[0].EditSeq
	d := seq + 1
	do("down", 0, 400, 300, d) // the label's middle
	do("up", 0, 401, 300, d)
	if b.Users[0].EditSeq != before+1 {
		t.Fatal("a click on the label did not open its editor")
	}
	d = seq + 1
	do("down", 0, 400, 300, d)
	do("move", 0, 460, 340, d)
	do("up", 0, 460, 340, d)
	if b.Users[0].EditSeq != before+1 {
		t.Fatal("a drag by the label opened its editor")
	}
	d = seq + 1
	do("down", 0, 230, 230, d) // the box, far from its label
	do("up", 0, 230, 230, d)
	if b.Users[0].EditSeq != before+1 {
		t.Fatal("a click off the label opened its editor")
	}
}
