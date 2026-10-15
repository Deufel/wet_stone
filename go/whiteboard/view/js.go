package view

import (
	"strconv"
	"strings"

	"whiteboard/hub"
)

// The page's Datastar expressions. They live in Go, not in templ attribute
// literals: every one carries the board's own path, and an @ in templ markup is
// a sigil (a misparse waiting to happen).

func post(name, op string) string { return "@post('/b/" + name + "/" + op + "')" }

// postKept is a post that a later one never cancels (Datastar cancels a pending
// request to the same URL by default, which is what /move wants and these don't).
func postKept(name, op string) string {
	return "@post('/b/" + name + "/" + op + "', {requestCancellation: 'disabled'})"
}

// pointer maps the event to board coordinates on the square stage, and counts
// it ($seq), so the server can drop a pointer command that arrives late.
const pointer = "const r = stage.getBoundingClientRect(); $x = Math.round((evt.clientX - r.left) * 1000 / r.width); $y = Math.round((evt.clientY - r.top) * 1000 / r.height); $seq = $seq + 1; "

func onInit(name string) string {
	return "$uid = Math.floor(Math.random() * 4294967295); @get('/b/" + name + "/stream')"
}

// Only the main button draws and drags; the right button is lock/unlock.
func onDown(name string) string {
	return "if (evt.button === 0) { evt.preventDefault(); " + pointer +
		postKept(name, "down") + " }"
}

// Right-click: the shape there (selected if it was not) and the selection
// unlock if anything is linked, else lock onto what overlaps them.
func onContext(name string) string {
	return "evt.preventDefault(); " + pointer + postKept(name, "locktoggle")
}

// histPost sets the history panel's argument signal and posts the command.
func histPost(name, signal string, id int64, op string) string {
	return "$" + signal + " = " + strconv.FormatInt(id, 10) + "; " + postKept(name, op)
}

func branchMeta(b hub.BranchView) string {
	switch {
	case b.Head == 0:
		return "empty"
	case b.Fork != 0:
		return "from #" + strconv.FormatInt(b.Fork, 10) + ", " + strconv.Itoa(b.Ahead) + " ahead"
	case b.Current:
		return "here"
	}
	return "#" + strconv.FormatInt(b.Head, 10)
}

func onMove(name string) string {
	return pointer + post(name, "move")
}

func onUp(name string) string {
	return pointer + postKept(name, "up")
}

func onDblclick(name string) string {
	return "const r = stage.getBoundingClientRect(); $lx = Math.round((evt.clientX - r.left) * 1000 / r.width); $ly = Math.round((evt.clientY - r.top) * 1000 / r.height); " +
		postKept(name, "link")
}

// Keys outside the label field. With a selection, arrows nudge it, and in the
// select tool so do h j k l (vim: left, down, up, right); in a drawing tool l
// still picks the line (a shape just drawn stays selected). Esc toggles between
// the select tool and the tool you were on.
func onKey(name string) string {
	return strings.Join([]string{
		"const k = evt.key, t = evt.target;",
		"const d = {ArrowLeft: [-1, 0], ArrowRight: [1, 0], ArrowUp: [0, -1], ArrowDown: [0, 1], h: [-1, 0], j: [0, 1], k: [0, -1], l: [1, 0]}[k];",
		"if (t.id === 'label') { if (k === 'Enter' || k === 'Escape') { evt.preventDefault(); " + post(name, "label") + "; t.blur() } }",
		"else if (t.tagName === 'INPUT' || t.tagName === 'SELECT') {}",
		"else if (evt.ctrlKey || evt.metaKey || evt.altKey) {}",
		"else if (t.tagName === 'BUTTON' && k === 'Enter') {}",
		"else if (k === 'Escape') { if ($tool !== 0) { $_prev = $tool; $tool = 0 } else { $tool = $_prev } }",
		"else if (k === 'Enter' && $sel >= 0) { evt.preventDefault(); document.getElementById('label').focus(); document.getElementById('label').select() }",
		"else if (d && $nsel > 0 && (k.startsWith('Arrow') || $tool === 0)) { evt.preventDefault(); $ndx = d[0]; $ndy = d[1]; " + postKept(name, "nudge") + " }",
		"else if (k === 'Delete' || k === 'Backspace') { " + post(name, "delete") + " }",
		"else if (k === 'g') { " + post(name, "group") + " }",
		"else if (k === 'G') { " + post(name, "ungroup") + " }",
		"else if (k === 'r') { $tool = 1 }",
		"else if (k === 'l') { $tool = 2 }",
		"else if (k === 'a') { $tool = 3 }",
		"else if (k >= '1' && k <= '5') { $tool = Number(k) - 1 }",
	}, " ")
}

const signals = "{uid: 0, x: 0, y: 0, lx: 0, ly: 0, tool: 0, sel: -1, label: '', note: '', shapes: 0, users: 0, ndx: 0, ndy: 0, seq: 0, nsel: 0, grouped: false, edit: 0, hrev: 0, hbranch: 0, theme: 'system', hue: 255, " +
	"_prev: 0}"

const themeEffect = "$theme === 'system' ? document.documentElement.removeAttribute('data-ui-theme') : document.documentElement.setAttribute('data-ui-theme', $theme); document.documentElement.style.setProperty('--hue', $hue)"

const editFocus = "$edit > 0 && setTimeout(() => { el.focus(); el.select() })"

func pressed(tool int) string {
	return "$tool === " + string(rune('0'+tool)) + " ? 'true' : 'false'"
}

func pick(tool int) string { return "$tool = " + string(rune('0'+tool)) }
