package daemon

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/PiForgeZA/pooka/internal/backend"
)

// spriteResponse mirrors the Sprites API SpriteResponse schema.
type spriteResponse struct {
	ID            string       `json:"id"`
	Name          string       `json:"name"`
	Organization  string       `json:"organization"`
	URL           string       `json:"url"`
	Status        string       `json:"status"`
	CreatedAt     string       `json:"created_at"`
	UpdatedAt     string       `json:"updated_at"`
	LastRunningAt *string      `json:"last_running_at"`
	URLSettings   *urlSettings `json:"url_settings,omitempty"`
	Labels        []string     `json:"labels,omitempty"`
}

type urlSettings struct {
	Auth string `json:"auth"`
}

type spriteListResponse struct {
	Sprites []spriteResponse `json:"sprites"`
	HasMore bool             `json:"has_more"`
	Running int              `json:"running"`
	Warm    int              `json:"warm"`
	Cold    int              `json:"cold"`
	Name    string           `json:"name"`
}

// toSprite maps a backend Env to the Sprites shape. Stopped environments report
// "cold": pooka has no warm state in phase 2.
func (s *Server) toSprite(e backend.Env) spriteResponse {
	status := "cold"
	url := ""
	if e.Status == "running" {
		status = "running"
		if e.IP != "" {
			url = "http://" + e.IP + ":8080"
		}
	}
	created := ""
	if !e.Created.IsZero() {
		created = e.Created.UTC().Format(time.RFC3339)
	}
	return spriteResponse{
		ID:           fmt.Sprintf("sprite-%d", e.VMID),
		Name:         e.Name,
		Organization: s.Org,
		URL:          url,
		Status:       status,
		CreatedAt:    created,
		UpdatedAt:    created,
		URLSettings:  &urlSettings{Auth: "sprite"},
	}
}

func (s *Server) listSprites(w http.ResponseWriter, r *http.Request) {
	envs, err := s.Backend.List(r.Context())
	if err != nil {
		writeBackendError(w, err)
		return
	}
	prefix := r.URL.Query().Get("prefix")
	resp := spriteListResponse{Sprites: []spriteResponse{}, Name: s.Org}
	for _, e := range envs {
		if prefix != "" && !strings.HasPrefix(e.Name, prefix) {
			continue
		}
		sp := s.toSprite(e)
		if sp.Status == "running" {
			resp.Running++
		} else {
			resp.Cold++
		}
		resp.Sprites = append(resp.Sprites, sp)
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) createSprite(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	env, err := s.Backend.Create(r.Context(), req.Name, s.Create)
	if err != nil {
		writeBackendError(w, err)
		return
	}
	if env.Created.IsZero() {
		env.Created = time.Now()
	}
	writeJSON(w, http.StatusCreated, s.toSprite(env))
}

func (s *Server) getSprite(w http.ResponseWriter, r *http.Request) {
	env, err := s.Backend.Get(r.Context(), r.PathValue("name"))
	if err != nil {
		writeBackendError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.toSprite(env))
}

// updateSprite accepts URL settings and ignores them: phase 2 has no URLs.
func (s *Server) updateSprite(w http.ResponseWriter, r *http.Request) {
	env, err := s.Backend.Get(r.Context(), r.PathValue("name"))
	if err != nil {
		writeBackendError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.toSprite(env))
}

func (s *Server) deleteSprite(w http.ResponseWriter, r *http.Request) {
	if err := s.Backend.Destroy(r.Context(), r.PathValue("name")); err != nil {
		writeBackendError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
