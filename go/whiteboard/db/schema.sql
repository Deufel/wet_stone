-- A board's drawing as a history: every saved change is a revision (the whole
-- drawing as JSON: shapes, link nodes, group colors), each with its parent.
-- Branches are named heads. The board points at what is checked out: a branch
-- (branch_id) and the revision on screen (rev_id); branch_id NULL means
-- detached at an older revision, and the next change starts a new branch there.
-- Viewers, selections, drags and laser strokes never land here.

CREATE TABLE IF NOT EXISTS boards (
	id        INTEGER PRIMARY KEY,
	name      TEXT NOT NULL UNIQUE,
	branch_id INTEGER,
	rev_id    INTEGER
);

CREATE TABLE IF NOT EXISTS revisions (
	id        INTEGER PRIMARY KEY,
	board_id  INTEGER NOT NULL REFERENCES boards(id) ON DELETE CASCADE,
	parent_id INTEGER,
	at_ms     INTEGER NOT NULL, -- unix milliseconds
	op        TEXT NOT NULL,    -- what made it: "draw box", "move", "label", ...
	shapes    INTEGER NOT NULL,
	snapshot  TEXT NOT NULL     -- board.Snapshot as JSON
);
CREATE INDEX IF NOT EXISTS revisions_board ON revisions (board_id, id);

CREATE TABLE IF NOT EXISTS branches (
	id       INTEGER PRIMARY KEY,
	board_id INTEGER NOT NULL REFERENCES boards(id) ON DELETE CASCADE,
	name     TEXT NOT NULL,
	head     INTEGER, -- NULL until the branch's first revision
	is_main  INTEGER NOT NULL DEFAULT 0,
	UNIQUE (board_id, name)
);
