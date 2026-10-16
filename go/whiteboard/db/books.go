package db

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
)

// ErrNoBook is a book name that is not there.
var ErrNoBook = errors.New("no such book")

// Book is a collection of boards: chapters in order, pages in order.
type Book struct {
	ID         int64
	Name       string // the URL slug
	Title      string
	OwnerID    int64
	Visibility string // public or private (owner only)
	Chapters   []Chapter
}

type Chapter struct {
	ID    int64
	No    int // 1-based, in the book's order
	Title string
	Pages []Page
}

// Page is one board in a book. No counts pages through the whole book.
type Page struct {
	ID         int64
	No         int
	Board      string // the board's name
	Title      string
	Visibility string
}

// CanOpen: a public book to anyone signed in, a private one to its owner.
func (b *Book) CanOpen(u *User) bool {
	return u != nil && (b.Visibility == Public || b.OwnerID == u.ID)
}

// Pages is every page in order.
func (b *Book) Pages() []Page {
	var out []Page
	for _, c := range b.Chapters {
		out = append(out, c.Pages...)
	}
	return out
}

// CreateBook makes a book with a first chapter and a first page; it returns
// the book's name and the first page's board.
func (d *DB) CreateBook(ctx context.Context, owner *User, title, visibility string) (string, string, error) {
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return "", "", err
	}
	defer tx.Rollback()
	name, err := slug()
	if err != nil {
		return "", "", err
	}
	var bookID, chID int64
	if err := tx.QueryRowContext(ctx, `INSERT INTO books (name, title, owner_id, visibility) VALUES (?, ?, ?, ?) RETURNING id`,
		name, title, owner.ID, visibility).Scan(&bookID); err != nil {
		return "", "", err
	}
	if err := tx.QueryRowContext(ctx, `INSERT INTO book_chapters (book_id, position, title) VALUES (?, 1, 'Chapter 1') RETURNING id`, bookID).Scan(&chID); err != nil {
		return "", "", err
	}
	page, err := addPage(ctx, tx, chID, owner.ID, visibility, "Page 1")
	if err != nil {
		return "", "", err
	}
	return name, page, tx.Commit()
}

// addPage makes a board at the end of a chapter.
func addPage(ctx context.Context, tx *sql.Tx, chapterID, ownerID int64, visibility, title string) (string, error) {
	boardID, board, err := newBoard(ctx, tx, ownerID, title, visibility)
	if err != nil {
		return "", err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO book_pages (chapter_id, board_id, position)
		VALUES (?, ?, COALESCE((SELECT max(position) FROM book_pages WHERE chapter_id = ?), 0) + 1)`, chapterID, boardID, chapterID)
	return board, err
}

// Book reads a book and its contents.
func (d *DB) Book(ctx context.Context, name string) (*Book, error) {
	ctx, cancel := reading(ctx)
	defer cancel()
	b := &Book{Name: name}
	err := d.rd.QueryRowContext(ctx, `SELECT id, title, owner_id, visibility FROM books WHERE name = ?`, name).
		Scan(&b.ID, &b.Title, &b.OwnerID, &b.Visibility)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoBook
	}
	if err != nil {
		return nil, err
	}
	rows, err := d.rd.QueryContext(ctx, `SELECT c.id, c.title, p.id, bo.name, bo.title, bo.visibility
		FROM book_chapters c LEFT JOIN book_pages p ON p.chapter_id = c.id LEFT JOIN boards bo ON bo.id = p.board_id
		WHERE c.book_id = ? ORDER BY c.position, c.id, p.position, p.id`, b.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	page := 0
	for rows.Next() {
		var chID int64
		var chTitle string
		var pID sql.NullInt64
		var board, title, vis sql.NullString
		if err := rows.Scan(&chID, &chTitle, &pID, &board, &title, &vis); err != nil {
			return nil, err
		}
		if n := len(b.Chapters); n == 0 || b.Chapters[n-1].ID != chID {
			b.Chapters = append(b.Chapters, Chapter{ID: chID, No: n + 1, Title: chTitle})
		}
		if pID.Valid {
			page++
			c := &b.Chapters[len(b.Chapters)-1]
			c.Pages = append(c.Pages, Page{ID: pID.Int64, No: page, Board: board.String, Title: title.String, Visibility: vis.String})
		}
	}
	return b, rows.Err()
}

// BookOf is the book a board is a page of: its name, or "" for none.
func (d *DB) BookOf(ctx context.Context, boardID int64) string {
	ctx, cancel := reading(ctx)
	defer cancel()
	var name string
	_ = d.rd.QueryRowContext(ctx, `SELECT b.name FROM book_pages p JOIN book_chapters c ON c.id = p.chapter_id
		JOIN books b ON b.id = c.book_id WHERE p.board_id = ?`, boardID).Scan(&name)
	return name
}

// BooksOf lists a user's own books, newest first.
func (d *DB) BooksOf(ctx context.Context, userID int64) ([]Book, error) {
	ctx, cancel := reading(ctx)
	defer cancel()
	rows, err := d.rd.QueryContext(ctx, `SELECT id, name, title, owner_id, visibility FROM books WHERE owner_id = ? ORDER BY id DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Book
	for rows.Next() {
		var b Book
		if err := rows.Scan(&b.ID, &b.Name, &b.Title, &b.OwnerID, &b.Visibility); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// AddChapter adds "Chapter N" at the end, with a first page; it returns the page's board.
func (d *DB) AddChapter(ctx context.Context, book *Book) (string, error) {
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	n := len(book.Chapters) + 1
	var chID int64
	if err := tx.QueryRowContext(ctx, `INSERT INTO book_chapters (book_id, position, title)
		VALUES (?, COALESCE((SELECT max(position) FROM book_chapters WHERE book_id = ?), 0) + 1, ?) RETURNING id`,
		book.ID, book.ID, "Chapter "+strconv.Itoa(n)).Scan(&chID); err != nil {
		return "", err
	}
	board, err := addPage(ctx, tx, chID, book.OwnerID, book.Visibility, "Page "+strconv.Itoa(len(book.Pages())+1))
	if err != nil {
		return "", err
	}
	return board, tx.Commit()
}

// AddPage adds a page at the end of a chapter of the book; it returns its board.
func (d *DB) AddPage(ctx context.Context, book *Book, chapterID int64) (string, error) {
	ok := false
	for _, c := range book.Chapters {
		ok = ok || c.ID == chapterID
	}
	if !ok {
		return "", ErrNoBook
	}
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	board, err := addPage(ctx, tx, chapterID, book.OwnerID, book.Visibility, "Page "+strconv.Itoa(len(book.Pages())+1))
	if err != nil {
		return "", err
	}
	return board, tx.Commit()
}

func (d *DB) RenameBook(ctx context.Context, bookID int64, title string) error {
	_, err := d.sql.ExecContext(ctx, `UPDATE books SET title = ? WHERE id = ?`, title, bookID)
	return err
}

func (d *DB) RenameChapter(ctx context.Context, bookID, chapterID int64, title string) error {
	_, err := d.sql.ExecContext(ctx, `UPDATE book_chapters SET title = ? WHERE id = ? AND book_id = ?`, title, chapterID, bookID)
	return err
}

// RemovePage takes a page out of the book; its board stays (in its owner's boards).
func (d *DB) RemovePage(ctx context.Context, bookID, pageID int64) error {
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var boardID, ownerID int64
	if err := tx.QueryRowContext(ctx, `SELECT p.board_id, b.owner_id FROM book_pages p JOIN book_chapters c ON c.id = p.chapter_id
		JOIN books b ON b.id = c.book_id WHERE p.id = ? AND b.id = ?`, pageID, bookID).Scan(&boardID, &ownerID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM book_pages WHERE id = ?`, pageID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO tabs (user_id, board_id, opened_ms) VALUES (?, ?, unixepoch() * 1000)`, ownerID, boardID); err != nil {
		return err
	}
	return tx.Commit()
}
