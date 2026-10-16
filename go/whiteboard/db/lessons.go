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

// AIKey is one of a user's keys for an AI service; its secret never leaves
// the server (Hint is its last four characters).
type AIKey struct {
	ID        int64
	Service   string // "anthropic"
	Label     string
	Hint      string
	Workspace string
	Active    bool
}

// AIKeys is a user's keys, active first, then newest.
func (d *DB) AIKeys(ctx context.Context, userID int64) ([]AIKey, error) {
	ctx, cancel := reading(ctx)
	defer cancel()
	rows, err := d.rd.QueryContext(ctx, `SELECT id, service, label, substr(secret, -4), workspace, active FROM ai_keys
		WHERE user_id = ? ORDER BY active DESC, id DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AIKey
	for rows.Next() {
		var k AIKey
		if err := rows.Scan(&k.ID, &k.Service, &k.Label, &k.Hint, &k.Workspace, &k.Active); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// AddAIKey keeps a new key, active (the service's others go inactive).
func (d *DB) AddAIKey(ctx context.Context, userID int64, service, label, secret, workspace string) error {
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE ai_keys SET active = 0 WHERE user_id = ? AND service = ?`, userID, service); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO ai_keys (user_id, service, label, secret, workspace, active) VALUES (?, ?, ?, ?, ?, 1)`,
		userID, service, label, secret, workspace); err != nil {
		return err
	}
	return tx.Commit()
}

// SetAIKeyActive turns a key on (its service's others off) or off.
func (d *DB) SetAIKeyActive(ctx context.Context, userID, id int64, on bool) error {
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if on {
		if _, err := tx.ExecContext(ctx, `UPDATE ai_keys SET active = 0 WHERE user_id = ? AND service = (SELECT service FROM ai_keys WHERE id = ? AND user_id = ?)`, userID, id, userID); err != nil {
			return err
		}
	}
	res, err := tx.ExecContext(ctx, `UPDATE ai_keys SET active = ? WHERE id = ? AND user_id = ?`, on, id, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errors.New("no such key")
	}
	return tx.Commit()
}

// RemoveAIKey forgets a key.
func (d *DB) RemoveAIKey(ctx context.Context, userID, id int64) error {
	_, err := d.sql.ExecContext(ctx, `DELETE FROM ai_keys WHERE id = ? AND user_id = ?`, id, userID)
	return err
}

// SetAIKeyWorkspace names the workspace a key's requests name ("" none).
func (d *DB) SetAIKeyWorkspace(ctx context.Context, userID, id int64, workspace string) error {
	_, err := d.sql.ExecContext(ctx, `UPDATE ai_keys SET workspace = ? WHERE id = ? AND user_id = ?`, workspace, id, userID)
	return err
}

// AnthropicKey is the user's active Anthropic key and the workspace its
// requests name ("" for none), for the server's own calls only.
func (d *DB) AnthropicKey(ctx context.Context, userID int64) (key, workspace string, err error) {
	ctx, cancel := reading(ctx)
	defer cancel()
	err = d.rd.QueryRowContext(ctx, `SELECT secret, workspace FROM ai_keys WHERE user_id = ? AND service = 'anthropic' AND active = 1
		ORDER BY id DESC LIMIT 1`, userID).Scan(&key, &workspace)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", nil
	}
	return key, workspace, err
}

const boardLetter = board.Letter
