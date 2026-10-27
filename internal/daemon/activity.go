package daemon

import (
	"net/http"
	"time"
)

// Activity policy. This file owns what counts as activity for idle stop (SPEC
// 18.5, ADR 0009). idle.go is the only reader of the record.
//
// Activity is: a proxy request (the proxy calls Touch), an exec session
// (addSession and endSession), a filesystem read or write, a checkpoint create
// or restore, a start, a create (idle.go reads Env.Created) and a daemon start
// (Server.started). Reads of information are not activity: sprite and
// checkpoint listings, settings changes, stop, session kill and checkpoint
// delete do not keep an environment awake.

// activityPatterns lists the routes that are activity for the environment in
// {name}. The patterns must match the registrations in Handler and pookaRoutes
// exactly: the mux sets r.Pattern to the registered string.
var activityPatterns = map[string]bool{
	"GET /v1/sprites/{name}/fs/read":                   true,
	"PUT /v1/sprites/{name}/fs/write":                  true,
	"GET /v1/sprites/{name}/fs/list":                   true,
	"POST /v1/sprites/{name}/fs/chown":                 true,
	"POST /v1/sprites/{name}/checkpoint":               true,
	"POST /v1/sprites/{name}/checkpoints/{id}/restore": true,
	"POST /pooka/v1/sprites/{name}/start":              true,
	"POST /v1/sprites/{name}/exec":                     true, // an HTTP exec; the WebSocket exec on GET counts through its session
	"POST /pooka/v1/sprites/{name}/exec":               true,
}

// recordActivity records activity for every request on an activity route after
// its handler returns, so a long call counts until it ends. A request that
// failed inside the handler still counts: the caller meant to use the
// environment.
func (s *Server) recordActivity(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)
		if activityPatterns[r.Pattern] {
			s.Touch(r.PathValue("name"))
		}
	})
}

// Touch records activity for an environment now. The proxy calls it for every
// request it forwards.
func (s *Server) Touch(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.touchLocked(name)
}

// touchLocked is Touch for callers that hold s.mu.
func (s *Server) touchLocked(name string) {
	if s.activity == nil {
		s.activity = map[string]time.Time{}
	}
	s.activity[name] = s.now()
}
