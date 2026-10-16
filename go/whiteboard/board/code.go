package board

import (
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

// IsCode: is this label a code block?
func IsCode(label string) bool { return strings.HasPrefix(label, "```") }

// CodeOf splits a code label into its language ("" for none we highlight)
// and its code.
func CodeOf(label string) (lang, code string) {
	rest := strings.TrimPrefix(label, "```")
	first, body, _ := strings.Cut(rest, "\n")
	lang = strings.ToLower(strings.TrimSpace(first))
	if !codeLangs[lang] && lang != "math" {
		lang = ""
	}
	return lang, strings.TrimSuffix(strings.TrimRight(body, " \t\n"), "```")
}

// code draws a code label: a <pre><code> the page's highlighter colours. Its
// id changes with the code, so the element is new when the code is, and its
// data-init highlights it once, as it lands.
func (w *writer) code(s *Shape, box [4]int32) {
	lang, code := CodeOf(s.Label)
	h := fnv.New32a()
	h.Write([]byte(s.Label))
	if lang == "math" {
		w.math(code, box, strconv.FormatUint(uint64(h.Sum32()), 36))
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
	w.s(`" data-init="window.highlightAll &amp;&amp; highlightAll()">`)
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
	w.s(`" data-init="window.temml &amp;&amp; temml.render(el.textContent, el, {displayMode: true, throwOnError: false})">`)
	w.s(html.EscapeString(tex))
	w.s("</div></div></foreignObject>")
}
