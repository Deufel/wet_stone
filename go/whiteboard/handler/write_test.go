package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"whiteboard/board"
)

// A fake Messages API: the first reply fails the checker, the second passes;
// the repair round must carry the checker's complaint.
func TestClaudeWritesAndRepairs(t *testing.T) {
	good, _ := json.Marshal(map[string]any{"title": "Maps", "chapters": []any{
		map[string]any{"title": "Basics", "pages": []any{Courses["go"].Chapters[1].Pages[0]}}}})
	bad := `{"title": "Maps", "chapters": [{"title": "Basics", "pages": [{"title": "x", "steps": [{"do": "fly"}]}]}]}`
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("x-api-key") != "sk-ant-test" || r.Header.Get("anthropic-workspace-id") != "wrkspc_test" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var req struct{ Messages []message }
		_ = json.NewDecoder(r.Body).Decode(&req)
		reply := "```json\n" + bad + "\n```"
		if len(req.Messages) == 3 {
			if !strings.Contains(req.Messages[2].Content, "unknown step") {
				t.Errorf("repair without the complaint: %q", req.Messages[2].Content)
			}
			reply = string(good)
		}
		w.Header().Set("content-type", "text/event-stream")
		for i := 0; i < len(reply); i += 40 { // in pieces, as the API streams
			piece, _ := json.Marshal(reply[i:min(i+40, len(reply))])
			fmt.Fprintf(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":%s}}\n\n", piece)
		}
		fmt.Fprint(w, "data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n")
	}))
	defer srv.Close()
	anthropicURL = srv.URL

	got, err := writeCourse(context.Background(), apiKey{"sk-ant-test", "wrkspc_test"}, "maps", 1)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || calls != 2 || got.Chapters[0].Pages[0].Title != "copy: a slice of your own" {
		t.Fatalf("calls %d, course %+v", calls, got)
	}
	if _, err := claude(context.Background(), apiKey{"sk-ant-wrong", ""}, "", nil); err == nil || !strings.Contains(err.Error(), "key") {
		t.Fatalf("a refused key: %v", err)
	}
}

// "I don't get it": the explainer's steps come back checked, and an aside
// that tries to ask (or touch the checklist) is sent back for repair.
func TestExplainSteps(t *testing.T) {
	good := `{"steps": [{"do": "say", "text": "Think of it as a phone book."},
		{"do": "ring", "at": [300, 300], "r": 100}, {"do": "penarrow", "from": [420, 300], "to": [640, 300]}]}`
	bad := `{"steps": [{"do": "ask", "question": "ok?"}]}`
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var req struct {
			System   string
			Messages []message
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if !strings.Contains(req.System, "I don't get it") || !strings.Contains(req.Messages[0].Content, "What is cap(s)?") {
			t.Errorf("the explainer was not told the question")
		}
		reply := bad
		if len(req.Messages) == 3 {
			reply = good
		}
		piece, _ := json.Marshal(reply)
		fmt.Fprintf(w, "data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":%s}}\n\n", piece)
	}))
	defer srv.Close()
	anthropicURL = srv.URL
	steps, err := explainSteps(context.Background(), apiKey{"sk-ant-test", ""}, Lessons["go"], Step{Do: "ask", Question: "What is cap(s)?", Answer: "4"})
	if err != nil || calls != 2 || len(steps) != 3 || steps[0].Do != "say" {
		t.Fatalf("calls %d, steps %+v, err %v", calls, steps, err)
	}
}

// AI draw: Claude is told what the board holds, and a sketch that would wipe
// the board is sent back.
func TestSketchSteps(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var req struct{ Messages []message }
		_ = json.NewDecoder(r.Body).Decode(&req)
		if !strings.Contains(req.Messages[0].Content, `box [100, 100, 300, 180] "cache"`) {
			t.Errorf("the board was not described: %q", req.Messages[0].Content)
		}
		reply := `{"steps": [{"do": "erase"}]}`
		if len(req.Messages) == 3 {
			reply = `{"steps": [{"do": "code", "at": [400, 100, 900, 300], "lang": "odin", "text": "x := 1"}, {"do": "penarrow", "from": [300, 140], "to": [400, 140]}]}`
		}
		piece, _ := json.Marshal(reply)
		fmt.Fprintf(w, "data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":%s}}\n\n", piece)
	}))
	defer srv.Close()
	anthropicURL = srv.URL
	snap := board.Snapshot{Shapes: []board.SavedShape{{Kind: board.Box, X1: 100, Y1: 100, X2: 300, Y2: 180, Label: "cache"}}}
	steps, err := sketchSteps(context.Background(), apiKey{"sk-ant-test", ""}, snap, "show the cache's code")
	if err != nil || calls != 2 || len(steps) != 2 || steps[0].Do != "code" {
		t.Fatalf("calls %d, steps %+v, err %v", calls, steps, err)
	}
}
