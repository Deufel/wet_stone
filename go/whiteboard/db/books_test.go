package db

import (
	"context"
	"path/filepath"
	"testing"
)

func TestBooks(t *testing.T) {
	ctx := context.Background()
	d, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	u, err := d.UpsertUser(ctx, "dev:a", "a@x.dev", "A", "")
	if err != nil {
		t.Fatal(err)
	}
	name, first, err := d.CreateBook(ctx, &u, "Learning Go", Public)
	if err != nil || first == "" {
		t.Fatal(err)
	}
	b, _ := d.Book(ctx, name)
	if _, err := d.AddPage(ctx, b, b.Chapters[0].ID); err != nil {
		t.Fatal(err)
	}
	b, _ = d.Book(ctx, name)
	if _, err := d.AddChapter(ctx, b); err != nil {
		t.Fatal(err)
	}
	b, _ = d.Book(ctx, name)
	if len(b.Chapters) != 2 || len(b.Chapters[0].Pages) != 2 || len(b.Chapters[1].Pages) != 1 {
		t.Fatalf("contents: %+v", b.Chapters)
	}
	if b.Chapters[1].Pages[0].No != 3 || b.Chapters[1].No != 2 {
		t.Fatalf("numbering: chapter %d page %d", b.Chapters[1].No, b.Chapters[1].Pages[0].No)
	}
	if got := d.BookOf(ctx, mustMeta(t, d, first)); got != name {
		t.Fatalf("BookOf = %q", got)
	}
	// a page out of the book: the board stays, back in its owner's boards
	if err := d.RemovePage(ctx, b.ID, b.Chapters[0].Pages[0].ID); err != nil {
		t.Fatal(err)
	}
	b, _ = d.Book(ctx, name)
	if len(b.Pages()) != 2 || b.Pages()[0].No != 1 {
		t.Fatalf("after remove: %+v", b.Pages())
	}
	if d.BookOf(ctx, mustMeta(t, d, first)) != "" {
		t.Fatal("removed page still in the book")
	}
	tabs, _ := d.Tabs(ctx, u.ID)
	found := false
	for _, tb := range tabs {
		found = found || tb.Name == first
	}
	if !found {
		t.Fatal("removed page's board not back in the owner's boards")
	}
}

func mustMeta(t *testing.T, d *DB, board string) int64 {
	m, err := d.Meta(context.Background(), board)
	if err != nil {
		t.Fatal(err)
	}
	return m.ID
}
