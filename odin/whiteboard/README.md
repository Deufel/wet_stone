# whiteboard — a streamed SVG diagram board

Tina + Datastar + [mike.css](https://github.com/Deufel/css). Boxes, lines and
arrows with labels, corner and endpoint handles, snapping, links that follow,
live cursors, a theme changer. Every open window shows the same board at the
same instant, because the board only ever exists on the server.

```bash
make run        # open http://127.0.0.1:8080 in two windows
```

Tools: **select** (1), **box** (2), **line** (3), **arrow** (4). Drag on empty
board to draw. Drag a shape to move it, a corner or endpoint to reshape it.
Type in the label field to label your selection, or press **Enter** to jump
into it and **Enter** (or Esc) again to return to the board. **Arrow keys** move
the selection one grid step: a box carries its linked ends along, a line moves
only its unlinked ends. **Delete** removes it, **clear** empties the board.

**Selection and groups.** With the select tool, drag on empty board for a
marquee: dragged **downward** it takes every shape it touches (dashed), dragged
**upward** only the shapes it fully encloses (solid). Once two or more shapes
are selected, a click on a shape toggles it in or out, and a press-and-drag on a
selected shape moves the whole selection; a click on empty board clears it. A
multi-selection is outlined by its extents in a dotted line. **group** (G) gives
the selection one color and makes it select as one; **ungroup** (Shift+G) undoes it.
A group moves as a unit, by drag or arrow keys, whatever its links: links inside
it move with it, outside shapes linked to a member stretch to follow, and a
member's line end held by something outside lets go. A node inside a group (a
line end, a box corner) can still be pressed and dragged on its own.
Arrows, Delete and dragging act on the whole selection. Each viewer's selection
is its own, held on the server.

**Lock and unlock.** **L** links every vertex of the selection to whatever
overlaps it: a line end joins a link node, a box port or other free line ends
there; a box corner joins other boxes' corners; free line ends lying on the
box's ports link to it. **U** lets the selection go of every link it has, line
ends and corners staying where they are.

**Laser** (5). Drag to draw a pointer trail every viewer sees, which fades and
is gone in 1.5 s. The server keeps a point only 8 px from the last and joins the
kept points with quadratics between their midpoints (smooth, and one short path
per point). Each piece is rendered once under a stable id, so the stream's morph
only appends it, and each page fades it with a CSS animation of `--fg` from 1 to
0. `--fg` is a registered number in mike.css, so it interpolates, and 0 is the
surface color, so the ink fades into the board in any theme. (`--lift` offsets
surfaces, not ink, and is registered as `*`, so it would not animate.)

**Labels by double-click.** A double-click on a shape or line, away from a link
point, opens that one item's label, even inside a group (the group stays
selected). Enter or Esc returns to the board.

**Corner links.** Double-click where corners of two or more boxes meet to give
them one link node (line ends there join too); double-click it again to unlink.
Dragging a box moves every box corner-linked to it as one rigid cluster;
resizing by a linked corner moves the shared vertex, so the other box's corner
follows. Arrow keys (outside groups) move only what is not held: a corner linked to a box outside
the selection keeps its edges, so the selected box stretches from it and the
other box stays put.

**Ports.** Every box has eight ports, its corners and edge midpoints, drawn as
circles when the box is selected or when a line end is dragged near it (filled
once linked); they are also the selected box's handles. A dragged line end snaps to a port or a link node from 20 px and,
on release, links itself: one joint per port, so every line drawn to a port
joins the same link node. Only the ends a drag moved link themselves, so an end
you unlinked stays unlinked until you drag it there again.

**Linking.** A double-click puts its own coordinate in the `$lx`/`$ly` signals
and posts `/link`. The server gathers every node on that coordinate (line and
arrow ends, plus at most one box anchor) and puts them all in one **joint**: a
single shared position they all read. Double-click the same point again and the
joint dissolves, each end staying where it is. The status line says what
happened (`$note`: "linked 3 nodes", "unlinked 2 nodes", "no node here").
Pointerdown never changes a link, which is what broke unlinking before: the
first half of a double-click used to free the end, so the dblclick linked it
again.

- A joint with a box anchor follows the box, and its ends are pinned: drag
  the box, or unlink first.
- A joint without a box is a free point: drag any of its ends and all of them
  move together.
- Dragging a line's body moves its free ends only; joined ends stay put.
- Deleting a box turns its joints into free points. A joint left with one
  member dissolves itself.
- Joined ends are drawn as filled dots, free ends as hollow dots.

**Labels.** The stream sends each viewer `$sel`, the shape it has selected,
and `$label`, that shape's text, whenever the selection changes. The label
field posts both back, so the text lands on the shape it was typed for even
if you clicked something else during the 200 ms debounce.

**Theme.** The footer holds a theme select (system, light, dark) and a hue
slider. They set `data-ui-theme` on the root and `--hue`, which is all mike.css
needs; Datastar's `data-persist` keeps the choice in local storage.

## Land-and-stream, by the book

- **One view channel.** The page opens `/stream` once. Every render tick with a
  change ships the whole `<svg id="board">` as one `datastar-patch-elements`
  morph. Commands (`/down`, `/move`, `/up`, `/delete`, `/clear`) answer an empty
  stream and never a fragment. The dragging user's own view comes down the same
  stream as everyone else's.
- **Server-owned truth.** Shapes, cursors, selections and drags live in the
  Board isolate. Pointer events are transport: the page posts `x`, `y`, `tool`
  and `uid` as signals, the server decides what they mean.
- **Derive, never copy.** A linked end stores which box and which anchor it
  follows; its position is computed from the box at read time, for rendering,
  hit testing and snapping alike. Moving a box therefore moves every linked
  end with no cascade to keep in sync.
- **Snapping on the server.** A dragged point snaps to another shape's anchors
  (corners, edge midpoints, center; line ends and midpoint) within 14 px, else
  to its own shape's other end per axis within 10 px, else to the 20 px grid.
  Guides and anchor markers are rendered into the dragging user's frame only;
  each stream renders the board for its own viewer, so selection handles and
  guides are personal while the shapes are shared.
- **Coalesced, bounded.** Commands mark the board dirty; a 4 ms tick (up to
  250 frames a second) renders once per change regardless of how many users
  are moving. Measured locally: 121 moves a second in, 121 frames applied. 160 shapes, 160
  joints, 64 users, 128 streams, one 50 KB frame buffer, all sized at boot. The
  Makefile raises Tina's per-connection egress buffer to 56 KB (its ceiling is
  64 KB including connection state). A new shape or a longer label is refused
  with a note ("board is full") if the frame would no longer fit, so the stream
  can never stall on an oversized frame.
- **Every answer is SSE.** Commands answer an empty `text/event-stream`; the
  view only ever arrives on `/stream`.
- **Presence.** An open stream keeps its viewer counted, and closing it removes
  the viewer at once.

## Page shell

The page is mike.css's `.page` grid: `.pg-header` holds the tools and status,
`.pg-main` the stage, `.pg-footer` the help line and the theme changer. All
page CSS lives in one `@layer block`, zero-specificity `:where()` rules, per
the engine's authoring laws. Labels are `<foreignObject>` elements the engine
types, never SVG `<text>`. One engine rule bit during the build: the reset puts
`background-repeat: no-repeat` on everything, which is one reason the grid is
now SVG patterns instead of a CSS background.

## Color through the engine

No color value appears in the code. The stage sets `--fg: 0.75`; each shape's
`<g>` carries `--hue-shift: N` (six steps of 60°); the selected shape gets
`--fg: 1`; guides sit in a `.inf` group, which locks the info hue; grid and
fills are `color-mix(in oklch, currentColor …)`.

The grid is a static SVG layer under the streamed board, never morphed and
never re-sent: a "+" at every 20-unit snap point and a major line every five
cells (100 units). Each pattern is offset by half a tile so its mark lands on
the snap lattice itself, not in the middle of a cell. mike.css's bridge
(`svg { color: currentColor }`) paints the SVG with the engine's ink, so dark
mode, themes and `.bw` work without the server knowing.

## Shape of the code (`main.odin`)

| part | what |
|---|---|
| `Board`, `Shape`, `User`, `Drag` | fixed arrays, package global, one shard |
| `board_handler` | messages in, `dirty` flag, tick → broadcast `Feed_Msg` |
| `cmd_down/move/up/delete/clear/link/label` | hit testing, drag application, snapping, linking |
| `Joint`, `joint_pos`, `end_point`, `joint_dissolve`, `shape_remove` | shared positions and derived connector ends |
| `frame_fits` | refuses growth that would overflow one SSE event |
| `render_frame` | the SVG for one viewer into `frame_buf`, no allocation |
| `stream` | subscribe, park, render on feed, re-subscribe on timeout |
| `command` | read signals, send one message, answer an empty stream |

**Ghost.** Pointer moves are not throttled: each one posts `/move`, and
Datastar cancels a still-pending `/move` when the next one starts, so the latest
position wins (`/down`, `/up`, `/link` and `/nudge` are never cancelled). Every
drag has a dashed ghost drawn from local `_` signals (never posted) at pointer
speed, in its own `<svg id="ghost">` that the stream never touches. A new shape
draws at once. A move or resize starts when the stream's `$_gd` describes it:
for each of x1 y1 x2 y2, either a fixed value or `snap(pointer - g) + o`,
tagged with the page's drag id `$did`. `/up` carries the release point and the server applies it
before finishing the shape, so out-of-order moves cannot leave a stale end. The
stream sends `$drag: false` in the same event as the frame holding the finished
shape, and that is when the ghost goes: the server still finalizes, with no second
channel.

Honest caveats: the board is a package global read by the stream routes, which
is safe only because there is one shard (asserted at boot). A move or resize
ghost begins one round trip after pointerdown, since only the server knows what
was hit; the ghost snaps to the grid only, and anchor snapping shows when the
frame lands. Lines linked to a moving box are not ghosted; they follow in the
frame.
