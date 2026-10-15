// Package bus is an embedded, in-process NATS server and client: no TCP port,
// the client talks to the server over an in-memory pipe. Subjects follow
// entity.action and are scoped to one board or one person:
//
//	board.{name}.changed   anything on that board changed: the drawing, a
//	                       cursor, a selection, a laser stroke. Every stream on
//	                       that board re-renders its viewer's frame.
//	user.{id}.changed      that person's boards changed: one made, opened,
//	                       closed, renamed, shared with them or taken away.
//	                       Every stream they have open, on every device,
//	                       re-renders their tab strip or board list.
//
// Nothing is global: a board's streams never wake for another board.
package bus

import (
	"strconv"
	"fmt"
	"log/slog"
	"runtime/debug"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
)

type Bus struct {
	ns *server.Server
	nc *nats.Conn
}

// Open starts the embedded server and connects a client to it.
func Open() (*Bus, error) {
	ns, err := server.NewServer(&server.Options{DontListen: true})
	if err != nil {
		return nil, fmt.Errorf("new nats server: %w", err)
	}
	ns.Start()
	if !ns.ReadyForConnections(5 * time.Second) {
		return nil, fmt.Errorf("nats server not ready")
	}
	nc, err := nats.Connect("", nats.InProcessServer(ns))
	if err != nil {
		return nil, fmt.Errorf("connect in-process nats: %w", err)
	}
	return &Bus{ns: ns, nc: nc}, nil
}

func (b *Bus) Close() {
	if b.nc != nil {
		b.nc.Close()
	}
	if b.ns != nil {
		b.ns.Shutdown()
	}
}

// Publish sends a message on a subject. The subject is the signal; the payload
// is usually nil.
func (b *Bus) Publish(subject string, data []byte) error {
	return b.nc.Publish(subject, data)
}

// Subscribe registers a handler and returns the subscription so the caller can
// Unsubscribe on disconnect. NATS does not recover panics in handlers, and an
// unrecovered one takes the whole process down, so recover and log here.
func (b *Bus) Subscribe(subject string, handler func(subject string, data []byte)) (*nats.Subscription, error) {
	return b.nc.Subscribe(subject, func(m *nats.Msg) {
		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("bus handler panic", "subject", m.Subject, "panic", rec, "stack", string(debug.Stack()))
			}
		}()
		handler(m.Subject, m.Data)
	})
}

// BoardSubject is the subject for any change on one board. Board names are
// validated to [a-z0-9-], so they never contain a subject separator.
func BoardSubject(name string) string { return "board." + name + ".changed" }

// UserSubject is the subject for a change to one person's list of boards.
func UserSubject(id int64) string { return "user." + strconv.FormatInt(id, 10) + ".changed" }
