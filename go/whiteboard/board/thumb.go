package board

import (
	"strconv"
	"strings"
)

// Thumb draws a saved drawing as the inner markup of a small SVG on the
// board's own 1000×1000 viewBox: every thumbnail shares one scale, so a run
// of them reads as small multiples (Tufte), and change shows as change. No
// labels, no fills, one weight of ink: the shapes and nothing else.
func Thumb(snap Snapshot) string {
	b := New()
	b.Restore(snap)
	var sb strings.Builder
	n := func(v int32) { sb.WriteString(strconv.Itoa(int(v))) }
	for i := range b.Shapes {
		s := &b.Shapes[i]
		switch s.Kind {
		case Box:
			sb.WriteString(`<rect x="`)
			n(min(s.X1, s.X2))
			sb.WriteString(`" y="`)
			n(min(s.Y1, s.Y2))
			sb.WriteString(`" width="`)
			n(abs32(s.X2 - s.X1))
			sb.WriteString(`" height="`)
			n(abs32(s.Y2 - s.Y1))
			sb.WriteString(`"/>`)
		case Line, Arrow:
			ax, ay := b.endPoint(s, 0)
			bx, by := b.endPoint(s, 1)
			sb.WriteString(`<line x1="`)
			n(ax)
			sb.WriteString(`" y1="`)
			n(ay)
			sb.WriteString(`" x2="`)
			n(bx)
			sb.WriteString(`" y2="`)
			n(by)
			sb.WriteString(`"/>`)
		}
	}
	return sb.String()
}
