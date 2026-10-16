package handler

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"whiteboard/ack"
	"whiteboard/auth"
	"whiteboard/bus"
	"whiteboard/db"
	"whiteboard/view"

	"github.com/go-chi/chi/v5"
	"github.com/starfederation/datastar-go/datastar"
)

// Books: a book is a collection of boards, in chapters, in order.
//
//	POST /books/new/{visibility}                make one (a first chapter, a first page)
//	GET  /book/{name}                           its contents
//	GET  /book/{name}/stream                    the contents, live
//	GET  /book/{name}/print                     every page, for paper
//	POST /book/{name}/title                     the owner renames it (payload {text})
//	POST /book/{name}/chapter                   adds a chapter (and its first page)
//	POST /book/{name}/chapter/{id}/title        renames a chapter
//	POST /book/{name}/chapter/{id}/page         adds a page (a new board) to a chapter
//	POST /book/{name}/page/{id}/remove          takes a page out (the board stays)

func (h *Handler) bookRoutes(r chi.Router) {
	r.Post("/books/new/{visibility}", h.newBook)
	r.Get("/book/{name}", h.bookPage)
	r.Get("/book/{name}/stream", h.bookStream)
	r.Get("/book/{name}/print", h.bookPrint)
	r.Post("/book/{name}/title", h.bookEdit)
	r.Post("/book/{name}/chapter", h.bookEdit)
	r.Post("/book/{name}/chapter/{id}/title", h.bookEdit)
	r.Post("/book/{name}/chapter/{id}/page", h.bookEdit)
	r.Post("/book/{name}/page/{id}/remove", h.bookEdit)
}

func (h *Handler) newBook(w http.ResponseWriter, r *http.Request) {
	u := auth.User(r.Context())
	if u == nil {
		http.Error(w, "sign in first", http.StatusUnauthorized)
		return
	}
	vis := chi.URLParam(r, "visibility")
	if vis != db.Private {
		vis = db.Public
	}
	// a book is its whiteboard and the pages sent from it: the teacher
	// starts at the whiteboard
	_, board, err := h.DB.CreateWhiteboardBook(r.Context(), u, "Untitled book", vis)
	if err != nil {
		slog.Error("new book", "err", err)
		http.Error(w, "could not make a book", http.StatusInternalServerError)
		return
	}
	h.Hub.PublishUser(u.ID)
	_ = datastar.NewSSE(w, r).Redirect("/b/" + board)
}

// openBook resolves the book in the path for a signed-in reader; on false it
// has answered.
func (h *Handler) openBook(w http.ResponseWriter, r *http.Request, page bool) (*db.Book, *db.User, bool) {
	u := auth.User(r.Context())
	if u == nil {
		if page {
			http.Redirect(w, r, auth.SignInURL(r.URL.Path), http.StatusSeeOther)
		} else {
			http.Error(w, "sign in first", http.StatusUnauthorized)
		}
		return nil, nil, false
	}
	b, err := h.DB.Book(r.Context(), chi.URLParam(r, "name"))
	if errors.Is(err, db.ErrNoBook) {
		h.notFound(w, r, page)
		return nil, nil, false
	}
	if err != nil {
		http.Error(w, "book unavailable", http.StatusServiceUnavailable)
		return nil, nil, false
	}
	if !b.CanOpen(u) {
		if page {
			html(w)
			w.WriteHeader(http.StatusForbidden)
			_ = view.Private(u).Render(r.Context(), w)
		} else {
			http.Error(w, "not yours to open", http.StatusForbidden)
		}
		return nil, nil, false
	}
	return b, u, true
}

func (h *Handler) bookPage(w http.ResponseWriter, r *http.Request) {
	b, u, ok := h.openBook(w, r, true)
	if !ok {
		return
	}
	sheets, _ := h.DB.Sheets(r.Context(), b.ID)
	html(w)
	_ = view.BookPage(u, b, sheets).Render(r.Context(), w)
}

// bookStream re-sends the contents whenever the book changes.
func (h *Handler) bookStream(w http.ResponseWriter, r *http.Request) {
	b, u, ok := h.openBook(w, r, false)
	if !ok {
		return
	}
	wake, unsub, err := h.wakeOn(bus.BookSubject(b.ID))
	if err != nil {
		http.Error(w, "bus unavailable", http.StatusServiceUnavailable)
		return
	}
	defer unsub()
	sse := datastar.NewSSE(w, r, h.sseOptions()...)
	push := func() bool {
		fresh, err := h.DB.Book(r.Context(), b.Name)
		if err != nil {
			return true
		}
		if !fresh.CanOpen(u) {
			_ = sse.Redirect("/")
			return false
		}
		sheets, _ := h.DB.Sheets(r.Context(), fresh.ID)
		return sse.PatchElements(renderString(r, view.BookContents(u, fresh, sheets))) == nil
	}
	keep := time.NewTicker(Beat)
	defer keep.Stop()
	if !push() {
		return
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case <-wake:
			if !push() {
				return
			}
		case <-keep.C:
			if err := beat(sse); err != nil {
				return
			}
		}
	}
}

// bookEdit is every owner's change to a book; the contents come back on the
// book's stream, to every page that has it open.
func (h *Handler) bookEdit(w http.ResponseWriter, r *http.Request) {
	b, u, ok := h.openBook(w, r, false)
	if !ok {
		return
	}
	if b.OwnerID != u.ID {
		http.Error(w, "only the owner can change this book", http.StatusForbidden)
		return
	}
	var p payload
	_ = datastar.ReadSignals(r, &p)
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	ctx := r.Context()
	path := r.URL.Path
	var err error
	goTo := "" // a new page: the page goes to it
	switch {
	case hasSuffix(path, "/title") && id == 0:
		if t := cleanText(p.Text, 80); t != "" {
			err = h.DB.RenameBook(ctx, b.ID, t)
		}
	case hasSuffix(path, "/title"):
		if t := cleanText(p.Text, 80); t != "" {
			err = h.DB.RenameChapter(ctx, b.ID, id, t)
		}
	case hasSuffix(path, "/chapter") && b.Board != "":
		err = h.DB.AddChapterOnly(ctx, b) // the next pages sent go in it
	case hasSuffix(path, "/chapter"):
		goTo, err = h.DB.AddChapter(ctx, b)
	case hasSuffix(path, "/page"):
		goTo, err = h.DB.AddPage(ctx, b, id)
	case hasSuffix(path, "/remove"):
		err = h.DB.RemovePage(ctx, b.ID, id)
		h.Hub.PublishUser(b.OwnerID) // the board is back in its owner's boards
	}
	if err != nil {
		slog.Error("book", "book", b.Name, "path", path, "err", err)
		ack.Bad(datastar.NewSSE(w, r), "book", "Not saved")
		return
	}
	if err := h.Bus.Publish(bus.BookSubject(b.ID), nil); err != nil {
		slog.Error("publish book", "err", err)
	}
	sse := datastar.NewSSE(w, r)
	if goTo != "" {
		_ = sse.Redirect("/b/" + goTo)
		return
	}
	ack.Saved(sse, "book", "Saved")
}

func hasSuffix(s, suf string) bool { return len(s) >= len(suf) && s[len(s)-len(suf):] == suf }

// bookNav is where a board sits in its book, or nil.
func (h *Handler) bookNav(r *http.Request, boardID int64, board string) *view.BookNav {
	name := h.DB.BookOf(r.Context(), boardID)
	if name == "" {
		return nil
	}
	b, err := h.DB.Book(r.Context(), name)
	if err != nil {
		return nil
	}
	pages := b.Pages()
	n := &view.BookNav{Book: b.Name, Title: b.Title, Of: len(pages)}
	for _, c := range b.Chapters {
		for _, p := range c.Pages {
			if p.Board == board {
				n.No, n.Chapter = p.No, "Ch "+strconv.Itoa(c.No)+" · "+c.Title
			}
		}
	}
	for i, p := range pages {
		if p.Board == board {
			if i > 0 {
				n.Prev = pages[i-1].Board
			}
			if i+1 < len(pages) {
				n.Next = pages[i+1].Board
			}
		}
	}
	return n
}

// printPage gathers one board for paper: its drawing, its checklist and the
// viewer's notes.
func (h *Handler) printPage(r *http.Request, u *db.User, board string) (view.PrintPage, bool) {
	b, _, err := h.Hub.Board(r.Context(), board)
	if err != nil {
		return view.PrintPage{}, false
	}
	meta, err := h.Hub.Meta(r.Context(), board)
	if err != nil {
		return view.PrintPage{}, false
	}
	todos, _ := h.DB.Todos(r.Context(), meta.ID)
	note, _ := h.DB.Note(r.Context(), u.ID, meta.ID)
	return view.PrintPage{Board: board, Title: meta.Title, Drawing: b.Print(), Todos: todos, Note: note}, true
}

// bookPrint: the cover and contents, then every page, each with its chapter
// and page number in the footer.
func (h *Handler) bookPrint(w http.ResponseWriter, r *http.Request) {
	b, u, ok := h.openBook(w, r, true)
	if !ok {
		return
	}
	if b.Board != "" { // a whiteboard's book: its pages, each a letter sheet
		sheets, err := h.DB.Sheets(r.Context(), b.ID)
		if err != nil {
			http.Error(w, "book unavailable", http.StatusServiceUnavailable)
			return
		}
		html(w)
		_ = view.SheetsPrint(b, sheets).Render(r.Context(), w)
		return
	}
	d := view.PrintData{Back: "/book/" + b.Name, BackLabel: "Back to the book", Title: b.Title, Book: b, Printed: h.DB.Now(r.Context())}
	for _, c := range b.Chapters {
		for _, p := range c.Pages {
			pg, ok := h.printPage(r, u, p.Board)
			if !ok {
				continue
			}
			pg.Named = "bp" + strconv.Itoa(p.No)
			pg.Footer = "Chapter " + strconv.Itoa(c.No) + " · " + c.Title
			pg.Mark = "p. " + strconv.Itoa(p.No)
			d.Pages = append(d.Pages, pg)
		}
	}
	html(w)
	_ = view.Print(d).Render(r.Context(), w)
}
