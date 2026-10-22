package daemon

import (
	"context"
	"encoding/json"
	"log"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/PiForgeZA/pooka/internal/backend"
	"github.com/PiForgeZA/pooka/internal/backend/backendtest"
)

// Phase 3: URLs, url auth and idle stop (ADR 0008, 0009).

func TestSpriteURLWithDomain(t *testing.T) {
	fb := backendtest.New()
	s := New(fb, "secret")
	s.Domain = "192-168-68-200.sslip.io"
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	_, b := do(t, ts, "GET", "/v1/sprites/dev1", nil, "secret")
	var sp spriteResponse
	json.Unmarshal(b, &sp)
	if sp.URL != "https://dev1-local.192-168-68-200.sslip.io" || sp.URLSettings.Auth != "sprite" || sp.Pooka == nil || sp.Pooka.IP != "10.0.0.5" || sp.Pooka.IdleTimeout != nil {
		t.Errorf("dev1: %+v %+v", sp, sp.Pooka)
	}
	// A stopped environment keeps its URL: a request wakes it.
	_, b = do(t, ts, "GET", "/v1/sprites/dev2", nil, "secret")
	json.Unmarshal(b, &sp)
	if sp.URL != "https://dev2-local.192-168-68-200.sslip.io" || sp.Pooka.IP != "" {
		t.Errorf("dev2: %+v", sp)
	}
}

func TestUpdateSpriteStoresURLAuth(t *testing.T) {
	ts, fb := newTestServer(t)
	resp, b := do(t, ts, "PUT", "/v1/sprites/dev1", strings.NewReader(`{"url_settings":{"auth":"public"}}`), "secret")
	if resp.StatusCode != 200 {
		t.Fatalf("%d %s", resp.StatusCode, b)
	}
	var sp spriteResponse
	json.Unmarshal(b, &sp)
	if sp.URLSettings.Auth != "public" || fb.Envs["dev1"].URLAuth != "public" {
		t.Errorf("auth not stored: %+v", sp.URLSettings)
	}
	if resp, _ := do(t, ts, "PUT", "/v1/sprites/dev1", strings.NewReader(`{"url_settings":{"auth":"nope"}}`), "secret"); resp.StatusCode != 400 {
		t.Errorf("invalid auth: %d", resp.StatusCode)
	}
	// An empty body is the official CLI's no-op update.
	if resp, _ := do(t, ts, "PUT", "/v1/sprites/dev1", nil, "secret"); resp.StatusCode != 200 {
		t.Errorf("empty body: %d", resp.StatusCode)
	}
}

func TestSettingsExtension(t *testing.T) {
	ts, fb := newTestServer(t)
	resp, b := do(t, ts, "PUT", "/pooka/v1/sprites/dev1/settings", strings.NewReader(`{"idle_timeout":"0s"}`), "secret")
	if resp.StatusCode != 200 {
		t.Fatalf("%d %s", resp.StatusCode, b)
	}
	var sp spriteResponse
	json.Unmarshal(b, &sp)
	if sp.Pooka == nil || sp.Pooka.IdleTimeout == nil || *sp.Pooka.IdleTimeout != "0s" {
		t.Errorf("idle_timeout: %+v", sp.Pooka)
	}
	if e := fb.Envs["dev1"]; e.IdleTimeout == nil || *e.IdleTimeout != 0 {
		t.Errorf("not stored: %+v", e.Settings)
	}
	if resp, _ := do(t, ts, "PUT", "/pooka/v1/sprites/dev1/settings", strings.NewReader(`{"idle_timeout":"-5m"}`), "secret"); resp.StatusCode != 400 {
		t.Errorf("negative: %d", resp.StatusCode)
	}
	if resp, _ := do(t, ts, "PUT", "/pooka/v1/sprites/nope/settings", strings.NewReader(`{"url_auth":"public"}`), "secret"); resp.StatusCode != 404 {
		t.Errorf("missing: %d", resp.StatusCode)
	}
	// create with an idle timeout
	resp, b = do(t, ts, "POST", "/pooka/v1/sprites", strings.NewReader(`{"name":"dev3","idle_timeout":"2h"}`), "secret")
	if resp.StatusCode != 201 {
		t.Fatalf("%d %s", resp.StatusCode, b)
	}
	if fb.Created.IdleTimeout == nil || *fb.Created.IdleTimeout != 2*time.Hour {
		t.Errorf("create idle_timeout: %+v", fb.Created)
	}
}

func newIdleServer(t *testing.T) (*Server, *backendtest.Fake, *time.Time) {
	t.Helper()
	fb := backendtest.New()
	s := New(fb, "secret")
	s.Log = log.New(testWriter{t}, "", 0)
	now := time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return now }
	s.started = now
	s.IdleTimeout = 30 * time.Minute
	return s, fb, &now
}

type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Log(strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

func stops(fb *backendtest.Fake) []string {
	var out []string
	for _, c := range fb.Calls {
		if strings.HasPrefix(c, "stop ") {
			out = append(out, strings.TrimPrefix(c, "stop "))
		}
	}
	return out
}

func TestIdleStopsAfterTimeout(t *testing.T) {
	s, fb, now := newIdleServer(t)
	ctx := context.Background()
	if got := s.idleTick(ctx); len(got) != 0 {
		t.Fatalf("stopped too early: %v", got)
	}
	*now = now.Add(31 * time.Minute)
	if got := s.idleTick(ctx); len(got) != 1 || got[0] != "dev1" {
		t.Fatalf("want dev1 stopped, got %v", got)
	}
	if st := stops(fb); len(st) != 1 || st[0] != "dev1" {
		t.Errorf("backend stops: %v", st)
	}
	if !strings.Contains(strings.Join(fb.Calls, "\n"), "ss -Htn state established") {
		t.Error("connection check not run")
	}
}

func TestIdleActivityResetsClock(t *testing.T) {
	s, _, now := newIdleServer(t)
	*now = now.Add(20 * time.Minute)
	s.Touch("dev1")
	*now = now.Add(20 * time.Minute) // 40 min since start, 20 since touch
	if got := s.idleTick(context.Background()); len(got) != 0 {
		t.Errorf("stopped despite activity: %v", got)
	}
	*now = now.Add(11 * time.Minute)
	if got := s.idleTick(context.Background()); len(got) != 1 {
		t.Errorf("want a stop, got %v", got)
	}
}

func TestIdleKeepsEnvWithConnections(t *testing.T) {
	s, fb, now := newIdleServer(t)
	fb.Established["dev1"] = true
	*now = now.Add(time.Hour)
	if got := s.idleTick(context.Background()); len(got) != 0 {
		t.Errorf("stopped an environment with an SSH session: %v", got)
	}
	// The check counted as activity: a later tick without connections waits
	// a full timeout again.
	fb.Established["dev1"] = false
	*now = now.Add(10 * time.Minute)
	if got := s.idleTick(context.Background()); len(got) != 0 {
		t.Errorf("stopped before a full timeout after the last connection: %v", got)
	}
	*now = now.Add(21 * time.Minute)
	if got := s.idleTick(context.Background()); len(got) != 1 {
		t.Errorf("want a stop, got %v", got)
	}
}

func TestIdleKeepsEnvWithExecSession(t *testing.T) {
	s, _, now := newIdleServer(t)
	ss := s.addSession("dev1", execParams{cmd: []string{"bash"}}, func() {})
	*now = now.Add(time.Hour)
	if got := s.idleTick(context.Background()); len(got) != 0 {
		t.Errorf("stopped with an open exec session: %v", got)
	}
	s.endSession(ss.ID)
	if got := s.idleTick(context.Background()); len(got) != 0 {
		t.Errorf("stopped right after the session ended: %v", got)
	}
	*now = now.Add(31 * time.Minute)
	if got := s.idleTick(context.Background()); len(got) != 1 {
		t.Errorf("want a stop, got %v", got)
	}
}

func TestIdlePerEnvironmentTimeout(t *testing.T) {
	s, fb, now := newIdleServer(t)
	off := time.Duration(0)
	short := 5 * time.Minute
	e := fb.Envs["dev1"]
	e.IdleTimeout = &off
	fb.Envs["dev1"] = e
	fb.Envs["dev3"] = backend.Env{Name: "dev3", VMID: 9102, Status: "running", IP: "10.0.0.7", Settings: backend.Settings{IdleTimeout: &short}}
	*now = now.Add(6 * time.Minute)
	if got := s.idleTick(context.Background()); len(got) != 1 || got[0] != "dev3" {
		t.Errorf("want only dev3, got %v", got)
	}
	*now = now.Add(time.Hour)
	if got := s.idleTick(context.Background()); len(got) != 0 {
		t.Errorf("dev1 opted out but was stopped: %v", got)
	}
}

func TestIdleDisabledByDefault(t *testing.T) {
	s, _, now := newIdleServer(t)
	s.IdleTimeout = 0
	*now = now.Add(24 * time.Hour)
	if got := s.idleTick(context.Background()); len(got) != 0 {
		t.Errorf("idle_timeout 0 must disable: %v", got)
	}
}
