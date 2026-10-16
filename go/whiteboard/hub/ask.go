package hub

import (
	"context"
	"errors"
	"sync/atomic"
)

// Ask is a lesson's question on a board, waiting for the one person who may
// answer it (the lesson's viewer); everyone on the board sees it. Once
// answered it shows Result for a moment, then goes.
type Ask struct {
	Version  int64
	Question string
	Options  []string // empty: a free-text answer
	By       string   // who answers (first name)
	ByID     int64
	Answered string // the answer given, "" while waiting
	Result   string // the feedback once answered
	Correct  bool
}

var askVersion atomic.Int64

// ErrNoAsk: no question is waiting on that board for that person.
var ErrNoAsk = errors.New("no question waiting")

// Ask puts a question on the board and waits for its answer (or ctx).
func (h *Hub) Ask(ctx context.Context, name string, q Ask) (string, error) {
	l, err := h.live(ctx, name)
	if err != nil {
		return "", err
	}
	answers := make(chan string, 1)
	l.mu.Lock()
	l.answers = answers
	l.mu.Unlock()
	q.Version = askVersion.Add(1)
	l.ask.Store(&q)
	h.Publish(name)
	defer func() {
		l.mu.Lock()
		if l.answers == answers {
			l.answers = nil
		}
		l.mu.Unlock()
	}()
	select {
	case a := <-answers:
		return a, nil
	case <-ctx.Done():
		h.Settle(name, nil)
		return "", ctx.Err()
	}
}

// Answer hands an answer to the question waiting on the board, if it is the
// given user's to answer.
func (h *Hub) Answer(ctx context.Context, name string, userID int64, text string) error {
	l, err := h.live(ctx, name)
	if err != nil {
		return err
	}
	q := l.ask.Load()
	if q == nil || q.ByID != userID || q.Answered != "" {
		return ErrNoAsk
	}
	l.mu.Lock()
	ch := l.answers
	l.mu.Unlock()
	if ch == nil {
		return ErrNoAsk
	}
	select {
	case ch <- text:
		return nil
	default:
		return ErrNoAsk // already answered
	}
}

// Settle shows the answer's feedback on the card (q), or clears it (nil).
func (h *Hub) Settle(name string, q *Ask) {
	h.mu.Lock()
	s := h.boards[name]
	h.mu.Unlock()
	if s == nil || s.l == nil {
		return
	}
	if q != nil {
		q.Version = askVersion.Add(1)
	}
	s.l.ask.Store(q)
	h.Publish(name)
}

// AskOf is the board's question, or nil.
func (h *Hub) AskOf(name string) *Ask {
	h.mu.Lock()
	s := h.boards[name]
	h.mu.Unlock()
	if s == nil || s.l == nil {
		return nil
	}
	select {
	case <-s.ready:
	default:
		return nil
	}
	return s.l.ask.Load()
}
