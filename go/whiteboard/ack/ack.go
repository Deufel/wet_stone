// Package ack is the one way a command says how it went. The page has no
// forms: an input's value rides up as a signal, the change itself comes back
// down the stream like every other, and the command's own answer is only this
// small acknowledgement: one signal, $_saved, that the input's .saved line
// (view.Saved) shows for a moment.
//
//	{_saved: {k: "title", m: "Saved", bad: false, n: 1760000000000}}
//
// k names the input, so only its own line lights; n makes a repeat show again.
// The leading underscore keeps it out of every request.
package ack

import (
	"encoding/json"
	"time"

	"github.com/starfederation/datastar-go/datastar"
)

type saved struct {
	K   string `json:"k"`
	M   string `json:"m"`
	Bad bool   `json:"bad"`
	N   int64  `json:"n"`
}

func send(sse *datastar.ServerSentEventGenerator, key, msg string, bad bool) {
	b, _ := json.Marshal(map[string]saved{"_saved": {key, msg, bad, time.Now().UnixMilli()}})
	_ = sse.PatchSignals(b)
}

// Saved: the input's value is stored.
func Saved(sse *datastar.ServerSentEventGenerator, key, msg string) { send(sse, key, msg, false) }

// Bad: it was not, and why, in a few words.
func Bad(sse *datastar.ServerSentEventGenerator, key, msg string) { send(sse, key, msg, true) }
