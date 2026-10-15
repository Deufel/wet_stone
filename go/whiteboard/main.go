// whiteboard — the Go sister of the Odin board: Go + templ + Datastar + an
// embedded NATS bus + SQLite (modernc).
//
// Land-and-stream, pure CQRS. A page opens one stream per board; every change
// publishes board.{name}.changed on the bus and each stream re-renders its
// viewer's <svg id="board"> as a morph. Commands answer an empty SSE stream.
// Boards live in memory for the drag path; the drawing (not cursors,
// selections, drags or laser strokes) is saved to SQLite after each command
// that changed it.
//
//	make run    then open http://127.0.0.1:8081 (boards at /b/{name})
package main

import (
	"crypto/rand"
	"encoding/hex"
	"context"
	"errors"
	"flag"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"whiteboard/auth"
	"whiteboard/bus"
	"whiteboard/db"
	"whiteboard/handler"
	"whiteboard/hub"
	"whiteboard/static"

	"github.com/go-chi/chi/v5"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8081", "listen address")
	dbPath := flag.String("db", "whiteboard.db", "SQLite file")
	devLogin := flag.Bool("dev-login", false, "allow signing in by email alone (local testing only)")
	compress := flag.String("compress", "br", "stream encoding: br, gzip or none")
	flag.Parse()

	d, err := db.Open(*dbPath)
	if err != nil {
		slog.Error("db", "err", err)
		os.Exit(1)
	}
	defer d.Close()

	b, err := bus.Open()
	if err != nil {
		slog.Error("bus", "err", err)
		os.Exit(1)
	}
	defer b.Close()

	h := hub.New(d, b)

	// Google sign-in comes from the environment (Coolify's environment variables).
	base := strings.TrimRight(os.Getenv("BASE_URL"), "/")
	if base == "" {
		base = "http://" + *addr
	}
	a := &auth.Auth{
		DB: d, BaseURL: base, Dev: *devLogin,
		ClientID: os.Getenv("GOOGLE_CLIENT_ID"), ClientSecret: os.Getenv("GOOGLE_CLIENT_SECRET"),
	}
	if !a.Google() {
		slog.Warn("Google sign-in is not configured: set GOOGLE_CLIENT_ID and GOOGLE_CLIENT_SECRET")
	}
	if a.Dev {
		slog.Warn("dev login is ON: anyone can sign in by email alone")
	}

	_ = mime.AddExtensionType(".webmanifest", "application/manifest+json")
	r := chi.NewRouter()
	r.Use(a.Middleware)
	r.Handle("/static/*", http.StripPrefix("/static/", http.FileServerFS(static.FS)))
	// this process's id: a page carries it, and a page from another process
	// (a deploy happened) reloads instead of talking past the server
	var inst [6]byte
	_, _ = rand.Read(inst[:])
	(&handler.Handler{Hub: h, Bus: b, DB: d, Auth: a, Compress: *compress, Instance: hex.EncodeToString(inst[:])}).Routes(r)

	// Streams are lifelong, so shutdown cancels every request context (which
	// ends each stream's loop) instead of waiting for them to finish.
	baseCtx, cancelRequests := context.WithCancel(context.Background())
	srv := &http.Server{
		Addr:        *addr,
		Handler:     r,
		BaseContext: func(_ net.Listener) context.Context { return baseCtx },
	}

	go func() {
		slog.Info("whiteboard", "url", "http://"+*addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("serve", "err", err)
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	slog.Info("shutting down")
	cancelRequests()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
	h.Close() // flushes pending saves
}
