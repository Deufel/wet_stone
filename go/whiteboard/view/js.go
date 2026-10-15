package view

import (
	"strconv"
	"strings"

	"whiteboard/db"
	"whiteboard/hub"

	"github.com/a-h/templ"
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
// it ($seq); a down also marks its stroke ($dseq = its own seq), which every
// move and up after it carries, so the server can put a stroke's commands back
// in order when they arrive out of it.
const pointer = "const r = stage.getBoundingClientRect(); $x = Math.round((evt.clientX - r.left) * 1000 / r.width); $y = Math.round((evt.clientY - r.top) * 1000 / r.height); $seq = $seq + 1; "

// streamGet opens a page's view channel and keeps it open. Datastar's default
// ('auto') never retries a stream that ends cleanly (a deploy, a proxy closing
// a long connection) and gives up after ten failures (a phone asleep, a
// network change): that device's page then went silently dead while its
// commands still landed. So: always reconnect, forever, at most 5 s apart.
// The stream's first push is the whole truth, so a reconnect heals the page.
func streamGet(path string) string {
	return "@get('" + path + "', {retry: 'always', retryMaxCount: Infinity, retryMaxWait: 5000})"
}

func onInit(name string) string {
	return "$uid = Math.floor(Math.random() * 4294967295); " + streamGet("/b/"+name+"/stream")
}

// Only the main button draws and drags; the right button is lock/unlock.
func onDown(name string) string {
	return "if (evt.button === 0) { evt.preventDefault(); " + pointer +
		"$dseq = $seq; " + postKept(name, "down") + " }"
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
		return strconv.Itoa(b.Ahead) + " ahead" // ⑂ on the line marks where it leaves
	case b.Current:
		return "here"
	}
	return "on this line"
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
		"else if (t.tagName === 'INPUT' || t.tagName === 'SELECT' || t.isContentEditable) {}",
		"else if (evt.ctrlKey || evt.metaKey || evt.altKey) {}",
		"else if (t.tagName === 'BUTTON' && k === 'Enter') {}",
		"else if (k === '?') { evt.preventDefault(); document.getElementById('keys').showModal() }",
		"else if (k === 'H') { document.getElementById('history-drawer').togglePopover() }",
		"else if (k === 'f' || k === 'F') { " + fullToggle + " }",
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

// titleEdit wires a board name (the header's, the tab's) for in-place
// renaming, for the board's owner only; anyone else gets plain text. A click
// makes it editable with the caret at the end; Enter or leaving it saves (the
// rename comes back down the stream to every view of it), Esc puts it back.
// Its keys stop here, so the board's own shortcuts never see them.
func titleEdit(name string, u *db.User, m *hub.Meta) templ.Attributes {
	if u == nil || m.OwnerID != u.ID {
		return nil
	}
	return templ.Attributes{
		"class": "editable",
		"title": "Click to rename",
		"data-on:click": "evt.preventDefault(); if (!el.isContentEditable) { el.dataset.orig = el.textContent; " +
			"try { el.contentEditable = 'plaintext-only' } catch { el.contentEditable = 'true' } " +
			"el.focus(); const r = document.createRange(); r.selectNodeContents(el); r.collapse(false); " +
			"const s = window.getSelection(); s.removeAllRanges(); s.addRange(r) }",
		"data-on:keydown": "evt.stopPropagation(); if (evt.key === 'Enter') { evt.preventDefault(); el.blur() } " +
			"else if (evt.key === 'Escape') { evt.preventDefault(); el.textContent = el.dataset.orig; el.blur() }",
		"data-on:blur": "if (el.isContentEditable) { el.removeAttribute('contenteditable'); const t = el.textContent.trim(); " +
			"if (t && t !== el.dataset.orig) { $title = t; " + post(name, "share/title") + " } else { el.textContent = el.dataset.orig } }",
	}
}

// newBoard makes a board; the answer sends the page to it.
func newBoard(vis string) string { return "@post('/new/" + vis + "')" }

// pageSignals: the board page's signals, with the server process it landed on.
func pageSignals(inst string) string { return "{inst: " + jsString(inst) + ", " + signals[1:] }

const signals = "{" + savedSignal + ", uid: 0, x: 0, y: 0, lx: 0, ly: 0, tool: 0, sel: -1, label: '', note: '', shapes: 0, users: 0, ndx: 0, ndy: 0, seq: 0, dseq: 0, _full: false, _todo: '', _notes: {text: '', lang: '', v: 0}, nsel: 0, grouped: false, edit: 0, hrev: 0, hbranch: 0, invite: '', who: '', title: '', _prev: 0}"

// fullToggle: the whiteboard alone on the screen. The card fills the window
// ($_full, CSS); where the browser can, the window goes full screen too (an
// iPhone cannot, and the installed app is already).
const fullToggle = "$_full = !$_full; if ($_full) { document.documentElement.requestFullscreen?.().catch(() => {}) } else if (document.fullscreenElement) { document.exitFullscreen() }"

// fullSync: leaving the browser's full screen (Esc, a gesture) leaves ours too.
const fullSync = "document.addEventListener('fullscreenchange', () => { if (!document.fullscreenElement && $_full) { $_full = false } })"

const editFocus = "$edit > 0 && setTimeout(() => { el.focus(); el.select() })"

func pressed(tool int) string {
	return "$tool === " + string(rune('0'+tool)) + " ? 'true' : 'false'"
}

func pick(tool int) string { return "$tool = " + string(rune('0'+tool)) }
