package httpapi

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/PiForgeZA/pooka/internal/backend"
	"github.com/PiForgeZA/pooka/internal/backend/backendtest"
	"github.com/PiForgeZA/pooka/internal/daemon"
)

// Phase 3: URL, url auth and idle timeout travel through the daemon (ADR 0008, 0009).

func TestSettingsRoundTrip(t *testing.T) {
	fb := backendtest.New()
	s := daemon.New(fb, "secret")
	s.Domain = "192-168-68-200.sslip.io"
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	c := New(ts.URL, "secret")
	ctx := context.Background()

	e, err := c.Get(ctx, "dev1")
	if err != nil {
		t.Fatal(err)
	}
	if e.URL != "https://dev1-local.192-168-68-200.sslip.io" || e.IP != "10.0.0.5" || e.URLAuth != "sprite" || e.IdleTimeout != nil {
		t.Errorf("dev1: %+v", e)
	}
	d := 45 * time.Minute
	if err := c.Update(ctx, "dev1", backend.Settings{URLAuth: "public", IdleTimeout: &d}); err != nil {
		t.Fatal(err)
	}
	e, _ = c.Get(ctx, "dev1")
	if e.URLAuth != "public" || e.IdleTimeout == nil || *e.IdleTimeout != d {
		t.Errorf("after update: %+v", e.Settings)
	}
	if err := c.Update(ctx, "dev1", backend.Settings{URLAuth: "nope"}); err == nil {
		t.Error("want an error for an invalid auth value")
	}
	if err := c.Update(ctx, "ghost", backend.Settings{URLAuth: "public"}); !errors.Is(err, backend.ErrNotFound) {
		t.Errorf("missing: %v", err)
	}
	off := time.Duration(0)
	if _, err := c.Create(ctx, "dev3", backend.CreateOptions{IdleTimeout: &off}); err != nil {
		t.Fatal(err)
	}
	if fb.Created.IdleTimeout == nil || *fb.Created.IdleTimeout != 0 {
		t.Errorf("create idle_timeout: %+v", fb.Created)
	}
}
