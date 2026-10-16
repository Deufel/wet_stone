// Package board is the whiteboard's model: shapes, link nodes (joints), and
// every viewer's cursor, selection and drag. It is a port of the Odin
// version's board isolate. A Board is guarded by one mutex: commands lock it,
// renders read-lock it. Nothing here does IO; the hub persists and publishes.
//
// Joint positions are derived at read time (derive, never copy): a line end in
// a joint reads the joint, and a joint a box drives reads the box's anchor.
//
// Color never appears here. Shapes carry a --hue-shift step, the selection
// carries --fg, and mike.css paints the SVG through its currentColor bridge.
package board

import (
	"sync"
	"time"
)

const (
	W           = 1000
	H           = 1000 // a square board's height (the boards before letter)
	Letter      = 1294 // a letter page's height: 1000 × 11 / 8.5
	Grid        = 20
	SnapAnchor  = 14 // px: snap a dragged point to another shape's anchor
	SnapAxis    = 10 // px: snap a dragged point to its own shape's other end
	HandleR     = 9  // px: hit radius for corners, endpoints and link targets
	LineHit     = 7  // px: hit distance for a line's body
	ArrowLen    = 30 // an arrow head: open, a triangle this long
	ArrowHalf   = 13 // and half this wide
	ShapesMax   = 400
	JointsMax   = 400
	JointEps    = 2 // px: nodes this close count as the same coordinate
	UsersMax    = 64
	LabelMax    = 48
	HueStep     = 60
	AnchorCount = 9  // box anchors: 4 corners, 4 edge midpoints, center
	PortCount   = 8  // anchors 0..7, the corners and edge midpoints, are a box's ports
	SnapPort    = 20 // px: a dragged connector end snaps to a port or a link node from this far
	ExtentsPad  = 8  // px: the dotted outline sits this far outside a multi-selection

	LaserMax  = 160 // live laser segments on a board, all users
	LaserStep = 8   // px: a laser point is kept only this far from the last one
	LaserTTL  = 1500 * time.Millisecond
	UserTTL   = 12 * time.Second // a viewer with no stream and no command leaves

	snapBias = 2000 // a multiple of Grid larger than any delta on the board
)

type Kind uint8

const (
	None Kind = iota
	Box
	Line
	Arrow
	Pen // a freehand stroke that stays: its points, its box their extent
)

type Shape struct {
	Kind           Kind
	Hue            uint8
	X1, Y1, X2, Y2 int32    // a connector end's own position, used while it is free
	Joint          [2]int32 // connectors: the joint each end belongs to, -1 = free
	Corner         [4]int32 // boxes: the joint each corner belongs to, -1 = free; corner c is x2 if c&1, y2 if c>=2
	Group          int32    // -1 = none; members share a color and are selected together
	Label          string
	Points         []int32 // pen: x, y pairs in board units; X1..Y2 hold their extent
	Ink            Ink     // a teacher's ink (hue, strength, meaning); zero: the board's own
	Turn           Turn    // how it is turned: rotated, flipped, scaled; zero: as drawn
}

// Turn is a shape's motion state, drawn as a CSS transform about its centre
// (with a transition, so a change animates on every page). Rot is degrees and
// keeps counting (a spin is +360: it goes round, and ends where it began).
type Turn struct {
	Rot    int32 `json:",omitempty"`
	FX, FY bool  `json:",omitempty"`
	Scale  int16 `json:",omitempty"` // percent; 0 = 100
}

// Turns are the motion commands, by name.
var Turns = map[string]bool{"cw45": true, "ccw45": true, "cw30": true, "ccw30": true}

func (t Turn) apply(op string) Turn {
	sc := int32(t.Scale)
	if sc == 0 {
		sc = 100
	}
	switch op {
	case "cw45":
		t.Rot += 45
	case "ccw45":
		t.Rot -= 45
	case "cw30":
		t.Rot += 30
	case "ccw30":
		t.Rot -= 30
	case "flipx":
		t.FX = !t.FX
	case "flipy":
		t.FY = !t.FY
	case "grow":
		sc = min(sc*5/4, 400)
	case "shrink":
		sc = max(sc*4/5, 25)
	case "unturn": // back as drawn, the short way round
		t = Turn{Rot: (t.Rot + 180) / 360 * 360}
		sc = 100
	}
	if sc == 100 {
		sc = 0
	}
	t.Scale = int16(sc)
	return t
}

// Ink is a shape's ink as the engine reads it (mike.css): an absolute hue
// (--hue-lock, degrees + 1; 0 = the board's own hue), a strength (--fg in
// tenths, -10..10; 0 = the resting ink) and a meaning (one of the semantic
// classes: .inf .suc .wrn .dgr, which lock their own hue).
type Ink struct {
	Hue int16 `json:",omitempty"`
	Fg  int8  `json:",omitempty"`
	Sem uint8 `json:",omitempty"`
}

// Semantics are the semantic classes, by Ink.Sem (0: none).
var Semantics = [...]string{"", "inf", "suc", "wrn", "dgr"}

// SemOf is a semantic class's Ink.Sem, or 0.
func SemOf(class string) uint8 {
	for i, s := range Semantics {
		if s != "" && s == class {
			return uint8(i)
		}
	}
	return 0
}

// PenPointsMax caps one stroke (pairs).
const PenPointsMax = 1500

// penBounds sets a pen stroke's box to the extent of its points.
func penBounds(s *Shape) {
	if len(s.Points) < 2 {
		return
	}
	s.X1, s.Y1, s.X2, s.Y2 = s.Points[0], s.Points[1], s.Points[0], s.Points[1]
	for i := 2; i+1 < len(s.Points); i += 2 {
		s.X1, s.X2 = min(s.X1, s.Points[i]), max(s.X2, s.Points[i])
		s.Y1, s.Y2 = min(s.Y1, s.Points[i+1]), max(s.Y2, s.Points[i+1])
	}
}

// penShift moves a pen stroke by (dx, dy).
func penShift(s *Shape, dx, dy int32) {
	for i := 0; i+1 < len(s.Points); i += 2 {
		s.Points[i] += dx
		s.Points[i+1] += dy
	}
	s.X1, s.X2, s.Y1, s.Y2 = s.X1+dx, s.X2+dx, s.Y1+dy, s.Y2+dy
}

// penNear reports whether (x, y) is within LineHit of the stroke.
func penNear(s *Shape, x, y int32) bool {
	p := s.Points
	if len(p) == 2 {
		return dist2(x, y, p[0], p[1]) <= LineHit*LineHit
	}
	for i := 0; i+3 < len(p); i += 2 {
		if segDist2(x, y, p[i], p[i+1], p[i+2], p[i+3]) <= LineHit*LineHit {
			return true
		}
	}
	return false
}

// One shared position for every node linked at a coordinate.
type Joint struct {
	Active bool
	Box    int32 // box whose anchor drives the joint, -1 = free point
	Anchor uint8
	X, Y   int32 // position while free
}

type DragKind uint8

const (
	DragNone DragKind = iota
	DragWhole
	DragHandle
	DragGroup   // the whole selection moves
	DragMarquee // a selection rectangle from (GX, GY)
	DragLaser   // a fading pointer stroke
	DragPen     // a pen stroke being drawn
)

type Drag struct {
	Kind   DragKind
	Shape  int32
	HX, HY uint8 // Handle: which coordinates move. Whole connector: HX = reference end
	GX, GY int32 // Whole: grab offset from the reference point
	Fresh  bool  // a shape being drawn: both of its ends are new
	AX, AY int32 // Group: the grid delta applied so far
	Toggle int32 // Group: the shape a click without movement deselects, -1 = none
	Moved  bool  // Group: the pointer has really moved
	Label  bool  // the press was on the shape's label text: let go here, it is a click that edits it
	PX, PY int32 // where the press was
	Code   bool  // the code tool's box: let go, it opens as a code block
}

// One smoothed piece of a laser stroke: a quadratic from a through control c
// to b. It is rendered once under a stable id and never changes, so the morph
// only appends it, and every page fades it with its own clock.
type LaserSeg struct {
	Active                 bool
	Hue                    uint8
	ID                     uint32
	Born                   time.Time
	AX, AY, CX, CY, BX, BY int32
}

type User struct {
	Active   bool
	UID      uint32
	Hue      uint8
	X, Y     int32
	Selected int32 // the selection's primary, the label target, or -1
	Last     int32 // the shape they drew last (a pen stroke is not selected), or -1
	Drag     Drag
	Sel      [ShapesMax]bool // the selection
	EditSeq  uint32          // bumped to put the page in the label editor
	GuideVX  int32           // vertical guide at x, -1 = none
	GuideHY  int32           // horizontal guide at y, -1 = none
	AnchorOn bool
	AnchorX  int32
	AnchorY  int32
	LastSeen time.Time
	Name     string // shown beside this viewer's cursor on everyone else's board
	Note     string // one-line feedback for this user ($note)
	NoteSeq  uint32

	laser    [3][2]int32 // the last kept laser points
	laserN   int
	laserSeg int   // segments emitted in this stroke
	streams  int   // open streams for this user; present while > 0
	lastSeq  int64 // the newest pointer command applied
	downSeq  int64 // the Seq of the current stroke's down
	upSeq    int64 // the Seq of its up, once applied (0 = the stroke is open)
	heldUp   Cmd   // an up that arrived before its down
}

type Board struct {
	mu       sync.RWMutex
	Shapes   [ShapesMax]Shape
	Joints   [JointsMax]Joint
	Users    [UsersMax]User
	GroupHue [ShapesMax]uint8 // by group id
	Lasers   [LaserMax]LaserSeg
	nextHue  uint8
	laserID  uint32

	// Height is the board's in units: H (square) or Letter (a book's
	// whiteboard, shaped as the page it becomes). Set once, as it loads.
	Height int32
}

func New() *Board {
	b := &Board{Height: H}
	for i := range b.Shapes {
		b.Shapes[i].Joint = [2]int32{-1, -1}
		b.Shapes[i].Corner = [4]int32{-1, -1, -1, -1}
		b.Shapes[i].Group = -1
	}
	return b
}

// ─── Geometry: anchors, joints, effective end points ────────────────────────

func isConnector(k Kind) bool { return k == Line || k == Arrow }

// The nine anchors of a box: TL TR BL BR, top mid, bottom mid, left mid, right mid, center.
func anchorPoint(s *Shape, index uint8) (int32, int32) {
	x1, x2 := min(s.X1, s.X2), max(s.X1, s.X2)
	y1, y2 := min(s.Y1, s.Y2), max(s.Y1, s.Y2)
	mx, my := (x1+x2)/2, (y1+y2)/2
	switch index {
	case 0:
		return x1, y1
	case 1:
		return x2, y1
	case 2:
		return x1, y2
	case 3:
		return x2, y2
	case 4:
		return mx, y1
	case 5:
		return mx, y2
	case 6:
		return x1, my
	case 7:
		return x2, my
	}
	return mx, my
}

func (b *Board) jointPos(j int32) (int32, int32) {
	jt := &b.Joints[j]
	if jt.Box >= 0 && b.Shapes[jt.Box].Kind == Box {
		return anchorPoint(&b.Shapes[jt.Box], jt.Anchor)
	}
	return jt.X, jt.Y
}

// Where a connector's end is: its joint's position if joined, else its own.
func (b *Board) endPoint(s *Shape, end int) (int32, int32) {
	if j := s.Joint[end]; j >= 0 && b.Joints[j].Active {
		return b.jointPos(j)
	}
	if end == 0 {
		return s.X1, s.Y1
	}
	return s.X2, s.Y2
}

func setEnd(s *Shape, end int, x, y int32) {
	if end == 0 {
		s.X1, s.Y1 = x, y
	} else {
		s.X2, s.Y2 = x, y
	}
}

func endIsFree(s *Shape, end int) bool { return s.Joint[end] < 0 }

// A joint a box drives, or box corners share, cannot be dragged by a line end.
func (b *Board) jointPinned(j int32) bool {
	return b.Joints[j].Box >= 0 || b.jointHasCorners(j)
}

func (b *Board) jointHasCorners(j int32) bool {
	for i := range b.Shapes {
		if b.Shapes[i].Kind == Box && boxInJoint(&b.Shapes[i], j) {
			return true
		}
	}
	return false
}

func boxInJoint(s *Shape, j int32) bool {
	for _, c := range s.Corner {
		if c == j {
			return true
		}
	}
	return false
}

func cornerPos(s *Shape, c int) (x, y int32) {
	x, y = s.X1, s.Y1
	if c&1 == 1 {
		x = s.X2
	}
	if c >= 2 {
		y = s.Y2
	}
	return
}

func setCorner(s *Shape, c int, x, y int32) {
	if c&1 == 1 {
		s.X2 = x
	} else {
		s.X1 = x
	}
	if c >= 2 {
		s.Y2 = y
	} else {
		s.Y1 = y
	}
}

// After box start changed shape, each of its linked corners carries its joint
// and every other box corner in that joint takes the same position: a shared
// vertex. That box may move further linked corners, so it repeats, bounded,
// since boxes can link in a cycle.
func (b *Board) cornersSettle(start int32) {
	var queue [64]int32
	head, tail := 0, 1
	queue[0] = start
	for head < tail {
		bi := queue[head]
		head++
		s := &b.Shapes[bi]
		for c := 0; c < 4; c++ {
			j := s.Corner[c]
			if j < 0 {
				continue
			}
			cx, cy := cornerPos(s, c)
			jt := &b.Joints[j]
			if jt.X == cx && jt.Y == cy {
				continue
			}
			jt.X, jt.Y = cx, cy
			for oi := range b.Shapes {
				o := &b.Shapes[oi]
				if o.Kind != Box || int32(oi) == bi {
					continue
				}
				for k := 0; k < 4; k++ {
					if o.Corner[k] != j {
						continue
					}
					if ox, oy := cornerPos(o, k); ox == cx && oy == cy {
						continue
					}
					setCorner(o, k, cx, cy)
					if tail < len(queue) {
						queue[tail] = int32(oi)
						tail++
					}
				}
			}
		}
	}
}

func (b *Board) jointNew() int32 {
	for i := range b.Joints {
		if !b.Joints[i].Active {
			b.Joints[i] = Joint{Active: true, Box: -1}
			return int32(i)
		}
	}
	return -1
}

// Dissolve a joint: every member keeps the position it had.
func (b *Board) jointDissolve(j int32) (freed int) {
	x, y := b.jointPos(j)
	for i := range b.Shapes {
		s := &b.Shapes[i]
		if s.Kind == Box {
			for c := range s.Corner {
				if s.Corner[c] == j {
					s.Corner[c] = -1
					freed++
				}
			}
			continue
		}
		if !isConnector(s.Kind) {
			continue
		}
		for end := 0; end < 2; end++ {
			if s.Joint[end] == j {
				s.Joint[end] = -1
				setEnd(s, end, x, y)
				freed++
			}
		}
	}
	if b.Joints[j].Box >= 0 {
		freed++
	}
	b.Joints[j].Active = false
	return
}

// A joint needs two members to mean anything.
func (b *Board) jointGC(j int32) {
	if j < 0 || !b.Joints[j].Active {
		return
	}
	members := 0
	if b.Joints[j].Box >= 0 {
		members++
	}
	for i := range b.Shapes {
		s := &b.Shapes[i]
		if s.Kind == Box {
			for _, c := range s.Corner {
				if c == j {
					members++
				}
			}
			continue
		}
		if !isConnector(s.Kind) {
			continue
		}
		for end := 0; end < 2; end++ {
			if s.Joint[end] == j {
				members++
			}
		}
	}
	if members < 2 {
		b.jointDissolve(j)
	}
}

// Remove a shape and keep the joint table consistent.
func (b *Board) shapeRemove(index int32) {
	s := &b.Shapes[index]
	switch s.Kind {
	case Box:
		// its corners leave their joints; joints it drove become free points where they stand
		for c := range s.Corner {
			j := s.Corner[c]
			if j < 0 {
				continue
			}
			s.Corner[c] = -1
			s.Kind = None
			b.jointGC(j)
			s.Kind = Box
		}
		for j := range b.Joints {
			jt := &b.Joints[j]
			if jt.Active && jt.Box == index {
				jt.X, jt.Y = b.jointPos(int32(j))
				jt.Box = -1
				s.Kind = None
				b.jointGC(int32(j))
				s.Kind = Box
			}
		}
	case Line, Arrow:
		for end := 0; end < 2; end++ {
			j := s.Joint[end]
			if j >= 0 {
				x, y := b.jointPos(j)
				setEnd(s, end, x, y) // so a dissolved neighbour stays put
				s.Joint[end] = -1
				s.Kind = None
				b.jointGC(j)
			}
		}
	}
	s.Kind = None
	for ui := range b.Users {
		u := &b.Users[ui]
		u.Sel[index] = false
		if u.Active && u.Selected == index {
			u.Selected = -1
			u.Drag = Drag{}
		}
	}
}

func (b *Board) shapeNew(kind Kind, x, y int32) int32 {
	for i := range b.Shapes {
		if b.Shapes[i].Kind == None {
			b.Shapes[i] = Shape{
				Kind: kind, Hue: uint8(i % 6),
				X1: x, Y1: y, X2: x, Y2: y,
				Joint:  [2]int32{-1, -1},
				Corner: [4]int32{-1, -1, -1, -1},
				Group:  -1,
			}
			return int32(i)
		}
	}
	return -1
}

func (b *Board) shapeHue(s *Shape) uint8 {
	if s.Group >= 0 {
		return b.GroupHue[s.Group]
	}
	return s.Hue
}

// ─── Users and notes ────────────────────────────────────────────────────────

func (b *Board) userFor(uid uint32) *User {
	now := time.Now()
	for i := range b.Users {
		u := &b.Users[i]
		if u.Active && u.UID == uid {
			u.LastSeen = now
			return u
		}
	}
	for i := range b.Users {
		u := &b.Users[i]
		if !u.Active {
			*u = User{
				Active: true, UID: uid, Hue: b.nextHue, Selected: -1, Last: -1,
				GuideVX: -1, GuideHY: -1, LastSeen: now,
			}
			b.nextHue = (b.nextHue + 1) % 6
			return u
		}
	}
	return nil
}

func (b *Board) viewer(uid uint32) *User {
	for i := range b.Users {
		if b.Users[i].Active && b.Users[i].UID == uid {
			return &b.Users[i]
		}
	}
	return nil
}

func (u *User) note(text string) {
	if len(text) > 40 {
		text = text[:40]
	}
	u.Note = text
	u.NoteSeq++
}

func (u *User) clearGuides() {
	u.GuideVX, u.GuideHY, u.AnchorOn = -1, -1, false
}

// ─── Arithmetic ─────────────────────────────────────────────────────────────

func abs32(v int32) int32 {
	if v < 0 {
		return -v
	}
	return v
}

func snapGrid(v int32) int32 {
	if v < 0 {
		return 0
	}
	return ((v + Grid/2) / Grid) * Grid
}

// snapGrid for a signed delta (snapGrid clamps negatives to 0).
func snapDelta(d int32) int32 { return snapGrid(d+snapBias) - snapBias }

func dist2(ax, ay, bx, by int32) int64 {
	dx, dy := int64(ax-bx), int64(ay-by)
	return dx*dx + dy*dy
}

// squared distance from p to segment ab
func segDist2(px, py, ax, ay, bx, by int32) int64 {
	vx, vy := int64(bx-ax), int64(by-ay)
	wx, wy := int64(px-ax), int64(py-ay)
	len2 := vx*vx + vy*vy
	if len2 == 0 {
		return wx*wx + wy*wy
	}
	t := wx*vx + wy*vy
	if t <= 0 {
		return wx*wx + wy*wy
	}
	if t >= len2 {
		return dist2(px, py, bx, by)
	}
	cx := int64(ax) + vx*t/len2
	cy := int64(ay) + vy*t/len2
	dx, dy := int64(px)-cx, int64(py)-cy
	return dx*dx + dy*dy
}

// Do segments ab and cd properly cross?
func segCross(ax, ay, bx, by, cx, cy, dx, dy int32) bool {
	side := func(ax, ay, bx, by, px, py int32) int64 {
		v := int64(bx-ax)*int64(py-ay) - int64(by-ay)*int64(px-ax)
		switch {
		case v > 0:
			return 1
		case v < 0:
			return -1
		}
		return 0
	}
	return side(cx, cy, dx, dy, ax, ay)*side(cx, cy, dx, dy, bx, by) < 0 &&
		side(ax, ay, bx, by, cx, cy)*side(ax, ay, bx, by, dx, dy) < 0
}

func clamp32(v, lo, hi int32) int32 { return min(max(v, lo), hi) }
