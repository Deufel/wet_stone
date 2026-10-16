package board

import "html"

// The paper's head: a page says what it is on itself, in its own units, so the
// whiteboard, its print and its thumbnails are the same drawing. The title
// sits in the band above y = HeadBand (drawings start below it); a foot line
// (author, date, page) runs along the bottom edge.

// HeadBand is the top of the paper the head takes.
const HeadBand = 90

// Head is what a page's head and foot say.
type Head struct {
	Title string
	Sub   string // under the title (a book's chapter); "" for none
	Foot  string // along the bottom (author · date · p. 3); "" for none
}

// Title metrics, in board units: the live page's editable title (HTML in a
// foreignObject) uses the same.
const (
	HeadTitleX    = 40
	HeadTitleY    = 58 // its baseline
	HeadTitleSize = 32
	HeadSubSize   = 16
)

// HeadSVG draws a head (and foot) at a page whose top-left is (x, y) and
// height h, as SVG text.
func HeadSVG(hd Head, x, y, h int32) string {
	w := &writer{buf: make([]byte, 0, 512)}
	w.s(`<g class="paper-head-print">`)
	if hd.Title != "" {
		w.s(`<text class="paper-title"`)
		w.attr("x", x+HeadTitleX)
		w.attr("y", y+HeadTitleY)
		w.s(` font-size="32" font-weight="650">`)
		w.s(html.EscapeString(hd.Title))
		w.s(`</text>`)
	}
	if hd.Sub != "" {
		w.s(`<text class="paper-sub"`)
		w.attr("x", x+HeadTitleX)
		w.attr("y", y+HeadTitleY+24)
		w.s(` font-size="16">`)
		w.s(html.EscapeString(hd.Sub))
		w.s(`</text>`)
	}
	if hd.Foot != "" {
		w.s(`<text class="paper-foot" text-anchor="end"`)
		w.attr("x", x+1000-HeadTitleX)
		w.attr("y", y+h-18)
		w.s(` font-size="14">`)
		w.s(html.EscapeString(hd.Foot))
		w.s(`</text>`)
	}
	w.s(`</g>`)
	return string(w.buf)
}
