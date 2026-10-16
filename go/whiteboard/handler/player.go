package handler

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"

	"whiteboard/ack"
	"whiteboard/board"
	"whiteboard/db"
	"whiteboard/view"

	"github.com/go-chi/chi/v5"
	"github.com/starfederation/datastar-go/datastar"
)

// The lesson player. A lesson is a script, and the board at step k is what
// steps 0..k-1 drew: seeking wipes the board and replays them at once, then
// plays on from k. So a page plays as often as you like, pauses, steps back
// and scrubs; the run is the only thing that moves, and every viewer sees
// it move (the board's stream carries the bar, #lesson-bar).
//
//	POST /b/{name}/lesson/{op}   play, pause, restart, back, seek (payload {n})

// lessonRun is one lesson playing (or paused, or done) on a board.
type lessonRun struct {
	cancel context.CancelFunc
	done   chan struct{} // closed when the run's goroutine is gone

	mu       sync.Mutex
	paused   bool
	resume   chan struct{} // closed on resume
	step, of int
	finished bool
	marks    []int // where "back" goes: the steps that begin a thought
}

// runVersion counts every run's changes: a stream re-sends the bar when it moves.
var runVersion atomic.Int64

func (r *lessonRun) set(f func()) {
	r.mu.Lock()
	f()
	r.mu.Unlock()
	runVersion.Add(1)
}

// hold waits while the run is paused.
func (r *lessonRun) hold(ctx context.Context) error {
	for {
		r.mu.Lock()
		if !r.paused {
			r.mu.Unlock()
			return nil
		}
		ch := r.resume
		r.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (r *lessonRun) pause() {
	r.set(func() {
		if !r.paused && !r.finished {
			r.paused, r.resume = true, make(chan struct{})
		}
	})
}

func (r *lessonRun) play() {
	r.set(func() {
		if r.paused {
			r.paused = false
			close(r.resume)
		}
	})
}

// marks are the steps that begin a thought: a caption, a question, a tick,
// more notes, a wiped board. "Back" goes to the one before the current.
func marks(steps []Step) []int {
	out := []int{0}
	for i, s := range steps {
		switch {
		case s.Do == "say", s.Do == "ask", s.Do == "check", s.Do == "notes", s.Do == "erase" && len(s.At) == 0:
			if i > 0 {
				out = append(out, i)
			}
		}
	}
	return out
}

// startRun stops the board's run, if any, wipes the board (and its
// checklist, and the viewer's notes when the lesson has some), and plays the
// lesson from step from: the steps before it at once, the rest as written.
func (h *Handler) startRun(l *Lesson, name string, boardID int64, user db.User, from int) {
	h.stopRun(name)
	ctx, cancel := context.WithCancel(context.Background())
	r := &lessonRun{cancel: cancel, done: make(chan struct{}), of: len(l.Steps), marks: marks(l.Steps)}
	h.runs.Store(name, r)
	runVersion.Add(1)
	if err := h.Hub.Apply(ctx, name, board.Cmd{Op: "clear", UID: demoUID}); err != nil {
		slog.Error("lesson wipe", "board", name, "err", err)
	}
	if h.DB.ClearTodos(ctx, boardID) == nil {
		h.Hub.TodoChanged(ctx, name)
	}
	if l.Notes != nil && h.DB.SaveNote(ctx, user.ID, boardID, "", l.Notes.Lang) == nil {
		h.Hub.PublishUser(user.ID)
	}
	end := h.pageEnd(ctx, boardID, name)
	go func() {
		defer close(r.done)
		defer cancel()
		h.play(ctx, l, name, user, end, r, from)
		r.set(func() { r.finished, r.step = ctx.Err() == nil, r.of })
		h.Hub.Publish(name)
	}()
}

// stopRun ends the board's run and waits for it to be gone.
func (h *Handler) stopRun(name string) {
	if v, ok := h.runs.Load(name); ok {
		r := v.(*lessonRun)
		r.cancel()
		<-r.done
	}
}

// running: is a lesson playing (or paused) on the board?
func (h *Handler) running(name string) bool {
	v, ok := h.runs.Load(name)
	if !ok {
		return false
	}
	r := v.(*lessonRun)
	r.mu.Lock()
	defer r.mu.Unlock()
	return !r.finished
}

// lessonState is what the board's bar shows this viewer.
func (h *Handler) lessonState(name string, u *db.User, ownerID int64, has bool) view.LessonState {
	st := view.LessonState{Has: has, Owner: u != nil && u.ID == ownerID, Version: runVersion.Load()}
	if v, ok := h.runs.Load(name); ok {
		r := v.(*lessonRun)
		r.mu.Lock()
		st.Running, st.Paused, st.Done, st.Step, st.Of = !r.finished, r.paused, r.finished, r.step, r.of
		r.mu.Unlock()
	}
	return st
}

// lessonControl is the bar's buttons: the board's owner drives the lesson.
func (h *Handler) lessonControl(w http.ResponseWriter, r *http.Request) {
	n, u, meta, ok := h.open(w, r, false)
	if !ok {
		return
	}
	var p payload
	_ = datastar.ReadSignals(r, &p)
	if h.moved(w, r, p.Inst) {
		return
	}
	if meta.OwnerID != u.ID {
		ack.Bad(datastar.NewSSE(w, r), "lesson", "Only the page's owner plays it")
		return
	}
	var l Lesson
	if err := json.Unmarshal([]byte(h.DB.LessonScript(r.Context(), meta.ID)), &l); err != nil || l.Validate() != nil {
		ack.Bad(datastar.NewSSE(w, r), "lesson", "No lesson on this board")
		return
	}
	var run *lessonRun
	if v, ok := h.runs.Load(n); ok {
		run = v.(*lessonRun)
	}
	step, finished := 0, true
	if run != nil {
		run.mu.Lock()
		step, finished = run.step, run.finished
		run.mu.Unlock()
	}
	switch op := chi.URLParam(r, "op"); op {
	case "play":
		if run != nil && !finished {
			run.play()
		} else {
			h.startRun(&l, n, meta.ID, *u, 0)
		}
	case "pause":
		if run != nil {
			run.pause()
		}
	case "restart":
		h.startRun(&l, n, meta.ID, *u, 0)
	case "back":
		to := 0
		for _, m := range marks(l.Steps) {
			if m < step-1 {
				to = m
			}
		}
		h.startRun(&l, n, meta.ID, *u, to)
	case "seek":
		h.startRun(&l, n, meta.ID, *u, min(max(p.N, 0), len(l.Steps)))
	default:
		http.NotFound(w, r)
		return
	}
	h.Hub.Publish(n)
	datastar.NewSSE(w, r)
}

