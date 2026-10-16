package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
