package handler

import (
	"log/slog"
	"net/http"
	"strconv"
	"sync/atomic"

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

// sheetVersion counts pages sent and removed anywhere: a whiteboard's stream
// re-sends its page strip when it moves.
var sheetVersion atomic.Int64

func (h *Handler) sheetRoutes(r chi.Router) {
	r.Post("/b/{name}/sendpage", h.sendPage)
	r.Get("/book/{name}/page/{no}", h.sheetPage)
	r.Post("/book/{name}/sheet/{id}/remove", h.removeSheet)
}


func (h *Handler) sendPage(w http.ResponseWriter, r *http.Request) {
	n, u, meta, ok := h.open(w, r, false)
	if !ok {
		return
	}
	var s signals
	if err := datastar.ReadSignals(r, &s); err != nil {
		http.Error(w, "bad signals", http.StatusBadRequest)
		return
	}
	if h.moved(w, r, s.Inst) {
		return
	}
	if meta.OwnerID != u.ID {
		ack.Bad(datastar.NewSSE(w, r), "sheet", "Only the teacher sends pages")
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
	no, err := h.DB.AddSheet(ctx, book.ID, snap, box)
	if err != nil {
		slog.Error("send page", "board", n, "err", err)
		ack.Bad(datastar.NewSSE(w, r), "sheet", "Not sent")
		return
	}
	// the page is turned: a clean whiteboard (its history keeps the old one)
	if err := h.Hub.Apply(ctx, n, board.Cmd{Op: "clear", UID: s.UID}); err != nil {
		slog.Error("send page: clear", "board", n, "err", err)
	}
	sheetVersion.Add(1)
	h.Hub.Publish(n)
	_ = h.Bus.Publish(bus.BookSubject(book.ID), nil)
	ack.Saved(datastar.NewSSE(w, r), "sheet", "Page "+strconv.Itoa(no))
}

// pageStrip is the whiteboard's footer: its book's pages, or nothing.
func (h *Handler) pageStrip(r *http.Request, boardID int64, name string) string {
	bookName := h.DB.WhiteboardBook(r.Context(), boardID)
	if bookName == "" {
		return renderString(r, view.PageStrip(name, nil, nil))
	}
	book, err := h.DB.Book(r.Context(), bookName)
	if err != nil {
		return renderString(r, view.PageStrip(name, nil, nil))
	}
	sheets, _ := h.DB.Sheets(r.Context(), book.ID)
	return renderString(r, view.PageStrip(name, book, sheets))
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
	html(w)
	_ = view.SheetView(u, b, sheets, no).Render(r.Context(), w)
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

