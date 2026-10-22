package cli

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/PiForgeZA/pooka/internal/backend"
)

// Phase 3 commands: url and idle (SPEC section 18).

func (f *fakeBackend) Update(_ context.Context, name string, s backend.Settings) error {
	idle := "nil"
	if s.IdleTimeout != nil {
		idle = s.IdleTimeout.String()
	}
	f.rec(fmt.Sprintf("update %s auth=%s idle=%s", name, s.URLAuth, idle))
	for i := range f.envs {
		if f.envs[i].Name == name {
			if s.URLAuth != "" {
				f.envs[i].URLAuth = s.URLAuth
			}
			if s.IdleTimeout != nil {
				d := *s.IdleTimeout
				f.envs[i].IdleTimeout = &d
			}
			return nil
		}
	}
	return backend.ErrNotFound
}

func urlEnvs() []backend.Env {
	return []backend.Env{
		{Name: "dev1", VMID: 9100, Status: "running", IP: "10.0.0.5", URL: "https://dev1-local.192-168-68-200.sslip.io", Settings: backend.Settings{URLAuth: "sprite"}},
		{Name: "pub", VMID: 9101, Status: "stopped", URL: "https://pub-local.192-168-68-200.sslip.io", Settings: backend.Settings{URLAuth: "public"}},
		{Name: "nourl", VMID: 9102, Status: "running"},
	}
}

func TestURLCommand(t *testing.T) {
	fb := &fakeBackend{envs: urlEnvs()}
	a, out, _ := newApp(fb, "", "")
	a.Cfg.Token = "tok-1"
	if code := a.Execute(context.Background(), []string{"url", "dev1"}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if got := strings.TrimSpace(out.String()); got != "https://dev1-local.192-168-68-200.sslip.io/?token=tok-1" {
		t.Errorf("sprite url: %q", got)
	}

	a, out, _ = newApp(fb, "", "")
	var opened string
	a.OpenURL = func(u string) error { opened = u; return nil }
	if code := a.Execute(context.Background(), []string{"url", "pub", "--open"}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if got := strings.TrimSpace(out.String()); got != "https://pub-local.192-168-68-200.sslip.io" || opened != got {
		t.Errorf("public url: %q opened %q", got, opened)
	}

	a, _, errb := newApp(fb, "", "")
	if code := a.Execute(context.Background(), []string{"url", "nourl"}); code != 1 || !strings.Contains(errb.String(), "pookad.yaml") {
		t.Errorf("no url: exit %d %s", code, errb.String())
	}

	// --auth sets the setting first, then prints.
	a, out, _ = newApp(fb, "", "")
	fb.calls = nil
	if code := a.Execute(context.Background(), []string{"url", "dev1", "--auth", "public"}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if len(fb.calls) != 1 || fb.calls[0] != "update dev1 auth=public idle=nil" {
		t.Errorf("calls: %v", fb.calls)
	}
	if got := strings.TrimSpace(out.String()); strings.Contains(got, "token=") {
		t.Errorf("public url must not carry the token: %q", got)
	}
	a, _, _ = newApp(fb, "", "")
	if code := a.Execute(context.Background(), []string{"url", "dev1", "--auth", "nope"}); code != 1 {
		t.Errorf("invalid auth: exit %d", code)
	}
}

func TestIdleCommand(t *testing.T) {
	fb := &fakeBackend{envs: urlEnvs()}
	cases := []struct {
		args   []string
		active string
		call   string
		out    string
	}{
		{[]string{"idle", "dev1"}, "", "", "dev1: idle timeout is the daemon default"},
		{[]string{"idle", "dev1", "2h"}, "", "update dev1 auth= idle=2h0m0s", "dev1: stops after 2h0m0s"},
		{[]string{"idle", "off"}, "dev1", "update dev1 auth= idle=0s", "dev1: idle stop is off"},
		{[]string{"idle", "-n", "dev1", "45m"}, "", "update dev1 auth= idle=45m0s", "dev1: stops after 45m0s"},
		{[]string{"idle"}, "dev1", "", "dev1: stops after 45m0s"},
	}
	for _, c := range cases {
		fb.calls = nil
		a, out, errb := newApp(fb, "", c.active)
		if code := a.Execute(context.Background(), c.args); code != 0 {
			t.Fatalf("%v: exit %d %s", c.args, code, errb.String())
		}
		if c.call == "" && len(fb.calls) != 0 || c.call != "" && (len(fb.calls) != 1 || fb.calls[0] != c.call) {
			t.Errorf("%v: calls %v, want %q", c.args, fb.calls, c.call)
		}
		if !strings.Contains(out.String(), c.out) {
			t.Errorf("%v: out %q, want %q", c.args, out.String(), c.out)
		}
	}
	a, _, _ := newApp(fb, "", "")
	if code := a.Execute(context.Background(), []string{"idle", "dev1", "soon"}); code != 1 {
		t.Errorf("bad duration: exit %d", code)
	}
	a, _, _ = newApp(fb, "", "")
	if code := a.Execute(context.Background(), []string{"idle", "5m"}); code != 1 {
		t.Errorf("value without an environment: exit %d", code)
	}
	fb.calls = nil
	a, _, _ = newApp(fb, "", "")
	if code := a.Execute(context.Background(), []string{"idle", "-n", "dev1", "dev1", "5m"}); code != 1 || len(fb.calls) != 0 {
		t.Errorf("-n with two positionals must fail, not silently skip: exit %d calls %v", code, fb.calls)
	}
}

func TestCreateIdleTimeoutFlag(t *testing.T) {
	fb := &fakeBackend{}
	a, _, _ := newApp(fb, "", "")
	if code := a.Execute(context.Background(), []string{"create", "dev9", "--idle-timeout", "off"}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if fb.lastCreate.IdleTimeout == nil || *fb.lastCreate.IdleTimeout != 0 {
		t.Errorf("idle timeout not passed: %+v", fb.lastCreate)
	}
	a, _, _ = newApp(fb, "", "")
	if code := a.Execute(context.Background(), []string{"create", "dev9", "--idle-timeout", "90m"}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if fb.lastCreate.IdleTimeout == nil || *fb.lastCreate.IdleTimeout != 90*time.Minute {
		t.Errorf("idle timeout not passed: %+v", fb.lastCreate)
	}
	a, _, _ = newApp(fb, "", "")
	if code := a.Execute(context.Background(), []string{"create", "dev9", "--idle-timeout", "later"}); code != 1 {
		t.Errorf("bad value: exit %d", code)
	}
}
