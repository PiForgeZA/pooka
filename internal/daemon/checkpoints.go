package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/PiForgeZA/pooka/internal/backend"
)

// checkpointInfo mirrors the Sprites CheckpointInfo schema.
type checkpointInfo struct {
	ID         string `json:"id"`
	CreateTime string `json:"create_time"`
	Comment    string `json:"comment"`
}

func toCheckpoint(cp backend.Checkpoint) checkpointInfo {
	return checkpointInfo{ID: cp.ID, CreateTime: cp.Time.UTC().Format(time.RFC3339), Comment: cp.Comment}
}

func (s *Server) createCheckpoint(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var req struct {
		Comment string `json:"comment"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&req) // empty body is fine
	}
	if _, err := s.Backend.Get(r.Context(), name); err != nil {
		writeBackendError(w, err)
		return
	}
	st := newStreamer(w)
	ctx := context.WithoutCancel(r.Context()) // finish the snapshot even if the client leaves
	s.Touch(name)
	st.runStream(r.Context(), "Creating checkpoint...", func() (string, error) {
		cp, err := s.Backend.Checkpoint(ctx, name, req.Comment)
		if err != nil {
			return "", err
		}
		return "Checkpoint " + cp.ID + " created", nil
	})
}

func (s *Server) listCheckpoints(w http.ResponseWriter, r *http.Request) {
	cps, err := s.Backend.ListCheckpoints(r.Context(), r.PathValue("name"))
	if err != nil {
		writeBackendError(w, err)
		return
	}
	out := make([]checkpointInfo, 0, len(cps))
	for i := len(cps) - 1; i >= 0; i-- { // newest first, as Sprites does
		out = append(out, toCheckpoint(cps[i]))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) getCheckpoint(w http.ResponseWriter, r *http.Request) {
	cps, err := s.Backend.ListCheckpoints(r.Context(), r.PathValue("name"))
	if err != nil {
		writeBackendError(w, err)
		return
	}
	for _, cp := range cps {
		if cp.ID == r.PathValue("id") {
			writeJSON(w, http.StatusOK, toCheckpoint(cp))
			return
		}
	}
	writeError(w, http.StatusNotFound, "checkpoint not found: "+r.PathValue("id"))
}

func (s *Server) restoreCheckpoint(w http.ResponseWriter, r *http.Request) {
	name, id := r.PathValue("name"), r.PathValue("id")
	if _, err := s.Backend.Get(r.Context(), name); err != nil {
		writeBackendError(w, err)
		return
	}
	st := newStreamer(w)
	ctx := context.WithoutCancel(r.Context())
	st.runStream(r.Context(), "Restoring to checkpoint "+id+"...", func() (string, error) {
		if err := s.Backend.Restore(ctx, name, id); err != nil {
			return "", err
		}
		s.dropSessions(name)
		s.Touch(name)
		return "Restored to " + id, nil
	})
}

var errNoSession = errors.New("session not found")
