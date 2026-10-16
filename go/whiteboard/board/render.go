package board

import (
	"encoding/json"
	"html"
	"math"
	"strconv"
)

// Sent is what one stream last told its page, so signals go down only when
// they change (the frame itself always goes; the stream's Brotli window makes a
// near-identical frame cost about its delta).
type Sent struct {
	Selected int32
	NoteSeq  uint32
	Edit     uint32
}

func NewSent() Sent { return Sent{Selected: -2} }

// Frame renders the whole <svg id="board"> for one viewer, and the signals that
// changed for it since sent.
func (b *Board) Frame(uid uint32, sent *Sent) (frame string, signals []byte) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	v := b.viewer(uid)
	return b.render(v, uid), b.signals(v, sent)
}

type writer struct{ buf []byte }

func (w *writer) s(s string) { w.buf = append(w.buf, s...) }
func (w *writer) i(v int32)  { w.buf = strconv.AppendInt(w.buf, int64(v), 10) }

func (w *writer) attr(name string, v int32) {
	w.s(" ")
	w.s(name)
	w.s(`="`)
	w.i(v)
	w.s(`"`)
}

func (w *writer) hue(h uint8) {
	w.s(` style="--hue-shift:`)
	w.i(int32(h) * HueStep)
	w.s(`"`)
}

func (w *writer) circle(cx, cy, r int32, extra string) {
	w.s("<circle")
	w.attr("cx", cx)
	w.attr("cy", cy)
	w.attr("r", r)
	w.s(extra)
	w.s("/>")
}

// a label as a foreignObject the engine types, centered in the given box
// penPath writes a pen stroke as path data, smoothed: a quadratic through
// each point to the midpoint of the next (the laser's smoothing).
func (w *writer) penPath(p []int32) {
	if len(p) < 2 {
		return
	}
	w.s("M")
	w.i(p[0])
	w.s(" ")
	w.i(p[1])
	n := len(p) / 2
	if n == 1 {
		w.s("h0") // a dot
		return
	}
	for k := 1; k < n-1; k++ {
		x, y := p[2*k], p[2*k+1]
		w.s(" Q")
		w.i(x)
		w.s(" ")
		w.i(y)
		w.s(" ")
		w.i((x + p[2*k+2]) / 2)
		w.s(" ")
		w.i((y + p[2*k+3]) / 2)
	}
	w.s(" L")
	w.i(p[2*n-2])
	w.s(" ")
	w.i(p[2*n-1])
}

// labelBox is where a shape's label sits, [x, y, w, h] in board units: a
// box's own rectangle, a connector's band across its middle. The page's label
// editor opens over exactly this box.
func (b *Board) labelBox(s *Shape) [4]int32 {
	if s.Kind == Box || s.Kind == Pen {
		x1, x2 := min(s.X1, s.X2), max(s.X1, s.X2)
		y1, y2 := min(s.Y1, s.Y2), max(s.Y1, s.Y2)
		return [4]int32{x1, y1, x2 - x1, y2 - y1}
	}
	ax, ay := b.endPoint(s, 0)
	bx, by := b.endPoint(s, 1)
	return [4]int32{(ax+bx)/2 - 120, (ay+by)/2 - 22, 240, 44}
}

// onLabel reports whether (x, y) is on the shape's label text: centred in its
// label box, about 15 units a character at the 26-unit face, wrapping when
// the box is narrow (the server's estimate; the page needs no hit test).
func (b *Board) onLabel(s *Shape, x, y int32) bool {
	n := int32(len([]rune(s.Label)))
	if n == 0 {
		return false
	}
	box := b.labelBox(s)
	if IsCode(s.Label) { // a code block is all label: a click in it edits
		return x >= box[0] && x <= box[0]+box[2] && y >= box[1] && y <= box[1]+box[3]
	}
	cx, cy := box[0]+box[2]/2, box[1]+box[3]/2
	width := n*15 + 16
	lines := int32(1)
	if inner := box[2] - 16; inner > 0 && width > inner {
		lines = (width + inner - 1) / inner
		width = inner
	}
	halfW, halfH := width/2, min(box[3]/2, lines*15+8)
	return abs32(x-cx) <= halfW && abs32(y-cy) <= halfH
}

func (w *writer) label(s *Shape, box [4]int32) {
	x, y, width, height := box[0], box[1], box[2], box[3]
	if s.Label == "" {
		return
	}
	if IsCode(s.Label) {
		w.code(s, box)
		return
	}
	w.s("<foreignObject")
	w.attr("x", x)
	w.attr("y", y)
	w.attr("width", width)
	w.attr("height", height)
	w.s(`><div xmlns="http://www.w3.org/1999/xhtml" class="label"><span>`)
	w.s(html.EscapeString(s.Label))
	w.s("</span></div></foreignObject>")
}

// A box's eight ports, corners and midpoints alike; a linked port is filled.
func (b *Board) ports(w *writer, box int32) {
	s := &b.Shapes[box]
	w.s(`<g class="ports"`)
	w.hue(b.shapeHue(s))
	w.s(">")
	for a := uint8(0); a < PortCount; a++ {
		px, py := anchorPoint(s, a)
		extra := ` class="port"`
		if b.portLinked(box, a) {
			extra = ` class="port" data-ui-linked`
		}
		w.circle(px, py, 5, extra)
	}
	w.s("</g>")
}

func (b *Board) render(v *User, uid uint32) string {
	w := &writer{buf: make([]byte, 0, 16<<10)}
	selected, count := int32(-1), 0
	if v != nil {
		selected, count = v.Selected, b.selCount(v)
	}

	w.s(`<svg id="board" xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1000 `)
	w.i(b.Height)
	w.s(`">`)

	b.drawShapes(w, v)

	// ports: the selected box's, and the box a dragged connector end is near
	if count == 1 && selected >= 0 && b.Shapes[selected].Kind == Box {
		b.ports(w, selected)
	}
	// a multi-selection's extents, dotted
	if count >= 2 {
		if x1, y1, x2, y2, ok := b.maskExtents(&v.Sel); ok {
			w.s(`<rect class="extents"`)
			w.attr("x", x1-ExtentsPad)
			w.attr("y", y1-ExtentsPad)
			w.attr("width", x2-x1+2*ExtentsPad)
			w.attr("height", y2-y1+2*ExtentsPad)
			w.s("/>")
		}
	}
	if v != nil && v.Drag.Kind == DragHandle && isConnector(b.Shapes[v.Drag.Shape].Kind) {
		const reach = SnapPort * 2
		for i := int32(ShapesMax - 1); i >= 0; i-- {
			s := &b.Shapes[i]
			if s.Kind != Box || i == selected {
				continue
			}
			if v.X >= min(s.X1, s.X2)-reach && v.X <= max(s.X1, s.X2)+reach &&
				v.Y >= min(s.Y1, s.Y2)-reach && v.Y <= max(s.Y1, s.Y2)+reach {
				b.ports(w, i)
				break
			}
		}
	}

	// the viewer's selection handles, for a single connector (a box's handles are its ports)
	if count == 1 && selected >= 0 && b.Shapes[selected].Kind != None {
		s := &b.Shapes[selected]
		w.s(`<g class="handles"`)
		w.hue(b.shapeHue(s))
		w.s(">")
		if isConnector(s.Kind) {
			ax, ay := b.endPoint(s, 0)
			bx, by := b.endPoint(s, 1)
			w.circle(ax, ay, 6, "")
			w.circle(bx, by, 6, "")
		}
		w.s("</g>")
	}

	// the viewer's marquee: dashed while it takes what it touches (dragged
	// down), solid when it takes only what it encloses (dragged up)
	if v != nil && v.Drag.Kind == DragMarquee {
		w.s(`<rect class="marquee`)
		if v.Y < v.Drag.GY {
			w.s(` enclose`)
		}
		w.s(`"`)
		w.attr("x", min(v.Drag.GX, v.X))
		w.attr("y", min(v.Drag.GY, v.Y))
		w.attr("width", abs32(v.X-v.Drag.GX))
		w.attr("height", abs32(v.Y-v.Drag.GY))
		w.s("/>")
	}

	// the viewer's snap guides
	if v != nil && (v.GuideVX >= 0 || v.GuideHY >= 0 || v.AnchorOn) {
		w.s(`<g class="guides inf">`)
		if v.GuideVX >= 0 {
			w.s(`<line class="guide"`)
			w.attr("x1", v.GuideVX)
			w.attr("y1", 0)
			w.attr("x2", v.GuideVX)
			w.attr("y2", b.Height)
			w.s("/>")
		}
		if v.GuideHY >= 0 {
			w.s(`<line class="guide"`)
			w.attr("x1", 0)
			w.attr("y1", v.GuideHY)
			w.attr("x2", W)
			w.attr("y2", v.GuideHY)
			w.s("/>")
		}
		if v.AnchorOn {
			w.circle(v.AnchorX, v.AnchorY, 8, ` class="anchor"`)
		}
		w.s("</g>")
	}

	// laser segments, each under a stable id so the morph only appends them
	w.s(`<g class="lasers">`)
	for i := range b.Lasers {
		seg := &b.Lasers[i]
		if !seg.Active {
			continue
		}
		w.s(`<g class="laser" id="lz`)
		w.buf = strconv.AppendUint(w.buf, uint64(seg.ID), 10)
		w.s(`"`)
		w.hue(seg.Hue)
		w.s(`><path d="M`)
		w.i(seg.AX)
		w.s(" ")
		w.i(seg.AY)
		w.s("Q")
		w.i(seg.CX)
		w.s(" ")
		w.i(seg.CY)
		w.s(" ")
		w.i(seg.BX)
		w.s(" ")
		w.i(seg.BY)
		w.s(`"/></g>`)
	}
	w.s("</g>")

	// everyone else's cursor
	w.s(`<g class="cursors">`)
	for i := range b.Users {
		u := &b.Users[i]
		if !u.Active || u.UID == uid {
			continue
		}
		w.s(`<g class="cursor" transform="translate(`)
		w.i(u.X)
		w.s(",")
		w.i(u.Y)
		w.s(`)"`)
		w.hue(u.Hue)
		w.s(`><path d="M0 0v16l4-3.5 2.5 6 3-1.3-2.6-6H11z"/>`)
		if u.Name != "" {
			w.s(`<foreignObject x="12" y="14" width="220" height="40"><div xmlns="http://www.w3.org/1999/xhtml" class="who"><span>`)
			w.s(html.EscapeString(u.Name))
			w.s(`</span></div></foreignObject>`)
		}
		w.s(`</g>`)
	}
	w.s("</g></svg>")
	return string(w.buf)
}

// The signals for this viewer: counts every frame; selection and label, note,
// and the edit trigger when they change.
func (b *Board) signals(v *User, sent *Sent) []byte {
	shapes, users := 0, 0
	for i := range b.Shapes {
		if b.Shapes[i].Kind != None {
			shapes++
		}
	}
	for i := range b.Users {
		if b.Users[i].Active {
			users++
		}
	}
	m := map[string]any{"shapes": shapes, "users": users}
	if v == nil {
		out, _ := json.Marshal(m)
		return out
	}
	grouped := false
	for i := range v.Sel {
		if v.Sel[i] && b.Shapes[i].Kind != None && b.Shapes[i].Group >= 0 {
			grouped = true
		}
	}
	m["nsel"] = b.selCount(v)
	m["grouped"] = grouped
	if v.EditSeq != sent.Edit {
		// open the page's label editor: over this shape's label box, with
		// its text as it stands
		sent.Edit = v.EditSeq
		m["edit"] = v.EditSeq
		if s := v.Selected; s >= 0 && b.Shapes[s].Kind != None {
			m["lbox"] = b.labelBox(&b.Shapes[s])
			m["label"] = b.Shapes[s].Label
		}
	}
	selected := v.Selected
	if selected >= 0 && b.Shapes[selected].Kind == None {
		selected = -1
	}
	if selected != sent.Selected {
		sent.Selected = selected
		m["sel"] = selected
		m["label"] = ""
		if selected >= 0 {
			m["label"] = b.Shapes[selected].Label
		}
	}
	if v.NoteSeq != sent.NoteSeq {
		sent.NoteSeq = v.NoteSeq
		m["note"] = v.Note
	}
	out, _ := json.Marshal(m)
	return out
}

func b2i(v bool) int {
	if v {
		return 1
	}
	return 0
}

// drawShapes writes the shapes, with their labels and link dots, as the
// viewer v sees them (v nil: nothing selected, as on paper).
func (b *Board) drawShapes(w *writer, v *User) {
	w.s(`<g class="shapes">`)
	for i := range b.Shapes {
		s := &b.Shapes[i]
		if s.Kind == None {
			continue
		}
		w.s("<g")
		w.hue(b.shapeHue(s))
		if v != nil && v.Sel[i] {
			w.s(" data-ui-selected")
		}
		w.s(">")
		switch s.Kind {
		case Box:
			x1, x2 := min(s.X1, s.X2), max(s.X1, s.X2)
			y1, y2 := min(s.Y1, s.Y2), max(s.Y1, s.Y2)
			w.s("<rect")
			w.attr("x", x1)
			w.attr("y", y1)
			w.attr("width", x2-x1)
			w.attr("height", y2-y1)
			w.s(` rx="6"/>`)
			for k := 0; k < 4; k++ {
				if s.Corner[k] >= 0 {
					cx, cy := cornerPos(s, k)
					w.circle(cx, cy, 4, ` class="node" data-ui-linked`)
				}
			}
			w.label(s, b.labelBox(s))
		case Pen:
			w.s(`<path class="pen" d="`)
			w.penPath(s.Points)
			w.s(`"/>`)
			w.label(s, b.labelBox(s))
		case Line, Arrow:
			ax, ay := b.endPoint(s, 0)
			bx, by := b.endPoint(s, 1)
			lx, ly := bx, by // where the stroke ends (short of an arrow head)
			if s.Kind == Arrow {
				dx, dy := float64(bx-ax), float64(by-ay)
				if length := math.Hypot(dx, dy); length > 1 {
					ux, uy := dx/length, dy/length
					baseX, baseY := float64(bx)-ux*ArrowLen, float64(by)-uy*ArrowLen
					lx, ly = int32(baseX), int32(baseY)
					w.s(`<path class="head" d="M`)
					w.i(bx)
					w.s(" ")
					w.i(by)
					w.s(" L")
					w.i(int32(baseX - uy*ArrowHalf))
					w.s(" ")
					w.i(int32(baseY + ux*ArrowHalf))
					w.s(" L")
					w.i(int32(baseX + uy*ArrowHalf))
					w.s(" ")
					w.i(int32(baseY - ux*ArrowHalf))
					w.s(` Z"/>`)
				}
			}
			w.s("<line")
			w.attr("x1", ax)
			w.attr("y1", ay)
			w.attr("x2", lx)
			w.attr("y2", ly)
			w.s("/>")
			// end dots: hollow when free, filled when linked
			for end, p := range [2][2]int32{{ax, ay}, {bx, by}} {
				extra := ` class="node"`
				if s.Joint[end] >= 0 {
					extra = ` class="node" data-ui-linked`
				}
				w.circle(p[0], p[1], 4, extra)
			}
			w.label(s, b.labelBox(s))
		}
		w.s("</g>")
	}
	w.s("</g>")
}

// Print is the drawing alone for the print page: the shapes and their labels,
// no cursors, handles, guides or lasers.
func (b *Board) Print() string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	w := &writer{buf: make([]byte, 0, 16<<10)}
	// the whole board, in its own shape (square, or a letter page)
	w.s(`<svg class="print-board" style="aspect-ratio: 1000 / `)
	w.i(b.Height)
	w.s(`" xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1000 `)
	w.i(b.Height)
	w.s(`">`)
	b.drawShapes(w, nil)
	w.s(`</svg>`)
	return string(w.buf)
}
