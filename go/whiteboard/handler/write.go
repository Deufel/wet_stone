package handler

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"whiteboard/ack"
	"whiteboard/auth"
	"whiteboard/db"
	"whiteboard/view"

	"github.com/starfederation/datastar-go/datastar"
)

// Writing lesson books with the user's own Anthropic key: a topic goes in,
// Claude writes a course (the same JSON as courses/*.json), it is checked
// like a built-in one (one round of repair when it fails), and it lands as a
// book in the user's books. The key is kept on the server and only ever sent
// to Anthropic.
//
//	POST /me/key         keep the key (payload {text}; "" removes it)
//	POST /lessons/write  write a book (payload {text: the topic, n: pages})

// WriteModel writes the lessons.
const WriteModel = "claude-opus-5-5"

type writeJob struct {
	Topic, State, Title, Board, Err string
}

func (h *Handler) setKey(w http.ResponseWriter, r *http.Request) {
	u := auth.User(r.Context())
	if u == nil {
		http.Error(w, "sign in first", http.StatusUnauthorized)
		return
	}
	var p payload
	_ = datastar.ReadSignals(r, &p)
	key := strings.TrimSpace(p.Text)
	if key != "" && (!strings.HasPrefix(key, "sk-ant-") || len(key) > 300 || strings.ContainsAny(key, " \t\r\n")) {
		ack.Bad(datastar.NewSSE(w, r), "key", "That does not look like an Anthropic key")
		return
	}
	if err := h.DB.SetAnthropicKey(r.Context(), u.ID, key); err != nil {
		slog.Error("set key", "err", err)
		ack.Bad(datastar.NewSSE(w, r), "key", "Not saved")
		return
	}
	h.Hub.PublishUser(u.ID)
	msg := "Kept"
	if key == "" {
		msg = "Removed"
	}
	ack.Saved(datastar.NewSSE(w, r), "key", msg)
}

// writerState is what the writer's lines show this user.
func (h *Handler) writerState(ctx context.Context, u *db.User) view.WriterState {
	var s view.WriterState
	if key, _ := h.DB.AnthropicKey(ctx, u.ID); len(key) > 4 {
		s.KeyEnd = key[len(key)-4:]
	}
	if v, ok := h.writing.Load(u.ID); ok {
		j := v.(*writeJob)
		s.State, s.Topic, s.Title, s.Board, s.Err = j.State, j.Topic, j.Title, j.Board, j.Err
	}
	return s
}

func (h *Handler) write(w http.ResponseWriter, r *http.Request) {
	u := auth.User(r.Context())
	if u == nil {
		http.Error(w, "sign in first", http.StatusUnauthorized)
		return
	}
	var p payload
	_ = datastar.ReadSignals(r, &p)
	topic := strings.TrimSpace(p.Text)
	if len(topic) > 1500 {
		topic = topic[:1500]
	}
	if topic == "" {
		ack.Bad(datastar.NewSSE(w, r), "write", "What should it teach?")
		return
	}
	key, err := h.DB.AnthropicKey(r.Context(), u.ID)
	if err != nil || key == "" {
		ack.Bad(datastar.NewSSE(w, r), "write", "Keep your Anthropic key first")
		return
	}
	pages := min(max(p.N, 1), maxPages)
	job := &writeJob{Topic: short(topic, 60), State: "writing"}
	if v, ok := h.writing.Load(u.ID); ok && v.(*writeJob).State == "writing" {
		ack.Bad(datastar.NewSSE(w, r), "write", "One is being written already")
		return
	}
	h.writing.Store(u.ID, job)
	h.Hub.PublishUser(u.ID)
	go h.writeBook(*u, key, topic, pages, job)
	ack.Saved(datastar.NewSSE(w, r), "write", "Writing: it lands in Learn, in the menu")
}

func short(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// writeBook asks Claude for the course, checks it (one repair round), and
// stores it as the user's book.
func (h *Handler) writeBook(u db.User, key, topic string, pages int, job *writeJob) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	done := func(j writeJob) {
		h.writing.Store(u.ID, &j)
		h.Hub.PublishUser(u.ID)
	}
	c, err := writeCourse(ctx, key, topic, pages)
	if err != nil {
		slog.Error("write lesson", "user", u.ID, "err", err)
		done(writeJob{Topic: job.Topic, State: "failed", Err: err.Error()})
		return
	}
	_, first, err := h.makeCourse(ctx, &u, c, db.Private)
	if err != nil {
		slog.Error("write lesson: store", "err", err)
		done(writeJob{Topic: job.Topic, State: "failed", Err: "could not store it"})
		return
	}
	done(writeJob{Topic: job.Topic, State: "ready", Title: c.Title, Board: first})
}

// writeCourse asks Claude for the course and checks it, with one round of
// repair (the checker's complaint sent back).
func writeCourse(ctx context.Context, key, topic string, pages int) (*Course, error) {
	msgs := []message{{Role: "user", Content: writePrompt(topic, pages)}}
	for try := 0; try < 2; try++ {
		text, err := claude(ctx, key, writeSystem(), msgs)
		if err != nil {
			return nil, err
		}
		var c Course
		err = json.Unmarshal([]byte(jsonOf(text)), &c)
		if err == nil {
			err = c.resolve()
		}
		if err == nil {
			return &c, nil
		}
		slog.Info("write lesson: repair", "err", err)
		msgs = append(msgs, message{Role: "assistant", Content: text},
			message{Role: "user", Content: "That does not pass the checker: " + err.Error() + "\nSend the whole corrected JSON, and nothing else."})
	}
	return nil, errors.New("the lesson did not come out right; try again")
}

// jsonOf takes the JSON object out of a reply (fences and words around it aside).
func jsonOf(s string) string {
	i, j := strings.Index(s, "{"), strings.LastIndex(s, "}")
	if i < 0 || j < i {
		return s
	}
	return s[i : j+1]
}

// ─── The Messages API ───────────────────────────────────────────────────────

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// anthropicURL is the Messages API (a test points it elsewhere).
var anthropicURL = "https://api.anthropic.com/v1/messages"

var claudeClient = &http.Client{Timeout: 14 * time.Minute}

// claude sends one conversation to the Messages API, streamed (a long book
// takes minutes), and returns the reply's text.
func claude(ctx context.Context, key, system string, msgs []message) (string, error) {
	body, _ := json.Marshal(map[string]any{
		"model": WriteModel, "max_tokens": 64000, "stream": true,
		"system": system, "messages": msgs,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, anthropicURL, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("x-api-key", key)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("content-type", "application/json")
	res, err := claudeClient.Do(req)
	if err != nil {
		return "", errors.New("could not reach Anthropic")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		var e struct {
			Error struct{ Message string } `json:"error"`
		}
		_ = json.NewDecoder(res.Body).Decode(&e)
		switch res.StatusCode {
		case http.StatusUnauthorized:
			return "", errors.New("Anthropic did not accept the key")
		case http.StatusTooManyRequests, 529:
			return "", errors.New("Anthropic is busy; try again in a minute")
		}
		return "", fmt.Errorf("Anthropic: %s", short(e.Error.Message, 120))
	}
	var out strings.Builder
	sc := bufio.NewScanner(res.Body)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line, ok := strings.CutPrefix(sc.Text(), "data: ")
		if !ok {
			continue
		}
		var ev struct {
			Type  string `json:"type"`
			Delta struct {
				Type, Text string
				StopReason string `json:"stop_reason"`
			} `json:"delta"`
			Error struct{ Message string } `json:"error"`
		}
		if json.Unmarshal([]byte(line), &ev) != nil {
			continue
		}
		switch ev.Type {
		case "content_block_delta":
			if ev.Delta.Type == "text_delta" {
				out.WriteString(ev.Delta.Text)
			}
		case "message_delta":
			if ev.Delta.StopReason == "max_tokens" {
				return "", errors.New("the book ran too long; ask for fewer pages")
			}
		case "error":
			return "", fmt.Errorf("Anthropic: %s", short(ev.Error.Message, 120))
		}
	}
	if err := sc.Err(); err != nil {
		return "", errors.New("the reply was cut off")
	}
	return out.String(), nil
}
