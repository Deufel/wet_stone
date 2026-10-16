package board

import (
	"encoding/json"
	"strings"
	"testing"
)

// A teacher's ink: kept in a snapshot, and drawn as the engine reads it (a
// semantic class, an absolute hue, a strength).
func TestInk(t *testing.T) {
	b := New()
	b.Join(1, "T")
	b.Apply(Cmd{Op: "down", UID: 1, Tool: 1, Seq: 1, DSeq: 1, X: 100, Y: 100})
	b.Apply(Cmd{Op: "up", UID: 1, Tool: 1, Seq: 2, DSeq: 1, X: 300, Y: 200})
	if got := b.Apply(Cmd{Op: "style", UID: 1, Ink: Ink{Hue: 211, Fg: 8, Sem: SemOf("inf")}}); got != "style" {
		t.Fatalf("style: %q", got)
	}
	raw, _ := json.Marshal(b.Snapshot())
	var snap Snapshot
	_ = json.Unmarshal(raw, &snap)
	c := New()
	c.Restore(snap)
	sent := NewSent()
	frame, _ := c.Frame(1, &sent)
	for _, want := range []string{`class="inf"`, `--hue-lock:210`, `--fg:0.8`} {
		if !strings.Contains(frame, want) {
			t.Fatalf("%s not drawn: %s", want, frame[:min(len(frame), 400)])
		}
	}
}
