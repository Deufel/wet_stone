package board

import (
	"fmt"
	"hash/fnv"
	"html"
	"strconv"
	"strings"
)

// A code block is a box whose label is fenced, as in Markdown:
//
//	```go
//	s := append(s, 1)
//
// The first line names the language (the highlighter's: static/highlight.js);
// the rest is the code, drawn monospace, top left, highlighted on the page.
// "math" is LaTeX: the page typesets it (Temml) as MathML, which the
// browser draws itself.

// CodeMax is a code label's limit (a plain label's is LabelMax); CodeW and
// CodeH are a code block's size when the code tool is only clicked.
const (
	CodeMax = 1500
	CodeW   = 440
	CodeH   = 200
)

// codeLangs are the languages the page highlights.
var codeLangs = map[string]bool{"css": true, "html": true, "python": true, "javascript": true, "go": true, "sqlite": true, "odin": true, "mojo": true}

// Text blocks: ```title, ```heading and ```text are words set on the paper,
// frameless, left-aligned, at the paper title's face (the board's own head):
// a page's headings and paragraphs, as a teacher would write them.
// Plain text takes a size, a type step as the page's own: ```text+2,
// ```text-1 (-3 … +3), 18 units at 0 and 1.25 times each step.
var textKinds = map[string]bool{"title": true, "heading": true, "text": true,
	"text-3": true, "text-2": true, "text-1": true, "text+1": true, "text+2": true, "text+3": true}

// TextW is a text block's width when the text tool is only clicked.
const TextW = 400

// TextKind is the fence word for a text size.
func TextKind(size int8) string {
	if size == 0 {
		return "text"
	}
	return fmt.Sprintf("text%+d", size)
}

// textStyle is a text kind's face: its size in board units and weight.
func textStyle(kind string) (float64, int) {
	switch kind {
	case "title":
		return 32, 650
	case "heading":
		return 24, 620
	}
	n, _ := strconv.Atoi(strings.TrimPrefix(kind, "text"))
	px := 18.0
	for ; n > 0; n-- {
		px *= 1.25
	}
	for ; n < 0; n++ {
		px /= 1.25
	}
	w := 400
	if px >= 28 {
		w = 650
	} else if px >= 22 {
		w = 600
	}
	return px, w
}

// TextLineH is one line's height at a size (its line-height, 1.3).
func TextLineH(size int8) int32 {
	px, _ := textStyle(TextKind(size))
	return int32(px*1.3 + 0.5)
}

// IsText: is this label a text block (title, heading or text)?
func IsText(label string) bool {
	if !strings.HasPrefix(label, "```") {
		return false
	}
	lang, _ := CodeOf(label)
	return textKinds[lang]
}

// IsCode: is this label a code block?
func IsCode(label string) bool { return strings.HasPrefix(label, "```") }

// CodeOf splits a code label into its language ("" for none we highlight)
// and its code.
func CodeOf(label string) (lang, code string) {
	rest := strings.TrimPrefix(label, "```")
	first, body, _ := strings.Cut(rest, "\n")
	lang = strings.ToLower(strings.TrimSpace(first))
	if !codeLangs[lang] && lang != "math" && lang != "svg" && !textKinds[lang] {
		lang = ""
	}
	return lang, strings.TrimSuffix(strings.TrimRight(body, " \t\n"), "```")
}

// code draws a code label: a <pre><code> the page's highlighter colours. Its
// id changes with the code and the box, so the element is new when either is,
// and its data-init highlights and fits it once, as it lands.
func (w *writer) code(s *Shape, box [4]int32) {
	lang, code := CodeOf(s.Label)
	h := fnv.New32a()
	h.Write([]byte(s.Label))
	fmt.Fprint(h, box[2], box[3]) // a new size fits again
	if lang == "math" {
		w.math(code, box, strconv.FormatUint(uint64(h.Sum32()), 36))
		return
	}
	if lang == "svg" {
		w.art(code, box)
		return
	}
	if textKinds[lang] {
		w.s("<foreignObject")
		w.attr("x", box[0])
		w.attr("y", box[1])
		w.attr("width", box[2])
		w.attr("height", box[3])
		// new with its words or its box, and shrunk (once, as it lands) until
		// its lines fit the box: a model cannot measure its own words
		px, weight := textStyle(lang)
		w.s(`><div xmlns="http://www.w3.org/1999/xhtml" class="label textblock" style="font-size: `)
		w.s(strconv.FormatFloat(px, 'f', 1, 64))
		w.s(`px; font-weight: `)
		w.s(strconv.Itoa(weight))
		w.s(`" id="text-`)
		w.s(strconv.FormatUint(uint64(h.Sum32()), 36))
		w.s(`" data-ignore-morph data-init="let f = parseFloat(getComputedStyle(el).fontSize); for (let i = 0; i &lt; 24 &amp;&amp; f &gt; 7 &amp;&amp; el.scrollHeight &gt; el.clientHeight + 1; i++) { f *= 0.92; el.style.fontSize = f + 'px' }">`)
		w.s(html.EscapeString(code))
		w.s("</div></foreignObject>")
		return
	}
	w.s("<foreignObject")
	w.attr("x", box[0])
	w.attr("y", box[1])
	w.attr("width", box[2])
	w.attr("height", box[3])
	w.s(`><div xmlns="http://www.w3.org/1999/xhtml" class="label code"><pre><code id="code-`)
	w.s(strconv.FormatUint(uint64(h.Sum32()), 36))
	w.s(`" class="`)
	w.s(lang)
	// data-ignore-morph: the highlighter's spans would morph back to plain
	// text; shrunk until its longest line and its lines fit the box
	w.s(`" data-ignore-morph data-init="window.highlightAll &amp;&amp; highlightAll(); const p = el.parentElement, fit = () => { const k = Math.min(1, p.clientWidth / el.scrollWidth, p.clientHeight / el.scrollHeight); if (k &lt; 1) { el.style.fontSize = Math.max(5, parseFloat(getComputedStyle(el).fontSize) * k * 0.97) + 'px' } }; fit(); document.fonts.ready.then(fit)">`)
	w.s(html.EscapeString(code))
	w.s("</code></pre></div></foreignObject>")
}

// math draws a ```math label: its LaTeX, which the page's Temml turns into
// MathML as it lands (a new id when the TeX changes, so it typesets again).
func (w *writer) math(tex string, box [4]int32, key string) {
	w.s("<foreignObject")
	w.attr("x", box[0])
	w.attr("y", box[1])
	w.attr("width", box[2])
	w.attr("height", box[3])
	w.s(`><div xmlns="http://www.w3.org/1999/xhtml" class="label math"><div class="tex" id="tex-`)
	w.s(key)
	// data-ignore-morph: the board is drawn again on every change, and its
	// TeX would morph back over the MathML Temml made of it
	// typeset, then shrunk until it fits its box (a model cannot know how
	// wide the typeset math will be)
	w.s(`" data-ignore-morph data-init="if (window.temml) { temml.render(el.textContent, el, {displayMode: true, throwOnError: false}); const b = el.parentElement, k = Math.min(1, (b.clientWidth - 16) / el.scrollWidth, (b.clientHeight - 16) / el.scrollHeight); if (k &lt; 1) { el.style.fontSize = (40 * k) + 'px' } }">`)
	w.s(html.EscapeString(tex))
	w.s("</div></div></foreignObject>")
}

// IsArt: is this label a free drawing (```svg …)? Its box is not drawn.
func IsArt(label string) bool {
	return strings.HasPrefix(label, "```svg")
}

// art draws a ```svg label: its (cleaned) SVG fitted into the box. Cleaned
// again here, whatever wrote it.
func (w *writer) art(src string, box [4]int32) {
	clean, err := CleanSVG(strings.TrimSpace(src))
	if err != nil {
		return
	}
	w.s(`<svg class="art"`)
	w.attr("x", box[0])
	w.attr("y", box[1])
	w.attr("width", box[2])
	w.attr("height", box[3])
	w.s(` preserveAspectRatio="xMidYMid meet" `)
	w.s(strings.TrimPrefix(clean, "<svg "))
}
