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

// CodeMax is a code label's limit (a plain label's is LabelMax).
const CodeMax = 1500

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
	if !codeLangs[lang] {
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
