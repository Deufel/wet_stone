# whiteboard (Go) — the sister of `odin/whiteboard`

The same board, feature for feature, on the Go stack: Go + templ + Datastar +
an embedded NATS bus + SQLite (modernc), in the shape of `eos-go`.

```bash
make run        # templ generate, go build, ./whiteboard
                # open http://127.0.0.1:8081 (→ /b/main), or any /b/{name}
./whiteboard -addr 127.0.0.1:8081 -db whiteboard.db
```

It runs on :8081 so it can sit next to the Odin version on :8080.

**Deployed** at [draw.deufel.dev](https://draw.deufel.dev) with Coolify from
this directory's `Dockerfile` (base directory `go/whiteboard`): a static
binary on port 8080, its SQLite history on the `/data` volume. `GET /healthz`
answers `200 ok` while the database answers (503 otherwise); the image's
`HEALTHCHECK` and Coolify's health check both use it. The stream's
`-compress` flag (`br`, the default, `gzip` or `none`) exists because some
proxies hold a compressed stream back; ngrok holds Brotli.

## Measured (draw.deufel.dev, 2026-10-15)

No client-side drawing at all: pointer events go up as commands, and every frame
is the whole SVG, drawn on the server for each viewer and streamed down as a
morph. Through Coolify's proxy (HTTP/2, Brotli), from a home connection:

| | |
|---|---|
| network round trip (`/healthz`) | ~30 ms |
| pointer move → server-drawn frame on screen | median 32 ms (25–45) |
| sustained drag | 121 moves/s in → 116 frames/s applied; last move on screen 25 ms after sending |
| wire cost per frame (60-box board, Brotli) | ~40 B for a ~5.8 KB SVG (144×) |

The server's whole path (apply, render, compress, stream, morph) costs a few
milliseconds on top of the round trip.

## Accounts, boards and access

Sign-in is Google (OpenID Connect, written directly: no SDK), with a server-side
session (the cookie holds a random token, SQLite holds only its hash). The
landing page is public; every board needs sign-in.

| board | who may open it |
|---|---|
| global (`/b/main`) | everyone signed in |
| public | everyone signed in who has the link |
| private | its owner and the Google emails invited (an invite works before they ever sign in, and becomes a tab when they do) |

New boards get unguessable names. The owner renames a board and manages who is
invited from the Share dialog; the title and the dialog are morph targets on the
board's stream, so a change shows for everyone at once. Every board route
(page, stream, commands, share) checks access on the server.

Configure with environment variables (in Coolify):

| | |
|---|---|
| `GOOGLE_CLIENT_ID`, `GOOGLE_CLIENT_SECRET` | an OAuth client (type: web application) |
| `BASE_URL` | `https://draw.deufel.dev`; the redirect URI is `BASE_URL/auth/google/callback` |

`-dev-login` adds an email-only sign-in for local testing. Never in production.

The schema migrates forward by `PRAGMA user_version` (`db/migrate.go`); an
existing database keeps every board and revision.

## The app shell

A header (the board, Share, shortcuts, theme, you), the boards as underline
tabs, a tool rail on tablet and desktop that moves to a bottom bar on phones
(mike.css's `.mobile` / `.tablet` / `.desktop` gates), and a context toolbar over
the paper. The history is always a right-hand `.drawer` (⇧H), on every width.

**History, after Tufte:** a sparkline of the drawing's size over its life,
labelled at its ends; the branches as plain text; the checked-out line on one
hairline with a dot per revision, each a **small multiple** (a thumbnail of the
drawing at that moment, every one on the board's own scale), what changed, by
how much, and when. Forks are labelled where they leave the line.

**PWA:** a manifest (`display: standalone`, scope `/`), the apple-* tags that
keep iOS from bringing Safari's bars back on in-app navigation, `viewport-fit:
cover` with safe-area padding, a paper icon (`static/icon.svg`, rasterized to
the PNG sizes), and cross-document view transitions.

## Shape of the code

| package | what |
|---|---|
| `board` | the model, ported from the Odin isolate: shapes, joints, viewers, drags, ghost descriptors, laser, render. One `sync.RWMutex` per board: commands lock, renders read-lock. No IO. |
| `hub` | live boards by name: loaded from SQLite on first use, kept in memory; per board a keeper (the one history writer) and a ticker; `history.go` builds the panel's view |
| `bus` | embedded in-process NATS (no TCP port), subject `board.{name}.changed` |
| `db` | `schema.sql`; revisions, branches and the board's checked-out position |
| `auth` | Google sign-in, sessions, the request's user; the local dev sign-in |
| `handler` | routes: landing, home, new board, page, stream, commands, share |
| `view` | `layout.templ` (the PWA head), `landing.templ`, `page.templ` (the board), `history.templ`, `share.templ`, `icons.go`, `js.go` (every Datastar expression, built in Go) |
| `static` | `mike.css` (committed copy), `board.css`, `board.js` (`gridSnap`), embedded |

## Land-and-stream

- **One view channel.** `GET /b/{name}/stream` subscribes to the board's
  subject, pushes the current frame at once (a reconnect lands on truth), then
  re-renders on every event: `<svg id="board">` as a morph, plus this viewer's
  signals. Events that land during a render collapse into one wake. One
  goroutine does every write, with a 15 s keepalive, Brotli on the stream.
- **Commands.** `POST /b/{name}/{op}` reads the signals, applies the command,
  publishes, and answers an empty SSE stream. The caller's view comes down its
  stream like everyone else's.
- **Scoped.** A board's events wake only that board's streams.

## Keys and mouse

| | |
|---|---|
| `Esc` | toggle the select tool and the tool you were on |
| `r` `l` `a` / `1`–`5` | box, line, arrow / select, box, line, arrow, laser |
| arrows; `h j k l` in select | nudge the selection one grid step (vim: left, down, up, right) |
| `Enter` | edit the selected shape's label; `Enter`/`Esc` again returns |
| `g` / `G` | group / ungroup |
| `Del` | delete the selection |
| right-click | the shape there (selected if it was not) and the selection unlock if anything is linked, else lock onto what overlaps |
| double-click | link or unlink where nodes meet; elsewhere on a shape, edit its label |

In a drawing tool `l` picks the line (a shape just drawn stays selected), so
`h j k l` nudge only in the select tool; arrows nudge in any tool.

## History and branches (what SQLite holds)

Only the drawing, as history. Every command that can change the drawing (an
`up`, delete, clear, link, label, nudge, group, ungroup, lock/unlock) hands its
change to the board's **keeper**, the one goroutine that writes the board's
history: it commits the latest drawing as a revision (`revisions`: the whole
drawing as JSON, its parent, the time, what made it). Bursts collapse to the
newest state, an unchanged drawing is not written, and a label or nudge within
2 s of the same edit folds into it. Viewers, selections, drags and laser
strokes never reach the store.

The aside (`.pg-aside`, a second morph target on the same stream, sent only
when the history changed) shows the branches and the checked-out line of
revisions, newest first:

- **Jump**: click any revision. If it is a branch's head, that branch is
  checked out; otherwise the board is detached there, and the next change
  starts a new branch (`b2`, `b3`, ...) from that point. Nothing is lost.
- **Branches**: switch to one (its head comes back), make it main (main cannot
  be deleted, and wins when a revision heads several branches; a new board
  starts on main), or delete it (not main, not the checked-out one); a
  delete removes every revision no remaining branch reaches. `⑂ name` marks
  where a branch leaves the line on screen.

The checked-out branch is shared: everyone on the board sees the same drawing,
and a checkout clears every viewer's selection. The keeper runs history
commands in order after any pending save, so an edit made just before a jump
is kept on the branch it was made on. The position (branch, revision) is
stored, so a restart opens where you were.

The schema is created if missing; a column change needs a fresh
`whiteboard.db` (or a migration) while this is young.

## Differences from the Odin version

- Many boards, by URL. `/` sends you to `/b/main`.
- No frame budget: Go has no fixed egress buffer, so the 50 KB cap and the
  "board is full" refusals on growth are gone (the 400-shape table remains).
- Ticks are time-based: lasers leave after 1.5 s and idle viewers after 12 s,
  checked every 50 ms; renders are driven by events, not a render tick.
