package board

// Sending to a page: a page is a saved board, the whiteboard as it is now
// (the current drawing, none of its history), the whole of it. Drawn again,
// it is the board's own page: every shape where it was, the size it was.

// Page is the whole drawing and the box a page shows (the whole board); ok
// is false when there is nothing on it.
func (b *Board) Page() (snap Snapshot, box [4]int32, ok bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	snap = b.snapshot(nil)
	return snap, [4]int32{0, 0, W, b.Height}, len(snap.Shapes) > 0
}

// Drawing draws a saved drawing as an SVG showing box (the page scales it
// to fit, top centred).
func Drawing(snap Snapshot, box [4]int32, class string) string {
	b := New()
	b.Height = 1 << 20 // a drawing from any board
	b.Restore(snap)
	w := &writer{buf: make([]byte, 0, 8<<10)}
	w.s(`<svg class="`)
	w.s(class)
	w.s(`" xmlns="http://www.w3.org/2000/svg" preserveAspectRatio="xMidYMin meet" viewBox="`)
	w.i(box[0])
	w.s(" ")
	w.i(box[1])
	w.s(" ")
	w.i(max(box[2]-box[0], 1))
	w.s(" ")
	w.i(max(box[3]-box[1], 1))
	w.s(`">`)
	b.drawShapes(w, nil)
	w.s(`</svg>`)
	return string(w.buf)
}
