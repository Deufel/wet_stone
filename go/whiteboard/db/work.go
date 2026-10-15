package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

var sprintf = fmt.Sprintf

// ─── The checklist ──────────────────────────────────────────────────────────

// Todo is one checklist item as shown: SQLite formats the times (today's as
// 15:04, older ones with the date), in the server's local time zone (TZ).
type Todo struct {
	ID      int64
	Text    string
	Done    bool
	DoneAt  string // "14:03" or "10/14 09:12"; "" while open
	DoneBy  string // the first name of who checked it
	DoneISO string // for <time datetime>
}

const whenSQL = `CASE WHEN date(%[1]s, 'unixepoch', 'localtime') = date('now', 'localtime')
	THEN strftime('%%H:%%M', %[1]s, 'unixepoch', 'localtime')
	ELSE strftime('%%m/%%d %%H:%%M', %[1]s, 'unixepoch', 'localtime') END`

// Todos lists a board's checklist: open items first in the order added, then
// the done ones, most recently done first.
func (d *DB) Todos(ctx context.Context, boardID int64) ([]Todo, error) {
	rows, err := d.sql.QueryContext(ctx, `SELECT t.id, t.text, t.done_at IS NOT NULL,
		COALESCE(`+sprintf(whenSQL, "t.done_at")+`, ''),
		COALESCE(substr(u.name, 1, instr(u.name || ' ', ' ') - 1), ''),
		COALESCE(strftime('%Y-%m-%dT%H:%M:%SZ', t.done_at, 'unixepoch'), '')
		FROM todos t LEFT JOIN users u ON u.id = t.done_by
		WHERE t.board_id = ?
		ORDER BY t.done_at IS NOT NULL, CASE WHEN t.done_at IS NULL THEN t.id ELSE -t.done_at END`, boardID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Todo
	for rows.Next() {
		var t Todo
		if err := rows.Scan(&t.ID, &t.Text, &t.Done, &t.DoneAt, &t.DoneBy, &t.DoneISO); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (d *DB) AddTodo(ctx context.Context, boardID, userID int64, text string) error {
	_, err := d.sql.ExecContext(ctx, `INSERT INTO todos (board_id, text, created_by) VALUES (?, ?, ?)`, boardID, text, userID)
	return err
}

// ToggleTodo checks an item (stamping now and who) or unchecks it.
func (d *DB) ToggleTodo(ctx context.Context, boardID, id, userID int64) error {
	_, err := d.sql.ExecContext(ctx, `UPDATE todos SET
		done_at = CASE WHEN done_at IS NULL THEN unixepoch() END,
		done_by = CASE WHEN done_at IS NULL THEN ? END
		WHERE id = ? AND board_id = ?`, userID, id, boardID)
	return err
}

func (d *DB) DeleteTodo(ctx context.Context, boardID, id int64) error {
	_, err := d.sql.ExecContext(ctx, `DELETE FROM todos WHERE id = ? AND board_id = ?`, id, boardID)
	return err
}

// ─── Private notes ──────────────────────────────────────────────────────────

// Note is one user's notes on one board. Updated (ms, from SQLite) tells a
// stream whether it has sent this version yet.
type Note struct {
	Text    string `json:"text"`
	Lang    string `json:"lang"`
	Updated int64  `json:"v"`
}

func (d *DB) Note(ctx context.Context, userID, boardID int64) (Note, error) {
	var n Note
	err := d.sql.QueryRowContext(ctx, `SELECT text, lang, updated_at FROM notes WHERE user_id = ? AND board_id = ?`, userID, boardID).
		Scan(&n.Text, &n.Lang, &n.Updated)
	if errors.Is(err, sql.ErrNoRows) {
		return Note{}, nil
	}
	return n, err
}

func (d *DB) SaveNote(ctx context.Context, userID, boardID int64, text, lang string) error {
	_, err := d.sql.ExecContext(ctx, `INSERT INTO notes (user_id, board_id, text, lang) VALUES (?, ?, ?, ?)
		ON CONFLICT (user_id, board_id) DO UPDATE SET text = excluded.text, lang = excluded.lang,
		updated_at = unixepoch('subsec') * 1000`, userID, boardID, text, lang)
	return err
}

// Now is SQLite's clock, formatted for a printed page.
func (d *DB) Now(ctx context.Context) string {
	var s string
	_ = d.sql.QueryRowContext(ctx, `SELECT strftime('%Y-%m-%d %H:%M', 'now', 'localtime')`).Scan(&s)
	return s
}
