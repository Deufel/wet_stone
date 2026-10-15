// Package hub owns the live boards. A board is loaded from SQLite on first use
// and stays in memory, where the drag path runs at pointer speed. Every
// command publishes board.{name}.changed. A command that changed the drawing
// hands its change to the board's keeper, the one goroutine that writes the
// board's history: it commits the latest drawing as a revision (bursts
// collapse; an unchanged drawing is never written) and runs history commands
// (checkout, switch, delete, make main) in order after any pending save.
package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"whiteboard/board"
	"whiteboard/bus"
	"whiteboard/db"
)

const (
	tickEvery = 50 * time.Millisecond // laser and idle-viewer expiry
	amendFor  = 2 * time.Second       // a label or nudge this soon after the last folds into it
)

type Hub struct {
	db     *db.DB
	bus    *bus.Bus
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu     sync.Mutex
	boards map[string]*slot // a board loaded, or loading
}

type live struct {
	name string
	b    *board.Board
	hist atomic.Pointer[History]
	meta atomic.Pointer[Meta]
	todo atomic.Int64  // the checklist's version: a stream re-reads it when this moves
	wake chan struct{} // cap 1

	mu      sync.Mutex
	pending string   // the latest unsaved change, "" = none
	ops     []HistOp // history commands, in order
}

// HistOp is a history command from the panel.
type HistOp struct {
	Kind string // checkout (ID = revision), switch, delbranch, mainbranch (ID = branch)
	ID   int64
}

func New(d *db.DB, b *bus.Bus) *Hub {
	ctx, cancel := context.WithCancel(context.Background())
	return &Hub{db: d, bus: b, ctx: ctx, cancel: cancel, boards: map[string]*slot{}}
}

// Meta is a board's identity and access list, versioned so a stream re-sends
// the share panel only when it changed.
type Meta struct {
	db.Meta
	Version int
}

// Board returns the named board and its history, loading it on first use.
func (h *Hub) Board(ctx context.Context, name string) (*board.Board, *History, error) {
	l, err := h.live(ctx, name)
	if err != nil {
		return nil, nil, err
	}
	return l.b, l.hist.Load(), nil
}

// Meta returns the named board's identity and access, loading it on first use.
func (h *Hub) Meta(ctx context.Context, name string) (*Meta, error) {
	l, err := h.live(ctx, name)
	if err != nil {
		return nil, err
	}
	return l.meta.Load(), nil
}

// Share changes a board's title or access, then republishes its metadata.
// op: "title" (arg = the new title), "invite" / "uninvite" (arg = an email).
func (h *Hub) Share(ctx context.Context, name, op, arg string) error {
	l, err := h.live(ctx, name)
	if err != nil {
		return err
	}
	m := l.meta.Load()
	switch op {
	case "title":
		err = h.db.SetTitle(ctx, m.ID, arg)
	case "invite":
		err = h.db.Invite(ctx, m.ID, arg)
	case "uninvite":
		err = h.db.Uninvite(ctx, m.ID, arg)
	}
	if err != nil {
		return err
	}
	fresh, err := h.db.Meta(ctx, name)
	if err != nil {
		return err
	}
	l.meta.Store(&Meta{Meta: fresh, Version: m.Version + 1})
	h.Publish(name)
	// whose tab strips changed: everyone holding the board for a rename (the
	// owner included), the invited or uninvited person for a share change
	switch op {
	case "title":
		ids, _ := h.db.TabHolders(ctx, m.ID)
		for _, id := range ids {
			h.PublishUser(id)
		}
	default:
		if id := h.db.UserID(ctx, arg); id != 0 {
			h.PublishUser(id)
		}
	}
	return nil
}

// History is the board's current history view (replaced whole on change).
func (h *Hub) History(name string) *History {
	h.mu.Lock()
	s := h.boards[name]
	h.mu.Unlock()
	if s == nil {
		return nil
	}
	select {
	case <-s.ready:
	default:
		return nil // still loading
	}
	if s.l == nil {
		return nil
	}
	return s.l.hist.Load()
}

// slot is a board in the hub: loaded once by whoever asked first, while the
// others wait on ready. The hub's lock covers only the map, never the load
// (the database work, the history, the thumbnails): it once covered it all,
// so loading one board held up every command on every board.
type slot struct {
	ready chan struct{}
	l     *live
	err   error
}

func (h *Hub) live(ctx context.Context, name string) (*live, error) {
	h.mu.Lock()
	s, ok := h.boards[name]
	if !ok {
		s = &slot{ready: make(chan struct{})}
		h.boards[name] = s
	}
	h.mu.Unlock()
	if !ok {
		s.l, s.err = h.load(context.WithoutCancel(ctx), name)
		if s.err != nil { // forget it, so the next ask tries again
			h.mu.Lock()
			delete(h.boards, name)
			h.mu.Unlock()
		}
		close(s.ready)
	}
	select {
	case <-s.ready:
		return s.l, s.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// load reads a board in and starts its keeper and ticker.
func (h *Hub) load(ctx context.Context, name string) (*live, error) {
	pos, err := h.db.Ensure(ctx, name)
	if err != nil {
		return nil, err
	}
	meta, err := h.db.Meta(ctx, name)
	if err != nil {
		return nil, err
	}
	snap, err := h.db.Snapshot(ctx, pos.Rev)
	if err != nil {
		return nil, err
	}
	l := &live{name: name, b: board.New(), wake: make(chan struct{}, 1)}
	l.meta.Store(&Meta{Meta: meta, Version: 1})
	l.b.Restore(snap)
	k := &keeper{h: h, l: l, pos: pos}
	k.saved, _ = json.Marshal(snap)
	if err := k.refresh(ctx); err != nil {
		return nil, err
	}
	h.wg.Add(2)
	go k.run()
	go h.ticker(l)
	slog.Info("board loaded", "board", name, "rev", pos.Rev, "shapes", len(snap.Shapes))
	return l, nil
}

// Apply runs a command on the named board, publishes, and hands a drawing
// change to the keeper.
func (h *Hub) Apply(ctx context.Context, name string, c board.Cmd) error {
	l, err := h.live(ctx, name)
	if err != nil {
		return err
	}
	change := l.b.Apply(c)
	h.Publish(name)
	if change != "" {
		l.mu.Lock()
		l.pending = change
		l.mu.Unlock()
		l.poke()
	}
	return nil
}

// Hist queues a history command for the keeper.
func (h *Hub) Hist(ctx context.Context, name string, op HistOp) error {
	l, err := h.live(ctx, name)
	if err != nil {
		return err
	}
	l.mu.Lock()
	l.ops = append(l.ops, op)
	l.mu.Unlock()
	l.poke()
	return nil
}

func (l *live) poke() {
	select {
	case l.wake <- struct{}{}:
	default: // already awake; it will take this too
	}
}

func (h *Hub) Publish(name string) {
	if err := h.bus.Publish(bus.BoardSubject(name), nil); err != nil {
		slog.Error("publish", "board", name, "err", err)
	}
}

// TodoChanged marks the board's checklist changed and wakes its streams.
func (h *Hub) TodoChanged(ctx context.Context, name string) {
	if l, err := h.live(ctx, name); err == nil {
		l.todo.Add(1)
		h.Publish(name)
	}
}

// TodoVersion is the checklist's version (1 from the start, so a new stream
// always sends it once).
func (h *Hub) TodoVersion(ctx context.Context, name string) int64 {
	l, err := h.live(ctx, name)
	if err != nil {
		return 0
	}
	return l.todo.Load() + 1
}

// PublishUser wakes every stream that person has open, on every device.
func (h *Hub) PublishUser(id int64) {
	if err := h.bus.Publish(bus.UserSubject(id), nil); err != nil {
		slog.Error("publish", "user", id, "err", err)
	}
}

func (h *Hub) ticker(l *live) {
	defer h.wg.Done()
	t := time.NewTicker(tickEvery)
	defer t.Stop()
	for {
		select {
		case now := <-t.C:
			if l.b.Tick(now) {
				h.Publish(l.name)
			}
		case <-h.ctx.Done():
			return
		}
	}
}

// Close stops every board's goroutines after flushing pending saves.
func (h *Hub) Close() {
	h.cancel()
	h.wg.Wait()
}

// ─── The keeper: the one writer of a board's history ────────────────────────

type keeper struct {
	h     *Hub
	l     *live
	pos   db.Pos
	saved []byte // the drawing at pos.Rev, to skip unchanged saves

	lastOp  string // the last commit, for folding bursts
	lastAt  time.Time
	lastRev int64

	thumbs map[int64]thumb // a revision's thumbnail, kept until it is amended
}

type thumb struct {
	at  time.Time
	svg string
}

func (k *keeper) run() {
	defer k.h.wg.Done()
	for {
		stopping := false
		select {
		case <-k.l.wake:
		case <-k.h.ctx.Done():
			stopping = true // flush what is pending on the way out
		}
		k.l.mu.Lock()
		change, ops := k.l.pending, k.l.ops
		k.l.pending, k.l.ops = "", nil
		k.l.mu.Unlock()

		ctx := context.Background()
		changed := false
		if change != "" {
			changed = k.save(ctx, change)
		}
		if !stopping {
			for _, op := range ops {
				if err := k.apply(ctx, op); err != nil {
					slog.Error("history", "board", k.l.name, "op", op.Kind, "id", op.ID, "err", err)
				}
				changed = true
			}
		}
		if changed {
			if err := k.refresh(ctx); err != nil {
				slog.Error("history refresh", "board", k.l.name, "err", err)
			}
			k.h.Publish(k.l.name)
		}
		if stopping {
			return
		}
	}
}

// save commits the board's drawing as a revision, unless it is unchanged.
func (k *keeper) save(ctx context.Context, change string) bool {
	snap := k.l.b.Snapshot()
	key, _ := json.Marshal(snap)
	if bytes.Equal(key, k.saved) {
		return false
	}
	amend := (change == "label" || change == "nudge") && change == k.lastOp &&
		k.lastRev == k.pos.Rev && k.pos.Branch != 0 && time.Since(k.lastAt) < amendFor
	pos, err := k.h.db.Commit(ctx, k.pos, change, snap, amend)
	if err != nil {
		slog.Error("commit", "board", k.l.name, "err", err)
		return false
	}
	k.pos, k.saved = pos, key
	k.lastOp, k.lastAt, k.lastRev = change, time.Now(), pos.Rev
	return true
}

func (k *keeper) apply(ctx context.Context, op HistOp) error {
	k.lastOp = "" // never fold across a history move
	switch op.Kind {
	case "checkout":
		if !k.l.hist.Load().has(op.ID) {
			return nil
		}
		pos, err := k.h.db.Checkout(ctx, k.pos, op.ID)
		if err != nil {
			return err
		}
		return k.load(ctx, pos)
	case "switch":
		pos, err := k.h.db.Switch(ctx, k.pos, op.ID)
		if err != nil {
			return err
		}
		return k.load(ctx, pos)
	case "delbranch":
		return k.h.db.DeleteBranch(ctx, k.pos, op.ID)
	case "mainbranch":
		return k.h.db.MakeMain(ctx, k.pos, op.ID)
	}
	return nil
}

// load puts the drawing at pos on the board.
func (k *keeper) load(ctx context.Context, pos db.Pos) error {
	snap, err := k.h.db.Snapshot(ctx, pos.Rev)
	if err != nil {
		return err
	}
	k.l.b.Replace(snap)
	k.pos = pos
	k.saved, _ = json.Marshal(snap)
	return nil
}

func (k *keeper) refresh(ctx context.Context) error {
	revs, branches, err := k.h.db.History(ctx, k.pos.Board)
	if err != nil {
		return err
	}
	version := 1
	if old := k.l.hist.Load(); old != nil {
		version = old.Version + 1
	}
	h := buildHistory(version, k.pos, revs, branches)
	if k.thumbs == nil {
		k.thumbs = map[int64]thumb{}
	}
	for i := range h.Lineage {
		if i >= thumbsMax {
			break
		}
		r := &h.Lineage[i]
		t, ok := k.thumbs[r.ID]
		if !ok || !t.at.Equal(r.At) { // new, or amended since
			snap, err := k.h.db.Snapshot(ctx, r.ID)
			if err != nil {
				return err
			}
			t = thumb{at: r.At, svg: board.Thumb(snap)}
			k.thumbs[r.ID] = t
		}
		r.Thumb = t.svg
	}
	k.l.hist.Store(h)
	return nil
}
