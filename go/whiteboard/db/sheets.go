package db

import (
	"context"
	"encoding/json"
	"errors"

	"whiteboard/board"
)

// ─── A book's whiteboard and its sheets ─────────────────────────────────────
//
// A book made since sheets has one whiteboard, where its teacher draws. What
// is drawn is sent to the book as a sheet: a page, frozen (the shapes sent
// and their extent), in the book's last chapter; the whiteboard then turns
// to a clean page.

// Sheet is one page of a book.
type Sheet struct {
	ID           int64
	No           int // 1-based, through the whole book
	ChapterNo    int
	ChapterTitle string
	Drawing      board.Snapshot
	Box          [4]int32 // the drawing's extent: x1, y1, x2, y2
	Made         string   // the date it was sent, as SQLite writes it
}

// WhiteboardBook is the book a board is the whiteboard of ("" for none).
func (d *DB) WhiteboardBook(ctx context.Context, boardID int64) string {
	ctx, cancel := reading(ctx)
	defer cancel()
	var name string
	_ = d.rd.QueryRowContext(ctx, `SELECT name FROM books WHERE board_id = ?`, boardID).Scan(&name)
	return name
}

// CreateBookFor makes a book whose whiteboard is the given board, with a
// first chapter; it returns the book's name.
func (d *DB) CreateBookFor(ctx context.Context, owner *User, boardID int64, title, visibility string) (string, error) {
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	name, err := slug()
	if err != nil {
		return "", err
	}
	var bookID int64
	if err := tx.QueryRowContext(ctx, `INSERT INTO books (name, title, owner_id, visibility, board_id) VALUES (?, ?, ?, ?, ?) RETURNING id`,
		name, title, owner.ID, visibility, boardID).Scan(&bookID); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO book_chapters (book_id, position, title) VALUES (?, 1, 'Chapter 1')`, bookID); err != nil {
		return "", err
	}
	return name, tx.Commit()
}

// CreateWhiteboardBook makes a book and its whiteboard (a letter board, in
// the owner's boards); it returns the book's name and the board's.
func (d *DB) CreateWhiteboardBook(ctx context.Context, owner *User, title, visibility string) (string, string, error) {
	board, err := d.CreateBoard(ctx, owner, title, visibility)
	if err != nil {
		return "", "", err
	}
	m, err := d.Meta(ctx, board)
	if err != nil {
		return "", "", err
	}
	book, err := d.CreateBookFor(ctx, owner, m.ID, title, visibility)
	return book, board, err
}

// AddSheet adds a page to the end of the book's last chapter; it returns its
// number and id.
func (d *DB) AddSheet(ctx context.Context, bookID int64, drawing board.Snapshot, box [4]int32) (int, int64, error) {
	snap, err := json.Marshal(drawing)
	if err != nil {
		return 0, 0, err
	}
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()
	var chapter, id int64
	if err := tx.QueryRowContext(ctx, `SELECT id FROM book_chapters WHERE book_id = ? ORDER BY position DESC, id DESC LIMIT 1`, bookID).Scan(&chapter); err != nil {
		return 0, 0, err
	}
	if err := tx.QueryRowContext(ctx, `INSERT INTO book_sheets (book_id, chapter_id, position, snapshot, x1, y1, x2, y2)
		VALUES (?, ?, COALESCE((SELECT max(position) FROM book_sheets WHERE book_id = ?), 0) + 1, ?, ?, ?, ?, ?) RETURNING id`,
		bookID, chapter, bookID, string(snap), box[0], box[1], box[2], box[3]).Scan(&id); err != nil {
		return 0, 0, err
	}
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM book_sheets WHERE book_id = ?`, bookID).Scan(&n); err != nil {
		return 0, 0, err
	}
	return n, id, tx.Commit()
}

// Sheets is the book's pages in order: by chapter, then as sent.
func (d *DB) Sheets(ctx context.Context, bookID int64) ([]Sheet, error) {
	ctx, cancel := reading(ctx)
	defer cancel()
	rows, err := d.rd.QueryContext(ctx, `SELECT s.id, c.title, (SELECT count(*) FROM book_chapters c2 WHERE c2.book_id = c.book_id AND (c2.position < c.position OR (c2.position = c.position AND c2.id <= c.id))),
		s.snapshot, s.x1, s.y1, s.x2, s.y2, date(s.created_at, 'unixepoch', 'localtime')
		FROM book_sheets s JOIN book_chapters c ON c.id = s.chapter_id
		WHERE s.book_id = ? ORDER BY c.position, c.id, s.position`, bookID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Sheet
	for rows.Next() {
		var s Sheet
		var snap string
		if err := rows.Scan(&s.ID, &s.ChapterTitle, &s.ChapterNo, &snap, &s.Box[0], &s.Box[1], &s.Box[2], &s.Box[3], &s.Made); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(snap), &s.Drawing); err != nil {
			return nil, err
		}
		s.No = len(out) + 1
		out = append(out, s)
	}
	return out, rows.Err()
}

// RemoveSheet takes a page out of the book.
func (d *DB) RemoveSheet(ctx context.Context, bookID, sheetID int64) error {
	res, err := d.sql.ExecContext(ctx, `DELETE FROM book_sheets WHERE id = ? AND book_id = ?`, sheetID, bookID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errors.New("no such page")
	}
	return nil
}

// Editing is the page back on the book's whiteboard to be changed (its id
// and number), or 0, 0.
func (d *DB) Editing(ctx context.Context, bookID int64) (int64, int) {
	ctx, cancel := reading(ctx)
	defer cancel()
	var id int64
	if err := d.rd.QueryRowContext(ctx, `SELECT COALESCE(editing_sheet, 0) FROM books WHERE id = ?`, bookID).Scan(&id); err != nil || id == 0 {
		return 0, 0
	}
	sheets, err := d.Sheets(ctx, bookID)
	if err != nil {
		return 0, 0
	}
	for _, s := range sheets {
		if s.ID == id {
			return id, s.No
		}
	}
	return 0, 0
}

// SetEditing marks a page as back on the whiteboard (0: none).
func (d *DB) SetEditing(ctx context.Context, bookID, sheetID int64) error {
	var v any
	if sheetID != 0 {
		v = sheetID
	}
	_, err := d.sql.ExecContext(ctx, `UPDATE books SET editing_sheet = ? WHERE id = ?`, v, bookID)
	return err
}

// UpdateSheet saves a page again from the whiteboard (its place and
// chapter stay), and ends its editing.
func (d *DB) UpdateSheet(ctx context.Context, bookID, sheetID int64, drawing board.Snapshot, box [4]int32) error {
	snap, err := json.Marshal(drawing)
	if err != nil {
		return err
	}
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE book_sheets SET snapshot = ?, x1 = ?, y1 = ?, x2 = ?, y2 = ? WHERE id = ? AND book_id = ?`,
		string(snap), box[0], box[1], box[2], box[3], sheetID, bookID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errors.New("no such page")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE books SET editing_sheet = NULL WHERE id = ?`, bookID); err != nil {
		return err
	}
	return tx.Commit()
}
