package main

// ============================================================================
// whiteboard — a streamed SVG diagram board on Tina + Datastar + mike.css
//
// The Board isolate owns every shape and every user's cursor, selection and
// drag. Browsers post pointer events as commands; the Board applies them with
// snapping and answers nothing. Every open page holds one SSE stream and gets
// the whole <svg id="board"> as a morph on each render tick. One view channel,
// commands answer with an empty stream: land-and-stream, pure CQRS.
//
// Shapes: boxes, lines, arrows, each with an optional label.
//
// Linking. A double-click (the native dblclick event, through data-on) puts
// its own coordinate in the $lx/$ly signals and posts /link. The server takes
// EVERY node on that coordinate (line and arrow ends, plus at most one box
// anchor) and puts them in one Joint: a single shared position they all read.
// A joint with a box anchor follows the box; a joint without one is a free
// point that moves all its ends together. Double-clicking a joint dissolves
// it. Joint positions are derived at read time: derive, never copy. Nothing
// else links or unlinks; pointerdown never changes membership.
//
// Labels. The server tells each viewer which shape it has selected ($sel) and
// that shape's text ($label). The label field posts both back, so text always
// lands on the shape it was typed for, whatever was clicked since.
//
// Color never appears in this file. Shapes carry a --hue-shift step, the
// selection carries --fg, guides carry the .inf lock, and mike.css paints the
// SVG through its currentColor bridge. Text goes through <foreignObject> so the
// engine types it (never SVG <text>).
//
// Build & run:  make run   (sets -define:HTTP_EGRESS_BUFFER_SIZE=57344)
//               open http://127.0.0.1:8080 in two windows
// ============================================================================

import tina "../tina/src"
import datastar "../tina/src/extensions/http/datastar"
import http "../tina/src/extensions/http/server"
import "core:fmt"
import "core:math"
import "core:strconv"

BOARD_W :: 1000
BOARD_H :: 1000
GRID :: 20
SNAP_ANCHOR :: 14 // px: snap a dragged point to another shape's anchor
SNAP_AXIS :: 10 // px: snap a dragged point to its own shape's other end
HANDLE_R :: 9 // px: hit radius for corners, endpoints and link targets
LINE_HIT :: 7 // px: hit distance for a line's body
ARROW_LEN :: 16
ARROW_HALF :: 6
SHAPES_MAX :: 160
JOINTS_MAX :: 160
JOINT_EPS :: 2 // px: nodes this close count as the same coordinate
USERS_MAX :: 64
SUBSCRIBERS_MAX :: 128
LABEL_MAX :: 48
FRAME_MAX :: 50_000 // one SSE event; must fit Tina's per-connection egress buffer
FRAME_RESERVE :: 9_000 // room kept free for cursors, handles, guides and new users
NOTE_MAX :: 40
EGRESS_BUFFER_SIZE :: #config(HTTP_EGRESS_BUFFER_SIZE, 4096) // mirrors the -define Tina reads
TICK_NS :: u64(4_000_000) // up to 250 frames a second; a tick renders only when a command changed something
PARK_NS :: u64(3_000_000_000)
USER_TTL_TICKS :: u32(12_000_000_000 / TICK_NS) // 12 s without a message and the cursor disappears
HUE_STEP :: 60
ANCHOR_COUNT :: 9 // box anchors: 4 corners, 4 edge midpoints, center
PORT_COUNT :: 8 // anchors 0..7, the corners and edge midpoints, are a box's ports
SNAP_PORT :: 20 // px: a dragged connector end snaps to a port or a link node from this far
EXTENTS_PAD :: 8 // px: the dotted outline sits this far outside a multi-selection
GHOST_MARQUEE :: 4 // ghost kinds past Box, Line, Arrow
GHOST_EXTENTS :: 5
LASER_MAX :: 160 // live laser segments on the board, all users
LASER_STEP :: 8 // px: a laser point is kept only this far from the last one
LASER_TTL_TICKS :: u32(1_500_000_000 / TICK_NS) // a segment leaves after 1.5 s; the page fades it over 1.2 s

TAG_TICK: tina.Message_Tag : tina.USER_MESSAGE_TAG_BASE + 1
TAG_SUBSCRIBE: tina.Message_Tag : tina.USER_MESSAGE_TAG_BASE + 2
TAG_UNSUBSCRIBE: tina.Message_Tag : tina.USER_MESSAGE_TAG_BASE + 3
TAG_FEED: tina.Message_Tag : tina.USER_MESSAGE_TAG_BASE + 4
TAG_DOWN: tina.Message_Tag : tina.USER_MESSAGE_TAG_BASE + 5
TAG_MOVE: tina.Message_Tag : tina.USER_MESSAGE_TAG_BASE + 6
TAG_UP: tina.Message_Tag : tina.USER_MESSAGE_TAG_BASE + 7
TAG_DELETE: tina.Message_Tag : tina.USER_MESSAGE_TAG_BASE + 8
TAG_CLEAR: tina.Message_Tag : tina.USER_MESSAGE_TAG_BASE + 9
TAG_LINK: tina.Message_Tag : tina.USER_MESSAGE_TAG_BASE + 10
TAG_LABEL: tina.Message_Tag : tina.USER_MESSAGE_TAG_BASE + 11
TAG_NUDGE: tina.Message_Tag : tina.USER_MESSAGE_TAG_BASE + 12
TAG_GROUP: tina.Message_Tag : tina.USER_MESSAGE_TAG_BASE + 13
TAG_UNGROUP: tina.Message_Tag : tina.USER_MESSAGE_TAG_BASE + 14
TAG_LOCK: tina.Message_Tag : tina.USER_MESSAGE_TAG_BASE + 15
TAG_UNLOCK: tina.Message_Tag : tina.USER_MESSAGE_TAG_BASE + 16

// ─── Board state (package global: one shard, one thread, routes read it) ────

Kind :: enum u8 {
	None,
	Box,
	Line,
	Arrow,
}

Shape :: struct {
	kind:           Kind,
	hue:            u8,
	x1, y1, x2, y2: i32, // a connector end's own position, used while it is free
	joint:          [2]i32, // connectors: the joint each end belongs to, -1 = free
	corner:         [4]i32, // boxes: the joint each corner belongs to, -1 = free; corner c is x2 if c&1, y2 if c>=2
	group:          i32, // -1 = none; members share a color and are selected together
	label:          [LABEL_MAX]u8,
	label_len:      u8,
}

// One shared position for every node linked at a coordinate.
Joint :: struct {
	active: bool,
	box:    i32, // box whose anchor drives the joint, -1 = free point
	anchor: u8,
	x, y:   i32, // position while free
}

Drag_Kind :: enum u8 {
	None,
	Whole,
	Handle,
	Group, // the whole selection moves
	Marquee, // a selection rectangle from (gx, gy)
	Laser, // a fading pointer stroke
}

// One smoothed piece of a laser stroke: a quadratic from a through control c to
// b. It is rendered once under a stable id and never changes, so the morph only
// appends it, and every page fades it with its own clock.
Laser_Seg :: struct {
	active:          bool,
	hue:             u8,
	id:              u32,
	born:            u32, // board tick
	ax, ay, cx, cy, bx, by: i32,
}

Drag :: struct {
	kind:   Drag_Kind,
	shape:  i32,
	hx, hy: u8, // Handle: which coordinates move. Whole connector: hx = reference end
	gx, gy: i32, // Whole: grab offset from the reference point
	fresh:  bool, // a shape being drawn: both of its ends are new
	ax, ay: i32, // Group: the grid delta applied so far
	toggle: i32, // Group: the shape a click without movement deselects, -1 = none
	moved:  bool, // Group: the pointer has really moved
}

// What the page needs to draw a drag itself, at pointer speed. For each of
// x1 y1 x2 y2: either it stays at v, or it is snap(pointer - g) + o.
Ghost :: struct {
	kind:  u8, // a Kind, or GHOST_MARQUEE / GHOST_EXTENTS
	raw:   bool, // follow the pointer unsnapped (the marquee)
	moves: [4]bool,
	v:     [4]i32,
	g:     [4]i32,
	o:     [4]i32,
}

User :: struct {
	active:    bool,
	uid:       u32,
	hue:       u8,
	x, y:      i32,
	selected:  i32, // shape index or -1
	drag:      Drag,
	drag_id:   i32, // the page's id for the drag it started ($did)
	sel:       [SHAPES_MAX]bool, // the selection; `selected` is its primary, the label target
	edit_seq:  u32, // bumped to put the page in the label editor
	laser:     [3][2]i32, // the last kept laser points
	laser_n:   int, // how many of them are set (0..3)
	laser_seg: int, // segments emitted in this stroke
	ghost:     Ghost,
	guide_vx:  i32, // vertical guide at x, -1 = none
	guide_hy:  i32, // horizontal guide at y, -1 = none
	anchor_on: bool,
	anchor_x:  i32,
	anchor_y:  i32,
	last_seen: u32,
	note:      [NOTE_MAX]u8, // one-line feedback for this user ($note)
	note_len:  u8,
	note_seq:  u32,
}

Board :: struct {
	shapes:   [SHAPES_MAX]Shape,
	joints:   [JOINTS_MAX]Joint,
	users:    [USERS_MAX]User,
	tick:     u32,
	version:  u32,
	next_hue: u8,
	group_hue: [SHAPES_MAX]u8, // by group id
	lasers:    [LASER_MAX]Laser_Seg,
	laser_id:  u32,
}

board: Board
board_handle: tina.Isolate_Handle = tina.ISOLATE_HANDLE_NONE
frame_buf: [FRAME_MAX]u8

// ─── Messages ───────────────────────────────────────────────────────────────

Cmd_Msg :: struct {
	uid:    u32,
	x, y:   i32,
	tool:   u8, // 0 select, 1 box, 2 line, 3 arrow
	did:    i32, // /down: the page's id for this drag
	nx, ny: i8, // /nudge: grid steps, -1..1
}

Label_Msg :: struct {
	uid:   u32,
	shape: i32,
	len:   u8,
	label: [LABEL_MAX]u8,
}

Sub_Msg :: struct {
	request_token: http.Request_Token,
	uid:           u32,
}

Feed_Msg :: struct {
	request_token: http.Request_Token,
	version:       u32,
}

// Signals as Datastar posts them. Unknown keys (theme, hue, counts) are ignored.
Signals :: struct {
	Uid:   u32 `json:"uid"`,
	X:     i32 `json:"x"`,
	Y:     i32 `json:"y"`,
	Lx:    i32 `json:"lx"`,
	Ly:    i32 `json:"ly"`,
	Tool:  u8 `json:"tool"`,
	Sel:   i32 `json:"sel"`,
	Label: string `json:"label"`,
	Did:   i32 `json:"did"`,
	Ndx:   i32 `json:"ndx"`,
	Ndy:   i32 `json:"ndy"`,
}

// ─── The Board isolate ──────────────────────────────────────────────────────

Subscriber :: struct {
	handle:        tina.Isolate_Handle,
	request_token: http.Request_Token,
	uid:           u32,
}

Board_Isolate :: struct {
	subscribers:      [SUBSCRIBERS_MAX]Subscriber,
	subscriber_count: u32,
	dirty:            bool,
}

board_init :: proc(self_raw: rawptr, args: []u8) -> tina.Isolate_Transition {
	self := tina.self_as(Board_Isolate, self_raw)
	self.subscriber_count = 0
	self.dirty = true
	board_handle = tina.ctx_self_handle()
	tina.ctx_register_timer(TICK_NS, TAG_TICK)
	return tina.ISOLATE_TRANSITION_WAIT_MESSAGE
}

board_handler :: proc(self_raw: rawptr, message: ^tina.Message) -> tina.Isolate_Transition {
	self := tina.self_as(Board_Isolate, self_raw)

	switch message.tag {
	case TAG_SUBSCRIBE:
		if self.subscriber_count < SUBSCRIBERS_MAX {
			msg := tina.payload_as(Sub_Msg, message.user.payload[:])
			self.subscribers[self.subscriber_count] = Subscriber {
				handle        = message.user.source,
				request_token = msg.request_token,
				uid           = msg.uid,
			}
			self.subscriber_count += 1
			_ = user_for(msg.uid) // present from now on, cursor or not
			self.dirty = true
		}

	case TAG_UNSUBSCRIBE:
		for index := u32(0); index < self.subscriber_count; index += 1 {
			if self.subscribers[index].handle == message.user.source {
				user_leave(self.subscribers[index].uid)
				self.subscriber_count -= 1
				self.subscribers[index] = self.subscribers[self.subscriber_count]
				self.dirty = true
				break
			}
		}

	case TAG_DOWN:
		cmd_down(tina.payload_as(Cmd_Msg, message.user.payload[:])^)
		self.dirty = true
	case TAG_MOVE:
		cmd_move(tina.payload_as(Cmd_Msg, message.user.payload[:])^)
		self.dirty = true
	case TAG_UP:
		cmd_up(tina.payload_as(Cmd_Msg, message.user.payload[:])^)
		self.dirty = true
	case TAG_DELETE:
		cmd_delete(tina.payload_as(Cmd_Msg, message.user.payload[:])^)
		self.dirty = true
	case TAG_CLEAR:
		cmd_clear()
		self.dirty = true
	case TAG_LINK:
		cmd_link(tina.payload_as(Cmd_Msg, message.user.payload[:])^)
		self.dirty = true
	case TAG_LABEL:
		cmd_label(tina.payload_as(Label_Msg, message.user.payload[:]))
		self.dirty = true
	case TAG_NUDGE:
		cmd_nudge(tina.payload_as(Cmd_Msg, message.user.payload[:])^)
		self.dirty = true
	case TAG_GROUP:
		cmd_group(tina.payload_as(Cmd_Msg, message.user.payload[:])^)
		self.dirty = true
	case TAG_UNGROUP:
		cmd_ungroup(tina.payload_as(Cmd_Msg, message.user.payload[:])^)
		self.dirty = true
	case TAG_LOCK:
		cmd_lock(tina.payload_as(Cmd_Msg, message.user.payload[:])^)
		self.dirty = true
	case TAG_UNLOCK:
		cmd_unlock(tina.payload_as(Cmd_Msg, message.user.payload[:])^)
		self.dirty = true

	case TAG_TICK:
		board.tick += 1
		// an open stream keeps its viewer present, commands or not
		for index := u32(0); index < self.subscriber_count; index += 1 {
			_ = user_for(self.subscribers[index].uid)
		}
		for &seg in board.lasers {
			if seg.active && board.tick - seg.born > LASER_TTL_TICKS {
				seg.active = false
				self.dirty = true
			}
		}
		for &u in board.users {
			if u.active && board.tick - u.last_seen > USER_TTL_TICKS {
				u.active = false
				self.dirty = true
			}
		}
		if self.dirty {
			self.dirty = false
			board.version += 1
			board_broadcast(self)
		}
		tina.ctx_register_timer(TICK_NS, TAG_TICK)
	}
	return tina.ISOLATE_TRANSITION_WAIT_MESSAGE
}

board_broadcast :: proc(self: ^Board_Isolate) {
	write_index := u32(0)
	for read_index := u32(0); read_index < self.subscriber_count; read_index += 1 {
		subscriber := self.subscribers[read_index]
		feed := Feed_Msg {
			request_token = subscriber.request_token,
			version       = board.version,
		}
		if tina.ctx_send(subscriber.handle, TAG_FEED, &feed) == .stale_handle {
			user_leave(subscriber.uid)
			continue
		}
		self.subscribers[write_index] = subscriber
		write_index += 1
	}
	self.subscriber_count = write_index
}

// ─── Geometry: anchors, joints, effective end points ────────────────────────

is_connector :: proc(k: Kind) -> bool {
	return k == .Line || k == .Arrow
}

// The nine anchors of a box: TL TR BL BR, top mid, bottom mid, left mid, right mid, center.
anchor_point :: proc(b: ^Shape, index: u8) -> (x, y: i32) {
	x1, x2 := min(b.x1, b.x2), max(b.x1, b.x2)
	y1, y2 := min(b.y1, b.y2), max(b.y1, b.y2)
	mx, my := (x1 + x2) / 2, (y1 + y2) / 2
	switch index {
	case 0: return x1, y1
	case 1: return x2, y1
	case 2: return x1, y2
	case 3: return x2, y2
	case 4: return mx, y1
	case 5: return mx, y2
	case 6: return x1, my
	case 7: return x2, my
	}
	return mx, my
}

joint_pos :: proc(j: i32) -> (x, y: i32) {
	jt := &board.joints[j]
	if jt.box >= 0 && board.shapes[jt.box].kind == .Box {
		return anchor_point(&board.shapes[jt.box], jt.anchor)
	}
	return jt.x, jt.y
}

// Where a connector's end is: its joint's position if joined, else its own.
end_point :: proc(s: ^Shape, end: int) -> (x, y: i32) {
	if j := s.joint[end]; j >= 0 && board.joints[j].active do return joint_pos(j)
	if end == 0 do return s.x1, s.y1
	return s.x2, s.y2
}

set_end :: proc(s: ^Shape, end: int, x, y: i32) {
	if end == 0 {
		s.x1, s.y1 = x, y
	} else {
		s.x2, s.y2 = x, y
	}
}

end_is_free :: proc(s: ^Shape, end: int) -> bool {
	return s.joint[end] < 0
}

// A joint a box drives, or box corners share, cannot be dragged by a line end.
joint_pinned :: proc(j: i32) -> bool {
	return board.joints[j].box >= 0 || joint_has_corners(j)
}

joint_has_corners :: proc(j: i32) -> bool {
	for &s in board.shapes do if s.kind == .Box && box_in_joint(&s, j) do return true
	return false
}

box_in_joint :: proc(b: ^Shape, j: i32) -> bool {
	for c in b.corner do if c == j do return true
	return false
}

corner_pos :: proc(b: ^Shape, c: int) -> (x, y: i32) {
	x = b.x2 if c & 1 == 1 else b.x1
	y = b.y2 if c >= 2 else b.y1
	return
}

set_corner :: proc(b: ^Shape, c: int, x, y: i32) {
	if c & 1 == 1 do b.x2 = x
	else do b.x1 = x
	if c >= 2 do b.y2 = y
	else do b.y1 = y
}

// After box `start` changed shape, each of its linked corners carries its joint
// and every other box corner in that joint takes the same position: a shared
// vertex. That box may move further linked corners, so it repeats, bounded,
// since boxes can link in a cycle.
corners_settle :: proc(start: i32) {
	queue: [64]i32
	head, tail := 0, 1
	queue[0] = start
	for head < tail {
		bi := queue[head]
		head += 1
		b := &board.shapes[bi]
		for c in 0 ..< 4 {
			j := b.corner[c]
			if j < 0 do continue
			cx, cy := corner_pos(b, c)
			jt := &board.joints[j]
			if jt.x == cx && jt.y == cy do continue
			jt.x, jt.y = cx, cy
			for &o, oi in board.shapes {
				if o.kind != .Box || i32(oi) == bi do continue
				for k in 0 ..< 4 {
					if o.corner[k] != j do continue
					if ox, oy := corner_pos(&o, k); ox == cx && oy == cy do continue
					set_corner(&o, k, cx, cy)
					if tail < len(queue) {
						queue[tail] = i32(oi)
						tail += 1
					}
				}
			}
		}
	}
}

joint_new :: proc() -> i32 {
	for &jt, index in board.joints {
		if !jt.active {
			jt = Joint {
				active = true,
				box    = -1,
			}
			return i32(index)
		}
	}
	return -1
}

// Dissolve a joint: every member end keeps the position it had.
joint_dissolve :: proc(j: i32) -> (freed: int) {
	x, y := joint_pos(j)
	for &s in board.shapes {
		if s.kind == .Box {
			for &c in s.corner do if c == j {
				c = -1
				freed += 1
			}
			continue
		}
		if !is_connector(s.kind) do continue
		for end in 0 ..< 2 {
			if s.joint[end] == j {
				s.joint[end] = -1
				set_end(&s, end, x, y)
				freed += 1
			}
		}
	}
	if board.joints[j].box >= 0 do freed += 1
	board.joints[j].active = false
	return
}

// A joint needs two members to mean anything.
joint_gc :: proc(j: i32) {
	if j < 0 || !board.joints[j].active do return
	members := 1 if board.joints[j].box >= 0 else 0
	for &s in board.shapes {
		if s.kind == .Box {
			for c in s.corner do if c == j do members += 1
			continue
		}
		if !is_connector(s.kind) do continue
		for end in 0 ..< 2 do if s.joint[end] == j do members += 1
	}
	if members < 2 do joint_dissolve(j)
}

// Remove a shape and keep the joint table consistent.
shape_remove :: proc(index: i32) {
	s := &board.shapes[index]
	switch s.kind {
	case .Box:
		// its corners leave their joints; joints it drove become free points where they stand
		for &c in s.corner {
			j := c
			if j < 0 do continue
			c = -1
			s.kind = .None
			joint_gc(j)
			s.kind = .Box
		}
		for &jt, j in board.joints {
			if jt.active && jt.box == index {
				jt.x, jt.y = joint_pos(i32(j))
				jt.box = -1
				s.kind = .None
				joint_gc(i32(j))
				s.kind = .Box
			}
		}
	case .Line, .Arrow:
		for end in 0 ..< 2 {
			j := s.joint[end]
			if j >= 0 {
				set_end(s, end, joint_pos(j)) // so a dissolved neighbour stays put
				s.joint[end] = -1
				s.kind = .None
				joint_gc(j)
			}
		}
	case .None:
	}
	s.kind = .None
	for &other in board.users {
		other.sel[index] = false
		if other.active && other.selected == index {
			other.selected = -1
			other.drag = Drag{}
		}
	}
}

// ─── Users and notes ────────────────────────────────────────────────────────

user_for :: proc(uid: u32) -> ^User {
	for &u in board.users {
		if u.active && u.uid == uid {
			u.last_seen = board.tick
			return &u
		}
	}
	for &u in board.users {
		if !u.active {
			u = User {
				active    = true,
				uid       = uid,
				hue       = board.next_hue,
				selected  = -1,
				guide_vx  = -1,
				guide_hy  = -1,
				last_seen = board.tick,
			}
			board.next_hue = (board.next_hue + 1) % 6
			return &u
		}
	}
	return nil
}

user_leave :: proc(uid: u32) {
	for &u in board.users {
		if u.active && u.uid == uid do u.active = false
	}
}

note :: proc(u: ^User, text: string) {
	n := min(len(text), NOTE_MAX)
	copy(u.note[:], text[:n])
	u.note_len = u8(n)
	u.note_seq += 1
}

clear_guides :: proc(u: ^User) {
	u.guide_vx = -1
	u.guide_hy = -1
	u.anchor_on = false
}

// ─── Frame budget: a frame that cannot fit one SSE event is never allowed ───

frame_fits :: proc(extra: int) -> bool {
	frame, ok := render_frame(0)
	return ok && len(frame) + extra + FRAME_RESERVE <= FRAME_MAX
}

escaped_len :: proc(text: []u8) -> (n: int) {
	for c in text {
		switch c {
		case '<', '>': n += 4
		case '&': n += 5
		case '"': n += 6
		case: n += 1
		}
	}
	return
}

// ─── Commands ───────────────────────────────────────────────────────────────

shape_new :: proc(kind: Kind, x, y: i32) -> i32 {
	for &s, index in board.shapes {
		if s.kind == .None {
			s = Shape {
				kind  = kind,
				hue   = u8(index % 6),
				x1    = x,
				y1    = y,
				x2    = x,
				y2    = y,
				joint  = {-1, -1},
				corner = {-1, -1, -1, -1},
				group  = -1,
			}
			return i32(index)
		}
	}
	return -1
}

cmd_down :: proc(cmd: Cmd_Msg) {
	u := user_for(cmd.uid)
	if u == nil do return
	u.x, u.y = cmd.x, cmd.y
	clear_guides(u)
	switch cmd.tool {
	case 4:
		u.drag = Drag{kind = .Laser, shape = -1}
		u.laser_n, u.laser_seg = 0, 0
		laser_point(u, cmd.x, cmd.y)
	case 1, 2, 3:
		if !frame_fits(400) {
			note(u, "board is full: delete something first")
			return
		}
		gx, gy := snap_grid(cmd.x), snap_grid(cmd.y)
		kind: Kind = .Box if cmd.tool == 1 else (.Line if cmd.tool == 2 else .Arrow)
		if kind != .Box {
			if px, py, found := nearest_port(cmd.x, cmd.y, -1, {-1, -1}); found {
				gx, gy = px, py
			} else if ax, ay, found_anchor := nearest_anchor(cmd.x, cmd.y, -1, -1); found_anchor {
				gx, gy = ax, ay
			}
		}
		index := shape_new(kind, gx, gy)
		if index < 0 {
			note(u, "board is full: delete something first")
			return
		}
		sel_clear(u)
		u.sel[index] = true
		u.selected = index
		u.drag = Drag{kind = .Handle, shape = index, hx = 1, hy = 1, fresh = true}
	case:
		index, drag, hit := hit_test(cmd.x, cmd.y)
		if !hit {
			sel_clear(u)
			u.selected = -1
			u.drag = Drag{kind = .Marquee, shape = -1, gx = cmd.x, gy = cmd.y}
		} else if board.shapes[index].group >= 0 && drag.kind == .Handle {
			// a node inside a group (a line end, a box corner) is dragged on its own;
			// the group stays selected around it
			if !u.sel[index] {
				sel_clear(u)
				sel_set(u, index, true)
			}
			u.selected = index
			u.drag = drag
		} else if sel_count(u) >= 2 {
			// a multi-selection: a click toggles a shape (with its group), a drag moves them all
			toggle := i32(-1)
			if u.sel[index] do toggle = index
			else do sel_set(u, index, true)
			u.selected = index
			u.drag = Drag{kind = .Group, shape = index, gx = cmd.x, gy = cmd.y, toggle = toggle}
		} else {
			sel_clear(u)
			sel_set(u, index, true)
			u.selected = index
			u.drag = drag
			if sel_count(u) >= 2 do u.drag = Drag{kind = .Group, shape = index, gx = cmd.x, gy = cmd.y, toggle = -1}
		}
	}
	ghost_begin(u, cmd.did)
}

// Describe the drag as it starts, so the page can draw it ahead of the server.
// The page snaps to the grid only; anchor snapping shows when the frame lands.
ghost_begin :: proc(u: ^User, did: i32) {
	u.drag_id = did
	u.ghost = Ghost{}
	gh := &u.ghost
	#partial switch u.drag.kind {
	case .None, .Laser:
		return
	case .Marquee:
		gh.kind, gh.raw = GHOST_MARQUEE, true
		gh.v = {u.drag.gx, u.drag.gy, u.drag.gx, u.drag.gy}
		gh.moves = {false, false, true, true}
		return
	case .Group:
		// the dotted extents, moved by snap_delta(pointer - grab) as group_move does
		x1, y1, x2, y2, _ := mask_extents(&u.sel)
		gh.kind = GHOST_EXTENTS
		gh.v = {x1 - EXTENTS_PAD, y1 - EXTENTS_PAD, x2 + EXTENTS_PAD, y2 + EXTENTS_PAD}
		gh.moves = {true, true, true, true}
		gh.g = {u.drag.gx - SNAP_BIAS, u.drag.gy - SNAP_BIAS, u.drag.gx - SNAP_BIAS, u.drag.gy - SNAP_BIAS}
		for i in 0 ..< 4 do gh.o[i] = gh.v[i] - SNAP_BIAS
		return
	}
	s := &board.shapes[u.drag.shape]
	gh.kind = u8(s.kind)
	ax, ay, bx, by := s.x1, s.y1, s.x2, s.y2
	if is_connector(s.kind) {
		ax, ay = end_point(s, 0)
		bx, by = end_point(s, 1)
	}
	gh.v = {ax, ay, bx, by}
	switch u.drag.kind {
	case .Handle:
		if is_connector(s.kind) {
			gh.moves[u.drag.hx * 2] = true
			gh.moves[u.drag.hx * 2 + 1] = true
		} else {
			gh.moves[u.drag.hx * 2] = true
			gh.moves[u.drag.hy * 2 + 1] = true
		}
	case .Whole:
		if s.kind == .Box {
			gh.moves = {true, true, true, true}
			gh.g = {u.drag.gx, u.drag.gy, u.drag.gx, u.drag.gy}
			gh.o = {0, 0, bx - ax, by - ay}
		} else {
			// the reference end follows the grab; other free ends keep their offset to it
			rx, ry := end_point(s, int(u.drag.hx))
			for end in 0 ..< 2 {
				if !end_is_free(s, end) do continue
				ex, ey := end_point(s, end)
				gh.moves[end * 2], gh.moves[end * 2 + 1] = true, true
				gh.g[end * 2], gh.g[end * 2 + 1] = u.drag.gx, u.drag.gy
				gh.o[end * 2], gh.o[end * 2 + 1] = ex - rx, ey - ry
			}
		}
	case .None, .Group, .Marquee, .Laser:
	}
}

cmd_move :: proc(cmd: Cmd_Msg) {
	u := user_for(cmd.uid)
	if u == nil do return
	u.x, u.y = cmd.x, cmd.y
	if u.drag.kind == .None || u.drag.kind == .Marquee do return
	if u.drag.kind == .Laser {
		laser_point(u, cmd.x, cmd.y)
		return
	}
	if u.drag.kind == .Group {
		group_move(u, cmd.x, cmd.y)
		return
	}
	index := u.drag.shape
	s := &board.shapes[index]
	if s.kind == .None {
		u.drag = Drag{}
		return
	}
	clear_guides(u)

	switch u.drag.kind {
	case .Whole:
		if s.kind == .Box {
			// corner-linked boxes come along as one rigid cluster
			nx := snap_grid(cmd.x - u.drag.gx)
			ny := snap_grid(cmd.y - u.drag.gy)
			mask: [SHAPES_MAX]bool
			mask[index] = true
			translate_shapes(&mask, nx - s.x1, ny - s.y1)
		} else {
			// a connector body moves its free ends; joined ends stay in their joints
			ref := int(u.drag.hx)
			rx, ry := end_point(s, ref)
			dx := snap_grid(cmd.x - u.drag.gx) - rx
			dy := snap_grid(cmd.y - u.drag.gy) - ry
			for end in 0 ..< 2 {
				if !end_is_free(s, end) do continue
				ex, ey := end_point(s, end)
				set_end(s, end, ex + dx, ey + dy)
			}
		}

	case .Handle:
		joint := i32(-1)
		if is_connector(s.kind) do joint = s.joint[u.drag.hx]
		else do joint = s.corner[u.drag.hy * 2 + u.drag.hx]
		px, py := cmd.x, cmd.y
		port_x, port_y, on_port := i32(0), i32(0), false
		if is_connector(s.kind) do port_x, port_y, on_port = nearest_port(px, py, index, s.joint)
		// 0. a connector end: a box port or a link node, where it will link itself
		if on_port {
			px, py = port_x, port_y
			u.anchor_on = true
			u.anchor_x, u.anchor_y = px, py
		} else if ax, ay, found := nearest_anchor(px, py, index, joint); found {
			// 1. another shape's anchor
			px, py = ax, ay
			u.anchor_on = true
			u.anchor_x, u.anchor_y = ax, ay
		} else {
			// 2. the shape's own other end, per axis
			ox, oy: i32
			if is_connector(s.kind) {
				ox, oy = end_point(s, 1 - int(u.drag.hx))
			} else {
				ox = s.x1 if u.drag.hx == 1 else s.x2
				oy = s.y1 if u.drag.hy == 1 else s.y2
			}
			snapped_x, snapped_y := false, false
			if abs32(px - ox) <= SNAP_AXIS {
				px = ox
				u.guide_vx = ox
				snapped_x = true
			}
			if abs32(py - oy) <= SNAP_AXIS {
				py = oy
				u.guide_hy = oy
				snapped_y = true
			}
			// 3. the grid
			if !snapped_x do px = snap_grid(px)
			if !snapped_y do py = snap_grid(py)
		}
		if is_connector(s.kind) && joint >= 0 {
			// a free joint moves as one: every end in it comes along
			board.joints[joint].x, board.joints[joint].y = px, py
		} else {
			if u.drag.hx == 0 do s.x1 = px
			else do s.x2 = px
			if u.drag.hy == 0 do s.y1 = py
			else do s.y2 = py
			if s.kind == .Box do corners_settle(index) // a linked corner is a shared vertex
		}
	case .None, .Group, .Marquee, .Laser:
	}
}

// The selection follows the pointer in whole grid steps from where it was grabbed.
group_move :: proc(u: ^User, x, y: i32) {
	d := &u.drag
	if abs32(x - d.gx) > 3 || abs32(y - d.gy) > 3 do d.moved = true
	dx := snap_delta(x - d.gx) - d.ax
	dy := snap_delta(y - d.gy) - d.ay
	if dx == 0 && dy == 0 do return
	dx, dy = clamp_to_board(&u.sel, dx, dy)
	move_selection(u, dx, dy, stretch = false)
	d.ax += dx
	d.ay += dy
}

// Move the selection: grouped shapes as rigid units, the rest either as
// corner-linked clusters (a drag) or by their unheld edges (stretch: arrow keys).
move_selection :: proc(u: ^User, dx, dy: i32, stretch: bool) {
	grouped, loose: [SHAPES_MAX]bool
	for &s, i in board.shapes {
		if !u.sel[i] || s.kind == .None do continue
		if s.group >= 0 do grouped[i] = true
		else do loose[i] = true
	}
	if stretch do stretch_shapes(&loose, dx, dy)
	else do translate_shapes(&loose, dx, dy)
	group_translate(&grouped, dx, dy)
}

// A group moves as a unit whatever its links: every member keeps its shape and
// links inside the group move with it. Outside shapes linked to a member stretch
// to follow (a line end riding a member box, a box sharing a member's corner); a
// member's line end held by something outside lets go and comes along.
group_translate :: proc(mask: ^[SHAPES_MAX]bool, dx, dy: i32) {
	if dx == 0 && dy == 0 do return
	for &s, i in board.shapes {
		if !mask[i] || !is_connector(s.kind) do continue
		for end in 0 ..< 2 {
			j := s.joint[end]
			if j < 0 || joint_inside(j, mask) do continue
			x, y := end_point(&s, end)
			s.joint[end] = -1
			set_end(&s, end, x, y)
			joint_gc(j)
		}
	}
	moved: [JOINTS_MAX]bool
	for &b, i in board.shapes {
		if !mask[i] || b.kind != .Box do continue
		b.x1 += dx
		b.x2 += dx
		b.y1 += dy
		b.y2 += dy
		for j in b.corner {
			if j < 0 || moved[j] || !joint_inside(j, mask) do continue // outside: settled below
			board.joints[j].x += dx
			board.joints[j].y += dy
			moved[j] = true
		}
	}
	for &jt, j in board.joints {
		if !jt.active || jt.box >= 0 || moved[j] || joint_has_corners(i32(j)) || !joint_inside(i32(j), mask) do continue
		jt.x += dx
		jt.y += dy
	}
	for &s, i in board.shapes {
		if !mask[i] || !is_connector(s.kind) do continue
		for end in 0 ..< 2 {
			if !end_is_free(&s, end) do continue
			ex, ey := end_point(&s, end)
			set_end(&s, end, ex + dx, ey + dy)
		}
	}
	for &b, i in board.shapes do if mask[i] && b.kind == .Box do corners_settle(i32(i))
}

// Is every member of joint j (its driving box, box corners, line ends) in mask?
joint_inside :: proc(j: i32, mask: ^[SHAPES_MAX]bool) -> bool {
	jt := &board.joints[j]
	if jt.box >= 0 && !mask[jt.box] do return false
	for &s, i in board.shapes {
		if s.kind == .None || mask[i] do continue
		if s.kind == .Box && box_in_joint(&s, j) do return false
		if is_connector(s.kind) && (s.joint[0] == j || s.joint[1] == j) do return false
	}
	return true
}

// Move every shape in mask by (dx, dy). A box brings every box corner-linked to
// it, a rigid cluster, with their corner joints; a connector moves only its free
// ends; a free link node moves when every end in it is moving.
translate_shapes :: proc(mask: ^[SHAPES_MAX]bool, dx, dy: i32) {
	boxes: [SHAPES_MAX]bool
	moved: [JOINTS_MAX]bool
	stack: [SHAPES_MAX]i32
	n := 0
	for i in 0 ..< SHAPES_MAX {
		if mask[i] && board.shapes[i].kind == .Box {
			boxes[i] = true
			stack[n] = i32(i)
			n += 1
		}
	}
	for n > 0 {
		n -= 1
		b := &board.shapes[stack[n]]
		for j in b.corner {
			if j < 0 do continue
			for &o, oi in board.shapes {
				if o.kind != .Box || boxes[oi] || !box_in_joint(&o, j) do continue
				boxes[oi] = true
				stack[n] = i32(oi)
				n += 1
			}
		}
	}
	for &b, i in board.shapes {
		if !boxes[i] do continue
		b.x1 += dx
		b.x2 += dx
		b.y1 += dy
		b.y2 += dy
		for j in b.corner {
			if j < 0 || moved[j] do continue
			board.joints[j].x += dx
			board.joints[j].y += dy
			moved[j] = true
		}
	}
	for &jt, j in board.joints {
		if !jt.active || jt.box >= 0 || moved[j] || joint_has_corners(i32(j)) do continue
		any, all := false, true
		for &s, i in board.shapes {
			if !is_connector(s.kind) do continue
			for end in 0 ..< 2 do if s.joint[end] == i32(j) {
				any = true
				if !mask[i] do all = false
			}
		}
		if any && all {
			jt.x += dx
			jt.y += dy
		}
	}
	for &s, i in board.shapes {
		if !mask[i] || !is_connector(s.kind) do continue
		for end in 0 ..< 2 {
			if !end_is_free(&s, end) do continue
			ex, ey := end_point(&s, end)
			set_end(&s, end, ex + dx, ey + dy)
		}
	}
}

// Bounding box of the shapes in mask.
mask_extents :: proc(mask: ^[SHAPES_MAX]bool) -> (x1, y1, x2, y2: i32, ok: bool) {
	x1, y1, x2, y2 = max(i32), max(i32), min(i32), min(i32)
	for &s, i in board.shapes {
		if !mask[i] || s.kind == .None do continue
		ax, ay, bx, by := s.x1, s.y1, s.x2, s.y2
		if is_connector(s.kind) {
			ax, ay = end_point(&s, 0)
			bx, by = end_point(&s, 1)
		}
		x1 = min(x1, ax, bx)
		y1 = min(y1, ay, by)
		x2 = max(x2, ax, bx)
		y2 = max(y2, ay, by)
		ok = true
	}
	return
}

// Shorten a move so the shapes in mask stay on the board.
clamp_to_board :: proc(mask: ^[SHAPES_MAX]bool, dx, dy: i32) -> (i32, i32) {
	x1, y1, x2, y2, ok := mask_extents(mask)
	if !ok do return dx, dy
	return clamp(dx, -x1, BOARD_W - x2), clamp(dy, -y1, BOARD_H - y2)
}

// snap_grid for a signed delta (snap_grid clamps negatives to 0).
SNAP_BIAS :: 2000 // a multiple of GRID larger than any delta on the board
snap_delta :: proc(d: i32) -> i32 {
	return snap_grid(d + SNAP_BIAS) - SNAP_BIAS
}

// ─── Selection ──────────────────────────────────────────────────────────────

sel_count :: proc(u: ^User) -> (n: int) {
	for i in 0 ..< SHAPES_MAX do if u.sel[i] && board.shapes[i].kind != .None do n += 1
	return
}

sel_clear :: proc(u: ^User) {
	u.sel = {}
}

// A shape is selected or deselected together with its whole group.
sel_set :: proc(u: ^User, index: i32, on: bool) {
	g := board.shapes[index].group
	if g < 0 {
		u.sel[index] = on
		return
	}
	for &s, i in board.shapes do if s.kind != .None && s.group == g do u.sel[i] = on
}

// The primary follows the selection when it narrows to one shape.
sel_primary :: proc(u: ^User) {
	if u.selected >= 0 && !u.sel[u.selected] do u.selected = -1
	if sel_count(u) != 1 do return
	for i in 0 ..< SHAPES_MAX do if u.sel[i] && board.shapes[i].kind != .None do u.selected = i32(i)
}

// A marquee dragged downward takes every shape it touches; dragged upward, only
// the shapes it fully encloses. Groups come whole.
marquee_select :: proc(u: ^User, sx, sy, ex, ey: i32) {
	sel_clear(u)
	u.selected = -1
	if abs32(ex - sx) < 4 && abs32(ey - sy) < 4 do return // a click on empty board
	touch := ey >= sy
	rx1, rx2 := min(sx, ex), max(sx, ex)
	ry1, ry2 := min(sy, ey), max(sy, ey)
	inside :: proc(x, y, rx1, ry1, rx2, ry2: i32) -> bool {
		return x >= rx1 && x <= rx2 && y >= ry1 && y <= ry2
	}
	for &s, i in board.shapes {
		hit := false
		switch s.kind {
		case .None:
			continue
		case .Box:
			bx1, bx2 := min(s.x1, s.x2), max(s.x1, s.x2)
			by1, by2 := min(s.y1, s.y2), max(s.y1, s.y2)
			if touch do hit = bx1 <= rx2 && bx2 >= rx1 && by1 <= ry2 && by2 >= ry1
			else do hit = bx1 >= rx1 && bx2 <= rx2 && by1 >= ry1 && by2 <= ry2
		case .Line, .Arrow:
			ax, ay := end_point(&s, 0)
			bx, by := end_point(&s, 1)
			a_in := inside(ax, ay, rx1, ry1, rx2, ry2)
			b_in := inside(bx, by, rx1, ry1, rx2, ry2)
			if !touch {
				hit = a_in && b_in
			} else {
				hit = a_in || b_in ||
					seg_cross(ax, ay, bx, by, rx1, ry1, rx2, ry1) ||
					seg_cross(ax, ay, bx, by, rx2, ry1, rx2, ry2) ||
					seg_cross(ax, ay, bx, by, rx2, ry2, rx1, ry2) ||
					seg_cross(ax, ay, bx, by, rx1, ry2, rx1, ry1)
			}
		}
		if hit do sel_set(u, i32(i), true)
	}
	sel_primary(u)
}

// Do segments ab and cd properly cross?
seg_cross :: proc(ax, ay, bx, by, cx, cy, dx, dy: i32) -> bool {
	side :: proc(ax, ay, bx, by, px, py: i32) -> i64 {
		v := i64(bx - ax) * i64(py - ay) - i64(by - ay) * i64(px - ax)
		return 1 if v > 0 else (-1 if v < 0 else 0)
	}
	return side(cx, cy, dx, dy, ax, ay) * side(cx, cy, dx, dy, bx, by) < 0 &&
		side(ax, ay, bx, by, cx, cy) * side(ax, ay, bx, by, dx, dy) < 0
}

// Double-click on a shape away from any link point: edit that shape's label, even
// inside a group (the group stays selected around it).
edit_at :: proc(u: ^User, x, y: i32) -> bool {
	index, _, hit := hit_test(x, y)
	if !hit do return false
	if !u.sel[index] {
		sel_clear(u)
		sel_set(u, index, true)
	}
	u.selected = index
	u.edit_seq += 1
	return true
}

shape_hue :: proc(s: ^Shape) -> u8 {
	return board.group_hue[s.group] if s.group >= 0 else s.hue
}

cmd_group :: proc(cmd: Cmd_Msg) {
	u := user_for(cmd.uid)
	if u == nil do return
	n := sel_count(u)
	if n < 2 {
		note(u, "select two or more shapes to group")
		return
	}
	used: [SHAPES_MAX]bool
	for &s in board.shapes do if s.kind != .None && s.group >= 0 do used[s.group] = true
	g := i32(-1)
	for i in 0 ..< SHAPES_MAX do if !used[i] {
		g = i32(i)
		break
	}
	if g < 0 do return
	// the group takes the primary's color, else the first member's
	first := u.selected
	if first < 0 do for i in 0 ..< SHAPES_MAX do if u.sel[i] && board.shapes[i].kind != .None {
		first = i32(i)
		break
	}
	board.group_hue[g] = shape_hue(&board.shapes[first])
	for &s, i in board.shapes do if u.sel[i] && s.kind != .None do s.group = g
	buf: [NOTE_MAX]u8
	note(u, fmt.bprintf(buf[:], "grouped %d shapes", n))
}

cmd_ungroup :: proc(cmd: Cmd_Msg) {
	u := user_for(cmd.uid)
	if u == nil do return
	for i in 0 ..< SHAPES_MAX {
		g := board.shapes[i].group
		if !u.sel[i] || board.shapes[i].kind == .None || g < 0 do continue
		for &s in board.shapes do if s.group == g do s.group = -1
	}
	note(u, "ungrouped")
}

cmd_up :: proc(cmd: Cmd_Msg) {
	u := user_for(cmd.uid)
	if u == nil do return
	if u.drag.kind == .None {
		clear_guides(u)
		return
	}
	// The up carries the release point. Moves are unthrottled and may arrive out
	// of order, so the release, not the last move to land, decides where it ends.
	cmd_move(cmd)
	clear_guides(u)
	drag := u.drag
	u.drag = Drag{}
	switch drag.kind {
	case .Marquee:
		marquee_select(u, drag.gx, drag.gy, cmd.x, cmd.y)
		return
	case .Group:
		if !drag.moved && drag.toggle >= 0 {
			sel_set(u, drag.toggle, false)
			sel_primary(u)
		}
		return
	case .Laser:
		laser_finish(u)
		return
	case .None, .Whole, .Handle:
	}
	index := drag.shape
	s := &board.shapes[index]
	if s.kind == .Box {
		// normalize, and the corner links travel with the corners they name
		if s.x1 > s.x2 {
			s.x1, s.x2 = s.x2, s.x1
			s.corner[0], s.corner[1] = s.corner[1], s.corner[0]
			s.corner[2], s.corner[3] = s.corner[3], s.corner[2]
		}
		if s.y1 > s.y2 {
			s.y1, s.y2 = s.y2, s.y1
			s.corner[0], s.corner[2] = s.corner[2], s.corner[0]
			s.corner[1], s.corner[3] = s.corner[3], s.corner[1]
		}
		if s.x2 - s.x1 < 8 || s.y2 - s.y1 < 8 do shape_remove(index) // a click, not a box
	} else if is_connector(s.kind) {
		ax, ay := end_point(s, 0)
		bx, by := end_point(s, 1)
		if dist2(ax, ay, bx, by) < 64 {
			shape_remove(index)
			return
		}
		// only the ends this drag moved link themselves: an end left on a port
		// after an unlink stays unlinked until it is dragged there again
		moved := [2]bool{drag.fresh || drag.kind == .Whole, drag.kind == .Whole}
		if drag.kind == .Handle do moved[drag.hx] = true
		linked := 0
		for end in 0 ..< 2 do if moved[end] && auto_link(index, end) do linked += 1
		if linked > 0 {
			buf: [NOTE_MAX]u8
			note(u, fmt.bprintf(buf[:], "linked %d end%s", linked, "s" if linked > 1 else ""))
		}
	}
}

// Arrow keys: the selection moves one grid step. Groups move as units; other
// shapes move only what is not held (see stretch_shapes).
cmd_nudge :: proc(cmd: Cmd_Msg) {
	u := user_for(cmd.uid)
	if u == nil || sel_count(u) == 0 do return
	if sel_count(u) == 1 && u.selected >= 0 {
		s := &board.shapes[u.selected]
		if is_connector(s.kind) && s.group < 0 && !end_is_free(s, 0) && !end_is_free(s, 1) {
			note(u, "both ends are linked")
			return
		}
	}
	dx, dy := clamp_to_board(&u.sel, i32(cmd.nx) * GRID, i32(cmd.ny) * GRID)
	move_selection(u, dx, dy, stretch = true)
}

// Arrow keys on ungrouped shapes: a box moves only the edges no outside corner
// link holds; connectors move their free ends and fully selected free link nodes.
stretch_shapes :: proc(mask: ^[SHAPES_MAX]bool, dx, dy: i32) {
	for &b, i in board.shapes {
		if !mask[i] || b.kind != .Box do continue
		held: [4]bool // corner c holds its x (x2 if c&1) and its y (y2 if c>=2)
		for c in 0 ..< 4 do held[c] = corner_held(b.corner[c], mask)
		x1, x2, y1, y2 := b.x1, b.x2, b.y1, b.y2
		if !held[0] && !held[2] do x1 += dx
		if !held[1] && !held[3] do x2 += dx
		if !held[0] && !held[1] do y1 += dy
		if !held[2] && !held[3] do y2 += dy
		if abs32(x2 - x1) < 8 do x1, x2 = b.x1, b.x2 // never collapse a box
		if abs32(y2 - y1) < 8 do y1, y2 = b.y1, b.y2
		if x1 == b.x1 && x2 == b.x2 && y1 == b.y1 && y2 == b.y2 do continue
		b.x1, b.x2, b.y1, b.y2 = x1, x2, y1, y2
		corners_settle(i32(i)) // corner links among selected boxes stay shared
	}
	lines := mask^
	for &b, i in board.shapes do if b.kind == .Box do lines[i] = false
	translate_shapes(&lines, dx, dy)
}

// A corner is held when it shares a link node with a box outside the selection.
corner_held :: proc(j: i32, sel: ^[SHAPES_MAX]bool) -> bool {
	if j < 0 do return false
	for &o, oi in board.shapes do if o.kind == .Box && !sel[oi] && box_in_joint(&o, j) do return true
	return false
}

// A free connector end released on a link node joins it; released on a box port
// it joins that port's joint, made on first use. One joint per port, so every
// line drawn to a port ends up in the same link node.
auto_link :: proc(index: i32, end: int) -> bool {
	s := &board.shapes[index]
	if !is_connector(s.kind) || !end_is_free(s, end) do return false
	ex, ey := end_point(s, end)
	eps := i64(JOINT_EPS * JOINT_EPS)
	other := s.joint[1 - end]
	for &jt, j in board.joints {
		if !jt.active || i32(j) == other do continue
		jx, jy := joint_pos(i32(j))
		if dist2(jx, jy, ex, ey) > eps do continue
		s.joint[end] = i32(j)
		set_end(s, end, jx, jy)
		return true
	}
	for i := SHAPES_MAX - 1; i >= 0; i -= 1 { // topmost box wins
		b := &board.shapes[i]
		if b.kind != .Box do continue
		for a in 0 ..< PORT_COUNT {
			px, py := anchor_point(b, u8(a))
			if dist2(px, py, ex, ey) > eps do continue
			j := joint_new()
			if j < 0 do return false
			board.joints[j].box, board.joints[j].anchor = i32(i), u8(a)
			board.joints[j].x, board.joints[j].y = px, py
			s.joint[end] = j
			set_end(s, end, px, py)
			return true
		}
	}
	return false
}

// Nearest box port or link node within SNAP_PORT, ignoring the dragged shape
// and the joints its own ends are in.
nearest_port :: proc(px, py: i32, skip: i32, skip_joints: [2]i32) -> (x, y: i32, found: bool) {
	best := i64(SNAP_PORT * SNAP_PORT)
	for &s, i in board.shapes {
		if s.kind != .Box || i32(i) == skip do continue
		for a in 0 ..< PORT_COUNT {
			cx, cy := anchor_point(&s, u8(a))
			if d := dist2(px, py, cx, cy); d <= best {
				best, x, y, found = d, cx, cy, true
			}
		}
	}
	for &jt, j in board.joints {
		if !jt.active || i32(j) == skip_joints[0] || i32(j) == skip_joints[1] do continue
		cx, cy := joint_pos(i32(j))
		if d := dist2(px, py, cx, cy); d <= best {
			best, x, y, found = d, cx, cy, true
		}
	}
	return
}

// A port is linked when a line end rides it, or (a corner) when it shares a
// link node with other boxes' corners.
port_linked :: proc(box: i32, anchor: u8) -> bool {
	if anchor < 4 && board.shapes[box].corner[anchor] >= 0 do return true
	for jt in board.joints do if jt.active && jt.box == box && jt.anchor == anchor do return true
	return false
}

// ─── Laser ──────────────────────────────────────────────────────────────────
//
// Smoothing: keep a point only LASER_STEP from the last, then join the kept
// points with quadratics between their midpoints, each kept point the control.
// The curve is C1-smooth and costs one short path per kept point, sent once.

laser_point :: proc(u: ^User, x, y: i32) {
	if u.laser_n > 0 {
		last := u.laser[u.laser_n - 1]
		if dist2(x, y, last[0], last[1]) < LASER_STEP * LASER_STEP do return
	}
	if u.laser_n == 3 {
		u.laser[0], u.laser[1] = u.laser[1], u.laser[2]
		u.laser_n = 2
	}
	u.laser[u.laser_n] = {x, y}
	u.laser_n += 1
	if u.laser_n < 3 do return
	p0, p1, p2 := u.laser[0], u.laser[1], u.laser[2]
	ax, ay := (p0[0] + p1[0]) / 2, (p0[1] + p1[1]) / 2
	if u.laser_seg == 0 do ax, ay = p0[0], p0[1] // the stroke starts at its first point
	laser_emit(u, ax, ay, p1[0], p1[1], (p1[0] + p2[0]) / 2, (p1[1] + p2[1]) / 2)
}

// The stroke's last piece runs from the last midpoint to the last point; a dot
// if the stroke never moved.
laser_finish :: proc(u: ^User) {
	switch u.laser_n {
	case 1:
		p := u.laser[0]
		laser_emit(u, p[0], p[1], p[0], p[1], p[0] + 1, p[1])
	case 2:
		p0, p1 := u.laser[0], u.laser[1]
		if u.laser_seg == 0 do laser_emit(u, p0[0], p0[1], p0[0], p0[1], p1[0], p1[1])
	case 3:
		p1, p2 := u.laser[1], u.laser[2]
		mx, my := (p1[0] + p2[0]) / 2, (p1[1] + p2[1]) / 2
		laser_emit(u, mx, my, p2[0], p2[1], p2[0], p2[1])
	}
	u.laser_n = 0
}

laser_emit :: proc(u: ^User, ax, ay, cx, cy, bx, by: i32) {
	slot := 0 // a free slot, else the oldest segment
	for &seg, i in board.lasers {
		if !seg.active {
			slot = i
			break
		}
		if seg.born < board.lasers[slot].born do slot = i
	}
	board.laser_id += 1
	board.lasers[slot] = Laser_Seg {
		active = true,
		hue    = u.hue,
		id     = board.laser_id,
		born   = board.tick,
		ax     = ax, ay = ay, cx = cx, cy = cy, bx = bx, by = by,
	}
	u.laser_seg += 1
}

// ─── Lock and unlock (L, U) ─────────────────────────────────────────────────

// L: every vertex of the selection links to whatever overlaps it. A line end
// joins a link node, a box port or other free line ends there; a box corner
// joins other boxes' corners; free line ends on the box's ports link to it.
cmd_lock :: proc(cmd: Cmd_Msg) {
	u := user_for(cmd.uid)
	if u == nil do return
	eps := i64(JOINT_EPS * JOINT_EPS)
	linked := 0
	for i in 0 ..< SHAPES_MAX {
		if !u.sel[i] do continue
		s := &board.shapes[i]
		#partial switch s.kind {
		case .Line, .Arrow:
			for end in 0 ..< 2 {
				if !end_is_free(s, end) do continue
				if auto_link(i32(i), end) {
					linked += 1
					continue
				}
				// other free line ends here: a new free link node for them all
				ex, ey := end_point(s, end)
				others := 0
				for &o, oi in board.shapes {
					if !is_connector(o.kind) do continue
					for e in 0 ..< 2 {
						if (oi == i && e == end) || !end_is_free(&o, e) do continue
						if ox, oy := end_point(&o, e); dist2(ox, oy, ex, ey) <= eps do others += 1
					}
				}
				if others == 0 do continue
				j := joint_new()
				if j < 0 do continue
				board.joints[j].x, board.joints[j].y = ex, ey
				for &o in board.shapes {
					if !is_connector(o.kind) do continue
					for e in 0 ..< 2 {
						if !end_is_free(&o, e) do continue
						if ox, oy := end_point(&o, e); dist2(ox, oy, ex, ey) <= eps {
							set_end(&o, e, ex, ey)
							o.joint[e] = j
							linked += 1
						}
					}
				}
			}
		case .Box:
			for c in 0 ..< 4 {
				if s.corner[c] < 0 && lock_corner(i32(i), c) do linked += 1
			}
			for a in 0 ..< PORT_COUNT {
				px, py := anchor_point(s, u8(a))
				for &o, oi in board.shapes {
					if !is_connector(o.kind) do continue
					for e in 0 ..< 2 {
						if !end_is_free(&o, e) do continue
						if ox, oy := end_point(&o, e); dist2(ox, oy, px, py) <= eps && auto_link(i32(oi), e) do linked += 1
					}
				}
			}
		}
	}
	buf: [NOTE_MAX]u8
	note(u, fmt.bprintf(buf[:], "locked %d node%s", linked, "" if linked == 1 else "s") if linked > 0 else "nothing overlaps to lock")
}

// Link box corner c to what is on it: an existing link node (a box-driven one
// at another box's corner becomes a shared corner node), else other boxes'
// free corners there, in a new node.
lock_corner :: proc(bi: i32, c: int) -> bool {
	b := &board.shapes[bi]
	px, py := corner_pos(b, c)
	eps := i64(JOINT_EPS * JOINT_EPS)
	for &jt, j in board.joints {
		if !jt.active do continue
		jx, jy := joint_pos(i32(j))
		if dist2(jx, jy, px, py) > eps do continue
		if jt.box >= 0 {
			if jt.box == bi || jt.anchor >= 4 do return false // our own, or a midpoint
			if board.shapes[jt.box].corner[jt.anchor] >= 0 do return false
			board.shapes[jt.box].corner[jt.anchor] = i32(j)
			jt.box = -1
		}
		jt.x, jt.y = jx, jy
		set_corner(b, c, jx, jy)
		b.corner[c] = i32(j)
		return true
	}
	j := i32(-1)
	for &o, oi in board.shapes {
		if o.kind != .Box || i32(oi) == bi do continue
		for k in 0 ..< 4 {
			if o.corner[k] >= 0 do continue
			ox, oy := corner_pos(&o, k)
			if dist2(ox, oy, px, py) > eps do continue
			if j < 0 {
				j = joint_new()
				if j < 0 do return false
				board.joints[j].x, board.joints[j].y = px, py
			}
			set_corner(&o, k, px, py)
			o.corner[k] = j
		}
	}
	if j < 0 do return false
	b.corner[c] = j
	return true
}

// U: the selection lets go of every link: line ends leave their nodes, box
// corners leave theirs, and lines riding a box are freed where they stand.
cmd_unlock :: proc(cmd: Cmd_Msg) {
	u := user_for(cmd.uid)
	if u == nil do return
	freed := 0
	for i in 0 ..< SHAPES_MAX {
		if !u.sel[i] do continue
		s := &board.shapes[i]
		#partial switch s.kind {
		case .Line, .Arrow:
			for end in 0 ..< 2 {
				j := s.joint[end]
				if j < 0 do continue
				x, y := end_point(s, end)
				s.joint[end] = -1
				set_end(s, end, x, y)
				joint_gc(j)
				freed += 1
			}
		case .Box:
			for &c in s.corner {
				j := c
				if j < 0 do continue
				c = -1
				joint_gc(j)
				freed += 1
			}
			for &jt, j in board.joints {
				if jt.active && jt.box == i32(i) do freed += joint_dissolve(i32(j)) - 1
			}
		}
	}
	buf: [NOTE_MAX]u8
	note(u, fmt.bprintf(buf[:], "unlocked %d node%s", freed, "" if freed == 1 else "s") if freed > 0 else "nothing to unlock")
}

cmd_delete :: proc(cmd: Cmd_Msg) {
	u := user_for(cmd.uid)
	if u == nil do return
	for i in 0 ..< SHAPES_MAX do if u.sel[i] && board.shapes[i].kind != .None do shape_remove(i32(i))
	if u.selected >= 0 && board.shapes[u.selected].kind != .None do shape_remove(u.selected)
}

cmd_clear :: proc() {
	for &s in board.shapes do s.kind = .None
	for &jt in board.joints do jt.active = false
	for &u in board.users {
		u.selected = -1
		u.sel = {}
		u.drag = Drag{}
		clear_guides(&u)
	}
}

// Double-click at ($lx, $ly): gather every node on that coordinate. If any of
// them is in a joint, dissolve those joints (unlink). Otherwise put them all in
// one new joint (link). Box anchors join too, but only one box can drive.
cmd_link :: proc(cmd: Cmd_Msg) {
	u := user_for(cmd.uid)
	if u == nil do return
	r2 := i64(HANDLE_R * HANDLE_R)

	// 1. the node nearest the click is the coordinate we mean
	best := r2 + 1
	tx, ty: i32
	for &s in board.shapes {
		switch s.kind {
		case .None:
		case .Box:
			for a in 0 ..< ANCHOR_COUNT {
				ax, ay := anchor_point(&s, u8(a))
				if d := dist2(cmd.x, cmd.y, ax, ay); d < best {
					best, tx, ty = d, ax, ay
				}
			}
		case .Line, .Arrow:
			for end in 0 ..< 2 {
				ex, ey := end_point(&s, end)
				if d := dist2(cmd.x, cmd.y, ex, ey); d < best {
					best, tx, ty = d, ex, ey
				}
			}
		}
	}
	if best > r2 {
		if !edit_at(u, cmd.x, cmd.y) do note(u, "no node here")
		return
	}

	// 2. unlink: any joint on this coordinate is dissolved
	eps := i64(JOINT_EPS * JOINT_EPS)
	freed := 0
	for &jt, j in board.joints {
		if !jt.active do continue
		jx, jy := joint_pos(i32(j))
		if dist2(jx, jy, tx, ty) <= eps do freed += joint_dissolve(i32(j))
	}
	if freed > 0 {
		buf: [NOTE_MAX]u8
		note(u, fmt.bprintf(buf[:], "unlinked %d nodes", freed))
		return
	}

	// 3a. box corners: two or more boxes with a corner here share one link node
	corners := 0
	for &s in board.shapes {
		if s.kind != .Box do continue
		for c in 0 ..< 4 {
			if cx, cy := corner_pos(&s, c); dist2(cx, cy, tx, ty) <= eps {
				corners += 1
				break
			}
		}
	}
	if corners >= 2 {
		j := joint_new()
		if j < 0 {
			note(u, "too many links")
			return
		}
		board.joints[j].x, board.joints[j].y = tx, ty
		nodes := 0
		for &s in board.shapes {
			if s.kind == .Box {
				for c in 0 ..< 4 {
					if cx, cy := corner_pos(&s, c); dist2(cx, cy, tx, ty) <= eps {
						set_corner(&s, c, tx, ty)
						s.corner[c] = j
						nodes += 1
						break
					}
				}
			} else if is_connector(s.kind) {
				for end in 0 ..< 2 {
					if ex, ey := end_point(&s, end); dist2(ex, ey, tx, ty) <= eps {
						set_end(&s, end, tx, ty)
						s.joint[end] = j
						nodes += 1
					}
				}
			}
		}
		buf: [NOTE_MAX]u8
		note(u, fmt.bprintf(buf[:], "linked %d nodes", nodes))
		return
	}

	// 3b. link: every free connector end and the topmost box anchor here
	ends := 0
	driver, driver_anchor := i32(-1), u8(0)
	for &s, i in board.shapes {
		switch s.kind {
		case .None:
		case .Box:
			for a in 0 ..< ANCHOR_COUNT {
				ax, ay := anchor_point(&s, u8(a))
				if dist2(ax, ay, tx, ty) <= eps {
					driver, driver_anchor = i32(i), u8(a) // later index = drawn on top
					break
				}
			}
		case .Line, .Arrow:
			for end in 0 ..< 2 {
				ex, ey := end_point(&s, end)
				if dist2(ex, ey, tx, ty) <= eps do ends += 1
			}
		}
	}
	nodes := ends + (1 if driver >= 0 else 0)
	if ends == 0 || nodes < 2 {
		// one node is no link: a double-click on a shape edits its label
		if !edit_at(u, cmd.x, cmd.y) do note(u, "only one node here: nothing to link")
		return
	}
	j := joint_new()
	if j < 0 {
		note(u, "too many links")
		return
	}
	board.joints[j].box = driver
	board.joints[j].anchor = driver_anchor
	board.joints[j].x, board.joints[j].y = tx, ty
	for &s in board.shapes {
		if !is_connector(s.kind) do continue
		for end in 0 ..< 2 {
			ex, ey := end_point(&s, end)
			if dist2(ex, ey, tx, ty) <= eps {
				set_end(&s, end, tx, ty)
				s.joint[end] = j
			}
		}
	}
	buf: [NOTE_MAX]u8
	note(u, fmt.bprintf(buf[:], "linked %d nodes", nodes))
}

// The label carries the shape it was typed for, not "whatever is selected now".
cmd_label :: proc(msg: ^Label_Msg) {
	u := user_for(msg.uid)
	if u == nil do return
	if msg.shape < 0 || msg.shape >= SHAPES_MAX do return
	s := &board.shapes[msg.shape]
	if s.kind == .None do return
	n := int(min(msg.len, LABEL_MAX))
	old := escaped_len(s.label[:s.label_len])
	grow := escaped_len(msg.label[:n]) - old
	if s.label_len == 0 do grow += 200 // the foreignObject wrapper
	if grow > 0 && !frame_fits(grow) {
		note(u, "board is full: label not saved")
		return
	}
	copy(s.label[:], msg.label[:n])
	s.label_len = u8(n)
}

// ─── Geometry ───────────────────────────────────────────────────────────────

abs32 :: proc(v: i32) -> i32 {
	return -v if v < 0 else v
}

snap_grid :: proc(v: i32) -> i32 {
	return ((v + GRID / 2) / GRID) * GRID if v >= 0 else 0
}

dist2 :: proc(ax, ay, bx, by: i32) -> i64 {
	dx := i64(ax - bx)
	dy := i64(ay - by)
	return dx * dx + dy * dy
}

// squared distance from p to segment ab
seg_dist2 :: proc(px, py, ax, ay, bx, by: i32) -> i64 {
	vx, vy := i64(bx - ax), i64(by - ay)
	wx, wy := i64(px - ax), i64(py - ay)
	len2 := vx * vx + vy * vy
	if len2 == 0 do return wx * wx + wy * wy
	t := (wx * vx + wy * vy)
	if t <= 0 do return wx * wx + wy * wy
	if t >= len2 do return dist2(px, py, bx, by)
	cx := i64(ax) + vx * t / len2
	cy := i64(ay) + vy * t / len2
	dx, dy := i64(px) - cx, i64(py) - cy
	return dx * dx + dy * dy
}

// Topmost shape under (x, y): which shape, and how a drag from here behaves.
// Pinned things (an end in a box-driven joint, a body with no free end) are
// selected but not dragged.
hit_test :: proc(x, y: i32) -> (index: i32, drag: Drag, hit: bool) {
	r2 := i64(HANDLE_R * HANDLE_R)
	for i := SHAPES_MAX - 1; i >= 0; i -= 1 {
		s := &board.shapes[i]
		switch s.kind {
		case .None:
			continue
		case .Line, .Arrow:
			ax, ay := end_point(s, 0)
			bx, by := end_point(s, 1)
			for end in 0 ..< 2 {
				ex, ey := end_point(s, end)
				if dist2(x, y, ex, ey) > r2 do continue
				j := s.joint[end]
				if j >= 0 && joint_pinned(j) do return i32(i), Drag{}, true
				return i32(i), Drag{kind = .Handle, shape = i32(i), hx = u8(end), hy = u8(end)}, true
			}
			if seg_dist2(x, y, ax, ay, bx, by) <= i64(LINE_HIT * LINE_HIT) {
				ref := 0 if end_is_free(s, 0) else 1
				if !end_is_free(s, ref) do return i32(i), Drag{}, true
				rx, ry := end_point(s, ref)
				return i32(i), Drag{kind = .Whole, shape = i32(i), hx = u8(ref), gx = x - rx, gy = y - ry}, true
			}
		case .Box:
			if dist2(x, y, s.x1, s.y1) <= r2 do return i32(i), Drag{kind = .Handle, shape = i32(i), hx = 0, hy = 0}, true
			if dist2(x, y, s.x2, s.y1) <= r2 do return i32(i), Drag{kind = .Handle, shape = i32(i), hx = 1, hy = 0}, true
			if dist2(x, y, s.x1, s.y2) <= r2 do return i32(i), Drag{kind = .Handle, shape = i32(i), hx = 0, hy = 1}, true
			if dist2(x, y, s.x2, s.y2) <= r2 do return i32(i), Drag{kind = .Handle, shape = i32(i), hx = 1, hy = 1}, true
			if x >= min(s.x1, s.x2) && x <= max(s.x1, s.x2) && y >= min(s.y1, s.y2) && y <= max(s.y1, s.y2) {
				return i32(i), Drag{kind = .Whole, shape = i32(i), gx = x - s.x1, gy = y - s.y1}, true
			}
		}
	}
	return -1, Drag{}, false
}

// Nearest anchor within SNAP_ANCHOR, ignoring the dragged shape, the joint
// being dragged, and any end that rides on the dragged box (its anchors are
// where the box already is, so snapping to them would pin the corner).
nearest_anchor :: proc(px, py: i32, skip: i32, skip_joint: i32) -> (ax, ay: i32, found: bool) {
	best := i64(SNAP_ANCHOR * SNAP_ANCHOR)
	consider :: proc(px, py, cx, cy: i32, best: ^i64, ax, ay: ^i32, found: ^bool) {
		d := dist2(px, py, cx, cy)
		if d <= best^ {
			best^ = d
			ax^, ay^ = cx, cy
			found^ = true
		}
	}
	for &s, i in board.shapes {
		if i32(i) == skip || s.kind == .None do continue
		if s.kind == .Box {
			if skip_joint >= 0 && box_in_joint(&s, skip_joint) do continue // it moves with us
			for a in 0 ..< ANCHOR_COUNT {
				cx, cy := anchor_point(&s, u8(a))
				consider(px, py, cx, cy, &best, &ax, &ay, &found)
			}
			continue
		}
		for end in 0 ..< 2 {
			j := s.joint[end]
			if j >= 0 && (j == skip_joint || board.joints[j].box == skip) do continue
			ex, ey := end_point(&s, end)
			consider(px, py, ex, ey, &best, &ax, &ay, &found)
		}
		x1, y1 := end_point(&s, 0)
		x2, y2 := end_point(&s, 1)
		consider(px, py, (x1 + x2) / 2, (y1 + y2) / 2, &best, &ax, &ay, &found)
	}
	return
}

// ─── Renderer: the whole <svg id="board"> for one viewer, into frame_buf ────

Writer :: struct {
	buf:      []u8,
	n:        int,
	overflow: bool,
}

w_str :: proc(w: ^Writer, s: string) {
	if w.n + len(s) > len(w.buf) {
		w.overflow = true
		return
	}
	copy(w.buf[w.n:], s)
	w.n += len(s)
}

w_int :: proc(w: ^Writer, v: i32) {
	tmp: [16]u8
	w_str(w, strconv.write_int(tmp[:], i64(v), 10))
}

w_attr :: proc(w: ^Writer, name: string, v: i32) {
	w_str(w, " ")
	w_str(w, name)
	w_str(w, "=\"")
	w_int(w, v)
	w_str(w, "\"")
}

w_hue :: proc(w: ^Writer, hue: u8) {
	w_str(w, " style=\"--hue-shift:")
	w_int(w, i32(hue) * HUE_STEP)
	w_str(w, "\"")
}

w_circle :: proc(w: ^Writer, cx, cy, r: i32, extra: string) {
	w_str(w, "<circle")
	w_attr(w, "cx", cx)
	w_attr(w, "cy", cy)
	w_attr(w, "r", r)
	w_str(w, extra)
	w_str(w, "/>")
}

w_html :: proc(w: ^Writer, s: string) {
	for c in transmute([]u8)s {
		switch c {
		case '<':
			w_str(w, "&lt;")
		case '>':
			w_str(w, "&gt;")
		case '&':
			w_str(w, "&amp;")
		case '"':
			w_str(w, "&quot;")
		case:
			if c >= 32 do w_str(w, string([]u8{c}))
		}
	}
}

w_json_string :: proc(w: ^Writer, s: string) {
	w_str(w, "\"")
	for c in transmute([]u8)s {
		switch c {
		case '"':
			w_str(w, "\\\"")
		case '\\':
			w_str(w, "\\\\")
		case:
			if c >= 32 do w_str(w, string([]u8{c}))
			else do w_str(w, " ")
		}
	}
	w_str(w, "\"")
}

// a label as a foreignObject the engine types; centered in the given box
w_label :: proc(w: ^Writer, s: ^Shape, x, y, width, height: i32) {
	if s.label_len == 0 do return
	w_str(w, "<foreignObject")
	w_attr(w, "x", x)
	w_attr(w, "y", y)
	w_attr(w, "width", width)
	w_attr(w, "height", height)
	w_str(w, `><div xmlns="http://www.w3.org/1999/xhtml" class="label"><span>`)
	w_html(w, string(s.label[:s.label_len]))
	w_str(w, "</span></div></foreignObject>")
}

// A box's eight ports, corners and midpoints alike; a linked port is filled.
w_ports :: proc(w: ^Writer, box: i32) {
	b := &board.shapes[box]
	w_str(w, `<g class="ports"`)
	w_hue(w, shape_hue(b))
	w_str(w, ">")
	for a in 0 ..< PORT_COUNT {
		px, py := anchor_point(b, u8(a))
		w_circle(w, px, py, 5, ` class="port" data-ui-linked` if port_linked(box, u8(a)) else ` class="port"`)
	}
	w_str(w, "</g>")
}

render_frame :: proc(viewer_uid: u32) -> (frame: string, ok: bool) {
	w := Writer {
		buf = frame_buf[:],
	}
	viewer: ^User
	for &u in board.users {
		if u.active && u.uid == viewer_uid {
			viewer = &u
			break
		}
	}
	selected := i32(-1)
	count := 0
	if viewer != nil {
		selected = viewer.selected
		count = sel_count(viewer)
	}

	w_str(&w, `<svg id="board" xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1000 1000">`)

	// shapes
	w_str(&w, `<g class="shapes">`)
	for &s, i in board.shapes {
		if s.kind == .None do continue
		w_str(&w, "<g")
		w_hue(&w, shape_hue(&s))
		if viewer != nil && viewer.sel[i] do w_str(&w, " data-ui-selected")
		w_str(&w, ">")
		switch s.kind {
		case .Box:
			x1, x2 := min(s.x1, s.x2), max(s.x1, s.x2)
			y1, y2 := min(s.y1, s.y2), max(s.y1, s.y2)
			w_str(&w, "<rect")
			w_attr(&w, "x", x1)
			w_attr(&w, "y", y1)
			w_attr(&w, "width", x2 - x1)
			w_attr(&w, "height", y2 - y1)
			w_str(&w, ` rx="6"/>`)
			for c in 0 ..< 4 {
				if s.corner[c] < 0 do continue
				cx, cy := corner_pos(&s, c)
				w_circle(&w, cx, cy, 4, ` class="node" data-ui-linked`)
			}
			w_label(&w, &s, x1, y1, x2 - x1, y2 - y1)
		case .Line, .Arrow:
			ax, ay := end_point(&s, 0)
			bx, by := end_point(&s, 1)
			lx, ly := bx, by // where the stroke ends (short of an arrow head)
			if s.kind == .Arrow {
				dx, dy := f32(bx - ax), f32(by - ay)
				length := math.sqrt(dx * dx + dy * dy)
				if length > 1 {
					ux, uy := dx / length, dy / length
					base_x, base_y := f32(bx) - ux * ARROW_LEN, f32(by) - uy * ARROW_LEN
					lx, ly = i32(base_x), i32(base_y)
					w_str(&w, `<path class="head" d="M`)
					w_int(&w, bx)
					w_str(&w, " ")
					w_int(&w, by)
					w_str(&w, " L")
					w_int(&w, i32(base_x - uy * ARROW_HALF))
					w_str(&w, " ")
					w_int(&w, i32(base_y + ux * ARROW_HALF))
					w_str(&w, " L")
					w_int(&w, i32(base_x + uy * ARROW_HALF))
					w_str(&w, " ")
					w_int(&w, i32(base_y - ux * ARROW_HALF))
					w_str(&w, ` Z"/>`)
				}
			}
			w_str(&w, "<line")
			w_attr(&w, "x1", ax)
			w_attr(&w, "y1", ay)
			w_attr(&w, "x2", lx)
			w_attr(&w, "y2", ly)
			w_str(&w, "/>")
			// end dots: hollow when free, filled when linked to a box
			w_circle(&w, ax, ay, 4, ` class="node" data-ui-linked` if s.joint[0] >= 0 else ` class="node"`)
			w_circle(&w, bx, by, 4, ` class="node" data-ui-linked` if s.joint[1] >= 0 else ` class="node"`)
			w_label(&w, &s, (ax + bx) / 2 - 90, (ay + by) / 2 - 16, 180, 32)
		case .None:
		}
		w_str(&w, "</g>")
	}
	w_str(&w, "</g>")

	// ports: the selected box's, and the box a dragged connector end is near
	if count == 1 && selected >= 0 && board.shapes[selected].kind == .Box do w_ports(&w, selected)
	// a multi-selection's extents, dotted
	if count >= 2 {
		if x1, y1, x2, y2, ok := mask_extents(&viewer.sel); ok {
			w_str(&w, `<rect class="extents"`)
			w_attr(&w, "x", x1 - EXTENTS_PAD)
			w_attr(&w, "y", y1 - EXTENTS_PAD)
			w_attr(&w, "width", x2 - x1 + 2 * EXTENTS_PAD)
			w_attr(&w, "height", y2 - y1 + 2 * EXTENTS_PAD)
			w_str(&w, "/>")
		}
	}
	if viewer != nil && viewer.drag.kind == .Handle && is_connector(board.shapes[viewer.drag.shape].kind) {
		for i := i32(SHAPES_MAX - 1); i >= 0; i -= 1 {
			b := &board.shapes[i]
			if b.kind != .Box || i == selected do continue
			reach := i32(SNAP_PORT * 2)
			if viewer.x >= min(b.x1, b.x2) - reach && viewer.x <= max(b.x1, b.x2) + reach &&
			   viewer.y >= min(b.y1, b.y2) - reach && viewer.y <= max(b.y1, b.y2) + reach {
				w_ports(&w, i)
				break
			}
		}
	}

	// the viewer's selection handles, for a single shape
	if count == 1 && selected >= 0 && board.shapes[selected].kind != .None {
		s := &board.shapes[selected]
		w_str(&w, `<g class="handles"`)
		w_hue(&w, shape_hue(s))
		w_str(&w, ">")
		if is_connector(s.kind) { // a box's handles are its ports, drawn above
			ax, ay := end_point(s, 0)
			bx, by := end_point(s, 1)
			w_circle(&w, ax, ay, 6, "")
			w_circle(&w, bx, by, 6, "")
		}
		w_str(&w, "</g>")
	}

	// the viewer's snap guides
	if viewer != nil && (viewer.guide_vx >= 0 || viewer.guide_hy >= 0 || viewer.anchor_on) {
		w_str(&w, `<g class="guides inf">`)
		if viewer.guide_vx >= 0 {
			w_str(&w, `<line class="guide"`)
			w_attr(&w, "x1", viewer.guide_vx)
			w_attr(&w, "y1", 0)
			w_attr(&w, "x2", viewer.guide_vx)
			w_attr(&w, "y2", BOARD_H)
			w_str(&w, "/>")
		}
		if viewer.guide_hy >= 0 {
			w_str(&w, `<line class="guide"`)
			w_attr(&w, "x1", 0)
			w_attr(&w, "y1", viewer.guide_hy)
			w_attr(&w, "x2", BOARD_W)
			w_attr(&w, "y2", viewer.guide_hy)
			w_str(&w, "/>")
		}
		if viewer.anchor_on do w_circle(&w, viewer.anchor_x, viewer.anchor_y, 8, ` class="anchor"`)
		w_str(&w, "</g>")
	}

	// laser segments, each under a stable id so the morph only appends them;
	// skipped rather than overflow the frame
	w_str(&w, `<g class="lasers">`)
	for &seg in board.lasers {
		if !seg.active do continue
		if w.n + 160 > FRAME_MAX - 6_000 do break
		w_str(&w, `<g class="laser" id="lz`)
		w_int(&w, i32(seg.id))
		w_str(&w, `"`)
		w_hue(&w, seg.hue)
		w_str(&w, `><path d="M`)
		w_int(&w, seg.ax)
		w_str(&w, " ")
		w_int(&w, seg.ay)
		w_str(&w, "Q")
		w_int(&w, seg.cx)
		w_str(&w, " ")
		w_int(&w, seg.cy)
		w_str(&w, " ")
		w_int(&w, seg.bx)
		w_str(&w, " ")
		w_int(&w, seg.by)
		w_str(&w, `"/></g>`)
	}
	w_str(&w, "</g>")

	// everyone else's cursor
	w_str(&w, `<g class="cursors">`)
	for u in board.users {
		if !u.active || u.uid == viewer_uid do continue
		w_str(&w, `<g class="cursor" transform="translate(`)
		w_int(&w, u.x)
		w_str(&w, ",")
		w_int(&w, u.y)
		w_str(&w, ")\"")
		w_hue(&w, u.hue)
		w_str(&w, `><path d="M0 0v16l4-3.5 2.5 6 3-1.3-2.6-6H11z"/></g>`)
	}
	w_str(&w, "</g></svg>")

	if w.overflow do return "", false
	return string(frame_buf[:w.n]), true
}

count_shapes :: proc() -> (shapes, users: i32) {
	for s in board.shapes do if s.kind != .None do shapes += 1
	for u in board.users do if u.active do users += 1
	return
}

// ─── HTTP: the page ─────────────────────────────────────────────────────────

// mike.css v3.2.3, committed copy per the css repo's own rule ("a product commits
// a copy and never edits it in place"), embedded into the binary at build time.
MIKE_CSS :: #load("mike.css", string)

INDEX_HTML :: `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>whiteboard</title>
<link rel="stylesheet" href="/mike.css">
<script type="module" src="https://cdn.jsdelivr.net/gh/starfederation/datastar@v1.0.4/bundles/datastar.js"></script>
<script>
  // the server's snap_grid, for the ghost
  window.gridSnap = v => v < 0 ? 0 : Math.floor((v + 10) / 20) * 20
</script>
<style>
@layer block {
  :where(.pg-header) { --bg: 0.08; padding: 0.25lh 0.5em; }
  :where(.pg-footer) { --bg: 0.08; padding: 0.25lh 0.5em; --fg: -0.4; }
  /* the section is a size container, so the stage can be the largest square that fits it */
  :where(.pg-main) > :where(section) { display: grid; place-items: center; block-size: 100%; container-type: size; }
  :where(.tools) > :where(button) { --type: -1; }
  :where(.tools) kbd { --fg: -0.5; margin-inline-start: 0.25em; }
  :where(#label) { inline-size: 14em; }
  :where(.status) { --fg: -0.4; }
  :where(.theme) > :where(label) { display: inline-flex; align-items: center; gap: 0.5em; }
  :where(.theme) input[type="range"] { inline-size: 9em; }
  :where(#stage) {
    position: relative; inline-size: min(100cqi, 100cqb); aspect-ratio: 1; touch-action: none; user-select: none;
    --fg: 0.75; border: 1px solid color-mix(in oklch, currentColor 28%, transparent); border-radius: var(--cfg-radius, .5rem); overflow: hidden;
  }
  /* the grid is a static SVG layer under the streamed board: never morphed, never re-sent */
  :where(#stage) > :where(svg) { position: absolute; inset: 0; inline-size: 100%; block-size: 100%; display: block; }
  /* strokes keep their pixel width however big the square stage is drawn */
  :where(#stage) :where(svg) * { vector-effect: non-scaling-stroke; }
  :where(#stage) .plus  { fill: none; stroke: color-mix(in oklch, currentColor 40%, transparent); stroke-width: 1; }
  :where(#stage) .major { fill: none; stroke: color-mix(in oklch, currentColor 22%, transparent); stroke-width: 1; }
  :where(.note) { --fg: 0.6; }
  :where(#board) rect { fill: color-mix(in oklch, currentColor 14%, transparent); stroke: currentColor; stroke-width: 2; }
  :where(#board) line { stroke: currentColor; stroke-width: 2; stroke-linecap: round; }
  :where(#board) .head { fill: currentColor; stroke: none; }
  :where(#board) .node { fill: color-mix(in oklch, currentColor 10%, Canvas); stroke: currentColor; stroke-width: 1.5; }
  :where(#board) .node[data-ui-linked] { fill: currentColor; }
  :where(#board) [data-ui-selected] { --fg: 1; }
  :where(#board) [data-ui-selected] rect, :where(#board) [data-ui-selected] line { stroke-width: 3; }
  :where(#board) .handles circle { fill: color-mix(in oklch, currentColor 35%, transparent); stroke: currentColor; stroke-width: 2; }
  :where(#board) .guide { stroke: currentColor; stroke-width: 1; stroke-dasharray: 6 6; }
  :where(#board) .anchor { fill: none; stroke: currentColor; stroke-width: 2; }
  :where(#ghost) { pointer-events: none; }
  :where(#ghost) .head { fill: currentColor; stroke: none; }
  /* marquee: dashed while it takes what it touches (dragged down), solid when it takes only what it encloses (dragged up) */
  :where(#ghost) rect.marquee { fill: color-mix(in oklch, currentColor 6%, transparent); stroke-width: 1; }
  :where(#ghost) rect.enclose { stroke-dasharray: none; }
  :where(#ghost) rect.extents, :where(#board) .extents { fill: none; stroke: currentColor; stroke-width: 1.5; stroke-dasharray: 0 6; stroke-linecap: round; }
  :where(#board) .port { fill: Canvas; stroke: currentColor; stroke-width: 2; }
  :where(#board) .port[data-ui-linked] { fill: currentColor; }
  :where(#ghost) rect, :where(#ghost) line { fill: color-mix(in oklch, currentColor 8%, transparent); stroke: currentColor; stroke-width: 2; stroke-dasharray: 6 4; stroke-linecap: round; }
  /* the laser fades by its ink: --fg is a registered number, so it animates, and 0 is the surface */
  @keyframes laser-fade { from { --fg: 1; } to { --fg: 0; } }
  :where(#board) .laser { animation: laser-fade 1.2s ease-in forwards; }
  :where(#board) .laser path { fill: none; stroke: currentColor; stroke-width: 4; stroke-linecap: round; stroke-linejoin: round; }
  :where(#board) .cursor path { fill: currentColor; stroke: none; }
  :where(#board) .label { display: flex; align-items: center; justify-content: center; block-size: 100%; inline-size: 100%; text-align: center; padding: 0.25em; overflow: hidden; pointer-events: none; --type: 1; }
  :where(#board) .label > span { background: color-mix(in oklch, Canvas 70%, transparent); padding: 0 0.3em; border-radius: 0.3em; }
}
</style>
</head>
<body data-signals="{uid: 0, x: 0, y: 0, lx: 0, ly: 0, tool: 0, sel: -1, label: '', note: '', shapes: 0, users: 0, drag: false, did: 0, ndx: 0, ndy: 0, nsel: 0, grouped: false, edit: 0, theme: 'system', hue: 255, _gd: {id: -1, k: 0, raw: 0, m0: 0, v0: 0, g0: 0, o0: 0, m1: 0, v1: 0, g1: 0, o1: 0, m2: 0, v2: 0, g2: 0, o2: 0, m3: 0, v3: 0, g3: 0, o3: 0}, _tk: 0, _down: false, _moved: false, _sx: 0, _sy: 0, _px: 0, _py: 0}"
      data-persist:whiteboard="{include: /^(theme|hue)$/}"
      data-init="$uid = Math.floor(Math.random() * 4294967295); @get('/stream')"
      data-effect="$theme === 'system' ? document.documentElement.removeAttribute('data-ui-theme') : document.documentElement.setAttribute('data-ui-theme', $theme); document.documentElement.style.setProperty('--hue', $hue)"
      data-computed:_live="$_gd.id === $did && $_gd.k > 0"
      data-computed:_gk="$_live ? $_gd.k : ($_tk <= 3 ? $_tk : 0)"
      data-computed:_ax="$_live ? ($_gd.m0 ? ($_gd.raw ? $_px : gridSnap($_px - $_gd.g0) + $_gd.o0) : $_gd.v0) : gridSnap($_sx)"
      data-computed:_ay="$_live ? ($_gd.m1 ? ($_gd.raw ? $_py : gridSnap($_py - $_gd.g1) + $_gd.o1) : $_gd.v1) : gridSnap($_sy)"
      data-computed:_bx="$_live ? ($_gd.m2 ? ($_gd.raw ? $_px : gridSnap($_px - $_gd.g2) + $_gd.o2) : $_gd.v2) : gridSnap($_px)"
      data-computed:_by="$_live ? ($_gd.m3 ? ($_gd.raw ? $_py : gridSnap($_py - $_gd.g3) + $_gd.o3) : $_gd.v3) : gridSnap($_py)"
      data-computed:_show="$_live ? ($_moved || $_tk > 0) && ($_down || $drag) : $_tk > 0 && $_tk <= 3 && $_down"
      data-on:keydown__window="const k = evt.key, t = evt.target; if (t.id === 'label') { if (k === 'Enter' || k === 'Escape') { evt.preventDefault(); @post('/label'); t.blur() } } else if (t.tagName === 'INPUT' || t.tagName === 'SELECT') {} else if (evt.target.tagName === 'BUTTON' && k === 'Enter') {} else if (k === 'Enter' && $sel >= 0) { evt.preventDefault(); document.getElementById('label').focus(); document.getElementById('label').select() } else if (k.startsWith('Arrow') && $nsel > 0) { evt.preventDefault(); $ndx = k === 'ArrowLeft' ? -1 : k === 'ArrowRight' ? 1 : 0; $ndy = k === 'ArrowUp' ? -1 : k === 'ArrowDown' ? 1 : 0; @post('/nudge', {requestCancellation: 'disabled'}) } else if (k === 'Delete' || k === 'Backspace') { @post('/delete') } else if (k === 'g') { @post('/group') } else if (k === 'G') { @post('/ungroup') } else if (k === 'l' && $nsel > 0) { @post('/lock') } else if (k === 'u' && $nsel > 0) { @post('/unlock') } else if (k >= '1' && k <= '5') { $tool = Number(k) - 1 }">
<div class="page">
  <header class="pg-header spread">
    <div class="row tools">
      <h3>whiteboard</h3>
      <button data-on:click="$tool = 0" data-attr:aria-pressed="$tool === 0 ? 'true' : 'false'">select<kbd>1</kbd></button>
      <button data-on:click="$tool = 1" data-attr:aria-pressed="$tool === 1 ? 'true' : 'false'">box<kbd>2</kbd></button>
      <button data-on:click="$tool = 2" data-attr:aria-pressed="$tool === 2 ? 'true' : 'false'">line<kbd>3</kbd></button>
      <button data-on:click="$tool = 3" data-attr:aria-pressed="$tool === 3 ? 'true' : 'false'">arrow<kbd>4</kbd></button>
      <button data-on:click="$tool = 4" data-attr:aria-pressed="$tool === 4 ? 'true' : 'false'">laser<kbd>5</kbd></button>
      <input id="label" type="text" placeholder="label of the selected shape" maxlength="48" data-bind:label data-attr:disabled="$sel < 0" data-on:input__debounce.200ms="@post('/label')" data-effect="$edit > 0 && setTimeout(() => { el.focus(); el.select() })">
      <button data-on:click="@post('/group')" data-attr:disabled="$nsel < 2">group<kbd>G</kbd></button>
      <button data-on:click="@post('/ungroup')" data-attr:disabled="!$grouped">ungroup<kbd>&#8679;G</kbd></button>
      <button data-on:click="@post('/lock')" data-attr:disabled="$nsel < 1">lock<kbd>L</kbd></button>
      <button data-on:click="@post('/unlock')" data-attr:disabled="$nsel < 1">unlock<kbd>U</kbd></button>
      <button data-on:click="@post('/delete')" data-attr:disabled="$nsel < 1">delete<kbd>Del</kbd></button>
      <button class="dgr" data-on:click="@post('/clear')">clear</button>
    </div>
    <span class="status"><span class="note" data-text="$note"></span> <span data-text="$shapes"></span> shapes &middot; <span data-text="$users"></span> here</span>
  </header>
  <main class="pg-main">
    <section>
      <div id="stage"
           data-on:pointerdown="evt.preventDefault(); const r = stage.getBoundingClientRect(); $x = Math.round((evt.clientX - r.left) * 1000 / r.width); $y = Math.round((evt.clientY - r.top) * 1000 / r.height); $did = $did + 1; $_tk = $tool; $_down = true; $_moved = false; $_sx = $x; $_sy = $y; $_px = $x; $_py = $y; @post('/down', {requestCancellation: 'disabled'})"
           data-on:pointermove__window="const r = stage.getBoundingClientRect(); $x = Math.round((evt.clientX - r.left) * 1000 / r.width); $y = Math.round((evt.clientY - r.top) * 1000 / r.height); if ($_down) { $_px = $x; $_py = $y; $_moved = true } @post('/move')"
           data-on:pointerup__window="const r = stage.getBoundingClientRect(); $x = Math.round((evt.clientX - r.left) * 1000 / r.width); $y = Math.round((evt.clientY - r.top) * 1000 / r.height); if ($_down) { $_px = $x; $_py = $y } $_down = false; @post('/up', {requestCancellation: 'disabled'})"
           data-on:dblclick="const r = stage.getBoundingClientRect(); $lx = Math.round((evt.clientX - r.left) * 1000 / r.width); $ly = Math.round((evt.clientY - r.top) * 1000 / r.height); @post('/link', {requestCancellation: 'disabled'})">
        <svg class="grid" xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1000 1000" aria-hidden="true">
          <defs>
            <!-- each pattern is offset by half a tile so its mark lands ON the snap lattice (multiples of 20 and 100) -->
            <pattern id="minor" width="20" height="20" x="-10" y="-10" patternUnits="userSpaceOnUse"><path class="plus" d="M7 10h6M10 7v6"/></pattern>
            <pattern id="major" width="100" height="100" x="-50" y="-50" patternUnits="userSpaceOnUse"><path class="major" d="M50 0v100M0 50h100"/></pattern>
          </defs>
          <rect width="1000" height="1000" fill="url(#major)"/>
          <rect width="1000" height="1000" fill="url(#minor)"/>
        </svg>
        <svg id="board" xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1000 1000"></svg>
        <!-- the ghost: this page's own drag, drawn from local signals at pointer speed
             (new shapes at once, moves and resizes once $_gd describes them); it
             stays until the stream's frame says the drag is done -->
        <svg id="ghost" xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1000 1000" aria-hidden="true" style="display: none" data-show="$_show">
          <defs><marker id="ghost-head" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="8" markerHeight="8" markerUnits="userSpaceOnUse" orient="auto"><path class="head" d="M0 0L10 5L0 10z"/></marker></defs>
          <rect rx="6" data-show="$_gk === 1 || $_gk >= 4" data-class:marquee="$_gk === 4" data-class:enclose="$_gk === 4 && $_by < $_ay" data-class:extents="$_gk === 5" data-attr:x="Math.min($_ax, $_bx)" data-attr:y="Math.min($_ay, $_by)" data-attr:width="Math.abs($_bx - $_ax)" data-attr:height="Math.abs($_by - $_ay)"/>
          <line data-show="$_gk === 2 || $_gk === 3" data-attr:x1="$_ax" data-attr:y1="$_ay" data-attr:x2="$_bx" data-attr:y2="$_by" data-attr:marker-end="$_gk === 3 ? 'url(#ghost-head)' : 'none'"/>
        </svg>
      </div>
    </section>
  </main>
  <footer class="pg-footer spread">
    <span>Draw with box, line or arrow; a line end dropped on a box port (&#9670;) or a link node links itself. Drag on empty board to select: downward takes what it touches, upward only what it encloses; then clicks toggle. Double-click where nodes or corners meet to link or unlink them, anywhere else on a shape to edit its label. Arrows nudge; Enter edits the label, Enter again returns. L locks the selection onto what overlaps it, U unlocks it. Laser (5) draws a trail that fades.</span>
    <div class="row theme">
      <label>theme <select data-bind:theme><option value="system">system</option><option value="light">light</option><option value="dark">dark</option></select></label>
      <label>hue <input type="range" min="0" max="360" data-bind:hue> <span data-text="$hue"></span></label>
    </div>
  </footer>
</div>
</body>
</html>
`

index :: proc(request: ^http.Request, response: ^http.Response) -> http.Route_Step {
	return http.respond_bytes(
		response,
		http.HTTP_STATUS_OK,
		"text/html; charset=utf-8",
		transmute([]u8)string(INDEX_HTML),
	)
}

// 216 KB is bigger than one response reservation, so it is streamed: write
// what fits, flush, write more on each Send_Ready until the file is done.
Stylesheet_State :: struct {
	offset: int,
}

stylesheet :: proc(
	event: http.Route_Event,
	request: ^http.Request,
	response: ^http.Response,
	route_context: http.Route_Context,
	state: rawptr,
) -> http.Route_Step {
	st := cast(^Stylesheet_State)state
	#partial switch ev in event {
	case http.Request_Start:
		st.offset = 0
		_ = http.header_set(response, "Cache-Control", "public, max-age=31536000, immutable")
		if http.begin_stream(response, http.HTTP_STATUS_OK, "text/css; charset=utf-8") != .Begun do return http.close()
		return stylesheet_write(st, response)
	case http.Send_Ready:
		return stylesheet_write(st, response)
	}
	return http.close()
}

stylesheet_write :: proc(st: ^Stylesheet_State, response: ^http.Response) -> http.Route_Step {
	data := transmute([]u8)MIKE_CSS
	for st.offset < len(data) {
		n := int(http.write_bytes(response, data[st.offset:]))
		if n == 0 do break
		st.offset += n
	}
	return http.flush(final = st.offset >= len(data))
}

// ─── HTTP: the stream ───────────────────────────────────────────────────────

Stream_State :: struct {
	uid:           u32,
	subscribed:    bool,
	sent_version:  u32,
	sent_selected: i32,
	sent_note:     u32,
	sent_drag:     i8, // -1 = never sent
	sent_drag_id:  i32,
	sent_edit:     u32,
}

stream :: proc(
	event: http.Route_Event,
	request: ^http.Request,
	response: ^http.Response,
	route_context: http.Route_Context,
	state: rawptr,
) -> http.Route_Step {
	st := cast(^Stream_State)state

	switch ev in event {
	case http.Request_Start:
		st^ = Stream_State {
			sent_selected = -2,
			sent_drag     = -1,
			sent_drag_id  = -1,
		}
		if board_handle == tina.ISOLATE_HANDLE_NONE {
			return http.respond_text(response, http.HTTP_STATUS_SERVICE_UNAVAILABLE, "board not ready")
		}
		signals: Signals
		if datastar.read_signals(request, &signals) == .None do st.uid = signals.Uid
		sse, start_error := datastar.start_sse(response)
		if start_error != .None do return http.close()
		if datastar.patch_signals(&sse, `{"live": true}`) != .None do return http.close()
		return http.flush()

	case http.Send_Ready:
		return subscribe_and_park(st, route_context)

	case http.Application_Notification:
		feed := (cast(^Feed_Msg)raw_data(ev.payload_bytes))^
		if feed.request_token != http.route_request_token(route_context) || feed.version == st.sent_version {
			return park(route_context)
		}
		frame, ok := render_frame(st.uid)
		if !ok do return park(route_context) // cannot happen while frame_fits guards growth; retried next tick
		st.sent_version = feed.version
		sse := datastar.resume(response)
		if datastar.patch_elements(&sse, frame) != .None do return http.close()

		// counts; this viewer's selection and its label when it changes; this viewer's note
		shapes, users := count_shapes()
		viewer: ^User
		for &u in board.users do if u.active && u.uid == st.uid { viewer = &u; break }
		selected := i32(-1)
		if viewer != nil do selected = viewer.selected
		if selected >= 0 && board.shapes[selected].kind == .None do selected = -1
		sw := Writer {
			buf = make([]u8, 1024, http.request_scratch(request)),
		}
		w_str(&sw, `{"shapes": `)
		w_int(&sw, shapes)
		w_str(&sw, `, "users": `)
		w_int(&sw, users)
		if viewer != nil {
			grouped := false
			for i in 0 ..< SHAPES_MAX do if viewer.sel[i] && board.shapes[i].kind != .None && board.shapes[i].group >= 0 do grouped = true
			w_str(&sw, `, "nsel": `)
			w_int(&sw, i32(sel_count(viewer)))
			w_str(&sw, `, "grouped": `)
			w_str(&sw, "true" if grouped else "false")
			if viewer.edit_seq != st.sent_edit {
				st.sent_edit = viewer.edit_seq
				w_str(&sw, `, "edit": `)
				w_int(&sw, i32(viewer.edit_seq))
			}
		}
		if selected != st.sent_selected {
			st.sent_selected = selected
			w_str(&sw, `, "sel": `)
			w_int(&sw, selected)
			w_str(&sw, `, "label": `)
			if selected >= 0 {
				sh := &board.shapes[selected]
				w_json_string(&sw, string(sh.label[:sh.label_len]))
			} else {
				w_str(&sw, `""`)
			}
		}
		if viewer != nil && viewer.note_seq != st.sent_note {
			st.sent_note = viewer.note_seq
			w_str(&sw, `, "note": `)
			w_json_string(&sw, string(viewer.note[:viewer.note_len]))
		}
		// $drag goes false in the same event as the frame holding the finished shape
		dragging := viewer != nil && viewer.drag.kind != .None
		if dragging && viewer.drag_id != st.sent_drag_id {
			st.sent_drag_id = viewer.drag_id
			w_ghost(&sw, viewer)
		}
		if i8(dragging) != st.sent_drag {
			st.sent_drag = i8(dragging)
			w_str(&sw, `, "drag": `)
			w_str(&sw, "true" if dragging else "false")
		}
		w_str(&sw, "}")
		if !sw.overflow && datastar.patch_signals(&sse, string(sw.buf[:sw.n])) != .None do return http.close()
		return http.flush()

	case http.Application_Reply:
		st.subscribed = false // parking window elapsed: re-subscribe (board may have restarted)
		return subscribe_and_park(st, route_context)

	case http.Body_Chunk:
		return http.close()

	case http.Peer_Closed, http.Server_Drain:
		if board_handle != tina.ISOLATE_HANDLE_NONE {
			empty: [0]u8
			_ = http.route_send(route_context, board_handle, TAG_UNSUBSCRIBE, empty[:])
		}
		return http.close()
	}
	return http.close()
}

// $_gd: the drag as ghost_begin described it, tagged with the page's drag id.
// Flat keys, since a patch merges objects but would not merge arrays.
w_ghost :: proc(w: ^Writer, u: ^User) {
	gh := &u.ghost
	w_str(w, `, "_gd": {"id": `)
	w_int(w, u.drag_id)
	w_str(w, `, "k": `)
	w_int(w, i32(gh.kind))
	w_str(w, `, "raw": `)
	w_int(w, 1 if gh.raw else 0)
	for i in 0 ..< 4 {
		w_str(w, `, "m`)
		w_int(w, i32(i))
		w_str(w, `": `)
		w_int(w, 1 if gh.moves[i] else 0)
		w_str(w, `, "v`)
		w_int(w, i32(i))
		w_str(w, `": `)
		w_int(w, gh.v[i])
		w_str(w, `, "g`)
		w_int(w, i32(i))
		w_str(w, `": `)
		w_int(w, gh.g[i])
		w_str(w, `, "o`)
		w_int(w, i32(i))
		w_str(w, `": `)
		w_int(w, gh.o[i])
	}
	w_str(w, "}")
}

subscribe_and_park :: proc(st: ^Stream_State, route_context: http.Route_Context) -> http.Route_Step {
	if !st.subscribed {
		handle := board_handle
		if handle == tina.ISOLATE_HANDLE_NONE do return http.close()
		sub := Sub_Msg {
			request_token = http.route_request_token(route_context),
			uid           = st.uid,
		}
		if http.route_send(route_context, handle, TAG_SUBSCRIBE, tina.bytes_of(&sub)) != .ok do return http.close()
		st.subscribed = true
	}
	return park(route_context)
}

park :: proc(route_context: http.Route_Context) -> http.Route_Step {
	return http.expect_notification(route_context, PARK_NS, message_tag = TAG_FEED)
}

// ─── HTTP: commands ─────────────────────────────────────────────────────────

down :: proc(event: http.Route_Event, request: ^http.Request, response: ^http.Response, route_context: http.Route_Context, state: rawptr) -> http.Route_Step {
	return command(event, request, response, route_context, TAG_DOWN)
}
move :: proc(event: http.Route_Event, request: ^http.Request, response: ^http.Response, route_context: http.Route_Context, state: rawptr) -> http.Route_Step {
	return command(event, request, response, route_context, TAG_MOVE)
}
up :: proc(event: http.Route_Event, request: ^http.Request, response: ^http.Response, route_context: http.Route_Context, state: rawptr) -> http.Route_Step {
	return command(event, request, response, route_context, TAG_UP)
}
delete_shape :: proc(event: http.Route_Event, request: ^http.Request, response: ^http.Response, route_context: http.Route_Context, state: rawptr) -> http.Route_Step {
	return command(event, request, response, route_context, TAG_DELETE)
}
clear :: proc(event: http.Route_Event, request: ^http.Request, response: ^http.Response, route_context: http.Route_Context, state: rawptr) -> http.Route_Step {
	return command(event, request, response, route_context, TAG_CLEAR)
}
link :: proc(event: http.Route_Event, request: ^http.Request, response: ^http.Response, route_context: http.Route_Context, state: rawptr) -> http.Route_Step {
	return command(event, request, response, route_context, TAG_LINK)
}
nudge :: proc(event: http.Route_Event, request: ^http.Request, response: ^http.Response, route_context: http.Route_Context, state: rawptr) -> http.Route_Step {
	return command(event, request, response, route_context, TAG_NUDGE)
}
group :: proc(event: http.Route_Event, request: ^http.Request, response: ^http.Response, route_context: http.Route_Context, state: rawptr) -> http.Route_Step {
	return command(event, request, response, route_context, TAG_GROUP)
}
ungroup :: proc(event: http.Route_Event, request: ^http.Request, response: ^http.Response, route_context: http.Route_Context, state: rawptr) -> http.Route_Step {
	return command(event, request, response, route_context, TAG_UNGROUP)
}
lock :: proc(event: http.Route_Event, request: ^http.Request, response: ^http.Response, route_context: http.Route_Context, state: rawptr) -> http.Route_Step {
	return command(event, request, response, route_context, TAG_LOCK)
}
unlock :: proc(event: http.Route_Event, request: ^http.Request, response: ^http.Response, route_context: http.Route_Context, state: rawptr) -> http.Route_Step {
	return command(event, request, response, route_context, TAG_UNLOCK)
}

// Read the signals, send one message to the Board, answer with an empty
// Datastar stream (what @post expects). The view arrives on /stream.
command :: proc(
	event: http.Route_Event,
	request: ^http.Request,
	response: ^http.Response,
	route_context: http.Route_Context,
	$tag: tina.Message_Tag,
) -> http.Route_Step {
	#partial switch ev in event {
	case http.Request_Start:
		signals: Signals
		if datastar.read_signals(request, &signals) != .None {
			return http.respond_text(response, http.HTTP_STATUS_BAD_REQUEST, "bad signals")
		}
		x, y := signals.X, signals.Y
		when tag == TAG_LINK {
			x, y = signals.Lx, signals.Ly // the double-click's own coordinate
		}
		cmd := Cmd_Msg {
			uid  = signals.Uid,
			x    = clamp(x, 0, BOARD_W),
			y    = clamp(y, 0, BOARD_H),
			tool = signals.Tool,
			did  = signals.Did,
			nx   = i8(clamp(signals.Ndx, -1, 1)),
			ny   = i8(clamp(signals.Ndy, -1, 1)),
		}
		handle := board_handle
		if handle == tina.ISOLATE_HANDLE_NONE ||
		   http.route_send(route_context, handle, tag, tina.bytes_of(&cmd)) != .ok {
			return http.respond_text(response, http.HTTP_STATUS_SERVICE_UNAVAILABLE, "board unavailable")
		}
		return empty_stream(response)
	}
	return http.close()
}

// POST /label: $sel names the shape, $label carries the text; control bytes are dropped.
label :: proc(
	event: http.Route_Event,
	request: ^http.Request,
	response: ^http.Response,
	route_context: http.Route_Context,
	state: rawptr,
) -> http.Route_Step {
	#partial switch ev in event {
	case http.Request_Start:
		signals: Signals
		if datastar.read_signals(request, &signals) != .None {
			return http.respond_text(response, http.HTTP_STATUS_BAD_REQUEST, "bad signals")
		}
		msg := Label_Msg {
			uid   = signals.Uid,
			shape = signals.Sel,
		}
		n := 0
		for c in transmute([]u8)signals.Label {
			if n >= LABEL_MAX do break
			if c < 32 do continue
			msg.label[n] = c
			n += 1
		}
		msg.len = u8(n)
		handle := board_handle
		if handle == tina.ISOLATE_HANDLE_NONE ||
		   http.route_send(route_context, handle, TAG_LABEL, tina.bytes_of(&msg)) != .ok {
			return http.respond_text(response, http.HTTP_STATUS_SERVICE_UNAVAILABLE, "board unavailable")
		}
		return empty_stream(response)
	}
	return http.close()
}

empty_stream :: proc(response: ^http.Response) -> http.Route_Step {
	sse, start_error := datastar.start_sse(response)
	if start_error != .None do return http.close()
	if datastar.patch_signals(&sse, `{"live": true}`) != .None do return http.close()
	return http.flush(final = true)
}

// ─── Boot ───────────────────────────────────────────────────────────────────

main :: proc() {
	#assert(FRAME_MAX + 1024 < EGRESS_BUFFER_SIZE, "build with -define:HTTP_EGRESS_BUFFER_SIZE=57344 (see Makefile)")
	#assert(size_of(Label_Msg) <= tina.MAX_PAYLOAD_SIZE, "label message must fit one mailbox envelope")

	app := http.App {
		routes = []http.Route {
			http.get("/", index),
			http.get_event("/mike.css", stylesheet, state_size = u16(size_of(Stylesheet_State))),
			http.get_event("/stream", stream, state_size = u16(size_of(Stream_State))),
			http.post_event("/down", down, body_size_max = 1024, body_mode = .Buffered),
			http.post_event("/move", move, body_size_max = 1024, body_mode = .Buffered),
			http.post_event("/up", up, body_size_max = 1024, body_mode = .Buffered),
			http.post_event("/delete", delete_shape, body_size_max = 1024, body_mode = .Buffered),
			http.post_event("/clear", clear, body_size_max = 1024, body_mode = .Buffered),
			http.post_event("/link", link, body_size_max = 1024, body_mode = .Buffered),
			http.post_event("/label", label, body_size_max = 1024, body_mode = .Buffered),
			http.post_event("/nudge", nudge, body_size_max = 1024, body_mode = .Buffered),
			http.post_event("/group", group, body_size_max = 1024, body_mode = .Buffered),
			http.post_event("/ungroup", ungroup, body_size_max = 1024, body_mode = .Buffered),
			http.post_event("/lock", lock, body_size_max = 1024, body_mode = .Buffered),
			http.post_event("/unlock", unlock, body_size_max = 1024, body_mode = .Buffered),
		},
	}
	server := http.Server {
		address = tina.ipv4(127, 0, 0, 1, 8080),
		app     = &app,
	}
	spec := http.install_development_defaults(&server)
	assert(len(spec.shard_specs) == 1, "the board is a package global: one shard only")

	board_type_id := tina.Isolate_Type_Id(len(spec.types))
	types := make([]tina.IsolateTypeDescriptor, len(spec.types) + 1)
	copy(types, spec.types)
	types[board_type_id] = tina.IsolateTypeDescriptor {
		id                = board_type_id,
		slot_count        = 1,
		stride            = size_of(Board_Isolate),
		soa_metadata_size = size_of(tina.Isolate_Metadata),
		mailbox_capacity  = 1024,
		init_handler      = board_init,
		handler_fn        = board_handler,
	}
	spec.types = types

	root := &spec.shard_specs[0].root_group
	children := make([]tina.Child_Spec, len(root.children) + 1)
	children[0] = tina.Group_Spec {
		strategy              = .One_For_One,
		restart_count_max     = 10_000,
		window_duration_ticks = 1,
		children              = []tina.Child_Spec {
			tina.Static_Child_Spec{type_id = board_type_id, restart_type = .permanent},
		},
	}
	copy(children[1:], root.children)
	root.children = children

	for &u in board.users {
		u.selected = -1
		u.guide_vx = -1
		u.guide_hy = -1
	}
	for &s in board.shapes {
		s.joint = {-1, -1}
		s.corner = {-1, -1, -1, -1}
		s.group = -1
	}

	fmt.println("whiteboard — open http://127.0.0.1:8080 in two windows")
	tina.tina_start(&spec)
}
