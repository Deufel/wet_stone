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
