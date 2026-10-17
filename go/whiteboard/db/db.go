// Package db keeps each board's drawing history in SQLite (modernc, pure Go):
// revisions (whole drawings as JSON, each with a parent), named branch heads,
// and the board's checked-out branch and revision.
package db

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"whiteboard/board"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schema string

type DB struct {
	sql *sql.DB // the one writer
	rd  *sql.DB // the readers (WAL: any number beside the writer)
}

// Rev is one revision without its drawing.
type Rev struct {
	ID, Parent int64 // Parent 0 = the first revision
	At         time.Time
	Op         string
	Shapes     int
}

type Branch struct {
	ID   int64
	Name string
	Head int64 // 0 = no revision yet
	Main bool
}

// Pos is what a board has checked out. Branch 0 = detached at Rev.
type Pos struct {
	Board  int64
	Branch int64
	Rev    int64
}

func Open(path string) (*DB, error) {
	sqldb, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(on)")
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	// One writer at a time is all SQLite allows anyway; one connection also
	// means no pool slot can inherit a pending interrupt (see eos-go db.Open).
	sqldb.SetMaxOpenConns(1)
	if _, err := sqldb.Exec(schema); err != nil {
		sqldb.Close()
		return nil, fmt.Errorf("schema: %w", err)
	}
	if err := migrate(sqldb); err != nil {
		sqldb.Close()
		return nil, err
	}
	// The readers: WAL lets any number of them run beside the one writer, so
	// a session lookup (every request, every pointer move) or a page's reads
	// never queue behind a commit. query_only keeps them honest.
	rd, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(on)&_pragma=query_only(1)")
	if err != nil {
		sqldb.Close()
		return nil, fmt.Errorf("open readers: %w", err)
	}
	rd.SetMaxOpenConns(8)
	rd.SetMaxIdleConns(8)
	return &DB{sql: sqldb, rd: rd}, nil
}

// reading detaches a read from its request's cancellation (with its own short
// limit instead). A cancelled context makes the driver interrupt the
// connection mid-query, and with a pool of connections that interrupt can
// linger and fail the next query to use it (eos-go learnt this); a page
// cancels its pointer posts all the time.
func reading(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
}

func (d *DB) Close() error {
	d.rd.Close()
	return d.sql.Close()
}

// Ping reports whether the database answers.
func (d *DB) Ping(ctx context.Context) error { return d.sql.PingContext(ctx) }

// GlobalName is the one board that makes itself: everyone's shared board.
const GlobalName = "main"

// DemoName is the other: the demo anyone may draw on, signed in or not, with
// a pen and the laser only; it clears itself every minute.
const DemoName = "demo"

// Ensure returns the board's position. Only the global board is created on
// first use; any other unknown name is ErrNoBoard (boards are made by CreateBoard).
func (d *DB) Ensure(ctx context.Context, name string) (Pos, error) {
	var p Pos
	var branch, rev sql.NullInt64
	err := d.sql.QueryRowContext(ctx, `SELECT id, branch_id, rev_id FROM boards WHERE name = ?`, name).Scan(&p.Board, &branch, &rev)
	if err == nil {
		p.Branch, p.Rev = branch.Int64, rev.Int64
		return p, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return p, err
	}
	if name != GlobalName && name != DemoName {
		return p, ErrNoBoard
	}
	title, vis, height := "Global board", "global", 1000
	if name == DemoName {
		title, vis, height = "Try draw", "public", 1294
	}
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return p, err
	}
	defer tx.Rollback()
	if err := tx.QueryRowContext(ctx, `INSERT INTO boards (name, title, visibility, created_ms, height) VALUES (?, ?, ?, ?, ?) RETURNING id`,
		name, title, vis, time.Now().UnixMilli(), height).Scan(&p.Board); err != nil {
		return p, err
	}
	if err := tx.QueryRowContext(ctx, `INSERT INTO branches (board_id, name, is_main) VALUES (?, 'main', 1) RETURNING id`, p.Board).Scan(&p.Branch); err != nil {
		return p, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE boards SET branch_id = ? WHERE id = ?`, p.Branch, p.Board); err != nil {
		return p, err
	}
	return p, tx.Commit()
}

// Snapshot loads one revision's drawing; revision 0 is the empty board.
func (d *DB) Snapshot(ctx context.Context, rev int64) (board.Snapshot, error) {
	ctx, cancel := reading(ctx)
	defer cancel()
	var snap board.Snapshot
	if rev == 0 {
		return snap, nil
	}
	var raw string
	if err := d.rd.QueryRowContext(ctx, `SELECT snapshot FROM revisions WHERE id = ?`, rev).Scan(&raw); err != nil {
		return snap, err
	}
	return snap, json.Unmarshal([]byte(raw), &snap)
}

// History lists the board's revisions (oldest first) and branches.
func (d *DB) History(ctx context.Context, boardID int64) ([]Rev, []Branch, error) {
	ctx, cancel := reading(ctx)
	defer cancel()
	rows, err := d.rd.QueryContext(ctx, `SELECT id, COALESCE(parent_id, 0), at_ms, op, shapes FROM revisions WHERE board_id = ? ORDER BY id`, boardID)
	if err != nil {
		return nil, nil, err
	}
	var revs []Rev
	for rows.Next() {
		var r Rev
		var ms int64
		if err := rows.Scan(&r.ID, &r.Parent, &ms, &r.Op, &r.Shapes); err != nil {
			rows.Close()
			return nil, nil, err
		}
		r.At = time.UnixMilli(ms)
		revs = append(revs, r)
	}
	rows.Close()

	rows, err = d.rd.QueryContext(ctx, `SELECT id, name, COALESCE(head, 0), is_main FROM branches WHERE board_id = ? ORDER BY id`, boardID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var branches []Branch
	for rows.Next() {
		var b Branch
		if err := rows.Scan(&b.ID, &b.Name, &b.Head, &b.Main); err != nil {
			return nil, nil, err
		}
		branches = append(branches, b)
	}
	return revs, branches, rows.Err()
}

// Commit records snap as a new revision after p.Rev. On a branch it advances
// the branch; detached, it starts a new branch there. amend replaces p.Rev in
// place instead (a burst of the same edit folding into one revision). It
// returns the new position.
func (d *DB) Commit(ctx context.Context, p Pos, op string, snap board.Snapshot, amend bool) (Pos, error) {
	raw, err := json.Marshal(snap)
	if err != nil {
		return p, err
	}
	now := time.Now().UnixMilli()
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return p, err
	}
	defer tx.Rollback()

	if amend && p.Rev != 0 {
		if _, err := tx.ExecContext(ctx, `UPDATE revisions SET at_ms = ?, shapes = ?, snapshot = ? WHERE id = ?`,
			now, len(snap.Shapes), string(raw), p.Rev); err != nil {
			return p, err
		}
		return p, tx.Commit()
	}

	var parent any
	if p.Rev != 0 {
		parent = p.Rev
	}
	var rev int64
	if err := tx.QueryRowContext(ctx, `INSERT INTO revisions (board_id, parent_id, at_ms, op, shapes, snapshot)
		VALUES (?, ?, ?, ?, ?, ?) RETURNING id`, p.Board, parent, now, op, len(snap.Shapes), string(raw)).Scan(&rev); err != nil {
		return p, err
	}
	if p.Branch == 0 {
		name, err := freeBranchName(ctx, tx, p.Board)
		if err != nil {
			return p, err
		}
		if err := tx.QueryRowContext(ctx, `INSERT INTO branches (board_id, name, head) VALUES (?, ?, ?) RETURNING id`,
			p.Board, name, rev).Scan(&p.Branch); err != nil {
			return p, err
		}
	} else if _, err := tx.ExecContext(ctx, `UPDATE branches SET head = ? WHERE id = ?`, rev, p.Branch); err != nil {
		return p, err
	}
	p.Rev = rev
	if err := setPos(ctx, tx, p); err != nil {
		return p, err
	}
	return p, tx.Commit()
}

// Checkout puts rev on screen. If a branch's head is rev it is checked out
// (the current branch preferred, then main); otherwise the board is detached.
func (d *DB) Checkout(ctx context.Context, p Pos, rev int64) (Pos, error) {
	var branch int64
	err := d.sql.QueryRowContext(ctx, `SELECT id FROM branches WHERE board_id = ? AND head = ?
		ORDER BY (id = ?) DESC, is_main DESC, id LIMIT 1`, p.Board, rev, p.Branch).Scan(&branch)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return p, err
	}
	p.Branch, p.Rev = branch, rev
	return p, setPos(ctx, d.sql, p)
}

// Parent is the revision a revision was made from (0: it is the first).
func (d *DB) Parent(ctx context.Context, rev int64) (int64, error) {
	ctx, cancel := reading(ctx)
	defer cancel()
	var parent sql.NullInt64
	err := d.rd.QueryRowContext(ctx, `SELECT parent_id FROM revisions WHERE id = ?`, rev).Scan(&parent)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return parent.Int64, err
}

// Switch checks out a branch at its head.
func (d *DB) Switch(ctx context.Context, p Pos, branch int64) (Pos, error) {
	var head int64
	if err := d.sql.QueryRowContext(ctx, `SELECT COALESCE(head, 0) FROM branches WHERE id = ? AND board_id = ?`,
		branch, p.Board).Scan(&head); err != nil {
		return p, err
	}
	p.Branch, p.Rev = branch, head
	return p, setPos(ctx, d.sql, p)
}

func (d *DB) MakeMain(ctx context.Context, p Pos, branch int64) error {
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE branches SET is_main = (id = ?) WHERE board_id = ?`, branch, p.Board)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errors.New("no such branch")
	}
	return tx.Commit()
}

// DeleteBranch removes a branch that is neither main nor checked out, then
// every revision no remaining branch head (or the checked-out revision) reaches.
func (d *DB) DeleteBranch(ctx context.Context, p Pos, branch int64) error {
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var main bool
	if err := tx.QueryRowContext(ctx, `SELECT is_main FROM branches WHERE id = ? AND board_id = ?`, branch, p.Board).Scan(&main); err != nil {
		return err
	}
	if main || branch == p.Branch {
		return errors.New("main and the checked-out branch cannot be deleted")
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM branches WHERE id = ?`, branch); err != nil {
		return err
	}

	parent := map[int64]int64{}
	rows, err := tx.QueryContext(ctx, `SELECT id, COALESCE(parent_id, 0) FROM revisions WHERE board_id = ?`, p.Board)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id, par int64
		if err := rows.Scan(&id, &par); err != nil {
			rows.Close()
			return err
		}
		parent[id] = par
	}
	rows.Close()
	live := map[int64]bool{}
	reach := func(r int64) {
		for r != 0 && !live[r] {
			live[r] = true
			r = parent[r]
		}
	}
	reach(p.Rev)
	heads, err := tx.QueryContext(ctx, `SELECT COALESCE(head, 0) FROM branches WHERE board_id = ?`, p.Board)
	if err != nil {
		return err
	}
	for heads.Next() {
		var h int64
		if err := heads.Scan(&h); err != nil {
			heads.Close()
			return err
		}
		reach(h)
	}
	heads.Close()
	for id := range parent {
		if !live[id] {
			if _, err := tx.ExecContext(ctx, `DELETE FROM revisions WHERE id = ?`, id); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func setPos(ctx context.Context, e execer, p Pos) error {
	var branch any
	if p.Branch != 0 {
		branch = p.Branch
	}
	var rev any
	if p.Rev != 0 {
		rev = p.Rev
	}
	_, err := e.ExecContext(ctx, `UPDATE boards SET branch_id = ?, rev_id = ? WHERE id = ?`, branch, rev, p.Board)
	return err
}

func freeBranchName(ctx context.Context, tx *sql.Tx, boardID int64) (string, error) {
	for n := 2; ; n++ {
		name := fmt.Sprintf("b%d", n)
		var taken int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM branches WHERE board_id = ? AND name = ?`, boardID, name).Scan(&taken); err != nil {
			return "", err
		}
		if taken == 0 {
			return name, nil
		}
	}
}

// PruneRevisions keeps a board's history short: its latest keep revisions,
// its branch heads and the one on screen; the rest go, and a revision whose
// parent went becomes a root. (The demo board's, which draws all day.)
func (d *DB) PruneRevisions(ctx context.Context, boardID int64, keep int) error {
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM revisions WHERE board_id = ?
		AND id NOT IN (SELECT id FROM revisions WHERE board_id = ? ORDER BY id DESC LIMIT ?)
		AND id NOT IN (SELECT head FROM branches WHERE board_id = ? AND head IS NOT NULL)
		AND id NOT IN (SELECT rev_id FROM boards WHERE id = ? AND rev_id IS NOT NULL)`, boardID, boardID, keep, boardID, boardID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE revisions SET parent_id = NULL WHERE board_id = ? AND parent_id IS NOT NULL
		AND parent_id NOT IN (SELECT id FROM revisions WHERE board_id = ?)`, boardID, boardID); err != nil {
		return err
	}
	return tx.Commit()
}
