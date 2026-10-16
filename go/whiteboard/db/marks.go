package db

import (
	"context"
	"encoding/json"
)

// ─── A student's own marks ──────────────────────────────────────────────────
//
// On a book's whiteboard, someone who is not its teacher draws with a pencil
// that only they see: their comments, questions and notes. A mark is a
// stroke's points; a page sent from the whiteboard takes the marks on it.

// AddMark keeps a stroke a student drew on the whiteboard.
func (d *DB) AddMark(ctx context.Context, boardID, userID int64, points []int32) error {
	p, err := json.Marshal(points)
	if err != nil {
		return err
	}
	_, err = d.sql.ExecContext(ctx, `INSERT INTO board_marks (board_id, user_id, points) VALUES (?, ?, ?)`, boardID, userID, string(p))
	return err
}

// Marks are a student's strokes on the whiteboard (sheetID 0) or on a page.
func (d *DB) Marks(ctx context.Context, boardID, userID, sheetID int64) ([][]int32, error) {
	ctx, cancel := reading(ctx)
	defer cancel()
	q := `SELECT points FROM board_marks WHERE board_id = ? AND user_id = ? AND sheet_id IS NULL ORDER BY id`
	args := []any{boardID, userID}
	if sheetID != 0 {
		q = `SELECT points FROM board_marks WHERE user_id = ? AND sheet_id = ? ORDER BY id`
		args = []any{userID, sheetID}
	}
	rows, err := d.rd.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out [][]int32
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		var p []int32
		if json.Unmarshal([]byte(s), &p) == nil {
			out = append(out, p)
		}
	}
	return out, rows.Err()
}

// ClearMarks takes away a student's marks on the whiteboard.
func (d *DB) ClearMarks(ctx context.Context, boardID, userID int64) error {
	_, err := d.sql.ExecContext(ctx, `DELETE FROM board_marks WHERE board_id = ? AND user_id = ? AND sheet_id IS NULL`, boardID, userID)
	return err
}

// MarksToSheet: the page just saved takes every student's marks on the
// whiteboard with it; it returns who had some.
func (d *DB) MarksToSheet(ctx context.Context, boardID, sheetID int64) ([]int64, error) {
	rows, err := d.sql.QueryContext(ctx, `UPDATE board_marks SET sheet_id = ? WHERE board_id = ? AND sheet_id IS NULL RETURNING user_id`, sheetID, boardID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seen := map[int64]bool{}
	var out []int64
	for rows.Next() {
		var u int64
		if rows.Scan(&u) == nil && !seen[u] {
			seen[u] = true
			out = append(out, u)
		}
	}
	return out, rows.Err()
}
