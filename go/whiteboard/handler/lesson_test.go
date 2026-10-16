package handler

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestLessonsLoad(t *testing.T) {
	for _, k := range []string{"review", "tour", "go"} {
		if Lessons[k] == nil {
			t.Fatalf("lesson %q missing", k)
		}
	}
	if Lessons["go"].Notes.Lang != "go" {
		t.Fatal("the Go lesson's notes are not Go")
	}
}

func TestLessonValidate(t *testing.T) {
	bad := map[string]string{
		`{"title":"x","steps":[{"do":"fly"}]}`:                   "unknown step",
		`{"title":"x","steps":[{"do":"box","at":[1,2,3]}]}`:      "at:",
		`{"title":"x","steps":[{"do":"box","at":[1,2,3,4000]}]}`: "at:",
		`{"title":"x","steps":[{"do":"notes","n":3}]}`:           "n:",
		`{"title":"","steps":[{"do":"pause","ms":5}]}`:           "title",
		`{"title":"x","steps":[{"do":"pen","points":[[1,1]]}]}`:  "points",
	}
	for src, want := range bad {
		var l Lesson
		if err := json.Unmarshal([]byte(src), &l); err != nil {
			t.Fatal(err)
		}
		err := l.Validate()
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v, want %q", src, err, want)
		}
	}
}

func TestLessonAskValidate(t *testing.T) {
	var l Lesson
	src := `{"title":"x","steps":[{"do":"ask","question":"q?","options":["a","b"],"if":{"*":[{"do":"fly"}]}}]}`
	if err := json.Unmarshal([]byte(src), &l); err != nil {
		t.Fatal(err)
	}
	if err := l.Validate(); err == nil || !strings.Contains(err.Error(), "[*]") {
		t.Fatalf("a bad step in a branch: %v", err)
	}
	if !same(" Copy ", "copy") || same("cop", "copy") {
		t.Fatal("same")
	}
}

// The pen's points are filled in: no gap wider than a few units, and every
// point the script gave is on the stroke.
func TestDensify(t *testing.T) {
	in := [][2]int32{{100, 100}, {300, 100}, {300, 400}}
	out := densify(in, penStep)
	for i := 1; i < len(out); i++ {
		dx, dy := out[i][0]-out[i-1][0], out[i][1]-out[i-1][1]
		if dx*dx+dy*dy > 12*12 {
			t.Fatalf("gap at %d: %v → %v", i, out[i-1], out[i])
		}
	}
	has := func(p [2]int32) bool {
		for _, q := range out {
			if q == p {
				return true
			}
		}
		return false
	}
	for _, p := range in {
		if !has(p) {
			t.Fatalf("%v is not on the stroke", p)
		}
	}
}
