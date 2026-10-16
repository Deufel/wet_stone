package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"whiteboard/db"
	"whiteboard/hub"

	"whiteboard/ack"
	"whiteboard/board"
	"whiteboard/bus"
	"whiteboard/view"

	"github.com/go-chi/chi/v5"
	"github.com/starfederation/datastar-go/datastar"
)

// Sending to a page. A book's teacher draws on its whiteboard; "Send to page"
// saves the whole whiteboard as it is now (the drawing, not its history) as
// the book's next page, a letter sheet with the book's title, chapter, author
// and page number, and the whiteboard turns to a clean page (undo brings it
// back). A board with no book becomes one's whiteboard on its first send.
//
//	POST /b/{name}/sendpage             the owner sends (signals: uid)
//	GET  /book/{name}/page/{no}         a page, as it prints
//	POST /book/{name}/sheet/{id}/remove the owner takes a page out
//	POST /b/{name}/sheet/{id}/copy      a copy of a page, after it
//	POST /b/{name}/sheet/{id}/delete    a page out of the book
//	POST /b/{name}/sheet/{id}/move/{to} a page dragged onto another's place

// sheetVersion counts pages sent and removed anywhere: a whiteboard's stream
// re-sends its page strip when it moves.
var sheetVersion atomic.Int64

func (h *Handler) sheetRoutes(r chi.Router) {
	r.Post("/b/{name}/sendpage", h.sendPage)
	r.Post("/b/{name}/editpage/{no}", h.editPage)
	r.Post("/b/{name}/newpage", h.newPage)
	r.Post("/b/{name}/sheet/{id}/copy", h.sheetOp)
	r.Post("/b/{name}/sheet/{id}/delete", h.sheetOp)
	r.Post("/b/{name}/sheet/{id}/move/{to}", h.sheetOp)
	r.Get("/book/{name}/page/{no}", h.sheetPage)
	r.Post("/book/{name}/sheet/{id}/remove", h.removeSheet)
}

func (h *Handler) sendPage(w http.ResponseWriter, r *http.Request) {
	n, u, meta, s, ok := h.teacher(w, r)
	if !ok {
		return
	}
	b, _, err := h.Hub.Board(r.Context(), n)
	if err != nil {
		http.Error(w, "board unavailable", http.StatusServiceUnavailable)
		return
	}
	snap, box, ok := b.Page()
	if !ok {
		ack.Bad(datastar.NewSSE(w, r), "sheet", "Nothing to send yet")
		return
	}
	ctx := r.Context()
	bookName := h.DB.WhiteboardBook(ctx, meta.ID)
	if bookName == "" {
		h.whiteboards.Store(meta.ID, true)
		if bookName, err = h.DB.CreateBookFor(ctx, u, meta.ID, meta.Title, meta.Visibility); err != nil {
			slog.Error("send page: book", "board", n, "err", err)
			ack.Bad(datastar.NewSSE(w, r), "sheet", "Not sent")
			return
		}
		h.Hub.PublishUser(u.ID)
	}
	book, err := h.DB.Book(ctx, bookName)
	if err != nil {
		ack.Bad(datastar.NewSSE(w, r), "sheet", "Not sent")
		return
	}
	// a page back on the whiteboard is saved again; otherwise a new one
	sheetID, no := h.DB.Editing(ctx, book.ID)
	if sheetID != 0 {
		err = h.DB.UpdateSheet(ctx, book.ID, sheetID, snap, box)
	} else {
		no, sheetID, err = h.DB.AddSheet(ctx, book.ID, snap, box)
	}
	if err != nil {
		slog.Error("send page", "board", n, "err", err)
		ack.Bad(datastar.NewSSE(w, r), "sheet", "Not sent")
		return
	}
	// the students' marks go with the page they were made on
	if who, err := h.DB.MarksToSheet(ctx, meta.ID, sheetID); err == nil {
		for _, id := range who {
			h.marksChanged(n, id)
		}
	}
	h.turnPage(r, n, s.UID, book.ID)
	ack.Saved(datastar.NewSSE(w, r), "sheet", "Page "+strconv.Itoa(no))
}

// turnPage: a clean whiteboard (its history keeps the old one), and the
// strip and the book told.
func (h *Handler) turnPage(r *http.Request, n string, uid uint32, bookID int64) {
	if err := h.Hub.Apply(r.Context(), n, board.Cmd{Op: "clear", UID: uid}); err != nil {
		slog.Error("turn page", "board", n, "err", err)
	}
	sheetVersion.Add(1)
	h.Hub.Publish(n)
	_ = h.Bus.Publish(bus.BookSubject(bookID), nil)
}

// teacher resolves a whiteboard command only its owner may give; on false it
// has answered.
func (h *Handler) teacher(w http.ResponseWriter, r *http.Request) (string, *db.User, *hub.Meta, signals, bool) {
	var s signals
	n, u, meta, ok := h.open(w, r, false)
	if !ok {
		return n, u, meta, s, false
	}
	if err := datastar.ReadSignals(r, &s); err != nil {
		http.Error(w, "bad signals", http.StatusBadRequest)
		return n, u, meta, s, false
	}
	if h.moved(w, r, s.Inst) {
		return n, u, meta, s, false
	}
	if meta.OwnerID != u.ID {
		ack.Bad(datastar.NewSSE(w, r), "sheet", "Only the teacher changes pages")
		return n, u, meta, s, false
	}
	return n, u, meta, s, true
}

// editPage puts a page back on the whiteboard to be changed: the next save
// replaces it.
func (h *Handler) editPage(w http.ResponseWriter, r *http.Request) {
	n, u, meta, _, ok := h.teacher(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	book, err := h.DB.Book(ctx, h.DB.WhiteboardBook(ctx, meta.ID))
	if err != nil {
		ack.Bad(datastar.NewSSE(w, r), "sheet", "No book here")
		return
	}
	sheets, err := h.DB.Sheets(ctx, book.ID)
	no, _ := strconv.Atoi(chi.URLParam(r, "no"))
	if err != nil || no < 1 || no > len(sheets) {
		ack.Bad(datastar.NewSSE(w, r), "sheet", "No such page")
		return
	}
	s := sheets[no-1]
	if err := h.Hub.Redraw(ctx, n, s.Drawing); err != nil {
		ack.Bad(datastar.NewSSE(w, r), "sheet", "Not opened")
		return
	}
	if err := h.DB.SetEditing(ctx, book.ID, s.ID); err != nil {
		slog.Error("edit page", "board", n, "err", err)
	}
	sheetVersion.Add(1)
	h.Hub.Publish(n)
	// a lesson page never presented: its lesson begins
	if s.Lesson != "" && len(s.Drawing.Shapes) == 0 {
		var l Lesson
		if json.Unmarshal([]byte(s.Lesson), &l) == nil && l.Validate() == nil {
			h.startRun(&l, n, meta.ID, *u, 0, s.ID)
			ack.Saved(datastar.NewSSE(w, r), "sheet", "Page "+strconv.Itoa(no)+": its lesson")
			return
		}
	}
	h.stopRun(n) // another page: the lesson on the last one ends
	ack.Saved(datastar.NewSSE(w, r), "sheet", "Page "+strconv.Itoa(no)+" is back on the whiteboard")
}

// sheetOp copies, deletes or moves a page from the strip (by its id).
func (h *Handler) sheetOp(w http.ResponseWriter, r *http.Request) {
	n, _, meta, s, ok := h.teacher(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	book, err := h.DB.Book(ctx, h.DB.WhiteboardBook(ctx, meta.ID))
	if err != nil {
		ack.Bad(datastar.NewSSE(w, r), "sheet", "No book here")
		return
	}
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	path := r.URL.Path
	editing, _ := h.DB.Editing(ctx, book.ID)
	msg := ""
	switch {
	case strings.HasSuffix(path, "/copy"):
		err, msg = h.DB.CopySheet(ctx, book.ID, id), "Page copied"
	case strings.HasSuffix(path, "/delete"):
		if id == editing {
			h.stopRun(n) // its lesson, if it was playing
		}
		err, msg = h.DB.RemoveSheet(ctx, book.ID, id), "Page deleted"
	default:
		to, _ := strconv.ParseInt(chi.URLParam(r, "to"), 10, 64)
		err, msg = h.DB.MoveSheet(ctx, book.ID, id, to), "Page moved"
	}
	if err != nil {
		slog.Error("page", "board", n, "path", path, "err", err)
		ack.Bad(datastar.NewSSE(w, r), "sheet", "Not done: "+err.Error())
		return
	}
	if strings.HasSuffix(path, "/delete") && id == editing {
		h.turnPage(r, n, s.UID, book.ID) // it was on the whiteboard: a clean page
	} else {
		sheetVersion.Add(1)
		h.Hub.Publish(n)
		_ = h.Bus.Publish(bus.BookSubject(book.ID), nil)
	}
	ack.Saved(datastar.NewSSE(w, r), "sheet", msg)
}

// newPage leaves a page being changed as it was, and turns to a clean one.
func (h *Handler) newPage(w http.ResponseWriter, r *http.Request) {
	n, _, meta, s, ok := h.teacher(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	book, err := h.DB.Book(ctx, h.DB.WhiteboardBook(ctx, meta.ID))
	if err != nil {
		datastar.NewSSE(w, r)
		return
	}
	_ = h.DB.SetEditing(ctx, book.ID, 0)
	h.turnPage(r, n, s.UID, book.ID)
	ack.Saved(datastar.NewSSE(w, r), "sheet", "A clean page")
}

// pageStrip is the whiteboard's footer (its book's pages) and its save
// button (a new page, or the page back on it), for this viewer.
func (h *Handler) pageStrip(r *http.Request, u *db.User, meta *hub.Meta, name string) string {
	owner := u != nil && u.ID == meta.OwnerID
	bookName := h.DB.WhiteboardBook(r.Context(), meta.ID)
	if bookName == "" {
		return renderString(r, view.PageStrip(name, nil, nil, owner, 0))
	}
	book, err := h.DB.Book(r.Context(), bookName)
	if err != nil {
		return renderString(r, view.PageStrip(name, nil, nil, owner, 0))
	}
	sheets, _ := h.DB.Sheets(r.Context(), book.ID)
	_, editing := h.DB.Editing(r.Context(), book.ID)
	return renderString(r, view.PageStrip(name, book, sheets, owner, editing))
}

func (h *Handler) sheetPage(w http.ResponseWriter, r *http.Request) {
	b, u, ok := h.openBook(w, r, true)
	if !ok {
		return
	}
	sheets, err := h.DB.Sheets(r.Context(), b.ID)
	if err != nil {
		http.Error(w, "book unavailable", http.StatusServiceUnavailable)
		return
	}
	no, _ := strconv.Atoi(chi.URLParam(r, "no"))
	if no < 1 || no > len(sheets) {
		h.notFound(w, r, true)
		return
	}
	mine, _ := h.DB.Marks(r.Context(), 0, u.ID, sheets[no-1].ID)
	html(w)
	_ = view.SheetView(u, b, sheets, no, marksOver(mine)).Render(r.Context(), w)
}

func (h *Handler) removeSheet(w http.ResponseWriter, r *http.Request) {
	b, u, ok := h.openBook(w, r, false)
	if !ok {
		return
	}
	if b.OwnerID != u.ID {
		http.Error(w, "only the owner can change this book", http.StatusForbidden)
		return
	}
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err := h.DB.RemoveSheet(r.Context(), b.ID, id); err != nil {
		ack.Bad(datastar.NewSSE(w, r), "book", "Not removed")
		return
	}
	sheetVersion.Add(1)
	_ = h.Bus.Publish(bus.BookSubject(b.ID), nil)
	if b.Board != "" {
		h.Hub.Publish(b.Board)
	}
	ack.Saved(datastar.NewSSE(w, r), "book", "Removed")
}
