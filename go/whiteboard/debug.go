package main

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/pprof"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// The debug listener: only on the container's own loopback (never published),
// read from Coolify's terminal when something hangs:
//
//	wget -qO- localhost:6060/inflight                      requests being handled now
//	wget -qO- 'localhost:6060/debug/pprof/goroutine?debug=2'  where every goroutine waits
//
// A request that hangs in a browser but is not in /inflight never reached
// the server (the connection); one that is shows in the goroutine dump where
// it is stuck.

// flight is a request being handled, and what the server has sent on it.
type flight struct {
	method, path string
	session      bool // it carried a sign-in cookie
	start        time.Time

	bytes, writes, last atomic.Int64
}

// report is one flight as /inflight shows it. A stream that keeps writing
// while its page stands still lost the data on the way; one that stopped
// writing is stuck in the server.
type report struct {
	Method      string `json:"method"`
	Path        string `json:"path"`
	Session     bool   `json:"session"`
	AgeMs       int64  `json:"age_ms"`
	Bytes       int64  `json:"bytes"`
	Writes      int64  `json:"writes"`
	LastWriteMs int64  `json:"last_write_ms_ago"` // -1: nothing written yet
}

// counting is the response writer that keeps a flight's tally. It passes
// Flush and Unwrap through, so a stream behaves exactly as before.
type counting struct {
	http.ResponseWriter
	f *flight
}

func (c counting) Write(p []byte) (int, error) {
	n, err := c.ResponseWriter.Write(p)
	c.f.bytes.Add(int64(n))
	c.f.writes.Add(1)
	c.f.last.Store(time.Now().UnixMilli())
	return n, err
}

func (c counting) Flush() {
	if f, ok := c.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (c counting) Unwrap() http.ResponseWriter { return c.ResponseWriter }

var (
	flights  sync.Map // id → *flight
	flightID atomic.Int64
)

// track keeps every request in the in-flight table while it runs.
func track(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := flightID.Add(1)
		_, err := r.Cookie("wb_session")
		f := &flight{method: r.Method, path: r.URL.Path, session: err == nil, start: time.Now()}
		flights.Store(id, f)
		defer flights.Delete(id)
		next.ServeHTTP(counting{w, f}, r)
	})
}

func inflight(w http.ResponseWriter, _ *http.Request) {
	var out []report
	now := time.Now()
	flights.Range(func(_, v any) bool {
		f := v.(*flight)
		r := report{Method: f.method, Path: f.path, Session: f.session, AgeMs: now.Sub(f.start).Milliseconds(),
			Bytes: f.bytes.Load(), Writes: f.writes.Load(), LastWriteMs: -1}
		if last := f.last.Load(); last > 0 {
			r.LastWriteMs = now.UnixMilli() - last
		}
		out = append(out, r)
		return true
	})
	sort.Slice(out, func(i, j int) bool { return out[i].AgeMs > out[j].AgeMs })
	streams := 0
	for _, f := range out {
		if strings.HasSuffix(f.Path, "/stream") {
			streams++
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"total": len(out), "streams": streams, "requests": out})
}

// debugListen serves the debug endpoints on loopback only.
func debugListen() {
	mux := http.NewServeMux()
	mux.HandleFunc("/inflight", inflight)
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	go func() {
		if err := http.ListenAndServe("127.0.0.1:6060", mux); err != nil {
			slog.Warn("debug listener", "err", err)
		}
	}()
}
