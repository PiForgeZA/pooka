package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PiForgeZA/pooka/internal/backend"
	"github.com/PiForgeZA/pooka/internal/backend/backendtest"
)

const domain = "192-168-68-200.sslip.io"

// newProxy returns a proxy in front of an in-memory backend whose running
// environment dev1 answers on a local test server. Requests to the proxy set the
// Host header by hand, as a browser would after DNS.
func newProxy(t *testing.T) (*Proxy, *httptest.Server, *backendtest.Fake, *httptest.Server) {
	t.Helper()
	var hits atomic.Int32
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		fmt.Fprintf(w, "app host=%s xff=%s path=%s", r.Host, r.Header.Get("X-Forwarded-For"), r.URL.Path)
	}))
	t.Cleanup(app.Close)
	_, port, _ := net.SplitHostPort(app.Listener.Addr().String())
	n, _ := strconv.Atoi(port)

	fb := backendtest.New()
	for _, name := range []string{"dev1", "dev2"} {
		e := fb.Envs[name]
		e.IP = "127.0.0.1"
		fb.Envs[name] = e
	}
	var touched []string
	var mu sync.Mutex
	p := &Proxy{
		Backend: fb, Org: "local", Domain: domain, Token: "secret", Port: n,
		StartWait: 2 * time.Second, Poll: 10 * time.Millisecond,
		Log:   log.New(io.Discard, "", 0),
		Touch: func(name string) { mu.Lock(); touched = append(touched, name); mu.Unlock() },
		API:   http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "api") }),
	}
	t.Cleanup(func() {
		mu.Lock()
		defer mu.Unlock()
		t.Logf("touched: %v", touched)
	})
	ts := httptest.NewServer(p)
	t.Cleanup(ts.Close)
	return p, ts, fb, app
}

func get(t *testing.T, ts *httptest.Server, host, path string, set func(*http.Request)) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest("GET", ts.URL+path, nil)
	req.Host = host
	if set != nil {
		set(req)
	}
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(b)
}

func bearer(req *http.Request) { req.Header.Set("Authorization", "Bearer secret") }

func TestRoutesByFirstLabel(t *testing.T) {
	_, ts, _, _ := newProxy(t)
	for _, host := range []string{"dev1-local." + domain, "dev1-local.pooka.piforge.internal", "DEV1-local.anything:443"} {
		resp, body := get(t, ts, host, "/hello?x=1", bearer)
		if resp.StatusCode != 200 || !strings.Contains(body, "path=/hello") {
			t.Errorf("%s: %d %s", host, resp.StatusCode, body)
		}
		if !strings.Contains(body, "host="+strings.ToLower(strings.TrimSuffix(host, ":443"))) && !strings.Contains(body, "host="+host) {
			t.Errorf("%s: Host not passed through: %s", host, body)
		}
		if !strings.Contains(body, "xff=127.0.0.1") {
			t.Errorf("%s: no X-Forwarded-For: %s", host, body)
		}
	}
	if resp, _ := get(t, ts, "dev1-other."+domain, "/", bearer); resp.StatusCode != 404 {
		t.Errorf("wrong org: %d", resp.StatusCode)
	}
	if resp, _ := get(t, ts, "nohyphen."+domain, "/", bearer); resp.StatusCode != 404 {
		t.Errorf("no hyphen: %d", resp.StatusCode)
	}
	if resp, _ := get(t, ts, "ghost-local."+domain, "/", bearer); resp.StatusCode != 404 {
		t.Errorf("unknown env: %d", resp.StatusCode)
	}
	if resp, body := get(t, ts, "api."+domain, "/v1/sprites", nil); resp.StatusCode != 200 || body != "api" {
		t.Errorf("api host: %d %s", resp.StatusCode, body)
	}
}

func TestSplitHost(t *testing.T) {
	cases := map[string][3]any{
		"web-local.x.y":      {"web", "local", true},
		"my-app-local.x":     {"my-app", "local", true},
		"web-local":          {"web", "local", true},
		"web-local.x.y:8443": {"web", "local", true},
		"web.x":              {"", "", false},
		"-local.x":           {"", "", false},
		"web-.x":             {"", "", false},
	}
	for in, want := range cases {
		n, o, ok := SplitHost(in)
		if n != want[0] || o != want[1] || ok != want[2] {
			t.Errorf("%s: got %q %q %v", in, n, o, ok)
		}
	}
}

func TestURLAuth(t *testing.T) {
	_, ts, fb, _ := newProxy(t)
	host := "dev1-local." + domain
	if resp, body := get(t, ts, host, "/", nil); resp.StatusCode != 401 || !strings.Contains(body, "pooka url") {
		t.Errorf("no credentials: %d %s", resp.StatusCode, body)
	}
	if resp, _ := get(t, ts, host, "/", func(r *http.Request) { r.Header.Set("Authorization", "Bearer wrong") }); resp.StatusCode != 401 {
		t.Errorf("wrong bearer: %d", resp.StatusCode)
	}
	if resp, _ := get(t, ts, host, "/?token=wrong", nil); resp.StatusCode != 401 {
		t.Errorf("wrong token param: %d", resp.StatusCode)
	}
	// ?token= sets the cookie and redirects to the clean URL.
	resp, _ := get(t, ts, host, "/dash?token=secret&a=b", nil)
	if resp.StatusCode != 303 || resp.Header.Get("Location") != "/dash?a=b" {
		t.Fatalf("token param: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	var cookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == CookieName {
			cookie = c
		}
	}
	if cookie == nil || !cookie.HttpOnly || !cookie.Secure || cookie.Value != CookieValue("secret", host) || cookie.Value == "secret" {
		t.Fatalf("cookie: %+v", cookie)
	}
	if resp, _ := get(t, ts, host, "/", func(r *http.Request) { r.AddCookie(cookie) }); resp.StatusCode != 200 {
		t.Errorf("cookie: %d", resp.StatusCode)
	}
	// The cookie is bound to the host: a second environment rejects it.
	if resp, _ := get(t, ts, "dev2-local."+domain, "/", func(r *http.Request) { r.AddCookie(cookie) }); resp.StatusCode != 401 {
		t.Errorf("cookie reused on another host: %d", resp.StatusCode)
	}
	// public skips the check.
	e := fb.Envs["dev1"]
	e.URLAuth = "public"
	fb.Envs["dev1"] = e
	time.Sleep(5 * time.Millisecond)
	p := ts.Config.Handler.(*Proxy)
	p.CacheTTL = time.Nanosecond
	if resp, _ := get(t, ts, host, "/", nil); resp.StatusCode != 200 {
		t.Errorf("public: %d", resp.StatusCode)
	}
}

func TestWakeOnRequest(t *testing.T) {
	_, ts, fb, _ := newProxy(t)
	resp, body := get(t, ts, "dev2-local."+domain, "/", bearer)
	if resp.StatusCode != 200 || !strings.Contains(body, "app") {
		t.Fatalf("wake: %d %s", resp.StatusCode, body)
	}
	starts := 0
	for _, c := range fb.Calls {
		if c == "start dev2" {
			starts++
		}
	}
	if starts != 1 || fb.Envs["dev2"].Status != "running" {
		t.Errorf("starts=%d status=%s", starts, fb.Envs["dev2"].Status)
	}
}

func TestConcurrentWakesShareOneStart(t *testing.T) {
	_, ts, fb, _ := newProxy(t)
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if resp, _ := get(t, ts, "dev2-local."+domain, "/", bearer); resp.StatusCode != 200 {
				t.Errorf("wake: %d", resp.StatusCode)
			}
		}()
	}
	wg.Wait()
	starts := 0
	for _, c := range fb.Calls {
		if c == "start dev2" {
			starts++
		}
	}
	if starts != 1 {
		t.Errorf("want one start, got %d", starts)
	}
}

func TestNothingListening(t *testing.T) {
	p, ts, fb, app := newProxy(t)
	app.Close()
	start := time.Now()
	if resp, body := get(t, ts, "dev1-local."+domain, "/", bearer); resp.StatusCode != 502 || !strings.Contains(body, "nothing listens") {
		t.Errorf("running, closed port: %d %s", resp.StatusCode, body)
	}
	if time.Since(start) > p.StartWait {
		t.Error("a running environment must fail at once, not after StartWait")
	}
	// A woken environment gets the full wait, then 502.
	p.StartWait = 100 * time.Millisecond
	if resp, body := get(t, ts, "dev2-local."+domain, "/", bearer); resp.StatusCode != 502 || !strings.Contains(body, "did not answer") {
		t.Errorf("woken, closed port: %d %s", resp.StatusCode, body)
	}
	if fb.Envs["dev2"].Status != "running" {
		t.Error("the environment stays started even when the port never answered")
	}
}

func TestLookupCache(t *testing.T) {
	p, ts, fb, _ := newProxy(t)
	now := time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC)
	p.Now = func() time.Time { return now }
	get(t, ts, "dev1-local."+domain, "/", bearer)
	delete(fb.Envs, "dev1")
	if resp, _ := get(t, ts, "dev1-local."+domain, "/", bearer); resp.StatusCode != 200 {
		t.Errorf("within the TTL the cached lookup serves: %d", resp.StatusCode)
	}
	now = now.Add(3 * time.Second)
	if resp, _ := get(t, ts, "dev1-local."+domain, "/", bearer); resp.StatusCode != 404 {
		t.Errorf("after the TTL the backend is asked: %d", resp.StatusCode)
	}
}

func TestUnknownNameIsCachedAndRunningWithoutIPFailsFast(t *testing.T) {
	p, ts, fb, _ := newProxy(t)
	gets := func() int { return fb.Gets["ghost"] }
	for i := 0; i < 3; i++ {
		if resp, _ := get(t, ts, "ghost-local."+domain, "/", bearer); resp.StatusCode != 404 {
			t.Errorf("unknown: %d", resp.StatusCode)
		}
	}
	if gets() != 1 {
		t.Errorf("want one backend lookup for a repeated unknown name, got %d", gets())
	}
	e := fb.Envs["dev1"]
	e.IP = ""
	fb.Envs["dev1"] = e
	p.CacheTTL = time.Nanosecond
	start := time.Now()
	resp, body := get(t, ts, "dev1-local."+domain, "/", bearer)
	if resp.StatusCode != 502 || !strings.Contains(body, "no IP address") {
		t.Errorf("running without IP: %d %s", resp.StatusCode, body)
	}
	if time.Since(start) > time.Second {
		t.Error("a running environment without an IP must fail at once")
	}
}

func TestWakeRaceWithExternalStart(t *testing.T) {
	_, ts, fb, _ := newProxy(t)
	fb.StartErr = map[string]error{"dev2": errors.New("CT 9101 already running")}
	e := fb.Envs["dev2"]
	e.Status = "running" // started by someone else after the proxy's lookup
	fb.Envs["dev2"] = e
	// Prime the cache with "stopped" so the proxy tries a start.
	p := ts.Config.Handler.(*Proxy)
	p.init()
	p.mu.Lock()
	p.cache["dev2"] = cached{env: backend.Env{Name: "dev2", Status: "stopped", IP: "127.0.0.1"}, at: p.Now()}
	p.mu.Unlock()
	if resp, body := get(t, ts, "dev2-local."+domain, "/", bearer); resp.StatusCode != 200 {
		t.Errorf("already running must serve, got %d %s", resp.StatusCode, body)
	}
}

func TestContextCancelDuringWake(t *testing.T) {
	p, _, fb, _ := newProxy(t)
	p.init()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	env := fb.Envs["dev2"]
	env.IP = "" // never gets one
	fb.Envs["dev2"] = env
	if _, err := p.ready(ctx, env); err == nil {
		t.Error("want an error when the request context ends")
	}
	if _, err := p.Backend.Get(context.Background(), "dev2"); err != nil {
		t.Error(err)
	}
	_ = backend.ErrNotFound
}
