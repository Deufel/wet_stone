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

type flight struct {
	Method  string    `json:"method"`
	Path    string    `json:"path"`
	Session bool      `json:"session"` // it carried a sign-in cookie
	Start   time.Time `json:"-"`
	AgeMs   int64     `json:"age_ms"`
}

var (
	flights  sync.Map // id → *flight
	flightID atomic.Int64
)

// track keeps every request in the in-flight table while it runs.
func track(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := flightID.Add(1)
		_, err := r.Cookie("wb_session")
		flights.Store(id, &flight{Method: r.Method, Path: r.URL.Path, Session: err == nil, Start: time.Now()})
		defer flights.Delete(id)
		next.ServeHTTP(w, r)
	})
}

func inflight(w http.ResponseWriter, _ *http.Request) {
	var out []flight
	now := time.Now()
	flights.Range(func(_, v any) bool {
		f := *v.(*flight)
		f.AgeMs = now.Sub(f.Start).Milliseconds()
		out = append(out, f)
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
