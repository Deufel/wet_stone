package board

import (
	"fmt"
	"time"
)

// ─── Double-click: link, unlink, or edit a label ────────────────────────────

// Double-click at (x, y): gather every node on that coordinate. If any of them
// is in a joint, dissolve those joints (unlink). Otherwise put them all in one
// new joint (link). With no link point here, a double-click on a shape edits
// its label.
func (b *Board) link(c Cmd) {
	u := b.userFor(c.UID)
	if u == nil {
		return
	}
	r2 := int64(HandleR * HandleR)
	const eps = JointEps * JointEps

	// 1. the node nearest the click is the coordinate we mean
	best := r2 + 1
	var tx, ty int32
	for i := range b.Shapes {
		s := &b.Shapes[i]
		switch s.Kind {
		case Box:
			// its ports only (corners, edge midpoints): a link is never made
			// at a box's centre, and a double-click there means its label
			for a := uint8(0); a < PortCount; a++ {
				ax, ay := anchorPoint(s, a)
				if d := dist2(c.X, c.Y, ax, ay); d < best {
					best, tx, ty = d, ax, ay
				}
			}
		case Line, Arrow:
			for end := 0; end < 2; end++ {
				ex, ey := b.endPoint(s, end)
				if d := dist2(c.X, c.Y, ex, ey); d < best {
					best, tx, ty = d, ex, ey
				}
			}
		}
	}
	if best > r2 {
		if !b.editAt(u, c.X, c.Y) {
			u.note("no node here")
		}
		return
	}

	// 2. unlink: any joint on this coordinate is dissolved
	freed := 0
	for j := range b.Joints {
		if !b.Joints[j].Active {
			continue
		}
		if jx, jy := b.jointPos(int32(j)); dist2(jx, jy, tx, ty) <= eps {
			freed += b.jointDissolve(int32(j))
		}
	}
	if freed > 0 {
		u.note(fmt.Sprintf("unlinked %d nodes", freed))
		return
	}

	// 3a. box corners: two or more boxes with a corner here share one link node
	corners := 0
	for i := range b.Shapes {
		if s := &b.Shapes[i]; s.Kind == Box {
			for k := 0; k < 4; k++ {
				if cx, cy := cornerPos(s, k); dist2(cx, cy, tx, ty) <= eps {
					corners++
					break
				}
			}
		}
	}
	if corners >= 2 {
		j := b.jointNew()
		if j < 0 {
			u.note("too many links")
			return
		}
		b.Joints[j].X, b.Joints[j].Y = tx, ty
		nodes := 0
		for i := range b.Shapes {
			s := &b.Shapes[i]
			if s.Kind == Box {
				for k := 0; k < 4; k++ {
					if cx, cy := cornerPos(s, k); dist2(cx, cy, tx, ty) <= eps {
						setCorner(s, k, tx, ty)
						s.Corner[k] = j
						nodes++
						break
					}
				}
			} else if isConnector(s.Kind) {
				for end := 0; end < 2; end++ {
					if ex, ey := b.endPoint(s, end); dist2(ex, ey, tx, ty) <= eps {
						setEnd(s, end, tx, ty)
						s.Joint[end] = j
						nodes++
					}
				}
			}
		}
		u.note(fmt.Sprintf("linked %d nodes", nodes))
		return
	}

	// 3b. link: every free connector end and the topmost box anchor here
	ends := 0
	driver, driverAnchor := int32(-1), uint8(0)
	for i := range b.Shapes {
		s := &b.Shapes[i]
		switch s.Kind {
		case Box:
			for a := uint8(0); a < AnchorCount; a++ {
				if ax, ay := anchorPoint(s, a); dist2(ax, ay, tx, ty) <= eps {
					driver, driverAnchor = int32(i), a // later index = drawn on top
					break
				}
			}
		case Line, Arrow:
			for end := 0; end < 2; end++ {
				if ex, ey := b.endPoint(s, end); dist2(ex, ey, tx, ty) <= eps {
					ends++
				}
			}
		}
	}
	nodes := ends
	if driver >= 0 {
		nodes++
	}
	if ends == 0 || nodes < 2 {
		// one node is no link: a double-click on a shape edits its label
		if !b.editAt(u, c.X, c.Y) {
			u.note("only one node here: nothing to link")
		}
		return
	}
	j := b.jointNew()
	if j < 0 {
		u.note("too many links")
		return
	}
	b.Joints[j].Box, b.Joints[j].Anchor = driver, driverAnchor
	b.Joints[j].X, b.Joints[j].Y = tx, ty
	for i := range b.Shapes {
		s := &b.Shapes[i]
		if !isConnector(s.Kind) {
			continue
		}
		for end := 0; end < 2; end++ {
			if ex, ey := b.endPoint(s, end); dist2(ex, ey, tx, ty) <= eps {
				setEnd(s, end, tx, ty)
				s.Joint[end] = j
			}
		}
	}
	u.note(fmt.Sprintf("linked %d nodes", nodes))
}

// ─── Lock and unlock (right-click) ──────────────────────────────────────────

// Right-click at (x, y): a shape there joins the selection first if it is not
// in it (selected alone, with its group); then the selection unlocks if any of
// it is linked, else locks onto whatever overlaps it.
func (b *Board) lockToggle(c Cmd) string {
	u := b.userFor(c.UID)
	if u == nil {
		return ""
	}
	if index, _, hit := b.hitTest(c.X, c.Y); hit && !u.Sel[index] {
		u.Sel = [ShapesMax]bool{}
		b.selSet(u, index, true)
		u.Selected = index
	}
	if b.selCount(u) == 0 {
		u.note("right-click a shape to lock or unlock it")
		return ""
	}
	if b.selLinked(u) {
		b.unlock(u)
		return "unlock"
	}
	b.lock(u)
	return "lock"
}

// Does any selected shape have a link: a joined line end, a linked box corner,
// or a line riding the box?
func (b *Board) selLinked(u *User) bool {
	for i := range b.Shapes {
		s := &b.Shapes[i]
		if !u.Sel[i] || s.Kind == None {
			continue
		}
		for _, j := range s.Joint {
			if isConnector(s.Kind) && j >= 0 {
				return true
			}
		}
		for _, j := range s.Corner {
			if s.Kind == Box && j >= 0 {
				return true
			}
		}
		for j := range b.Joints {
			if b.Joints[j].Active && b.Joints[j].Box == int32(i) {
				return true
			}
		}
	}
	return false
}

// L: every vertex of the selection links to whatever overlaps it. A line end
// joins a link node, a box port or other free line ends there; a box corner
// joins other boxes' corners; free line ends on the box's ports link to it.
func (b *Board) lock(u *User) {
	const eps = JointEps * JointEps
	linked := 0
	for i := range b.Shapes {
		if !u.Sel[i] {
			continue
		}
		s := &b.Shapes[i]
		switch s.Kind {
		case Line, Arrow:
			for end := 0; end < 2; end++ {
				if !endIsFree(s, end) {
					continue
				}
				if b.autoLink(int32(i), end) {
					linked++
					continue
				}
				// other free line ends here: a new free link node for them all
				ex, ey := b.endPoint(s, end)
				others := 0
				for oi := range b.Shapes {
					o := &b.Shapes[oi]
					if !isConnector(o.Kind) {
						continue
					}
					for e := 0; e < 2; e++ {
						if (oi == i && e == end) || !endIsFree(o, e) {
							continue
						}
						if ox, oy := b.endPoint(o, e); dist2(ox, oy, ex, ey) <= eps {
							others++
						}
					}
				}
				if others == 0 {
					continue
				}
				j := b.jointNew()
				if j < 0 {
					continue
				}
				b.Joints[j].X, b.Joints[j].Y = ex, ey
				for oi := range b.Shapes {
					o := &b.Shapes[oi]
					if !isConnector(o.Kind) {
						continue
					}
					for e := 0; e < 2; e++ {
						if !endIsFree(o, e) {
							continue
						}
						if ox, oy := b.endPoint(o, e); dist2(ox, oy, ex, ey) <= eps {
							setEnd(o, e, ex, ey)
							o.Joint[e] = j
							linked++
						}
					}
				}
			}
		case Box:
			for k := 0; k < 4; k++ {
				if s.Corner[k] < 0 && b.lockCorner(int32(i), k) {
					linked++
				}
			}
			for a := uint8(0); a < PortCount; a++ {
				px, py := anchorPoint(s, a)
				for oi := range b.Shapes {
					o := &b.Shapes[oi]
					if !isConnector(o.Kind) {
						continue
					}
					for e := 0; e < 2; e++ {
						if !endIsFree(o, e) {
							continue
						}
						if ox, oy := b.endPoint(o, e); dist2(ox, oy, px, py) <= eps && b.autoLink(int32(oi), e) {
							linked++
						}
					}
				}
			}
		}
	}
	if linked > 0 {
		u.note(fmt.Sprintf("locked %d node%s", linked, plural(linked)))
	} else {
		u.note("nothing overlaps to lock")
	}
}

// Link box corner k to what is on it: an existing link node (a box-driven one
// at another box's corner becomes a shared corner node), else other boxes'
// free corners there, in a new node.
func (b *Board) lockCorner(bi int32, k int) bool {
	s := &b.Shapes[bi]
	px, py := cornerPos(s, k)
	const eps = JointEps * JointEps
	for j := range b.Joints {
		jt := &b.Joints[j]
		if !jt.Active {
			continue
		}
		jx, jy := b.jointPos(int32(j))
		if dist2(jx, jy, px, py) > eps {
			continue
		}
		if jt.Box >= 0 {
			if jt.Box == bi || jt.Anchor >= 4 {
				return false // our own, or a midpoint
			}
			if b.Shapes[jt.Box].Corner[jt.Anchor] >= 0 {
				return false
			}
			b.Shapes[jt.Box].Corner[jt.Anchor] = int32(j)
			jt.Box = -1
		}
		jt.X, jt.Y = jx, jy
		setCorner(s, k, jx, jy)
		s.Corner[k] = int32(j)
		return true
	}
	j := int32(-1)
	for oi := range b.Shapes {
		o := &b.Shapes[oi]
		if o.Kind != Box || int32(oi) == bi {
			continue
		}
		for m := 0; m < 4; m++ {
			if o.Corner[m] >= 0 {
				continue
			}
			ox, oy := cornerPos(o, m)
			if dist2(ox, oy, px, py) > eps {
				continue
			}
			if j < 0 {
				if j = b.jointNew(); j < 0 {
					return false
				}
				b.Joints[j].X, b.Joints[j].Y = px, py
			}
			setCorner(o, m, px, py)
			o.Corner[m] = j
		}
	}
	if j < 0 {
		return false
	}
	s.Corner[k] = j
	return true
}

// U: the selection lets go of every link: line ends leave their nodes, box
// corners leave theirs, and lines riding a box are freed where they stand.
func (b *Board) unlock(u *User) {
	freed := 0
	for i := range b.Shapes {
		if !u.Sel[i] {
			continue
		}
		s := &b.Shapes[i]
		switch s.Kind {
		case Line, Arrow:
			for end := 0; end < 2; end++ {
				j := s.Joint[end]
				if j < 0 {
					continue
				}
				x, y := b.endPoint(s, end)
				s.Joint[end] = -1
				setEnd(s, end, x, y)
				b.jointGC(j)
				freed++
			}
		case Box:
			for k := range s.Corner {
				j := s.Corner[k]
				if j < 0 {
					continue
				}
				s.Corner[k] = -1
				b.jointGC(j)
				freed++
			}
			for j := range b.Joints {
				if jt := &b.Joints[j]; jt.Active && jt.Box == int32(i) {
					freed += b.jointDissolve(int32(j)) - 1
				}
			}
		}
	}
	if freed > 0 {
		u.note(fmt.Sprintf("unlocked %d node%s", freed, plural(freed)))
	} else {
		u.note("nothing to unlock")
	}
}

// ─── Laser ──────────────────────────────────────────────────────────────────
//
// Smoothing: keep a point only LaserStep from the last, then join the kept
// points with quadratics between their midpoints, each kept point the control.
// The curve is C1-smooth and costs one short path per kept point, sent once.

func (b *Board) laserPoint(u *User, x, y int32) {
	if u.laserN > 0 {
		last := u.laser[u.laserN-1]
		if dist2(x, y, last[0], last[1]) < LaserStep*LaserStep {
			return
		}
	}
	if u.laserN == 3 {
		u.laser[0], u.laser[1] = u.laser[1], u.laser[2]
		u.laserN = 2
	}
	u.laser[u.laserN] = [2]int32{x, y}
	u.laserN++
	if u.laserN < 3 {
		return
	}
	p0, p1, p2 := u.laser[0], u.laser[1], u.laser[2]
	ax, ay := (p0[0]+p1[0])/2, (p0[1]+p1[1])/2
	if u.laserSeg == 0 {
		ax, ay = p0[0], p0[1] // the stroke starts at its first point
	}
	b.laserEmit(u, ax, ay, p1[0], p1[1], (p1[0]+p2[0])/2, (p1[1]+p2[1])/2)
}

// The stroke's last piece runs from the last midpoint to the last point; a dot
// if the stroke never moved.
func (b *Board) laserFinish(u *User) {
	switch u.laserN {
	case 1:
		p := u.laser[0]
		b.laserEmit(u, p[0], p[1], p[0], p[1], p[0]+1, p[1])
	case 2:
		if p0, p1 := u.laser[0], u.laser[1]; u.laserSeg == 0 {
			b.laserEmit(u, p0[0], p0[1], p0[0], p0[1], p1[0], p1[1])
		}
	case 3:
		p1, p2 := u.laser[1], u.laser[2]
		b.laserEmit(u, (p1[0]+p2[0])/2, (p1[1]+p2[1])/2, p2[0], p2[1], p2[0], p2[1])
	}
	u.laserN = 0
}

func (b *Board) laserEmit(u *User, ax, ay, cx, cy, bx, by int32) {
	slot := 0 // a free slot, else the oldest segment
	for i := range b.Lasers {
		if !b.Lasers[i].Active {
			slot = i
			break
		}
		if b.Lasers[i].Born.Before(b.Lasers[slot].Born) {
			slot = i
		}
	}
	b.laserID++
	b.Lasers[slot] = LaserSeg{
		Active: true, Hue: u.Hue, ID: b.laserID, Born: time.Now(),
		AX: ax, AY: ay, CX: cx, CY: cy, BX: bx, BY: by,
	}
	u.laserSeg++
}
