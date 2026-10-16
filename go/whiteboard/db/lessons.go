package db

import (
	"context"
	"database/sql"
	"errors"
	"whiteboard/board"
)

// ─── Lesson books ───────────────────────────────────────────────────────────

// CourseChapter and CoursePage are a lesson book to be made: each page
// carries the script presented on the book's whiteboard.
type CourseChapter struct {
	Title string
	Pages []CoursePage
}

type CoursePage struct {
	Title  string
	Script string // the page's lesson, JSON
}

// CreateCourse makes a lesson book: a whiteboard, chapters, and pages that
// each carry a lesson (their drawing empty until it is first presented); it
// returns the book's name and its whiteboard's.
func (d *DB) CreateCourse(ctx context.Context, owner *User, title, visibility string, chapters []CourseChapter) (string, string, error) {
	board, err := d.CreateBoard(ctx, owner, title, visibility)
	if err != nil {
		return "", "", err
	}
	m, err := d.Meta(ctx, board)
	if err != nil {
		return "", "", err
	}
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return "", "", err
	}
	defer tx.Rollback()
	name, err := slug()
	if err != nil {
		return "", "", err
	}
	var bookID int64
	if err := tx.QueryRowContext(ctx, `INSERT INTO books (name, title, owner_id, visibility, board_id) VALUES (?, ?, ?, ?, ?) RETURNING id`,
		name, title, owner.ID, visibility, m.ID).Scan(&bookID); err != nil {
		return "", "", err
	}
	pos := 0
	for i, c := range chapters {
		var chID int64
		if err := tx.QueryRowContext(ctx, `INSERT INTO book_chapters (book_id, position, title) VALUES (?, ?, ?) RETURNING id`,
			bookID, i+1, c.Title).Scan(&chID); err != nil {
			return "", "", err
		}
		for _, p := range c.Pages {
			pos++
			if _, err := tx.ExecContext(ctx, `INSERT INTO book_sheets (book_id, chapter_id, position, snapshot, x1, y1, x2, y2, lesson)
				VALUES (?, ?, ?, '{}', 0, 0, 1000, ?, ?)`, bookID, chID, pos, boardLetter, p.Script); err != nil {
				return "", "", err
			}
		}
	}
	return name, board, tx.Commit()
}

// AttachLesson gives a board a lesson, to play when its owner opens it (a
// demo: a board of its own, not a book's page).
func (d *DB) AttachLesson(ctx context.Context, board, script string) error {
	_, err := d.sql.ExecContext(ctx, `INSERT INTO board_lessons (board_id, script) SELECT id, ? FROM boards WHERE name = ?
		ON CONFLICT (board_id) DO UPDATE SET script = excluded.script, played_at = NULL`, script, board)
	return err
}

// HasLesson: does the board carry a lesson script?
func (d *DB) HasLesson(ctx context.Context, boardID int64) bool {
	ctx, cancel := reading(ctx)
	defer cancel()
	var n int
	_ = d.rd.QueryRowContext(ctx, `SELECT count(*) FROM board_lessons WHERE board_id = ?`, boardID).Scan(&n)
	return n > 0
}

// ClaimLesson hands out the board's script the first time only (it marks it
// played); ok is false once played, or with no lesson.
func (d *DB) ClaimLesson(ctx context.Context, boardID int64) (string, bool) {
	var script string
	err := d.sql.QueryRowContext(ctx, `UPDATE board_lessons SET played_at = unixepoch()
		WHERE board_id = ? AND played_at IS NULL RETURNING script`, boardID).Scan(&script)
	return script, err == nil
}

// LessonScript is the board's script, played or not ("" for none).
func (d *DB) LessonScript(ctx context.Context, boardID int64) string {
	ctx, cancel := reading(ctx)
	defer cancel()
	var script string
	_ = d.rd.QueryRowContext(ctx, `SELECT script FROM board_lessons WHERE board_id = ?`, boardID).Scan(&script)
	return script
}

// ClearTodos empties a board's checklist (a lesson played again).
func (d *DB) ClearTodos(ctx context.Context, boardID int64) error {
	_, err := d.sql.ExecContext(ctx, `DELETE FROM todos WHERE board_id = ?`, boardID)
	return err
}

// ─── The user's Anthropic key ───────────────────────────────────────────────

// SetAnthropicKey keeps the key ("" removes it).
func (d *DB) SetAnthropicKey(ctx context.Context, userID int64, key string) error {
	if key == "" {
		_, err := d.sql.ExecContext(ctx, `DELETE FROM user_keys WHERE user_id = ?`, userID)
		return err
	}
	_, err := d.sql.ExecContext(ctx, `INSERT INTO user_keys (user_id, anthropic) VALUES (?, ?)
		ON CONFLICT (user_id) DO UPDATE SET anthropic = excluded.anthropic, updated_at = unixepoch()`, userID, key)
	return err
}

// AnthropicKey is the user's key and the workspace its requests name ("" for
// none), for the server's own calls only.
func (d *DB) AnthropicKey(ctx context.Context, userID int64) (key, workspace string, err error) {
	ctx, cancel := reading(ctx)
	defer cancel()
	err = d.rd.QueryRowContext(ctx, `SELECT anthropic, workspace FROM user_keys WHERE user_id = ?`, userID).Scan(&key, &workspace)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", nil
	}
	return key, workspace, err
}

// SetAnthropicWorkspace names the workspace for the user's key ("" for none).
func (d *DB) SetAnthropicWorkspace(ctx context.Context, userID int64, workspace string) error {
	_, err := d.sql.ExecContext(ctx, `UPDATE user_keys SET workspace = ? WHERE user_id = ?`, workspace, userID)
	return err
}

const boardLetter = board.Letter
