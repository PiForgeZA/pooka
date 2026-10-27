// Package daemon implements the Sprites HTTP and WebSocket API on top of a Backend.
// See docs/sprites-api/README.md for the endpoint map and ADR 0004 for the decision.
package daemon

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/PiForgeZA/pooka/internal/backend"
)

// Server serves the API for one backend.
type Server struct {
	Backend backend.Backend
	Token   string
	Org     string // organization slug reported to clients; default "local"
	Version string
	Create  backend.CreateOptions // defaults for new environments
	Log     *log.Logger
	// Domain is the DNS suffix of environment URLs (ADR 0008). Empty means no
	// proxy: the url field falls back to http://<ip>:8080.
	Domain string
	// IdleTimeout is the default idle stop timeout (ADR 0009). 0 disables it.
	IdleTimeout time.Duration
	// Now returns the current time. Tests set it; nil means time.Now.
	Now func() time.Time

	mu       sync.Mutex
	nextID   int
	sessions map[string]*session
	activity map[string]time.Time // last activity per environment
	started  time.Time
}

// New returns a server with sane defaults.
func New(b backend.Backend, token string) *Server {
	return &Server{Backend: b, Token: token, Org: "local", Version: "dev", Log: log.Default(), sessions: map[string]*session{}, activity: map[string]time.Time{}, started: time.Now()}
}

// URLFor returns the environment's URL under the configured domain, or "".
func (s *Server) URLFor(name string) string {
	if s.Domain == "" {
		return ""
	}
	return "https://" + name + "-" + s.Org + "." + s.Domain
}

// Handler returns the HTTP handler, with authentication applied.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/sprites", s.listSprites)
	mux.HandleFunc("POST /v1/sprites", s.createSprite)
	mux.HandleFunc("GET /v1/sprites/{name}", s.getSprite)
	mux.HandleFunc("PUT /v1/sprites/{name}", s.updateSprite)
	mux.HandleFunc("DELETE /v1/sprites/{name}", s.deleteSprite)

	mux.HandleFunc("POST /v1/sprites/{name}/checkpoint", s.createCheckpoint)
	mux.HandleFunc("GET /v1/sprites/{name}/checkpoints", s.listCheckpoints)
	mux.HandleFunc("GET /v1/sprites/{name}/checkpoints/{id}", s.getCheckpoint)
	mux.HandleFunc("POST /v1/sprites/{name}/checkpoints/{id}/restore", s.restoreCheckpoint)

	mux.HandleFunc("GET /v1/sprites/{name}/exec", s.execGet) // WebSocket upgrade or session list
	mux.HandleFunc("POST /v1/sprites/{name}/exec", s.execPost)
	mux.HandleFunc("POST /v1/sprites/{name}/exec/{id}/kill", s.killSession)

	mux.HandleFunc("GET /v1/sprites/{name}/fs/read", s.fsRead)
	mux.HandleFunc("PUT /v1/sprites/{name}/fs/write", s.fsWrite)
	mux.HandleFunc("GET /v1/sprites/{name}/fs/list", s.fsList)
	mux.HandleFunc("POST /v1/sprites/{name}/fs/chown", s.fsChown)

	s.pookaRoutes(mux)

	mux.HandleFunc("/v1/", s.notImplemented)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "version": s.Version})
	})
	return s.auth(s.recordActivity(mux))
}

func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if got == "" {
			got = r.URL.Query().Get("token")
		}
		if s.Token == "" || subtle.ConstantTimeCompare([]byte(got), []byte(s.Token)) != 1 {
			writeError(w, http.StatusUnauthorized, "invalid or missing bearer token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) notImplemented(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusNotImplemented, fmt.Sprintf("%s %s is not implemented by pooka yet", r.Method, r.URL.Path))
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// writeBackendError maps backend errors to HTTP codes.
func writeBackendError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, backend.ErrNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, backend.ErrExists):
		writeError(w, http.StatusConflict, err.Error())
	case strings.Contains(err.Error(), "invalid name"):
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}

// streamEvent is one ndjson line for long operations (checkpoint, restore).
type streamEvent struct {
	Type  string `json:"type"`
	Data  string `json:"data,omitempty"`
	Error string `json:"error,omitempty"`
	Time  string `json:"time"`
}

// streamer writes ndjson events and flushes after each.
type streamer struct {
	w http.ResponseWriter
	f http.Flusher
}

func newStreamer(w http.ResponseWriter) *streamer {
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	f, _ := w.(http.Flusher)
	return &streamer{w: w, f: f}
}

func (st *streamer) send(ev streamEvent) {
	ev.Time = time.Now().UTC().Format(time.RFC3339)
	_ = json.NewEncoder(st.w).Encode(ev)
	if st.f != nil {
		st.f.Flush()
	}
}

func (st *streamer) info(msg string)     { st.send(streamEvent{Type: "info", Data: msg}) }
func (st *streamer) complete(msg string) { st.send(streamEvent{Type: "complete", Data: msg}) }
func (st *streamer) fail(err error)      { st.send(streamEvent{Type: "error", Error: err.Error()}) }

// runStream runs fn while sending keepalives, then reports the outcome.
func (st *streamer) runStream(ctx context.Context, start string, fn func() (string, error)) {
	st.info(start)
	done := make(chan struct{})
	var msg string
	var err error
	go func() {
		msg, err = fn()
		close(done)
	}()
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-done:
			if err != nil {
				st.fail(err)
			} else {
				st.complete(msg)
			}
			return
		case <-tick.C:
			st.send(streamEvent{Type: "keepalive"})
		case <-ctx.Done():
			return
		}
	}
}
