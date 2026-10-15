package board

// Snapshot is the drawing and nothing else: shapes, link nodes and group
// colors. Viewers, selections, drags and laser strokes are never in it, so they
// never reach the store.
type Snapshot struct {
	Shapes []SavedShape
	Joints []SavedJoint
	Groups []SavedGroup
}

type SavedShape struct {
	Idx            int32
	Kind           Kind
	Hue            uint8
	X1, Y1, X2, Y2 int32
	Joint          [2]int32
	Corner         [4]int32
	Group          int32
	Label          string
}

type SavedJoint struct {
	Idx    int32
	Box    int32
	Anchor uint8
	X, Y   int32
}

type SavedGroup struct {
	Idx int32
	Hue uint8
}

func (b *Board) Snapshot() Snapshot {
	b.mu.RLock()
	defer b.mu.RUnlock()
	var snap Snapshot
	var used [ShapesMax]bool
	for i := range b.Shapes {
		s := &b.Shapes[i]
		if s.Kind == None {
			continue
		}
		snap.Shapes = append(snap.Shapes, SavedShape{
			Idx: int32(i), Kind: s.Kind, Hue: s.Hue,
			X1: s.X1, Y1: s.Y1, X2: s.X2, Y2: s.Y2,
			Joint: s.Joint, Corner: s.Corner, Group: s.Group, Label: s.Label,
		})
		if s.Group >= 0 {
			used[s.Group] = true
		}
	}
	for j := range b.Joints {
		if jt := &b.Joints[j]; jt.Active {
			snap.Joints = append(snap.Joints, SavedJoint{Idx: int32(j), Box: jt.Box, Anchor: jt.Anchor, X: jt.X, Y: jt.Y})
		}
	}
	for g := range used {
		if used[g] {
			snap.Groups = append(snap.Groups, SavedGroup{Idx: int32(g), Hue: b.GroupHue[g]})
		}
	}
	return snap
}

// Restore loads a saved drawing into an empty board. Rows outside the fixed
// tables are skipped rather than trusted.
func (b *Board) Restore(snap Snapshot) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, s := range snap.Shapes {
		if s.Idx < 0 || s.Idx >= ShapesMax || s.Kind == None || s.Kind > Arrow {
			continue
		}
		b.Shapes[s.Idx] = Shape{
			Kind: s.Kind, Hue: s.Hue, X1: s.X1, Y1: s.Y1, X2: s.X2, Y2: s.Y2,
			Joint: s.Joint, Corner: s.Corner, Group: s.Group, Label: s.Label,
		}
	}
	for _, j := range snap.Joints {
		if j.Idx < 0 || j.Idx >= JointsMax {
			continue
		}
		b.Joints[j.Idx] = Joint{Active: true, Box: j.Box, Anchor: j.Anchor, X: j.X, Y: j.Y}
	}
	for _, g := range snap.Groups {
		if g.Idx >= 0 && g.Idx < ShapesMax {
			b.GroupHue[g.Idx] = g.Hue
		}
	}
}
