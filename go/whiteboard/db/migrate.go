package db

import (
	"database/sql"
	"fmt"
)

// Migrations run in order, each once, tracked by SQLite's user_version.
// schema.sql (version 1) is the drawing history; every later change is a new
// entry here, never an edit to an earlier one, so a deployed database moves
// forward without losing what it holds.
var migrations = []string{
	// 2: accounts, sessions, and who may open which board
	`
	CREATE TABLE IF NOT EXISTS users (
		id         INTEGER PRIMARY KEY,
		sub        TEXT NOT NULL UNIQUE, -- the identity provider's subject ("google:…")
		email      TEXT NOT NULL,        -- lowercased
		name       TEXT NOT NULL DEFAULT '',
		picture    TEXT NOT NULL DEFAULT '',
		created_ms INTEGER NOT NULL
	);
	CREATE INDEX IF NOT EXISTS users_email ON users (email);

	CREATE TABLE IF NOT EXISTS sessions (
		token_hash TEXT PRIMARY KEY, -- sha256 of the cookie value; the cookie is never stored
		user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		expires_ms INTEGER NOT NULL
	);

	ALTER TABLE boards ADD COLUMN title      TEXT NOT NULL DEFAULT '';
	ALTER TABLE boards ADD COLUMN owner_id   INTEGER REFERENCES users(id);
	ALTER TABLE boards ADD COLUMN visibility TEXT NOT NULL DEFAULT 'public'; -- global, public, private
	ALTER TABLE boards ADD COLUMN created_ms INTEGER NOT NULL DEFAULT 0;
	UPDATE boards SET visibility = 'global', title = 'Global board' WHERE name = 'main';
	UPDATE boards SET title = name WHERE title = '';

	-- a private board's guests, by email: an invite works before they ever sign in
	CREATE TABLE IF NOT EXISTS board_access (
		board_id INTEGER NOT NULL REFERENCES boards(id) ON DELETE CASCADE,
		email    TEXT NOT NULL,
		PRIMARY KEY (board_id, email)
	);

	-- the boards a user keeps as tabs
	CREATE TABLE IF NOT EXISTS tabs (
		user_id   INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		board_id  INTEGER NOT NULL REFERENCES boards(id) ON DELETE CASCADE,
		opened_ms INTEGER NOT NULL,
		PRIMARY KEY (user_id, board_id)
	);
	`,

	// 3: a board's checklist (shared, no history: each item keeps when it was
	// done and by whom) and each user's private notes on a board. SQLite keeps
	// the time: unixepoch() on write, strftime() on read.
	`
	CREATE TABLE IF NOT EXISTS todos (
		id         INTEGER PRIMARY KEY,
		board_id   INTEGER NOT NULL REFERENCES boards(id) ON DELETE CASCADE,
		text       TEXT NOT NULL,
		created_at INTEGER NOT NULL DEFAULT (unixepoch()),
		created_by INTEGER REFERENCES users(id),
		done_at    INTEGER,             -- NULL while open
		done_by    INTEGER REFERENCES users(id)
	);
	CREATE INDEX IF NOT EXISTS todos_board ON todos (board_id, id);

	CREATE TABLE IF NOT EXISTS notes (
		user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		board_id   INTEGER NOT NULL REFERENCES boards(id) ON DELETE CASCADE,
		text       TEXT NOT NULL DEFAULT '',
		lang       TEXT NOT NULL DEFAULT '', -- the highlighter's language, '' = plain
		updated_at INTEGER NOT NULL DEFAULT (unixepoch('subsec') * 1000),
		PRIMARY KEY (user_id, board_id)
	);
	`,

	// 4: books: a title and an owner, chapters in order, and pages in order,
	// each page a board (one book per board)
	`
	CREATE TABLE IF NOT EXISTS books (
		id         INTEGER PRIMARY KEY,
		name       TEXT NOT NULL UNIQUE, -- the URL slug
		title      TEXT NOT NULL,
		owner_id   INTEGER NOT NULL REFERENCES users(id),
		visibility TEXT NOT NULL DEFAULT 'public', -- public, private (owner only)
		created_at INTEGER NOT NULL DEFAULT (unixepoch())
	);
	CREATE TABLE IF NOT EXISTS book_chapters (
		id       INTEGER PRIMARY KEY,
		book_id  INTEGER NOT NULL REFERENCES books(id) ON DELETE CASCADE,
		position INTEGER NOT NULL,
		title    TEXT NOT NULL
	);
	CREATE INDEX IF NOT EXISTS book_chapters_book ON book_chapters (book_id, position);
	CREATE TABLE IF NOT EXISTS book_pages (
		id         INTEGER PRIMARY KEY,
		chapter_id INTEGER NOT NULL REFERENCES book_chapters(id) ON DELETE CASCADE,
		board_id   INTEGER NOT NULL UNIQUE REFERENCES boards(id) ON DELETE CASCADE,
		position   INTEGER NOT NULL
	);
	CREATE INDEX IF NOT EXISTS book_pages_chapter ON book_pages (chapter_id, position);
	`,

	// 5: lessons on boards (a page of a lesson book: the script the presenter
	// plays when the page is first opened), and a user's own Anthropic key
	// (it writes their lessons; it never leaves the server)
	`
	CREATE TABLE IF NOT EXISTS board_lessons (
		board_id  INTEGER PRIMARY KEY REFERENCES boards(id) ON DELETE CASCADE,
		script    TEXT NOT NULL,
		played_at INTEGER -- NULL until first played
	);
	CREATE TABLE IF NOT EXISTS user_keys (
		user_id    INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
		anthropic  TEXT NOT NULL,
		updated_at INTEGER NOT NULL DEFAULT (unixepoch())
	);
	`,

	// 6: the workspace a key's requests name (an organization-wide key needs one)
	`
	ALTER TABLE user_keys ADD COLUMN workspace TEXT NOT NULL DEFAULT '';
	`,

	// 7: a board's height in units: 1000 square (every board before), 1294 a
	// letter page (a book's whiteboard)
	`
	ALTER TABLE boards ADD COLUMN height INTEGER NOT NULL DEFAULT 1000;
	`,

	// 8: a book's whiteboard (where its teacher draws) and its sheets: pages
	// sent from the whiteboard, each a frozen drawing (the shapes sent and
	// their extent), in a chapter, in order
	`
	ALTER TABLE books ADD COLUMN board_id INTEGER REFERENCES boards(id);
	CREATE UNIQUE INDEX IF NOT EXISTS books_board ON books (board_id) WHERE board_id IS NOT NULL;
	CREATE TABLE IF NOT EXISTS book_sheets (
		id         INTEGER PRIMARY KEY,
		book_id    INTEGER NOT NULL REFERENCES books(id) ON DELETE CASCADE,
		chapter_id INTEGER NOT NULL REFERENCES book_chapters(id) ON DELETE CASCADE,
		position   INTEGER NOT NULL,
		snapshot   TEXT NOT NULL,    -- board.Snapshot as JSON: only the shapes sent
		x1 INTEGER NOT NULL, y1 INTEGER NOT NULL, x2 INTEGER NOT NULL, y2 INTEGER NOT NULL,
		created_at INTEGER NOT NULL DEFAULT (unixepoch())
	);
	CREATE INDEX IF NOT EXISTS book_sheets_book ON book_sheets (book_id, position);
	`,

	// 9: a student's own pencil marks on a book's whiteboard (comments,
	// questions, notes: only they see them); a page sent takes them with it
	`
	CREATE TABLE IF NOT EXISTS board_marks (
		id         INTEGER PRIMARY KEY,
		board_id   INTEGER NOT NULL REFERENCES boards(id) ON DELETE CASCADE,
		user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		sheet_id   INTEGER REFERENCES book_sheets(id) ON DELETE CASCADE, -- NULL: still on the whiteboard
		points     TEXT NOT NULL, -- [x, y, x, y, …] in board units
		created_at INTEGER NOT NULL DEFAULT (unixepoch())
	);
	CREATE INDEX IF NOT EXISTS board_marks_board ON board_marks (board_id, user_id, sheet_id);
	-- the page back on the whiteboard to be changed: its save replaces it
	ALTER TABLE books ADD COLUMN editing_sheet INTEGER REFERENCES book_sheets(id) ON DELETE SET NULL;
	`,

	// 10: the books of boards are retired: a book is its whiteboard and its
	// pages. The old books go, and the boards that were their pages; a page
	// may carry a lesson, presented on the whiteboard
	`
	DELETE FROM boards WHERE id IN (SELECT p.board_id FROM book_pages p
		JOIN book_chapters c ON c.id = p.chapter_id JOIN books b ON b.id = c.book_id WHERE b.board_id IS NULL);
	DELETE FROM books WHERE board_id IS NULL;
	DROP TABLE IF EXISTS book_pages;
	ALTER TABLE book_sheets ADD COLUMN lesson TEXT NOT NULL DEFAULT '';
	`,

	// 11: time on a checklist item: what has been spent, the clock running
	// (since, in ms; NULL stopped), and a budget (seconds; 0 none)
	`
	ALTER TABLE todos ADD COLUMN spent_ms INTEGER NOT NULL DEFAULT 0;
	ALTER TABLE todos ADD COLUMN started_ms INTEGER;
	ALTER TABLE todos ADD COLUMN budget_s INTEGER NOT NULL DEFAULT 0;
	`,

	// 12: AI keys: several a user, each for a service, one active a service;
	// the single Anthropic key before becomes the first, active
	`
	CREATE TABLE IF NOT EXISTS ai_keys (
		id         INTEGER PRIMARY KEY,
		user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		service    TEXT NOT NULL,             -- "anthropic"
		label      TEXT NOT NULL DEFAULT '',
		secret     TEXT NOT NULL,
		workspace  TEXT NOT NULL DEFAULT '',  -- the workspace its requests name
		active     INTEGER NOT NULL DEFAULT 0,
		created_at INTEGER NOT NULL DEFAULT (unixepoch())
	);
	CREATE INDEX IF NOT EXISTS ai_keys_user ON ai_keys (user_id, service);
	INSERT INTO ai_keys (user_id, service, label, secret, workspace, active)
		SELECT user_id, 'anthropic', '', anthropic, workspace, 1 FROM user_keys;
	DROP TABLE user_keys;
	`,

	// 13: a board can be locked: only the laser marks it (a lesson's board
	// is locked while a lesson page is on it, whatever this says)
	`
	ALTER TABLE boards ADD COLUMN locked INTEGER NOT NULL DEFAULT 0;
	`,
}

func migrate(db *sql.DB) error {
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	if version == 0 {
		version = 1 // schema.sql is version 1
	}
	for v := version + 1; v <= len(migrations)+1; v++ {
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migrations[v-2]); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: %w", v, err)
		}
		if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, v)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}
