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
// onDown starts a stroke. The stage captures the pointer, so every move and
// the release come back to it even when the drag leaves the board, or the
// browser window: a release out there used to be lost, the drag never ended,
// and the shape kept following the mouse (the "freeze").
func onDown(name string) string {
	// a press on the board ends an open label edit first (preventDefault keeps
	// focus where it was, so the editor would never hear its blur)
	return "if ($_ledit) { document.getElementById('label-edit').blur() } " +
		"if (evt.button === 0) { evt.preventDefault(); try { el.setPointerCapture(evt.pointerId) } catch {} $_drag = true; " + pointer +
		"$dseq = $seq; " + postKept(name, "down") + " }"
}

// onLost ends a stroke the browser took away (pointercancel, a lost capture)
// where it was last seen, as if released there.
func onLost(name string) string {
	return "if ($_drag) { $_drag = false; $seq = $seq + 1; " + postKept(name, "up") + " }"
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

// onMove sends the pointer one post at a time: while one is on its way, the
// newest position waits (el._again) and goes the moment it returns. Moves are
// never cancelled. Each cancelled move left a half-closed request on the
// browser's HTTP/3 connection; enough of them used up its request slots, and
// then no new request (a move, a click, a page) could start while the open
// streams went on: the freeze. The server orders strokes by $seq, and the up
// carries the final point, so a skipped middle position costs nothing.
func onMove(name string) string {
	return pointer + "if (el._mv) { el._again = true } else { const send = () => { el._mv = true; el._again = false; " +
		"@post('/b/" + name + "/move', {requestCancellation: 'disabled'}).finally(() => { el._mv = false; if (el._again) { send() } }) }; send() }"
}

func onUp(name string) string {
	return "$_drag = false; " + pointer + postKept(name, "up")
}



// The label editor. It opens when the server bumps $edit (sending the
// shape's label box, $lbox, and its text, $label) and only then: el._seen
// keeps a change of $label from reopening it.
const labelBoxStyle = "{left: $lbox[0] / 10 + '%', top: $lbox[1] / 10 + '%', width: $lbox[2] / 10 + '%', height: $lbox[3] / 10 + '%'}"

const labelOpen = "if ($edit > 0 && $edit !== el._seen) { el._seen = $edit; $_ledit = true; el.textContent = $label; " +
	"requestAnimationFrame(() => { el.focus(); const r = document.createRange(); r.selectNodeContents(el); r.collapse(false); const s = window.getSelection(); s.removeAllRanges(); s.addRange(r) }) }"

// Enter or leaving the editor saves; Esc puts it back. Its keys stop here.
const labelKeys = "evt.stopPropagation(); if (evt.key === 'Enter') { evt.preventDefault(); el.blur() } else if (evt.key === 'Escape') { evt.preventDefault(); el._cancel = true; el.blur() }"

func labelCommit(name string) string {
	return "if ($_ledit) { $_ledit = false; const t = el.textContent.replace(/\\s+/g, ' ').trim().slice(0, 48); " +
		"if (!el._cancel && t !== $label) { $label = t; " + postKept(name, "label") + " } el._cancel = false }"
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
		"if (t.tagName === 'INPUT' || t.tagName === 'SELECT' || t.isContentEditable) {}",
		"else if (evt.ctrlKey || evt.metaKey || evt.altKey) {}",
		"else if (t.tagName === 'BUTTON' && k === 'Enter') {}",
		"else if (k === '?') { evt.preventDefault(); document.getElementById('keys').showModal() }",
		"else if (k === 'H') { document.getElementById('history-drawer').togglePopover() }",
		"else if (k === 'f' || k === 'F') { " + fullToggle + " }",
		"else if (k === 'Escape') { if ($tool !== 0) { $_prev = $tool; $tool = 0 } else { $tool = $_prev } }",
		"else if (k === 'Enter' && $sel >= 0) { evt.preventDefault(); " + postKept(name, "editsel") + " }",
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

// The alive sentinel, in Datastar: each heartbeat ($_beat, every 5 s from the
// stream) stamps the page element (el.beatAt); once a second, a visible page
// with no beat for 12 s goes black and white (mike.css .bw on <html>,
// data-ui-lost too), and the next beat brings the colour back. A hidden tab's
// stream is closed on purpose, so while hidden the stamp is kept fresh (a
// grace on return). It arms on the first beat: a page with no stream never
// greys. (The stamp is not a signal: an effect that assigns a signal also
// reads it, re-runs on its own write, and the stamp never aged.)
const aliveBeat = "$_beat && (el.beatAt = Date.now())"

const aliveCheck = "if (el.beatAt) { if (document.hidden) { el.beatAt = Date.now() } else { const lost = Date.now() - el.beatAt > 12000; document.documentElement.classList.toggle('bw', lost); document.documentElement.toggleAttribute('data-ui-lost', lost) } }"

// newBoard makes a board; the answer sends the page to it.
func newBoard(vis string) string { return "@post('/new/" + vis + "')" }

// pageSignals: the board page's signals, with the server process it landed on.
func pageSignals(inst string) string { return "{inst: " + jsString(inst) + ", " + signals[1:] }

const signals = "{" + savedSignal + ", uid: 0, x: 0, y: 0, lx: 0, ly: 0, tool: 0, sel: -1, label: '', note: '', shapes: 0, users: 0, ndx: 0, ndy: 0, seq: 0, dseq: 0, lbox: [0, 0, 0, 0], _ledit: false, _drag: false, _beat: 0, _full: false, _todo: '', _notes: {text: '', lang: '', v: 0}, nsel: 0, grouped: false, edit: 0, hrev: 0, hbranch: 0, invite: '', who: '', title: '', _prev: 0}"

// fullToggle: the whiteboard alone on the screen. The card fills the window
// ($_full, CSS); where the browser can, the window goes full screen too (an
// iPhone cannot, and the installed app is already).
const fullToggle = "$_full = !$_full; if ($_full) { document.documentElement.requestFullscreen?.().catch(() => {}) } else if (document.fullscreenElement) { document.exitFullscreen() }"

// fullSync: leaving the browser's full screen (Esc, a gesture) leaves ours too.
const fullSync = "document.addEventListener('fullscreenchange', () => { if (!document.fullscreenElement && $_full) { $_full = false } })"

func pressed(tool int) string {
	return "$tool === " + string(rune('0'+tool)) + " ? 'true' : 'false'"
}

func pick(tool int) string { return "$tool = " + string(rune('0'+tool)) }
