package board

import (
	"fmt"
	"time"
)

// Cmd is one command as the handler read it from the page's signals.
type Cmd struct {
	Op     string // down move up delete clear link label nudge group ungroup locktoggle
	UID    uint32
	X, Y   int32
	Tool   uint8 // 0 select, 1 box, 2 line, 3 arrow, 4 laser
	NX, NY int8  // nudge: grid steps, -1..1
	Seq    int64 // pointer commands: the page's running count
	DSeq   int64 // pointer commands: the Seq of the down that began this stroke
	Shape  int32 // label: the shape the text was typed for
	Label  string
}

// Apply runs one command. It returns what changed the drawing, for the
// history ("draw box", "move", "label", ...), or "" when the drawing itself
// cannot have changed: moves never do (a drag is saved once, at its up), nor a
// click, a marquee or a laser stroke.
func (b *Board) Apply(c Cmd) (change string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if (c.Op == "down" || c.Op == "move" || c.Op == "up") && c.Seq != 0 {
		return b.pointer(c)
	}
	switch c.Op {
	case "down":
		b.down(c)
	case "move":
		b.move(c)
	case "up":
		return b.up(c)
	case "delete":
		b.delete(c)
		return "delete"
	case "clear":
		b.clear()
		return "clear"
	case "link":
		b.link(c)
		return "link"
	case "editsel": // Enter: the selected shape's editor opens
		if u := b.userFor(c.UID); u != nil && u.Selected >= 0 && b.Shapes[u.Selected].Kind != None {
			u.EditSeq++
		}
		return ""
	case "label":
		b.label(c)
		return "label"
	case "nudge":
		b.nudge(c)
		return "nudge"
	case "group":
		b.group(c)
		return "group"
	case "ungroup":
		b.ungroup(c)
		return "ungroup"
	case "locktoggle":
		return b.lockToggle(c)
	}
	return ""
}

// Replace puts a saved drawing on the board (a history checkout). Every
// viewer's selection and drag goes, since they named shapes that may not exist.
func (b *Board) Replace(snap Snapshot) {
	b.mu.Lock()
	for i := range b.Shapes {
		b.Shapes[i] = Shape{Joint: [2]int32{-1, -1}, Corner: [4]int32{-1, -1, -1, -1}, Group: -1}
	}
	for j := range b.Joints {
		b.Joints[j] = Joint{}
	}
	for i := range b.Users {
		u := &b.Users[i]
		u.Sel = [ShapesMax]bool{}
		u.Selected = -1
		u.Drag = Drag{}
		u.clearGuides()
	}
	b.mu.Unlock()
	b.Restore(snap)
}

// Join and Leave count a viewer's open streams; an open stream keeps it present.
func (b *Board) Join(uid uint32, name string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if u := b.userFor(uid); u != nil {
		u.streams++
		u.Name = name
	}
}

func (b *Board) Leave(uid uint32) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if u := b.viewer(uid); u != nil {
		u.streams--
		if u.streams <= 0 {
			u.Active = false
		}
	}
}

// Tick expires laser segments and idle viewers; it reports whether any went.
func (b *Board) Tick(now time.Time) (changed bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for i := range b.Lasers {
		if seg := &b.Lasers[i]; seg.Active && now.Sub(seg.Born) > LaserTTL {
			seg.Active = false
			changed = true
		}
	}
	for i := range b.Users {
		if u := &b.Users[i]; u.Active && u.streams <= 0 && now.Sub(u.LastSeen) > UserTTL {
			u.Active = false
			changed = true
		}
	}
	return
}

// ─── Pointer commands ───────────────────────────────────────────────────────

// pointer puts one viewer's pointer commands back in stroke order. They run on
// separate requests, so a later one can overtake an earlier one (several
// connections, or HTTP/2 streams each on its own goroutine). Dropping whatever
// is older than the newest seen, as this once did, lost a down that its first
// move overtook (the box never appeared) and an up that a hover move overtook
// (the drag never ended: the box followed the cursor, the board "froze").
// Now, by the stroke each command names (DSeq, its down's Seq):
//
//   - a down is never lost to a move; an older down than the current one is
//     stale and goes;
//   - an up that beats its down is held, and applied the moment the down lands;
//   - a move applies only within the current stroke and in order; any other is
//     stale (the up carries the final point, so a lost move costs nothing).
func (b *Board) pointer(c Cmd) string {
	u := b.userFor(c.UID)
	if u == nil {
		return ""
	}
	if c.DSeq == 0 && c.Op != "down" { // a page from before DSeq: its strokes are in order enough
		c.DSeq = u.downSeq
	}
	switch c.Op {
	case "down":
		if c.Seq <= u.downSeq {
			return ""
		}
		change := ""
		if u.downSeq != 0 && u.upSeq == 0 && u.Drag.Kind != DragNone {
			// the last stroke's up never came: end it where it was last seen
			// rather than leave a half-made shape
			change = b.up(Cmd{Op: "up", UID: c.UID, X: u.X, Y: u.Y})
		}
		u.downSeq, u.upSeq = c.Seq, 0
		u.lastSeq = max(u.lastSeq, c.Seq)
		b.down(c)
		up := u.heldUp
		u.heldUp = Cmd{}
		if up.Seq != 0 && up.DSeq == c.Seq { // its up arrived first
			u.upSeq, u.lastSeq = up.Seq, max(u.lastSeq, up.Seq)
			change = b.up(up)
		}
		return change
	case "up":
		switch {
		case c.DSeq > u.downSeq:
			u.heldUp = c // its down is still on the way
			return ""
		case c.DSeq < u.downSeq || u.upSeq != 0:
			return "" // an older stroke's, or a repeat
		}
		u.upSeq, u.lastSeq = c.Seq, max(u.lastSeq, c.Seq)
		return b.up(c)
	}
	// (a fresh viewer, downSeq 0, takes any move in order: only its cursor)
	if (c.DSeq != u.downSeq && u.downSeq != 0) || c.Seq <= u.lastSeq {
		return "" // a move from another stroke, or one overtaken
	}
	u.lastSeq = c.Seq
	b.move(c)
	return ""
}

func (b *Board) down(c Cmd) {
	u := b.userFor(c.UID)
	if u == nil {
		return
	}
	u.X, u.Y = c.X, c.Y
	u.clearGuides()
	switch c.Tool {
	case 5: // the pen: a stroke that stays
		index := b.shapeNew(Pen, c.X, c.Y)
		if index < 0 {
			u.note("board is full: delete something first")
			return
		}
		b.Shapes[index].Points = []int32{c.X, c.Y}
		u.Sel = [ShapesMax]bool{}
		u.Selected = -1
		u.Drag = Drag{Kind: DragPen, Shape: index, Fresh: true}
	case 4:
		u.Drag = Drag{Kind: DragLaser, Shape: -1}
		u.laserN, u.laserSeg = 0, 0
		b.laserPoint(u, c.X, c.Y)
	case 1, 2, 3:
		gx, gy := snapGrid(c.X), snapGrid(c.Y)
		kind := Box
		if c.Tool == 2 {
			kind = Line
		} else if c.Tool == 3 {
			kind = Arrow
		}
		if kind != Box {
			if px, py, ok := b.nearestPort(c.X, c.Y, -1, [2]int32{-1, -1}); ok {
				gx, gy = px, py
			} else if ax, ay, ok := b.nearestAnchor(c.X, c.Y, -1, -1); ok {
				gx, gy = ax, ay
			}
		}
		index := b.shapeNew(kind, gx, gy)
		if index < 0 {
			u.note("board is full: delete something first")
			return
		}
		u.Sel = [ShapesMax]bool{}
		u.Sel[index] = true
		u.Selected = index
		u.Drag = Drag{Kind: DragHandle, Shape: index, HX: 1, HY: 1, Fresh: true}
	default:
		if b.arrowFromPort(u, c.X, c.Y) {
			return
		}
		index, drag, hit := b.hitTest(c.X, c.Y)
		switch {
		case !hit:
			u.Sel = [ShapesMax]bool{}
			u.Selected = -1
			u.Drag = Drag{Kind: DragMarquee, Shape: -1, GX: c.X, GY: c.Y}
		case b.Shapes[index].Group >= 0 && drag.Kind == DragHandle:
			// a node inside a group (a line end, a box corner) is dragged on its
			// own; the group stays selected around it
			if !u.Sel[index] {
				u.Sel = [ShapesMax]bool{}
				b.selSet(u, index, true)
			}
			u.Selected = index
			u.Drag = drag
		case b.selCount(u) >= 2:
			// a multi-selection: a click toggles a shape (with its group), a drag moves them all
			toggle := int32(-1)
			if u.Sel[index] {
				toggle = index
			} else {
				b.selSet(u, index, true)
			}
			u.Selected = index
			u.Drag = Drag{Kind: DragGroup, Shape: index, GX: c.X, GY: c.Y, Toggle: toggle}
		default:
			u.Sel = [ShapesMax]bool{}
			b.selSet(u, index, true)
			u.Selected = index
			u.Drag = drag
			if b.selCount(u) >= 2 {
				u.Drag = Drag{Kind: DragGroup, Shape: index, GX: c.X, GY: c.Y, Toggle: -1}
			}
		}
		// a press on a label's text: let go without moving, and its editor opens
		if hit && u.Drag.Kind != DragHandle && b.onLabel(&b.Shapes[index], c.X, c.Y) {
			u.Drag.Label, u.Drag.PX, u.Drag.PY = true, c.X, c.Y
		}
	}
}

func (b *Board) move(c Cmd) {
	u := b.userFor(c.UID)
	if u == nil {
		return
	}
	u.X, u.Y = c.X, c.Y
	switch u.Drag.Kind {
	case DragNone, DragMarquee:
		return
	case DragLaser:
		b.laserPoint(u, c.X, c.Y)
		return
	case DragPen:
		b.penPoint(u.Drag.Shape, c.X, c.Y)
		return
	case DragGroup:
		b.groupMove(u, c.X, c.Y)
		return
	}
	index := u.Drag.Shape
	s := &b.Shapes[index]
	if s.Kind == None {
		u.Drag = Drag{}
		return
	}
	u.clearGuides()

	switch u.Drag.Kind {
	case DragWhole:
		if s.Kind == Pen {
			penShift(s, c.X-u.Drag.GX-s.X1, c.Y-u.Drag.GY-s.Y1)
		} else if s.Kind == Box {
			// corner-linked boxes come along as one rigid cluster
			nx, ny := snapGrid(c.X-u.Drag.GX), snapGrid(c.Y-u.Drag.GY)
			var mask [ShapesMax]bool
			mask[index] = true
			b.translateShapes(&mask, nx-s.X1, ny-s.Y1)
		} else {
			// a connector body moves its free ends; joined ends stay in their joints
			rx, ry := b.endPoint(s, int(u.Drag.HX))
			dx, dy := snapGrid(c.X-u.Drag.GX)-rx, snapGrid(c.Y-u.Drag.GY)-ry
			for end := 0; end < 2; end++ {
				if !endIsFree(s, end) {
					continue
				}
				ex, ey := b.endPoint(s, end)
				setEnd(s, end, ex+dx, ey+dy)
			}
		}

	case DragHandle:
		joint := s.Corner[u.Drag.HY*2+u.Drag.HX]
		if isConnector(s.Kind) {
			joint = s.Joint[u.Drag.HX]
		}
		px, py := c.X, c.Y
		portX, portY, onPort := int32(0), int32(0), false
		if isConnector(s.Kind) {
			portX, portY, onPort = b.nearestPort(px, py, index, s.Joint)
		}
		if onPort {
			// 0. a connector end: a box port or a link node, where it will link itself
			px, py = portX, portY
			u.AnchorOn, u.AnchorX, u.AnchorY = true, px, py
		} else if ax, ay, ok := b.nearestAnchor(px, py, index, joint); ok {
			// 1. another shape's anchor
			px, py = ax, ay
			u.AnchorOn, u.AnchorX, u.AnchorY = true, ax, ay
		} else {
			// 2. the shape's own other end, per axis
			var ox, oy int32
			if isConnector(s.Kind) {
				ox, oy = b.endPoint(s, 1-int(u.Drag.HX))
			} else {
				ox, oy = s.X2, s.Y2
				if u.Drag.HX == 1 {
					ox = s.X1
				}
				if u.Drag.HY == 1 {
					oy = s.Y1
				}
			}
			snappedX, snappedY := false, false
			if abs32(px-ox) <= SnapAxis {
				px, u.GuideVX, snappedX = ox, ox, true
			}
			if abs32(py-oy) <= SnapAxis {
				py, u.GuideHY, snappedY = oy, oy, true
			}
			// 3. the grid
			if !snappedX {
				px = snapGrid(px)
			}
			if !snappedY {
				py = snapGrid(py)
			}
		}
		if isConnector(s.Kind) && joint >= 0 {
			// a free joint moves as one: every end in it comes along
			b.Joints[joint].X, b.Joints[joint].Y = px, py
		} else {
			if u.Drag.HX == 0 {
				s.X1 = px
			} else {
				s.X2 = px
			}
			if u.Drag.HY == 0 {
				s.Y1 = py
			} else {
				s.Y2 = py
			}
			if s.Kind == Box {
				b.cornersSettle(index) // a linked corner is a shared vertex
			}
		}
	}
}

// The up carries the release point. Moves are unthrottled and may arrive out of
// order, so the release, not the last move to land, decides where it ends.
func (b *Board) up(c Cmd) (change string) {
	u := b.userFor(c.UID)
	if u == nil {
		return ""
	}
	if u.Drag.Kind == DragNone {
		u.clearGuides()
		return ""
	}
	b.move(c)
	u.clearGuides()
	drag := u.Drag
	u.Drag = Drag{}
	if drag.Label && abs32(c.X-drag.PX) <= 4 && abs32(c.Y-drag.PY) <= 4 {
		u.EditSeq++ // a click on the label: the page opens its editor
	}
	switch drag.Kind {
	case DragMarquee:
		b.marqueeSelect(u, drag.GX, drag.GY, c.X, c.Y)
		return ""
	case DragGroup:
		if !drag.Moved && drag.Toggle >= 0 {
			b.selSet(u, drag.Toggle, false)
			b.selPrimary(u)
			return ""
		}
		return "move selection"
	case DragLaser:
		b.laserFinish(u)
		return ""
	case DragPen:
		s := &b.Shapes[drag.Shape]
		if s.Kind != Pen {
			return ""
		}
		b.penPoint(drag.Shape, c.X, c.Y)
		if len(s.Points) < 4 { // a click, not a stroke
			b.shapeRemove(drag.Shape)
			return ""
		}
		return "draw pen"
	}
	index := drag.Shape
	s := &b.Shapes[index]
	change = "reshape"
	if drag.Fresh {
		change = "draw " + [...]string{"", "box", "line", "arrow"}[s.Kind]
	} else if drag.Kind == DragWhole {
		change = "move"
	}
	switch {
	case s.Kind == Box:
		// normalize, and the corner links travel with the corners they name
		if s.X1 > s.X2 {
			s.X1, s.X2 = s.X2, s.X1
			s.Corner[0], s.Corner[1] = s.Corner[1], s.Corner[0]
			s.Corner[2], s.Corner[3] = s.Corner[3], s.Corner[2]
		}
		if s.Y1 > s.Y2 {
			s.Y1, s.Y2 = s.Y2, s.Y1
			s.Corner[0], s.Corner[2] = s.Corner[2], s.Corner[0]
			s.Corner[1], s.Corner[3] = s.Corner[3], s.Corner[1]
		}
		if s.X2-s.X1 < 8 || s.Y2-s.Y1 < 8 {
			b.shapeRemove(index) // a click, not a box
			if drag.Fresh {
				return ""
			}
			return "delete"
		}
	case isConnector(s.Kind):
		ax, ay := b.endPoint(s, 0)
		bx, by := b.endPoint(s, 1)
		if dist2(ax, ay, bx, by) < 64 {
			b.shapeRemove(index)
			if drag.Fresh {
				return "" // a click with a line tool, not a line
			}
			return "delete"
		}
		// only the ends this drag moved link themselves: an end left on a port
		// after an unlink stays unlinked until it is dragged there again
		moved := [2]bool{drag.Fresh || drag.Kind == DragWhole, drag.Kind == DragWhole}
		if drag.Kind == DragHandle {
			moved[drag.HX] = true
		}
		linked := 0
		for end := 0; end < 2; end++ {
			if moved[end] && b.autoLink(index, end) {
				linked++
			}
		}
		if linked > 0 {
			u.note(fmt.Sprintf("linked %d end%s", linked, plural(linked)))
		}
	}
	return change
}

// arrowFromPort: with one box selected (its ports showing), a press on one of
// its edge midpoints starts an arrow there, drawn out as the drag goes; the
// release links its start to that port. Corners stay resize handles.
func (b *Board) arrowFromPort(u *User, x, y int32) bool {
	box := u.Selected
	if box < 0 || b.Shapes[box].Kind != Box || b.selCount(u) != 1 {
		return false
	}
	r2 := int64(HandleR * HandleR)
	for a := uint8(4); a < PortCount; a++ {
		px, py := anchorPoint(&b.Shapes[box], a)
		if dist2(x, y, px, py) > r2 {
			continue
		}
		index := b.shapeNew(Arrow, px, py)
		if index < 0 {
			u.note("board is full: delete something first")
			return true
		}
		u.Sel = [ShapesMax]bool{}
		u.Sel[index] = true
		u.Selected = index
		u.Drag = Drag{Kind: DragHandle, Shape: index, HX: 1, HY: 1, Fresh: true}
		return true
	}
	return false
}

// ─── Snapping and hit testing ───────────────────────────────────────────────

// Topmost shape under (x, y): which shape, and how a drag from here behaves.
// Pinned things (an end in a box-driven joint, a body with no free end) are
// selected but not dragged.
func (b *Board) hitTest(x, y int32) (int32, Drag, bool) {
	r2 := int64(HandleR * HandleR)
	for i := int32(ShapesMax - 1); i >= 0; i-- {
		s := &b.Shapes[i]
		switch s.Kind {
		case Line, Arrow:
			ax, ay := b.endPoint(s, 0)
			bx, by := b.endPoint(s, 1)
			for end := 0; end < 2; end++ {
				ex, ey := b.endPoint(s, end)
				if dist2(x, y, ex, ey) > r2 {
					continue
				}
				if j := s.Joint[end]; j >= 0 && b.jointPinned(j) {
					return i, Drag{}, true
				}
				return i, Drag{Kind: DragHandle, Shape: i, HX: uint8(end), HY: uint8(end)}, true
			}
			if segDist2(x, y, ax, ay, bx, by) <= LineHit*LineHit {
				ref := 0
				if !endIsFree(s, 0) {
					ref = 1
				}
				if !endIsFree(s, ref) {
					return i, Drag{}, true
				}
				rx, ry := b.endPoint(s, ref)
				return i, Drag{Kind: DragWhole, Shape: i, HX: uint8(ref), GX: x - rx, GY: y - ry}, true
			}
		case Pen:
			if penNear(s, x, y) {
				return i, Drag{Kind: DragWhole, Shape: i, GX: x - s.X1, GY: y - s.Y1}, true
			}
		case Box:
			for _, h := range [4][2]uint8{{0, 0}, {1, 0}, {0, 1}, {1, 1}} {
				cx, cy := cornerPos(s, int(h[1])*2+int(h[0]))
				if dist2(x, y, cx, cy) <= r2 {
					return i, Drag{Kind: DragHandle, Shape: i, HX: h[0], HY: h[1]}, true
				}
			}
			if x >= min(s.X1, s.X2) && x <= max(s.X1, s.X2) && y >= min(s.Y1, s.Y2) && y <= max(s.Y1, s.Y2) {
				return i, Drag{Kind: DragWhole, Shape: i, GX: x - s.X1, GY: y - s.Y1}, true
			}
		}
	}
	return -1, Drag{}, false
}

// Nearest anchor within SnapAnchor, ignoring the dragged shape, the joint being
// dragged, and any end that rides on the dragged box (its anchors are where the
// box already is, so snapping to them would pin the corner).
func (b *Board) nearestAnchor(px, py, skip, skipJoint int32) (ax, ay int32, found bool) {
	best := int64(SnapAnchor * SnapAnchor)
	consider := func(cx, cy int32) {
		if d := dist2(px, py, cx, cy); d <= best {
			best, ax, ay, found = d, cx, cy, true
		}
	}
	for i := range b.Shapes {
		s := &b.Shapes[i]
		if int32(i) == skip || s.Kind == None {
			continue
		}
		if s.Kind == Box {
			if skipJoint >= 0 && boxInJoint(s, skipJoint) {
				continue // it moves with us
			}
			for a := uint8(0); a < AnchorCount; a++ {
				consider(anchorPoint(s, a))
			}
			continue
		}
		for end := 0; end < 2; end++ {
			if j := s.Joint[end]; j >= 0 && (j == skipJoint || b.Joints[j].Box == skip) {
				continue
			}
			consider(b.endPoint(s, end))
		}
		x1, y1 := b.endPoint(s, 0)
		x2, y2 := b.endPoint(s, 1)
		consider((x1+x2)/2, (y1+y2)/2)
	}
	return
}

// Nearest box port or link node within SnapPort, ignoring the dragged shape and
// the joints its own ends are in.
func (b *Board) nearestPort(px, py, skip int32, skipJoints [2]int32) (x, y int32, found bool) {
	best := int64(SnapPort * SnapPort)
	for i := range b.Shapes {
		s := &b.Shapes[i]
		if s.Kind != Box || int32(i) == skip {
			continue
		}
		for a := uint8(0); a < PortCount; a++ {
			cx, cy := anchorPoint(s, a)
			if d := dist2(px, py, cx, cy); d <= best {
				best, x, y, found = d, cx, cy, true
			}
		}
	}
	for j := range b.Joints {
		if !b.Joints[j].Active || int32(j) == skipJoints[0] || int32(j) == skipJoints[1] {
			continue
		}
		cx, cy := b.jointPos(int32(j))
		if d := dist2(px, py, cx, cy); d <= best {
			best, x, y, found = d, cx, cy, true
		}
	}
	return
}

// A free connector end released on a link node joins it; released on a box port
// it joins that port's joint, made on first use. One joint per port, so every
// line drawn to a port ends up in the same link node.
func (b *Board) autoLink(index int32, end int) bool {
	s := &b.Shapes[index]
	if !isConnector(s.Kind) || !endIsFree(s, end) {
		return false
	}
	ex, ey := b.endPoint(s, end)
	other := s.Joint[1-end]
	for j := range b.Joints {
		if !b.Joints[j].Active || int32(j) == other {
			continue
		}
		jx, jy := b.jointPos(int32(j))
		if dist2(jx, jy, ex, ey) > JointEps*JointEps {
			continue
		}
		s.Joint[end] = int32(j)
		setEnd(s, end, jx, jy)
		return true
	}
	for i := int32(ShapesMax - 1); i >= 0; i-- { // topmost box wins
		bx := &b.Shapes[i]
		if bx.Kind != Box {
			continue
		}
		for a := uint8(0); a < PortCount; a++ {
			px, py := anchorPoint(bx, a)
			if dist2(px, py, ex, ey) > JointEps*JointEps {
				continue
			}
			j := b.jointNew()
			if j < 0 {
				return false
			}
			b.Joints[j].Box, b.Joints[j].Anchor = i, a
			b.Joints[j].X, b.Joints[j].Y = px, py
			s.Joint[end] = j
			setEnd(s, end, px, py)
			return true
		}
	}
	return false
}

// A port is linked when a line end rides it, or (a corner) when it shares a
// link node with other boxes' corners.
func (b *Board) portLinked(box int32, anchor uint8) bool {
	if anchor < 4 && b.Shapes[box].Corner[anchor] >= 0 {
		return true
	}
	for j := range b.Joints {
		if jt := &b.Joints[j]; jt.Active && jt.Box == box && jt.Anchor == anchor {
			return true
		}
	}
	return false
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// penPoint adds a point to a pen stroke, if it has moved far enough.
func (b *Board) penPoint(index int32, x, y int32) {
	s := &b.Shapes[index]
	if s.Kind != Pen || len(s.Points) >= 2*PenPointsMax {
		return
	}
	n := len(s.Points)
	if n >= 2 && dist2(x, y, s.Points[n-2], s.Points[n-1]) < 16 {
		return // under 4 units: keep the path light
	}
	s.Points = append(s.Points, x, y)
	penBounds(s)
}

// SelectedBy is the shape that viewer has selected (its primary), or -1: what
// a scripted presenter (the demo) labels after drawing it.
func (b *Board) SelectedBy(uid uint32) int32 {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if u := b.viewer(uid); u != nil {
		return u.Selected
	}
	return -1
}
