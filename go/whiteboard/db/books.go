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
	Board      string // its whiteboard (a book made since sheets), "" for a book of boards
	Author     string // the owner's name
}

type Chapter struct {
	ID    int64
	No    int // 1-based, in the book's order
	Title string
}

// CanOpen: a public book to anyone signed in, a private one to its owner.
func (b *Book) CanOpen(u *User) bool {
	return u != nil && (b.Visibility == Public || b.OwnerID == u.ID)
}

// Book reads a book and its contents.
func (d *DB) Book(ctx context.Context, name string) (*Book, error) {
	ctx, cancel := reading(ctx)
	defer cancel()
	b := &Book{Name: name}
	err := d.rd.QueryRowContext(ctx, `SELECT b.id, b.title, b.owner_id, b.visibility, COALESCE(w.name, ''), COALESCE(u.name, '')
		FROM books b LEFT JOIN boards w ON w.id = b.board_id LEFT JOIN users u ON u.id = b.owner_id WHERE b.name = ?`, name).
		Scan(&b.ID, &b.Title, &b.OwnerID, &b.Visibility, &b.Board, &b.Author)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoBook
	}
	if err != nil {
		return nil, err
	}
	rows, err := d.rd.QueryContext(ctx, `SELECT id, title FROM book_chapters WHERE book_id = ? ORDER BY position, id`, b.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		c := Chapter{No: len(b.Chapters) + 1}
		if err := rows.Scan(&c.ID, &c.Title); err != nil {
			return nil, err
		}
		b.Chapters = append(b.Chapters, c)
	}
	return b, rows.Err()
}

// BooksOf lists a user's own books, newest first.
func (d *DB) BooksOf(ctx context.Context, userID int64) ([]Book, error) {
	ctx, cancel := reading(ctx)
	defer cancel()
	rows, err := d.rd.QueryContext(ctx, `SELECT b.id, b.name, b.title, b.owner_id, b.visibility, COALESCE(w.name, '') FROM books b
		LEFT JOIN boards w ON w.id = b.board_id WHERE b.owner_id = ? ORDER BY b.id DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Book
	for rows.Next() {
		var b Book
		if err := rows.Scan(&b.ID, &b.Name, &b.Title, &b.OwnerID, &b.Visibility, &b.Board); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// AddChapter adds "Chapter N" at the end of the book: the pages sent next
// go in it.
func (d *DB) AddChapter(ctx context.Context, book *Book) error {
	_, err := d.sql.ExecContext(ctx, `INSERT INTO book_chapters (book_id, position, title)
		VALUES (?, COALESCE((SELECT max(position) FROM book_chapters WHERE book_id = ?), 0) + 1, ?)`,
		book.ID, book.ID, "Chapter "+strconv.Itoa(len(book.Chapters)+1))
	return err
}

func (d *DB) RenameBook(ctx context.Context, bookID int64, title string) error {
	_, err := d.sql.ExecContext(ctx, `UPDATE books SET title = ? WHERE id = ?`, title, bookID)
	return err
}

func (d *DB) RenameChapter(ctx context.Context, bookID, chapterID int64, title string) error {
	_, err := d.sql.ExecContext(ctx, `UPDATE book_chapters SET title = ? WHERE id = ? AND book_id = ?`, title, chapterID, bookID)
	return err
}
