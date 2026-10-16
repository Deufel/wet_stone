package db

import (
	"context"
	"path/filepath"
	"testing"

	"whiteboard/board"
)

// A book is its whiteboard and its pages: pages go into the last chapter,
// number through the book, come back to be changed, and go.
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
	name, wb, err := d.CreateWhiteboardBook(ctx, &u, "Learning Go", Public)
	if err != nil {
		t.Fatal(err)
	}
	m, _ := d.Meta(ctx, wb)
	if m.Height != board.Letter || d.WhiteboardBook(ctx, m.ID) != name {
		t.Fatalf("whiteboard: height %d, book %q", m.Height, d.WhiteboardBook(ctx, m.ID))
	}
	b, _ := d.Book(ctx, name)
	snap := board.Snapshot{Shapes: []board.SavedShape{{Kind: board.Box, X2: 100, Y2: 100, Label: "x"}}}
	box := [4]int32{0, 0, 1000, board.Letter}
	if _, _, err := d.AddSheet(ctx, b.ID, snap, box); err != nil {
		t.Fatal(err)
	}
	if err := d.AddChapter(ctx, b); err != nil {
		t.Fatal(err)
	}
	no, id, err := d.AddSheet(ctx, b.ID, snap, box)
	if err != nil || no != 2 {
		t.Fatalf("page %d, %v", no, err)
	}
	sheets, _ := d.Sheets(ctx, b.ID)
	if len(sheets) != 2 || sheets[1].ChapterNo != 2 || sheets[1].Drawing.Shapes[0].Label != "x" {
		t.Fatalf("pages: %+v", sheets)
	}
	// back on the whiteboard, saved again
	if err := d.SetEditing(ctx, b.ID, id); err != nil {
		t.Fatal(err)
	}
	if got, n := d.Editing(ctx, b.ID); got != id || n != 2 {
		t.Fatalf("editing %d (page %d)", got, n)
	}
	snap.Shapes[0].Label = "y"
	if err := d.UpdateSheet(ctx, b.ID, id, snap, box); err != nil {
		t.Fatal(err)
	}
	sheets, _ = d.Sheets(ctx, b.ID)
	if got, _ := d.Editing(ctx, b.ID); got != 0 || sheets[1].Drawing.Shapes[0].Label != "y" {
		t.Fatalf("after save: editing %d, label %q", got, sheets[1].Drawing.Shapes[0].Label)
	}
	if err := d.RemoveSheet(ctx, b.ID, sheets[0].ID); err != nil {
		t.Fatal(err)
	}
	if sheets, _ = d.Sheets(ctx, b.ID); len(sheets) != 1 || sheets[0].No != 1 {
		t.Fatalf("after remove: %+v", sheets)
	}
	// copied (right after it), then reordered across chapters
	if err := d.CopySheet(ctx, b.ID, sheets[0].ID); err != nil {
		t.Fatal(err)
	}
	snap.Shapes[0].Label = "z"
	if _, _, err := d.AddSheet(ctx, b.ID, snap, box); err != nil {
		t.Fatal(err)
	}
	labels := func() string {
		sheets, _ = d.Sheets(ctx, b.ID)
		out := ""
		for _, s := range sheets {
			out += s.Drawing.Shapes[0].Label
		}
		return out
	}
	if got := labels(); got != "yyz" {
		t.Fatalf("after copy: %s", got)
	}
	if err := d.MoveSheet(ctx, b.ID, sheets[2].ID, sheets[0].ID); err != nil || labels() != "zyy" {
		t.Fatalf("move back: %s %v", labels(), err)
	}
	if err := d.MoveSheet(ctx, b.ID, sheets[0].ID, sheets[2].ID); err != nil || labels() != "yyz" {
		t.Fatalf("move on: %s %v", labels(), err)
	}
}

// A lesson book: a whiteboard, and pages that carry their lessons.
func TestCourse(t *testing.T) {
	ctx := context.Background()
	d, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	u, _ := d.UpsertUser(ctx, "dev:a", "a@x.dev", "A", "")
	name, wb, err := d.CreateCourse(ctx, &u, "Go", Private, []CourseChapter{
		{Title: "Slices", Pages: []CoursePage{{Title: "a", Script: `{"title":"a"}`}, {Title: "b", Script: `{"title":"b"}`}}},
		{Title: "Maps", Pages: []CoursePage{{Title: "c", Script: `{"title":"c"}`}}},
	})
	if err != nil || wb == "" {
		t.Fatal(err)
	}
	b, _ := d.Book(ctx, name)
	sheets, _ := d.Sheets(ctx, b.ID)
	if len(b.Chapters) != 2 || len(sheets) != 3 || sheets[2].ChapterNo != 2 || sheets[1].Lesson != `{"title":"b"}` {
		t.Fatalf("chapters %d, pages %+v", len(b.Chapters), sheets)
	}
}
