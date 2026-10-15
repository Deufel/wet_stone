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
