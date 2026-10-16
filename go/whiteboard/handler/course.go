package handler

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"whiteboard/auth"
	"whiteboard/db"
	"whiteboard/hub"

	"github.com/go-chi/chi/v5"
	"github.com/starfederation/datastar-go/datastar"
)

// Lesson books: a course becomes a book, each page a board that carries its
// lesson. A page's presenter plays when its owner first opens it, and leaves
// a card pointing at the next page; the owner can play it again.
//
//	POST /course/{kind}     a built-in course (courses/{kind}.json), as a new book
//
// (Playing, pausing and seeking a page: player.go.)

func (h *Handler) course(w http.ResponseWriter, r *http.Request) {
	u := auth.User(r.Context())
	if u == nil {
		http.Error(w, "sign in first", http.StatusUnauthorized)
		return
	}
	c, ok := Courses[chi.URLParam(r, "kind")]
	if !ok {
		http.NotFound(w, r)
		return
	}
	_, board, err := h.makeCourse(r.Context(), u, c, db.Public)
	if err != nil {
		slog.Error("course", "err", err)
		http.Error(w, "could not make the book", http.StatusInternalServerError)
		return
	}
	_ = datastar.NewSSE(w, r).Redirect("/b/" + board)
}

// makeCourse stores a (resolved) course as a lesson book: a whiteboard and
// its pages, each carrying its lesson; it returns the book and its whiteboard.
func (h *Handler) makeCourse(ctx context.Context, u *db.User, c *Course, vis string) (string, string, error) {
	var chapters []db.CourseChapter
	for _, ch := range c.Chapters {
		out := db.CourseChapter{Title: ch.Title}
		for _, pg := range ch.Pages {
			script, err := json.Marshal(pg)
			if err != nil {
				return "", "", err
			}
			out.Pages = append(out.Pages, db.CoursePage{Title: pg.Title, Script: string(script)})
		}
		chapters = append(chapters, out)
	}
	book, first, err := h.DB.CreateCourse(ctx, u, c.Title, vis, chapters)
	if err == nil {
		h.Hub.PublishUser(u.ID)
	}
	return book, first, err
}

// lessonOnOpen starts a page's lesson the first time its owner lands on it.
func (h *Handler) lessonOnOpen(r *http.Request, u *db.User, meta *hub.Meta, name string) {
	if meta.OwnerID != u.ID || !h.DB.HasLesson(r.Context(), meta.ID) {
		return
	}
	script, ok := h.DB.ClaimLesson(r.Context(), meta.ID)
	if !ok {
		return
	}
	h.startLesson(script, meta.ID, name, *u)
}

func (h *Handler) startLesson(script string, boardID int64, name string, u db.User) bool {
	var l Lesson
	if err := json.Unmarshal([]byte(script), &l); err != nil || l.Validate() != nil {
		slog.Error("lesson script", "board", name, "err", err)
		return false
	}
	h.startRun(&l, name, boardID, u, 0, 0)
	return true
}
