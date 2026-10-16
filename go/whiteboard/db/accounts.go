package db

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

// ErrNoBoard: no board by that name (and only the global board makes itself).
var ErrNoBoard = errors.New("no such board")

const (
	Global  = "global"  // every signed-in user
	Public  = "public"  // every signed-in user with the link
	Private = "private" // the owner and the emails invited
)

type User struct {
	ID      int64
	Sub     string
	Email   string
	Name    string
	Picture string
}

// First is the user's first name, else the part of the email before @.
func (u *User) First() string {
	if f, _, _ := strings.Cut(strings.TrimSpace(u.Name), " "); f != "" {
		return f
	}
	at, _, _ := strings.Cut(u.Email, "@")
	return at
}

// Meta is who and what a board is, apart from its drawing.
type Meta struct {
	ID         int64
	Name       string // the URL slug
	Title      string
	Visibility string
	OwnerID    int64 // 0 = nobody (the global board, or one from before accounts)
	Height     int32 // in board units: 1000 square, 1294 letter
	Access     []string
}

// CanOpen: may this signed-in user open the board?
func (m *Meta) CanOpen(u *User) bool {
	if u == nil {
		return false
	}
	if m.Visibility != Private || m.OwnerID == u.ID {
		return true
	}
	for _, e := range m.Access {
		if e == u.Email {
			return true
		}
	}
	return false
}

// Tab is one board in a user's tab strip.
type Tab struct {
	Name, Title, Visibility string
	Owned                   bool
}

// UpsertUser records a signed-in identity and returns the user, and gives them
// a tab for every private board their email was invited to.
func (d *DB) UpsertUser(ctx context.Context, sub, email, name, picture string) (User, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	u := User{Sub: sub, Email: email, Name: name, Picture: picture}
	err := d.sql.QueryRowContext(ctx, `INSERT INTO users (sub, email, name, picture, created_ms) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (sub) DO UPDATE SET email = excluded.email, name = excluded.name, picture = excluded.picture
		RETURNING id`, sub, email, name, picture, time.Now().UnixMilli()).Scan(&u.ID)
	if err != nil {
		return u, err
	}
	_, err = d.sql.ExecContext(ctx, `INSERT OR IGNORE INTO tabs (user_id, board_id, opened_ms)
		SELECT ?, board_id, ? FROM board_access WHERE email = ?`, u.ID, time.Now().UnixMilli(), email)
	return u, err
}

const sessionTTL = 60 * 24 * time.Hour

// NewSession returns a fresh session token for the user. Only its hash is stored.
func (d *DB) NewSession(ctx context.Context, userID int64) (string, time.Time, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", time.Time{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	expires := time.Now().Add(sessionTTL)
	_, err := d.sql.ExecContext(ctx, `INSERT INTO sessions (token_hash, user_id, expires_ms) VALUES (?, ?, ?)`,
		hashToken(token), userID, expires.UnixMilli())
	return token, expires, err
}

// SessionUser returns the user a live session token belongs to, or nil.
func (d *DB) SessionUser(ctx context.Context, token string) (*User, error) {
	ctx, cancel := reading(ctx)
	defer cancel()
	if token == "" {
		return nil, nil
	}
	var u User
	err := d.rd.QueryRowContext(ctx, `SELECT u.id, u.sub, u.email, u.name, u.picture FROM sessions s
		JOIN users u ON u.id = s.user_id WHERE s.token_hash = ? AND s.expires_ms > ?`,
		hashToken(token), time.Now().UnixMilli()).Scan(&u.ID, &u.Sub, &u.Email, &u.Name, &u.Picture)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

func (d *DB) EndSession(ctx context.Context, token string) error {
	_, err := d.sql.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ? OR expires_ms < ?`,
		hashToken(token), time.Now().UnixMilli())
	return err
}

func hashToken(t string) string {
	h := sha256.Sum256([]byte(t))
	return hex.EncodeToString(h[:])
}

// Meta loads a board's identity and access list.
func (d *DB) Meta(ctx context.Context, name string) (Meta, error) {
	ctx, cancel := reading(ctx)
	defer cancel()
	m := Meta{Name: name}
	var owner sql.NullInt64
	err := d.rd.QueryRowContext(ctx, `SELECT id, title, visibility, owner_id, height FROM boards WHERE name = ?`, name).
		Scan(&m.ID, &m.Title, &m.Visibility, &owner, &m.Height)
	if errors.Is(err, sql.ErrNoRows) {
		return m, ErrNoBoard
	}
	if err != nil {
		return m, err
	}
	m.OwnerID = owner.Int64
	rows, err := d.rd.QueryContext(ctx, `SELECT email FROM board_access WHERE board_id = ? ORDER BY email`, m.ID)
	if err != nil {
		return m, err
	}
	defer rows.Close()
	for rows.Next() {
		var e string
		if err := rows.Scan(&e); err != nil {
			return m, err
		}
		m.Access = append(m.Access, e)
	}
	return m, rows.Err()
}

// CreateBoard makes a new board owned by the user under an unguessable name.
func (d *DB) CreateBoard(ctx context.Context, owner *User, title, visibility string) (string, error) {
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	id, name, err := newBoard(ctx, tx, owner.ID, title, visibility)
	if err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO tabs (user_id, board_id, opened_ms) VALUES (?, ?, ?)`, owner.ID, id, time.Now().UnixMilli()); err != nil {
		return "", err
	}
	return name, tx.Commit()
}

// slug is a fresh random URL name.
func slug() (string, error) {
	raw := make([]byte, 10)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return strings.ToLower(strings.TrimRight(base32.StdEncoding.EncodeToString(raw), "=")), nil
}

// newBoard inserts a board and its main branch inside tx.
func newBoard(ctx context.Context, tx *sql.Tx, ownerID int64, title, visibility string) (int64, string, error) {
	name, err := slug()
	if err != nil {
		return 0, "", err
	}
	var id, branch int64
	if err := tx.QueryRowContext(ctx, `INSERT INTO boards (name, title, owner_id, visibility, created_ms) VALUES (?, ?, ?, ?, ?) RETURNING id`,
		name, title, ownerID, visibility, time.Now().UnixMilli()).Scan(&id); err != nil {
		return 0, "", err
	}
	if err := tx.QueryRowContext(ctx, `INSERT INTO branches (board_id, name, is_main) VALUES (?, 'main', 1) RETURNING id`, id).Scan(&branch); err != nil {
		return 0, "", err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE boards SET branch_id = ? WHERE id = ?`, branch, id); err != nil {
		return 0, "", err
	}
	return id, name, nil
}

func (d *DB) SetTitle(ctx context.Context, boardID int64, title string) error {
	_, err := d.sql.ExecContext(ctx, `UPDATE boards SET title = ? WHERE id = ?`, title, boardID)
	return err
}

// Invite gives an email access, and a tab if that person already has an account.
func (d *DB) Invite(ctx context.Context, boardID int64, email string) error {
	email = strings.ToLower(strings.TrimSpace(email))
	if _, err := d.sql.ExecContext(ctx, `INSERT OR IGNORE INTO board_access (board_id, email) VALUES (?, ?)`, boardID, email); err != nil {
		return err
	}
	_, err := d.sql.ExecContext(ctx, `INSERT OR IGNORE INTO tabs (user_id, board_id, opened_ms)
		SELECT id, ?, ? FROM users WHERE email = ?`, boardID, time.Now().UnixMilli(), email)
	return err
}

// Uninvite removes an email's access and its tab.
func (d *DB) Uninvite(ctx context.Context, boardID int64, email string) error {
	if _, err := d.sql.ExecContext(ctx, `DELETE FROM board_access WHERE board_id = ? AND email = ?`, boardID, email); err != nil {
		return err
	}
	_, err := d.sql.ExecContext(ctx, `DELETE FROM tabs WHERE board_id = ? AND user_id IN (SELECT id FROM users WHERE email = ?)`, boardID, email)
	return err
}

// OpenTab keeps a board in the user's tab strip (and moves it to the end).
// OpenTab adds the board to the user's tabs; it reports whether it was new.
func (d *DB) OpenTab(ctx context.Context, userID, boardID int64) (bool, error) {
	res, err := d.sql.ExecContext(ctx, `INSERT INTO tabs (user_id, board_id, opened_ms) VALUES (?, ?, ?)
		ON CONFLICT (user_id, board_id) DO NOTHING`, userID, boardID, time.Now().UnixMilli())
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// UserID is the account with this email, or 0 when nobody has signed in with it.
func (d *DB) UserID(ctx context.Context, email string) int64 {
	ctx, cancel := reading(ctx)
	defer cancel()
	var id int64
	_ = d.rd.QueryRowContext(ctx, `SELECT id FROM users WHERE email = ?`, strings.ToLower(strings.TrimSpace(email))).Scan(&id)
	return id
}

// TabHolders are the users who have the board in their tabs: the people a
// rename has to reach.
func (d *DB) TabHolders(ctx context.Context, boardID int64) ([]int64, error) {
	ctx, cancel := reading(ctx)
	defer cancel()
	rows, err := d.rd.QueryContext(ctx, `SELECT user_id FROM tabs WHERE board_id = ?`, boardID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (d *DB) CloseTab(ctx context.Context, userID int64, name string) error {
	_, err := d.sql.ExecContext(ctx, `DELETE FROM tabs WHERE user_id = ? AND board_id = (SELECT id FROM boards WHERE name = ?)`, userID, name)
	return err
}

// Tabs lists the user's boards, the global board first, then in the order opened.
func (d *DB) Tabs(ctx context.Context, userID int64) ([]Tab, error) {
	ctx, cancel := reading(ctx)
	defer cancel()
	rows, err := d.rd.QueryContext(ctx, `SELECT b.name, b.title, b.visibility, COALESCE(b.owner_id, 0) = ?
		FROM tabs t JOIN boards b ON b.id = t.board_id WHERE t.user_id = ?
		ORDER BY b.visibility = 'global' DESC, t.opened_ms`, userID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tabs []Tab
	for rows.Next() {
		var t Tab
		if err := rows.Scan(&t.Name, &t.Title, &t.Visibility, &t.Owned); err != nil {
			return nil, err
		}
		tabs = append(tabs, t)
	}
	return tabs, rows.Err()
}
