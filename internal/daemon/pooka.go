package daemon

import (
	"encoding/json"
	"net/http"
	"time"
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
}

func (s *Server) pookaRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /pooka/v1/sprites", s.createSpriteWithOptions)
	mux.HandleFunc("POST /pooka/v1/sprites/{name}/start", s.startSprite)
	mux.HandleFunc("POST /pooka/v1/sprites/{name}/stop", s.stopSprite)
	mux.HandleFunc("DELETE /pooka/v1/sprites/{name}/checkpoints/{id}", s.deleteCheckpoint)
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

func (s *Server) startSprite(w http.ResponseWriter, r *http.Request) {
	s.noContent(w, s.Backend.Start(r.Context(), r.PathValue("name")))
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
