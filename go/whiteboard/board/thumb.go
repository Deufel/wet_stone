package board

import (
	"strconv"
	"strings"
)

// Thumb draws a saved drawing as the inner markup of a small SVG on the
// board's own 1000×1000 viewBox: every thumbnail shares one scale, so a run
// of them reads as small multiples (Tufte), and change shows as change. No
// labels, no fills, one weight of ink: the shapes and nothing else.
//
// It is ONE <path>, however many shapes: the history panel holds dozens of
// thumbnails and is re-sent on change, and an element per shape made it
// thousands of nodes to morph (40 thumbnails of a 134-shape board: 261 KB).
func Thumb(snap Snapshot) string {
	b := New()
	b.Restore(snap)
	var sb strings.Builder
	n := func(v int32) { sb.WriteString(strconv.Itoa(int(v))) }
	sb.WriteString(`<path d="`)
	for i := range b.Shapes {
		s := &b.Shapes[i]
		switch s.Kind {
		case Box: // M x y h w v h h -w z
			sb.WriteByte('M')
			n(min(s.X1, s.X2))
			sb.WriteByte(' ')
			n(min(s.Y1, s.Y2))
			sb.WriteByte('h')
			n(abs32(s.X2 - s.X1))
			sb.WriteByte('v')
			n(abs32(s.Y2 - s.Y1))
			sb.WriteByte('h')
			n(-abs32(s.X2 - s.X1))
			sb.WriteByte('z')
		case Pen: // its points, joined
			for k := 0; k+1 < len(s.Points); k += 2 {
				if k == 0 {
					sb.WriteByte('M')
				} else {
					sb.WriteByte('L')
				}
				n(s.Points[k])
				sb.WriteByte(' ')
				n(s.Points[k+1])
			}
		case Line, Arrow: // M x1 y1 L x2 y2
			ax, ay := b.endPoint(s, 0)
			bx, by := b.endPoint(s, 1)
			sb.WriteByte('M')
			n(ax)
			sb.WriteByte(' ')
			n(ay)
			sb.WriteByte('L')
			n(bx)
			sb.WriteByte(' ')
			n(by)
		}
	}
	sb.WriteString(`"/>`)
	return sb.String()
}
