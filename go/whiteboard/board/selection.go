package board

import "fmt"

// ─── Selection ──────────────────────────────────────────────────────────────

func (b *Board) selCount(u *User) (n int) {
	for i := range u.Sel {
		if u.Sel[i] && b.Shapes[i].Kind != None {
			n++
		}
	}
	return
}

// A shape is selected or deselected together with its whole group.
func (b *Board) selSet(u *User, index int32, on bool) {
	g := b.Shapes[index].Group
	if g < 0 {
		u.Sel[index] = on
		return
	}
	for i := range b.Shapes {
		if b.Shapes[i].Kind != None && b.Shapes[i].Group == g {
			u.Sel[i] = on
		}
	}
}

// The primary follows the selection when it narrows to one shape.
func (b *Board) selPrimary(u *User) {
	if u.Selected >= 0 && !u.Sel[u.Selected] {
		u.Selected = -1
	}
	if b.selCount(u) != 1 {
		return
	}
	for i := range u.Sel {
		if u.Sel[i] && b.Shapes[i].Kind != None {
			u.Selected = int32(i)
		}
	}
}

// A marquee dragged downward takes every shape it touches; dragged upward, only
// the shapes it fully encloses. Groups come whole.
func (b *Board) marqueeSelect(u *User, sx, sy, ex, ey int32) {
	u.Sel = [ShapesMax]bool{}
	u.Selected = -1
	if abs32(ex-sx) < 4 && abs32(ey-sy) < 4 {
		return // a click on empty board
	}
	touch := ey >= sy
	rx1, rx2 := min(sx, ex), max(sx, ex)
	ry1, ry2 := min(sy, ey), max(sy, ey)
	inside := func(x, y int32) bool { return x >= rx1 && x <= rx2 && y >= ry1 && y <= ry2 }
	for i := range b.Shapes {
		s := &b.Shapes[i]
		hit := false
		switch s.Kind {
		case None:
			continue
		case Box:
			bx1, bx2 := min(s.X1, s.X2), max(s.X1, s.X2)
			by1, by2 := min(s.Y1, s.Y2), max(s.Y1, s.Y2)
			if touch {
				hit = bx1 <= rx2 && bx2 >= rx1 && by1 <= ry2 && by2 >= ry1
			} else {
				hit = bx1 >= rx1 && bx2 <= rx2 && by1 >= ry1 && by2 <= ry2
			}
		case Pen:
			if touch { // any of its points inside
				for p := 0; p+1 < len(s.Points) && !hit; p += 2 {
					hit = inside(s.Points[p], s.Points[p+1])
				}
			} else {
				hit = s.X1 >= rx1 && s.X2 <= rx2 && s.Y1 >= ry1 && s.Y2 <= ry2
			}
		case Line, Arrow:
			ax, ay := b.endPoint(s, 0)
			bx, by := b.endPoint(s, 1)
			aIn, bIn := inside(ax, ay), inside(bx, by)
			if !touch {
				hit = aIn && bIn
			} else {
				hit = aIn || bIn ||
					segCross(ax, ay, bx, by, rx1, ry1, rx2, ry1) ||
					segCross(ax, ay, bx, by, rx2, ry1, rx2, ry2) ||
					segCross(ax, ay, bx, by, rx2, ry2, rx1, ry2) ||
					segCross(ax, ay, bx, by, rx1, ry2, rx1, ry1)
			}
		}
		if hit {
			b.selSet(u, int32(i), true)
		}
	}
	b.selPrimary(u)
}

// Double-click on a shape away from any link point: edit that shape's label,
// even inside a group (the group stays selected around it).
func (b *Board) editAt(u *User, x, y int32) bool {
	index, _, hit := b.hitTest(x, y)
	if !hit {
		return false
	}
	if !u.Sel[index] {
		u.Sel = [ShapesMax]bool{}
		b.selSet(u, index, true)
	}
	u.Selected = index
	u.EditSeq++
	return true
}

// ─── Moving the selection ───────────────────────────────────────────────────

// The selection follows the pointer in whole grid steps from where it was grabbed.
func (b *Board) groupMove(u *User, x, y int32) {
	d := &u.Drag
	if abs32(x-d.GX) > 3 || abs32(y-d.GY) > 3 {
		d.Moved = true
	}
	dx := snapDelta(x-d.GX) - d.AX
	dy := snapDelta(y-d.GY) - d.AY
	if dx == 0 && dy == 0 {
		return
	}
	dx, dy = b.clampToBoard(&u.Sel, dx, dy)
	b.moveSelection(u, dx, dy, false)
	d.AX += dx
	d.AY += dy
}

// Move the selection: grouped shapes as rigid units, the rest either as
// corner-linked clusters (a drag) or by their unheld edges (stretch: arrow keys).
func (b *Board) moveSelection(u *User, dx, dy int32, stretch bool) {
	var grouped, loose [ShapesMax]bool
	for i := range b.Shapes {
		if !u.Sel[i] || b.Shapes[i].Kind == None {
			continue
		}
		if b.Shapes[i].Group >= 0 {
			grouped[i] = true
		} else {
			loose[i] = true
		}
	}
	if stretch {
		b.stretchShapes(&loose, dx, dy)
	} else {
		b.translateShapes(&loose, dx, dy)
	}
	b.groupTranslate(&grouped, dx, dy)
}

// A group moves as a unit whatever its links: every member keeps its shape and
// links inside the group move with it. Outside shapes linked to a member stretch
// to follow (a line end riding a member box, a box sharing a member's corner); a
// member's line end held by something outside lets go and comes along.
func (b *Board) groupTranslate(mask *[ShapesMax]bool, dx, dy int32) {
	if dx == 0 && dy == 0 {
		return
	}
	for i := range b.Shapes {
		s := &b.Shapes[i]
		if !mask[i] || !isConnector(s.Kind) {
			continue
		}
		for end := 0; end < 2; end++ {
			j := s.Joint[end]
			if j < 0 || b.jointInside(j, mask) {
				continue
			}
			x, y := b.endPoint(s, end)
			s.Joint[end] = -1
			setEnd(s, end, x, y)
			b.jointGC(j)
		}
	}
	var moved [JointsMax]bool
	for i := range b.Shapes {
		s := &b.Shapes[i]
		if !mask[i] || s.Kind != Box {
			continue
		}
		s.X1 += dx
		s.X2 += dx
		s.Y1 += dy
		s.Y2 += dy
		for _, j := range s.Corner {
			if j < 0 || moved[j] || !b.jointInside(j, mask) {
				continue // outside: settled below
			}
			b.Joints[j].X += dx
			b.Joints[j].Y += dy
			moved[j] = true
		}
	}
	for j := range b.Joints {
		jt := &b.Joints[j]
		if !jt.Active || jt.Box >= 0 || moved[j] || b.jointHasCorners(int32(j)) || !b.jointInside(int32(j), mask) {
			continue
		}
		jt.X += dx
		jt.Y += dy
	}
	for i := range b.Shapes {
		s := &b.Shapes[i]
		if !mask[i] || !isConnector(s.Kind) {
			continue
		}
		for end := 0; end < 2; end++ {
			if !endIsFree(s, end) {
				continue
			}
			ex, ey := b.endPoint(s, end)
			setEnd(s, end, ex+dx, ey+dy)
		}
	}
	for i := range b.Shapes {
		if mask[i] && b.Shapes[i].Kind == Box {
			b.cornersSettle(int32(i))
		}
	}
}

// Is every member of joint j (its driving box, box corners, line ends) in mask?
func (b *Board) jointInside(j int32, mask *[ShapesMax]bool) bool {
	jt := &b.Joints[j]
	if jt.Box >= 0 && !mask[jt.Box] {
		return false
	}
	for i := range b.Shapes {
		s := &b.Shapes[i]
		if s.Kind == None || mask[i] {
			continue
		}
		if s.Kind == Box && boxInJoint(s, j) {
			return false
		}
		if isConnector(s.Kind) && (s.Joint[0] == j || s.Joint[1] == j) {
			return false
		}
	}
	return true
}

// Move every shape in mask by (dx, dy). A box brings every box corner-linked to
// it, a rigid cluster, with their corner joints; a connector moves only its free
// ends; a free link node moves when every end in it is moving.
func (b *Board) translateShapes(mask *[ShapesMax]bool, dx, dy int32) {
	var boxes [ShapesMax]bool
	var moved [JointsMax]bool
	var stack [ShapesMax]int32
	n := 0
	for i := range b.Shapes {
		if mask[i] && b.Shapes[i].Kind == Box {
			boxes[i] = true
			stack[n] = int32(i)
			n++
		}
	}
	for n > 0 {
		n--
		s := &b.Shapes[stack[n]]
		for _, j := range s.Corner {
			if j < 0 {
				continue
			}
			for oi := range b.Shapes {
				o := &b.Shapes[oi]
				if o.Kind != Box || boxes[oi] || !boxInJoint(o, j) {
					continue
				}
				boxes[oi] = true
				stack[n] = int32(oi)
				n++
			}
		}
	}
	for i := range b.Shapes {
		if !boxes[i] {
			continue
		}
		s := &b.Shapes[i]
		s.X1 += dx
		s.X2 += dx
		s.Y1 += dy
		s.Y2 += dy
		for _, j := range s.Corner {
			if j < 0 || moved[j] {
				continue
			}
			b.Joints[j].X += dx
			b.Joints[j].Y += dy
			moved[j] = true
		}
	}
	for j := range b.Joints {
		jt := &b.Joints[j]
		if !jt.Active || jt.Box >= 0 || moved[j] || b.jointHasCorners(int32(j)) {
			continue
		}
		any, all := false, true
		for i := range b.Shapes {
			s := &b.Shapes[i]
			if !isConnector(s.Kind) {
				continue
			}
			for end := 0; end < 2; end++ {
				if s.Joint[end] == int32(j) {
					any = true
					if !mask[i] {
						all = false
					}
				}
			}
		}
		if any && all {
			jt.X += dx
			jt.Y += dy
		}
	}
	for i := range b.Shapes {
		s := &b.Shapes[i]
		if !mask[i] || !isConnector(s.Kind) {
			continue
		}
		for end := 0; end < 2; end++ {
			if !endIsFree(s, end) {
				continue
			}
			ex, ey := b.endPoint(s, end)
			setEnd(s, end, ex+dx, ey+dy)
		}
	}
	for i := range b.Shapes {
		if s := &b.Shapes[i]; mask[i] && s.Kind == Pen {
			penShift(s, dx, dy)
		}
	}
}

// Bounding box of the shapes in mask.
func (b *Board) maskExtents(mask *[ShapesMax]bool) (x1, y1, x2, y2 int32, ok bool) {
	const big = int32(1 << 30)
	x1, y1, x2, y2 = big, big, -big, -big
	for i := range b.Shapes {
		s := &b.Shapes[i]
		if !mask[i] || s.Kind == None {
			continue
		}
		ax, ay, bx, by := s.X1, s.Y1, s.X2, s.Y2
		if isConnector(s.Kind) {
			ax, ay = b.endPoint(s, 0)
			bx, by = b.endPoint(s, 1)
		}
		x1, y1 = min(x1, ax, bx), min(y1, ay, by)
		x2, y2 = max(x2, ax, bx), max(y2, ay, by)
		ok = true
	}
	return
}

// Shorten a move so the shapes in mask stay on the board.
func (b *Board) clampToBoard(mask *[ShapesMax]bool, dx, dy int32) (int32, int32) {
	x1, y1, x2, y2, ok := b.maskExtents(mask)
	if !ok {
		return dx, dy
	}
	return clamp32(dx, -x1, W-x2), clamp32(dy, -y1, b.Height-y2)
}

// Arrow keys: the selection moves one grid step. Groups move as units; other
// shapes move only what is not held (see stretchShapes).
func (b *Board) nudge(c Cmd) {
	u := b.userFor(c.UID)
	if u == nil || b.selCount(u) == 0 {
		return
	}
	if b.selCount(u) == 1 && u.Selected >= 0 {
		s := &b.Shapes[u.Selected]
		if isConnector(s.Kind) && s.Group < 0 && !endIsFree(s, 0) && !endIsFree(s, 1) {
			u.note("both ends are linked")
			return
		}
	}
	dx, dy := b.clampToBoard(&u.Sel, int32(c.NX)*Grid, int32(c.NY)*Grid)
	b.moveSelection(u, dx, dy, true)
}

// Arrow keys on ungrouped shapes: a box moves only the edges no outside corner
// link holds; connectors move their free ends and fully selected free link nodes.
func (b *Board) stretchShapes(mask *[ShapesMax]bool, dx, dy int32) {
	for i := range b.Shapes {
		s := &b.Shapes[i]
		if !mask[i] || s.Kind != Box {
			continue
		}
		var held [4]bool // corner c holds its x (x2 if c&1) and its y (y2 if c>=2)
		for c := 0; c < 4; c++ {
			held[c] = b.cornerHeld(s.Corner[c], mask)
		}
		x1, x2, y1, y2 := s.X1, s.X2, s.Y1, s.Y2
		if !held[0] && !held[2] {
			x1 += dx
		}
		if !held[1] && !held[3] {
			x2 += dx
		}
		if !held[0] && !held[1] {
			y1 += dy
		}
		if !held[2] && !held[3] {
			y2 += dy
		}
		if abs32(x2-x1) < 8 { // never collapse a box
			x1, x2 = s.X1, s.X2
		}
		if abs32(y2-y1) < 8 {
			y1, y2 = s.Y1, s.Y2
		}
		if x1 == s.X1 && x2 == s.X2 && y1 == s.Y1 && y2 == s.Y2 {
			continue
		}
		s.X1, s.X2, s.Y1, s.Y2 = x1, x2, y1, y2
		b.cornersSettle(int32(i)) // corner links among selected boxes stay shared
	}
	lines := *mask
	for i := range b.Shapes {
		if !isConnector(b.Shapes[i].Kind) { // boxes stretched above; a pen does not stretch
			lines[i] = false
		}
	}
	b.translateShapes(&lines, dx, dy)
}

// A corner is held when it shares a link node with a box outside the selection.
func (b *Board) cornerHeld(j int32, sel *[ShapesMax]bool) bool {
	if j < 0 {
		return false
	}
	for oi := range b.Shapes {
		if o := &b.Shapes[oi]; o.Kind == Box && !sel[oi] && boxInJoint(o, j) {
			return true
		}
	}
	return false
}

// ─── Groups, delete, clear ──────────────────────────────────────────────────

func (b *Board) group(c Cmd) {
	u := b.userFor(c.UID)
	if u == nil {
		return
	}
	n := b.selCount(u)
	if n < 2 {
		u.note("select two or more shapes to group")
		return
	}
	var used [ShapesMax]bool
	for i := range b.Shapes {
		if s := &b.Shapes[i]; s.Kind != None && s.Group >= 0 {
			used[s.Group] = true
		}
	}
	g := int32(-1)
	for i := range used {
		if !used[i] {
			g = int32(i)
			break
		}
	}
	if g < 0 {
		return
	}
	// the group takes the primary's color, else the first member's
	first := u.Selected
	if first < 0 {
		for i := range u.Sel {
			if u.Sel[i] && b.Shapes[i].Kind != None {
				first = int32(i)
				break
			}
		}
	}
	b.GroupHue[g] = b.shapeHue(&b.Shapes[first])
	for i := range b.Shapes {
		if u.Sel[i] && b.Shapes[i].Kind != None {
			b.Shapes[i].Group = g
		}
	}
	u.note(fmt.Sprintf("grouped %d shapes", n))
}

func (b *Board) ungroup(c Cmd) {
	u := b.userFor(c.UID)
	if u == nil {
		return
	}
	for i := range b.Shapes {
		g := b.Shapes[i].Group
		if !u.Sel[i] || b.Shapes[i].Kind == None || g < 0 {
			continue
		}
		for k := range b.Shapes {
			if b.Shapes[k].Group == g {
				b.Shapes[k].Group = -1
			}
		}
	}
	u.note("ungrouped")
}

func (b *Board) delete(c Cmd) {
	u := b.userFor(c.UID)
	if u == nil {
		return
	}
	for i := range u.Sel {
		if u.Sel[i] && b.Shapes[i].Kind != None {
			b.shapeRemove(int32(i))
		}
	}
	if u.Selected >= 0 && b.Shapes[u.Selected].Kind != None {
		b.shapeRemove(u.Selected)
	}
}

func (b *Board) clear() {
	for i := range b.Shapes {
		b.Shapes[i].Kind = None
	}
	for j := range b.Joints {
		b.Joints[j].Active = false
	}
	for i := range b.Users {
		u := &b.Users[i]
		u.Selected = -1
		u.Sel = [ShapesMax]bool{}
		u.Drag = Drag{}
		u.clearGuides()
	}
}

// The label carries the shape it was typed for, not "whatever is selected now".
func (b *Board) label(c Cmd) {
	u := b.userFor(c.UID)
	if u == nil || c.Shape < 0 || c.Shape >= ShapesMax {
		return
	}
	s := &b.Shapes[c.Shape]
	if s.Kind == None {
		return
	}
	s.Label = c.Label
}
