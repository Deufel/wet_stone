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
