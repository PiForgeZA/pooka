package daemon

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/PiForgeZA/pooka/internal/backend"
)

// pooka extensions: endpoints the Sprites spec does not define. They live under
// /pooka/v1 so they can never collide with a future Sprites endpoint. See ADR 0005.

// createRequest is the body of POST /pooka/v1/sprites. Zero values mean "use the
// daemon default".
type createRequest struct {
	Name      string `json:"name"`
	Cores     int    `json:"cores"`
	MemoryMB  int    `json:"memory_mb"`
	DiskGB    int    `json:"disk_gb"`
	SSHPubKey string `json:"ssh_pubkey"`
	// IdleTimeout is a Go duration such as "30m" or "0s". Empty means the daemon
	// default (ADR 0009).
	IdleTimeout string `json:"idle_timeout"`
}

// settingsRequest is the body of PUT /pooka/v1/sprites/{name}/settings. Empty
// fields keep their value.
type settingsRequest struct {
	URLAuth     string `json:"url_auth"`
	IdleTimeout string `json:"idle_timeout"`
}

func (s *Server) pookaRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /pooka/v1/sprites", s.createSpriteWithOptions)
	mux.HandleFunc("POST /pooka/v1/sprites/{name}/start", s.startSprite)
	mux.HandleFunc("POST /pooka/v1/sprites/{name}/stop", s.stopSprite)
	mux.HandleFunc("PUT /pooka/v1/sprites/{name}/settings", s.updateSettings)
	mux.HandleFunc("DELETE /pooka/v1/sprites/{name}/checkpoints/{id}", s.deleteCheckpoint)
	// Same exec protocol as /v1, plus root=true to run as root inside the environment.
	mux.HandleFunc("GET /pooka/v1/sprites/{name}/exec", s.execGet)
	mux.HandleFunc("POST /pooka/v1/sprites/{name}/exec", s.execPost)
	mux.HandleFunc("/pooka/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "no such pooka extension: "+r.Method+" "+r.URL.Path)
	})
}

func (s *Server) createSpriteWithOptions(w http.ResponseWriter, r *http.Request) {
	var req createRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	opts := s.Create
	if req.Cores > 0 {
		opts.Cores = req.Cores
	}
	if req.MemoryMB > 0 {
		opts.MemoryMB = req.MemoryMB
	}
	if req.DiskGB > 0 {
		opts.DiskGB = req.DiskGB
	}
	if req.SSHPubKey != "" {
		opts.SSHPubKey = req.SSHPubKey
	}
	if req.IdleTimeout != "" {
		d, err := parseIdle(req.IdleTimeout)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		opts.IdleTimeout = &d
	}
	env, err := s.Backend.Create(r.Context(), req.Name, opts)
	if err != nil {
		writeBackendError(w, err)
		return
	}
	if env.Created.IsZero() {
		env.Created = time.Now()
	}
	writeJSON(w, http.StatusCreated, s.toSprite(env))
}

// parseIdle reads an idle timeout. Negative values are an error.
func parseIdle(v string) (time.Duration, error) {
	d, err := time.ParseDuration(v)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("invalid idle_timeout %q: use a duration such as 30m, or 0 for never", v)
	}
	return d, nil
}

// updateSettings writes url_auth and idle_timeout (ADR 0008, 0009) and returns
// the sprite.
func (s *Server) updateSettings(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var req settingsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	set := backend.Settings{URLAuth: req.URLAuth}
	if err := backend.ValidateURLAuth(req.URLAuth); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.IdleTimeout != "" {
		d, err := parseIdle(req.IdleTimeout)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		set.IdleTimeout = &d
	}
	if err := s.Backend.Update(r.Context(), name, set); err != nil {
		writeBackendError(w, err)
		return
	}
	env, err := s.Backend.Get(r.Context(), name)
	if err != nil {
		writeBackendError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.toSprite(env))
}

func (s *Server) startSprite(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	err := s.Backend.Start(r.Context(), name)
	s.noContent(w, err)
}

func (s *Server) stopSprite(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	err := s.Backend.Stop(r.Context(), name)
	if err == nil {
		s.dropSessions(name)
	}
	s.noContent(w, err)
}

func (s *Server) deleteCheckpoint(w http.ResponseWriter, r *http.Request) {
	s.noContent(w, s.Backend.DeleteCheckpoint(r.Context(), r.PathValue("name"), r.PathValue("id")))
}

// noContent answers 204 on success and maps the error otherwise.
func (s *Server) noContent(w http.ResponseWriter, err error) {
	if err != nil {
		writeBackendError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
