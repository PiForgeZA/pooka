package daemon

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/PiForgeZA/pooka/internal/backend"
)

// The idle loop. See ADR 0009 and SPEC section 18.5. activity.go says what
// counts as activity; this file is the only reader of the record.
//
// The daemon keeps the last activity time in memory. A daemon start counts as
// activity for every environment, so a restart never stops anything before one
// full timeout has passed. A create counts too: a fresh environment has its
// creation time as a floor.

// lastActivity returns the latest of the recorded activity, the daemon start
// and the environment's creation.
func (s *Server) lastActivity(e backend.Env) time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.started
	if a, ok := s.activity[e.Name]; ok && a.After(t) {
		t = a
	}
	if e.Created.After(t) {
		t = e.Created
	}
	return t
}

// hasSessions reports whether an exec session is open on the environment.
func (s *Server) hasSessions(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ss := range s.sessions {
		if ss.Env == name {
			return true
		}
	}
	return false
}

func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// RunIdle runs idleTick every interval until ctx ends. A zero IdleTimeout with
// no per-environment timeouts makes every tick a no-op.
func (s *Server) RunIdle(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.idleTick(ctx)
		}
	}
}

// idleTick stops every running environment that is past its idle timeout and has
// no open exec session and no established TCP connection inside. It returns the
// names it stopped.
func (s *Server) idleTick(ctx context.Context) []string {
	envs, err := s.Backend.List(ctx)
	if err != nil {
		s.Log.Printf("idle: list: %v", err)
		return nil
	}
	var stopped []string
	now := s.now()
	for _, e := range envs {
		if e.Status != "running" {
			continue
		}
		timeout := s.IdleTimeout
		if e.IdleTimeout != nil {
			timeout = *e.IdleTimeout
		}
		if timeout <= 0 {
			continue
		}
		idle := now.Sub(s.lastActivity(e))
		if idle < timeout || s.hasSessions(e.Name) {
			continue
		}
		busy, err := s.established(ctx, e.Name)
		if err != nil {
			s.Log.Printf("idle: %s: connection check failed, not stopping: %v", e.Name, err)
			continue
		}
		if busy {
			s.Touch(e.Name)
			continue
		}
		if err := s.Backend.Stop(ctx, e.Name); err != nil {
			s.Log.Printf("idle: stop %s: %v", e.Name, err)
			continue
		}
		s.dropSessions(e.Name)
		s.Log.Printf("idle: stopped %s after %s without activity", e.Name, idle.Round(time.Second))
		stopped = append(stopped, e.Name)
	}
	return stopped
}

// established reports whether any TCP connection is established inside the
// environment: an SSH login, an IDE backend, an open WebSocket.
func (s *Server) established(ctx context.Context, name string) (bool, error) {
	var out, errb bytes.Buffer
	code, err := s.Backend.Exec(ctx, name, []string{"ss", "-Htn", "state", "established"}, backend.ExecOptions{Stdout: &out, Stderr: &errb, Root: true})
	if err != nil {
		return false, err
	}
	if code != 0 {
		return false, fmt.Errorf("ss exited %d: %s", code, strings.TrimSpace(errb.String()))
	}
	return strings.TrimSpace(out.String()) != "", nil
}
